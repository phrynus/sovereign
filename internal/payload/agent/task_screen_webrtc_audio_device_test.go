package main

import (
	"strings"
	"testing"
)

// TestSelectAudioDevice pins the whole device policy: loopback devices are only
// offered in speaker mode, mic mode avoids them, an index selects within the
// filtered list, and a host without a loopback device fails with an actionable
// message instead of recording the room.
func TestSelectAudioDevice(t *testing.T) {
	devices := []string{
		"Microphone (Realtek(R) Audio)",
		"CABLE Output (VB-Audio Virtual Cable)",
		"Stereo Mix (Realtek(R) Audio)",
		"virtual-audio-capturer",
	}

	mic, err := selectAudioDevice(devices, "mic", 0)
	if err != nil || mic != "Microphone (Realtek(R) Audio)" {
		t.Fatalf("mic[0] = %q err=%v", mic, err)
	}
	if got, err := selectAudioDevice(devices, "", 0); err != nil || got != mic {
		t.Fatalf("empty mode = %q err=%v, want the mic default", got, err)
	}

	speaker, err := selectAudioDevice(devices, "speaker", 0)
	if err != nil || speaker != "Stereo Mix (Realtek(R) Audio)" {
		t.Fatalf("speaker[0] = %q err=%v", speaker, err)
	}
	if got, err := selectAudioDevice(devices, "speaker", 1); err != nil || got != "virtual-audio-capturer" {
		t.Fatalf("speaker[1] = %q err=%v", got, err)
	}

	// Out of range names how many devices the mode offers.
	if _, err := selectAudioDevice(devices, "speaker", 2); err == nil || !strings.Contains(err.Error(), "2 speaker") {
		t.Fatalf("out-of-range error = %v", err)
	}
	if _, err := selectAudioDevice(devices, "mic", 9); err == nil {
		t.Fatal("an out-of-range mic index was accepted")
	}
	if _, err := selectAudioDevice(devices, "line", 0); err == nil {
		t.Fatal("an unknown mode was accepted")
	}

	// A microphone-only host cannot pretend to capture the speakers.
	if _, err := selectAudioDevice([]string{"Microphone (USB)"}, "speaker", 0); err == nil ||
		!strings.Contains(err.Error(), "loopback") {
		t.Fatalf("speaker without loopback = %v", err)
	}
	// A host that only exposes loopback devices still has a usable mic mode.
	if got, err := selectAudioDevice([]string{"virtual-audio-capturer"}, "mic", 0); err != nil || got != "virtual-audio-capturer" {
		t.Fatalf("loopback-only mic fallback = %q err=%v", got, err)
	}
	if _, err := selectAudioDevice(nil, "mic", 0); err == nil {
		t.Fatal("no devices was accepted")
	}
}

func TestIsLoopbackDevice(t *testing.T) {
	for _, name := range []string{"virtual-audio-capturer", "Stereo Mix (Realtek)", "What U Hear", "alsa_output.pci.monitor", "立体声混音"} {
		if !isLoopbackDevice(name) {
			t.Fatalf("%q was not classified as loopback", name)
		}
	}
	for _, name := range []string{"Microphone (Realtek)", "Line In", "麦克风阵列"} {
		if isLoopbackDevice(name) {
			t.Fatalf("%q was classified as loopback", name)
		}
	}
}
