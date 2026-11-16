//go:build windows
// +build windows

package main

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// TestRemoteInputKeyToVK pins the key map the console relies on: letters,
// digits, named keys, and printable punctuation resolved through the active
// keyboard layout (VkKeyScanW) instead of a hard-coded US table.
func TestRemoteInputKeyToVK(t *testing.T) {
	cases := []struct {
		key      string
		wantVK   uint16
		wantMods []string
	}{
		{"a", 0x41, nil},
		{",", 0xBC, nil}, // OEM_COMMA
		{"enter", 0x0D, nil},
		{"TAB", 0x09, nil},
		{"f5", 0x74, nil},
		{"pageup", 0x21, nil},
		{"insert", 0x2D, nil},
		{"win", 0x5B, nil},
	}
	for _, tc := range cases {
		vk, mods, ok := keyToVK(tc.key)
		if !ok {
			t.Fatalf("keyToVK(%q) was not resolved", tc.key)
		}
		if vk != tc.wantVK {
			t.Fatalf("keyToVK(%q) vk = %#x, want %#x", tc.key, vk, tc.wantVK)
		}
		if strings.Join(mods, ",") != strings.Join(tc.wantMods, ",") {
			t.Fatalf("keyToVK(%q) mods = %v, want %v", tc.key, mods, tc.wantMods)
		}
	}

	// An explicit uppercase character must hold shift, or "A" would type "a".
	if vk, mods, ok := keyToVK("A"); !ok || vk != 0x41 || !containsMod(mods, "shift") {
		t.Fatalf("keyToVK(\"A\") = %#x %v ok=%v, want shift+0x41", vk, mods, ok)
	}

	// Printable punctuation comes from the layout. "!" needs shift on every
	// Latin layout; the exact virtual key is layout-dependent, so assert the
	// modifier rather than a US-specific code.
	if vk, mods, ok := keyToVK("!"); !ok || vk == 0 || !containsMod(mods, "shift") {
		t.Fatalf("keyToVK(\"!\") = %#x %v ok=%v, want a shift-modified key", vk, mods, ok)
	}
	if _, _, ok := keyToVK("."); !ok {
		t.Fatal("keyToVK(\".\") was not resolved")
	}

	for _, bad := range []string{"", "ab", "hyper", "\x01"} {
		if _, _, ok := keyToVK(bad); ok {
			t.Fatalf("keyToVK(%q) accepted an unresolvable key", bad)
		}
	}
}

func containsMod(mods []string, want string) bool {
	for _, mod := range mods {
		if mod == want {
			return true
		}
	}
	return false
}

func TestRemoteInputNormalizeMods(t *testing.T) {
	got := normalizeRemoteInputMods([]string{" CTRL ", "ctrl", "Shift", "hyper"})
	if strings.Join(got, ",") != "ctrl,shift" {
		t.Fatalf("normalizeMods = %v, want [ctrl shift] (deduped, lowercased, unknown dropped)", got)
	}
	if normalizeRemoteInputMods(nil) != nil {
		t.Fatal("normalizeMods(nil) should stay nil")
	}
	if got := mergeRemoteInputMods([]string{"ctrl"}, []string{"shift", "ctrl"}); strings.Join(got, ",") != "ctrl,shift" {
		t.Fatalf("mergeMods = %v, want [ctrl shift]", got)
	}
}

func TestRemoteInputMouseButtonFlags(t *testing.T) {
	for _, tc := range []struct {
		inputType string
		down, up  uintptr
	}{
		{"click", mouseEventLeftDown, mouseEventLeftUp},
		{"click_right", mouseEventRightDown, mouseEventRightUp},
		{"click_middle", mouseEventMiddleDown, mouseEventMiddleUp},
	} {
		down, up := mouseButtonFlags(tc.inputType)
		if down != tc.down || up != tc.up {
			t.Fatalf("%s flags = %#x/%#x, want %#x/%#x", tc.inputType, down, up, tc.down, tc.up)
		}
	}
}

// TestRemoteInputVoidCallsAreNotChecked is the regression guard for the defect
// that made remote control useless on Windows: mouse_event and keybd_event are
// declared VOID, so their return value is always zero, and treating it as a
// BOOL made every click and keystroke report failure (the task path showed a
// failed task; the WebRTC path swallowed the error and looked "connected" but
// ignored input). Only SetCursorPos returns BOOL.
func TestRemoteInputVoidCallsAreNotChecked(t *testing.T) {
	src, err := os.ReadFile("remote_input_windows.go")
	if err != nil {
		t.Fatalf("read source: %v", err)
	}
	code := stripGoComments(string(src))
	voidChecks := []*regexp.Regexp{
		regexp.MustCompile(`procMouseEvent\.Call\([^\n]*\);\s*\w+\s*==\s*0`),
		regexp.MustCompile(`procKeybdEvent\.Call\([^\n]*\);\s*\w+\s*==\s*0`),
		regexp.MustCompile(`if\s+_\s*,\s*_\s*,\s*\w+\s*:=\s*proc(MouseEvent|KeybdEvent)\.Call`),
	}
	for _, re := range voidChecks {
		if loc := re.FindString(code); loc != "" {
			t.Fatalf("a VOID Win32 procedure is checked for success: %q", loc)
		}
	}
	if !strings.Contains(code, "procSetCursorPos.Call") {
		t.Fatal("SetCursorPos is no longer called; the guard is reading the wrong file")
	}
}

// stripGoComments removes // and /* */ comments so the guard does not trip on
// the explanation of the defect it pins.
func stripGoComments(src string) string {
	block := regexp.MustCompile(`(?s)/\*.*?\*/`)
	src = block.ReplaceAllString(src, "")
	line := regexp.MustCompile(`(?m)//.*$`)
	return line.ReplaceAllString(src, "")
}
