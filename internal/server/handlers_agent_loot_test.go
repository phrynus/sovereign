package server

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/r0si/sovereign/internal/db"
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
