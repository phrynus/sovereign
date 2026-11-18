//go:build linux || windows || darwin
// +build linux windows darwin

package main

import "time"

// setKeywordWatch applies an operator `keyword_watch` signal. Enabled is
// derived from the config: the watcher runs only when there is at least one
// term and at least one source selected. The config is normalized and stored
// under kwMu; the clipboard watcher and window-title hook read a snapshot.
func (s *agentWebRTCSession) setKeywordWatch(keywords []string, minIntervalMs int, clipboard, windowTitle *bool) {
	cfg := keywordConfig{
		Enabled:       true,
		Keywords:      keywords,
		MinIntervalMs: minIntervalMs,
	}
	if clipboard != nil {
		cfg.Clipboard = *clipboard
	}
	if windowTitle != nil {
		cfg.WindowTitle = *windowTitle
	}
	cfg = normalizeKeywordConfig(cfg)
	if len(cfg.Keywords) == 0 || (!cfg.Clipboard && !cfg.WindowTitle) {
		cfg.Enabled = false
	}

	s.kwMu.Lock()
	s.kwConfig = cfg
	s.kwLastAt = time.Time{}
	s.kwLastTerm = ""
	s.kwLastSrc = ""
	s.kwMu.Unlock()

	_ = s.sendJSON(map[string]any{
		"type": "keyword_watch", "enabled": cfg.Enabled,
		"terms": len(cfg.Keywords), "interval_ms": cfg.MinIntervalMs,
	})
}

// keywordAlertSnapshot returns the config + rate-limit state under one lock so
// callers can evaluate a match without holding the lock during a send.
func (s *agentWebRTCSession) keywordAlertSnapshot() keywordConfig {
	s.kwMu.Lock()
	defer s.kwMu.Unlock()
	return s.kwConfig
}

// maybeKeywordAlert checks one text value against the active watch config and
// emits a bounded `keyword_alert` frame when it matches and the interval allows
// it. `source` is "clipboard" or "window". Content is only ever a short preview.
func (s *agentWebRTCSession) maybeKeywordAlert(source, text string, now time.Time) {
	cfg := s.keywordAlertSnapshot()
	if !cfg.Enabled {
		return
	}
	term := keywordMatch(cfg, text)
	if term == "" {
		return
	}
	minInterval := time.Duration(cfg.MinIntervalMs) * time.Millisecond

	s.kwMu.Lock()
	allow := keywordAllow(&s.kwLastAt, &s.kwLastTerm, &s.kwLastSrc, term, source, now, minInterval)
	s.kwMu.Unlock()
	if !allow {
		return
	}
	_ = s.sendJSON(map[string]any{
		"type": "keyword_alert", "source": source,
		"term": term, "preview": keywordPreview(text), "ts": now.Unix(),
	})
}
