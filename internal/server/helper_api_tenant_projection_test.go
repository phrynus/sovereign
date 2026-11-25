package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/r0si/sovereign/internal/crypto"
	"github.com/r0si/sovereign/internal/db"
)

const (
	helperLateralCommand = "wmi|10.0.0.9|alice|LATERAL_SECRET_MARKER|whoami"
	helperLateralResult  = "LATERAL_RESULT_MARKER"
	helperLateralError   = "LATERAL_ERROR_MARKER"
	helperMalformedSpec  = `{"method":"wmi","target":"JSON_TARGET_MARKER"}`
	helperPrivescCommand = "privesc_check:windows"
	helperPrivescResult  = "[!] PRIVESC_RESULT_MARKER"
	helperShellCommand   = "SHELL_COMMAND_MARKER"
	helperShellResult    = "SHELL_RESULT_MARKER"
	helperForeignResult  = "FOREIGN_HELPER_RESULT_MARKER"
)

type helperAPIFixture struct {
	server         *Server
	agentOnly      db.User
	taskReader     db.User
	foreign        db.User
	ownAgent       db.Implant
	foreignAgent   db.Implant
	lateral        db.Task
	lateralFailed  db.Task
	malformed      db.Task
	credentialMove db.Task
	privesc        db.Task
	shell          db.Task
	foreignTask    db.Task
	secretMarkers  []string
}

func newHelperAPIFixture(t *testing.T) helperAPIFixture {
	t.Helper()
	crypto.InitLootEncryption(tenantVisibilityMasterHex)
	s := mustTenantServer(t)
	roles := []db.CustomRole{
		{Name: "helper-agent-only", Permissions: `["agents.read"]`},
		{Name: "helper-task-reader", Permissions: `["agents.read","tasks.read"]`},
	}
	for i := range roles {
		if err := s.db.Create(&roles[i]).Error; err != nil {
			t.Fatalf("seed role: %v", err)
		}
	}
	users := []db.User{
		{Username: "helper-agent-only", Role: roles[0].Name, TenantID: 1, IsActive: true},
		{Username: "helper-task-reader", Role: roles[1].Name, TenantID: 1, IsActive: true},
		{Username: "helper-foreign-reader", Role: roles[1].Name, TenantID: 2, IsActive: true},
	}
	for i := range users {
		if err := s.db.Create(&users[i]).Error; err != nil {
			t.Fatalf("seed user: %v", err)
		}
	}
	agents := []db.Implant{
		{ID: "helper-agent-1", TenantID: 1, Hostname: "HELPER-ONE", Status: "online", LastSeen: time.Now()},
		{ID: "helper-agent-2", TenantID: 2, Hostname: "HELPER-TWO", Status: "online", LastSeen: time.Now()},
	}
	for i := range agents {
		if err := s.db.Create(&agents[i]).Error; err != nil {
			t.Fatalf("seed agent: %v", err)
		}
	}
	credential := db.CredentialEntry{
		TenantID: 1, AgentID: agents[0].ID, Username: "svc", Password: "VAULT_SECRET_MARKER",
		Source: "manual", Type: "cleartext",
	}
	if err := s.db.Create(&credential).Error; err != nil {
		t.Fatalf("seed credential: %v", err)
	}

	now := time.Now().Truncate(time.Second)
	seed := func(task db.Task) db.Task {
		t.Helper()
		task.CreatedAt = now
		task.UpdatedAt = now.Add(time.Second)
		task.CreatedBy = "operator-one"
		if err := s.db.Create(&task).Error; err != nil {
			t.Fatalf("seed task: %v", err)
		}
		return task
	}
	fixture := helperAPIFixture{
		server: s, agentOnly: users[0], taskReader: users[1], foreign: users[2],
		ownAgent: agents[0], foreignAgent: agents[1],
		lateral: seed(db.Task{
			TenantID: 1, AgentID: agents[0].ID, Type: "lateral", Status: "completed",
			Command: helperLateralCommand, Result: helperLateralResult,
		}),
		lateralFailed: seed(db.Task{
			TenantID: 1, AgentID: agents[0].ID, Type: "lateral", Status: "failed",
			Command: "winrm|10.0.0.11|bob|FAIL_SECRET_MARKER|whoami", Error: helperLateralError,
		}),
		malformed: seed(db.Task{
			TenantID: 1, AgentID: agents[0].ID, Type: "lateral", Status: "completed",
			Command: helperMalformedSpec, Result: "malformed spec result",
		}),
		credentialMove: seed(db.Task{
			TenantID: 1, AgentID: agents[0].ID, Type: "lateral", Status: "completed",
			Command: "@cred:" + itoaUint(credential.ID), CredentialID: &credential.ID,
			ResolvedCommand: "ssh|10.0.0.12|svc|RESOLVED_SECRET_MARKER|whoami", Result: "credential move result",
		}),
		privesc: seed(db.Task{
			TenantID: 1, AgentID: agents[0].ID, Type: "privesc_check", Status: "completed",
			Command: helperPrivescCommand, Result: helperPrivescResult,
		}),
		shell: seed(db.Task{
			TenantID: 1, AgentID: agents[0].ID, Type: "shell", Status: "completed",
			Command: helperShellCommand, Result: helperShellResult,
		}),
		foreignTask: seed(db.Task{
			TenantID: 2, AgentID: agents[1].ID, Type: "lateral", Status: "completed",
			Command: "ssh|10.1.0.9|foreign|FOREIGN_COMMAND_MARKER|whoami", Result: helperForeignResult,
		}),
	}
	fixture.secretMarkers = []string{
		"LATERAL_SECRET_MARKER", helperLateralResult, helperLateralError, "FAIL_SECRET_MARKER",
		"JSON_TARGET_MARKER", "RESOLVED_SECRET_MARKER", "VAULT_SECRET_MARKER",
		helperPrivescResult, helperShellCommand, helperShellResult,
		"FOREIGN_COMMAND_MARKER", helperForeignResult,
	}
	return fixture
}

func helperAgentOnlyContext(t *testing.T, f helperAPIFixture, path string, params gin.Params) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	return taskProjectionContext(f.server, t, f.agentOnly, map[string]bool{db.PermAgentsRead: true}, path, params)
}

func helperReaderContext(t *testing.T, f helperAPIFixture, path string, params gin.Params) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	return taskProjectionContext(f.server, t, f.taskReader, map[string]bool{db.PermAgentsRead: true, db.PermTasksRead: true}, path, params)
}

func TestHelperTaskAPIsRequireTasksRead(t *testing.T) {
	f := newHelperAPIFixture(t)
	s := f.server

	t.Run("lateral history projection", func(t *testing.T) {
		c, w := helperAgentOnlyContext(t, f, "/api/lateral/history/all", gin.Params{{Key: "id", Value: "all"}})
		s.handleLateralHistory(c)
		if w.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
		}
		var payload struct {
			Tasks         []LateralHistoryRow `json:"tasks"`
			Total         int                 `json:"total"`
			TasksRedacted bool                `json:"tasks_redacted"`
		}
		unmarshalData(t, w.Body.Bytes(), &payload)
		if !payload.TasksRedacted || payload.Total != 4 || len(payload.Tasks) != 4 {
			t.Fatalf("unexpected redacted history: %+v", payload)
		}
		for _, row := range payload.Tasks {
			if row.Type != "" || row.Target != "" || row.Method != "" || row.Result != "" || row.Error != "" {
				t.Fatalf("redacted lateral row exposed task content: %+v", row)
			}
		}
		assertNoTaskProjectionSecrets(t, w.Body.Bytes(), f.secretMarkers)

		c, w = helperReaderContext(t, f, "/api/lateral/history/all", gin.Params{{Key: "id", Value: "all"}})
		s.handleLateralHistory(c)
		if w.Code != http.StatusOK {
			t.Fatalf("reader status=%d body=%s", w.Code, w.Body.String())
		}
		unmarshalData(t, w.Body.Bytes(), &payload)
		byID := make(map[uint]LateralHistoryRow, len(payload.Tasks))
		for _, row := range payload.Tasks {
			byID[row.TaskID] = row
		}
		row := byID[f.lateral.ID]
		if row.Type != "lateral" || row.Method != "wmi" || row.Target != "10.0.0.9" || row.Result != helperLateralResult || row.Error != "" {
			t.Fatalf("canonical lateral row = %+v", row)
		}
		if row.SourceAgentID != f.ownAgent.ID || row.CreatedBy != "operator-one" {
			t.Fatalf("lateral attribution = %+v", row)
		}
		if got := byID[f.lateralFailed.ID]; got.Error != helperLateralError || got.Method != "winrm" {
			t.Fatalf("failed lateral row = %+v", got)
		}
		if got := byID[f.credentialMove.ID]; got.Method != "ssh" || got.Target != "10.0.0.12" {
			t.Fatalf("credential-backed canonical row = %+v", got)
		}
		if got := byID[f.malformed.ID]; got.Method != "" || got.Target != "" {
			t.Fatalf("non-canonical JSON produced history metadata: %+v", got)
		}
	})

	t.Run("toolkit recent and agent tasks", func(t *testing.T) {
		c, w := helperAgentOnlyContext(t, f, "/toolkit/results", nil)
		s.handleToolkitRecentResults(c)
		if w.Code != http.StatusOK {
			t.Fatalf("recent status=%d body=%s", w.Code, w.Body.String())
		}
		var payload map[string]json.RawMessage
		unmarshalData(t, w.Body.Bytes(), &payload)
		assertTaskSummaryRows(t, payload["tasks"], 6)
		if string(payload["tasks_redacted"]) != "true" {
			t.Fatalf("recent tasks_redacted=%s", payload["tasks_redacted"])
		}
		assertNoTaskProjectionSecrets(t, w.Body.Bytes(), f.secretMarkers)

		params := gin.Params{{Key: "id", Value: f.ownAgent.ID}}
		c, w = helperAgentOnlyContext(t, f, "/toolkit/agents/"+f.ownAgent.ID+"/tasks", params)
		s.handleToolkitAgentTasks(c)
		if w.Code != http.StatusOK {
			t.Fatalf("agent tasks status=%d body=%s", w.Code, w.Body.String())
		}
		unmarshalData(t, w.Body.Bytes(), &payload)
		assertTaskSummaryRows(t, payload["tasks"], 6)
		assertNoTaskProjectionSecrets(t, w.Body.Bytes(), f.secretMarkers)

		c, w = helperReaderContext(t, f, "/toolkit/results", nil)
		s.handleToolkitRecentResults(c)
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), helperShellCommand) || !strings.Contains(w.Body.String(), helperShellResult) {
			t.Fatalf("reader recent results lost task data: status=%d body=%s", w.Code, w.Body.String())
		}
	})

	t.Run("toolkit agent info aggregates", func(t *testing.T) {
		params := gin.Params{{Key: "id", Value: f.ownAgent.ID}}
		c, w := helperAgentOnlyContext(t, f, "/toolkit/agents/"+f.ownAgent.ID+"/info", params)
		s.handleToolkitAgentInfo(c)
		if w.Code != http.StatusOK {
			t.Fatalf("info status=%d body=%s", w.Code, w.Body.String())
		}
		var payload map[string]json.RawMessage
		unmarshalData(t, w.Body.Bytes(), &payload)
		if _, ok := payload["success_rate"]; ok {
			t.Fatalf("redacted info exposed success_rate: %s", w.Body.String())
		}
		if string(payload["tasks_redacted"]) != "true" || string(payload["task_count"]) != "6" {
			t.Fatalf("redacted info payload=%s", w.Body.String())
		}

		c, w = helperReaderContext(t, f, "/toolkit/agents/"+f.ownAgent.ID+"/info", params)
		s.handleToolkitAgentInfo(c)
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"success_rate"`) {
			t.Fatalf("reader info lost success_rate: status=%d body=%s", w.Code, w.Body.String())
		}
	})

	t.Run("privesc page and history projection", func(t *testing.T) {
		c, w := helperAgentOnlyContext(t, f, "/privesc", nil)
		s.handlePrivescPage(c)
		if w.Code != http.StatusOK {
			t.Fatalf("page status=%d body=%s", w.Code, w.Body.String())
		}
		var page map[string]json.RawMessage
		if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
			t.Fatal(err)
		}
		if _, ok := page["findings"]; ok {
			t.Fatalf("redacted privesc page exposed findings: %s", w.Body.Bytes())
		}
		var history []privescHistoryEntry
		if err := json.Unmarshal(page["history"], &history); err != nil {
			t.Fatal(err)
		}
		if len(history) != 1 || history[0].CheckType != "" || history[0].Result != "" || history[0].FindingsCount != nil {
			t.Fatalf("redacted privesc history=%+v", history)
		}
		assertNoTaskProjectionSecrets(t, w.Body.Bytes(), f.secretMarkers)

		params := gin.Params{{Key: "id", Value: itoaUint(f.privesc.ID)}}
		c, w = helperAgentOnlyContext(t, f, "/api/privesc/history/"+itoaUint(f.privesc.ID), params)
		s.handlePrivescHistory(c)
		if w.Code != http.StatusOK {
			t.Fatalf("history status=%d body=%s", w.Code, w.Body.String())
		}
		var payload map[string]json.RawMessage
		unmarshalData(t, w.Body.Bytes(), &payload)
		if _, ok := payload["findings"]; ok {
			t.Fatalf("redacted privesc history exposed findings: %s", w.Body.String())
		}
		assertNoTaskProjectionSecrets(t, w.Body.Bytes(), f.secretMarkers)

		c, w = helperReaderContext(t, f, "/api/privesc/history/"+itoaUint(f.privesc.ID), params)
		s.handlePrivescHistory(c)
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), helperPrivescResult) || !strings.Contains(w.Body.String(), `"findings"`) {
			t.Fatalf("reader privesc history lost authorized data: status=%d body=%s", w.Code, w.Body.String())
		}
	})
}

func TestHelperTaskAPIsAreTenantScoped(t *testing.T) {
	f := newHelperAPIFixture(t)
	s := f.server

	t.Run("lateral", func(t *testing.T) {
		c, w := taskProjectionContext(s, t, f.foreign, map[string]bool{db.PermAgentsRead: true, db.PermTasksRead: true}, "/api/lateral/history/all", gin.Params{{Key: "id", Value: "all"}})
		s.handleLateralHistory(c)
		if w.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
		}
		var payload struct {
			Tasks []LateralHistoryRow `json:"tasks"`
			Total int                 `json:"total"`
		}
		unmarshalData(t, w.Body.Bytes(), &payload)
		if payload.Total != 1 || len(payload.Tasks) != 1 || payload.Tasks[0].TaskID != f.foreignTask.ID {
			t.Fatalf("foreign lateral history=%+v", payload)
		}
		assertNoTaskProjectionSecrets(t, w.Body.Bytes(), []string{helperLateralResult, helperPrivescResult, helperShellResult})

		c, w = taskProjectionContext(s, t, f.foreign, map[string]bool{db.PermAgentsRead: true, db.PermTasksRead: true}, "/api/lateral/history/"+f.ownAgent.ID, gin.Params{{Key: "id", Value: f.ownAgent.ID}})
		s.handleLateralHistory(c)
		if w.Code != http.StatusNotFound {
			t.Fatalf("foreign agent history status=%d, want 404", w.Code)
		}
	})

	t.Run("toolkit", func(t *testing.T) {
		c, w := taskProjectionContext(s, t, f.foreign, map[string]bool{db.PermAgentsRead: true, db.PermTasksRead: true}, "/toolkit/results", nil)
		s.handleToolkitRecentResults(c)
		if w.Code != http.StatusOK || strings.Contains(w.Body.String(), helperShellResult) || !strings.Contains(w.Body.String(), helperForeignResult) {
			t.Fatalf("foreign toolkit results leaked: status=%d body=%s", w.Code, w.Body.String())
		}
		params := gin.Params{{Key: "id", Value: f.ownAgent.ID}}
		c, w = taskProjectionContext(s, t, f.foreign, map[string]bool{db.PermAgentsRead: true, db.PermTasksRead: true}, "/", params)
		s.handleToolkitAgentInfo(c)
		if w.Code != http.StatusNotFound {
			t.Fatalf("foreign agent info status=%d, want 404", w.Code)
		}
		c, w = taskProjectionContext(s, t, f.foreign, map[string]bool{db.PermAgentsRead: true, db.PermTasksRead: true}, "/", params)
		s.handleToolkitAgentTasks(c)
		if w.Code != http.StatusNotFound {
			t.Fatalf("foreign agent tasks status=%d, want 404", w.Code)
		}
	})

	t.Run("privesc", func(t *testing.T) {
		params := gin.Params{{Key: "id", Value: itoaUint(f.privesc.ID)}}
		c, w := taskProjectionContext(s, t, f.foreign, map[string]bool{db.PermAgentsRead: true, db.PermTasksRead: true}, "/", params)
		s.handlePrivescHistory(c)
		if w.Code != http.StatusNotFound {
			t.Fatalf("foreign privesc task status=%d, want 404 body=%s", w.Code, w.Body.String())
		}
		c, w = taskProjectionContext(s, t, f.foreign, map[string]bool{db.PermAgentsRead: true}, "/privesc", nil)
		s.handlePrivescPage(c)
		if w.Code != http.StatusOK || strings.Contains(w.Body.String(), helperPrivescResult) {
			t.Fatalf("foreign privesc page leaked: status=%d body=%s", w.Code, w.Body.String())
		}
	})
}

func TestHelperTaskAPIQueryFailuresAre500(t *testing.T) {
	assertQueryFailureIs500(t, "lateral history", "/api/lateral/history/all", func(s *Server, c *gin.Context) {
		c.Params = gin.Params{{Key: "id", Value: "all"}}
		s.handleLateralHistory(c)
	})
	assertQueryFailureIs500(t, "toolkit recent", "/toolkit/results", func(s *Server, c *gin.Context) {
		s.handleToolkitRecentResults(c)
	})
	assertQueryFailureIs500(t, "toolkit agent info", "/toolkit/agents/a/info", func(s *Server, c *gin.Context) {
		c.Params = gin.Params{{Key: "id", Value: "a"}}
		s.handleToolkitAgentInfo(c)
	})
	assertQueryFailureIs500(t, "toolkit agent tasks", "/toolkit/agents/a/tasks", func(s *Server, c *gin.Context) {
		c.Params = gin.Params{{Key: "id", Value: "a"}}
		s.handleToolkitAgentTasks(c)
	})
	assertQueryFailureIs500(t, "privesc history", "/api/privesc/history/1", func(s *Server, c *gin.Context) {
		c.Params = gin.Params{{Key: "id", Value: "1"}}
		s.handlePrivescHistory(c)
	})
}
