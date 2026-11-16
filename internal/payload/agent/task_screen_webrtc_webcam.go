package main

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
)

// Video sources for the live desktop's video track.
const (
	VideoSourceScreen = "screen"
	VideoSourceWebcam = "webcam"
	// MaxWebcamDevice bounds the camera selector (per platform enumeration).
	MaxWebcamDevice = 7
)

// buildWebcamFFmpegArgsFor builds the camera → H.264 → RTP chain. The input
// side is platform-specific; scaling, encoder and rate control are shared with
// the screen path (videoOutputArgs) so both look the same to the browser.
//
// `device` is a dshow device name on Windows, a /dev/videoN path on Linux, and
// an AVFoundation video index on macOS.
func buildWebcamFFmpegArgsFor(goos string, settings webRTCSettings, rtpPort int, device string) ([]string, error) {
	if rtpPort <= 0 || rtpPort > 65535 {
		return nil, fmt.Errorf("invalid RTP port")
	}
	args := []string{"-hide_banner", "-loglevel", "error", "-nostdin"}
	switch goos {
	case "windows":
		device = strings.TrimSpace(device)
		if device == "" {
			return nil, fmt.Errorf("no camera capture device found")
		}
		args = append(args, "-f", "dshow", "-framerate", fmt.Sprintf("%d", settings.FPS), "-i", "video="+device)
	case "linux":
		path := strings.TrimSpace(device)
		if path == "" {
			path = "/dev/video0"
		}
		if !strings.HasPrefix(path, "/dev/video") {
			return nil, fmt.Errorf("invalid camera device %q", device)
		}
		args = append(args, "-f", "v4l2", "-framerate", fmt.Sprintf("%d", settings.FPS), "-i", path)
	case "darwin":
		index := strings.TrimSpace(device)
		if index == "" {
			index = "0"
		}
		n, convErr := strconv.Atoi(index)
		if convErr != nil || n < 0 {
			return nil, fmt.Errorf("invalid AVFoundation video index %q", device)
		}
		// "<video>:none" selects a camera without an audio input.
		args = append(args, "-f", "avfoundation", "-framerate", fmt.Sprintf("%d", settings.FPS),
			"-i", strconv.Itoa(n)+":none")
	default:
		return nil, fmt.Errorf("unsupported platform %s", goos)
	}
	args = append(args, videoOutputArgs(settings)...)
	args = append(args,
		"-an",
		"-f", "rtp",
		"-pkt_size", "1200",
		fmt.Sprintf("rtp://127.0.0.1:%d", rtpPort),
	)
	return args, nil
}

// selectIndexedDevice returns devices[index] with an actionable error. It is
// used for the camera selectors where a list position IS the index the console
// sends back.
func selectIndexedDevice(devices []string, index int, kind string) (string, error) {
	if len(devices) == 0 {
		return "", fmt.Errorf("no %s device found", kind)
	}
	if index < 0 || index >= len(devices) {
		return "", fmt.Errorf("%s device %d is not available (%d device(s) found)", kind, index, len(devices))
	}
	return devices[index], nil
}

// parseDShowVideoDevices extracts camera names from
// `ffmpeg -list_devices true -f dshow -i dummy`. It is the video twin of
// parseDShowAudioDevices and deliberately ignores the "@device..." alias lines.
func parseDShowVideoDevices(output string) []string {
	var devices []string
	for _, line := range strings.Split(output, "\n") {
		if !strings.Contains(line, "(video)") {
			continue
		}
		start := strings.Index(line, "\"")
		end := strings.LastIndex(line, "\"")
		if start < 0 || end <= start {
			continue
		}
		name := strings.TrimSpace(line[start+1 : end])
		if name == "" || strings.HasPrefix(name, "@device") {
			continue
		}
		devices = append(devices, name)
	}
	return devices
}

func listDShowVideoDevices(ffmpeg string) []string {
	out, _ := exec.Command(ffmpeg,
		"-hide_banner", "-list_devices", "true", "-f", "dshow", "-i", "dummy").CombinedOutput()
	return parseDShowVideoDevices(string(out))
}

// listLinuxCameraDevices returns the /dev/video* nodes in stable order.
func listLinuxCameraDevices() []string {
	paths, _ := filepath.Glob("/dev/video*")
	sort.Strings(paths)
	return paths
}

// listAVFoundationVideoDevices returns the video half of the AVFoundation
// device list (the indices are what `-i "<n>:none"` expects).
func listAVFoundationVideoDevices(ffmpeg string) ([]avDevice, error) {
	out, _ := exec.Command(ffmpeg,
		"-hide_banner", "-f", "avfoundation", "-list_devices", "true", "-i", "").CombinedOutput()
	devices := parseAVFoundationVideoDevices(string(out))
	if len(devices) == 0 {
		return nil, fmt.Errorf("ffmpeg reported no AVFoundation video devices")
	}
	return devices, nil
}

// webcamDeviceFor resolves the platform camera selector for the operator's
// index. It fails with an actionable message instead of handing ffmpeg an
// arbitrary string.
func (s *agentWebRTCSession) webcamDeviceFor(index int) (string, error) {
	switch runtime.GOOS {
	case "windows":
		name, err := selectIndexedDevice(listDShowVideoDevices(s.ffmpeg), index, "camera")
		if err != nil {
			return "", err
		}
		return name, nil
	case "linux":
		path, err := selectIndexedDevice(listLinuxCameraDevices(), index, "camera")
		if err != nil {
			return "", err
		}
		return path, nil
	case "darwin":
		devices, err := listAVFoundationVideoDevices(s.ffmpeg)
		if err != nil {
			return "", err
		}
		names := make([]string, 0, len(devices))
		for _, device := range devices {
			names = append(names, strconv.Itoa(device.Index))
		}
		return selectIndexedDevice(names, index, "camera")
	default:
		return "", fmt.Errorf("unsupported platform %s", runtime.GOOS)
	}
}
