//go:build linux || windows || darwin
// +build linux windows darwin

package main

import (
	"strings"
	"testing"
	"time"
)

func TestNormalizeKeywordConfig(t *testing.T) {
	c := normalizeKeywordConfig(keywordConfig{
		Enabled: true, Keywords: []string{"  password ", "", strings.Repeat("x", keywordMaxTermLen+1), "password", "银行"},
		MinIntervalMs: 1, Clipboard: true,
	})
	if len(c.Keywords) != 2 || c.Keywords[0] != "password" || c.Keywords[1] != "银行" {
		t.Fatalf("keywords = %v", c.Keywords)
	}
	wantMin := int(keywordMinInterval / time.Millisecond)
	if c.MinIntervalMs != wantMin {
		t.Fatalf("interval = %d, want %d", c.MinIntervalMs, wantMin)
	}
	if !c.Clipboard || c.WindowTitle {
		t.Fatalf("flags = %+v", c)
	}
}

func TestKeywordMatch(t *testing.T) {
	c := normalizeKeywordConfig(keywordConfig{Enabled: true, Keywords: []string{"密码", "token"}})
	if got := keywordMatch(c, "nothing relevant here"); got != "" {
		t.Fatalf("unexpected match %q", got)
	}
	if got := keywordMatch(c, "my TOKEN=abc"); got != "token" {
		t.Fatalf("case-insensitive match = %q", got)
	}
	if got := keywordMatch(c, "账号密码已复制"); got != "密码" {
		t.Fatalf("unicode match = %q", got)
	}
	if got := keywordMatch(keywordConfig{Enabled: false, Keywords: []string{"token"}}, "token"); got != "" {
		t.Fatalf("disabled config matched %q", got)
	}
}

func TestKeywordPreview(t *testing.T) {
	if got := keywordPreview("  a\nb\tc  "); got != "a b c" {
		t.Fatalf("preview = %q", got)
	}
	long := strings.Repeat("x", keywordMaxPreview+50)
	got := keywordPreview(long)
	if !strings.HasSuffix(got, "…") || len([]rune(got)) != keywordMaxPreview+1 {
		t.Fatalf("preview len = %d", len([]rune(got)))
	}
}

func TestKeywordAllowInterval(t *testing.T) {
	base := time.Unix(1000, 0)
	lastAt, lastTerm, lastSource := base, "", ""
	// First (changed from empty) emits.
	if !keywordAllow(&lastAt, &lastTerm, &lastSource, "token", "clipboard", base, 2*time.Second) {
		t.Fatal("first alert not emitted")
	}
	// Same term/source within interval is throttled.
	if keywordAllow(&lastAt, &lastTerm, &lastSource, "token", "clipboard", base.Add(time.Second), 2*time.Second) {
		t.Fatal("duplicate within interval emitted")
	}
	// After the interval it emits again.
	if !keywordAllow(&lastAt, &lastTerm, &lastSource, "token", "clipboard", base.Add(3*time.Second), 2*time.Second) {
		t.Fatal("alert after interval not emitted")
	}
	// A new source bypasses the interval.
	if !keywordAllow(&lastAt, &lastTerm, &lastSource, "token", "window", base.Add(3*time.Second+time.Millisecond), 2*time.Second) {
		t.Fatal("new source within interval not emitted")
	}
}
