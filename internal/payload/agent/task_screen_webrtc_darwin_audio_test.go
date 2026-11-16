package main

import (
	"strings"
	"testing"
)

// TestParseAVFoundationAudioDevices pins the parser: only the audio section is
// read, the device index is the LAST bracketed number (the prefix also has
// brackets), video devices are ignored, and names keep their spaces.
func TestParseAVFoundationAudioDevices(t *testing.T) {
	const sample = "[AVFoundation indev @ 0x7f9a1c] AVFoundation video devices:\n" +
		"[AVFoundation indev @ 0x7f9a1c] [0] FaceTime HD Camera\n" +
		"[AVFoundation indev @ 0x7f9a1c] [1] Capture screen 0\n" +
		"[AVFoundation indev @ 0x7f9a1c] AVFoundation audio devices:\n" +
		"[AVFoundation indev @ 0x7f9a1c] [0] MacBook Pro Microphone\n" +
		"[AVFoundation indev @ 0x7f9a1c] [1] BlackHole 2ch\n" +
		"[AVFoundation indev @ 0x7f9a1c] [2] Aggregate Device\n"

	devices := parseAVFoundationAudioDevices(sample)
	if len(devices) != 3 {
		t.Fatalf("devices = %+v, want 3 audio devices", devices)
	}
	if devices[0] != (avAudioDevice{Index: 0, Name: "MacBook Pro Microphone"}) {
		t.Fatalf("first device = %+v", devices[0])
	}
	if devices[1] != (avAudioDevice{Index: 1, Name: "BlackHole 2ch"}) {
		t.Fatalf("second device = %+v", devices[1])
	}
	if devices[2] != (avAudioDevice{Index: 2, Name: "Aggregate Device"}) {
		t.Fatalf("third device = %+v", devices[2])
	}

	// A section with no devices, and output that never mentions audio.
	if got := parseAVFoundationAudioDevices("[AVFoundation indev @ 0x1] AVFoundation audio devices:\n"); len(got) != 0 {
		t.Fatalf("empty audio section parsed as %+v", got)
	}
	if got := parseAVFoundationAudioDevices("no devices here\n"); len(got) != 0 {
		t.Fatalf("unrelated output parsed as %+v", got)
	}
}

func TestIsDarwinLoopbackDevice(t *testing.T) {
	for _, name := range []string{
		"BlackHole 2ch",
		"BlackHole 16ch",
		"Soundflower (2ch)",
		"Loopback Audio",
		"Existential Audio BlackHole",
		"Virtual Audio Device",
	} {
		if !isDarwinLoopbackDevice(name) {
			t.Fatalf("%q was not classified as a loopback device", name)
		}
	}
	for _, name := range []string{
		"MacBook Pro Microphone",
		"FaceTime HD Camera",
		"Aggregate Device",
	} {
		if isDarwinLoopbackDevice(name) {
			t.Fatalf("%q was classified as a loopback device", name)
		}
	}
}

// TestSelectAVFoundationDevice pins the macOS policy: speaker only accepts a
// virtual loopback device and fails actionably without one; mic uses a real
// input and falls back to device 0; indices are AVFoundation indices, not list
// positions.
func TestSelectAVFoundationDevice(t *testing.T) {
	devices := []avAudioDevice{
		{Index: 0, Name: "MacBook Pro Microphone"},
		{Index: 1, Name: "BlackHole 2ch"},
		{Index: 2, Name: "Aggregate Device"},
	}

	speaker, err := selectAVFoundationDevice(devices, "speaker", 0)
	if err != nil || speaker != (avAudioDevice{Index: 1, Name: "BlackHole 2ch"}) {
		t.Fatalf("speaker[0] = %+v err=%v", speaker, err)
	}
	if _, err := selectAVFoundationDevice(devices, "speaker", 1); err == nil ||
		!strings.Contains(err.Error(), "1 speaker") {
		t.Fatalf("out-of-range speaker error = %v", err)
	}

	mic, err := selectAVFoundationDevice(devices, "mic", 0)
	if err != nil || mic != (avAudioDevice{Index: 0, Name: "MacBook Pro Microphone"}) {
		t.Fatalf("mic[0] = %+v err=%v", mic, err)
	}
	// The second microphone candidate is the Aggregate Device (index 2): the
	// returned index is the AVFoundation index, not the candidate position.
	second, err := selectAVFoundationDevice(devices, "mic", 1)
	if err != nil || second != (avAudioDevice{Index: 2, Name: "Aggregate Device"}) {
		t.Fatalf("mic[1] = %+v err=%v", second, err)
	}
	if _, err := selectAVFoundationDevice(devices, "mic", 2); err == nil {
		t.Fatal("an out-of-range mic index was accepted")
	}

	// Microphone-only host: speaker is refused with an actionable message.
	if _, err := selectAVFoundationDevice([]avAudioDevice{{Index: 0, Name: "MacBook Pro Microphone"}}, "speaker", 0); err == nil ||
		!strings.Contains(err.Error(), "BlackHole") {
		t.Fatalf("speaker without BlackHole = %v", err)
	}
	// No enumeration at all: mic falls back to AVFoundation input 0.
	if got, err := selectAVFoundationDevice(nil, "mic", 0); err != nil || got.Index != 0 {
		t.Fatalf("empty mic list = %+v err=%v", got, err)
	}
	if _, err := selectAVFoundationDevice(nil, "mic", 1); err == nil {
		t.Fatal("an index past the default input was accepted")
	}
	if _, err := selectAVFoundationDevice(devices, "line", 0); err == nil {
		t.Fatal("an unknown mode was accepted")
	}
}
