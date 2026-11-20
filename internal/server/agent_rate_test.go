package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/r0si/sovereign/internal/server/middleware"
)

// TestAgentRateLimiterIsPerAgent pins the per-agent budget: one agent that
// checks in too fast is refused on its own, and the other agents behind the
// same address keep their allowance.
func TestAgentRateLimiterIsPerAgent(t *testing.T) {
	limiter := newAgentRateLimiter(2, time.Minute)

	for i := 0; i < 2; i++ {
		if !limiter.Allow("agent-1") {
			t.Fatalf("agent-1 check-in %d was refused inside its budget", i+1)
		}
	}
	if limiter.Allow("agent-1") {
		t.Fatal("agent-1 was allowed past its own budget")
	}
	if !limiter.Allow("agent-2") {
		t.Fatal("agent-2 was refused by agent-1's traffic")
	}
	if got := limiter.Limited(); got != 1 {
		t.Fatalf("refused count = %d, want 1", got)
	}
}

// TestAgentRateLimiterWindowResets keeps a throttled agent from staying
// throttled: the budget is per window, not per process.
func TestAgentRateLimiterWindowResets(t *testing.T) {
	limiter := newAgentRateLimiter(1, 10*time.Millisecond)
	if !limiter.Allow("agent-1") {
		t.Fatal("the first check-in was refused")
	}
	if limiter.Allow("agent-1") {
		t.Fatal("the second check-in was allowed inside a one-per-window budget")
	}
	time.Sleep(25 * time.Millisecond)
	if !limiter.Allow("agent-1") {
		t.Fatal("the window never reset")
	}
}

// beaconRequestCode sends one request through the server's address limiter as
// if it came from ip.
func beaconRequestCode(t *testing.T, s *Server, ip string) int {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/th", nil)
	c.Request.RemoteAddr = ip + ":12345"
	s.rateLimiter.Limit()(c)
	return w.Code
}

// TestBeaconClientsBehindOneAddressAreNotThrottledTogether is the NAT
// regression test: two agents behind one egress address must not spend one
// shared budget, or a whole fleet "goes offline" while the server is idle.
func TestBeaconClientsBehindOneAddressAreNotThrottledTogether(t *testing.T) {
	s := &Server{rateLimiter: middleware.NewRateLimiter(context.Background(), 1, time.Minute)}
	defer s.rateLimiter.Stop()

	const ip = "10.0.0.9"
	s.noteBeaconClient(ip, "agent-1")
	s.noteBeaconClient(ip, "agent-2")

	// The address serves two agents, so its budget is two requests.
	if code := beaconRequestCode(t, s, ip); code != http.StatusOK {
		t.Fatalf("request 1 = %d, want 200", code)
	}
	if code := beaconRequestCode(t, s, ip); code != http.StatusOK {
		t.Fatalf("request 2 = %d, want 200 (two agents share this address)", code)
	}
	if code := beaconRequestCode(t, s, ip); code != http.StatusTooManyRequests {
		t.Fatalf("request 3 = %d, want 429 (the scaled budget is 2)", code)
	}
}

// TestBeaconAddressWithoutKnownClientsKeepsTheBaseBudget proves the flood
// protection still exists: an address that has never identified an agent gets
// the base budget, so an unauthenticated flood is cut.
func TestBeaconAddressWithoutKnownClientsKeepsTheBaseBudget(t *testing.T) {
	s := &Server{rateLimiter: middleware.NewRateLimiter(context.Background(), 1, time.Minute)}
	defer s.rateLimiter.Stop()

	const ip = "10.0.0.10"
	if code := beaconRequestCode(t, s, ip); code != http.StatusOK {
		t.Fatalf("request 1 = %d, want 200", code)
	}
	if code := beaconRequestCode(t, s, ip); code != http.StatusTooManyRequests {
		t.Fatalf("request 2 = %d, want 429 (the base budget is 1)", code)
	}
}
