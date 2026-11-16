package main

import (
	"time"
)

// clipboardPollInterval is how often the target clipboard is compared against
// the last value sent. Polling (instead of a Win32 clipboard-viewer chain)
// keeps one implementation for every platform and costs one hash per tick.
const clipboardPollInterval = 1500 * time.Millisecond

// clipboardWatcher publishes the target clipboard when it changes. It sends the
// current value once on connect (so the console shows what is already there),
// then only on a hash change, and never while watching is disabled.
func (s *agentWebRTCSession) clipboardWatcher() {
	ticker := time.NewTicker(clipboardPollInterval)
	defer ticker.Stop()
	s.publishClipboard(false)
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
			s.publishClipboard(false)
		}
	}
}

// publishClipboard reads, sanitizes and (if changed or forced) sends the target
// clipboard. Read failures are silent: an empty or non-text clipboard is the
// normal state, not an error the operator needs every 1.5 seconds.
func (s *agentWebRTCSession) publishClipboard(force bool) {
	raw, err := clipboardGet()
	if err != nil {
		return
	}
	text, truncated := sanitizeClipboardText(raw)
	hash := clipboardHash(text)

	s.clipMu.Lock()
	if !s.clipWatch && !force {
		s.clipMu.Unlock()
		return
	}
	if !force && hash == s.clipLastHash {
		s.clipMu.Unlock()
		return
	}
	s.clipLastHash = hash
	s.clipMu.Unlock()

	_ = s.sendJSON(map[string]any{
		"type":      "clipboard",
		"text":      text,
		"source":    "agent",
		"truncated": truncated,
	})
}

// applyClipboardSet writes operator text into the target clipboard, remembers
// its hash (so the watcher does not echo the operator's own paste back), and
// confirms with the value the target now holds.
func (s *agentWebRTCSession) applyClipboardSet(raw string) {
	text, err := prepareClipboardWrite(raw)
	if err != nil {
		_ = s.sendJSON(map[string]any{"type": "clipboard_error", "reason": err.Error()})
		return
	}
	if err := clipboardSet(text); err != nil {
		_ = s.sendJSON(map[string]any{
			"type":   "clipboard_error",
			"reason": "clipboard write failed: " + err.Error(),
		})
		return
	}
	s.clipMu.Lock()
	s.clipLastHash = clipboardHash(text)
	s.clipMu.Unlock()
	_ = s.sendJSON(map[string]any{
		"type":      "clipboard",
		"text":      text,
		"source":    "operator",
		"truncated": false,
	})
}

// setClipboardWatch enables or disables change streaming.
func (s *agentWebRTCSession) setClipboardWatch(enabled bool) {
	s.clipMu.Lock()
	s.clipWatch = enabled
	s.clipMu.Unlock()
	if enabled {
		s.publishClipboard(true)
	}
}
