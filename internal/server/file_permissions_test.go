package server

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	forgecrypto "github.com/r0si/sovereign/internal/crypto"
	"github.com/r0si/sovereign/internal/db"
	"github.com/r0si/sovereign/internal/server/middleware"
)

type filePermissionFixture struct {
	server        *Server
	agent         db.Implant
	foreign       db.Implant
	users         map[string]db.User
	permissions   map[string]map[string]bool
	completedPull db.FileTransfer
	activePull    db.FileTransfer
	readTask      db.Task
	shellTask     db.Task
}

func newFilePermissionFixture(t *testing.T) filePermissionFixture {
	t.Helper()
	forgecrypto.InitLootEncryption(testStorageKeyHex)
	ginSetTestMode(t)
	s := mustTenantServer(t)
	s.configMu.Lock()
	s.cfg.Server.DataDir = t.TempDir()
	s.configMu.Unlock()

	roles := []db.CustomRole{
		{Name: "file-permission-reader", Permissions: `["files.read"]`},
		{Name: "file-permission-writer", Permissions: `["files.write"]`},
		{Name: "file-permission-agent-only", Permissions: `["agents.read"]`},
		{Name: "file-permission-task-reader", Permissions: `["agents.read","tasks.read"]`},
	}
	for i := range roles {
		if err := s.db.Create(&roles[i]).Error; err != nil {
			t.Fatalf("seed role: %v", err)
		}
	}
	users := make(map[string]db.User)
	for _, role := range roles {
		user := db.User{Username: role.Name, Role: role.Name, TenantID: 1, IsActive: true}
		if err := s.db.Create(&user).Error; err != nil {
			t.Fatalf("seed user: %v", err)
		}
		users[role.Name] = user
	}
	agent := db.Implant{ID: "file-permission-agent", TenantID: 1, Hostname: "FILES-ONE", OS: "windows", LastSeen: time.Now()}
	foreign := db.Implant{ID: "file-permission-foreign", TenantID: 2, Hostname: "FILES-TWO", OS: "linux", LastSeen: time.Now()}
	for _, implant := range []db.Implant{agent, foreign} {
		if err := s.db.Create(&implant).Error; err != nil {
			t.Fatalf("seed implant: %v", err)
		}
	}

	now := time.Now()
	completed := db.FileTransfer{
		TenantID: 1, AgentID: agent.ID, CreatedBy: users[roles[0].Name].Username,
		Direction: transferDirectionPull, RemotePath: "/srv/report.txt", PathKey: transferPathKey(agent.ID, "/srv/report.txt"),
		Filename: "report.txt", TotalSize: 13, CommittedOffset: 13, ChunkSize: FileTransferChunkSize,
		SHA256: fmt.Sprintf("%x", sha256.Sum256([]byte("pulled bytes\n"))), PrevMAC: strings.Repeat("0", 64),
		Status: transferStatusCompleted, CreatedAt: now, UpdatedAt: now, ExpiresAt: now.Add(time.Hour), CompletedAt: &now,
	}
	active := db.FileTransfer{
		TenantID: 1, AgentID: agent.ID, CreatedBy: users[roles[0].Name].Username,
		Direction: transferDirectionPull, RemotePath: "/srv/resume.txt", PathKey: transferPathKey(agent.ID, "/srv/resume.txt"),
		Filename: "resume.txt", TotalSize: 12, ChunkSize: FileTransferChunkSize,
		PrevMAC: strings.Repeat("0", 64), Status: transferStatusActive,
		CreatedAt: now, UpdatedAt: now, ExpiresAt: now.Add(time.Hour),
	}
	for _, transfer := range []*db.FileTransfer{&completed, &active} {
		if err := s.db.Create(transfer).Error; err != nil {
			t.Fatalf("seed transfer: %v", err)
		}
	}
	dir, err := s.transfersDir()
	if err != nil {
		t.Fatalf("transfer dir: %v", err)
	}
	if err := os.WriteFile(transferFinalPath(dir, completed.Filename, completed.ID), []byte("pulled bytes\n"), 0600); err != nil {
		t.Fatalf("seed pulled file: %v", err)
	}

	readTask := db.Task{TenantID: 1, AgentID: agent.ID, Type: "read", Command: `C:\private.txt`, Path: `C:\private.txt`, Status: "completed", Result: "FILE-CONTENT-MARKER", CreatedAt: now, UpdatedAt: now}
	shellTask := db.Task{TenantID: 1, AgentID: agent.ID, Type: "shell", Command: "whoami", Status: "completed", Result: "SHELL-CONTENT-MARKER", CreatedAt: now, UpdatedAt: now}
	for _, task := range []*db.Task{&readTask, &shellTask} {
		if err := s.db.Create(task).Error; err != nil {
			t.Fatalf("seed task: %v", err)
		}
	}

	return filePermissionFixture{
		server: s, agent: agent, foreign: foreign, users: users,
		permissions: map[string]map[string]bool{
			roles[0].Name: {db.PermFilesRead: true},
			roles[1].Name: {db.PermFilesWrite: true},
			roles[2].Name: {db.PermAgentsRead: true},
			roles[3].Name: {db.PermAgentsRead: true, db.PermTasksRead: true},
		},
		completedPull: completed, activePull: active, readTask: readTask, shellTask: shellTask,
	}
}

func (f filePermissionFixture) router(t *testing.T, username string) *gin.Engine {
	t.Helper()
	router := gin.New()
	auth := router.Group("/")
	auth.Use(func(c *gin.Context) {
		user := f.users[username]
		c.Set("user", user.Username)
		c.Set("user_role", user.Role)
		c.Set("user_id", user.ID)
		c.Set("role_permissions", f.permissions[user.Role])
		c.Set(middleware.TenantIDContextKey, user.TenantID)
	})
	f.server.registerAgentRoutes(auth)
	f.server.registerAgentCommandRoutes(auth)
	return router
}

func filePermissionRequest(t *testing.T, router http.Handler, username, method, target, body, contentType string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.Header.Set("Accept", "application/json")
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	return w
}

func fileTransferIDFromResponse(t *testing.T, w *httptest.ResponseRecorder) uint {
	t.Helper()
	var payload struct {
		Data struct {
			Transfer db.FileTransfer `json:"transfer"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode transfer response %d %s: %v", w.Code, w.Body.String(), err)
	}
	if payload.Data.Transfer.ID == 0 {
		t.Fatalf("missing transfer id: %s", w.Body.String())
	}
	return payload.Data.Transfer.ID
}

func TestFileRoutesUseFilePermissions(t *testing.T) {
	f := newFilePermissionFixture(t)
	agentPath := "/agents/" + f.agent.ID
	missingTransferPath := agentPath + "/transfers/999999"
	readCases := []struct {
		name, method, path, body, contentType string
		want                                  int
	}{
		{name: "page", method: http.MethodGet, path: agentPath + "/files?format=json", want: http.StatusOK},
		{name: "ls", method: http.MethodPost, path: agentPath + "/files/ls", want: http.StatusOK},
		{name: "search", method: http.MethodPost, path: agentPath + "/files/search", want: http.StatusBadRequest},
		{name: "scan", method: http.MethodPost, path: agentPath + "/files/scan", want: http.StatusBadRequest},
		{name: "thumb", method: http.MethodPost, path: agentPath + "/files/thumb", want: http.StatusBadRequest},
		{name: "thumb get", method: http.MethodGet, path: agentPath + "/files/thumb/" + strings.Repeat("a", 64), want: http.StatusNotFound},
		{name: "read", method: http.MethodPost, path: agentPath + "/files/read", want: http.StatusBadRequest},
		{name: "history", method: http.MethodGet, path: agentPath + "/files/history?path=" + url.QueryEscape(f.readTask.Command), want: http.StatusOK},
		{name: "revision", method: http.MethodGet, path: agentPath + "/files/revisions/" + strconv.FormatUint(uint64(f.readTask.ID), 10), want: http.StatusOK},
		{name: "find", method: http.MethodPost, path: agentPath + "/find", want: http.StatusBadRequest},
		{name: "drives", method: http.MethodPost, path: agentPath + "/drives", want: http.StatusOK},
		{name: "usb enum", method: http.MethodPost, path: agentPath + "/usb_enum", want: http.StatusOK},
		{name: "transfer list", method: http.MethodGet, path: agentPath + "/transfers", want: http.StatusOK},
		{name: "transfer detail", method: http.MethodGet, path: agentPath + "/transfers/" + strconv.FormatUint(uint64(f.completedPull.ID), 10), want: http.StatusOK},
		{name: "transfer download", method: http.MethodGet, path: agentPath + "/transfers/" + strconv.FormatUint(uint64(f.completedPull.ID), 10) + "/file", want: http.StatusOK},
		{name: "resume", method: http.MethodPost, path: agentPath + "/transfers/" + strconv.FormatUint(uint64(f.activePull.ID), 10) + "/resume", contentType: "application/json", body: `{}`, want: http.StatusOK},
	}
	for _, tc := range readCases {
		t.Run("read/"+tc.name, func(t *testing.T) {
			w := filePermissionRequest(t, f.router(t, "file-permission-reader"), "file-permission-reader", tc.method, tc.path, tc.body, tc.contentType)
			if w.Code != tc.want {
				t.Fatalf("files.read status = %d, want %d; body=%s", w.Code, tc.want, w.Body.String())
			}
			w = filePermissionRequest(t, f.router(t, "file-permission-agent-only"), "file-permission-agent-only", tc.method, tc.path, tc.body, tc.contentType)
			if w.Code != http.StatusForbidden {
				t.Fatalf("agents.read status = %d, want 403; body=%s", w.Code, w.Body.String())
			}
		})
	}

	writeCases := []struct {
		name, method, path, body, contentType string
		want                                  int
	}{
		{name: "delete", method: http.MethodPost, path: agentPath + "/files/delete", want: http.StatusBadRequest},
		{name: "mkdir", method: http.MethodPost, path: agentPath + "/files/mkdir", want: http.StatusBadRequest},
		{name: "rename", method: http.MethodPost, path: agentPath + "/files/rename", want: http.StatusBadRequest},
		{name: "chmod", method: http.MethodPost, path: agentPath + "/files/chmod", want: http.StatusBadRequest},
		{name: "write", method: http.MethodPost, path: agentPath + "/files/write", contentType: "application/json", body: `{}`, want: http.StatusBadRequest},
		{name: "download", method: http.MethodPost, path: agentPath + "/download", want: http.StatusBadRequest},
		{name: "download url", method: http.MethodPost, path: agentPath + "/download_url", want: http.StatusBadRequest},
		{name: "file hunt", method: http.MethodPost, path: agentPath + "/file_hunt", contentType: "application/json", body: `{}`, want: http.StatusOK},
		{name: "chunk", method: http.MethodPut, path: missingTransferPath + "/chunk", contentType: "application/json", body: `{}`, want: http.StatusNotFound},
		{name: "pause", method: http.MethodPost, path: missingTransferPath + "/pause", want: http.StatusNotFound},
		{name: "cancel", method: http.MethodDelete, path: missingTransferPath, want: http.StatusNotFound},
	}
	for _, tc := range writeCases {
		t.Run("write/"+tc.name, func(t *testing.T) {
			w := filePermissionRequest(t, f.router(t, "file-permission-writer"), "file-permission-writer", tc.method, tc.path, tc.body, tc.contentType)
			if w.Code != tc.want {
				t.Fatalf("files.write status = %d, want %d; body=%s", w.Code, tc.want, w.Body.String())
			}
			w = filePermissionRequest(t, f.router(t, "file-permission-reader"), "file-permission-reader", tc.method, tc.path, tc.body, tc.contentType)
			if w.Code != http.StatusForbidden {
				t.Fatalf("files.read status = %d, want 403; body=%s", w.Code, w.Body.String())
			}
		})
	}

	t.Run("agent scoped file task status and output", func(t *testing.T) {
		params := "/agents/" + f.agent.ID + "/tasks/" + strconv.FormatUint(uint64(f.readTask.ID), 10)
		for _, suffix := range []string{"", "/output"} {
			w := filePermissionRequest(t, f.router(t, "file-permission-reader"), "file-permission-reader", http.MethodGet, params+suffix, "", "")
			if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "FILE-CONTENT-MARKER") {
				t.Fatalf("file-only task detail denied: status=%d body=%s", w.Code, w.Body.String())
			}
			w = filePermissionRequest(t, f.router(t, "file-permission-agent-only"), "file-permission-agent-only", http.MethodGet, params+suffix, "", "")
			if w.Code != http.StatusForbidden {
				t.Fatalf("agent-only file task detail = %d, want 403; body=%s", w.Code, w.Body.String())
			}
		}
		shellPath := "/agents/" + f.agent.ID + "/tasks/" + strconv.FormatUint(uint64(f.shellTask.ID), 10)
		w := filePermissionRequest(t, f.router(t, "file-permission-reader"), "file-permission-reader", http.MethodGet, shellPath, "", "")
		if w.Code != http.StatusForbidden {
			t.Fatalf("file-only shell task detail = %d, want 403; body=%s", w.Code, w.Body.String())
		}
		w = filePermissionRequest(t, f.router(t, "file-permission-task-reader"), "file-permission-task-reader", http.MethodGet, shellPath, "", "")
		if w.Code != http.StatusOK {
			t.Fatalf("task-reader shell task detail = %d, want 200; body=%s", w.Code, w.Body.String())
		}
	})
}

func TestFileTransferDirectionPermissions(t *testing.T) {
	f := newFilePermissionFixture(t)
	agentPath := "/agents/" + f.agent.ID
	readerRouter := f.router(t, "file-permission-reader")
	writerRouter := f.router(t, "file-permission-writer")

	pullBody := `{"direction":"pull","path":"/srv/direction-pull.txt","total_size":4,"filename":"direction-pull.txt"}`
	w := filePermissionRequest(t, readerRouter, "file-permission-reader", http.MethodPost, agentPath+"/transfers", pullBody, "application/json")
	if w.Code != http.StatusOK {
		t.Fatalf("files.read pull create = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	pullID := fileTransferIDFromResponse(t, w)
	w = filePermissionRequest(t, writerRouter, "file-permission-writer", http.MethodPost, agentPath+"/transfers", pullBody, "application/json")
	if w.Code != http.StatusForbidden {
		t.Fatalf("files.write pull create = %d, want 403; body=%s", w.Code, w.Body.String())
	}

	digest := sha256.Sum256([]byte("data"))
	pushBody := fmt.Sprintf(`{"direction":"push","path":"C:\\temp\\direction-push.bin","total_size":4,"sha256":"%s","filename":"direction-push.bin"}`, hex.EncodeToString(digest[:]))
	w = filePermissionRequest(t, writerRouter, "file-permission-writer", http.MethodPost, agentPath+"/transfers", pushBody, "application/json")
	if w.Code != http.StatusOK {
		t.Fatalf("files.write push create = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	pushID := fileTransferIDFromResponse(t, w)
	w = filePermissionRequest(t, readerRouter, "file-permission-reader", http.MethodPost, agentPath+"/transfers", pushBody, "application/json")
	if w.Code != http.StatusForbidden {
		t.Fatalf("files.read push create = %d, want 403; body=%s", w.Code, w.Body.String())
	}

	pullPath := agentPath + "/transfers/" + strconv.FormatUint(uint64(pullID), 10)
	w = filePermissionRequest(t, readerRouter, "file-permission-reader", http.MethodPost, pullPath+"/resume", `{}`, "application/json")
	if w.Code != http.StatusOK {
		t.Fatalf("files.read pull resume = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	w = filePermissionRequest(t, writerRouter, "file-permission-writer", http.MethodPost, pullPath+"/resume", `{}`, "application/json")
	if w.Code != http.StatusForbidden {
		t.Fatalf("files.write pull resume = %d, want 403; body=%s", w.Code, w.Body.String())
	}
	pushPath := agentPath + "/transfers/" + strconv.FormatUint(uint64(pushID), 10)
	w = filePermissionRequest(t, readerRouter, "file-permission-reader", http.MethodPost, pushPath+"/resume", `{}`, "application/json")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("push resume = %d, want 400; body=%s", w.Code, w.Body.String())
	}

	chunkBody := `{"offset":0,"data_base64":"` + base64.StdEncoding.EncodeToString([]byte("data")) + `","final":true}`
	w = filePermissionRequest(t, readerRouter, "file-permission-reader", http.MethodPut, pushPath+"/chunk", chunkBody, "application/json")
	if w.Code != http.StatusForbidden {
		t.Fatalf("files.read push chunk = %d, want 403; body=%s", w.Code, w.Body.String())
	}
	w = filePermissionRequest(t, writerRouter, "file-permission-writer", http.MethodPut, pushPath+"/chunk", chunkBody, "application/json")
	if w.Code != http.StatusOK {
		t.Fatalf("files.write push chunk = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	w = filePermissionRequest(t, readerRouter, "file-permission-reader", http.MethodPost, pushPath+"/pause", `{}`, "application/json")
	if w.Code != http.StatusForbidden {
		t.Fatalf("files.read pause = %d, want 403; body=%s", w.Code, w.Body.String())
	}
	w = filePermissionRequest(t, writerRouter, "file-permission-writer", http.MethodPost, pushPath+"/pause", `{}`, "application/json")
	if w.Code != http.StatusOK {
		t.Fatalf("files.write pause = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	w = filePermissionRequest(t, readerRouter, "file-permission-reader", http.MethodDelete, pushPath, "", "")
	if w.Code != http.StatusForbidden {
		t.Fatalf("files.read cancel = %d, want 403; body=%s", w.Code, w.Body.String())
	}
	w = filePermissionRequest(t, writerRouter, "file-permission-writer", http.MethodDelete, pushPath, "", "")
	if w.Code != http.StatusOK {
		t.Fatalf("files.write cancel = %d, want 200; body=%s", w.Code, w.Body.String())
	}

	completedPath := agentPath + "/transfers/" + strconv.FormatUint(uint64(f.completedPull.ID), 10) + "/file"
	w = filePermissionRequest(t, readerRouter, "file-permission-reader", http.MethodGet, completedPath, "", "")
	if w.Code != http.StatusOK || w.Body.String() != "pulled bytes\n" {
		t.Fatalf("files.read transfer download = %d %q", w.Code, w.Body.String())
	}
	w = filePermissionRequest(t, writerRouter, "file-permission-writer", http.MethodGet, completedPath, "", "")
	if w.Code != http.StatusForbidden {
		t.Fatalf("files.write transfer download = %d, want 403; body=%s", w.Code, w.Body.String())
	}
}

func TestFileOnlyRoleCanBrowseAndPull(t *testing.T) {
	f := newFilePermissionFixture(t)
	username := "file-permission-reader"
	router := f.router(t, username)
	agentPath := "/agents/" + f.agent.ID

	for _, pagePath := range []string{agentPath + "/files?format=json"} {
		w := filePermissionRequest(t, router, username, http.MethodGet, pagePath, "", "")
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"os":"windows"`) {
			t.Fatalf("file-only page = %d, want OS metadata; body=%s", w.Code, w.Body.String())
		}
	}
	form := url.Values{"path": {`C:\private.txt`}}
	w := filePermissionRequest(t, router, username, http.MethodPost, agentPath+"/files/read", form.Encode(), "application/x-www-form-urlencoded")
	if w.Code != http.StatusOK {
		t.Fatalf("file-only read = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	var queued struct {
		Data struct {
			TaskID uint `json:"task_id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &queued); err != nil || queued.Data.TaskID == 0 {
		t.Fatalf("file-only read did not queue task: %s (%v)", w.Body.String(), err)
	}
	if err := f.server.db.Model(&db.Task{}).Where("id = ?", queued.Data.TaskID).Updates(map[string]any{"status": "completed", "result": "FILE-CONTENT-MARKER"}).Error; err != nil {
		t.Fatalf("complete read task: %v", err)
	}
	taskPath := agentPath + "/tasks/" + strconv.FormatUint(uint64(queued.Data.TaskID), 10)
	for _, suffix := range []string{"", "/output"} {
		w = filePermissionRequest(t, router, username, http.MethodGet, taskPath+suffix, "", "")
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "FILE-CONTENT-MARKER") {
			t.Fatalf("file-only task poll denied: status=%d body=%s", w.Code, w.Body.String())
		}
	}

	pullBody := `{"direction":"pull","path":"/srv/file-only.txt","total_size":4,"filename":"file-only.txt"}`
	w = filePermissionRequest(t, router, username, http.MethodPost, agentPath+"/transfers", pullBody, "application/json")
	if w.Code != http.StatusOK {
		t.Fatalf("file-only pull create = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	pullID := fileTransferIDFromResponse(t, w)
	pullPath := agentPath + "/transfers/" + strconv.FormatUint(uint64(pullID), 10)
	w = filePermissionRequest(t, router, username, http.MethodPost, pullPath+"/resume", `{}`, "application/json")
	if w.Code != http.StatusOK {
		t.Fatalf("file-only pull resume = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	w = filePermissionRequest(t, router, username, http.MethodGet, agentPath+"/transfers/"+strconv.FormatUint(uint64(f.completedPull.ID), 10)+"/file", "", "")
	if w.Code != http.StatusOK || w.Body.String() != "pulled bytes\n" {
		t.Fatalf("file-only completed pull download = %d %q", w.Code, w.Body.String())
	}

	for _, tc := range []struct{ method, path, body string }{
		{http.MethodPost, agentPath + "/files/write", `{}`},
		{http.MethodPost, agentPath + "/files/delete", ""},
		{http.MethodPost, agentPath + "/transfers", `{"direction":"push","path":"C:\\temp\\denied.bin","total_size":4,"sha256":"` + strings.Repeat("0", 64) + `"}`},
	} {
		w = filePermissionRequest(t, router, username, tc.method, tc.path, tc.body, "application/json")
		if w.Code != http.StatusForbidden {
			t.Fatalf("file-only write %s %s = %d, want 403; body=%s", tc.method, tc.path, w.Code, w.Body.String())
		}
	}

	agentOnly := f.router(t, "file-permission-agent-only")
	w = filePermissionRequest(t, agentOnly, "file-permission-agent-only", http.MethodGet, agentPath+"/files?format=json", "", "")
	if w.Code != http.StatusForbidden {
		t.Fatalf("agent-only files page = %d, want 403; body=%s", w.Code, w.Body.String())
	}
	w = filePermissionRequest(t, agentOnly, "file-permission-agent-only", http.MethodPost, agentPath+"/files/ls", "", "")
	if w.Code != http.StatusForbidden {
		t.Fatalf("agent-only ls = %d, want 403; body=%s", w.Code, w.Body.String())
	}

	foreignPath := "/agents/" + f.foreign.ID
	for _, tc := range []struct{ method, path, body, contentType string }{
		{http.MethodGet, foreignPath + "/files?format=json", "", ""},
		{http.MethodPost, foreignPath + "/files/ls", "", ""},
		{http.MethodPost, foreignPath + "/files/read", form.Encode(), "application/x-www-form-urlencoded"},
		{http.MethodGet, foreignPath + "/transfers", "", ""},
		{http.MethodGet, foreignPath + "/transfers/" + strconv.FormatUint(uint64(f.completedPull.ID), 10), "", ""},
	} {
		w = filePermissionRequest(t, router, username, tc.method, tc.path, tc.body, tc.contentType)
		if w.Code != http.StatusNotFound {
			t.Fatalf("cross-tenant %s %s = %d, want 404; body=%s", tc.method, tc.path, w.Code, w.Body.String())
		}
	}
}
