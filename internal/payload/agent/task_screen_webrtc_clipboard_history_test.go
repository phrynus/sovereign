package main

import (
	"strings"
	"testing"
	"time"
)

// resetClipboardHistory clears the process-wide ring so one test's values do
// not leak into the next.
func resetClipboardHistory() {
	clipHistMu.Lock()
	clipHist = nil
	clipHistMu.Unlock()
}

func TestClipboardHistoryBounds(t *testing.T) {
	resetClipboardHistory()
	base := time.Now()

	// 25 distinct values: the ring keeps the newest 20 and evicts the oldest.
	for i := 0; i < 25; i++ {
		clipboardHistoryRecord(string(rune('a'+i)), base.Add(time.Duration(i)*time.Second))
	}
	if got := clipboardHistoryTotal(); got != maxClipboardHistory {
		t.Fatalf("history size = %d, want %d", got, maxClipboardHistory)
	}
	snapshot := clipboardHistorySnapshot(maxClipboardHistory)
	if len(snapshot) != maxClipboardHistory {
		t.Fatalf("snapshot = %d entries", len(snapshot))
	}
	if snapshot[0].text != "y" || snapshot[len(snapshot)-1].text != "f" {
		t.Fatalf("snapshot order = %q..%q, want y..f (newest first, oldest evicted)",
			snapshot[0].text, snapshot[len(snapshot)-1].text)
	}

	// Consecutive duplicates are not recorded twice.
	clipboardHistoryRecord("y", base.Add(time.Minute))
	if got := clipboardHistoryTotal(); got != maxClipboardHistory {
		t.Fatalf("a duplicate grew the history to %d", got)
	}

	// A value larger than the cap is stored truncated to the cap.
	resetClipboardHistory()
	big := strings.Repeat("b", MaxClipboardBytes+500)
	clipboardHistoryRecord(big, base)
	entry, ok := clipboardHistoryAt(0)
	if !ok || len(entry.text) != MaxClipboardBytes {
		t.Fatalf("oversized entry = %d bytes ok=%v", len(entry.text), ok)
	}

	// Index resolution is newest-first and bounded.
	resetClipboardHistory()
	clipboardHistoryRecord("old", base)
	clipboardHistoryRecord("new", base.Add(time.Second))
	if e, _ := clipboardHistoryAt(0); e.text != "new" {
		t.Fatalf("index 0 = %q, want new", e.text)
	}
	if e, _ := clipboardHistoryAt(1); e.text != "old" {
		t.Fatalf("index 1 = %q, want old", e.text)
	}
	if _, ok := clipboardHistoryAt(2); ok {
		t.Fatal("index 2 resolved past the end")
	}
	if _, ok := clipboardHistoryAt(-1); ok {
		t.Fatal("a negative index resolved")
	}
	resetClipboardHistory()
}

func TestClipboardHistoryPreview(t *testing.T) {
	if got := clipboardPreviewText("  one\ntwo\tthree  "); got != "one two three" {
		t.Fatalf("preview = %q", got)
	}
	long := strings.Repeat("x", clipboardPreviewLen+30)
	got := clipboardPreviewText(long)
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("long preview not elided: %q", got)
	}
	if runes := []rune(got); len(runes) != clipboardPreviewLen+1 {
		t.Fatalf("preview runes = %d, want %d+ellipsis", len(runes), clipboardPreviewLen)
	}
	// Multi-byte characters must not be split by the elision.
	emoji := strings.Repeat("🙂", clipboardPreviewLen+5)
	preview := clipboardPreviewText(emoji)
	for _, r := range preview {
		if r == '\uFFFD' {
			t.Fatalf("elision split a rune: %q", preview)
		}
	}
}
