package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/r0si/sovereign/internal/config"
	forgecrypto "github.com/r0si/sovereign/internal/crypto"
	"github.com/r0si/sovereign/internal/db"
	"github.com/r0si/sovereign/internal/testutil"
	"gorm.io/gorm"
)

func newTransferServer(t *testing.T, database *gorm.DB, dataDir string) *Server {
	t.Helper()
	cfg := config.DefaultConfig()
	cfg.Server.BeaconKey = strings.Repeat("ab", 32)
	cfg.Server.DataDir = dataDir
	return &Server{
		db:                database,
		cfg:               cfg,
		fileChains:        newFileChainState(),
		wsClients:         make(map[*websocket.Conn]*wsClientConn),
		agentPendingTasks: make(map[string]int),
		ctx:               context.Background(),
		metrics:           &MetricsCollector{TasksTotal: prometheus.NewCounter(prometheus.CounterOpts{})},
	}
}

func transferTestSetup(t *testing.T) (*gorm.DB, *Server, db.Implant, string) {
	t.Helper()
	forgecrypto.InitLootEncryption(testStorageKeyHex)
	database := testutil.SetupTestDB(t)
	dataDir := t.TempDir()
	s := newTransferServer(t, database, dataDir)
	agent := db.Implant{ID: uuid.New().String(), Hostname: "TRANSFER-TEST", Username: "operator", OS: "windows"}
	if err := database.Create(&agent).Error; err != nil {
		t.Fatalf("seed agent: %v", err)
	}
	return database, s, agent, dataDir
}

type transferEnvelope struct {
	Success bool   `json:"success"`
	Error   string `json:"error"`
	Data    struct {
		Transfer db.FileTransfer `json:"transfer"`
		TaskID   uint            `json:"task_id"`
	} `json:"data"`
}

func transferRequest(t *testing.T, handler gin.HandlerFunc, agentID, transferID string, body any) transferEnvelope {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(raw))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Params = gin.Params{{Key: "id", Value: agentID}}
	if transferID != "" {
		c.Params = append(c.Params, gin.Param{Key: "transfer_id", Value: transferID})
	}
	c.Set("user_role", "admin")
	c.Set("user", "transfer-operator")
	handler(c)
	var env transferEnvelope
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode response %s: %v", w.Body.String(), err)
	}
	if !env.Success {
		t.Fatalf("request failed (%d): %s", w.Code, w.Body.String())
	}
	return env
}

func transferRequestStatus(t *testing.T, handler gin.HandlerFunc, agentID, transferID string, body any) int {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(raw))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Params = gin.Params{{Key: "id", Value: agentID}}
	if transferID != "" {
		c.Params = append(c.Params, gin.Param{Key: "transfer_id", Value: transferID})
	}
	c.Set("user_role", "admin")
	c.Set("user", "transfer-operator")
	handler(c)
	return w.Code
}

func transferChunkMACFor(t *testing.T, s *Server, agentID, prev string, data []byte) string {
	t.Helper()
	key := s.fileChainKey(agentID)
	if key == nil {
		t.Fatal("no file chain key")
	}
	prevBytes, err := hex.DecodeString(prev)
	if err != nil || len(prevBytes) != 32 {
		t.Fatalf("bad prev mac: %v", err)
	}
	return hex.EncodeToString(forgecrypto.FileChunkMAC(key, prevBytes, data))
}

func reloadTransfer(t *testing.T, database *gorm.DB, id uint) db.FileTransfer {
	t.Helper()
	var ft db.FileTransfer
	if err := database.First(&ft, id).Error; err != nil {
		t.Fatalf("reload transfer: %v", err)
	}
	return ft
}

func transferChunkResult(taskID uint, data []byte, offset int64, mac, rid string, final bool) taskResult {
	sum := sha256.Sum256(data)
	return taskResult{
		TaskID: taskID, Type: "upload", Output: base64.StdEncoding.EncodeToString(data),
		Encoding: "base64", Offset: offset, Size: int64(len(data)), MAC: mac,
		ResultID: rid, Final: final, SHA256: hex.EncodeToString(sum[:]),
	}
}

func TestFileTransferPushResumeAfterRestart(t *testing.T) {
	database, s, agent, dataDir := transferTestSetup(t)
	payload := make([]byte, FileTransferChunkSize+777)
	for i := range payload {
		payload[i] = byte(i % 251)
	}
	whole := sha256.Sum256(payload)
	env := transferRequest(t, s.handleCreateTransfer, agent.ID, "", map[string]any{
		"direction": "push", "path": "C:\\temp\\payload.bin", "total_size": len(payload),
		"sha256": hex.EncodeToString(whole[:]), "filename": "payload.bin",
	})
	ft := env.Data.Transfer
	if ft.ID == 0 || ft.Status != transferStatusActive || ft.ChunkSize != FileTransferChunkSize {
		t.Fatalf("bad session: %+v", ft)
	}
	for i, off := range []int{0, FileTransferChunkSize} {
		end := off + FileTransferChunkSize
		if end > len(payload) {
			end = len(payload)
		}
		chunk := payload[off:end]
		final := end == len(payload)
		if i == 1 {
			// Simulate a service restart with the same DB and data directory.
			s = newTransferServer(t, database, dataDir)
		}
		current := reloadTransfer(t, database, ft.ID)
		mac := transferChunkMACFor(t, s, agent.ID, current.PrevMAC, chunk)
		queued := transferRequest(t, s.handlePushTransferChunk, agent.ID, fmt.Sprintf("%d", ft.ID), map[string]any{
			"offset": off, "data_base64": base64.StdEncoding.EncodeToString(chunk), "final": final,
		})
		if queued.Data.TaskID == 0 {
			t.Fatal("no task id for push chunk")
		}
		r := transferChunkResult(queued.Data.TaskID, chunk, int64(off), mac, fmt.Sprintf("push-rid-%d", i), final)
		s.processTaskResults(agent, []taskResult{r}, agent.ID, time.Now())
		current = reloadTransfer(t, database, ft.ID)
		if current.CommittedOffset != int64(end) {
			t.Fatalf("after chunk %d offset = %d, want %d", i, current.CommittedOffset, end)
		}
	}
	done := reloadTransfer(t, database, ft.ID)
	if done.Status != transferStatusCompleted || done.CurrentTaskID != nil {
		t.Fatalf("session not completed: %+v", done)
	}
	var ledger []db.FileTransferChunk
	if err := database.Where("transfer_id = ?", ft.ID).Order(`"offset" ASC`).Find(&ledger).Error; err != nil || len(ledger) != 2 {
		t.Fatalf("ledger: %v, %+v", err, ledger)
	}
	digest, err := s.pushTransferSHA256(database, ft.ID, int64(len(payload)))
	if err != nil || digest != hex.EncodeToString(whole[:]) {
		t.Fatalf("pushed digest = %s, err = %v", digest, err)
	}
	if _, err := os.Stat(transferPartPath(filepath.Join(dataDir, "transfers"), ft.ID)); !os.IsNotExist(err) {
		t.Fatalf("push should not leave a .part file: %v", err)
	}
}

func TestFileTransferPullResumeAfterRestart(t *testing.T) {
	database, s, agent, dataDir := transferTestSetup(t)
	payload := []byte(strings.Repeat("pull-bytes-", FileTransferChunkSize/11+1))
	whole := sha256.Sum256(payload)
	env := transferRequest(t, s.handleCreateTransfer, agent.ID, "", map[string]any{
		"direction": "pull", "path": "/srv/remote/report.txt", "total_size": len(payload),
		"sha256": hex.EncodeToString(whole[:]), "filename": "report.txt",
	})
	ft := env.Data.Transfer
	for i, off := range []int{0, FileTransferChunkSize} {
		end := off + FileTransferChunkSize
		if end > len(payload) {
			end = len(payload)
		}
		chunk := payload[off:end]
		final := end == len(payload)
		if i == 1 {
			s = newTransferServer(t, database, dataDir)
		}
		current := reloadTransfer(t, database, ft.ID)
		mac := transferChunkMACFor(t, s, agent.ID, current.PrevMAC, chunk)
		resumed := transferRequest(t, s.handleResumeTransfer, agent.ID, fmt.Sprintf("%d", ft.ID), map[string]any{})
		var task db.Task
		if err := database.First(&task, resumed.Data.TaskID).Error; err != nil {
			t.Fatalf("load pull task: %v", err)
		}
		if task.Direction != "pull" || task.TransferID == nil || task.Offset != current.CommittedOffset {
			t.Fatalf("bad pull task: %+v", task)
		}
		r := taskResult{TaskID: task.ID, Type: "download", Output: base64.StdEncoding.EncodeToString(chunk),
			Encoding: "base64", Offset: current.CommittedOffset, Size: int64(len(chunk)), MAC: mac,
			ResultID: fmt.Sprintf("pull-rid-%d", i), Final: final}
		s.processTaskResults(agent, []taskResult{r}, agent.ID, time.Now())
		current = reloadTransfer(t, database, ft.ID)
		if current.CommittedOffset != int64(end) {
			var freshTask db.Task
			_ = database.First(&freshTask, task.ID).Error
			t.Fatalf("after pull chunk %d offset = %d, want %d; session status=%s err=%q; task status=%s err=%q", i,
				current.CommittedOffset, end, current.Status, current.Error, freshTask.Status, freshTask.Error)
		}
	}
	done := reloadTransfer(t, database, ft.ID)
	if done.Status != transferStatusCompleted {
		t.Fatalf("pull session not completed: %+v", done)
	}
	finalPath := transferFinalPath(filepath.Join(dataDir, "transfers"), done.Filename, done.ID)
	got, err := os.ReadFile(finalPath)
	if err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("final file: %v (%d bytes)", err, len(got))
	}

	// Range downloads are served from the durable final file.
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/", nil)
	c.Request.Header.Set("Range", "bytes=0-9")
	c.Params = gin.Params{{Key: "id", Value: agent.ID}, {Key: "transfer_id", Value: fmt.Sprintf("%d", ft.ID)}}
	c.Set("user_role", "admin")
	s.handleGetTransferFile(c)
	if w.Code != http.StatusPartialContent || w.Body.String() != string(payload[:10]) {
		t.Fatalf("range response %d: %q", w.Code, w.Body.String())
	}
}

func TestFileTransferRejectsGapOverlapAndDigestMismatch(t *testing.T) {
	database, s, agent, _ := transferTestSetup(t)
	payload := []byte("0123456789abcdef")
	sum := sha256.Sum256(payload)
	env := transferRequest(t, s.handleCreateTransfer, agent.ID, "", map[string]any{
		"direction": "push", "path": "/tmp/gap.bin", "total_size": len(payload), "sha256": hex.EncodeToString(sum[:]),
	})
	ft := env.Data.Transfer
	transferID := fmt.Sprintf("%d", ft.ID)
	first := payload[:8]
	second := payload[8:]

	if code := transferRequestStatus(t, s.handlePushTransferChunk, agent.ID, transferID,
		map[string]any{"offset": 8, "data_base64": base64.StdEncoding.EncodeToString(second)}); code != http.StatusConflict {
		t.Fatalf("gap offset status = %d, want 409", code)
	}
	q := transferRequest(t, s.handlePushTransferChunk, agent.ID, transferID,
		map[string]any{"offset": 0, "data_base64": base64.StdEncoding.EncodeToString(first)})
	if code := transferRequestStatus(t, s.handlePushTransferChunk, agent.ID, transferID,
		map[string]any{"offset": 0, "data_base64": base64.StdEncoding.EncodeToString([]byte("tampered"))}); code != http.StatusConflict {
		t.Fatalf("different data at queued offset status = %d, want 409", code)
	}
	current := reloadTransfer(t, database, ft.ID)
	mac := transferChunkMACFor(t, s, agent.ID, current.PrevMAC, first)
	s.processTaskResults(agent, []taskResult{transferChunkResult(q.Data.TaskID, first, 0, mac, "gap-rid-1", false)}, agent.ID, time.Now())

	// A replay with different bytes must not advance the committed offset.
	if code := transferRequestStatus(t, s.handlePushTransferChunk, agent.ID, transferID,
		map[string]any{"offset": 0, "data_base64": base64.StdEncoding.EncodeToString([]byte("tampered"))}); code != http.StatusConflict {
		t.Fatalf("committed digest mismatch status = %d, want 409", code)
	}
	current = reloadTransfer(t, database, ft.ID)
	if current.CommittedOffset != 8 {
		t.Fatalf("offset = %d, want 8", current.CommittedOffset)
	}

	// An agent result with a bad MAC fails the session and writes no ledger row.
	next := transferRequest(t, s.handlePushTransferChunk, agent.ID, transferID,
		map[string]any{"offset": 8, "data_base64": base64.StdEncoding.EncodeToString(second), "final": true})
	bad := transferChunkResult(next.Data.TaskID, second, 8, strings.Repeat("0", 64), "gap-rid-2", true)
	s.processTaskResults(agent, []taskResult{bad}, agent.ID, time.Now())
	current = reloadTransfer(t, database, ft.ID)
	if current.Status != transferStatusFailed || current.CommittedOffset != 8 {
		t.Fatalf("bad MAC session = %+v", current)
	}
	var count int64
	database.Model(&db.FileTransferChunk{}).Where("transfer_id = ?", ft.ID).Count(&count)
	if count != 1 {
		t.Fatalf("ledger rows = %d, want 1", count)
	}
}

func TestFileTransferEmptyFile(t *testing.T) {
	_, s, agent, dataDir := transferTestSetup(t)
	empty := sha256.Sum256(nil)
	push := transferRequest(t, s.handleCreateTransfer, agent.ID, "", map[string]any{
		"direction": "push", "path": "/tmp/empty.bin", "total_size": 0, "sha256": hex.EncodeToString(empty[:]),
	})
	ft := reloadTransfer(t, s.db, push.Data.Transfer.ID)
	mac := transferChunkMACFor(t, s, agent.ID, ft.PrevMAC, nil)
	q := transferRequest(t, s.handlePushTransferChunk, agent.ID, fmt.Sprintf("%d", ft.ID),
		map[string]any{"offset": 0, "data_base64": "", "final": true})
	s.processTaskResults(agent, []taskResult{transferChunkResult(q.Data.TaskID, nil, 0, mac, "empty-push", true)}, agent.ID, time.Now())
	if done := reloadTransfer(t, s.db, ft.ID); done.Status != transferStatusCompleted {
		t.Fatalf("empty push not completed: %+v", done)
	}

	pull := transferRequest(t, s.handleCreateTransfer, agent.ID, "", map[string]any{
		"direction": "pull", "path": "/tmp/empty-pull.txt", "total_size": 0, "filename": "empty.txt",
	})
	ft = pull.Data.Transfer
	resumed := transferRequest(t, s.handleResumeTransfer, agent.ID, fmt.Sprintf("%d", ft.ID), map[string]any{})
	var task db.Task
	if err := s.db.First(&task, resumed.Data.TaskID).Error; err != nil {
		t.Fatal(err)
	}
	current := reloadTransfer(t, s.db, ft.ID)
	pullMAC := transferChunkMACFor(t, s, agent.ID, current.PrevMAC, nil)
	r := taskResult{TaskID: task.ID, Type: "download", Output: "", Offset: 0, Size: 0,
		MAC: pullMAC, ResultID: "empty-pull", Final: true, SHA256: hex.EncodeToString(empty[:])}
	s.processTaskResults(agent, []taskResult{r}, agent.ID, time.Now())
	done := reloadTransfer(t, s.db, ft.ID)
	if done.Status != transferStatusCompleted {
		t.Fatalf("empty pull not completed: %+v", done)
	}
	info, err := os.Stat(transferFinalPath(filepath.Join(dataDir, "transfers"), done.Filename, done.ID))
	if err != nil || info.Size() != 0 {
		t.Fatalf("empty final file: %v, %+v", err, info)
	}
}

func TestFileTransferPauseCancelAndTenantIsolation(t *testing.T) {
	database, s, agent, dataDir := transferTestSetup(t)
	env := transferRequest(t, s.handleCreateTransfer, agent.ID, "", map[string]any{
		"direction": "pull", "path": "/tmp/pause.txt", "total_size": 20,
	})
	ft := env.Data.Transfer
	transferID := fmt.Sprintf("%d", ft.ID)
	queued := transferRequest(t, s.handleResumeTransfer, agent.ID, transferID, map[string]any{})
	var task db.Task
	if err := database.First(&task, queued.Data.TaskID).Error; err != nil {
		t.Fatal(err)
	}
	transferRequest(t, s.handlePauseTransfer, agent.ID, transferID, map[string]any{})
	paused := reloadTransfer(t, database, ft.ID)
	if paused.Status != transferStatusPaused || paused.CurrentTaskID != nil {
		t.Fatalf("paused session = %+v", paused)
	}
	if err := database.First(&task, task.ID).Error; err != nil {
		t.Fatal(err)
	}
	if task.Status != "cancelled" {
		t.Fatalf("paused task status = %s", task.Status)
	}

	// A result arriving after pause must not advance the committed offset.
	payload := []byte("0123456789abcdefghij")
	mac := transferChunkMACFor(t, s, agent.ID, paused.PrevMAC, payload[:8])
	s.processTaskResults(agent, []taskResult{{TaskID: task.ID, Type: "download", Output: base64.StdEncoding.EncodeToString(payload[:8]),
		Offset: 0, Size: 8, MAC: mac, ResultID: "late-after-pause"}}, agent.ID, time.Now())
	if after := reloadTransfer(t, database, ft.ID); after.CommittedOffset != 0 {
		t.Fatalf("late result advanced offset to %d", after.CommittedOffset)
	}

	transferRequest(t, s.handleCancelTransfer, agent.ID, transferID, map[string]any{})
	if cancelled := reloadTransfer(t, database, ft.ID); cancelled.Status != transferStatusCancelled {
		t.Fatalf("cancel status = %s", cancelled.Status)
	}
	if _, err := os.Stat(transferPartPath(filepath.Join(dataDir, "transfers"), ft.ID)); !os.IsNotExist(err) {
		t.Fatalf("cancel left .part behind: %v", err)
	}

	// A foreign tenant cannot see, pause, cancel or download the session.
	if err := database.Create(&db.User{Username: "foreign", TenantID: 77, Role: db.RoleAdmin}).Error; err != nil {
		t.Fatalf("seed foreign user: %v", err)
	}
	for _, handler := range []gin.HandlerFunc{s.handleGetTransfer, s.handlePauseTransfer, s.handleCancelTransfer, s.handleGetTransferFile} {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, "/", nil)
		c.Params = gin.Params{{Key: "id", Value: agent.ID}, {Key: "transfer_id", Value: transferID}}
		c.Set("user_role", "admin")
		c.Set("user", "foreign")
		handler(c)
		if w.Code != http.StatusNotFound {
			t.Fatalf("foreign tenant status = %d, want 404: %s", w.Code, w.Body.String())
		}
	}
}

func TestFileTransferReconciliation(t *testing.T) {
	database, s, agent, dataDir := transferTestSetup(t)
	dir := filepath.Join(dataDir, "transfers")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	now := time.Now()

	// Oversized .part is truncated to the confirmed offset.
	pull := transferRequest(t, s.handleCreateTransfer, agent.ID, "", map[string]any{
		"direction": "pull", "path": "/tmp/reconcile.txt", "total_size": 10,
	})
	ft := pull.Data.Transfer
	database.Model(&db.FileTransfer{}).Where("id = ?", ft.ID).Updates(map[string]any{"committed_offset": 5})
	if err := os.WriteFile(transferPartPath(dir, ft.ID), []byte("0123456789"), 0600); err != nil {
		t.Fatal(err)
	}
	s.reconcileFileTransfers()
	info, err := os.Stat(transferPartPath(dir, ft.ID))
	if err != nil || info.Size() != 5 {
		t.Fatalf("truncated .part: %v, %+v", err, info)
	}

	// Completed session with a complete .part regains its final file.
	done := transferRequest(t, s.handleCreateTransfer, agent.ID, "", map[string]any{
		"direction": "pull", "path": "/tmp/repair.txt", "total_size": 5, "filename": "repair.txt",
	}).Data.Transfer
	database.Model(&db.FileTransfer{}).Where("id = ?", done.ID).Updates(map[string]any{
		"status": transferStatusCompleted, "committed_offset": 5, "completed_at": now,
	})
	if err := os.WriteFile(transferPartPath(dir, done.ID), []byte("hello"), 0600); err != nil {
		t.Fatal(err)
	}
	s.reconcileFileTransfers()
	got, err := os.ReadFile(transferFinalPath(dir, done.Filename, done.ID))
	if err != nil || string(got) != "hello" {
		t.Fatalf("repaired final file: %v %q", err, got)
	}

	// A completed push task whose session update was lost is replayed once.
	pushDigest := sha256.Sum256([]byte("data"))
	pushResponse := transferRequest(t, s.handleCreateTransfer, agent.ID, "", map[string]any{
		"direction": "push", "path": "/tmp/replay.bin", "total_size": 4,
		"sha256": hex.EncodeToString(pushDigest[:]),
	})
	push := reloadTransfer(t, database, pushResponse.Data.Transfer.ID)
	chunk := []byte("data")
	mac := transferChunkMACFor(t, s, agent.ID, push.PrevMAC, chunk)
	q := transferRequest(t, s.handlePushTransferChunk, agent.ID, fmt.Sprintf("%d", push.ID),
		map[string]any{"offset": 0, "data_base64": base64.StdEncoding.EncodeToString(chunk), "final": true})
	taskID := q.Data.TaskID
	database.Model(&db.Task{}).Where("id = ?", taskID).Updates(map[string]any{"status": "completed", "prev_mac": push.PrevMAC, "mac": mac})
	database.Model(&db.FileTransfer{}).Where("id = ?", push.ID).Updates(map[string]any{"current_task_id": taskID})
	s.reconcileFileTransfers()
	replayed := reloadTransfer(t, database, push.ID)
	if replayed.CommittedOffset != 4 || replayed.Status != transferStatusCompleted {
		t.Fatalf("replayed push = %+v", replayed)
	}
	var count int64
	database.Model(&db.FileTransferChunk{}).Where("transfer_id = ?", push.ID).Count(&count)
	if count != 1 {
		t.Fatalf("replay ledger rows = %d", count)
	}
	s.reconcileFileTransfers()
	database.Model(&db.FileTransferChunk{}).Where("transfer_id = ?", push.ID).Count(&count)
	if count != 1 {
		t.Fatalf("second replay duplicated ledger: %d", count)
	}
}

func TestP2PTransferUsesDurableCommit(t *testing.T) {
	database, s, child, dataDir := transferTestSetup(t)
	parent := db.Implant{ID: uuid.New().String(), Hostname: "PARENT-TEST", OS: "windows"}
	if err := database.Create(&parent).Error; err != nil {
		t.Fatalf("seed parent: %v", err)
	}
	child.ParentID = parent.ID
	if err := database.Save(&child).Error; err != nil {
		t.Fatalf("bind child: %v", err)
	}
	chunk := []byte(strings.Repeat("relayed-chunk-", 40))
	env := transferRequest(t, s.handleCreateTransfer, child.ID, "", map[string]any{
		"direction": "pull", "path": "/tmp/relay.txt", "total_size": len(chunk), "filename": "relay.txt",
	})
	ft := env.Data.Transfer
	resumed := transferRequest(t, s.handleResumeTransfer, child.ID, fmt.Sprintf("%d", ft.ID), map[string]any{})
	mac := transferChunkMACFor(t, s, child.ID, reloadTransfer(t, database, ft.ID).PrevMAC, chunk)
	r := taskResult{TaskID: resumed.Data.TaskID, Type: "download", Output: base64.StdEncoding.EncodeToString(chunk),
		Encoding: "base64", Offset: 0, Size: int64(len(chunk)), MAC: mac, ResultID: "p2p-transfer", Final: true}
	s.processRelayedResults([]relayedData{{AgentID: child.ID, Results: []taskResult{r}}}, parent.ID, time.Now())

	done := reloadTransfer(t, database, ft.ID)
	if done.Status != transferStatusCompleted || done.CommittedOffset != int64(len(chunk)) {
		t.Fatalf("relayed transfer not committed: %+v", done)
	}
	part := transferPartPath(filepath.Join(dataDir, "transfers"), ft.ID)
	if _, err := os.Stat(part); !os.IsNotExist(err) {
		t.Fatalf("final pull should have renamed .part: %v", err)
	}
	final, err := os.ReadFile(transferFinalPath(filepath.Join(dataDir, "transfers"), done.Filename, done.ID))
	if err != nil || !bytes.Equal(final, chunk) {
		t.Fatalf("relayed final file: %v (%d bytes)", err, len(final))
	}
	var task db.Task
	if err := database.First(&task, resumed.Data.TaskID).Error; err != nil {
		t.Fatal(err)
	}
	if strings.Contains(task.Result, base64.StdEncoding.EncodeToString(chunk)) {
		t.Fatal("relayed transfer chunk leaked into Task.Result")
	}
}
