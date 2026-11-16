package main

import (
	"strings"
	"testing"
)

func TestSanitizeRemoteMessage(t *testing.T) {
	text, err := sanitizeRemoteMessage("  hello\x00 world  ")
	if err != nil || text != "hello world" {
		t.Fatalf("sanitize = %q err=%v", text, err)
	}
	for _, bad := range []string{"", "   ", "\x00\x00"} {
		if _, err := sanitizeRemoteMessage(bad); err == nil {
			t.Fatalf("%q was accepted as a message", bad)
		}
	}
	if _, err := sanitizeRemoteMessage(strings.Repeat("a", MaxRemoteMessageBytes+1)); err == nil {
		t.Fatal("an oversized message was accepted")
	}
	if _, err := sanitizeRemoteMessage(strings.Repeat("a", MaxRemoteMessageBytes)); err != nil {
		t.Fatalf("an exactly-capped message was refused: %v", err)
	}
}

func TestSanitizeRemoteMessageTitle(t *testing.T) {
	if got := sanitizeRemoteMessageTitle(""); got != "Operator message" {
		t.Fatalf("default title = %q", got)
	}
	if got := sanitizeRemoteMessageTitle("  \x00 "); got != "Operator message" {
		t.Fatalf("blank title = %q", got)
	}
	long := strings.Repeat("t", MaxRemoteMessageTitleBytes+20)
	if got := sanitizeRemoteMessageTitle(long); len(got) != MaxRemoteMessageTitleBytes {
		t.Fatalf("title len = %d, want %d", len(got), MaxRemoteMessageTitleBytes)
	}
	if got := sanitizeRemoteMessageTitle("Security notice"); got != "Security notice" {
		t.Fatalf("title = %q", got)
	}
}
