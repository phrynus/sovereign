package server

import (
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/r0si/sovereign/internal/db"
	"github.com/r0si/sovereign/internal/server/middleware"
	"gorm.io/gorm"
)

// One-shot scheduled tasks: "at 02:00 run X on agent Y". Distinct from
// automation rules — single execution, operator-facing, no rule machinery.

type oneShotRequest struct {
	AgentID          string               `json:"agent_id"`
	Command          string               `json:"command"`
	RunAt            string               `json:"run_at"` // RFC3339; empty = ASAP
	Type             string               `json:"type"`   // default "shell"
	CredentialID     *uint                `json:"credential_id,omitempty"`
	CredentialParams CredentialTaskParams `json:"credential_params,omitempty"`
}

func (s *Server) handleListOneShotTasks(c *gin.Context) {
	// one_shot_tasks has no tenant column, so the scope runs through the AGENT that owns the
	// row: without it every operator saw every tenant's scheduled COMMANDS and target ids.
	sub, ok := s.visibleAgentIDSubquery(c)
	if !ok {
		respond(c, gin.H{"tasks": []db.OneShotTask{}})
		return
	}
	var tasks []db.OneShotTask
	if err := s.db.Where("agent_id IN (?)", sub).Order("run_at asc").Limit(200).Find(&tasks).Error; err != nil {
		respondError(c, http.StatusInternalServerError, "query failed")
		return
	}
	respond(c, gin.H{"tasks": tasks})
}

// visibleAgentIDSubquery returns a subquery of the agent ids the caller may act on, or ok=false
// when the tenant lookup failed (fail closed: the caller answers an empty set, never the world).
func (s *Server) visibleAgentIDSubquery(c *gin.Context) (*gorm.DB, bool) {
	tid, resolved := s.resolveTenant(c)
	if !resolved {
		return nil, false
	}
	sub := s.db.Model(&db.Implant{}).Select("id")
	if tid != 0 {
		sub = sub.Where("tenant_id = ?", tid)
	}
	return sub, true
}

func (s *Server) handleCreateOneShotTask(c *gin.Context) {
	var req oneShotRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondError(c, http.StatusBadRequest, "agent_id and command required")
		return
	}
	if _, ok := s.getAgentOrFail(c, req.AgentID); !ok {
		return
	}
	taskType := req.Type
	if taskType == "" {
		taskType = "shell"
	}
	if req.CredentialID != nil {
		if !requireCredentialReferencePermission(c, req.CredentialID) {
			respondError(c, http.StatusForbidden, "credentials.read is required to reference vault entries")
			return
		}
		if !credentialReferenceTaskTypes(taskType) || req.Command != "" {
			respondError(c, http.StatusBadRequest, "invalid credential reference schedule")
			return
		}
		if _, _, _, _, _, _, err := s.prepareCredentialTask(req.AgentID, taskType,
			credentialTaskReference{ID: *req.CredentialID, Params: encodeCredentialParams(req.CredentialParams)}); err != nil {
			respondError(c, http.StatusBadRequest, sanitizeError(err, "invalid credential reference"))
			return
		}
	}
	var runAt time.Time
	if req.RunAt != "" {
		var err error
		runAt, err = time.Parse(time.RFC3339, req.RunAt)
		if err != nil {
			respondError(c, http.StatusBadRequest, "run_at must be RFC3339 (e.g. 2026-01-01T02:00:00Z)")
			return
		}
	}
	username := s.currentUsername(c)
	row := db.OneShotTask{
		AgentID:   req.AgentID,
		Type:      taskType,
		Command:   req.Command,
		Status:    "pending",
		CreatedBy: username,
	}
	row.CredentialID = req.CredentialID
	if req.CredentialID != nil {
		row.CredentialParams = encodeCredentialParams(req.CredentialParams)
	}
	if !runAt.IsZero() {
		row.RunAt = runAt
	} else {
		row.RunAt = time.Now()
	}
	if len(req.Command) > MaxCommandLength {
		respondError(c, http.StatusBadRequest, fmt.Sprintf("command too long (max %d characters)", MaxCommandLength))
		return
	}
	if err := s.db.Create(&row).Error; err != nil {
		respondError(c, http.StatusInternalServerError, "failed to schedule task")
		return
	}
	s.LogAuditRecord(c, "oneshot_schedule", "agent", req.AgentID,
		fmt.Sprintf("one-shot %s scheduled for %s: %s", taskType, row.RunAt.Format(time.RFC3339), truncateStr(req.Command, 120)), true, nil)
	respond(c, gin.H{"task": row})
}

func (s *Server) handleCancelOneShotTask(c *gin.Context) {
	id := c.Param("id")
	sub, ok := s.visibleAgentIDSubquery(c)
	if !ok {
		respondError(c, http.StatusNotFound, "pending task not found")
		return
	}
	// Same scope as the list: a cancel by id must not reach another tenant's schedule.
	res := s.db.Model(&db.OneShotTask{}).Where("id = ? AND status = ? AND agent_id IN (?)", id, "pending", sub).Update("status", "cancelled")
	if res.Error != nil {
		respondError(c, http.StatusInternalServerError, "failed to cancel")
		return
	}
	if res.RowsAffected == 0 {
		respondError(c, http.StatusNotFound, "pending task not found")
		return
	}
	s.LogAuditRecord(c, "oneshot_cancel", "agent", id, "", true, nil)
	respond(c, gin.H{})
}

// recoverClaimedOneShotTasks returns claims left behind by an interrupted
// process to the queue. Only the single local teamserver owns this SQLite DB,
// so any dispatching row observed during startup is necessarily stale.
func (s *Server) recoverClaimedOneShotTasks() {
	res := s.db.Model(&db.OneShotTask{}).
		Where("status = ?", "dispatching").
		Update("status", "pending")
	if res.Error != nil {
		slog.Error("OneShot scheduler: failed to recover stale claims", "err", res.Error)
		return
	}
	if res.RowsAffected > 0 {
		slog.Warn("OneShot scheduler: recovered stale claims", "count", res.RowsAffected)
	}
}

// dispatchDueOneShotTasks runs every scheduler tick: pending rows whose time
// has arrived are dispatched as regular shell tasks and marked done.
func (s *Server) dispatchDueOneShotTasks() {
	var due []db.OneShotTask
	if err := s.db.Where("status = ? AND run_at <= ?", "pending", time.Now()).Limit(50).Find(&due).Error; err != nil {
		slog.Error("OneShot scheduler: query failed", "err", err)
		return
	}
	for _, st := range due {
		// Claim before dispatch so overlapping scheduler ticks cannot create the
		// same task twice. RowsAffected is the ownership decision.
		claim := s.db.Model(&db.OneShotTask{}).
			Where("id = ? AND status = ?", st.ID, "pending").
			Update("status", "dispatching")
		if claim.Error != nil {
			slog.Error("OneShot scheduler: claim failed", "id", st.ID, "err", claim.Error)
			continue
		}
		if claim.RowsAffected == 0 {
			continue
		}

		opts := []TaskOption{}
		if st.CredentialID != nil && *st.CredentialID != 0 {
			allowed, permErr := middleware.UserHasPermission(s.db, st.CreatedBy, db.PermCredsRead)
			if permErr != nil || !allowed {
				slog.Warn("OneShot dispatch refused: creator lost credentials.read",
					"id", st.ID, "creator", st.CreatedBy, "err", permErr)
				now := time.Now()
				_ = s.db.Model(&db.OneShotTask{}).Where("id = ?", st.ID).Updates(map[string]interface{}{
					"status": "error", "finished_at": &now,
				}).Error
				continue
			}
			params, _ := decodeCredentialParams(st.CredentialParams)
			opts = append(opts, WithCredentialReference(*st.CredentialID, params))
		}
		task, err := s.createTask(st.AgentID, st.Type, st.Command, "", "", "", 0, 0, opts...)
		now := time.Now()
		if err != nil {
			slog.Warn("OneShot dispatch failed", "id", st.ID, "agent_id", st.AgentID, "err", err)
			if updateErr := s.db.Model(&db.OneShotTask{}).Where("id = ?", st.ID).Updates(map[string]interface{}{
				"status": "error", "finished_at": &now,
			}).Error; updateErr != nil {
				slog.Error("OneShot scheduler: failed to persist dispatch error", "id", st.ID, "err", updateErr)
			}
			continue
		}
		s.broadcastTaskUpdate(st.AgentID, *task)
		if updateErr := s.db.Model(&db.OneShotTask{}).Where("id = ?", st.ID).Updates(map[string]interface{}{
			"status": "done", "task_id": task.ID, "finished_at": &now,
		}).Error; updateErr != nil {
			slog.Error("OneShot scheduler: failed to persist dispatch result", "id", st.ID, "task_id", task.ID, "err", updateErr)
		}
		s.LogAuditRecord(nil, "oneshot_dispatched", "agent", st.AgentID,
			fmt.Sprintf("one-shot #%d dispatched as task #%d", st.ID, task.ID), true, nil)
	}
}
