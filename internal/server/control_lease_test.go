package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/r0si/sovereign/internal/db"
)

// TestControlLeaseExpiryAndTakeover pins the arbitration rules: the first
// operator owns the beacon, a second is refused by name, an idle lease expires,
// and an explicit take-over transfers it.
func TestControlLeaseExpiryAndTakeover(t *testing.T) {
	s := &Server{}
	start := time.Now()

	holder, ok := s.claimControlAt("agent-1", 1, "alice", false, start)
	if !ok || holder.Username != "alice" {
		t.Fatalf("first claim = %+v ok=%v", holder, ok)
	}
	holder, ok = s.claimControlAt("agent-1", 2, "bob", false, start.Add(10*time.Second))
	if ok {
		t.Fatal("a second operator claimed a live lease without force")
	}
	if holder.Username != "alice" {
		t.Fatalf("refusal named %q, want alice", holder.Username)
	}

	// Renewal keeps the same holder and does not extend anyone else's lease.
	if _, ok := s.claimControlAt("agent-1", 1, "alice", false, start.Add(50*time.Second)); !ok {
		t.Fatal("the holder could not renew")
	}
	if _, ok := s.claimControlAt("agent-1", 2, "bob", false, start.Add(70*time.Second)); ok {
		t.Fatal("a renewed lease was stolen by the TTL")
	}

	// An idle lease frees the beacon.
	if _, ok := s.claimControlAt("agent-1", 2, "bob", false, start.Add(50*time.Second+ControlLeaseTTL+time.Second)); !ok {
		t.Fatal("an expired lease was not claimable")
	}

	// Force transfers it even while live, and names the previous holder.
	s2 := &Server{}
	s2.claimControlAt("agent-2", 1, "alice", false, start)
	holder, ok = s2.claimControlAt("agent-2", 2, "bob", true, start.Add(time.Second))
	if !ok || holder.Username != "bob" || !holder.Since.Equal(start.Add(time.Second)) {
		t.Fatalf("forced takeover = %+v ok=%v", holder, ok)
	}

	// A stale tab cannot release a lease it no longer holds.
	if s2.releaseControlAt("agent-2", 1, start.Add(2*time.Second)) {
		t.Fatal("the previous holder released the new holder's lease")
	}
	if !s2.releaseControlAt("agent-2", 2, start.Add(2*time.Second)) {
		t.Fatal("the holder could not release")
	}
	if _, held := s2.controlHolderAt("agent-2", start.Add(2*time.Second)); held {
		t.Fatal("the lease survived its release")
	}
}

// TestAgentControlAPI covers the read/take/release surface the console badge
// uses, including "release by a non-holder is a no-op".
func TestAgentControlAPI(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := mustTenantServer(t)
	agent := seedImplant(t, s.db)

	call := func(method, body string, userID uint, username string) *httptest.ResponseRecorder {
		t.Helper()
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request, _ = http.NewRequest(method, "/agents/"+agent.ID+"/control", strings.NewReader(body))
		if body != "" {
			c.Request.Header.Set("Content-Type", "application/json")
		}
		c.Params = gin.Params{{Key: "id", Value: agent.ID}}
		c.Set("user_role", "admin")
		c.Set("user_id", userID)
		c.Set("user", username)
		s.handleAgentControl(c)
		return w
	}

	var envelope struct {
		Data struct {
			Held   bool   `json:"held"`
			Holder string `json:"holder"`
			You    bool   `json:"you"`
		} `json:"data"`
	}
	state := &envelope.Data
	read := func(userID uint, username string) {
		t.Helper()
		w := call(http.MethodGet, "", userID, username)
		if w.Code != http.StatusOK {
			t.Fatalf("GET control = %d: %s", w.Code, w.Body.String())
		}
		if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil {
			t.Fatalf("decode: %v (%s)", err, w.Body.String())
		}
	}

	read(1, "alice")
	if state.Held {
		t.Fatalf("a fresh beacon reported a holder: %+v", state)
	}

	if w := call(http.MethodPost, `{"action":"take"}`, 1, "alice"); w.Code != http.StatusOK {
		t.Fatalf("take = %d: %s", w.Code, w.Body.String())
	}
	read(2, "bob")
	if !state.Held || state.Holder != "alice" || state.You {
		t.Fatalf("holder view for bob = %+v", state)
	}
	read(1, "alice")
	if !state.You {
		t.Fatalf("the holder does not see itself as you: %+v", state)
	}

	// A non-holder's release is a no-op.
	if w := call(http.MethodPost, `{"action":"release"}`, 2, "bob"); w.Code != http.StatusOK {
		t.Fatalf("release by non-holder = %d: %s", w.Code, w.Body.String())
	}
	read(2, "bob")
	if !state.Held {
		t.Fatal("a non-holder released the lease")
	}
	if w := call(http.MethodPost, `{"action":"release"}`, 1, "alice"); w.Code != http.StatusOK {
		t.Fatalf("release = %d: %s", w.Code, w.Body.String())
	}
	read(1, "alice")
	if state.Held {
		t.Fatal("the lease survived the holder's release")
	}
	if w := call(http.MethodPost, `{"action":"steal"}`, 1, "alice"); w.Code != http.StatusBadRequest {
		t.Fatalf("unknown action = %d, want 400", w.Code)
	}
}

// TestAgentRemoteInputRequiresControl proves the task input path claims the
// lease, refuses a second operator by name, and lets an explicit take-over
// unblock them.
func TestAgentRemoteInputRequiresControl(t *testing.T) {
	gin.SetMode(gin.TestMode)
	database := newContractDB(t)
	srv := newTestFileServer(t, database)
	agent := seedImplant(t, database)

	send := func(body string, userID uint, username string) *httptest.ResponseRecorder {
		t.Helper()
		w, c := newRemoteInputContext(agent.ID, body)
		c.Set("user_id", userID)
		c.Set("user", username)
		srv.handleAgentRemoteInput(c)
		// Keep the per-agent queue from filling up across the assertions.
		database.Model(&db.Task{}).Where("agent_id = ?", agent.ID).Update("status", "completed")
		return w
	}

	if w := send(`{"type":"click","x":1,"y":2}`, 1, "alice"); w.Code != http.StatusOK {
		t.Fatalf("alice's first input = %d: %s", w.Code, w.Body.String())
	}
	w := send(`{"type":"click","x":3,"y":4}`, 2, "bob")
	if w.Code != http.StatusConflict {
		t.Fatalf("bob's input = %d, want 409: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "alice") {
		t.Fatalf("the refusal does not name the holder: %s", w.Body.String())
	}

	// Bob takes over through the control API, then his input is accepted.
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request, _ = http.NewRequest(http.MethodPost, "/agents/"+agent.ID+"/control", strings.NewReader(`{"action":"take"}`))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Params = gin.Params{{Key: "id", Value: agent.ID}}
	c.Set("user_role", "admin")
	c.Set("user_id", uint(2))
	c.Set("user", "bob")
	srv.handleAgentControl(c)
	if rec.Code != http.StatusOK {
		t.Fatalf("bob take = %d: %s", rec.Code, rec.Body.String())
	}
	if w := send(`{"type":"click","x":5,"y":6}`, 2, "bob"); w.Code != http.StatusOK {
		t.Fatalf("bob's input after take-over = %d: %s", w.Code, w.Body.String())
	}
	if w := send(`{"type":"click","x":7,"y":8}`, 1, "alice"); w.Code != http.StatusConflict {
		t.Fatalf("alice's input after losing control = %d, want 409", w.Code)
	}
}
