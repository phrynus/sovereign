package main

import (
	"strings"
	"testing"
)

// TestParsePactlSources pins the `pactl list short sources` parser: the second
// tab-separated column is the source name, monitor and microphone sources are
// both kept verbatim, and malformed lines do not become devices.
func TestParsePactlSources(t *testing.T) {
	const sample = "0\talsa_output.pci-0000_00_1f.3.analog-stereo.monitor\tmodule-alsa-card.c\ts16le 2ch 48000Hz\tSUSPENDED\n" +
		"1\talsa_input.pci-0000_00_1f.3.analog-stereo\tmodule-alsa-card.c\ts16le 2ch 48000Hz\tSUSPENDED\n" +
		"2\tvirtual_speaker.monitor\tmodule-null-sink.c\ts16le 2ch 48000Hz\tIDLE\n" +
		"\n" +
		"not-a-source\n"
	sources := parsePactlSources(sample)
	if len(sources) != 3 {
		t.Fatalf("sources = %v, want 3", sources)
	}
	if sources[0] != "alsa_output.pci-0000_00_1f.3.analog-stereo.monitor" {
		t.Fatalf("first source = %q", sources[0])
	}
	if sources[1] != "alsa_input.pci-0000_00_1f.3.analog-stereo" {
		t.Fatalf("second source = %q", sources[1])
	}
	if sources[2] != "virtual_speaker.monitor" {
		t.Fatalf("third source = %q", sources[2])
	}
	if got := parsePactlSources(""); len(got) != 0 {
		t.Fatalf("empty output parsed as %v", got)
	}
	// A space-separated variant is tolerated (older/pactl-alike output), and a
	// line with a single column is not a source.
	if got := parsePactlSources("0\tmonitor_source.monitor\tmodule-x\ts16le\tRUNNING\nmalformed\n1 real_source module-x s16le RUNNING\n"); len(got) != 2 || got[1] != "real_source" {
		t.Fatalf("space-separated parse = %v", got)
	}
}

func TestIsPulseMonitorSource(t *testing.T) {
	for _, name := range []string{
		"alsa_output.pci-0000_00_1f.3.analog-stereo.monitor",
		"virtual_speaker.monitor",
		"ALSA_OUTPUT.MONITOR",
	} {
		if !isPulseMonitorSource(name) {
			t.Fatalf("%q was not classified as a monitor source", name)
		}
	}
	for _, name := range []string{
		"alsa_input.pci-0000_00_1f.3.analog-stereo",
		"default",
		"monitor", // a source literally named "monitor" is not a .monitor source
	} {
		if isPulseMonitorSource(name) {
			t.Fatalf("%q was classified as a monitor source", name)
		}
	}
}

// TestSelectPulseSource pins the Linux policy: speaker only accepts .monitor
// sources and fails actionably without one; mic uses real sources and falls
// back to PulseAudio's "default"; out-of-range names the count.
func TestSelectPulseSource(t *testing.T) {
	sources := []string{
		"alsa_output.pci.analog-stereo.monitor",
		"alsa_input.pci.analog-stereo",
		"virtual_speaker.monitor",
	}
	speaker, err := selectPulseSource(sources, "speaker", 0)
	if err != nil || speaker != "alsa_output.pci.analog-stereo.monitor" {
		t.Fatalf("speaker[0] = %q err=%v", speaker, err)
	}
	if got, err := selectPulseSource(sources, "speaker", 1); err != nil || got != "virtual_speaker.monitor" {
		t.Fatalf("speaker[1] = %q err=%v", got, err)
	}
	if _, err := selectPulseSource(sources, "speaker", 2); err == nil ||
		!strings.Contains(err.Error(), "2 speaker") {
		t.Fatalf("out-of-range speaker error = %v", err)
	}

	mic, err := selectPulseSource(sources, "mic", 0)
	if err != nil || mic != "alsa_input.pci.analog-stereo" {
		t.Fatalf("mic[0] = %q err=%v", mic, err)
	}
	if _, err := selectPulseSource(sources, "mic", 1); err == nil {
		t.Fatal("an out-of-range mic index was accepted")
	}
	// An empty/default mode is microphone capture.
	if got, err := selectPulseSource(sources, "", 0); err != nil || got != "alsa_input.pci.analog-stereo" {
		t.Fatalf("empty mode = %q err=%v", got, err)
	}

	// Microphone sources list is empty (no pactl): PulseAudio's default still works.
	if got, err := selectPulseSource(nil, "mic", 0); err != nil || got != "default" {
		t.Fatalf("empty mic list = %q err=%v", got, err)
	}
	if _, err := selectPulseSource(nil, "mic", 1); err == nil {
		t.Fatal("an index past the fallback default was accepted")
	}

	// Monitor sources only, no microphone: speaker works, mic uses the default.
	monitorsOnly := []string{"alsa_output.pci.analog-stereo.monitor"}
	if got, err := selectPulseSource(monitorsOnly, "speaker", 0); err != nil || got != monitorsOnly[0] {
		t.Fatalf("monitor-only speaker = %q err=%v", got, err)
	}

	// A microphone-only host cannot pretend to capture the speakers.
	if _, err := selectPulseSource([]string{"alsa_input.usb-mic"}, "speaker", 0); err == nil ||
		!strings.Contains(err.Error(), "monitor") {
		t.Fatalf("speaker without a monitor source = %v", err)
	}
	if _, err := selectPulseSource(sources, "line", 0); err == nil {
		t.Fatal("an unknown mode was accepted")
	}
}
