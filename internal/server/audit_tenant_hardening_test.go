package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	forgecrypto "github.com/r0si/sovereign/internal/crypto"
	"github.com/r0si/sovereign/internal/db"
)

func auditRequestContext(s *Server, t *testing.T, username string, tenantID uint, target string) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	c, w := tenantScopedAdminContext(s, t, username, tenantID)
	c.Request = httptest.NewRequest(http.MethodGet, target, nil)
	return c, w
}

func readAuditVerdict(t *testing.T, s *Server, c *gin.Context, w *httptest.ResponseRecorder) auditChainVerdict {
	t.Helper()
	c = auditContextWithResponse(c, w)
	s.handleVerifyAuditChain(c)
	if w.Code != http.StatusOK {
		t.Fatalf("verify status = %d (%s)", w.Code, w.Body.String())
	}
	var out struct {
		Data auditChainVerdict `json:"data"`
	}
	unmarshalData(t, w.Body.Bytes(), &out.Data)
	return out.Data
}

func auditContextWithResponse(c *gin.Context, w *httptest.ResponseRecorder) *gin.Context {
	bound, _ := gin.CreateTestContext(w)
	bound.Request = c.Request
	bound.Params = c.Params
	for key, value := range c.Keys {
		if name, ok := key.(string); ok {
			bound.Set(name, value)
		}
	}
	return bound
}

func TestAuditOperatorActionsCarryTenant(t *testing.T) {
	s := mustTenantServer(t)
	c1, _ := auditRequestContext(s, t, "audit-tenant-one", 1, "/settings/test")
	c2, _ := auditRequestContext(s, t, "audit-tenant-two", 2, "/settings/test")

	s.LogOperatorAction(c1, OperatorAction{
		Action: "test_action", Resource: "test", TargetID: "target-one",
		Details: "token=operator-secret-marker", RiskLevel: "high",
	})
	s.LogOperatorAction(c2, OperatorAction{
		Action: "test_action", Resource: "test", TargetID: "target-two",
		Details: "ordinary detail", RiskLevel: "low",
	})

	var rows []db.AuditLog
	if err := s.db.Where("action = ?", "operator:test_action").Order("id ASC").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("operator rows = %d, want 2", len(rows))
	}
	for i, want := range []struct {
		user   string
		tenant uint
		target string
	}{{"audit-tenant-one", 1, "target-one"}, {"audit-tenant-two", 2, "target-two"}} {
		row := rows[i]
		if row.User != want.user || row.TenantID != want.tenant || row.AgentID != want.target {
			t.Fatalf("row %d attribution = %+v", i, row)
		}
		if !strings.Contains(row.Details, "target="+want.target) {
			t.Fatalf("row %d lost target= semantics: %q", i, row.Details)
		}
		if row.IP == "" || row.EntryHash == "" {
			t.Fatalf("row %d bypassed shared user/IP/hash construction: %+v", i, row)
		}
		if i > 0 && row.PrevHash != rows[i-1].EntryHash {
			t.Fatalf("row %d did not join the global chain", i)
		}
	}
	if strings.Contains(rows[0].Details, "operator-secret-marker") {
		t.Fatalf("operator secret leaked into audit details: %q", rows[0].Details)
	}
}

func TestAuditAttributionFailureDropsTargetDetails(t *testing.T) {
	s := mustTenantServer(t)
	c := auditCtx("missing-audit-user")
	s.LogOperatorAction(c, OperatorAction{
		Action: "sensitive_action", Resource: "agent", TargetID: "target-secret-marker",
		Details: "password=detail-secret-marker", RiskLevel: "critical",
	})

	var rows []db.AuditLog
	if err := s.db.Order("id ASC").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("audit rows = %d, want only the attribution failure", len(rows))
	}
	row := rows[0]
	if row.Action != "audit_attribution_failure" || row.TenantID != 0 || row.AgentID != "" || row.Success {
		t.Fatalf("attribution failure shape = %+v", row)
	}
	for _, secret := range []string{"sensitive_action", "target-secret-marker", "detail-secret-marker", "password"} {
		if strings.Contains(row.Details+row.Error, secret) {
			t.Fatalf("attribution failure retained dropped target detail %q: %+v", secret, row)
		}
	}
	if row.EntryHash == "" {
		t.Fatal("attribution failure did not enter the hash chain")
	}
}

func TestAuditAttributionFailureFallsBackToStrictUser(t *testing.T) {
	s := mustTenantServer(t)
	c, _ := auditRequestContext(s, t, "strict-audit-fallback", 9, "/settings/test")
	c.Set(tenantIDContextKey, tenantResolution{ok: false})
	s.LogAuditRecord(c, "fallback_probe", "test", "agent", "detail", true, nil)

	var row db.AuditLog
	if err := s.db.Where("action = ?", "fallback_probe").First(&row).Error; err != nil {
		t.Fatal(err)
	}
	if row.TenantID != 9 || row.User != "strict-audit-fallback" {
		t.Fatalf("strict fallback attribution = %+v", row)
	}

	system := auditCtx("system")
	s.LogAuditRecord(system, "system_probe", "test", "", "detail", true, nil)
	var systemRow db.AuditLog
	if err := s.db.Where("action = ?", "system_probe").First(&systemRow).Error; err != nil {
		t.Fatal(err)
	}
	if systemRow.TenantID != 0 || systemRow.User != "system" {
		t.Fatalf("system attribution = %+v", systemRow)
	}
}

func TestAuditAlternatingTenantsKeepOneGlobalChain(t *testing.T) {
	s := newAuditTestServer(t)
	contexts := []*gin.Context{
		mustAuditContext(s, t, "chain-tenant-a", 1),
		mustAuditContext(s, t, "chain-tenant-b", 2),
	}
	for i := range 6 {
		s.LogAuditRecord(contexts[i%2], "alternating_probe", "test", "", fmt.Sprintf("step=%d", i), true, nil)
	}
	var rows []db.AuditLog
	if err := s.db.Where("action = ?", "alternating_probe").Order("id ASC").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 6 {
		t.Fatalf("rows = %d, want 6", len(rows))
	}
	for i := range rows {
		wantTenant := uint(1 + i%2)
		if rows[i].TenantID != wantTenant {
			t.Fatalf("row %d tenant = %d, want %d", i, rows[i].TenantID, wantTenant)
		}
		if i > 0 && rows[i].PrevHash != rows[i-1].EntryHash {
			t.Fatalf("global chain broke between tenants at row %d", i)
		}
	}
	if verdict := s.verifyAuditChain(100); !verdict.OK {
		t.Fatalf("alternating global chain failed verification: %+v", verdict)
	}
}

func mustAuditContext(s *Server, t *testing.T, username string, tenantID uint) *gin.Context {
	t.Helper()
	c, _ := auditRequestContext(s, t, username, tenantID, "/audit/test")
	return c
}

func TestTenantAuditExportDeclaresFilteredScope(t *testing.T) {
	s := mustTenantServer(t)
	s.LogAuditRecord(nil, "global_probe", "test", "", "global", true, nil)
	tenantCtx := mustAuditContext(s, t, "export-tenant-one", 1)
	s.LogAuditRecord(tenantCtx, "tenant_probe", "test", "", "tenant", true, nil)
	otherCtx := mustAuditContext(s, t, "export-tenant-two", 2)
	s.LogAuditRecord(otherCtx, "other_probe", "test", "", "other", true, nil)

	call := func(c *gin.Context, query string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		c.Request = httptest.NewRequest(http.MethodGet, "/audit/export"+query, nil)
		c = auditContextWithResponse(c, w)
		s.handleExportAuditChain(c)
		return w
	}
	tenantExport := call(tenantCtx, "")
	if tenantExport.Code != http.StatusOK {
		t.Fatalf("tenant export = %d (%s)", tenantExport.Code, tenantExport.Body.String())
	}
	if tenantExport.Header().Get("X-Audit-Export-Filtered") != "true" ||
		tenantExport.Header().Get("X-Audit-Export-Scope") != "tenant=1" ||
		!strings.Contains(tenantExport.Header().Get("Warning"), "not a contiguous hash chain") {
		t.Fatalf("tenant export headers = %v", tenantExport.Header())
	}
	if strings.Contains(tenantExport.Body.String(), "other_probe") {
		t.Fatal("tenant export included another tenant's row")
	}

	globalCtx := mustAuditContext(s, t, "export-global", 0)
	globalExport := call(globalCtx, "")
	if globalExport.Header().Get("X-Audit-Export-Filtered") != "false" ||
		globalExport.Header().Get("X-Audit-Export-Scope") != "all" ||
		globalExport.Header().Get("Warning") != "" {
		t.Fatalf("unfiltered global export headers = %v", globalExport.Header())
	}

	jsonExport := call(tenantCtx, "?format=json")
	var body struct {
		Filtered        bool   `json:"filtered"`
		Scope           string `json:"scope"`
		ChainContiguous bool   `json:"chain_contiguous"`
		Warning         string `json:"warning"`
	}
	if err := json.Unmarshal(jsonExport.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if !body.Filtered || body.Scope != "tenant=1" || body.ChainContiguous || !strings.Contains(body.Warning, "not a contiguous hash chain") {
		t.Fatalf("tenant JSON envelope = %+v", body)
	}
}

func TestAuditAnchorSeparatesGlobalAndTenantViews(t *testing.T) {
	s := anchorTestServer(t)
	s.auditSigningSeed = auditTestSeed
	tenantCtx := mustAuditContext(s, t, "anchor-tenant-one", 1)
	otherCtx := mustAuditContext(s, t, "anchor-tenant-two", 2)
	s.LogAuditRecord(tenantCtx, "anchor_probe", "test", "", "tenant-one-a", true, nil)
	s.LogAuditRecord(otherCtx, "anchor_probe", "test", "", "tenant-two-a", true, nil)
	s.LogAuditRecord(tenantCtx, "anchor_probe", "test", "", "tenant-one-b", true, nil)
	s.LogAuditRecord(otherCtx, "anchor_probe", "test", "", "tenant-two-b", true, nil)

	var tenantLatest, globalLatest db.AuditLog
	if err := s.db.Where("tenant_id = ?", 1).Order("id DESC").First(&tenantLatest).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.db.Order("id DESC").First(&globalLatest).Error; err != nil {
		t.Fatal(err)
	}
	call := func(c *gin.Context) auditChainHead {
		w := httptest.NewRecorder()
		c.Request = httptest.NewRequest(http.MethodGet, "/audit/anchor", nil)
		c = auditContextWithResponse(c, w)
		s.handleAuditChainAnchor(c)
		if w.Code != http.StatusOK {
			t.Fatalf("anchor status = %d (%s)", w.Code, w.Body.String())
		}
		var head auditChainHead
		unmarshalData(t, w.Body.Bytes(), &head)
		return head
	}
	tenantHead := call(tenantCtx)
	if tenantHead.ID != tenantLatest.ID || tenantHead.EntryHash != tenantLatest.EntryHash ||
		tenantHead.Scope != "tenant" || tenantHead.AnchorApplicable || tenantHead.AnchorRecorded ||
		tenantHead.AnchorID != 0 || tenantHead.AnchorTimestamp != "" || tenantHead.AnchorSignature != "" ||
		!tenantHead.EntryHashValid || !tenantHead.SignatureValid {
		t.Fatalf("tenant anchor leaked or misstated the global view: %+v", tenantHead)
	}
	globalCtx := mustAuditContext(s, t, "anchor-global", 0)
	globalHead := call(globalCtx)
	if globalHead.ID != globalLatest.ID || globalHead.EntryHash != globalLatest.EntryHash ||
		globalHead.Scope != "global" || !globalHead.AnchorApplicable || !globalHead.AnchorRecorded ||
		!globalHead.AnchorMatches || globalHead.AnchorSignature == "" {
		t.Fatalf("global anchor lost its continuity proof: %+v", globalHead)
	}
}

func TestAuditVerifyScopedTamperDoesNotCrossTenants(t *testing.T) {
	s := anchorTestServer(t)
	s.auditSigningSeed = auditTestSeed
	tenantCtx := mustAuditContext(s, t, "verify-tenant-one", 1)
	otherCtx := mustAuditContext(s, t, "verify-tenant-two", 2)
	s.LogAuditRecord(tenantCtx, "verify_probe", "test", "", "tenant-one-a", true, nil)
	s.LogAuditRecord(otherCtx, "verify_probe", "test", "", "tenant-two-a", true, nil)
	s.LogAuditRecord(tenantCtx, "verify_probe", "test", "", "tenant-one-b", true, nil)

	var otherRow, tenantLatest db.AuditLog
	if err := s.db.Where("tenant_id = ?", 2).First(&otherRow).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.db.Where("tenant_id = ?", 1).Order("id DESC").First(&tenantLatest).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.db.Model(&db.AuditLog{}).Where("id = ?", otherRow.ID).Update("details", "other tenant tampered").Error; err != nil {
		t.Fatal(err)
	}
	tenantVerdict := readAuditVerdict(t, s, tenantCtx, httptest.NewRecorder())
	if !tenantVerdict.OK || tenantVerdict.Scope != "tenant" || tenantVerdict.AnchorApplicable == nil || *tenantVerdict.AnchorApplicable ||
		tenantVerdict.EntryHashValid == nil || !*tenantVerdict.EntryHashValid ||
		tenantVerdict.SignatureValid == nil || !*tenantVerdict.SignatureValid {
		t.Fatalf("another tenant's tamper affected scoped verification: %+v", tenantVerdict)
	}
	if err := s.db.Model(&db.AuditLog{}).Where("id = ?", tenantLatest.ID).Update("details", "own tenant tampered").Error; err != nil {
		t.Fatal(err)
	}
	tenantVerdict = readAuditVerdict(t, s, tenantCtx, httptest.NewRecorder())
	if tenantVerdict.OK || tenantVerdict.FirstBadID != tenantLatest.ID ||
		tenantVerdict.EntryHashValid == nil || *tenantVerdict.EntryHashValid ||
		tenantVerdict.SignatureValid == nil || *tenantVerdict.SignatureValid {
		t.Fatalf("scoped verification missed own-row tamper: %+v", tenantVerdict)
	}

	globalCtx := mustAuditContext(s, t, "verify-global", 0)
	globalVerdict := readAuditVerdict(t, s, globalCtx, httptest.NewRecorder())
	if globalVerdict.OK || globalVerdict.Scope != "global" || globalVerdict.FirstBadID != otherRow.ID {
		t.Fatalf("global verification missed another tenant's tamper: %+v", globalVerdict)
	}
}

func TestAPICreateTaskAuditsTenant(t *testing.T) {
	s := mustTenantServer(t)
	seedMissingAPIAdmin(t, s)
	seedTenantAgent(t, s, "audit-rest-agent", 1)

	requests := []string{
		`{"agent_id":"audit-rest-agent","type":"shell","command":"shell-secret-marker"}`,
		`{"agent_id":"audit-rest-agent","type":"token_make","command":"token-user","shell":"token-secret-marker"}`,
		`{"agent_id":"audit-rest-agent","type":"write","command":"write-secret-marker","path":"write-secret-marker","data":"write-data-secret-marker"}`,
		`{"agent_id":"audit-rest-agent","type":"upload","command":"upload-secret-marker","path":"upload-secret-marker","data":"upload-data-secret-marker"}`,
	}
	for _, body := range requests {
		c, w := missingAPICtx(http.MethodPost, "/api/v1/tasks", body, nil)
		s.apiCreateTask(c)
		if w.Code != http.StatusOK {
			t.Fatalf("create %s = %d (%s)", body, w.Code, w.Body.String())
		}
	}

	cred := db.CredentialEntry{
		TenantID: 1, AgentID: "audit-rest-agent", Domain: "CORP", Username: "alice",
		Password: "credential-secret-marker", Type: "cleartext", Source: "manual",
	}
	if err := s.db.Create(&cred).Error; err != nil {
		t.Fatal(err)
	}
	credentialBody := fmt.Sprintf(`{"agent_id":"audit-rest-agent","type":"token_make","credential_id":%d}`, cred.ID)
	c, w := missingAPICtx(http.MethodPost, "/api/v1/tasks", credentialBody, nil)
	s.apiCreateTask(c)
	if w.Code != http.StatusOK {
		t.Fatalf("credential create = %d (%s)", w.Code, w.Body.String())
	}

	var rows []db.AuditLog
	if err := s.db.Where("action = ?", "task_create").Order("id ASC").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 5 {
		t.Fatalf("task creation audits = %d, want 5", len(rows))
	}
	for _, row := range rows {
		if row.TenantID != 1 || row.User != "admin" || row.Resource != "task" || row.AgentID != "audit-rest-agent" {
			t.Fatalf("task audit attribution = %+v", row)
		}
		for _, field := range []string{"task_id=", "agent_id=audit-rest-agent", "created_by=admin", "args="} {
			if !strings.Contains(row.Details, field) {
				t.Fatalf("task audit missing %q: %q", field, row.Details)
			}
		}
		if strings.Contains(row.Details, "secret-marker") {
			t.Fatalf("task audit leaked raw arguments: %q", row.Details)
		}
	}
	var duplicate int64
	if err := s.db.Model(&db.AuditLog{}).Where("action = ?", "credential_task_create").Count(&duplicate).Error; err != nil {
		t.Fatal(err)
	}
	if duplicate != 0 {
		t.Fatalf("credential task wrote %d duplicate special audits", duplicate)
	}
}

func TestDispatchTaskAuditsCreationWithoutRawArguments(t *testing.T) {
	s := mustTenantServer(t)
	c, _ := auditRequestContext(s, t, "dispatch-audit-operator", 1, "/agents/a/send_command")
	seedTenantAgent(t, s, "dispatch-audit-agent", 1)
	task, err := s.createTask("dispatch-audit-agent", "shell", "dispatch-secret-marker", "cmd.exe", "", "", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	s.dispatchTask(c, task, "send_command", "dispatch-secret-marker")

	var rows []db.AuditLog
	if err := s.db.Where("agent_id = ?", "dispatch-audit-agent").Order("id ASC").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Action != "task_create" {
		t.Fatalf("dispatch audit rows = %+v", rows)
	}
	if strings.Contains(rows[0].Details, "dispatch-secret-marker") {
		t.Fatalf("dispatch audit leaked raw command: %+v", rows[0])
	}
	if !strings.Contains(rows[0].Details, "created_by=dispatch-audit-operator") || !strings.Contains(rows[0].Details, "args=") {
		t.Fatalf("creation audit lost creator/fingerprint: %+v", rows[0])
	}
	if task.CreatedBy != "dispatch-audit-operator" {
		t.Fatalf("task creator = %q", task.CreatedBy)
	}
}

func TestAuditReadEndpointsRejectMissingNamedUser(t *testing.T) {
	s := mustTenantServer(t)
	for name, handler := range map[string]func(*gin.Context){
		"export": s.handleExportAuditChain,
		"anchor": s.handleAuditChainAnchor,
		"verify": s.handleVerifyAuditChain,
	} {
		t.Run(name, func(t *testing.T) {
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest(http.MethodGet, "/audit/"+name, nil)
			c.Set("user", "missing-audit-reader")
			handler(c)
			if w.Code != http.StatusForbidden {
				t.Fatalf("missing named user = %d, want 403 body=%s", w.Code, w.Body.String())
			}
		})
	}
}

func TestIdempotentReplayDoesNotDuplicateTaskCreateAudit(t *testing.T) {
	forgecrypto.InitLootEncryption(testStorageKeyHex)
	s := mustTenantServer(t)
	seedTenantAgent(t, s, "replay-audit-agent", 1)
	c, _ := tenantScopedAdminContext(s, t, "replay-audit-op", 1)
	first, err := s.createTask("replay-audit-agent", "shell", "whoami", "cmd.exe", "", "", 0, 0, WithIdempotencyKey("replay-audit-key"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.createTask("replay-audit-agent", "shell", "whoami", "cmd.exe", "", "", 0, 0, WithIdempotencyKey("replay-audit-key"))
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != second.ID || !second.IdempotentReplay {
		t.Fatalf("replay = id %d/%d replay=%v, want same id and replay=true", first.ID, second.ID, second.IdempotentReplay)
	}
	s.dispatchTask(c, first, "", "")
	s.dispatchTask(c, second, "", "")
	var count int64
	if err := s.db.Model(&db.AuditLog{}).Where("action = ?", "task_create").Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("task_create audits = %d, want 1", count)
	}
}
