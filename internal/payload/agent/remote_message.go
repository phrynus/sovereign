package main

import (
	"fmt"
	"strings"
)

// MaxRemoteMessageBytes bounds a chat message shown on the target. It is a
// dialog a human has to read, not a file transfer.
const MaxRemoteMessageBytes = 1024

// MaxRemoteMessageTitleBytes bounds the dialog title.
const MaxRemoteMessageTitleBytes = 128

// sanitizeRemoteMessage canonicalises operator text for the target dialog.
// The text is DISPLAYED, never executed: no shell ever sees it, and control
// characters that could break a terminal or a dialog are removed.
func sanitizeRemoteMessage(text string) (string, error) {
	cleaned := strings.ToValidUTF8(text, "")
	cleaned = strings.ReplaceAll(cleaned, "\x00", "")
	cleaned = strings.TrimSpace(cleaned)
	if cleaned == "" {
		return "", fmt.Errorf("message text is required")
	}
	if len(cleaned) > MaxRemoteMessageBytes {
		return "", fmt.Errorf("message text exceeds %d bytes", MaxRemoteMessageBytes)
	}
	return cleaned, nil
}

// sanitizeRemoteMessageTitle bounds the optional dialog title and falls back
// to a neutral default, so a chat never arrives as an untitled system dialog.
func sanitizeRemoteMessageTitle(title string) string {
	cleaned := strings.ToValidUTF8(title, "")
	cleaned = strings.ReplaceAll(cleaned, "\x00", "")
	cleaned = strings.TrimSpace(cleaned)
	if cleaned == "" {
		return "Operator message"
	}
	if len(cleaned) > MaxRemoteMessageTitleBytes {
		cleaned = cleaned[:MaxRemoteMessageTitleBytes]
	}
	return cleaned
}
