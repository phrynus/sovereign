package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/r0si/sovereign/internal/config"
	"github.com/r0si/sovereign/internal/db"
)

func TestCreateTaskIdempotencyFingerprintConflict(t *testing.T) {
	s := newTasksTestServer(t)
	seedAgent(t, s, "fingerprint-agent")
	first, err := s.createTask("fingerprint-agent", "shell", "whoami", "", "", "", 0, 0, WithIdempotencyKey("fingerprint-key"))
	if err != nil {
		t.Fatal(err)
	}
	if first.IdempotencyFingerprint == "" {
		t.Fatal("new task did not persist its canonical fingerprint")
	}
	replay, err := s.createTask("fingerprint-agent", "shell", "whoami", "", "", "", 0, 0, WithIdempotencyKey("fingerprint-key"))
	if err != nil || replay.ID != first.ID || !replay.IdempotentReplay {
		t.Fatalf("same request replay = %+v, err = %v", replay, err)
	}
	if _, err := s.createTask("fingerprint-agent", "shell", "hostname", "", "", "", 0, 0, WithIdempotencyKey("fingerprint-key")); !errors.Is(err, errIdempotencyConflict) {
		t.Fatalf("different command error = %v", err)
	}
	if _, err := s.createTask("fingerprint-agent", "shell", "whoami", "cmd", "", "", 0, 0, WithIdempotencyKey("fingerprint-key")); !errors.Is(err, errIdempotencyConflict) {
		t.Fatalf("different shell error = %v", err)
	}
	var count int64
	if err := s.db.Model(&db.Task{}).Where("agent_id = ?", "fingerprint-agent").Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("conflicting requests persisted %d tasks", count)
	}
}

func TestRESTTaskIdempotencyReplaysWithoutSideEffects(t *testing.T) {
	s := mustTenantServer(t)
	seedMissingAPIAdmin(t, s)
	seedTenantAgent(t, s, "rest-idempotency-agent", 1)
	body := `{"agent_id":"rest-idempotency-agent","type":"shell","command":"whoami","encrypt_result":true,"idempotency_key":"rest-key"}`

	c, w := missingAPICtx(http.MethodPost, "/api/v1/tasks", body, nil)
	s.apiCreateTask(c)
	if w.Code != http.StatusOK {
		t.Fatalf("create status = %d, body = %s", w.Code, w.Body.String())
	}
	var created struct {
		ID uint `json:"id"`
	}
	unmarshalData(t, w.Body.Bytes(), &created)
	if created.ID == 0 {
		t.Fatal("create returned no task id")
	}
	var first db.Task
	if err := s.db.First(&first, created.ID).Error; err != nil {
		t.Fatal(err)
	}
	if first.TaskKey == "" {
		t.Fatal("encrypt_result did not mint a task key")
	}

	c, w = missingAPICtx(http.MethodPost, "/api/v1/tasks", body, nil)
	s.apiCreateTask(c)
	if w.Code != http.StatusOK {
		t.Fatalf("replay status = %d, body = %s", w.Code, w.Body.String())
	}
	var replayed struct {
		ID uint `json:"id"`
	}
	unmarshalData(t, w.Body.Bytes(), &replayed)
	if replayed.ID != created.ID {
		t.Fatalf("replay id = %d, want %d", replayed.ID, created.ID)
	}
	var after db.Task
	if err := s.db.First(&after, created.ID).Error; err != nil {
		t.Fatal(err)
	}
	if after.TaskKey != first.TaskKey {
		t.Fatal("replay rotated the task encryption key")
	}

	c, w = missingAPICtx(http.MethodPost, "/api/v1/tasks",
		`{"agent_id":"rest-idempotency-agent","type":"shell","command":"hostname","idempotency_key":"rest-key"}`, nil)
	s.apiCreateTask(c)
	if w.Code != http.StatusConflict {
		t.Fatalf("different request status = %d, body = %s", w.Code, w.Body.String())
	}

	c, w = missingAPICtx(http.MethodPost, "/api/v1/tasks",
		`{"agent_id":"rest-idempotency-agent","type":"shell","command":"whoami"}`, nil)
	c.Request.Header.Set("Idempotency-Key", "header-key")
	s.apiCreateTask(c)
	if w.Code != http.StatusOK {
		t.Fatalf("header-key create status = %d, body = %s", w.Code, w.Body.String())
	}

	c, w = missingAPICtx(http.MethodPost, "/api/v1/tasks",
		`{"agent_id":"rest-idempotency-agent","type":"shell","command":"whoami","idempotency_key":"body-key"}`, nil)
	c.Request.Header.Set("Idempotency-Key", "different-header-key")
	s.apiCreateTask(c)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("disagreeing keys status = %d, body = %s", w.Code, w.Body.String())
	}
}

func TestBatchTaskIdempotencyFingerprintConflict(t *testing.T) {
	s := newTasksTestServer(t)
	s.cfg = &config.Config{}
	seedImplantForBatch(t, s, "batch-fingerprint-agent")
	send := func(command string) *httptest.ResponseRecorder {
		t.Helper()
		body, _ := json.Marshal(map[string]interface{}{
			"agent_ids": []string{"batch-fingerprint-agent"}, "task_type": "shell",
			"command": command, "idempotency_key": "batch-fingerprint-key",
		})
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, "/agents/batch", bytes.NewReader(body))
		c.Request.Header.Set("Content-Type", "application/json")
		c.Set("user", "alice")
		c.Set("user_role", db.RoleAdmin)
		s.handleBatchCommand(c)
		return w
	}
	if w := send("whoami"); w.Code != http.StatusOK {
		t.Fatalf("create status = %d, body = %s", w.Code, w.Body.String())
	}
	if w := send("whoami"); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"deduped":1`) {
		t.Fatalf("replay status = %d, body = %s", w.Code, w.Body.String())
	}
	if w := send("hostname"); w.Code != http.StatusConflict {
		t.Fatalf("conflict status = %d, body = %s", w.Code, w.Body.String())
	}
	var count int64
	if err := s.db.Model(&db.Task{}).Where("agent_id = ?", "batch-fingerprint-agent").Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("batch requests persisted %d tasks", count)
	}
}
