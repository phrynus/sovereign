package main

import (
	"fmt"
	"net"
	"os/exec"
	"runtime"
	"strings"

	"github.com/pion/rtp"
)

const (
	audioSampleRate = 48000
	// audioBitrateKbps is voice-grade mono Opus. The desktop video stream is
	// the bandwidth consumer; audio is for "what is being said / played", not
	// for music production.
	audioBitrateKbps = 32
	audioFrameMs     = 20
)

// buildAudioFFmpegArgs builds the capture → Opus → RTP chain that feeds the
// WebRTC audio track. The input side is the only platform-specific part; the
// encoder settings are fixed so the browser's Opus decoder (48 kHz mono,
// 20 ms frames) is always satisfied.
func buildAudioFFmpegArgs(rtpPort int, device string) ([]string, error) {
	if rtpPort <= 0 || rtpPort > 65535 {
		return nil, fmt.Errorf("invalid RTP port")
	}
	args := []string{"-hide_banner", "-loglevel", "error", "-nostdin"}
	switch runtime.GOOS {
	case "windows":
		device = strings.TrimSpace(device)
		if device == "" {
			return nil, fmt.Errorf("no audio capture device found")
		}
		// dshow takes the device name verbatim: "audio=<name>".
		args = append(args, "-f", "dshow", "-i", "audio="+device)
	case "linux":
		// PulseAudio is the default on desktop distributions; ALSA-only hosts
		// answer an explicit error instead of silence.
		args = append(args, "-f", "pulse", "-i", "default")
	case "darwin":
		args = append(args, "-f", "avfoundation", "-i", ":0")
	default:
		return nil, fmt.Errorf("unsupported platform %s", runtime.GOOS)
	}
	args = append(args,
		"-c:a", "libopus",
		"-b:a", fmt.Sprintf("%dk", audioBitrateKbps),
		"-ar", fmt.Sprintf("%d", audioSampleRate),
		"-ac", "1",
		"-application", "lowdelay",
		"-frame_duration", fmt.Sprintf("%d", audioFrameMs),
		"-f", "rtp",
		fmt.Sprintf("rtp://127.0.0.1:%d", rtpPort),
	)
	return args, nil
}

// parseDShowAudioDevices extracts audio capture devices from
// `ffmpeg -list_devices true -f dshow -i dummy`. ffmpeg prints one quoted name
// per device followed by "(audio)" or "(video)", plus an "@device..." alias
// line that must not be mistaken for a name.
func parseDShowAudioDevices(output string) []string {
	var devices []string
	for _, line := range strings.Split(output, "\n") {
		if !strings.Contains(line, "(audio)") {
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

// listDShowAudioDevices asks ffmpeg for the installed DirectShow devices.
func listDShowAudioDevices(ffmpeg string) []string {
	out, _ := exec.Command(ffmpeg,
		"-hide_banner", "-list_devices", "true", "-f", "dshow", "-i", "dummy").CombinedOutput()
	return parseDShowAudioDevices(string(out))
}

// audioDeviceKinds classifies capture devices so "microphone" and
// "speaker/loopback" can be offered as separate sources.
const (
	AudioModeMic     = "mic"
	AudioModeSpeaker = "speaker"
)

// loopbackDeviceHints are the names virtual loopback capture devices use:
// screen-capture tools install virtual-audio-capturer, sound-card drivers
// expose Stereo Mix / What U Hear / Wave Out Mix, and PulseAudio exposes
// ".monitor" sources.
var loopbackDeviceHints = []string{
	"virtual-audio-capturer",
	"stereo mix",
	"what u hear",
	"wave out mix",
	"loopback",
	"monitor",
	"立体声混音",
}

// isLoopbackDevice reports whether a capture device carries what the machine
// plays (rather than what the room says).
func isLoopbackDevice(name string) bool {
	lower := strings.ToLower(name)
	for _, hint := range loopbackDeviceHints {
		if strings.Contains(lower, hint) {
			return true
		}
	}
	return false
}

// selectAudioDevice picks the capture device for a mode and index. It is the
// whole policy in one pure function so the console's choice is testable without
// a sound card:
//
//   - "speaker" (loopback) only accepts a virtual/stereo-mix device; when none
//     is installed it fails with an actionable error instead of silently
//     recording the room.
//   - "mic" prefers real capture devices and falls back to the full list if the
//     host only exposes loopback ones.
//   - an index out of range is an error naming how many devices exist.
func selectAudioDevice(devices []string, mode string, index int) (string, error) {
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode == "" {
		mode = AudioModeMic
	}
	var candidates []string
	switch mode {
	case AudioModeSpeaker:
		for _, d := range devices {
			if isLoopbackDevice(d) {
				candidates = append(candidates, d)
			}
		}
		if len(candidates) == 0 {
			return "", fmt.Errorf("no loopback capture device found: install a virtual-audio-capturer or enable Stereo Mix")
		}
	case AudioModeMic:
		for _, d := range devices {
			if !isLoopbackDevice(d) {
				candidates = append(candidates, d)
			}
		}
		if len(candidates) == 0 {
			candidates = devices
		}
	default:
		return "", fmt.Errorf("audio mode must be mic or speaker")
	}
	if len(candidates) == 0 {
		return "", fmt.Errorf("no audio capture device found")
	}
	if index < 0 || index >= len(candidates) {
		return "", fmt.Errorf("audio device %d is not available (%d %s device(s) found)", index, len(candidates), mode)
	}
	return candidates[index], nil
}

// startAudioPipeline starts the capture→Opus→RTP process and pipes it into the
// existing audio track. The track is added to the peer connection whether or not
// audio is enabled, so toggling it later needs no renegotiation.
func (s *agentWebRTCSession) startAudioPipeline() error {
	if s.audioTrack == nil {
		return fmt.Errorf("audio track is not initialized")
	}
	if s.ctx.Err() != nil {
		return fmt.Errorf("session is already closed")
	}
	s.audioMu.Lock()
	if s.audioCmd != nil {
		s.audioMu.Unlock()
		return nil // already running
	}
	s.audioMu.Unlock()

	settings := s.settingsSnapshot()
	device := ""
	if runtime.GOOS == "windows" {
		devices := listDShowAudioDevices(s.ffmpeg)
		selected, err := selectAudioDevice(devices, settings.AudioMode, settings.AudioDevice)
		if err != nil {
			return err
		}
		device = selected
	} else if strings.EqualFold(settings.AudioMode, AudioModeSpeaker) {
		// PulseAudio's ".monitor" source and macOS BlackHole exist, but neither
		// is enumerable without extra tooling; refuse rather than record the
		// room under a "speaker" label.
		return fmt.Errorf("speaker loopback capture is not supported on this platform yet")
	}
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		return fmt.Errorf("audio RTP listener: %w", err)
	}
	args, err := buildAudioFFmpegArgs(conn.LocalAddr().(*net.UDPAddr).Port, device)
	if err != nil {
		_ = conn.Close()
		return err
	}
	cmd := exec.CommandContext(s.ctx, s.ffmpeg, args...)
	applyHideWindow(cmd)
	stderr := &cappedBuffer{max: 4096}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		_ = conn.Close()
		return fmt.Errorf("audio ffmpeg start: %w", err)
	}

	s.audioMu.Lock()
	s.audioCmd = cmd
	s.audioConn = conn
	gen := s.audioGen
	s.audioMu.Unlock()

	go func() {
		buf := make([]byte, 2048)
		for {
			n, _, readErr := conn.ReadFromUDP(buf)
			if readErr != nil {
				return
			}
			var pkt rtp.Packet
			if err := pkt.Unmarshal(buf[:n]); err != nil {
				continue
			}
			if err := s.audioTrack.WriteRTP(&pkt); err != nil {
				return
			}
		}
	}()
	go func() {
		waitErr := cmd.Wait()
		s.audioMu.Lock()
		stale := s.audioGen != gen
		s.audioMu.Unlock()
		if stale || s.ctx.Err() != nil {
			return
		}
		reason := strings.TrimSpace(stderr.String())
		if reason == "" && waitErr != nil {
			reason = waitErr.Error()
		}
		if reason == "" {
			reason = "audio capture exited"
		}
		if len(reason) > 300 {
			reason = reason[:300]
		}
		_ = s.sendJSON(map[string]any{"type": "audio", "enabled": false, "reason": reason})
	}()

	_ = s.sendJSON(map[string]any{"type": "audio", "enabled": true})
	return nil
}

// stopAudio kills the capture process and its RTP sink without touching video.
func (s *agentWebRTCSession) stopAudio() {
	s.audioMu.Lock()
	s.audioGen++
	cmd := s.audioCmd
	conn := s.audioConn
	s.audioCmd = nil
	s.audioConn = nil
	s.audioMu.Unlock()
	if conn != nil {
		_ = conn.Close()
	}
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}

// setAudio enables, disables or reconfigures live audio. Video keeps running in
// every case. A mode/device change while audio is on restarts only the capture
// process; the Opus track and its negotiated m-line are untouched.
func (s *agentWebRTCSession) setAudio(enabled bool, mode string, device *int) {
	s.settingsMu.Lock()
	changed := s.settings.Audio != enabled
	curMode := s.settings.AudioMode
	if strings.TrimSpace(mode) != "" {
		curMode = strings.ToLower(strings.TrimSpace(mode))
		if s.settings.AudioMode != curMode {
			s.settings.AudioMode = curMode
			changed = true
		}
	}
	if device != nil && s.settings.AudioDevice != *device {
		s.settings.AudioDevice = *device
		changed = true
	}
	s.settings.Audio = enabled
	curDevice := s.settings.AudioDevice
	s.settingsMu.Unlock()

	if !enabled {
		s.stopAudio()
		_ = s.sendJSON(map[string]any{"type": "audio", "enabled": false, "mode": curMode, "device": curDevice})
		return
	}
	if !changed {
		// Already running with this mode/device: just confirm.
		_ = s.sendJSON(map[string]any{"type": "audio", "enabled": true, "mode": curMode, "device": curDevice})
		return
	}
	s.stopAudio()
	if err := s.startAudioPipeline(); err != nil {
		_ = s.sendJSON(map[string]any{"type": "audio", "enabled": false, "mode": curMode, "device": curDevice, "reason": err.Error()})
	}
}
