package main

import (
	"runtime"
	"strings"
	"testing"
)

func TestBuildAudioFFmpegArgs(t *testing.T) {
	args, err := buildAudioFFmpegArgs(40000, "Microphone (Realtek)")
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "rtp://127.0.0.1:40000") {
		t.Fatalf("RTP output missing: %s", joined)
	}
	for _, want := range []string{"-c:a libopus", "-ar 48000", "-ac 1", "-application lowdelay", "-frame_duration 20"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("audio encoder option %q missing: %s", want, joined)
		}
	}
	switch runtime.GOOS {
	case "windows":
		if !strings.Contains(joined, "-f dshow") || !strings.Contains(joined, "audio=Microphone (Realtek)") {
			t.Fatalf("windows capture missing: %s", joined)
		}
		// A Windows build without a device name cannot guess one.
		if _, err := buildAudioFFmpegArgs(40000, "   "); err == nil {
			t.Fatal("an empty dshow device was accepted")
		}
	case "linux":
		if !strings.Contains(joined, "-f pulse") {
			t.Fatalf("linux capture missing: %s", joined)
		}
	case "darwin":
		if !strings.Contains(joined, "-f avfoundation") {
			t.Fatalf("darwin capture missing: %s", joined)
		}
	}
	if _, err := buildAudioFFmpegArgs(0, "dev"); err == nil {
		t.Fatal("invalid port must fail")
	}
}

// TestParseDShowAudioDevices pins the ffmpeg -list_devices parsing: only
// quoted "(audio)" names are devices, and the "@device..." alias line must not
// be mistaken for one.
// TestBuildAudioFFmpegArgsForPlatforms exercises every input path on any host,
// which the runtime-switched test cannot do: Linux must use a named
// PulseAudio source (a .monitor source is how speaker capture is selected) and
// fall back to "default" only when no source was chosen.
func TestBuildAudioFFmpegArgsForPlatforms(t *testing.T) {
	monitor := "alsa_output.pci-0000_00_1f.3.analog-stereo.monitor"

	linux, err := buildAudioFFmpegArgsFor("linux", 41000, monitor)
	if err != nil {
		t.Fatalf("linux build: %v", err)
	}
	joined := strings.Join(linux, " ")
	if !strings.Contains(joined, "-f pulse -i "+monitor) {
		t.Fatalf("linux monitor source missing: %s", joined)
	}
	if strings.Contains(joined, "-i default") {
		t.Fatalf("an explicit monitor source was replaced by default: %s", joined)
	}

	linuxDefault, err := buildAudioFFmpegArgsFor("linux", 41000, "  ")
	if err != nil {
		t.Fatalf("linux default build: %v", err)
	}
	if !strings.Contains(strings.Join(linuxDefault, " "), "-f pulse -i default") {
		t.Fatalf("linux default source missing: %v", linuxDefault)
	}

	windows, err := buildAudioFFmpegArgsFor("windows", 41000, "Microphone (Realtek)")
	if err != nil || !strings.Contains(strings.Join(windows, " "), `-f dshow -i audio=Microphone (Realtek)`) {
		t.Fatalf("windows build = %v err=%v", windows, err)
	}
	if _, err := buildAudioFFmpegArgsFor("windows", 41000, ""); err == nil {
		t.Fatal("an empty dshow device was accepted")
	}

	darwin, err := buildAudioFFmpegArgsFor("darwin", 41000, "")
	if err != nil || !strings.Contains(strings.Join(darwin, " "), "-f avfoundation -i :0") {
		t.Fatalf("darwin build = %v err=%v", darwin, err)
	}

	if _, err := buildAudioFFmpegArgsFor("plan9", 41000, ""); err == nil {
		t.Fatal("an unsupported platform was accepted")
	}
	if _, err := buildAudioFFmpegArgsFor("linux", 0, ""); err == nil {
		t.Fatal("invalid port must fail")
	}
}

func TestParseDShowAudioDevices(t *testing.T) {
	const sample = `[dshow @ 000001] "ASUS FHD webcam" (video)
[dshow @ 000001]   Alternative name "@device_pnp_\\?\usb#vid_3277"
[dshow @ 000001] "麦克风阵列 (适用于数字麦克风的英特尔® 智音技术)" (audio)
[dshow @ 000001]   Alternative name "@device_cm_{33D9A762}"
[dshow @ 000001] "Line In (Realtek)" (audio)
`
	devices := parseDShowAudioDevices(sample)
	if len(devices) != 2 {
		t.Fatalf("devices = %v, want 2 audio devices", devices)
	}
	if devices[0] != "麦克风阵列 (适用于数字麦克风的英特尔® 智音技术)" {
		t.Fatalf("first device = %q", devices[0])
	}
	if devices[1] != "Line In (Realtek)" {
		t.Fatalf("second device = %q", devices[1])
	}
	if got := parseDShowAudioDevices("no devices here\n"); len(got) != 0 {
		t.Fatalf("no-device output parsed as %v", got)
	}
}
