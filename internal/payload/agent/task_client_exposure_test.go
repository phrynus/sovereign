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

func TestClientTargetsPerPlatform(t *testing.T) {
	win := clientTargets("windows", "C:\\Users\\u", "C:\\Users\\u\\AppData\\Roaming")
	if len(win) < 10 || !strings.Contains(win[0].dirs[0], "PuTTY") {
		t.Fatalf("windows targets = %v", win)
	}
	mac := clientTargets("darwin", "/Users/u", "")
	if len(mac) < 4 {
		t.Fatalf("darwin targets = %v", mac)
	}
	lin := clientTargets("linux", "/home/u", "")
	if len(lin) < 3 || !strings.Contains(lin[0].dirs[0], "filezilla") {
		t.Fatalf("linux targets = %v", lin)
	}
}

func TestCollectClientExposureNoContentRead(t *testing.T) {
	root := t.TempDir()
	putty := filepath.Join(root, "PuTTY")
	if err := os.MkdirAll(putty, 0o755); err != nil {
		t.Fatal(err)
	}
	// A file containing a secret-looking value. The collector must report its
	// presence but never include the content in the output.
	secret := "SUPER-SECRET-SESSION-KEY-12345"
	if err := os.WriteFile(filepath.Join(putty, "sessions"), []byte(secret), 0o644); err != nil {
		t.Fatal(err)
	}
	// Symlink to an outside file — must not be followed.
	outside := filepath.Join(root, "outside")
	if err := os.WriteFile(outside, []byte("OUTSIDE-SECRET"), 0o644); err != nil {
		t.Fatal(err)
	}
	_ = os.Symlink(outside, filepath.Join(putty, "link"))

	report := collectClientExposureFrom([]clientTarget{
		{label: "PuTTY", dirs: []string{putty}},
		{label: "KeePass", dirs: []string{filepath.Join(root, "missing")}},
	})
	if !strings.Contains(report, "PuTTY") || !strings.Contains(report, "1 files") {
		t.Fatalf("report missing PuTTY count: %s", report)
	}
	if strings.Contains(report, secret) || strings.Contains(report, "OUTSIDE-SECRET") {
		t.Fatalf("collector leaked file contents: %s", report)
	}
	if !strings.Contains(report, "KeePass\t<not installed>") {
		t.Fatalf("missing client not reported: %s", report)
	}
}

func TestClientExposureHandleBase64(t *testing.T) {
	var res TaskResult
	handleClientExposureCollect(Task{Type: "client_exposure_collect"}, &res)
	if res.Encoding != "base64" || res.Output == "" {
		t.Fatalf("bad envelope: %q %q", res.Encoding, res.Output)
	}
	if _, err := base64.StdEncoding.DecodeString(res.Output); err != nil {
		t.Fatal(err)
	}
}
