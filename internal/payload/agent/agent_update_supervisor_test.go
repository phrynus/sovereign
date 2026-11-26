//go:build linux || windows || darwin

package main

import (
	"strings"
	"testing"
)

func TestUnixUpdateSupervisorKeepsBackupAndRollsBack(t *testing.T) {
	script := buildUnixUpdateScript("/opt/agent", "/opt/agent.update.tmp")
	for _, want := range []string{
		"cp -f '/opt/agent' '/opt/agent.prev'",
		"until cp -f '/opt/agent.update.tmp' '/opt/agent'",
		"kill -0 \"$pid\"",
		"cp -f '/opt/agent.prev' '/opt/agent'",
		"exec '/opt/agent'",
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("Unix update supervisor missing %q:\n%s", want, script)
		}
	}
}

func TestWindowsUpdateSupervisorKeepsBackupAndRollsBack(t *testing.T) {
	script := buildWindowsUpdateScript(`C:\Program Files\agent.exe`, `C:\Program Files\agent.exe.update.tmp`)
	for _, want := range []string{
		"$backup = ($exe + '.prev')",
		"Copy-Item -LiteralPath $exe -Destination $backup -Force",
		"$child = Start-Process",
		"if (-not $child.HasExited)",
		"Copy-Item -LiteralPath $backup -Destination $exe -Force",
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("Windows update supervisor missing %q:\n%s", want, script)
		}
	}
}

func TestUpdateScriptQuotesUnsafePaths(t *testing.T) {
	if got := shellQuote("/tmp/agent's binary"); got != `'/tmp/agent'"'"'s binary'` {
		t.Fatalf("shell quote = %q", got)
	}
	script := buildWindowsUpdateScript("C:\\Temp\\agent's.exe", "C:\\Temp\\stage's.exe")
	if !strings.Contains(script, "'C:\\Temp\\agent''s.exe'") || !strings.Contains(script, "'C:\\Temp\\stage''s.exe'") {
		t.Fatalf("Windows paths were not escaped: %s", script)
	}
}

func TestVerifyUpdateSignatureRejectsMalformedPublicKey(t *testing.T) {
	if verifyUpdateSignature("00", make([]byte, 64), make([]byte, 64)) {
		t.Fatal("malformed public key was accepted")
	}
}
