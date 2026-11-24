package server

import (
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/r0si/sovereign/internal/db"
	"github.com/r0si/sovereign/pkg/protocol"
)

const (
	// maxEditorFileBytes bounds one text edit. The file manager preview uses
	// the same ceiling so a binary blob can never be opened in the editor.
	maxEditorFileBytes = 1 << 20
)

var errNoFileBaseline = errors.New("no completed read or write baseline for this path")

// findFileEditBaseline resolves the optimistic-concurrency baseline. A caller
// may name an exact task; otherwise the newest completed baseline wins.
// Hash-only baselines are required: without one the agent could not detect a
// concurrent external edit before replacing the file.
func (s *Server) findFileEditBaseline(agentID, path string, baseTaskID uint) (db.Task, error) {
	q := s.db.Where("agent_id = ? AND type IN ? AND command = ? AND status = ? AND result_sha256 <> ''",
		agentID, []string{protocol.TaskTypeRead, protocol.TaskTypeWrite}, path, "completed")
	if baseTaskID != 0 {
		q = q.Where("id = ?", baseTaskID)
	}
	var task db.Task
	if err := q.Order("id DESC").First(&task).Error; err != nil {
		return db.Task{}, errNoFileBaseline
	}
	return task, nil
}

// handleFileWrite queues a compare-and-swap text edit. It never returns the
// draft in any public task projection: the encrypted Task.Data is only
// available through the explicit revisions endpoint.
func (s *Server) handleFileWrite(c *gin.Context) {
	if !s.requireOperator(c) {
		return
	}
	agentID := c.Param("id")
	if _, ok := s.getAgentOrFail(c, agentID); !ok {
		return
	}
	path := strings.TrimSpace(c.PostForm("path"))
	if path == "" {
		respondError(c, http.StatusBadRequest, "path required")
		return
	}
	content := c.PostForm("content")
	if content == "" {
		content = c.PostForm("content_base64")
	}
	if content == "" && c.PostForm("empty") == "true" {
		content = ""
	}
	raw := []byte(content)
	if c.PostForm("content_base64") != "" || c.PostForm("encoding") == "base64" {
		decoded, err := base64.StdEncoding.DecodeString(content)
		if err != nil {
			respondError(c, http.StatusBadRequest, "content is not valid base64")
			return
		}
		raw = decoded
	}
	if len(raw) > maxEditorFileBytes {
		respondError(c, http.StatusBadRequest, fmt.Sprintf("file too large (max %d bytes)", maxEditorFileBytes))
		return
	}
	if !utf8.Valid(raw) {
		respondError(c, http.StatusBadRequest, "file content must be valid UTF-8")
		return
	}
	if strings.IndexByte(string(raw), 0) >= 0 {
		respondError(c, http.StatusBadRequest, "file content must not contain NUL bytes")
		return
	}
	baseID, _ := strconv.ParseUint(c.PostForm("base_task_id"), 10, 64)
	baseline, err := s.findFileEditBaseline(agentID, path, uint(baseID))
	if err != nil {
		respondError(c, http.StatusNotFound, err.Error())
		return
	}

	// Continue the integrity chain from the baseline task so a reordered or
	// replayed edit cannot be applied out of sequence.
	chainKey := s.fileChainKey(agentID)
	if chainKey == nil {
		respondError(c, http.StatusInternalServerError, "cannot derive the file integrity key")
		return
	}
	prev := make([]byte, 32)
	if decoded, derr := hex.DecodeString(baseline.MAC); derr == nil && len(decoded) == 32 {
		prev = decoded
	}
	prevMAC, mac, err := chainForPushSequence(chainKey, prev, raw)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "failed to sign file edit")
		return
	}
	baselineID := baseline.ID
	task := s.issueAgentTask(c, agentID, TaskSpec{
		Type:           protocol.TaskTypeWrite,
		Command:        path,
		Path:           path,
		Data:           base64.StdEncoding.EncodeToString(raw),
		BaseTaskID:     &baselineID,
		ExpectedSHA256: baseline.ResultSHA256,
		FileChainPrev:  prevMAC,
		FileChainMAC:   mac,
	})
	if task == nil {
		return
	}
	s.LogAuditRecord(c, "file_editor_write", "agent", agentID,
		fmt.Sprintf("path=%s sha256=%s size=%d base_task=%d", path, baseline.ResultSHA256, len(raw), baseline.ID), true, nil)
	s.broadcastTaskUpdate(agentID, *task)
	respond(c, gin.H{"task_id": task.ID, "base_task_id": baseline.ID, "sha256": baseline.ResultSHA256})
}

// handleFileHistory lists read/write tasks for one path on one agent.
func (s *Server) handleFileHistory(c *gin.Context) {
	agentID := c.Param("id")
	if _, ok := s.getAgentOrFail(c, agentID); !ok {
		return
	}
	path := strings.TrimSpace(c.Query("path"))
	if path == "" {
		respondError(c, http.StatusBadRequest, "path required")
		return
	}
	limit := 50
	if raw := c.Query("limit"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 && n <= 50 {
			limit = n
		}
	}
	var tasks []db.Task
	if err := s.db.Where("agent_id = ? AND type IN ? AND command = ?",
		agentID, []string{protocol.TaskTypeRead, protocol.TaskTypeWrite}, path).
		Order("id DESC").Limit(limit).Find(&tasks).Error; err != nil {
		handleQueryError(c, err, "Failed to query file history")
		return
	}
	type revisionRow struct {
		TaskID    uint   `json:"task_id"`
		Type      string `json:"type"`
		Operator  string `json:"operator"`
		CreatedAt string `json:"created_at"`
		SHA256    string `json:"sha256"`
		Mtime     int64  `json:"mtime"`
		Size      int    `json:"size"`
	}
	rows := make([]revisionRow, 0, len(tasks))
	for _, task := range tasks {
		size := len(task.ResultSHA256) // placeholder, replaced below
		_ = size
		content := task.Result
		if task.Type == protocol.TaskTypeWrite {
			content = task.Data
		}
		if decoded, err := base64.StdEncoding.DecodeString(content); err == nil {
			size = len(decoded)
		} else {
			size = len(content)
		}
		rows = append(rows, revisionRow{
			TaskID: task.ID, Type: task.Type, Operator: task.CreatedBy,
			CreatedAt: task.CreatedAt.UTC().Format(time.RFC3339),
			SHA256:    task.ResultSHA256, Mtime: task.ResultModTime, Size: size,
		})
	}
	respond(c, gin.H{"revisions": rows})
}

// handleFileRevision returns ONE snapshot's bytes. This is deliberately a
// separate endpoint from task projection: normal task reads never include
// file contents.
func (s *Server) handleFileRevision(c *gin.Context) {
	agentID := c.Param("id")
	if _, ok := s.getAgentOrFail(c, agentID); !ok {
		return
	}
	taskID, err := strconv.ParseUint(c.Param("taskId"), 10, 64)
	if err != nil {
		respondError(c, http.StatusBadRequest, "invalid task id")
		return
	}
	var task db.Task
	if err := s.db.Where("id = ? AND agent_id = ? AND type IN ?", taskID, agentID,
		[]string{protocol.TaskTypeRead, protocol.TaskTypeWrite}).First(&task).Error; err != nil {
		respondError(c, http.StatusNotFound, "revision not found")
		return
	}
	content := task.Result
	if task.Type == protocol.TaskTypeWrite {
		content = task.Data
	}
	respond(c, gin.H{
		"task_id":        task.ID,
		"path":           task.Command,
		"operator":       task.CreatedBy,
		"created_at":     task.CreatedAt.UTC().Format(time.RFC3339),
		"sha256":         task.ResultSHA256,
		"mtime":          task.ResultModTime,
		"content_base64": content,
	})
}
