package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/r0si/sovereign/internal/db"
	"github.com/r0si/sovereign/internal/server/middleware"
	"gorm.io/gorm"
)

// handleLateralPage renders the lateral movement page
func (s *Server) handleLateralPage(c *gin.Context) {
	stats := s.getNavStats(c)

	// Get available agents
	var agents []db.Implant
	if err := s.tenantScope(s.db, c).Where("status = 'online'").Order("last_seen desc").Limit(5000).Find(&agents).Error; err != nil {
		respondError(c, http.StatusInternalServerError, "failed to query agents")
		return
	}

	var credentials []db.CredentialEntry
	if err := s.tenantScope(s.db, c).Order("created_at desc").Limit(50).Find(&credentials).Error; err != nil {
		respondError(c, http.StatusInternalServerError, "failed to query credentials")
		return
	}

	var onlineAgents int64
	if err := s.tenantScope(s.db.Model(&db.Implant{}), c).Where("status = 'online'").Count(&onlineAgents).Error; err != nil {
		respondError(c, http.StatusInternalServerError, "failed to count agents")
		return
	}

	var totalCreds int64
	if err := s.tenantScope(s.db.Model(&db.CredentialEntry{}), c).Count(&totalCreds).Error; err != nil {
		respondError(c, http.StatusInternalServerError, "failed to count credentials")
		return
	}

	var totalTasks int64
	if err := s.tenantScope(s.db.Model(&db.Task{}), c).Where("type = 'lateral'").Count(&totalTasks).Error; err != nil {
		respondError(c, http.StatusInternalServerError, "failed to count tasks")
		return
	}

	data := gin.H{
		"Title":        "Sovereign - Lateral Movement",
		"ActiveNav":    "lateral",
		"Stats":        stats,
		"Agents":       agents,
		"Credentials":  credentials,
		"OnlineAgents": onlineAgents,
		"TotalCreds":   totalCreds,
	}
	if middleware.HasPermission(c, db.PermTasksRead) {
		data["TotalTasks"] = totalTasks
	}
	s.renderPageOrJSON(c, data)
}

// LateralHistoryRow is the fixed lateral-history projection. Command,
// credential material and task payloads never appear in this shape.
type LateralHistoryRow struct {
	TaskID        uint      `json:"task_id"`
	SourceAgentID string    `json:"source_agent_id"`
	Type          string    `json:"type,omitempty"`
	Status        string    `json:"status"`
	CreatedAt     time.Time `json:"created_at"`
	CreatedBy     string    `json:"created_by"`
	Target        string    `json:"target,omitempty"`
	Method        string    `json:"method,omitempty"`
	Result        string    `json:"result,omitempty"`
	Error         string    `json:"error,omitempty"`
}

// handleLateralHistory returns tenant-scoped lateral movement history.
func (s *Server) handleLateralHistory(c *gin.Context) {
	agentID := c.Param("id")
	if agentID != "all" {
		var visible db.Implant
		if err := s.tenantScope(s.db, c).Select("id").Where("id = ?", agentID).First(&visible).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				respondError(c, http.StatusNotFound, "agent not found")
			} else {
				respondError(c, http.StatusInternalServerError, "failed to query agent")
			}
			return
		}
	}

	query := s.tenantScope(s.db, c).Where("type = 'lateral'")
	if agentID != "all" {
		query = query.Where("agent_id = ?", agentID)
	}
	var tasks []db.Task
	if err := query.Order("created_at desc").Limit(50).Find(&tasks).Error; err != nil {
		respondError(c, http.StatusInternalServerError, "failed to query lateral history")
		return
	}

	countQuery := s.tenantScope(s.db.Model(&db.Task{}), c).Where("type = 'lateral'")
	if agentID != "all" {
		countQuery = countQuery.Where("agent_id = ?", agentID)
	}
	var total int64
	if err := countQuery.Count(&total).Error; err != nil {
		respondError(c, http.StatusInternalServerError, "failed to count lateral history")
		return
	}

	rows := make([]LateralHistoryRow, 0, len(tasks))
	for _, task := range tasks {
		rows = append(rows, lateralHistoryRowForOperator(c, task))
	}
	respond(c, gin.H{
		"tasks":          rows,
		"total":          total,
		"tasks_redacted": !middleware.HasPermission(c, db.PermTasksRead),
	})
}

func lateralHistoryRowForOperator(c *gin.Context, task db.Task) LateralHistoryRow {
	row := LateralHistoryRow{
		TaskID:        task.ID,
		SourceAgentID: task.AgentID,
		Status:        task.Status,
		CreatedAt:     task.CreatedAt,
		CreatedBy:     task.CreatedBy,
	}
	projected, authorized := taskForOperator(c, task).(db.Task)
	if !authorized {
		return row
	}
	row.Type = projected.Type
	row.Result = projected.Result
	row.Error = projected.Error
	command, _, err := taskWireArgs(task)
	if err != nil {
		slog.Error("Failed to resolve lateral history arguments", "task_id", task.ID, "error", err)
		return row
	}
	row.Method, row.Target = canonicalLateralMethodTarget(command)
	return row
}

// canonicalLateralMethodTarget accepts only the implant's canonical
// method|target|... wire form. JSON and other display strings are not a
// source of history metadata.
func canonicalLateralMethodTarget(command string) (method, target string) {
	parts := strings.SplitN(strings.TrimSpace(command), "|", 3)
	if len(parts) < 2 {
		return "", ""
	}
	method = strings.ToLower(strings.TrimSpace(parts[0]))
	target = strings.TrimSpace(parts[1])
	if method == "" || target == "" {
		return "", ""
	}
	return method, target
}

type lateralSpec struct {
	Source       string `json:"source"`
	Target       string `json:"target"`
	Method       string `json:"method"`
	CredentialID *uint  `json:"credential_id,omitempty"`
	Username     string `json:"username,omitempty"`
	Password     string `json:"password,omitempty"`
	Command      string `json:"command,omitempty"`
	Hash         string `json:"hash,omitempty"`
	KeyPath      string `json:"key_path,omitempty"`
	Port         string `json:"port,omitempty"`
	Share        string `json:"share,omitempty"`
	Namespace    string `json:"namespace,omitempty"`
	Pivot        string `json:"pivot,omitempty"`
}

// lateralAuditSummary renders a redacted summary of a lateral-movement spec
// for logs and the tamper-evident audit chain. Credential material
// (password/hash/credential/key_path/pivot) is never written to either sink.
//
// It accepts either wire format: the pipe form the implant parses
// ("type|target|user|pass|cmd") and a JSON object (the API form). Only the
// leading identifying fields are echoed; the password slot and the command are
// always dropped.
func lateralAuditSummary(spec string) string {
	if strings.TrimSpace(spec) == "" {
		return "lateral movement"
	}
	trimmed := strings.TrimSpace(spec)
	var method, target, user string
	if strings.HasPrefix(trimmed, "{") {
		var m map[string]interface{}
		if err := json.Unmarshal([]byte(trimmed), &m); err == nil {
			method, _ = m["method"].(string)
			target, _ = m["target"].(string)
			user, _ = m["username"].(string)
		} else {
			return fmt.Sprintf("lateral movement (spec %d bytes)", len(spec))
		}
	} else {
		parts := strings.SplitN(trimmed, "|", 5)
		if len(parts) < 3 {
			return fmt.Sprintf("lateral movement (spec %d bytes)", len(spec))
		}
		method, target, user = parts[0], parts[1], parts[2]
		// parts[3] is the password and parts[4] the command; both stay out.
	}
	method = strings.ToLower(strings.TrimSpace(method))
	target = strings.TrimSpace(target)
	user = strings.TrimSpace(user)
	if method == "" {
		method = "unknown"
	}
	return fmt.Sprintf("lateral movement: method=%s target=%s username=%s", method, target, user)
}

// encodeLateralSpec renders the wire format the implant parses:
// "type|target|user|pass|cmd" (internal/payload/agent/agent_windows.go,
// lateralMove). It was previously sent as JSON, which the agent could never
// parse — strings.SplitN on "|" yields one part, so every lateral task failed
// with "format: type|target|user|pass|cmd".
//
// The implant splits with SplitN(..., 5), so the command may safely contain
// pipes; the fields before it may not, or they would shift the parse.
func encodeLateralSpec(req lateralSpec) (string, error) {
	for name, v := range map[string]string{
		"method": req.Method, "target": req.Target,
		"username": req.Username, "password": req.Password,
	} {
		if strings.Contains(v, "|") {
			return "", fmt.Errorf("%s must not contain '|'", name)
		}
	}
	cmd := strings.TrimSpace(req.Command)
	if cmd == "" {
		cmd = "whoami" // matches the implant's own default
	}
	return strings.Join([]string{
		strings.ToLower(strings.TrimSpace(req.Method)),
		strings.TrimSpace(req.Target),
		strings.TrimSpace(req.Username),
		req.Password,
		cmd,
	}, "|"), nil
}

// handleAPILateralExecute dispatches a lateral movement task via JSON API
func (s *Server) handleAPILateralExecute(c *gin.Context) {
	if !s.requireOperator(c) {
		return
	}
	var req lateralSpec
	if err := c.ShouldBindJSON(&req); err != nil {
		respondError(c, http.StatusBadRequest, "invalid request")
		return
	}
	if req.Source == "" || req.Target == "" || req.Method == "" {
		respondError(c, http.StatusBadRequest, "source, target and method required")
		return
	}
	if req.CredentialID != nil && (req.Username != "" || req.Password != "" || req.Hash != "" || req.KeyPath != "") {
		respondError(c, http.StatusBadRequest, "credential reference tasks must not carry inline secrets")
		return
	}
	// The implant has no pivot slot, so accepting one would let the operator
	// believe traffic is tunnelled through an agent they chose when it is not.
	if strings.TrimSpace(req.Pivot) != "" {
		respondError(c, http.StatusBadRequest, "pivot is not supported by the implant")
		return
	}
	spec := ""
	taskSpec := TaskSpec{Type: "lateral"}
	if req.CredentialID != nil {
		taskSpec.CredentialID = req.CredentialID
		taskSpec.CredentialParams = CredentialTaskParams{
			Target: req.Target, Method: req.Method, Command: req.Command,
		}
	} else {
		encoded, err := encodeLateralSpec(req)
		if err != nil {
			respondError(c, http.StatusBadRequest, err.Error())
			return
		}
		spec = encoded
		taskSpec.Command = spec
	}
	task := s.issueAgentTask(c, req.Source, taskSpec)
	if task == nil {
		return // issueAgentTask already responded: 404 (wrong tenant), 409 (locked) or 400
	}
	slog.Info("Lateral movement via JSON API", "agent_id", req.Source, "target", req.Target, "method", req.Method)
	auditSpec := spec
	if task.CredentialID != nil {
		// Same separator contract as the wire spec: never let a field shift
		// the summary the operator reads in the audit chain.
		if credentialCommandHasSeparator(req.Method, req.Target, task.CredentialLabel) {
			auditSpec = "reference|redacted|user|"
		} else {
			auditSpec = req.Method + "|" + req.Target + "|" + task.CredentialLabel + "|"
		}
	}
	s.LogAuditRecord(c, "lateral", "agent", req.Source, lateralAuditSummary(auditSpec), true, nil)
	s.broadcastTaskUpdate(req.Source, *task)
	respond(c, gin.H{"task_id": task.ID})
}
