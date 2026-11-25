package server

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	forgecrypto "github.com/r0si/sovereign/internal/crypto"
	"github.com/r0si/sovereign/internal/db"
	"github.com/r0si/sovereign/internal/testutil"
)

func editorTestServer(t *testing.T) *Server {
	t.Helper()
	forgecrypto.InitLootEncryption(testStorageKeyHex)
	return initV3BeaconServer(t, testutil.SetupTestDB(t), tenantVisibilityMasterHex)
}

func seedReadBaseline(t *testing.T, s *Server, agentID, path, content string) db.Task {
	t.Helper()
	sum := sha256.Sum256([]byte(content))
	task := db.Task{
		AgentID: agentID, TenantID: 1, Type: "read", Command: path, Path: path,
		Status: "completed", Result: base64.StdEncoding.EncodeToString([]byte(content)),
		ResultSHA256: hex.EncodeToString(sum[:]), ResultModTime: 1_700_000_000,
		MAC: strings.Repeat("0", 64), CreatedBy: "reader",
	}
	if err := s.db.Create(&task).Error; err != nil {
		t.Fatal(err)
	}
	return task
}

func editorFormCtx(t *testing.T, s *Server, agentID, content string, baseID uint) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	form := url.Values{}
	form.Set("path", "C:\\temp\\note.txt")
	form.Set("content", content)
	form.Set("base_task_id", strconv.FormatUint(uint64(baseID), 10))
	c, w := tenantScopedAdminContext(s, t, "editor", 1)
	c.Params = gin.Params{{Key: "id", Value: agentID}}
	c.Request, _ = http.NewRequest(http.MethodPost, "/agents/"+agentID+"/files/write", strings.NewReader(form.Encode()))
	c.Request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return c, w
}

func TestFileEditorWriteWithCompletedBaseline(t *testing.T) {
	s := editorTestServer(t)
	if err := s.db.Create(&db.Implant{ID: "edit-agent", TenantID: 1}).Error; err != nil {
		t.Fatal(err)
	}
	base := seedReadBaseline(t, s, "edit-agent", "C:\\temp\\note.txt", "old content")
	c, w := editorFormCtx(t, s, "edit-agent", "new content", base.ID)
	s.handleFileWrite(c)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var task db.Task
	if err := s.db.Order("id DESC").First(&task, "agent_id = ? AND type = ?", "edit-agent", "write").Error; err != nil {
		t.Fatal(err)
	}
	if task.BaseTaskID == nil || *task.BaseTaskID != base.ID {
		t.Fatalf("base task = %v", task.BaseTaskID)
	}
	if task.ExpectedSHA256 != base.ResultSHA256 {
		t.Fatalf("expected hash = %q", task.ExpectedSHA256)
	}
	command, _, err := taskWireArgs(task)
	if err != nil || command != "C:\\temp\\note.txt" {
		t.Fatalf("wire command = %q err=%v", command, err)
	}
	var rawDraft string
	if err := s.db.Table("tasks").Where("id = ?", task.ID).Select("data").Scan(&rawDraft).Error; err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(rawDraft, "FC2ENC:") {
		t.Fatalf("draft at rest = %q", rawDraft)
	}
	projected := taskForAuthorizedOperator(task)
	if projected.Data != "" {
		t.Fatalf("projection leaked the draft: %+v", projected)
	}
}

func TestFileEditorEmptyAndInvalidContent(t *testing.T) {
	s := editorTestServer(t)
	if err := s.db.Create(&db.Implant{ID: "edit-agent", TenantID: 1}).Error; err != nil {
		t.Fatal(err)
	}
	base := seedReadBaseline(t, s, "edit-agent", "C:\\temp\\note.txt", "")

	c, w := editorFormCtx(t, s, "edit-agent", "", base.ID)
	s.handleFileWrite(c)
	if w.Code != http.StatusOK {
		t.Fatalf("empty write status=%d body=%s", w.Code, w.Body.String())
	}
	var tasks []db.Task
	s.db.Where("agent_id = ? AND type = ?", "edit-agent", "write").Find(&tasks)
	if len(tasks) != 1 || tasks[0].ExpectedSHA256 != base.ResultSHA256 {
		t.Fatalf("empty write tasks = %+v", tasks)
	}

	c, w = editorFormCtx(t, s, "edit-agent", string([]byte{0, 'a'}), base.ID)
	s.handleFileWrite(c)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("NUL content status=%d", w.Code)
	}
	c, w = editorFormCtx(t, s, "edit-agent", strings.Repeat("x", (1<<20)+1), base.ID)
	s.handleFileWrite(c)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("oversized content status=%d", w.Code)
	}
}

func TestFileEditorRejectsForeignBaseline(t *testing.T) {
	s := editorTestServer(t)
	if err := s.db.Create(&db.Implant{ID: "edit-agent", TenantID: 1}).Error; err != nil {
		t.Fatal(err)
	}
	foreign := seedReadBaseline(t, s, "other-agent", "C:\\temp\\note.txt", "secret")
	c, w := editorFormCtx(t, s, "edit-agent", "new", foreign.ID)
	s.handleFileWrite(c)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var count int64
	s.db.Model(&db.Task{}).Where("agent_id = ? AND type = ?", "edit-agent", "write").Count(&count)
	if count != 0 {
		t.Fatalf("created %d write tasks from a foreign baseline", count)
	}
}

func TestFileEditorHistoryAndRevisionAreTenantScoped(t *testing.T) {
	s := editorTestServer(t)
	if err := s.db.Create(&db.Implant{ID: "edit-agent", TenantID: 1}).Error; err != nil {
		t.Fatal(err)
	}
	base := seedReadBaseline(t, s, "edit-agent", "C:\\temp\\note.txt", "revision bytes")
	foreign := seedReadBaseline(t, s, "other-agent", "C:\\temp\\note.txt", "foreign bytes")

	c, w := tenantScopedAdminContext(s, t, "viewer", 1)
	c.Request, _ = http.NewRequest(http.MethodGet, "/agents/edit-agent/files/history?path="+url.QueryEscape("C:\\temp\\note.txt"), nil)
	c.Params = gin.Params{{Key: "id", Value: "edit-agent"}}
	s.handleFileHistory(c)
	if w.Code != http.StatusOK {
		t.Fatalf("history status=%d", w.Code)
	}
	var history struct {
		Revisions []struct {
			TaskID uint   `json:"task_id"`
			SHA256 string `json:"sha256"`
			Mtime  int64  `json:"mtime"`
		} `json:"revisions"`
	}
	if err := json.Unmarshal(envelopeData(t, w.Body.Bytes()), &history); err != nil {
		t.Fatal(err)
	}
	if len(history.Revisions) != 1 || history.Revisions[0].TaskID != base.ID {
		t.Fatalf("history = %+v", history.Revisions)
	}

	c, w = tenantScopedAdminContext(s, t, "viewer", 1)
	c.Request, _ = http.NewRequest(http.MethodGet, "/agents/edit-agent/files/revisions/"+strconv.FormatUint(uint64(foreign.ID), 10), nil)
	c.Params = gin.Params{{Key: "id", Value: "edit-agent"}, {Key: "taskId", Value: strconv.FormatUint(uint64(foreign.ID), 10)}}
	s.handleFileRevision(c)
	if w.Code != http.StatusNotFound {
		t.Fatalf("foreign revision status=%d", w.Code)
	}

	c, w = tenantScopedAdminContext(s, t, "viewer", 1)
	c.Request, _ = http.NewRequest(http.MethodGet, "/agents/edit-agent/files/revisions/"+strconv.FormatUint(uint64(base.ID), 10), nil)
	c.Params = gin.Params{{Key: "id", Value: "edit-agent"}, {Key: "taskId", Value: strconv.FormatUint(uint64(base.ID), 10)}}
	s.handleFileRevision(c)
	if w.Code != http.StatusOK {
		t.Fatalf("revision status=%d", w.Code)
	}
	var revision struct {
		ContentBase64 string `json:"content_base64"`
		SHA256        string `json:"sha256"`
	}
	if err := json.Unmarshal(envelopeData(t, w.Body.Bytes()), &revision); err != nil {
		t.Fatal(err)
	}
	decoded, _ := base64.StdEncoding.DecodeString(revision.ContentBase64)
	if !bytes.Equal(decoded, []byte("revision bytes")) || revision.SHA256 != base.ResultSHA256 {
		t.Fatalf("revision = %+v", revision)
	}
}

func envelopeData(t *testing.T, body []byte) []byte {
	t.Helper()
	var env struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &env); err != nil || len(env.Data) == 0 {
		t.Fatalf("bad envelope: %s", body)
	}
	return env.Data
}
