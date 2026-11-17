package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/gin-gonic/gin"
)

func TestValidateTransportMigration(t *testing.T) {
	cases := []struct {
		transport string
		url       string
		wantErr   bool
	}{
		{"tcp", "tcp://127.0.0.1:4444", false},
		{"http", "https://c2.example/collect", false},
		{"wss", "wss://c2.example/ws", false},
		{"udp", "", false},
		{"tcp", "https://c2.example/", true}, // scheme/transport mismatch
		{"nope", "", true},
		{"tcp", "not-a-url", true},
		{"http", "ftp://c2.example/x", true},
	}
	for _, c := range cases {
		m, err := validateTransportMigration(c.transport, c.url)
		if (err != nil) != c.wantErr {
			t.Fatalf("validate(%q,%q) err=%v, wantErr=%v", c.transport, c.url, err, c.wantErr)
		}
		if err == nil && m.Transport == "" {
			t.Fatal("valid migration had an empty transport")
		}
	}
}

func TestPendingTransportMigrationIsOneShotWithTTL(t *testing.T) {
	const agentID = "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"
	pendingMigrations.mu.Lock()
	delete(pendingMigrations.items, agentID)
	pendingMigrations.mu.Unlock()
	t.Cleanup(func() {
		pendingMigrations.mu.Lock()
		delete(pendingMigrations.items, agentID)
		pendingMigrations.mu.Unlock()
	})

	if err := setPendingTransportMigration(agentID, "tcp", "tcp://127.0.0.1:4444"); err != nil {
		t.Fatalf("queue: %v", err)
	}
	if got := takePendingTransportMigration(agentID); got == nil || got.Transport != "tcp" || got.C2URL != "tcp://127.0.0.1:4444" {
		t.Fatalf("taken migration = %+v", got)
	}
	if got := takePendingTransportMigration(agentID); got != nil {
		t.Fatalf("migration was delivered twice: %+v", got)
	}

	// A stale command is dropped rather than delivered late.
	if err := setPendingTransportMigration(agentID, "udp", ""); err != nil {
		t.Fatalf("queue stale: %v", err)
	}
	pendingMigrations.mu.Lock()
	pendingMigrations.items[agentID] = pendingTransportMigration{
		migration: pendingMigrations.items[agentID].migration,
		at:        time.Now().Add(-transportMigrationTTL - time.Second),
	}
	pendingMigrations.mu.Unlock()
	if got := takePendingTransportMigration(agentID); got != nil {
		t.Fatalf("expired migration was delivered: %+v", got)
	}
}

func TestTransportMigrationQueuedOnNextBeacon(t *testing.T) {
	s, _ := v2TestServer(t)
	const agentID = "cccccccc-dddd-4eee-8fff-000000000000"
	if err := setPendingTransportMigration(agentID, "tcp", "tcp://127.0.0.1:4444"); err != nil {
		t.Fatalf("queue: %v", err)
	}
	t.Cleanup(func() {
		pendingMigrations.mu.Lock()
		delete(pendingMigrations.items, agentID)
		pendingMigrations.mu.Unlock()
	})

	first := s.processBeaconWithBudget(beaconRequest{UUID: agentID}, "", 0, 0)
	if first.TransportMigrate == nil || first.TransportMigrate.Transport != "tcp" {
		t.Fatalf("first response missing migration: %+v", first.TransportMigrate)
	}
	second := s.processBeaconWithBudget(beaconRequest{UUID: agentID}, "", 0, 0)
	if second.TransportMigrate != nil {
		t.Fatalf("migration repeated on the next beacon: %+v", second.TransportMigrate)
	}
}

func TestAgentTransportMigrationRoute(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := mustTenantServer(t)
	seedTenantAgent(t, s, "rd-migrate", 1)

	call := func(body string, userID uint, role string) *httptest.ResponseRecorder {
		t.Helper()
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		req, _ := http.NewRequest(http.MethodPost, "/api/agents/rd-migrate/transport", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		c.Request = req
		c.Params = gin.Params{{Key: "id", Value: "rd-migrate"}}
		c.Set("user_role", role)
		c.Set("user_id", userID)
		s.handleAgentTransportMigration(c)
		return w
	}

	w := call(`{"transport":"tcp","c2_url":"tcp://127.0.0.1:4444"}`, 7, db.RoleAdmin)
	if w.Code != http.StatusOK {
		t.Fatalf("queue = %d: %s", w.Code, w.Body.String())
	}
	var envelope struct {
		Data struct {
			Queued    bool   `json:"queued"`
			Transport string `json:"transport"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil || !envelope.Data.Queued || envelope.Data.Transport != "tcp" {
		t.Fatalf("response = %s err=%v", w.Body.String(), err)
	}
	if bad := call(`{"transport":"nope"}`, 7, db.RoleAdmin); bad.Code != http.StatusBadRequest {
		t.Fatalf("invalid transport = %d, want 400", bad.Code)
	}
	// A viewer cannot queue an operator mutation.
	if denied := call(`{"transport":"tcp"}`, 9, "viewer"); denied.Code != http.StatusForbidden {
		t.Fatalf("viewer queue = %d, want 403", denied.Code)
	}
}
