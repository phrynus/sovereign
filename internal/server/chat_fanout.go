package server

import (
	"sync"
	"time"
)

// Chat fan-out is deliberately separate from the agent/task fan-out: a chat
// frame is scoped to an ENGAGEMENT (and therefore to a tenant), not to one
// agent. The tenant rule is intentionally stricter than tenantScope's HTTP
// view: tenant 0 is the legacy operator scope and only sees legacy chat,
// while a real tenant also receives frames from legacy/system senders so an
// operator does not miss an announcement written before tenants existed.

// broadcastToTenantID delivers a chat frame only to sockets allowed to see
// that tenant. reliable is true for system/approval cards that a client
// cannot rebuild by polling; a slow client is disconnected and resyncs.
func (s *Server) broadcastToTenantID(payload []byte, tenantID uint, reliable bool) {
	if tenantID == 0 {
		s.broadcastFanout(payload, reliable, func(sub wsSubscriber) bool {
			return sub.TenantID == 0
		})
		return
	}
	s.broadcastFanout(payload, reliable, func(sub wsSubscriber) bool {
		return sub.TenantID == 0 || sub.TenantID == tenantID
	})
}

// Chat send throttling lives at package level because Server's struct is
// shared with the rest of the package and the chat limiter is only used by
// the chat handlers. It is bounded in both dimensions: at most 4096 operator
// buckets, and at most chatSendWindow of recent sends per bucket. Empty and
// expired buckets are reclaimed on every call, so a flood of one-shot
// identities cannot grow the map without limit.
const (
	chatSendLimit     = 10
	chatSendWindow    = 10 * time.Second
	chatLimiterMaxSet = 4096
)

var chatSendLimiter = struct {
	sync.Mutex
	recent map[string][]time.Time
}{recent: make(map[string][]time.Time)}

// allowChatSend records one send for identity and refuses it if that identity
// already sent more than chatSendLimit messages within chatSendWindow.
func allowChatSend(identity string) bool {
	if identity == "" {
		identity = "anonymous"
	}
	now := time.Now()
	cutoff := now.Add(-chatSendWindow)

	chatSendLimiter.Lock()
	defer chatSendLimiter.Unlock()

	for key, times := range chatSendLimiter.recent {
		kept := times[:0]
		for _, ts := range times {
			if ts.After(cutoff) {
				kept = append(kept, ts)
			}
		}
		if len(kept) == 0 {
			delete(chatSendLimiter.recent, key)
		} else {
			chatSendLimiter.recent[key] = kept
		}
	}

	times := chatSendLimiter.recent[identity]
	if len(times) >= chatSendLimit {
		return false
	}
	if _, exists := chatSendLimiter.recent[identity]; !exists && len(chatSendLimiter.recent) >= chatLimiterMaxSet {
		// Drop the oldest bucket's last activity. Under sustained abuse this
		// bounds memory; a legitimate operator with a very old bucket is the
		// acceptable cost of a bounded in-memory limiter.
		var oldest string
		var oldestTime time.Time
		for key, stamps := range chatSendLimiter.recent {
			last := stamps[len(stamps)-1]
			if oldest == "" || last.Before(oldestTime) {
				oldest, oldestTime = key, last
			}
		}
		delete(chatSendLimiter.recent, oldest)
	}
	chatSendLimiter.recent[identity] = append(times, now)
	return true
}

// resetChatRateLimiter is used by tests that need an isolated rate budget.
func resetChatRateLimiter() {
	chatSendLimiter.Lock()
	chatSendLimiter.recent = make(map[string][]time.Time)
	chatSendLimiter.Unlock()
}
