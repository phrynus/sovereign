package server

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/gin-gonic/gin"
	"github.com/r0si/sovereign/internal/crypto"
	"github.com/r0si/sovereign/internal/db"
	"github.com/r0si/sovereign/internal/server/middleware"
	"gorm.io/gorm"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Transfer HTTP surface. Create accepts either file permission and re-checks
// the requested direction in the handler. Chunk/pause/cancel require
// files.write; list/detail/file/resume require files.read.
// Every payload uses the canonical respond/respondError envelope.

func transferParamID(c *gin.Context) (uint, error) {
	raw := c.Param("transfer_id")
	if raw == "" {
		raw = c.Param("transferId")
	}
	if raw == "" {
		return 0, errors.New("transfer id required")
	}
	id, err := strconv.ParseUint(raw, 10, 64)
	if err != nil || id == 0 {
		return 0, errors.New("invalid transfer id")
	}
	return uint(id), nil
}

// loadTransferOrFail resolves an agent and a transfer owned by exactly that
// agent and tenant. Foreign IDs are reported as 404.
func (s *Server) loadTransferOrFail(c *gin.Context) (db.Implant, db.FileTransfer, bool) {
	agent, ok := s.getAgentOrFail(c, c.Param("id"))
	if !ok {
		return agent, db.FileTransfer{}, false
	}
	id, err := transferParamID(c)
	if err != nil {
		respondError(c, http.StatusBadRequest, err.Error())
		return agent, db.FileTransfer{}, false
	}
	ft, err := s.transferSessionForAgent(agent, id)
	if err != nil {
		respondError(c, http.StatusNotFound, "transfer not found")
		return agent, db.FileTransfer{}, false
	}
	return agent, ft, true
}

type createTransferRequest struct {
	Direction string `json:"direction"`
	Path      string `json:"path"`
	TotalSize int64  `json:"total_size"`
	SHA256    string `json:"sha256"`
	Filename  string `json:"filename"`
}

func (s *Server) handleCreateTransfer(c *gin.Context) {
	if !s.requireOperator(c) {
		return
	}
	agent, ok := s.getAgentOrFail(c, c.Param("id"))
	if !ok {
		return
	}
	var req createTransferRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondError(c, http.StatusBadRequest, "invalid transfer request")
		return
	}
	req.Direction = strings.ToLower(strings.TrimSpace(req.Direction))
	if req.Direction != transferDirectionPush && req.Direction != transferDirectionPull {
		respondError(c, http.StatusBadRequest, "direction must be push or pull")
		return
	}
	permission := db.PermFilesRead
	if req.Direction == transferDirectionPush {
		permission = db.PermFilesWrite
	}
	if !middleware.HasPermission(c, permission) {
		respondError(c, http.StatusForbidden, permission+" required")
		return
	}
	if req.TotalSize < 0 || req.TotalSize > MaxTransferBytes {
		respondError(c, http.StatusBadRequest, fmt.Sprintf("total_size must be between 0 and %d", MaxTransferBytes))
		return
	}
	remotePath, err := validateTransferRemotePath(req.Path)
	if err != nil {
		respondError(c, http.StatusBadRequest, err.Error())
		return
	}
	filename, err := validateTransferFilename(req.Filename, remotePath)
	if err != nil {
		respondError(c, http.StatusBadRequest, err.Error())
		return
	}
	digest := strings.ToLower(strings.TrimSpace(req.SHA256))
	if req.Direction == transferDirectionPush && !isTransferSHA256(digest) {
		respondError(c, http.StatusBadRequest, "sha256 must be a 64-character hex string")
		return
	}
	now := time.Now()
	pathKey := transferPathKey(agent.ID, remotePath)
	ft := db.FileTransfer{
		TenantID:   agent.TenantID,
		AgentID:    agent.ID,
		CreatedBy:  s.currentUsername(c),
		Direction:  req.Direction,
		RemotePath: remotePath,
		PathKey:    pathKey,
		Filename:   filename,
		TotalSize:  req.TotalSize,
		ChunkSize:  FileTransferChunkSize,
		SHA256:     digest,
		PrevMAC:    strings.Repeat("0", 64),
		Status:     transferStatusActive,
		ExpiresAt:  now.Add(transferSessionTTL),
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	var existing db.FileTransfer
	err = s.db.Where("agent_id = ? AND path_key = ? AND status IN ?", agent.ID, pathKey,
		[]string{transferStatusActive, transferStatusPaused}).First(&existing).Error
	if err == nil {
		respondError(c, http.StatusConflict, "an active transfer already exists for this path")
		return
	}
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		respondErrorSafe(c, http.StatusInternalServerError, err, "check existing transfer")
		return
	}
	if err := s.db.Create(&ft).Error; err != nil {
		if isUniqueViolation(err) {
			respondError(c, http.StatusConflict, "an active transfer already exists for this path")
			return
		}
		respondErrorSafe(c, http.StatusInternalServerError, err, "create transfer")
		return
	}
	s.LogAuditRecord(c, "transfer_create", "agent", agent.ID,
		fmt.Sprintf("created %s transfer %d for %s (%d bytes)", ft.Direction, ft.ID, remotePath, req.TotalSize), true, nil)
	respond(c, gin.H{"transfer": ft})
}

func isTransferSHA256(s string) bool {
	if len(s) != 64 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}

func (s *Server) handleListTransfers(c *gin.Context) {
	if !s.requireOperator(c) {
		return
	}
	agent, ok := s.getAgentOrFail(c, c.Param("id"))
	if !ok {
		return
	}
	limit := 100
	if raw := c.Query("limit"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 && n <= 500 {
			limit = n
		}
	}
	var transfers []db.FileTransfer
	if err := s.db.Where("agent_id = ? AND tenant_id = ?", agent.ID, agent.TenantID).
		Order("created_at DESC").Limit(limit).Find(&transfers).Error; err != nil {
		respondErrorSafe(c, http.StatusInternalServerError, err, "list transfers")
		return
	}
	respond(c, gin.H{"transfers": transfers})
}

func (s *Server) handleGetTransfer(c *gin.Context) {
	if !s.requireOperator(c) {
		return
	}
	_, ft, ok := s.loadTransferOrFail(c)
	if !ok {
		return
	}
	respond(c, gin.H{"transfer": ft})
}

type transferChunkRequest struct {
	Offset     int64  `json:"offset"`
	DataBase64 string `json:"data_base64"`
	Final      bool   `json:"final"`
}

func (s *Server) handlePushTransferChunk(c *gin.Context) {
	if !s.requireOperator(c) {
		return
	}
	agent, ft, ok := s.loadTransferOrFail(c)
	if !ok {
		return
	}
	var req transferChunkRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondError(c, http.StatusBadRequest, "invalid chunk request")
		return
	}
	if ft.Direction != transferDirectionPush {
		respondError(c, http.StatusBadRequest, "transfer is not a push")
		return
	}
	if ft.Status != transferStatusActive && ft.Status != transferStatusPaused {
		respondError(c, http.StatusConflict, "transfer is "+ft.Status)
		return
	}
	if !ft.ExpiresAt.After(time.Now()) {
		respondError(c, http.StatusGone, "transfer expired")
		return
	}
	if req.Offset < 0 || req.Offset != ft.CommittedOffset {
		respondError(c, http.StatusConflict, fmt.Sprintf("chunk offset must be %d", ft.CommittedOffset))
		return
	}
	data, err := base64.StdEncoding.DecodeString(req.DataBase64)
	if err != nil {
		respondError(c, http.StatusBadRequest, "data_base64 is not valid base64")
		return
	}
	if len(data) > FileTransferChunkSize {
		respondError(c, http.StatusBadRequest, fmt.Sprintf("chunk exceeds %d bytes", FileTransferChunkSize))
		return
	}
	if ft.TotalSize > 0 && req.Offset+int64(len(data)) > ft.TotalSize {
		respondError(c, http.StatusBadRequest, "chunk exceeds transfer size")
		return
	}
	if req.Final && ft.TotalSize > 0 && req.Offset+int64(len(data)) != ft.TotalSize {
		respondError(c, http.StatusBadRequest, "final chunk must end at total_size")
		return
	}
	sum := sha256.Sum256(data)
	digest := hex.EncodeToString(sum[:])

	// Already committed: identical bytes are an acknowledgement replay,
	// different bytes for the same offset are a conflict.
	var ledger db.FileTransferChunk
	err = s.db.Where("transfer_id = ? AND offset = ?", ft.ID, req.Offset).First(&ledger).Error
	if err == nil {
		if !strings.EqualFold(ledger.SHA256, digest) {
			respondError(c, http.StatusConflict, "chunk digest differs from committed chunk")
			return
		}
		respond(c, gin.H{"task_id": ft.CurrentTaskID, "transfer": ft})
		return
	}
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		respondErrorSafe(c, http.StatusInternalServerError, err, "read transfer ledger")
		return
	}

	// A task already queued for this offset: the reservation may have been
	// lost with a restart, but the task's payload is still authoritative.
	var pending db.Task
	err = s.db.Where("transfer_id = ? AND type = ? AND \"offset\" = ? AND status IN ?",
		ft.ID, "upload", req.Offset, []string{"pending", "running", "sent", TaskStatusPendingApproval}).
		Order("id DESC").First(&pending).Error
	if err == nil {
		pendingDigest := sha256.Sum256([]byte(pending.Data))
		if strings.EqualFold(hex.EncodeToString(pendingDigest[:]), digest) {
			respond(c, gin.H{"task_id": pending.ID, "transfer": ft})
			return
		}
		respondError(c, http.StatusConflict, "different data already queued for this offset")
		return
	}
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		respondErrorSafe(c, http.StatusInternalServerError, err, "read pending transfer task")
		return
	}

	now := time.Now()
	token, err := s.reserveTransfer(&ft, now)
	if err != nil {
		if errors.Is(err, errTransferStale) {
			respondError(c, http.StatusConflict, "another chunk is being queued for this transfer")
		} else {
			respondErrorSafe(c, http.StatusInternalServerError, err, "reserve transfer chunk")
		}
		return
	}
	chainKey := s.fileChainKey(agent.ID)
	if chainKey == nil {
		s.releaseTransferReservation(ft, token)
		respondError(c, http.StatusInternalServerError, "cannot derive the transfer integrity key")
		return
	}
	prev, err := hex.DecodeString(ft.PrevMAC)
	if err != nil || len(prev) != 32 {
		s.releaseTransferReservation(ft, token)
		respondError(c, http.StatusInternalServerError, "transfer chain state is invalid")
		return
	}
	mac := crypto.FileChunkMAC(chainKey, prev, data)
	if mac == nil {
		s.releaseTransferReservation(ft, token)
		respondError(c, http.StatusInternalServerError, "cannot sign transfer chunk")
		return
	}
	macHex := hex.EncodeToString(mac)
	task, err := s.createTransferTask(agent.ID, ft, "upload", req.DataBase64, req.Offset, int64(len(data)),
		transferDirectionPush, req.Final, ft.PrevMAC, macHex, s.currentUsername(c), now)
	if err != nil {
		s.releaseTransferReservation(ft, token)
		respondTaskError(c, err)
		return
	}
	if err := s.bindTransferTask(ft, token, task.ID, now); err != nil {
		s.cancelTransferTask(agent.ID, task.ID, s.currentUsername(c))
		respondError(c, http.StatusConflict, "transfer reservation expired")
		return
	}
	s.broadcastTaskUpdate(agent.ID, *task)
	s.LogAuditRecord(c, "transfer_chunk_queued", "agent", agent.ID,
		fmt.Sprintf("queued push chunk for transfer %d at %d (%d bytes) final=%t", ft.ID, req.Offset, len(data), req.Final), true, nil)
	respond(c, gin.H{"task_id": task.ID, "transfer": ft})
}

func (s *Server) handleResumeTransfer(c *gin.Context) {
	if !s.requireOperator(c) {
		return
	}
	agent, ft, ok := s.loadTransferOrFail(c)
	if !ok {
		return
	}
	if ft.Direction != transferDirectionPull {
		respondError(c, http.StatusBadRequest, "transfer is not a pull")
		return
	}
	if ft.Status != transferStatusActive && ft.Status != transferStatusPaused {
		respondError(c, http.StatusConflict, "transfer is "+ft.Status)
		return
	}
	if !ft.ExpiresAt.After(time.Now()) {
		respondError(c, http.StatusGone, "transfer expired")
		return
	}
	if ft.TotalSize > 0 && ft.CommittedOffset >= ft.TotalSize {
		respondError(c, http.StatusConflict, "transfer is already complete")
		return
	}
	if ft.CurrentTaskID != nil && *ft.CurrentTaskID > 0 {
		var active db.Task
		if err := s.db.First(&active, *ft.CurrentTaskID).Error; err == nil {
			respond(c, gin.H{"task_id": active.ID, "transfer": ft})
			return
		}
	}
	now := time.Now()
	token, err := s.reserveTransfer(&ft, now)
	if err != nil {
		if errors.Is(err, errTransferStale) {
			respondError(c, http.StatusConflict, "another resume is in progress")
		} else {
			respondErrorSafe(c, http.StatusInternalServerError, err, "reserve transfer resume")
		}
		return
	}
	size := int64(FileTransferChunkSize)
	final := false
	if ft.TotalSize > 0 {
		remaining := ft.TotalSize - ft.CommittedOffset
		if remaining < size {
			size = remaining
		}
		final = remaining <= FileTransferChunkSize
	}
	task, err := s.createTransferTask(agent.ID, ft, "download", "", ft.CommittedOffset, size,
		transferDirectionPull, final, ft.PrevMAC, "", s.currentUsername(c), now)
	if err != nil {
		s.releaseTransferReservation(ft, token)
		respondTaskError(c, err)
		return
	}
	if err := s.bindTransferTask(ft, token, task.ID, now); err != nil {
		s.cancelTransferTask(agent.ID, task.ID, s.currentUsername(c))
		respondError(c, http.StatusConflict, "transfer reservation expired")
		return
	}
	s.broadcastTaskUpdate(agent.ID, *task)
	s.LogAuditRecord(c, "transfer_resume", "agent", agent.ID,
		fmt.Sprintf("resumed pull transfer %d at %d", ft.ID, ft.CommittedOffset), true, nil)
	respond(c, gin.H{"task_id": task.ID, "transfer": ft})
}

// lockTransferSession is the pause/cancel counterpart of lockTransferForCommit:
// both take the same short reservation on the session row, so a pause cannot
// race a chunk commit into advancing a stale offset.
func (s *Server) lockTransferSession(ft db.FileTransfer, now time.Time) (string, error) {
	token, err := newTransferToken()
	if err != nil {
		return "", err
	}
	res := s.withBusyRetryDB("transfer_pause_lock", func() *gorm.DB {
		return s.db.Model(&db.FileTransfer{}).
			Where("id = ? AND status IN ? AND (reservation_token = ? OR reservation_expires_at IS NULL OR reservation_expires_at < ?)",
				ft.ID, []string{transferStatusActive, transferStatusPaused}, "", now).
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

func (s *Server) handlePauseTransfer(c *gin.Context) {
	if !s.requireOperator(c) {
		return
	}
	agent, ft, ok := s.loadTransferOrFail(c)
	if !ok {
		return
	}
	if err := s.pauseOrCancelTransfer(ft, transferStatusPaused, s.currentUsername(c)); err != nil {
		if errors.Is(err, errTransferStale) {
			respondError(c, http.StatusConflict, "transfer changed concurrently")
		} else {
			respondErrorSafe(c, http.StatusInternalServerError, err, "pause transfer")
		}
		return
	}
	s.LogAuditRecord(c, "transfer_pause", "agent", agent.ID,
		fmt.Sprintf("paused transfer %d at %d", ft.ID, ft.CommittedOffset), true, nil)
	respond(c, gin.H{"transfer": ft})
}

func (s *Server) handleCancelTransfer(c *gin.Context) {
	if !s.requireOperator(c) {
		return
	}
	agent, ft, ok := s.loadTransferOrFail(c)
	if !ok {
		return
	}
	if err := s.pauseOrCancelTransfer(ft, transferStatusCancelled, s.currentUsername(c)); err != nil {
		if errors.Is(err, errTransferStale) {
			respondError(c, http.StatusConflict, "transfer changed concurrently")
		} else {
			respondErrorSafe(c, http.StatusInternalServerError, err, "cancel transfer")
		}
		return
	}
	s.removeTransferFiles(ft)
	s.LogAuditRecord(c, "transfer_cancel", "agent", agent.ID,
		fmt.Sprintf("cancelled transfer %d at %d", ft.ID, ft.CommittedOffset), true, nil)
	respond(c, gin.H{"transfer": ft})
}

// pauseOrCancelTransfer cancels the bound task and moves the session to the
// requested terminal-or-paused state under one row lock.
func (s *Server) pauseOrCancelTransfer(ft db.FileTransfer, status, actor string) error {
	now := time.Now()
	token, err := s.lockTransferSession(ft, now)
	if err != nil {
		return err
	}
	if ft.CurrentTaskID != nil {
		s.cancelTransferTask(ft.AgentID, *ft.CurrentTaskID, actor)
	}
	updates := map[string]any{
		"status":                 status,
		"current_task_id":        nil,
		"reservation_token":      "",
		"reservation_expires_at": nil,
		"updated_at":             now,
	}
	if status == transferStatusCancelled {
		updates["error"] = "cancelled by operator"
	}
	res := s.withBusyRetryDB("transfer_state", func() *gorm.DB {
		return s.db.Model(&db.FileTransfer{}).Where("id = ? AND reservation_token = ?", ft.ID, token).Updates(updates)
	})
	if res.Error != nil {
		s.releaseTransferReservation(ft, token)
		return res.Error
	}
	if res.RowsAffected != 1 {
		return errTransferStale
	}
	ft.Status = status
	ft.CurrentTaskID = nil
	ft.UpdatedAt = now
	return nil
}

func (s *Server) handleGetTransferFile(c *gin.Context) {
	if !s.requireOperator(c) {
		return
	}
	_, ft, ok := s.loadTransferOrFail(c)
	if !ok {
		return
	}
	if ft.Direction != transferDirectionPull || ft.Status != transferStatusCompleted {
		respondError(c, http.StatusNotFound, "transfer file not available")
		return
	}
	dir, err := s.transfersDir()
	if err != nil {
		respondErrorSafe(c, http.StatusInternalServerError, err, "transfer directory")
		return
	}
	serveFileSafe(c, transferFinalPath(dir, ft.Filename, ft.ID), dir, ft.Filename)
}
