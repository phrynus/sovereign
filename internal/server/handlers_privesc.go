package server

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/r0si/sovereign/internal/db"
	"github.com/r0si/sovereign/internal/server/middleware"
	"gorm.io/gorm"
)

// handlePrivescCheck handles privilege escalation reconnaissance
func (s *Server) handlePrivescCheck(c *gin.Context) {
	if !s.requireOperator(c) {
		return
	}
	agentID := c.Param("id")

	var req struct {
		CheckType string `json:"check_type"` // all, windows, linux, cve_match
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		req.CheckType = c.PostForm("check_type")
		if req.CheckType == "" {
			req.CheckType = "all"
		}
	}

	// Route through issueAgentTask like every other agent-facing dispatch:
	// the bare Create used to skip the tenant scope, the agent-existence
	// check and the approval/lock gates inside createTask.
	task := s.issueAgentTask(c, agentID, TaskSpec{Type: "privesc_check", Command: fmt.Sprintf("privesc_check:%s", req.CheckType)})
	if task == nil {
		return
	}

	// Log audit
	s.LogAuditRecord(c, "privilege_escalation_check",
		fmt.Sprintf("Privilege escalation check task %d created (type: %s)", task.ID, req.CheckType),
		agentID, fmt.Sprintf("Check type: %s", req.CheckType),
		true, nil)

	respond(c, gin.H{

		"task_id": task.ID,
		"message": fmt.Sprintf("Privilege escalation check started (type: %s)", req.CheckType),
	})
}

// handlePrivescPage renders the privilege escalation page
func (s *Server) handlePrivescPage(c *gin.Context) {
	stats := s.getNavStats(c)

	var agents []db.Implant
	if err := s.tenantScope(s.db, c).Where("status = 'online'").Limit(5000).Find(&agents).Error; err != nil {
		respondError(c, http.StatusInternalServerError, "failed to query agents")
		return
	}

	var tasks []db.Task
	if err := s.tenantScope(s.db, c).Where("type IN ?", []string{"privesc_check", "privesc_execute"}).
		Order("created_at desc").Limit(50).Find(&tasks).Error; err != nil {
		respondError(c, http.StatusInternalServerError, "failed to query privesc history")
		return
	}

	data := gin.H{
		"Title":          "Sovereign - Privilege Escalation",
		"ActiveNav":      "privesc",
		"Stats":          stats,
		"Agents":         agents,
		"History":        s.buildPrivescHistory(c, tasks),
		"tasks_redacted": !middleware.HasPermission(c, db.PermTasksRead),
	}
	if middleware.HasPermission(c, db.PermTasksRead) {
		data["Findings"] = s.buildPrivescFindings(tasks)
	}
	s.renderPageOrJSON(c, data)
}

// privescHistoryEntry is the JSON shape the frontend privesc page expects for history rows.
type privescHistoryEntry struct {
	ID            uint      `json:"id"`
	AgentID       string    `json:"agent_id"`
	CheckType     string    `json:"check_type,omitempty"`
	Status        string    `json:"status"`
	Result        string    `json:"result,omitempty"`
	FindingsCount *int      `json:"findings_count,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
}

// handlePrivescExecute was removed: privesc_execute was never a registered
// protocol task type, so the task it created could not be dispatched to any
// implant (and the UI never received an exploit_command to send, since
// findings carry only reference URLs). Routing it through issueAgentTask
// exposed the phantom type; the honest fix is to delete the endpoint rather
// than keep an operation that claimed "exploit task created" for a task that
// could never run. A real exploit-execution surface needs a proper TaskSpec
// plus an agent handler, not an unregistered type.

// buildPrivescHistory maps privesc tasks to the frontend history shape.
func (s *Server) buildPrivescHistory(c *gin.Context, tasks []db.Task) []privescHistoryEntry {
	out := make([]privescHistoryEntry, 0, len(tasks))
	for _, t := range tasks {
		entry := privescHistoryEntry{
			ID:        t.ID,
			AgentID:   t.AgentID,
			Status:    t.Status,
			CreatedAt: t.CreatedAt,
		}
		if projected, ok := taskForOperator(c, t).(db.Task); ok {
			entry.CheckType = strings.TrimPrefix(projected.Command, "privesc_check:")
			if entry.CheckType == projected.Command {
				entry.CheckType = "all"
			}
			entry.Result = projected.Result
			findingsCount := len(s.parsePrivescFindings(projected.Result))
			entry.FindingsCount = &findingsCount
		}
		out = append(out, entry)
	}
	return out
}

// handlePrivescHistory returns privesc check history for a single task.
func (s *Server) handlePrivescHistory(c *gin.Context) {
	taskID, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || taskID == 0 {
		respondError(c, http.StatusNotFound, "privesc task not found")
		return
	}

	var task db.Task
	if err := s.tenantScope(s.db, c).
		Where("id = ? AND type IN ?", taskID, []string{"privesc_check", "privesc_execute"}).First(&task).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			respondError(c, http.StatusNotFound, "privesc task not found")
		} else {
			respondError(c, http.StatusInternalServerError, "failed to query privesc task")
		}
		return
	}

	payload := gin.H{
		"tasks":          s.buildPrivescHistory(c, []db.Task{task}),
		"total":          1,
		"tasks_redacted": !middleware.HasPermission(c, db.PermTasksRead),
	}
	if middleware.HasPermission(c, db.PermTasksRead) {
		payload["findings"] = s.parsePrivescFindings(task.Result)
	}
	respond(c, payload)
}

// buildPrivescFindings collects parsed findings across a set of privesc tasks.
func (s *Server) buildPrivescFindings(tasks []db.Task) []gin.H {
	out := make([]gin.H, 0)
	for _, t := range tasks {
		out = append(out, s.parsePrivescFindings(t.Result)...)
	}
	return out
}

// parsePrivescFindings best-effort parses structured findings from agent privesc result text.
func (s *Server) parsePrivescFindings(result string) []gin.H {
	findings := make([]gin.H, 0)
	lines := strings.Split(result, "\n")

	var currentCVE string
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)

		// Track CVE section headers: "--- CVE-2021-4034 (pkexec/pwnkit) ---"
		if strings.HasPrefix(trimmed, "--- CVE-") {
			rest := strings.TrimPrefix(trimmed, "---")
			rest = strings.TrimSuffix(rest, "---")
			fields := strings.SplitN(strings.TrimSpace(rest), " ", 2)
			if len(fields) > 0 && strings.HasPrefix(fields[0], "CVE-") {
				currentCVE = strings.TrimRight(fields[0], ":")
				continue
			}
			continue
		}

		isFinding := strings.Contains(trimmed, "[!]") ||
			strings.Contains(trimmed, "WRITABLE") ||
			strings.Contains(trimmed, "vulnerable") ||
			strings.Contains(trimmed, "potentially vulnerable")

		if !isFinding || strings.TrimSpace(trimmed) == "" {
			continue
		}

		severity := "high"
		if strings.Contains(trimmed, "WRITABLE") || strings.Contains(trimmed, "possible") {
			severity = "medium"
		}
		if strings.Contains(trimmed, "potentially vulnerable") {
			severity = "medium"
		}

		title := strings.Trim(strings.TrimPrefix(trimmed, "[!]"), " -")
		if currentCVE != "" {
			title = fmt.Sprintf("%s (%s)", currentCVE, title)
		}

		findings = append(findings, gin.H{
			"title":          title,
			"severity":       severity,
			"cve_id":         currentCVE,
			"description":    trimmed,
			"recommendation": "",
		})
	}
	return findings
}

// handlePrivescRun creates a privesc check task for the given agent.
func (s *Server) handlePrivescRun(c *gin.Context) {
	if !s.requireOperator(c) {
		return
	}
	var req struct {
		AgentID   string `json:"agent_id"`
		CheckType string `json:"check_type"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.AgentID == "" {
		respondError(c, http.StatusBadRequest, "agent_id required")
		return
	}
	if req.CheckType == "" {
		req.CheckType = "all"
	}
	// issueAgentTask validates the agent (tenant scope included) and routes
	// through the same gates as every other task-issuing endpoint; the bare
	// Create used to accept any agent id from any tenant.
	task := s.issueAgentTask(c, req.AgentID, TaskSpec{Type: "privesc_check", Command: fmt.Sprintf("privesc_check:%s", req.CheckType)})
	if task == nil {
		return
	}
	s.LogAuditRecord(c, "privesc_check", "agent", req.AgentID,
		fmt.Sprintf("Privesc check task created (type: %s)", req.CheckType), true, nil)
	respond(c, gin.H{"task_id": task.ID})
}

// handlePrivescExecute was removed 2026-10: privesc_execute was never a
// registered protocol task type, so the row it created could not be
// dispatched to any implant, and no finding ever carried an exploit_command
// for the UI to send (agent output offers reference URLs only). Routing it
// through issueAgentTask exposed the phantom type as a 400; the honest fix
// is deleting the endpoint rather than keeping an operation that claims
// "exploit task created" for a task that can never run. A real
// exploit-execution surface needs a proper TaskSpec plus an agent handler.
