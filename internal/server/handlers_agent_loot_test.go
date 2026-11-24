package server

import (
	"bytes"
	"encoding/json"
	"github.com/r0si/sovereign/internal/db"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestNewLootScreenshot(t *testing.T) {
	mod := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	got := newLootScreenshot("agent-1", "desk.png", mod)
	if got.ID != "screenshot:agent-1:desk.png" {
		t.Fatalf("id=%s", got.ID)
	}
	if got.AgentID != "agent-1" || got.Filename != "desk.png" || got.Path != "agent-1/desk.png" {
		t.Fatalf("dto=%+v", got)
	}
	if got.CreatedAt != "2026-08-14T12:00:00Z" {
		t.Fatalf("created_at=%s", got.CreatedAt)
	}
}

func TestNewLootTaskIncludesHostname(t *testing.T) {
	got := newLootTask(db.Task{
		ID:      7,
		AgentID: "agent-1",
		Type:    "upload",
		Command: `C:\secret.txt`,
		Status:  "completed",
		Result:  "File chunk saved: secret.txt offset 0 (12 bytes)",
		Agent:   db.Implant{Hostname: "WS-01"},
	})
	if got.Hostname != "WS-01" {
		t.Fatalf("hostname=%s", got.Hostname)
	}
	if got.Type != "upload" || got.ID != 7 {
		t.Fatalf("dto=%+v", got)
	}
}

// TestLootPageDoesNotShipAgentRows pins the payload fix: the page derives its
// agent filter from the loot rows, and the old "agents" array shipped up to
// 5 000 full implant models (every zero-valued column included) that nothing
// read. Only the tenant gate needs the ids, and they stay server-side.
func TestLootPageDoesNotShipAgentRows(t *testing.T) {
	s := newIDORTestServer(t)
	seedIDORTenants(t, s)

	c, w := opCtx(http.MethodGet, "/x", "alice-op", "", nil)
	s.handleLootPage(c)
	if w.Code != http.StatusOK {
		t.Fatalf("loot page: expected 200, got %d; body=%s", w.Code, w.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode loot payload: %v", err)
	}
	if _, ok := payload["agents"]; ok {
		t.Fatal("loot payload still carries the unused agents array")
	}
}

func TestLootPageListsCompletedPullTransfers(t *testing.T) {
	s := mustTenantServer(t)
	if err := s.db.Create(&db.Implant{ID: "loot-agent", TenantID: 1, Hostname: "LOOT"}).Error; err != nil {
		t.Fatal(err)
	}
	ft := db.FileTransfer{
		TenantID: 1, AgentID: "loot-agent", CreatedBy: "operator", Direction: "pull",
		RemotePath: `C:\secret\loot.bin`, PathKey: strings.Repeat("a", 64), Filename: "loot.bin",
		TotalSize: 12, CommittedOffset: 12, ChunkSize: FileTransferChunkSize,
		SHA256: strings.Repeat("b", 64), PrevMAC: strings.Repeat("0", 64), Status: "completed",
	}
	if err := s.db.Create(&ft).Error; err != nil {
		t.Fatal(err)
	}
	c, w := tenantScopedAdminContext(s, t, "viewer", 1)
	c.Request, _ = http.NewRequest(http.MethodGet, "/loot", nil)
	s.handleLootPage(c)
	if w.Code != http.StatusOK {
		t.Fatalf("loot page: %d %s", w.Code, w.Body.String())
	}
	var payload struct {
		CompletedTransfers []lootTaskDTO `json:"completed_transfers"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.CompletedTransfers) != 1 || payload.CompletedTransfers[0].TransferID == nil ||
		*payload.CompletedTransfers[0].TransferID != ft.ID || payload.CompletedTransfers[0].Filename != "loot.bin" {
		t.Fatalf("completed transfers = %+v", payload.CompletedTransfers)
	}
}

func TestLootDeleteCompletedTransferIsTenantScoped(t *testing.T) {
	s := mustTenantServer(t)
	s.cfg.Server.DataDir = t.TempDir()
	if err := s.db.Create(&db.Implant{ID: "loot-agent", TenantID: 1, Hostname: "LOOT"}).Error; err != nil {
		t.Fatal(err)
	}
	ft := db.FileTransfer{
		TenantID: 1, AgentID: "loot-agent", Direction: "pull", RemotePath: `C:\loot.bin`,
		PathKey: strings.Repeat("c", 64), Filename: "loot.bin", Status: "completed",
		CommittedOffset: 4, PrevMAC: strings.Repeat("0", 64),
	}
	if err := s.db.Create(&ft).Error; err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(s.cfg.Server.DataDir, "transfers")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	finalPath := transferFinalPath(dir, ft.Filename, ft.ID)
	if err := os.WriteFile(finalPath, []byte("loot"), 0600); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]any{"ids": []string{"download:transfer:" + strconv.FormatUint(uint64(ft.ID), 10)}})
	foreign, fw := tenantScopedAdminContext(s, t, "foreign", 2)
	foreign.Request, _ = http.NewRequest(http.MethodPost, "/loot/bulk-delete", bytes.NewReader(body))
	foreign.Request.Header.Set("Content-Type", "application/json")
	s.handleLootBulkDelete(foreign)
	if fw.Code != http.StatusOK {
		t.Fatalf("foreign delete: %d", fw.Code)
	}
	var still int64
	s.db.Model(&db.FileTransfer{}).Where("id = ?", ft.ID).Count(&still)
	if still != 1 {
		t.Fatal("foreign tenant deleted the transfer")
	}
	owner, ow := tenantScopedAdminContext(s, t, "owner", 1)
	owner.Request, _ = http.NewRequest(http.MethodPost, "/loot/bulk-delete", bytes.NewReader(body))
	owner.Request.Header.Set("Content-Type", "application/json")
	s.handleLootBulkDelete(owner)
	if ow.Code != http.StatusOK {
		t.Fatalf("owner delete: %d", ow.Code)
	}
	s.db.Model(&db.FileTransfer{}).Where("id = ?", ft.ID).Count(&still)
	if still != 0 {
		t.Fatal("owner delete did not remove the transfer row")
	}
	if _, err := os.Stat(finalPath); !os.IsNotExist(err) {
		t.Fatalf("final transfer file still exists: %v", err)
	}
}
