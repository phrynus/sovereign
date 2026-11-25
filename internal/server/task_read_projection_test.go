package server

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/r0si/sovereign/internal/crypto"
	"github.com/r0si/sovereign/internal/db"
	"github.com/r0si/sovereign/internal/server/middleware"
)

const (
	projectionTokenShell   = "TOKEN_SHELL_MARKER_71c9"
	projectionTokenCommand = "TOKEN_COMMAND_MARKER_2ad4"
	projectionWriteData    = "WRITE_DATA_MARKER_b53e"
	projectionWritePath    = "WRITE_PATH_MARKER_8d20"
	projectionUploadData   = "UPLOAD_DATA_MARKER_4f61"
	projectionUploadPath   = "UPLOAD_PATH_MARKER_c912"
	projectionShellCommand = "SHELL_COMMAND_MARKER_6ab8"
	projectionShellResult  = "SHELL_RESULT_MARKER_e247"
	projectionFileResult   = "FILE_RESULT_MARKER_9bc1"
	projectionForeign      = "FOREIGN_TENANT_MARKER_03de"
)

type taskProjectionFixture struct {
	server        *Server
	agentOnly     db.User
	taskReader    db.User
	fileOnly      db.User
	foreign       db.User
	token         db.Task
	write         db.Task
	upload        db.Task
	shell         db.Task
	read          db.Task
	foreignTask   db.Task
	secretMarkers []string
}

func newTaskProjectionFixture(t *testing.T) taskProjectionFixture {
	t.Helper()
	crypto.InitLootEncryption(testStorageKeyHex)
	s := mustTenantServer(t)
	roles := []db.CustomRole{
		{Name: "projection-agent-only", Permissions: `["agents.read"]`},
		{Name: "projection-task-reader", Permissions: `["agents.read","tasks.read"]`},
		{Name: "projection-file-only", Permissions: `["agents.read","files.read"]`},
	}
	for i := range roles {
		if err := s.db.Create(&roles[i]).Error; err != nil {
			t.Fatalf("seed role: %v", err)
		}
	}
	users := []db.User{
		{Username: "projection-agent-only", Role: roles[0].Name, TenantID: 1, IsActive: true},
		{Username: "projection-task-reader", Role: roles[1].Name, TenantID: 1, IsActive: true},
		{Username: "projection-file-only", Role: roles[2].Name, TenantID: 1, IsActive: true},
		{Username: "projection-foreign-reader", Role: roles[1].Name, TenantID: 2, IsActive: true},
	}
	for i := range users {
		if err := s.db.Create(&users[i]).Error; err != nil {
			t.Fatalf("seed user: %v", err)
		}
	}
	agents := []db.Implant{
		{ID: "projection-agent-1", TenantID: 1, Hostname: "TENANT-ONE", LastSeen: time.Now()},
		{ID: "projection-agent-2", TenantID: 2, Hostname: "TENANT-TWO", LastSeen: time.Now()},
	}
	for i := range agents {
		if err := s.db.Create(&agents[i]).Error; err != nil {
			t.Fatalf("seed agent: %v", err)
		}
	}

	now := time.Now().Truncate(time.Second)
	seed := func(task db.Task) db.Task {
		t.Helper()
		task.CreatedAt = now
		task.UpdatedAt = now.Add(time.Second)
		if err := s.db.Create(&task).Error; err != nil {
			t.Fatalf("seed task: %v", err)
		}
		return task
	}
	fixture := taskProjectionFixture{
		server:     s,
		agentOnly:  users[0],
		taskReader: users[1],
		fileOnly:   users[2],
		foreign:    users[3],
		token: seed(db.Task{
			TenantID: 1, AgentID: agents[0].ID, Type: "token_make", Status: "completed",
			Command: projectionTokenCommand, Shell: projectionTokenShell, Result: projectionShellResult,
		}),
		write: seed(db.Task{
			TenantID: 1, AgentID: agents[0].ID, Type: "write", Status: "completed",
			Command: projectionWritePath, Path: projectionWritePath, Data: projectionWriteData, Result: projectionFileResult,
		}),
		upload: seed(db.Task{
			TenantID: 1, AgentID: agents[0].ID, Type: "upload", Status: "completed",
			Command: projectionUploadPath, Path: projectionUploadPath, Data: projectionUploadData, Result: projectionFileResult,
		}),
		shell: seed(db.Task{
			TenantID: 1, AgentID: agents[0].ID, Type: "shell", Status: "completed",
			Command: projectionShellCommand, Result: projectionShellResult,
		}),
		read: seed(db.Task{
			TenantID: 1, AgentID: agents[0].ID, Type: "read", Status: "completed",
			Command: "C:\\operator\\private.txt", Path: "C:\\operator\\private.txt", Result: projectionFileResult,
		}),
		foreignTask: seed(db.Task{
			TenantID: 2, AgentID: agents[1].ID, Type: "shell", Status: "completed",
			Command: projectionForeign, Result: projectionForeign,
		}),
	}
	fixture.secretMarkers = []string{
		projectionTokenShell, projectionTokenCommand, projectionWriteData, projectionWritePath,
		projectionUploadData, projectionUploadPath, projectionShellCommand, projectionShellResult,
		projectionFileResult, projectionForeign,
	}
	return fixture
}

func taskProjectionContext(s *Server, t *testing.T, user db.User, permissions map[string]bool, path string, params gin.Params) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, path, nil)
	c.Request.Header.Set("Accept", "application/json")
	c.Set("user", user.Username)
	c.Set("user_role", user.Role)
	c.Set("user_id", user.ID)
	c.Set("role_permissions", permissions)
	c.Set(middleware.TenantIDContextKey, user.TenantID)
	c.Params = params
	return c, w
}

func taskProjectionAgentOnlyContext(s *Server, t *testing.T, f taskProjectionFixture, path string, params gin.Params) (*gin.Context, *httptest.ResponseRecorder) {
	return taskProjectionContext(s, t, f.agentOnly, map[string]bool{db.PermAgentsRead: true}, path, params)
}

func taskProjectionReaderContext(s *Server, t *testing.T, f taskProjectionFixture, path string, params gin.Params) (*gin.Context, *httptest.ResponseRecorder) {
	return taskProjectionContext(s, t, f.taskReader, map[string]bool{db.PermAgentsRead: true, db.PermTasksRead: true}, path, params)
}

func assertNoTaskProjectionSecrets(t *testing.T, body []byte, markers []string) {
	t.Helper()
	for _, marker := range markers {
		if strings.Contains(string(body), marker) {
			t.Fatalf("response leaked task secret %q: %s", marker, body)
		}
	}
}

func assertTaskSummaryRows(t *testing.T, raw json.RawMessage, want int) {
	t.Helper()
	var rows []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &rows); err != nil {
		t.Fatalf("decode task summaries: %v (%s)", err, raw)
	}
	if len(rows) != want {
		t.Fatalf("summary count = %d, want %d (%s)", len(rows), want, raw)
	}
	for i, row := range rows {
		if len(row) != 4 {
			t.Fatalf("summary %d has fields outside the fixed contract: %v", i, row)
		}
		for _, key := range []string{"id", "status", "created_at", "updated_at"} {
			if _, ok := row[key]; !ok {
				t.Fatalf("summary %d missing %q: %v", i, key, row)
			}
		}
	}
}

func TestAgentTaskReadsRequireTasksRead(t *testing.T) {
	gin.SetMode(gin.TestMode)
	f := newTaskProjectionFixture(t)
	s := f.server
	agentParams := gin.Params{{Key: "id", Value: "projection-agent-1"}}

	t.Run("detail", func(t *testing.T) {
		c, w := taskProjectionAgentOnlyContext(s, t, f, "/agents/projection-agent-1?include_screenshots=false&include_unlinked=false", agentParams)
		s.handleAgentDetail(c)
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
		}
		var payload map[string]json.RawMessage
		unmarshalData(t, w.Body.Bytes(), &payload)
		if string(payload["tasks_redacted"]) != "true" {
			t.Fatalf("tasks_redacted = %s", payload["tasks_redacted"])
		}
		assertTaskSummaryRows(t, payload["tasks"], 5)
		for _, forbidden := range []string{"completed_tasks", "pending_tasks", "failed_tasks", "pending_count", "success_rate", "avg_response_time", "shell_tasks", "screenshot_tasks", "ps_tasks", "kill_tasks"} {
			if _, ok := payload[forbidden]; ok {
				t.Fatalf("redacted detail exposed derived aggregate %q", forbidden)
			}
		}
		assertNoTaskProjectionSecrets(t, w.Body.Bytes(), f.secretMarkers)

		c, w = taskProjectionReaderContext(s, t, f, "/agents/projection-agent-1?include_screenshots=false&include_unlinked=false", agentParams)
		s.handleAgentDetail(c)
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), projectionTokenCommand) || !strings.Contains(w.Body.String(), projectionWritePath) {
			t.Fatalf("task-reader detail lost authorized task data: status=%d body=%s", w.Code, w.Body.String())
		}
		if strings.Contains(w.Body.String(), `"tasks_redacted":true`) {
			t.Fatal("task-reader detail was marked redacted")
		}
	})

	t.Run("agent task pagination", func(t *testing.T) {
		path := "/agents/projection-agent-1/tasks?page=1&page_size=2"
		c, w := taskProjectionAgentOnlyContext(s, t, f, path, agentParams)
		s.handleGetAgentTasks(c)
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
		}
		var payload struct {
			Tasks         []taskSummary `json:"tasks"`
			Total         int           `json:"total"`
			Page          int           `json:"page"`
			PageSize      int           `json:"page_size"`
			TasksRedacted bool          `json:"tasks_redacted"`
		}
		unmarshalData(t, w.Body.Bytes(), &payload)
		if len(payload.Tasks) != 2 || payload.Total != 5 || payload.Page != 1 || payload.PageSize != 2 || !payload.TasksRedacted {
			t.Fatalf("unexpected redacted pagination: %+v", payload)
		}
		assertNoTaskProjectionSecrets(t, w.Body.Bytes(), f.secretMarkers)

		c, w = taskProjectionReaderContext(s, t, f, path, agentParams)
		s.handleGetAgentTasks(c)
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), projectionShellCommand) {
			t.Fatalf("task-reader list lost command: status=%d body=%s", w.Code, w.Body.String())
		}
	})

	t.Run("timeline", func(t *testing.T) {
		path := "/api/agents/projection-agent-1/timeline?kinds=task&limit=20"
		c, w := taskProjectionAgentOnlyContext(s, t, f, path, agentParams)
		s.handleAgentTimeline(c)
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
		}
		var payload struct {
			Events        []timelineEvent `json:"events"`
			Tasks         []taskSummary   `json:"tasks"`
			Total         int             `json:"total"`
			TasksRedacted bool            `json:"tasks_redacted"`
		}
		unmarshalData(t, w.Body.Bytes(), &payload)
		if !payload.TasksRedacted || payload.Total != 5 || len(payload.Tasks) != 5 {
			t.Fatalf("unexpected redacted timeline: %+v", payload)
		}
		for _, event := range payload.Events {
			if event.Kind == "task" && (event.Type != "" || event.Title != "Task") {
				t.Fatalf("redacted timeline exposed task type/command: %+v", event)
			}
		}
		assertNoTaskProjectionSecrets(t, w.Body.Bytes(), f.secretMarkers)

		c, w = taskProjectionReaderContext(s, t, f, path, agentParams)
		s.handleAgentTimeline(c)
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), projectionTokenCommand) {
			t.Fatalf("task-reader timeline lost command: status=%d body=%s", w.Code, w.Body.String())
		}
	})

	t.Run("status and output", func(t *testing.T) {
		for _, tc := range []struct {
			name         string
			task         db.Task
			marker       string
			outputMarker string
		}{
			{name: "token_make shell", task: f.token, marker: projectionTokenCommand, outputMarker: projectionShellResult},
			{name: "write data", task: f.write, marker: projectionWritePath, outputMarker: projectionFileResult},
			{name: "upload data", task: f.upload, marker: projectionUploadPath, outputMarker: projectionFileResult},
			{name: "shell", task: f.shell, marker: projectionShellCommand, outputMarker: projectionShellResult},
		} {
			t.Run(tc.name, func(t *testing.T) {
				params := gin.Params{{Key: "id", Value: "projection-agent-1"}, {Key: "taskId", Value: itoaUint(tc.task.ID)}}
				c, w := taskProjectionAgentOnlyContext(s, t, f, "/agents/projection-agent-1/tasks/"+itoaUint(tc.task.ID), params)
				s.handleGetTaskStatus(c)
				if w.Code != http.StatusForbidden {
					t.Fatalf("agent-only status = %d, want 403 body=%s", w.Code, w.Body.String())
				}
				assertNoTaskProjectionSecrets(t, w.Body.Bytes(), f.secretMarkers)

				c, w = taskProjectionAgentOnlyContext(s, t, f, "/agents/projection-agent-1/tasks/"+itoaUint(tc.task.ID)+"/output", params)
				s.handleDownloadTaskOutput(c)
				if w.Code != http.StatusForbidden {
					t.Fatalf("agent-only output = %d, want 403 body=%s", w.Code, w.Body.String())
				}
				assertNoTaskProjectionSecrets(t, w.Body.Bytes(), f.secretMarkers)

				c, w = taskProjectionReaderContext(s, t, f, "/agents/projection-agent-1/tasks/"+itoaUint(tc.task.ID), params)
				s.handleGetTaskStatus(c)
				if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), tc.marker) {
					t.Fatalf("task-reader status lost authorized data: status=%d body=%s", w.Code, w.Body.String())
				}

				c, w = taskProjectionReaderContext(s, t, f, "/agents/projection-agent-1/tasks/"+itoaUint(tc.task.ID)+"/output", params)
				s.handleDownloadTaskOutput(c)
				if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), tc.outputMarker) {
					t.Fatalf("task-reader output lost result: status=%d body=%s", w.Code, w.Body.String())
				}
			})
		}

		params := gin.Params{{Key: "id", Value: "projection-agent-1"}, {Key: "taskId", Value: itoaUint(f.read.ID)}}
		c, w := taskProjectionContext(s, t, f.fileOnly, map[string]bool{db.PermAgentsRead: true, db.PermFilesRead: true}, "/agents/projection-agent-1/tasks/"+itoaUint(f.read.ID), params)
		s.handleGetTaskStatus(c)
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), projectionFileResult) {
			t.Fatalf("file-only read status denied: status=%d body=%s", w.Code, w.Body.String())
		}
		c, w = taskProjectionContext(s, t, f.fileOnly, map[string]bool{db.PermAgentsRead: true, db.PermFilesRead: true}, "/agents/projection-agent-1/tasks/"+itoaUint(f.read.ID)+"/output", params)
		s.handleDownloadTaskOutput(c)
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), projectionFileResult) {
			t.Fatalf("file-only read output denied: status=%d body=%s", w.Code, w.Body.String())
		}
	})

	t.Run("dashboards", func(t *testing.T) {
		for _, tc := range []struct {
			name string
			call func(*gin.Context)
		}{
			{name: "api", call: s.apiDashboardStats},
			{name: "web", call: s.handleDashboard},
		} {
			t.Run(tc.name, func(t *testing.T) {
				c, w := taskProjectionAgentOnlyContext(s, t, f, "/api/v1/dashboard", nil)
				tc.call(c)
				if w.Code != http.StatusOK {
					t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
				}
				var payload map[string]json.RawMessage
				unmarshalData(t, w.Body.Bytes(), &payload)
				if string(payload["tasks_redacted"]) != "true" {
					t.Fatalf("tasks_redacted = %s", payload["tasks_redacted"])
				}
				key := "recent_tasks"
				if tc.name == "web" {
					key = "recent_tasks"
				}
				assertTaskSummaryRows(t, payload[key], 5)
				for _, forbidden := range []string{"today_tasks", "pending_tasks", "failed_tasks", "completed_tasks", "pending_count", "success_rate", "avg_response_time"} {
					if _, ok := payload[forbidden]; ok {
						t.Fatalf("redacted dashboard exposed derived aggregate %q", forbidden)
					}
				}
				assertNoTaskProjectionSecrets(t, w.Body.Bytes(), f.secretMarkers)

				c, w = taskProjectionReaderContext(s, t, f, "/api/v1/dashboard", nil)
				tc.call(c)
				if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), projectionShellCommand) {
					t.Fatalf("task-reader dashboard lost task data: status=%d body=%s", w.Code, w.Body.String())
				}
			})
		}
	})

	t.Run("tenant isolation", func(t *testing.T) {
		for _, tc := range []struct {
			name string
			call func(*gin.Context)
		}{
			{name: "detail", call: s.handleAgentDetail},
			{name: "tasks", call: s.handleGetAgentTasks},
			{name: "timeline", call: s.handleAgentTimeline},
		} {
			c, w := taskProjectionContext(s, t, f.foreign, map[string]bool{db.PermAgentsRead: true, db.PermTasksRead: true}, "/", agentParams)
			tc.call(c)
			if w.Code != http.StatusNotFound {
				t.Fatalf("foreign %s = %d, want 404 body=%s", tc.name, w.Code, w.Body.String())
			}
		}
		params := gin.Params{{Key: "id", Value: "projection-agent-1"}, {Key: "taskId", Value: itoaUint(f.shell.ID)}}
		c, w := taskProjectionContext(s, t, f.foreign, map[string]bool{db.PermAgentsRead: true, db.PermTasksRead: true}, "/", params)
		s.handleGetTaskStatus(c)
		if w.Code != http.StatusNotFound {
			t.Fatalf("foreign status = %d, want 404 body=%s", w.Code, w.Body.String())
		}
		c, w = taskProjectionContext(s, t, f.foreign, map[string]bool{db.PermAgentsRead: true, db.PermTasksRead: true}, "/", params)
		s.handleDownloadTaskOutput(c)
		if w.Code != http.StatusNotFound {
			t.Fatalf("foreign output = %d, want 404 body=%s", w.Code, w.Body.String())
		}

		c, w = taskProjectionContext(s, t, f.foreign, map[string]bool{db.PermAgentsRead: true, db.PermTasksRead: true}, "/api/v1/dashboard", nil)
		s.apiDashboardStats(c)
		if w.Code != http.StatusOK || strings.Contains(w.Body.String(), projectionShellCommand) || !strings.Contains(w.Body.String(), projectionForeign) {
			t.Fatalf("foreign dashboard was not tenant-scoped: status=%d body=%s", w.Code, w.Body.String())
		}
	})
}

func TestTaskWebSocketFanoutRequiresTasksRead(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, endpoint := range []string{"/ws", "/ws/operator"} {
		t.Run(endpoint, func(t *testing.T) {
			f := newTaskProjectionFixture(t)
			s := f.server
			s.cfg.Server.JWTSecret = "projection-ws-secret-at-least-32chars"
			if err := middleware.InitJWTSecret(s.cfg, ""); err != nil {
				t.Fatalf("InitJWTSecret: %v", err)
			}
			s.ctx = context.Background()
			s.wsUpgrader = websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
			if s.operatorSessions == nil {
				s.operatorSessions = &operatorSessionTracker{sessions: make(map[uint]*WSOperatorSession)}
			}

			if !s.userCanViewTasks(f.taskReader.Username) || !s.userCanViewTasks(f.foreign.Username) {
				t.Fatal("task-reader role did not resolve tasks.read")
			}
			if s.userCanViewTasks(f.agentOnly.Username) || s.userCanViewTasks(f.fileOnly.Username) || s.userCanViewTasks("missing-projection-user") {
				t.Fatal("a role without tasks.read received WebSocket task authority")
			}

			router := gin.New()
			if endpoint == "/ws" {
				router.GET(endpoint, s.handleWebSocket)
			} else {
				router.GET(endpoint, s.handleOperatorWS)
			}
			ts := httptest.NewServer(router)
			defer ts.Close()

			dial := func(user db.User) *websocket.Conn {
				t.Helper()
				token, err := middleware.GenerateToken(user, false, 24)
				if err != nil {
					t.Fatalf("generate token: %v", err)
				}
				if err := s.createSession(token, user.ID, "127.0.0.1", "projection-test", "", 86400); err != nil {
					t.Fatalf("create session: %v", err)
				}
				header := http.Header{}
				header.Add("Cookie", "sovereign_session="+token)
				conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(ts.URL, "http")+endpoint, header)
				if err != nil {
					t.Fatalf("dial %s: %v", endpoint, err)
				}
				conn.SetReadDeadline(time.Now().Add(3 * time.Second))
				_, first, err := conn.ReadMessage()
				if err != nil {
					t.Fatalf("read sync: %v", err)
				}
				var frame struct {
					Type string `json:"type"`
				}
				if err := json.Unmarshal(first, &frame); err != nil || frame.Type != "sync" {
					t.Fatalf("first frame = %s err=%v, want sync", first, err)
				}
				return conn
			}

			reader := dial(f.taskReader)
			defer reader.Close()
			agentOnly := dial(f.agentOnly)
			defer agentOnly.Close()
			fileOnly := dial(f.fileOnly)
			defer fileOnly.Close()
			foreign := dial(f.foreign)
			defer foreign.Close()

			small := f.shell
			small.Result = "WS_SMALL_RESULT_MARKER_51aa"
			s.broadcastTaskUpdate(small.AgentID, small)
			frame := readProjectionTaskFrame(t, reader)
			result, _ := frame["result"].(string)
			if frame["type"] != "task_update" || !strings.Contains(result, "WS_SMALL_RESULT_MARKER_51aa") {
				t.Fatalf("task-reader received wrong small task frame: %v", frame)
			}
			assertNoProjectionTaskFrame(t, agentOnly)
			assertNoProjectionTaskFrame(t, fileOnly)
			assertNoProjectionTaskFrame(t, foreign)

			large := f.shell
			large.ID += 100000
			large.Result = strings.Repeat("WS_LARGE_RESULT_MARKER_77bd\n", 1000)
			s.broadcastTaskUpdate(large.AgentID, large)
			var streamed strings.Builder
			for {
				frame = readProjectionTaskFrame(t, reader)
				if frame["type"] == "task_output" {
					chunk, _ := frame["chunk"].(string)
					streamed.WriteString(chunk)
					continue
				}
				if frame["type"] == "task_update" {
					break
				}
				t.Fatalf("unexpected task frame: %v", frame)
			}
			if !strings.Contains(streamed.String(), "WS_LARGE_RESULT_MARKER_77bd") {
				t.Fatalf("task-reader output stream lost content: %d bytes", streamed.Len())
			}
			assertNoProjectionTaskFrame(t, agentOnly)
			assertNoProjectionTaskFrame(t, fileOnly)
			assertNoProjectionTaskFrame(t, foreign)
		})
	}
}

func readProjectionTaskFrame(t *testing.T, conn *websocket.Conn) map[string]any {
	t.Helper()
	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	for {
		_, raw, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("read task frame: %v", err)
		}
		var frame map[string]any
		if err := json.Unmarshal(raw, &frame); err != nil {
			t.Fatalf("decode frame: %v (%s)", err, raw)
		}
		if frame["type"] == "task_update" || frame["type"] == "task_output" {
			return frame
		}
	}
}

func assertNoProjectionTaskFrame(t *testing.T, conn *websocket.Conn) {
	t.Helper()
	conn.SetReadDeadline(time.Now().Add(150 * time.Millisecond))
	for {
		_, raw, err := conn.ReadMessage()
		if err != nil {
			var netErr net.Error
			if errors.As(err, &netErr) && netErr.Timeout() {
				return
			}
			t.Fatalf("read while checking for forbidden task frame: %v", err)
		}
		var frame map[string]any
		if err := json.Unmarshal(raw, &frame); err != nil {
			t.Fatalf("decode frame: %v (%s)", err, raw)
		}
		if frame["type"] == "task_update" || frame["type"] == "task_output" {
			t.Fatalf("subscriber without tasks.read received task frame: %s", raw)
		}
	}
}

func TestAgentTimelineTotalNotLimited(t *testing.T) {
	gin.SetMode(gin.TestMode)
	f := newTaskProjectionFixture(t)
	params := gin.Params{{Key: "id", Value: "projection-agent-1"}}
	path := "/api/agents/projection-agent-1/timeline?kinds=task&limit=1"

	c, w := taskProjectionAgentOnlyContext(f.server, t, f, path, params)
	f.server.handleAgentTimeline(c)
	var redacted struct {
		Tasks []taskSummary `json:"tasks"`
		Total int           `json:"total"`
	}
	unmarshalData(t, w.Body.Bytes(), &redacted)
	if redacted.Total != 5 || len(redacted.Tasks) != 1 {
		t.Fatalf("redacted timeline total=%d rows=%d, want total=5 rows=1", redacted.Total, len(redacted.Tasks))
	}

	c, w = taskProjectionReaderContext(f.server, t, f, path, params)
	f.server.handleAgentTimeline(c)
	var full struct {
		Tasks []db.Task `json:"tasks"`
		Total int       `json:"total"`
	}
	unmarshalData(t, w.Body.Bytes(), &full)
	if full.Total != 5 || len(full.Tasks) != 1 {
		t.Fatalf("authorized timeline total=%d rows=%d, want total=5 rows=1", full.Total, len(full.Tasks))
	}
	if full.Tasks[0].UpdatedAt.IsZero() || full.Tasks[0].Result == "" {
		t.Fatalf("authorized timeline returned a partial task: %+v", full.Tasks[0])
	}
}
