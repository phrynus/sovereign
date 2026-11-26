package server

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/r0si/sovereign/internal/crypto"
	"github.com/r0si/sovereign/internal/db"
	"gorm.io/gorm"
)

// Durable transfer limits. FileTransferChunkSize is the raw byte cap for one
// transfer task: its base64 form (~2.8 MiB) fits comfortably inside the C
// implant's 4 MiB JSON result cap. MaxTransferChunkSize remains the wider cap
// for legacy, non-session file tasks.
const (
	FileTransferChunkSize = 2 << 20
	MaxTransferBytes      = 50 << 20
	// TransferChunkJSONLimit covers one 2 MiB base64 chunk plus JSON framing.
	// The chunk route needs this instead of MaxJSONBodySize.
	TransferChunkJSONLimit = (FileTransferChunkSize+2)/3*4 + 8192

	transferReservationTTL = 30 * time.Second
	transferCommitTTL      = 2 * time.Minute
	transferSessionTTL     = 24 * time.Hour
	transferFinalFileTTL   = 30 * 24 * time.Hour

	transferStatusActive    = "active"
	transferStatusPaused    = "paused"
	transferStatusCompleted = "completed"
	transferStatusFailed    = "failed"
	transferStatusCancelled = "cancelled"
	transferStatusExpired   = "expired"

	transferDirectionPush = "push"
	transferDirectionPull = "pull"
)

var errTransferStale = errors.New("transfer session changed concurrently")

// transferPathKey binds a session to one agent/path pair, independent of the
// direction. The partial unique index on (agent_id, path_key) uses it to keep
// simultaneous push and pull sessions for the same path from corrupting each
// other.
func transferPathKey(agentID, remotePath string) string {
	sum := sha256.Sum256([]byte(agentID + "\x00" + remotePath))
	return hex.EncodeToString(sum[:])
}

// validateTransferRemotePath mirrors the implant's sanitizeWritePath semantics
// without importing package main: clean the path and reject any .. component
// on either separator style.
func validateTransferRemotePath(raw string) (string, error) {
	p := strings.TrimSpace(raw)
	if p == "" {
		return "", errors.New("path required")
	}
	clean := filepath.Clean(p)
	if clean == ".." || clean == "." {
		return "", errors.New("invalid path")
	}
	for _, sep := range []string{string(filepath.Separator), "/", "\\"} {
		for _, part := range strings.Split(clean, sep) {
			if part == ".." {
				return "", errors.New("path traversal rejected")
			}
		}
	}
	return clean, nil
}

// validateTransferFilename accepts only a basename which cannot escape the
// per-transfer directory. An absent filename is derived from the remote path.
func validateTransferFilename(raw, remotePath string) (string, error) {
	name := strings.TrimSpace(raw)
	if name == "" {
		name = filepath.Base(remotePath)
	}
	base := filepath.Base(name)
	if base == "" || base == "." || base == ".." || base == string(filepath.Separator) || strings.ContainsAny(base, "/\\\x00") {
		return "", errors.New("invalid filename")
	}
	safe := sanitizeFilename(base)
	if safe == "" || safe == "download" && base != "download" {
		return "", errors.New("invalid filename")
	}
	return safe, nil
}

func (s *Server) transfersDir() (string, error) {
	dir := s.rawDataDir()
	if dir == "" {
		return "", errNoDataDir
	}
	root := filepath.Join(dir, "transfers")
	if err := os.MkdirAll(root, 0700); err != nil {
		return "", err
	}
	return root, nil
}

func transferPartPath(dir string, transferID uint) string {
	return filepath.Join(dir, fmt.Sprintf("%d.part", transferID))
}

func transferFinalPath(dir, filename string, transferID uint) string {
	return filepath.Join(dir, fmt.Sprintf("%d-%s", transferID, filename))
}

func newTransferToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// transferSessionForAgent loads a session by ID, requiring the row to belong
// to exactly this agent and tenant. Foreign IDs are indistinguishable from
// missing ones to the caller.
func (s *Server) transferSessionForAgent(agent db.Implant, transferID uint) (db.FileTransfer, error) {
	var ft db.FileTransfer
	err := s.db.Where("id = ? AND agent_id = ? AND tenant_id = ?", transferID, agent.ID, agent.TenantID).First(&ft).Error
	if err != nil {
		return ft, err
	}
	return ft, nil
}

// reserveTransfer atomically claims the next chunk slot. The reservation is a
// short conditional UPDATE rather than a SELECT-then-UPDATE, so two concurrent
// chunk requests for the same offset cannot both create a task. An expired
// reservation from a crashed caller is reclaimable.
func (s *Server) reserveTransfer(ft *db.FileTransfer, now time.Time) (string, error) {
	token, err := newTransferToken()
	if err != nil {
		return "", err
	}
	res := s.withBusyRetryDB("transfer_reserve", func() *gorm.DB {
		return s.db.Model(&db.FileTransfer{}).
			Where("id = ? AND agent_id = ? AND status IN ? AND committed_offset = ? AND current_task_id IS NULL AND (reservation_token = ? OR reservation_expires_at IS NULL OR reservation_expires_at < ?)",
				ft.ID, ft.AgentID, []string{transferStatusActive, transferStatusPaused}, ft.CommittedOffset, "", now).
			Updates(map[string]any{
				"reservation_token":      token,
				"reservation_expires_at": now.Add(transferReservationTTL),
				"updated_at":             now,
			})
	})
	if res.Error != nil {
		return "", res.Error
	}
	if res.RowsAffected != 1 {
		return "", errTransferStale
	}
	return token, nil
}

// bindTransferTask attaches the newly created task to its reservation. The
// token predicate makes this safe if the reservation expired and was reclaimed
// while createTask was running.
func (s *Server) bindTransferTask(ft db.FileTransfer, token string, taskID uint, now time.Time) error {
	res := s.withBusyRetryDB("transfer_bind", func() *gorm.DB {
		return s.db.Model(&db.FileTransfer{}).
			Where("id = ? AND reservation_token = ?", ft.ID, token).
			Updates(map[string]any{
				"current_task_id":        taskID,
				"reservation_token":      "",
				"reservation_expires_at": nil,
				"updated_at":             now,
			})
	})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected != 1 {
		return errTransferStale
	}
	return nil
}

func (s *Server) releaseTransferReservation(ft db.FileTransfer, token string) {
	if token == "" {
		return
	}
	if err := s.withBusyRetryDB("transfer_release", func() *gorm.DB {
		return s.db.Model(&db.FileTransfer{}).
			Where("id = ? AND reservation_token = ?", ft.ID, token).
			Updates(map[string]any{"reservation_token": "", "reservation_expires_at": nil})
	}).Error; err != nil {
		slog.Warn("Failed to release transfer reservation", "transfer_id", ft.ID, "error", err)
	}
}

// createTransferTask creates the wire task with its signed chain link stored
// in the INSERT (WithFileChain), so a beacon can never claim a half-signed
// task after a restart. Only the operator attribution needs a following UPDATE.
func (s *Server) createTransferTask(agentID string, ft db.FileTransfer, taskType, data string, offset, size int64, direction string, final bool, prevMAC, mac, creator string, now time.Time) (*db.Task, error) {
	opts := []TaskOption{
		WithTransferRef(ft.ID, direction, final),
		WithFileChain(prevMAC, mac),
		// One live task per (session, offset). A repeated chunk/resume request
		// collapses onto the same task instead of writing the offset twice.
		WithIdempotencyKey(fmt.Sprintf("transfer:%d:%d:%s", ft.ID, offset, direction)),
	}
	task, err := s.createTask(agentID, taskType, ft.RemotePath, "", ft.RemotePath, data, offset, size, opts...)
	if err != nil {
		return nil, err
	}
	res := s.withBusyRetryDB("transfer_creator", func() *gorm.DB {
		return s.db.Model(&db.Task{}).Where("id = ?", task.ID).
			Updates(map[string]any{"created_by": creator})
	})
	if res.Error != nil {
		s.cancelTransferTask(agentID, task.ID, creator)
		return nil, res.Error
	}
	task.PrevMAC = prevMAC
	task.MAC = mac
	task.CreatedBy = creator
	task.UpdatedAt = now
	return task, nil
}

// cancelTransferTask is the local transfer-cancel primitive. It flips a live
// task to cancelled and releases exactly one pending slot; the abort-frame
// injection of handleCancelTask is intentionally not duplicated here.
func (s *Server) cancelTransferTask(agentID string, taskID uint, actor string) {
	if taskID == 0 {
		return
	}
	now := time.Now()
	res := s.withBusyRetryDB("transfer_cancel", func() *gorm.DB {
		return s.db.Model(&db.Task{}).
			Where("id = ? AND agent_id = ? AND status IN ?", taskID, agentID,
				[]string{"pending", TaskStatusPendingApproval, "running", "sent"}).
			Updates(map[string]any{"status": "cancelled", "error": "cancelled by transfer", "updated_at": now})
	})
	if res.Error != nil {
		slog.Error("Failed to cancel transfer task", "task_id", taskID, "error", res.Error)
		return
	}
	if res.RowsAffected != 1 {
		return
	}
	s.decPendingTasks(agentID)
	var task db.Task
	if err := s.db.First(&task, taskID).Error; err == nil {
		task.Status = "cancelled"
		task.Error = "cancelled by transfer"
		s.broadcastTaskUpdate(agentID, task)
	}
	s.LogAuditRecord(nil, "transfer_task_cancelled", "agent", agentID,
		fmt.Sprintf("cancelled task %d by %s", taskID, actor), true, nil)
}

// lockTransferForCommit takes the same short row lock used by pause/cancel.
// It additionally binds the live task ID and offset so a late result cannot
// advance a session whose task was already cancelled.
func (s *Server) lockTransferForCommit(ft db.FileTransfer, taskID uint, offset int64, now time.Time) (string, error) {
	token, err := newTransferToken()
	if err != nil {
		return "", err
	}
	res := s.withBusyRetryDB("transfer_lock", func() *gorm.DB {
		return s.db.Model(&db.FileTransfer{}).
			Where("id = ? AND agent_id = ? AND status IN ? AND current_task_id = ? AND committed_offset = ? AND (reservation_token = ? OR reservation_expires_at IS NULL OR reservation_expires_at < ?)",
				ft.ID, ft.AgentID, []string{transferStatusActive, transferStatusPaused}, taskID, offset, "", now).
			Updates(map[string]any{
				"reservation_token":      token,
				"reservation_expires_at": now.Add(transferCommitTTL),
				"updated_at":             now,
			})
	})
	if res.Error != nil {
		return "", res.Error
	}
	if res.RowsAffected != 1 {
		return "", errTransferStale
	}
	return token, nil
}

// transferChunkMAC computes the next chain link from the session's durable
// PrevMAC. Restarting the server therefore cannot lose the chain state.
func (s *Server) transferChunkMAC(agentID, prevMACHex string, data []byte) (string, []byte, error) {
	key := s.fileChainKey(agentID)
	if key == nil {
		return "", nil, errors.New("cannot derive file chain key")
	}
	prev, err := hex.DecodeString(prevMACHex)
	if err != nil || len(prev) != 32 {
		return "", nil, errors.New("invalid previous chain MAC")
	}
	mac := crypto.FileChunkMAC(key, prev, data)
	return hex.EncodeToString(mac), mac, nil
}

// pushTransferSHA256 rebuilds the whole-file digest from the signed task
// payloads. Push sessions never keep file bytes on the teamserver, so the
// tasks are the only durable source for the final hash comparison.
func (s *Server) pushTransferSHA256(tx *gorm.DB, transferID uint, expected int64) (string, error) {
	var tasks []db.Task
	if err := tx.Where("transfer_id = ? AND type = ?", transferID, "upload").
		Order(`"offset" ASC`).Find(&tasks).Error; err != nil {
		return "", err
	}
	h := sha256.New()
	var seen int64
	for _, t := range tasks {
		if t.Offset != seen {
			return "", fmt.Errorf("push chain gap at offset %d", seen)
		}
		data, err := base64.StdEncoding.DecodeString(t.Data)
		if err != nil {
			return "", fmt.Errorf("push task %d has invalid data: %w", t.ID, err)
		}
		if t.Size > 0 && t.Size != int64(len(data)) {
			return "", fmt.Errorf("push task %d size mismatch", t.ID)
		}
		seen += int64(len(data))
		if seen > expected {
			return "", errors.New("push chain exceeds committed size")
		}
		if _, err := h.Write(data); err != nil {
			return "", err
		}
	}
	if seen != expected {
		return "", fmt.Errorf("push chain short: %d of %d bytes", seen, expected)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// commitTransferChunk validates and durably applies one acknowledged transfer
// chunk. It is called for both direct and relayed results, before the generic
// finality claim. The returned handled flag is false only for tasks that are
// not transfer sessions (the caller then uses the ordinary result path).
func (s *Server) commitTransferChunk(agent db.Implant, r taskResult, task *db.Task, now time.Time) (bool, error) {
	if task.TransferID == nil {
		return false, nil
	}
	transferID := *task.TransferID
	ft, err := s.transferSessionForAgent(agent, transferID)
	if err != nil {
		// The task must not fall through to the legacy file-chunk path when its
		// session disappeared: that would write bytes outside the session.
		return true, err
	}

	offset := r.Offset
	if offset == 0 {
		offset = task.Offset
	}
	taskStatus := "completed"
	var summary, failure string
	var chunkSHA, nextMAC string
	var chunkLen int64
	var final bool
	claimed := false
	// Derive the chain key on the pooled handle BEFORE opening the write
	// transaction: on a single-connection SQLite test DB a nested s.db query
	// would deadlock against our own transaction.
	chainKey := s.fileChainKey(agent.ID)
	lockToken, err := s.lockTransferForCommit(ft, task.ID, offset, now)
	if err != nil {
		if errors.Is(err, errTransferStale) {
			slog.Debug("Late transfer result dropped", "agent_id", agent.ID, "task_id", task.ID, "transfer_id", transferID)
			return true, nil
		}
		return true, err
	}

	txErr := s.db.Transaction(func(tx *gorm.DB) error {
		var session db.FileTransfer
		if err := tx.First(&session, transferID).Error; err != nil {
			return err
		}
		clearLock := map[string]any{
			"reservation_token":      "",
			"reservation_expires_at": nil,
			"updated_at":             now,
		}

		// fail records the terminal state in the SAME transaction as the lock,
		// so no failed session can keep a reservation or an orphaned task.
		fail := func(msg string) error {
			failure = truncateString(msg, 500)
			taskStatus = "failed"
			updates := map[string]any{
				"status":          transferStatusFailed,
				"error":           failure,
				"current_task_id": nil,
			}
			for k, v := range clearLock {
				updates[k] = v
			}
			if err := tx.Model(&db.FileTransfer{}).Where("id = ? AND reservation_token = ?", transferID, lockToken).Updates(updates).Error; err != nil {
				return err
			}
			return s.claimTransferTask(tx, task, r, taskStatus, summary, failure, chunkSHA, now, &claimed)
		}

		if r.Error != "" {
			return fail("agent reported: " + r.Error)
		}
		data, err := base64.StdEncoding.DecodeString(r.Output)
		if err != nil {
			return fail("transfer chunk is not valid base64")
		}
		if len(data) > FileTransferChunkSize {
			return fail(fmt.Sprintf("transfer chunk too large (%d bytes)", len(data)))
		}
		if r.Size > 0 && r.Size != int64(len(data)) {
			return fail("transfer chunk size mismatch")
		}
		if offset != session.CommittedOffset {
			return fail(fmt.Sprintf("unexpected chunk offset %d (committed %d)", offset, session.CommittedOffset))
		}
		if session.TotalSize > 0 && offset+int64(len(data)) > session.TotalSize {
			return fail("transfer chunk exceeds declared size")
		}
		if session.Direction == transferDirectionPush && (task.Final || r.Final) && session.TotalSize > 0 && offset+int64(len(data)) != session.TotalSize {
			return fail("final push chunk does not match declared size")
		}

		prev, decodeErr := hex.DecodeString(session.PrevMAC)
		if chainKey == nil || decodeErr != nil || len(prev) != 32 {
			return fail("invalid transfer chain state")
		}
		nextMAC = hex.EncodeToString(crypto.FileChunkMAC(chainKey, prev, data))
		gotMAC, macErr := hex.DecodeString(r.MAC)
		if macErr != nil || len(gotMAC) != 32 {
			return fail("missing or malformed transfer chunk MAC")
		}
		wantMAC, macErr := hex.DecodeString(nextMAC)
		if macErr != nil || subtle.ConstantTimeCompare(gotMAC, wantMAC) != 1 {
			return fail("transfer chunk HMAC mismatch")
		}

		final = task.Final || r.Final
		if !final && session.TotalSize > 0 && offset+int64(len(data)) >= session.TotalSize {
			final = true
		}
		// An agent that read fewer bytes than requested reached EOF even when
		// the file size was unknown at session creation.
		if !final && session.Direction == transferDirectionPull && r.Size > 0 && int64(len(data)) < r.Size {
			final = true
		}

		sum := sha256.Sum256(data)
		chunkSHA = hex.EncodeToString(sum[:])
		chunkLen = int64(len(data))
		computedSHA := ""
		if session.Direction == transferDirectionPull {
			if err := s.writePullChunk(session, offset, data); err != nil {
				return fail("could not write pull chunk: " + err.Error())
			}
			if final {
				digest, err := s.finishPullFile(session)
				if err != nil {
					return fail("could not finalize pull: " + err.Error())
				}
				computedSHA = digest
			}
		} else if final {
			actual, err := s.pushTransferSHA256(tx, session.ID, offset+chunkLen)
			if err != nil {
				return fail("could not verify pushed file: " + err.Error())
			}
			if !strings.EqualFold(actual, session.SHA256) {
				return fail("pushed file SHA-256 mismatch")
			}
			computedSHA = actual
		}

		ledger := db.FileTransferChunk{
			TransferID: session.ID,
			Offset:     offset,
			Size:       chunkLen,
			SHA256:     chunkSHA,
			MAC:        nextMAC,
			ResultID:   r.ResultID,
			CreatedAt:  now,
		}
		if err := tx.Create(&ledger).Error; err != nil {
			return fail("could not record transfer chunk")
		}

		updates := map[string]any{
			"committed_offset": offset + chunkLen,
			"prev_mac":         nextMAC,
			"current_task_id":  nil,
			"updated_at":       now,
		}
		if final {
			updates["status"] = transferStatusCompleted
			updates["completed_at"] = now
			updates["computed_sha256"] = computedSHA
			if session.TotalSize == 0 {
				updates["total_size"] = offset + chunkLen
			}
		}
		for k, v := range clearLock {
			updates[k] = v
		}
		if err := tx.Model(&db.FileTransfer{}).Where("id = ? AND reservation_token = ?", session.ID, lockToken).Updates(updates).Error; err != nil {
			return err
		}
		if session.Direction == transferDirectionPull {
			summary = fmt.Sprintf("Download chunk saved: %s offset %d (%d bytes)", session.Filename, offset, chunkLen)
		} else {
			summary = fmt.Sprintf("File chunk saved: %s offset %d (%d bytes)", session.Filename, offset, chunkLen)
		}
		return s.claimTransferTask(tx, task, r, taskStatus, summary, "", chunkSHA, now, &claimed)
	})
	if txErr != nil {
		// The lock was taken before the transaction, so a rollback leaves it
		// behind. Clear it now instead of waiting for the reservation TTL.
		s.releaseTransferReservation(ft, lockToken)
		if !errors.Is(txErr, errTransferStale) {
			slog.Error("Failed to commit transfer chunk", "agent_id", agent.ID, "transfer_id", transferID, "task_id", task.ID, "error", txErr)
		}
		return true, txErr
	}
	if !claimed {
		return true, errTransferStale
	}

	task.Status = taskStatus
	task.Result = summary
	task.Error = failure
	task.ResultSHA256 = chunkSHA
	task.LastResultID = r.ResultID
	task.AcknowledgedAt = &now
	task.UpdatedAt = now
	s.decrementPendingTasks(task.AgentID)
	s.fileChains.reset(task.ID)
	s.broadcastTaskUpdate(task.AgentID, *task)
	s.LogAuditRecord(nil, "transfer_chunk_commit", "agent", task.AgentID,
		fmt.Sprintf("transfer %d chunk at %d (%d bytes) status=%s final=%t", transferID, offset, chunkLen, taskStatus, final),
		taskStatus == "completed", nil)
	return true, nil
}

// claimTransferTask performs the first-final-wins task update inside the
// transfer transaction. Result/Error are encrypted with the same helper the
// ordinary result path uses; the in-memory task stays plaintext for WS/audit.
func (s *Server) claimTransferTask(tx *gorm.DB, task *db.Task, r taskResult, status, summary, failure, chunkSHA string, now time.Time, claimed *bool) error {
	stored := *task
	stored.Status = status
	stored.Result = summary
	stored.Error = failure
	if err := s.encryptTaskFieldsOrBlank(&stored); err != nil {
		return err
	}
	res := tx.Model(&db.Task{}).
		Where("id = ? AND agent_id = ? AND status IN ?", task.ID, task.AgentID, []string{"pending", "running", "sent"}).
		Updates(map[string]any{
			"status":          status,
			"result":          stored.Result,
			"error":           stored.Error,
			"result_sha256":   chunkSHA,
			"last_result_id":  r.ResultID,
			"acknowledged_at": now,
			"updated_at":      now,
		})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected != 1 {
		return errTransferStale
	}
	*claimed = true
	return nil
}

// writePullChunk writes raw bytes at the committed offset of the .part file.
// The caller holds the session row lock, so no other chunk can interleave.
func (s *Server) writePullChunk(ft db.FileTransfer, offset int64, data []byte) error {
	dir, err := s.transfersDir()
	if err != nil {
		return err
	}
	f, err := os.OpenFile(transferPartPath(dir, ft.ID), os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if info.Size() != offset {
		return fmt.Errorf("partial file is %d bytes, expected %d", info.Size(), offset)
	}
	if len(data) > 0 {
		if _, err := f.WriteAt(data, offset); err != nil {
			return err
		}
	}
	return f.Sync()
}

// finishPullFile verifies the assembled file and atomically publishes it.
func (s *Server) finishPullFile(ft db.FileTransfer) (string, error) {
	dir, err := s.transfersDir()
	if err != nil {
		return "", err
	}
	part := transferPartPath(dir, ft.ID)
	f, err := os.Open(part)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	size, err := io.Copy(h, f)
	closeErr := f.Close()
	if err != nil {
		return "", err
	}
	if closeErr != nil {
		return "", closeErr
	}
	digest := hex.EncodeToString(h.Sum(nil))
	if ft.TotalSize > 0 && size != ft.TotalSize {
		return "", fmt.Errorf("pulled file is %d bytes, expected %d", size, ft.TotalSize)
	}
	if ft.SHA256 != "" && !strings.EqualFold(digest, ft.SHA256) {
		return "", errors.New("pulled file SHA-256 mismatch")
	}
	final := transferFinalPath(dir, ft.Filename, ft.ID)
	if _, err := os.Lstat(final); err == nil {
		if err := os.Remove(final); err != nil {
			return "", err
		}
	} else if !os.IsNotExist(err) {
		return "", err
	}
	if err := os.Rename(part, final); err != nil {
		return "", err
	}
	return digest, nil
}

// reconcileFileTransfers heals sessions after a restart or a crashed chunk
// writer, and expires incomplete sessions past their 24h horizon. Main calls
// this at startup and from the periodic maintenance loop.
func (s *Server) reconcileFileTransfers() {
	if s.db == nil {
		return
	}
	now := time.Now()
	var expired []db.FileTransfer
	if err := s.db.Where("status IN ? AND expires_at < ?", []string{transferStatusActive, transferStatusPaused}, now).Find(&expired).Error; err != nil {
		slog.Warn("Failed to load expired transfers", "error", err)
	}
	for _, ft := range expired {
		if ft.CurrentTaskID != nil {
			s.cancelTransferTask(ft.AgentID, *ft.CurrentTaskID, "system")
		}
		if err := s.db.Model(&db.FileTransfer{}).Where("id = ? AND status IN ?", ft.ID, []string{transferStatusActive, transferStatusPaused}).
			Updates(map[string]any{"status": transferStatusExpired, "error": "transfer expired", "current_task_id": nil,
				"reservation_token": "", "reservation_expires_at": nil, "updated_at": now}).Error; err != nil {
			slog.Warn("Failed to expire transfer", "transfer_id", ft.ID, "error", err)
		}
		s.removeTransferFiles(ft)
	}

	var sessions []db.FileTransfer
	if err := s.db.Where("status IN ? OR status = ?", []string{transferStatusActive, transferStatusPaused}, transferStatusCompleted).Find(&sessions).Error; err != nil {
		slog.Warn("Failed to load transfers for reconciliation", "error", err)
		return
	}
	for _, ft := range sessions {
		switch ft.Status {
		case transferStatusActive, transferStatusPaused:
			if ft.CurrentTaskID != nil {
				s.replayCompletedPushTask(ft)
			}
			s.reconcilePartialFile(ft)
		case transferStatusCompleted:
			if ft.CompletedAt != nil && now.Sub(*ft.CompletedAt) > transferFinalFileTTL {
				s.removeTransferFiles(ft)
				continue
			}
			s.reconcileMissingFinalFile(ft)
		}
		s.reapStaleTransferTask(ft, now)
	}
}

// reapStaleTransferTask clears a reservation or task left behind by a crashed
// process. A live task is kept; a terminal one is detached and, for push,
// replayed on the next reconciliation pass.
func (s *Server) reapStaleTransferTask(ft db.FileTransfer, now time.Time) {
	if ft.ReservationToken != "" && (ft.ReservationExpiresAt == nil || ft.ReservationExpiresAt.Before(now)) {
		if err := s.db.Model(&db.FileTransfer{}).Where("id = ? AND reservation_token = ?", ft.ID, ft.ReservationToken).
			Updates(map[string]any{"reservation_token": "", "reservation_expires_at": nil}).Error; err != nil {
			slog.Warn("Failed to clear stale transfer reservation", "transfer_id", ft.ID, "error", err)
		}
	}
	if ft.CurrentTaskID == nil {
		return
	}
	var task db.Task
	if err := s.db.First(&task, *ft.CurrentTaskID).Error; err != nil {
		return
	}
	switch task.Status {
	case "completed":
		if ft.Direction == transferDirectionPush {
			s.replayCompletedPushTask(ft)
		}
	case "failed":
		if err := s.db.Model(&db.FileTransfer{}).Where("id = ? AND current_task_id = ?", ft.ID, *ft.CurrentTaskID).
			Updates(map[string]any{"status": transferStatusFailed, "error": "chunk task failed", "current_task_id": nil,
				"reservation_token": "", "reservation_expires_at": nil, "updated_at": now}).Error; err != nil {
			slog.Warn("Failed to fail transfer after task failure", "transfer_id", ft.ID, "error", err)
		}
	}
}

// replayCompletedPushTask records a push chunk whose task completed just before
// the server crashed. Replaying is idempotent: an existing ledger row with the
// same digest simply advances the session offset.
func (s *Server) replayCompletedPushTask(ft db.FileTransfer) {
	if ft.CurrentTaskID == nil || ft.Direction != transferDirectionPush {
		return
	}
	var task db.Task
	if err := s.db.Where("id = ? AND agent_id = ? AND transfer_id = ? AND type = ? AND status = ?",
		*ft.CurrentTaskID, ft.AgentID, ft.ID, "upload", "completed").First(&task).Error; err != nil {
		return
	}
	if task.Offset != ft.CommittedOffset {
		slog.Warn("Completed push task has unexpected offset", "transfer_id", ft.ID, "task_id", task.ID)
		return
	}
	data, err := base64.StdEncoding.DecodeString(task.Data)
	if err != nil || int64(len(data)) != task.Size {
		slog.Warn("Completed push task has unusable data", "transfer_id", ft.ID, "task_id", task.ID)
		return
	}
	nextMAC, _, err := s.transferChunkMAC(ft.AgentID, ft.PrevMAC, data)
	if err != nil {
		return
	}
	sum := sha256.Sum256(data)
	chunkSHA := hex.EncodeToString(sum[:])
	var ledger db.FileTransferChunk
	err = s.db.Where("transfer_id = ? AND offset = ?", ft.ID, task.Offset).First(&ledger).Error
	if err == nil {
		if ledger.SHA256 != chunkSHA || ledger.MAC != nextMAC {
			slog.Warn("Replayed push chunk conflicts with ledger", "transfer_id", ft.ID, "task_id", task.ID)
			return
		}
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return
	}
	now := time.Now()
	final := task.Final || (ft.TotalSize > 0 && task.Offset+int64(len(data)) >= ft.TotalSize)
	updates := map[string]any{
		"committed_offset":       task.Offset + int64(len(data)),
		"prev_mac":               nextMAC,
		"current_task_id":        nil,
		"reservation_token":      "",
		"reservation_expires_at": nil,
		"updated_at":             now,
	}
	if final {
		updates["status"] = transferStatusCompleted
		updates["completed_at"] = now
	}
	if err := s.db.Transaction(func(tx *gorm.DB) error {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			if err := tx.Create(&db.FileTransferChunk{
				TransferID: ft.ID, Offset: task.Offset, Size: int64(len(data)),
				SHA256: chunkSHA, MAC: nextMAC, ResultID: task.LastResultID, CreatedAt: now,
			}).Error; err != nil {
				return err
			}
		}
		if final {
			actual, err := s.pushTransferSHA256(tx, ft.ID, task.Offset+int64(len(data)))
			if err != nil {
				return err
			}
			if !strings.EqualFold(actual, ft.SHA256) {
				return errors.New("pushed file SHA-256 mismatch")
			}
			updates["computed_sha256"] = actual
		}
		return tx.Model(&db.FileTransfer{}).Where("id = ? AND current_task_id = ?", ft.ID, *ft.CurrentTaskID).Updates(updates).Error
	}); err != nil {
		slog.Warn("Failed to replay completed push task", "transfer_id", ft.ID, "task_id", task.ID, "error", err)
	}
}

// reconcilePartialFile truncates a .part file that grew past the committed
// offset (an unacknowledged write) and fails one that is shorter than the
// committed offset (lost bytes).
func (s *Server) reconcilePartialFile(ft db.FileTransfer) {
	if ft.Direction != transferDirectionPull {
		return
	}
	dir, err := s.transfersDir()
	if err != nil {
		return
	}
	part := transferPartPath(dir, ft.ID)
	info, err := os.Stat(part)
	if err != nil {
		return
	}
	if info.Size() > ft.CommittedOffset {
		if err := os.Truncate(part, ft.CommittedOffset); err != nil {
			slog.Warn("Failed to truncate partial transfer", "transfer_id", ft.ID, "error", err)
		}
		return
	}
	if info.Size() < ft.CommittedOffset {
		if err := s.db.Model(&db.FileTransfer{}).Where("id = ? AND committed_offset = ?", ft.ID, ft.CommittedOffset).
			Updates(map[string]any{"status": transferStatusFailed, "error": "partial file shorter than committed offset"}).Error; err != nil {
			slog.Warn("Failed to mark corrupt transfer", "transfer_id", ft.ID, "error", err)
		}
	}
}

// reconcileMissingFinalFile repairs a completed pull whose rename never
// landed, provided the .part file is complete and its hash still matches.
func (s *Server) reconcileMissingFinalFile(ft db.FileTransfer) {
	if ft.Direction != transferDirectionPull {
		return
	}
	dir, err := s.transfersDir()
	if err != nil {
		return
	}
	final := transferFinalPath(dir, ft.Filename, ft.ID)
	if _, err := os.Stat(final); err == nil {
		return
	}
	part := transferPartPath(dir, ft.ID)
	info, err := os.Stat(part)
	if err != nil || (ft.TotalSize > 0 && info.Size() != ft.TotalSize) {
		if err == nil {
			slog.Warn("Completed transfer has incomplete partial file", "transfer_id", ft.ID)
			_ = s.db.Model(&db.FileTransfer{}).Where("id = ?", ft.ID).
				Updates(map[string]any{"status": transferStatusFailed, "error": "final file missing"}).Error
		}
		return
	}
	digest, err := s.finishPullFile(ft)
	if err != nil {
		slog.Warn("Failed to repair completed transfer", "transfer_id", ft.ID, "error", err)
		_ = s.db.Model(&db.FileTransfer{}).Where("id = ?", ft.ID).
			Updates(map[string]any{"status": transferStatusFailed, "error": "final file could not be restored"}).Error
		return
	}
	if ft.ComputedSHA256 == "" {
		_ = s.db.Model(&db.FileTransfer{}).Where("id = ? AND computed_sha256 = ?", ft.ID, "").
			Update("computed_sha256", digest).Error
	}
}

func (s *Server) removeTransferFiles(ft db.FileTransfer) {
	dir, err := s.transfersDir()
	if err != nil {
		return
	}
	for _, p := range []string{transferPartPath(dir, ft.ID), transferFinalPath(dir, ft.Filename, ft.ID)} {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			slog.Warn("Failed to remove transfer file", "transfer_id", ft.ID, "path", p, "error", err)
		}
	}
}
