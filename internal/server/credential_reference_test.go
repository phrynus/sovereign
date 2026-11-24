package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	forgecrypto "github.com/r0si/sovereign/internal/crypto"
	"github.com/r0si/sovereign/internal/db"
	"github.com/r0si/sovereign/internal/server/middleware"
	"github.com/r0si/sovereign/internal/testutil"
)

func referenceTestServer(t *testing.T) (*Server, db.CredentialEntry) {
	t.Helper()
	forgecrypto.InitLootEncryption(testStorageKeyHex)
	s := initV3BeaconServer(t, testutil.SetupTestDB(t), tenantVisibilityMasterHex)
	if err := s.db.Create(&db.Implant{ID: "ref-agent", TenantID: 1, Hostname: "ref-host"}).Error; err != nil {
		t.Fatalf("seed agent: %v", err)
	}
	cred := db.CredentialEntry{
		TenantID: 1, AgentID: "ref-agent", Domain: "CORP", Username: "alice",
		Password: "S3cret!", Type: "cleartext", Source: "manual",
	}
	if err := s.db.Create(&cred).Error; err != nil {
		t.Fatalf("seed credential: %v", err)
	}
	return s, cred
}

func refID(id uint) string { return strconv.FormatUint(uint64(id), 10) }

func TestCredentialReferenceMaterializesAllowedTypes(t *testing.T) {
	s, cred := referenceTestServer(t)
	params := CredentialTaskParams{
		DC: "10.0.0.1", DelayMS: "250", Usernames: "bob\ncarol",
		LogonType: "network", Target: "10.0.0.9", Method: "smb", Command: "whoami",
	}
	ref := credentialTaskReference{ID: cred.ID, Params: encodeCredentialParams(params)}
	cases := map[string][4]string{
		"cred_check":     {"alice|CORP|S3cret!|10.0.0.1", "", "", ""},
		"password_spray": {"S3cret!|CORP|10.0.0.1|250", "", "", "bob\ncarol"},
		"token_make":     {"CORP\\alice", "S3cret!", "network", ""},
		"lateral":        {"smb|10.0.0.9|alice|S3cret!|whoami", "", "", ""},
	}
	for taskType, want := range cases {
		t.Run(taskType, func(t *testing.T) {
			command, shell, path, data, fingerprint, label, err := s.prepareCredentialTask("ref-agent", taskType, ref)
			if err != nil {
				t.Fatalf("resolve: %v", err)
			}
			if command != want[0] || shell != want[1] || path != want[2] || data != want[3] {
				t.Fatalf("wire = %q/%q/%q/%q, want %q/%q/%q/%q", command, shell, path, data, want[0], want[1], want[2], want[3])
			}
			if fingerprint != cred.DedupKey {
				t.Fatalf("fingerprint = %q, want %q", fingerprint, cred.DedupKey)
			}
			if label != "CORP\\alice" {
				t.Fatalf("label = %q", label)
			}
			task, err := s.createTask("ref-agent", taskType, "", "", "", "", 0, 0,
				WithCredentialReference(cred.ID, params))
			if err != nil {
				t.Fatalf("createTask: %v", err)
			}
			projected := taskForOperator(*task)
			if projected.Command != "@cred:"+refID(cred.ID) || projected.Shell != "" {
				t.Fatalf("stored command = %q/%q", projected.Command, projected.Shell)
			}
			wireCommand, wireShell, err := taskWireArgs(*task)
			if err != nil || wireCommand != want[0] || wireShell != want[1] {
				t.Fatalf("wire args = %q/%q err=%v", wireCommand, wireShell, err)
			}
		})
	}
}

func TestCredentialReferenceRejectsInvalidInputs(t *testing.T) {
	s, cred := referenceTestServer(t)
	badPassword := db.CredentialEntry{
		TenantID: 1, AgentID: "ref-agent", Username: "bob", Password: "pa|ss",
		Type: "cleartext", Source: "manual",
	}
	if err := s.db.Create(&badPassword).Error; err != nil {
		t.Fatal(err)
	}
	hashOnly := db.CredentialEntry{TenantID: 1, AgentID: "ref-agent", Username: "hash", Hash: "AAD3B4", Type: "ntlm"}
	if err := s.db.Create(&hashOnly).Error; err != nil {
		t.Fatal(err)
	}
	foreign := db.CredentialEntry{TenantID: 2, AgentID: "ref-agent", Username: "other", Password: "x", Type: "cleartext"}
	if err := s.db.Create(&foreign).Error; err != nil {
		t.Fatal(err)
	}
	expired := cred
	expired.ID = 0
	expired.Username = "expired"
	expired.Password = "expired-pass"
	expired.ExpiryKnown = true
	expired.ExpiresAt = time.Now().Add(-time.Hour)
	if err := s.db.Create(&expired).Error; err != nil {
		t.Fatal(err)
	}
	var before int64
	s.db.Model(&db.Task{}).Count(&before)

	cases := []struct {
		name     string
		taskType string
		id       uint
	}{
		{"missing", "cred_check", 999999},
		{"cross tenant", "cred_check", foreign.ID},
		{"separator", "cred_check", badPassword.ID},
		{"hash only", "cred_check", hashOnly.ID},
		{"expired", "cred_check", expired.ID},
		{"unsupported type", "shell", cred.ID},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := s.createTask("ref-agent", tc.taskType, "", "", "", "", 0, 0,
				WithCredentialReference(tc.id, CredentialTaskParams{})); err == nil {
				t.Fatal("expected rejection")
			}
		})
	}
	if _, err := s.createTask("ref-agent", "cred_check", "user|CORP|pw|", "", "", "", 0, 0,
		WithCredentialReference(cred.ID, CredentialTaskParams{})); err == nil {
		t.Fatal("inline + reference accepted")
	}
	var after int64
	s.db.Model(&db.Task{}).Count(&after)
	if after != before {
		t.Fatalf("task count changed: %d -> %d", before, after)
	}
}

func TestCredentialReferencePublicProjectionRedactsSecrets(t *testing.T) {
	s, cred := referenceTestServer(t)
	task, err := s.createTask("ref-agent", "cred_check", "", "", "", "", 0, 0,
		WithCredentialReference(cred.ID, CredentialTaskParams{DC: "10.0.0.1"}))
	if err != nil {
		t.Fatal(err)
	}
	var rawResolved string
	if err := s.db.Table("tasks").Where("id = ?", task.ID).
		Select("resolved_command").Scan(&rawResolved).Error; err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(rawResolved, "FC2ENC:") {
		t.Fatalf("resolved command at rest = %q", rawResolved)
	}
	projected := taskForOperator(*task)
	if projected.Command != "@cred:"+refID(cred.ID) || projected.Shell != "" || projected.Data != "" {
		t.Fatalf("projection = %+v", projected)
	}
	body, err := json.Marshal(projected)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"S3cret!", rawResolved, "task_key", "CredentialParams", "ResolvedCommand"} {
		if strings.Contains(string(body), secret) {
			t.Fatalf("projection leaked %q: %s", secret, body)
		}
	}
}

func TestCredentialReferenceApprovalRevalidatesSnapshot(t *testing.T) {
	s, cred := referenceTestServer(t)
	s.configMu.Lock()
	s.cfg.Security.RequireApproval = true
	s.configMu.Unlock()
	task, err := s.createTask("ref-agent", "cred_check", "", "", "", "", 0, 0,
		WithCredentialReference(cred.ID, CredentialTaskParams{}))
	if err != nil {
		t.Fatal(err)
	}
	if task.Status != TaskStatusPendingApproval {
		t.Fatalf("status = %s, want pending_approval", task.Status)
	}
	c, w := tenantScopedAdminContext(s, t, "approver", 1)
	c.Params = gin.Params{{Key: "taskId", Value: refID(task.ID)}}
	c.Request, _ = http.NewRequest(http.MethodPost, "/tasks/approve", nil)
	if err := s.db.Delete(&db.CredentialEntry{}, cred.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := validateCredentialSnapshot(s.db, *task); !errors.Is(err, errCredentialSnapshot) {
		t.Fatalf("snapshot err = %v", err)
	}
	s.handleApproveTask(c)
	if w.Code != http.StatusConflict {
		t.Fatalf("approve status = %d, want 409; body=%s", w.Code, w.Body.String())
	}
	var after db.Task
	if err := s.db.First(&after, task.ID).Error; err != nil {
		t.Fatal(err)
	}
	if after.Status != "failed" || after.Error != errCredentialSnapshot.Error() {
		t.Fatalf("task = %+v", after)
	}
	if got := s.agentPendingTasks["ref-agent"]; got != 0 {
		t.Fatalf("pending counter = %d, want 0", got)
	}
	s.failCredentialSnapshot(after)
	if got := s.agentPendingTasks["ref-agent"]; got != 0 {
		t.Fatalf("pending counter after duplicate release = %d", got)
	}
}

func TestCredentialReferenceUsageRecordedOnce(t *testing.T) {
	s, cred := referenceTestServer(t)
	task, err := s.createTask("ref-agent", "cred_check", "", "", "", "", 0, 0,
		WithCredentialReference(cred.ID, CredentialTaskParams{}))
	if err != nil {
		t.Fatal(err)
	}
	task.CreatedBy = "operator"
	for i := 0; i < 3; i++ {
		_ = s.recordCredentialUsageOnce(*task, "verify", "ok", "test")
	}
	var rows []db.CredentialUsage
	if err := s.db.Where("credential_id = ? AND task_id = ?", cred.ID, task.ID).Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("usage rows = %d, want 1", len(rows))
	}
	if rows[0].Action != "verify" || rows[0].Result != "ok" || rows[0].Operator != "operator" {
		t.Fatalf("usage = %+v", rows[0])
	}
	if action, result := credentialUsageForResult("password_spray", "", `{"results":[{"status":"valid"}]}`); action != "spray" || result != "ok" {
		t.Fatalf("mapping = %s/%s", action, result)
	}
}

func TestCredentialReferenceIdempotencyConflict(t *testing.T) {
	s, cred := referenceTestServer(t)
	other := cred
	other.ID = 0
	other.Username = "bob"
	other.Password = "Other1!"
	if err := s.db.Create(&other).Error; err != nil {
		t.Fatal(err)
	}
	opts := []TaskOption{WithIdempotencyKey("ref-key"), WithCredentialReference(cred.ID, CredentialTaskParams{})}
	first, err := s.createTask("ref-agent", "cred_check", "", "", "", "", 0, 0, opts...)
	if err != nil {
		t.Fatal(err)
	}
	again, err := s.createTask("ref-agent", "cred_check", "", "", "", "", 0, 0, opts...)
	if err != nil || again.ID != first.ID {
		t.Fatalf("same reference: task=%v err=%v", again, err)
	}
	_, err = s.createTask("ref-agent", "cred_check", "", "", "", "", 0, 0,
		WithIdempotencyKey("ref-key"), WithCredentialReference(other.ID, CredentialTaskParams{}))
	if !errors.Is(err, errCredentialReferenceConflict) {
		t.Fatalf("different reference err = %v", err)
	}
}

func TestCredentialReferenceScheduledExecutionRechecksPermission(t *testing.T) {
	s, cred := referenceTestServer(t)
	if err := s.db.Create(&db.User{Username: "scheduler", Role: "user", IsActive: true}).Error; err != nil {
		t.Fatal(err)
	}
	row := db.OneShotTask{
		AgentID: "ref-agent", Type: "cred_check", Status: "pending",
		RunAt: time.Now().Add(-time.Minute), CreatedBy: "scheduler",
		CredentialID: &cred.ID, CredentialParams: encodeCredentialParams(CredentialTaskParams{}),
	}
	if err := s.db.Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.db.Create(&db.CustomRole{Name: "noread", Permissions: `[]`}).Error; err != nil {
		t.Fatal(err)
	}
	s.db.Model(&db.User{}).Where("username = ?", "scheduler").Update("role", "noread")
	s.dispatchDueOneShotTasks()
	var scheduled db.OneShotTask
	if err := s.db.First(&scheduled, row.ID).Error; err != nil {
		t.Fatal(err)
	}
	if scheduled.Status != "error" {
		t.Fatalf("scheduled status = %s, want error", scheduled.Status)
	}
	var count int64
	s.db.Model(&db.Task{}).Where("agent_id = ?", "ref-agent").Count(&count)
	if count != 0 {
		t.Fatalf("created %d tasks despite revoked permission", count)
	}
}

func TestCredentialReferenceHasPermissionMatchesRouteGate(t *testing.T) {
	gin.SetMode(gin.TestMode)
	newCtx := func(role string) *gin.Context {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Set("user_role", role)
		return c
	}
	if !middleware.HasPermission(newCtx(db.RoleAdmin), db.PermCredsRead) {
		t.Fatal("admin must have credential read")
	}
	c := newCtx("custom")
	c.Set("role_permissions", map[string]bool{db.PermCredsRead: true})
	if !middleware.HasPermission(c, db.PermCredsRead) {
		t.Fatal("custom role grant ignored")
	}
	c.Set("auth_via_api_key", true)
	c.Set("api_key_scopes", map[string]bool{db.PermCredsRead: true})
	if !middleware.HasPermission(c, db.PermCredsRead) {
		t.Fatal("scoped API key with matching role ignored")
	}
	c.Set("role_permissions", map[string]bool{})
	if middleware.HasPermission(c, db.PermCredsRead) {
		t.Fatal("scoped API key escaped demoted owner")
	}
}
