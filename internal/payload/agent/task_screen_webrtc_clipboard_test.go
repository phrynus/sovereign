package main

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// TestSanitizeClipboardTextBounds pins the three rules a clipboard value must
// satisfy before it is put on the wire: valid UTF-8, no NUL bytes, and a cut
// that never splits a multi-byte rune.
func TestSanitizeClipboardTextBounds(t *testing.T) {
	if got, truncated := sanitizeClipboardText("hello\x00world"); got != "helloworld" || truncated {
		t.Fatalf("NUL handling = %q truncated=%v", got, truncated)
	}
	if got, _ := sanitizeClipboardText("ok\xfftext"); strings.ContainsRune(got, utf8.RuneError) {
		t.Fatalf("invalid UTF-8 survived: %q", got)
	}

	exact := strings.Repeat("a", MaxClipboardBytes)
	if got, truncated := sanitizeClipboardText(exact); len(got) != MaxClipboardBytes || truncated {
		t.Fatalf("exact cap: len=%d truncated=%v", len(got), truncated)
	}

	// A 3-byte rune straddling the cap must be dropped whole, not split into a
	// replacement glyph (which would paste as garbage).
	prefix := strings.Repeat("a", MaxClipboardBytes-1)
	long := prefix + "世" + "tail"
	got, truncated := sanitizeClipboardText(long)
	if !truncated {
		t.Fatal("oversized clipboard was not reported truncated")
	}
	if len(got) > MaxClipboardBytes {
		t.Fatalf("truncated len = %d, want <= %d", len(got), MaxClipboardBytes)
	}
	if !utf8.ValidString(got) {
		t.Fatalf("truncation split a rune: %q", got)
	}
	if got != prefix {
		t.Fatalf("truncated value = %q, want the ASCII prefix only", got[len(got)-4:])
	}

	if got, _ := sanitizeClipboardText(""); got != "" {
		t.Fatalf("empty clipboard = %q", got)
	}
}

// TestPrepareClipboardWriteRefusesOversize pins the asymmetry: reads truncate
// (safe to display), writes refuse (a half-pasted script runs a command nobody
// wrote).
func TestPrepareClipboardWriteRefusesOversize(t *testing.T) {
	text, err := prepareClipboardWrite("hello\x00 world")
	if err != nil || text != "hello world" {
		t.Fatalf("prepare = %q err=%v", text, err)
	}
	if _, err := prepareClipboardWrite(strings.Repeat("a", MaxClipboardBytes+1)); err == nil {
		t.Fatal("an oversized paste was accepted")
	}
	if _, err := prepareClipboardWrite(strings.Repeat("a", MaxClipboardBytes)); err != nil {
		t.Fatalf("an exactly-capped paste was refused: %v", err)
	}
}

func TestClipboardHashChangesWithContent(t *testing.T) {
	if clipboardHash("a") == clipboardHash("b") {
		t.Fatal("different clipboard values hash equal")
	}
	first := clipboardHash("same")
	second := clipboardHash("same")
	if first != second {
		t.Fatal("the same value hashed differently")
	}
}
