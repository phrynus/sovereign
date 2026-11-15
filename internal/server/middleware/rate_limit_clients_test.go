package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

// requestFrom sends one request through the limiter as if it came from ip.
func requestFrom(t *testing.T, rl *RateLimiter, ip string) int {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/th", nil)
	c.Request.RemoteAddr = ip + ":12345"
	rl.Limit()(c)
	return w.Code
}

// TestRateLimiterBudgetScalesWithTheClientsBehindAnAddress pins the NAT case:
// one egress address serving several agents must not be throttled as if it were
// a single flood, while an address with no known clients keeps the base budget.
func TestRateLimiterBudgetScalesWithTheClientsBehindAnAddress(t *testing.T) {
	rl := NewRateLimiter(context.Background(), 2, time.Minute)
	defer rl.Stop()
	const ip = "203.0.113.7"

	// Without a known client the base budget applies: 2 requests, then a 429.
	if code := requestFrom(t, rl, ip); code != http.StatusOK {
		t.Fatalf("first request = %d, want 200", code)
	}
	if code := requestFrom(t, rl, ip); code != http.StatusOK {
		t.Fatalf("second request = %d, want 200", code)
	}
	if code := requestFrom(t, rl, ip); code != http.StatusTooManyRequests {
		t.Fatalf("third request = %d, want 429 (the base budget is 2)", code)
	}

	// Two agents behind that address are now known: the budget doubles, and
	// neither of them is throttled for the other's traffic.
	rl2 := NewRateLimiter(context.Background(), 2, time.Minute)
	defer rl2.Stop()
	rl2.NoteClient(ip, "agent-1")
	rl2.NoteClient(ip, "agent-2")
	for i := 0; i < 4; i++ {
		if code := requestFrom(t, rl2, ip); code != http.StatusOK {
			t.Fatalf("request %d = %d, want 200: two agents share the address budget", i+1, code)
		}
	}
	if code := requestFrom(t, rl2, ip); code != http.StatusTooManyRequests {
		t.Fatalf("request 5 = %d, want 429 (the scaled budget is 4)", code)
	}
}

// TestRateLimiterClientBudgetIsCapped keeps one address from inflating its own
// budget without bound: the scale factor is bounded.
func TestRateLimiterClientBudgetIsCapped(t *testing.T) {
	rl := NewRateLimiter(context.Background(), 1, time.Minute)
	defer rl.Stop()
	const ip = "198.51.100.9"
	for i := 0; i < maxClientsPerIP+10; i++ {
		rl.NoteClient(ip, "agent-"+string(rune('a'+i%26))+string(rune('a'+i/26)))
	}
	rl.mu.Lock()
	clients := len(rl.clients[ip])
	budget := rl.limitFor(ip, time.Now())
	rl.mu.Unlock()
	if clients > maxClientsPerIP {
		t.Fatalf("clients = %d, want at most %d", clients, maxClientsPerIP)
	}
	if budget > maxClientsPerIP {
		t.Fatalf("budget = %d, want at most %d", budget, maxClientsPerIP)
	}
}

// TestRateLimiterForgetsIdleClients keeps the client map from growing forever:
// a client that stopped checking in stops counting towards the budget.
func TestRateLimiterForgetsIdleClients(t *testing.T) {
	rl := NewRateLimiter(context.Background(), 2, 10*time.Millisecond)
	defer rl.Stop()
	const ip = "192.0.2.4"
	rl.NoteClient(ip, "agent-1")
	rl.mu.Lock()
	before := len(rl.clients[ip])
	rl.mu.Unlock()
	if before != 1 {
		t.Fatalf("clients = %d, want 1", before)
	}
	time.Sleep(30 * time.Millisecond)
	rl.mu.Lock()
	after := rl.limitFor(ip, time.Now())
	rl.mu.Unlock()
	if after != 2 {
		t.Fatalf("budget = %d, want the base budget 2 once the client went idle", after)
	}
}
