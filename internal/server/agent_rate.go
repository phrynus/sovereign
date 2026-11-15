package server

import (
	"sync"
	"sync/atomic"
	"time"
)

// agentRateLimiter bounds one agent's own check-ins.
//
// The address limiter protects the server from a flood, but it cannot tell a
// single misbehaving agent from a hundred well-behaved ones behind the same
// NAT address. This one is per agent: an agent that checks in faster than its
// own budget is refused on its own, and the other agents behind that address
// keep their allowance.
//
// The implant treats 429 as retryable and honours Retry-After, so a refusal
// makes the offending agent back off instead of failing.
type agentRateLimiter struct {
	mu        sync.Mutex
	agents    map[string]*agentRateEntry
	limit     int
	window    time.Duration
	maxAgents int
	limited   atomic.Uint64
}

type agentRateEntry struct {
	windowStart time.Time
	count       int
}

const defaultMaxTrackedAgents = 100000

func newAgentRateLimiter(limit int, window time.Duration) *agentRateLimiter {
	if limit <= 0 {
		limit = BeaconRateLimit
	}
	if window <= 0 {
		window = BeaconRateWindow
	}
	return &agentRateLimiter{
		agents:    make(map[string]*agentRateEntry),
		limit:     limit,
		window:    window,
		maxAgents: defaultMaxTrackedAgents,
	}
}

// Allow reports whether the agent may check in now, and counts the request
// when it may.
func (l *agentRateLimiter) Allow(agentID string) bool {
	if l == nil || agentID == "" {
		return true
	}
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()

	entry, known := l.agents[agentID]
	if !known {
		if len(l.agents) >= l.maxAgents {
			l.evictOldestLocked(now)
		}
		l.agents[agentID] = &agentRateEntry{windowStart: now, count: 1}
		return true
	}
	if now.Sub(entry.windowStart) > l.window {
		entry.windowStart = now
		entry.count = 1
		return true
	}
	if entry.count >= l.limit {
		l.limited.Add(1)
		return false
	}
	entry.count++
	return true
}

// evictOldestLocked drops the agent that was seen longest ago, so a burst of
// new agents cannot evict the busy ones.
func (l *agentRateLimiter) evictOldestLocked(now time.Time) {
	oldestID := ""
	var oldest time.Time
	for id, entry := range l.agents {
		if oldestID == "" || entry.windowStart.Before(oldest) {
			oldestID = id
			oldest = entry.windowStart
		}
	}
	if oldestID != "" {
		delete(l.agents, oldestID)
	}
	_ = now
}

// SetLimits applies a hot-reloaded budget. Counters are preserved; the new
// limit applies from the next request.
func (l *agentRateLimiter) SetLimits(limit int, window time.Duration) {
	if l == nil || limit <= 0 || window <= 0 {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.limit = limit
	l.window = window
}

// Limited returns how many agent check-ins were refused since the process
// started.
func (l *agentRateLimiter) Limited() uint64 {
	if l == nil {
		return 0
	}
	return l.limited.Load()
}

// Tracked returns how many agents currently have a counter.
func (l *agentRateLimiter) Tracked() int {
	if l == nil {
		return 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.agents)
}

// noteBeaconClient tells the address limiter which agent lives behind an
// address, so the address budget scales with the number of agents it serves.
func (s *Server) noteBeaconClient(ip, agentID string) {
	if s == nil || s.rateLimiter == nil {
		return
	}
	s.rateLimiter.NoteClient(ip, agentID)
}
