package main

import (
	"encoding/json"
	"fmt"
	"strings"
)

// remoteInputEvent is the operator-side wire shape of one remote-control
// event, shared by every platform so the server contract is parsed once.
//
//	move          x,y
//	click         x,y [count] [mods]        left button
//	click_right   x,y [count] [mods]
//	click_middle  x,y [count] [mods]
//	wheel         x,y delta
//	key           key [mods]
type remoteInputEvent struct {
	Type  string   `json:"type"`
	X     int      `json:"x"`
	Y     int      `json:"y"`
	Key   string   `json:"key"`
	Count int      `json:"count"`
	Delta int      `json:"delta"`
	Mods  []string `json:"mods"`
}

// parseRemoteInputEvent decodes and canonicalises the event type. The payload
// used to be treated as literal text on Linux and macOS, so a move typed the
// JSON into the focused window instead of moving the pointer.
func parseRemoteInputEvent(payload string) (remoteInputEvent, error) {
	var ev remoteInputEvent
	if err := json.Unmarshal([]byte(payload), &ev); err != nil {
		return ev, fmt.Errorf("remote_input: invalid json: %w", err)
	}
	ev.Type = strings.ToLower(strings.TrimSpace(ev.Type))
	return ev, nil
}

// normalizeRemoteInputMods lowercases, trims and de-duplicates a modifier
// list. Names the platform does not know are dropped: the server validates
// them, and a hand-written task must not smuggle in an arbitrary key.
func normalizeRemoteInputMods(mods []string) []string {
	if len(mods) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(mods))
	out := make([]string, 0, len(mods))
	for _, mod := range mods {
		mod = strings.ToLower(strings.TrimSpace(mod))
		switch mod {
		case "ctrl", "alt", "shift", "meta":
		default:
			continue
		}
		if seen[mod] {
			continue
		}
		seen[mod] = true
		out = append(out, mod)
	}
	return out
}

// mergeRemoteInputMods unions two modifier lists, keeping the first list's
// order.
func mergeRemoteInputMods(a, b []string) []string {
	out := append([]string(nil), a...)
	seen := make(map[string]bool, len(a)+len(b))
	for _, mod := range a {
		seen[mod] = true
	}
	for _, mod := range b {
		if !seen[mod] {
			seen[mod] = true
			out = append(out, mod)
		}
	}
	return out
}
