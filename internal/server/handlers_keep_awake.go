package server

import (
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/r0si/sovereign/pkg/protocol"
)

// handleKeepAwake queues the agent's bounded sleep-prevention task. It is a
// normal write action (agents.write) and goes through issueAgentTask so the
// command, approval and audit semantics match every other task. "0" releases
// an active request early; empty means the agent-side default (300 s).
func (s *Server) handleKeepAwake(c *gin.Context) {
	id := c.Param("id")
	if _, ok := s.getAgentOrFail(c, id); !ok {
		return
	}

	raw := strings.TrimSpace(c.PostForm("seconds"))
	if strings.Contains(c.ContentType(), "application/json") {
		var req struct {
			Seconds *int `json:"seconds"`
		}
		if err := c.ShouldBindJSON(&req); err == nil && req.Seconds != nil {
			raw = strconv.Itoa(*req.Seconds)
		}
	}
	seconds := protocol.KeepAwakeDefaultSeconds
	if raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 0 || parsed > protocol.KeepAwakeMaxSeconds {
			respondError(c, http.StatusBadRequest,
				"invalid seconds: 0 (release) or 1-"+strconv.Itoa(protocol.KeepAwakeMaxSeconds))
			return
		}
		seconds = parsed
	}

	cmd := strconv.Itoa(seconds)
	task := s.issueAgentTask(c, id, TaskSpec{Type: protocol.TaskTypeKeepAwake, Command: cmd})
	if task == nil {
		return
	}
	slog.Info("Keep awake requested", "agent_id", id, "seconds", seconds)
	s.dispatchTask(c, task, "keep_awake", cmd)
}
