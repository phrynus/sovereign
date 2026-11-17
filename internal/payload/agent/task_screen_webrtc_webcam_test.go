package main

import (
	"strings"
	"testing"
)

// TestParseDShowVideoDevices pins the camera parser: only "(video)" names are
// kept, alias lines are ignored, and the audio device never appears.
func TestParseDShowVideoDevices(t *testing.T) {
	const sample = "[dshow @ 0x1] \"ASUS FHD webcam\" (video)\n" +
		"[dshow @ 0x1]   Alternative name \"@device_pnp_\\\\?\\usb#vid_3277\"\n" +
		"[dshow @ 0x1] \"Microphone (Realtek)\" (audio)\n" +
		"[dshow @ 0x1] \"OBS Virtual Camera\" (video)\n"
	devices := parseDShowVideoDevices(sample)
	if len(devices) != 2 || devices[0] != "ASUS FHD webcam" || devices[1] != "OBS Virtual Camera" {
		t.Fatalf("video devices = %v", devices)
	}
	if got := parseDShowVideoDevices("no devices\n"); len(got) != 0 {
		t.Fatalf("empty output parsed as %v", got)
	}
}

// TestParseAVFoundationVideoDevices proves the video half is read without the
// audio devices leaking in (they share the last-bracket index format).
func TestParseAVFoundationVideoDevices(t *testing.T) {
	const sample = "[AVFoundation indev @ 0x7f] AVFoundation video devices:\n" +
		"[AVFoundation indev @ 0x7f] [0] FaceTime HD Camera\n" +
		"[AVFoundation indev @ 0x7f] [1] Capture screen 0\n" +
		"[AVFoundation indev @ 0x7f] AVFoundation audio devices:\n" +
		"[AVFoundation indev @ 0x7f] [0] MacBook Pro Microphone\n" +
		"[AVFoundation indev @ 0x7f] [1] BlackHole 2ch\n"
	devices := parseAVFoundationVideoDevices(sample)
	if len(devices) != 2 {
		t.Fatalf("video devices = %+v, want 2", devices)
	}
	if devices[0] != (avDevice{Index: 0, Name: "FaceTime HD Camera"}) ||
		devices[1] != (avDevice{Index: 1, Name: "Capture screen 0"}) {
		t.Fatalf("video devices = %+v", devices)
	}
	if audio := parseAVFoundationAudioDevices(sample); len(audio) != 2 {
		t.Fatalf("audio devices = %+v, want 2", audio)
	}
}

// TestIndexVideoDevices pins the numbering the console sends back: a listed
// index is a position in the platform list, never the name itself.
func TestIndexVideoDevices(t *testing.T) {
	devices := indexVideoDevices([]string{"ASUS FHD webcam", "OBS Virtual Camera"})
	if len(devices) != 2 {
		t.Fatalf("devices = %+v", devices)
	}
	if devices[0].Index != 0 || devices[0].Name != "ASUS FHD webcam" {
		t.Fatalf("first = %+v", devices[0])
	}
	if devices[1].Index != 1 || devices[1].Name != "OBS Virtual Camera" {
		t.Fatalf("second = %+v", devices[1])
	}
	if got := indexVideoDevices(nil); len(got) != 0 {
		t.Fatalf("nil list = %+v", got)
	}
}

func TestSelectIndexedDevice(t *testing.T) {
	devices := []string{"cam0", "cam1"}
	if got, err := selectIndexedDevice(devices, 1, "camera"); err != nil || got != "cam1" {
		t.Fatalf("index 1 = %q err=%v", got, err)
	}
	if _, err := selectIndexedDevice(devices, 2, "camera"); err == nil ||
		!strings.Contains(err.Error(), "camera device 2") {
		t.Fatalf("out-of-range error = %v", err)
	}
	if _, err := selectIndexedDevice(nil, 0, "camera"); err == nil {
		t.Fatal("an empty device list was accepted")
	}
	if _, err := selectIndexedDevice(devices, -1, "camera"); err == nil {
		t.Fatal("a negative index was accepted")
	}
}

// TestBuildWebcamFFmpegArgs covers every camera input path on any host: the
// device selector differs per platform, while scaling/encoder/RTP are shared
// with the screen pipeline.
func TestBuildWebcamFFmpegArgs(t *testing.T) {
	settings := webRTCSettings{Width: 1280, Height: 720, FPS: 30, BitrateKbps: 2000, Encoder: "libx264"}

	windows, err := buildWebcamFFmpegArgsFor("windows", settings, 41000, "ASUS FHD webcam")
	if err != nil {
		t.Fatalf("windows build: %v", err)
	}
	joined := strings.Join(windows, " ")
	if !strings.Contains(joined, "-f dshow") || !strings.Contains(joined, "-i video=ASUS FHD webcam") {
		t.Fatalf("windows camera input missing: %s", joined)
	}
	if !strings.Contains(joined, "scale=1280:720") || !strings.Contains(joined, "rtp://127.0.0.1:41000") {
		t.Fatalf("windows camera output missing: %s", joined)
	}
	if !strings.Contains(joined, "-an") {
		t.Fatalf("camera stream must not carry audio: %s", joined)
	}
	if _, err := buildWebcamFFmpegArgsFor("windows", settings, 41000, "  "); err == nil {
		t.Fatal("an empty dshow camera name was accepted")
	}

	linux, err := buildWebcamFFmpegArgsFor("linux", settings, 41000, "/dev/video2")
	if err != nil || !strings.Contains(strings.Join(linux, " "), "-f v4l2") ||
		!strings.Contains(strings.Join(linux, " "), "-i /dev/video2") {
		t.Fatalf("linux build = %v err=%v", linux, err)
	}
	linuxDefault, err := buildWebcamFFmpegArgsFor("linux", settings, 41000, "")
	if err != nil || !strings.Contains(strings.Join(linuxDefault, " "), "-i /dev/video0") {
		t.Fatalf("linux default build = %v err=%v", linuxDefault, err)
	}
	if _, err := buildWebcamFFmpegArgsFor("linux", settings, 41000, "/etc/passwd"); err == nil {
		t.Fatal("a non-video Linux path was accepted")
	}

	darwin, err := buildWebcamFFmpegArgsFor("darwin", settings, 41000, "1")
	if err != nil || !strings.Contains(strings.Join(darwin, " "), "-f avfoundation") ||
		!strings.Contains(strings.Join(darwin, " "), "-i 1:none") {
		t.Fatalf("darwin build = %v err=%v", darwin, err)
	}
	for _, bad := range []string{"FaceTime HD Camera", "-1", "1:0"} {
		if _, err := buildWebcamFFmpegArgsFor("darwin", settings, 41000, bad); err == nil {
			t.Fatalf("darwin accepted %q as a video index", bad)
		}
	}

	if _, err := buildWebcamFFmpegArgsFor("plan9", settings, 41000, "0"); err == nil {
		t.Fatal("an unsupported platform was accepted")
	}
	if _, err := buildWebcamFFmpegArgsFor("linux", settings, 0, "/dev/video0"); err == nil {
		t.Fatal("an invalid RTP port was accepted")
	}
}

// TestWebcamSharesTheScreenEncoderTail proves the two pipelines produce the
// same scaling and rate control, so camera mode is not a quality regression.
func TestWebcamSharesTheScreenEncoderTail(t *testing.T) {
	settings := webRTCSettings{Width: 1920, Height: 1080, FPS: 30, BitrateKbps: 4500, Encoder: "libx264", Differential: true}
	camera, err := buildWebcamFFmpegArgsFor("linux", settings, 41000, "/dev/video0")
	if err != nil {
		t.Fatalf("camera build: %v", err)
	}
	screen, err := buildScreenFFmpegArgs(settings, 41000, screenRect{Width: 1920, Height: 1080}, true)
	if err != nil {
		t.Fatalf("screen build: %v", err)
	}
	tail := func(args []string) string {
		joined := strings.Join(args, " ")
		i := strings.Index(joined, "-vf")
		return joined[i:]
	}
	if tail(camera) != tail(screen) {
		t.Fatalf("camera and screen tails differ:\ncamera: %s\nscreen: %s", tail(camera), tail(screen))
	}
}
