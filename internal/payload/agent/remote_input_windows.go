//go:build windows
// +build windows

package main

import (
	"fmt"
	"strings"
	"syscall"
)

var (
	user32Input      = syscall.NewLazyDLL("user32.dll")
	procSetCursorPos = user32Input.NewProc("SetCursorPos")
	procMouseEvent   = user32Input.NewProc("mouse_event")
	procKeybdEvent   = user32Input.NewProc("keybd_event")
	// VkKeyScanW maps a printable character to its virtual-key code AND the
	// modifier state the current keyboard layout needs for it ("!" is
	// shift+1 on a US layout). Hard-coding punctuation virtual keys would be
	// wrong on every non-US layout.
	procVkKeyScanW = user32Input.NewProc("VkKeyScanW")
)

const (
	mouseEventLeftDown   = 0x0002
	mouseEventLeftUp     = 0x0004
	mouseEventRightDown  = 0x0008
	mouseEventRightUp    = 0x0010
	mouseEventMiddleDown = 0x0020
	mouseEventMiddleUp   = 0x0040
	mouseEventWheel      = 0x0800
	keyEventKeyUp        = 0x0002
	// wheelDelta is one notch. Windows adds the dwData value to the scroll
	// position, so a negative value scrolls towards the user.
	wheelDelta = 120
)

// win32 void-returning procedures.
//
// mouse_event and keybd_event are declared VOID. Treating their (always zero)
// return value as a BOOL made every click and every keystroke report
// "injection failed" even though the event had been delivered - the task path
// showed a failed task and the WebRTC path, which discarded the error, showed
// a connected desktop that ignored all input. Only SetCursorPos returns BOOL.
func remoteInputDispatch(payload string) (string, error) {
	ev, err := parseRemoteInputEvent(payload)
	if err != nil {
		return "", err
	}

	switch ev.Type {
	case "move":
		if err := moveCursor(ev.X, ev.Y); err != nil {
			return "", err
		}
		return fmt.Sprintf("remote_input: move to (%d,%d)", ev.X, ev.Y), nil
	case "click", "click_right", "click_middle":
		if err := moveCursor(ev.X, ev.Y); err != nil {
			return "", err
		}
		down, up := mouseButtonFlags(ev.Type)
		count := ev.Count
		if count < 1 {
			count = 1
		}
		mods := normalizeRemoteInputMods(ev.Mods)
		if err := pressModifiers(mods, true); err != nil {
			return "", err
		}
		for i := 0; i < count; i++ {
			mouseEvent(down, 0)
			mouseEvent(up, 0)
		}
		if err := pressModifiers(mods, false); err != nil {
			return "", err
		}
		return fmt.Sprintf("remote_input: %s x%d at (%d,%d) injected (mods=%v)", ev.Type, count, ev.X, ev.Y, mods), nil
	case "wheel":
		if err := moveCursor(ev.X, ev.Y); err != nil {
			return "", err
		}
		// Windows carries the delta in the low 32 bits of dwData.
		mouseEvent(mouseEventWheel, uintptr(uint32(int32(ev.Delta*wheelDelta))))
		return fmt.Sprintf("remote_input: wheel %d at (%d,%d)", ev.Delta, ev.X, ev.Y), nil
	case "key":
		vk, layoutMods, ok := keyToVK(ev.Key)
		if !ok {
			return "", fmt.Errorf("remote_input: unknown key: %s", ev.Key)
		}
		mods := mergeRemoteInputMods(normalizeRemoteInputMods(ev.Mods), layoutMods)
		if err := pressModifiers(mods, true); err != nil {
			return "", err
		}
		keybdEvent(vk, 0)
		keybdEvent(vk, keyEventKeyUp)
		if err := pressModifiers(mods, false); err != nil {
			return "", err
		}
		return fmt.Sprintf("remote_input: key %s (vk=%d, mods=%v) injected", ev.Key, vk, mods), nil
	default:
		return "", fmt.Errorf("remote_input: unknown type: %s", ev.Type)
	}
}

func moveCursor(x, y int) error {
	if r1, _, lastErr := procSetCursorPos.Call(uintptr(x), uintptr(y)); r1 == 0 {
		return fmt.Errorf("remote_input: SetCursorPos(%d,%d) failed: %v", x, y, lastErr)
	}
	return nil
}

// mouseEvent delivers one mouse event. The call has no return value, so there
// is nothing to check; the GetCursorPos/behaviour is observable on the target.
func mouseEvent(flags uintptr, data uintptr) {
	_, _, _ = procMouseEvent.Call(flags, 0, 0, data, 0)
}

// keybdEvent delivers one key event (VOID, same reasoning as mouseEvent).
func keybdEvent(vk uint16, flags uintptr) {
	_, _, _ = procKeybdEvent.Call(uintptr(vk), 0, flags, 0)
}

func mouseButtonFlags(inputType string) (down, up uintptr) {
	switch inputType {
	case "click_right":
		return mouseEventRightDown, mouseEventRightUp
	case "click_middle":
		return mouseEventMiddleDown, mouseEventMiddleUp
	default:
		return mouseEventLeftDown, mouseEventLeftUp
	}
}

// modVK maps a canonical modifier name to its virtual-key code.
func modVK(mod string) (uint16, bool) {
	switch mod {
	case "ctrl":
		return 0x11, true // VK_CONTROL
	case "alt":
		return 0x12, true // VK_MENU
	case "shift":
		return 0x10, true // VK_SHIFT
	case "meta":
		return 0x5B, true // VK_LWIN
	default:
		return 0, false
	}
}

// pressModifiers holds every modifier down (or releases them in reverse order)
// so Ctrl+C arrives as one chord rather than three unrelated key presses.
func pressModifiers(mods []string, down bool) error {
	if !down {
		for i := len(mods) - 1; i >= 0; i-- {
			vk, ok := modVK(mods[i])
			if !ok {
				continue
			}
			keybdEvent(vk, keyEventKeyUp)
		}
		return nil
	}
	for _, mod := range mods {
		vk, ok := modVK(mod)
		if !ok {
			return fmt.Errorf("remote_input: unknown modifier: %s", mod)
		}
		keybdEvent(vk, 0)
	}
	return nil
}

// keyToVK resolves a key name to a virtual-key code plus the modifiers the
// current keyboard layout needs for that character.
func keyToVK(key string) (uint16, []string, bool) {
	k := strings.TrimSpace(key)
	if k == "" {
		return 0, nil, false
	}
	lower := strings.ToLower(k)

	named := map[string]uint16{
		"enter": 0x0D, "return": 0x0D, "tab": 0x09, "space": 0x20,
		"backspace": 0x08, "escape": 0x1B, "esc": 0x1B,
		"left": 0x25, "up": 0x26, "right": 0x27, "down": 0x28,
		"delete": 0x2E, "insert": 0x2D, "home": 0x24, "end": 0x23,
		"pageup": 0x21, "pagedown": 0x22,
		"capslock": 0x14, "numlock": 0x90, "scrolllock": 0x91,
		"printscreen": 0x2C, "pause": 0x13,
		"minus": 0xBD, "equal": 0xBB, "plus": 0xBB,
		"comma": 0xBC, "period": 0xBE, "dot": 0xBE, "slash": 0xBF,
		"semicolon": 0xBA, "quote": 0xDE, "backtick": 0xC0,
		"leftbracket": 0xDB, "rightbracket": 0xDD, "backslash": 0xDC,
		"win": 0x5B, "meta": 0x5B,
		"f1": 0x70, "f2": 0x71, "f3": 0x72, "f4": 0x73,
		"f5": 0x74, "f6": 0x75, "f7": 0x76, "f8": 0x77,
		"f9": 0x78, "f10": 0x79, "f11": 0x7A, "f12": 0x7B,
	}
	if vk, ok := named[lower]; ok {
		return vk, nil, true
	}

	runes := []rune(k)
	if len(runes) != 1 {
		return 0, nil, false
	}
	ch := runes[0]
	if ch >= 'a' && ch <= 'z' {
		return uint16(strings.ToUpper(k)[0]), nil, true
	}
	if ch >= 'A' && ch <= 'Z' {
		// An explicit uppercase character means "hold shift" - the console
		// sends mods itself, but a hand-written task should still type the
		// character it names.
		return uint16(ch), []string{"shift"}, true
	}
	if ch < 0x20 || ch > 0x7E {
		return 0, nil, false
	}
	scan, _, _ := procVkKeyScanW.Call(uintptr(ch))
	vkScan := int16(uint16(scan))
	if vkScan == -1 {
		return 0, nil, false
	}
	var mods []string
	state := byte(uint16(scan) >> 8)
	if state&1 != 0 {
		mods = append(mods, "shift")
	}
	if state&2 != 0 {
		mods = append(mods, "ctrl")
	}
	if state&4 != 0 {
		mods = append(mods, "alt")
	}
	return uint16(byte(vkScan)), mods, true
}
