package main

import (
	"fmt"
	"net"
	"os/exec"
	"runtime"
	"strconv"
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
	return buildAudioFFmpegArgsFor(runtime.GOOS, rtpPort, device)
}

// buildAudioFFmpegArgsFor is buildAudioFFmpegArgs with the platform injected,
// so every input path is testable on any host. On Linux `device` is a
// PulseAudio SOURCE name: a ".monitor" source captures what the machine plays,
// an empty name means the default source. On macOS `device` is the
// AVFoundation AUDIO index (the number inside ":N"); empty means 0.
func buildAudioFFmpegArgsFor(goos string, rtpPort int, device string) ([]string, error) {
	if rtpPort <= 0 || rtpPort > 65535 {
		return nil, fmt.Errorf("invalid RTP port")
	}
	args := []string{"-hide_banner", "-loglevel", "error", "-nostdin"}
	switch goos {
	case "windows":
		device = strings.TrimSpace(device)
		if device == "" {
			return nil, fmt.Errorf("no audio capture device found")
		}
		// dshow takes the device name verbatim: "audio=<name>".
		args = append(args, "-f", "dshow", "-i", "audio="+device)
	case "linux":
		// PulseAudio is the default on desktop distributions; ALSA-only hosts
		// answer an explicit error instead of silence. A named source is how a
		// monitor (loopback) capture is selected.
		source := strings.TrimSpace(device)
		if source == "" {
			source = "default"
		}
		args = append(args, "-f", "pulse", "-i", source)
	case "darwin":
		// avfoundation addresses audio devices by index in an audio-only space,
		// i.e. ":2". A non-numeric "device" would become an ffmpeg option, so it
		// is refused here rather than passed through.
		index := strings.TrimSpace(device)
		if index == "" {
			index = "0"
		}
		n, convErr := strconv.Atoi(index)
		if convErr != nil || n < 0 {
			return nil, fmt.Errorf("invalid AVFoundation audio index %q", device)
		}
		args = append(args, "-f", "avfoundation", "-i", ":"+strconv.Itoa(n))
	default:
		return nil, fmt.Errorf("unsupported platform %s", goos)
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

// avDevice is one AVFoundation device as ffmpeg numbers it. Audio and video
// have separate index spaces, so the same struct serves both sections.
type avDevice struct {
	Index int    `json:"index"`
	Name  string `json:"name"`
}

// darwinLoopbackHints are the virtual audio drivers macOS uses to expose what
// the machine plays. BlackHole (Existential Audio) is the common one;
// Soundflower and Loopback are the older alternatives.
var darwinLoopbackHints = []string{
	"blackhole",
	"soundflower",
	"existential audio",
	"loopback",
	"virtual audio",
}

// isDarwinLoopbackDevice reports whether an AVFoundation device is a virtual
// loopback rather than a microphone.
func isDarwinLoopbackDevice(name string) bool {
	lower := strings.ToLower(name)
	for _, hint := range darwinLoopbackHints {
		if strings.Contains(lower, hint) {
			return true
		}
	}
	return false
}

// parseAVFoundationSection extracts one half of
// `ffmpeg -f avfoundation -list_devices true -i ""`. ffmpeg prints a video
// section and then an audio section; each device line looks like
//
//	[AVFoundation indev @ 0x7f...] [1] BlackHole 2ch
//
// so the device index is the LAST bracketed number on the line.
func parseAVFoundationSection(section, output string) []avDevice {
	var devices []avDevice
	header := "AVFoundation " + section + " devices:"
	other := "AVFoundation video devices:"
	if section == "video" {
		other = "AVFoundation audio devices:"
	}
	inSection := false
	for _, line := range strings.Split(output, "\n") {
		switch {
		case strings.Contains(line, header):
			inSection = true
			continue
		case strings.Contains(line, other):
			inSection = false
			continue
		}
		if !inSection {
			continue
		}
		open := strings.LastIndex(line, "[")
		closeIdx := strings.LastIndex(line, "]")
		if open < 0 || closeIdx <= open {
			continue
		}
		index, err := strconv.Atoi(strings.TrimSpace(line[open+1 : closeIdx]))
		if err != nil || index < 0 {
			continue
		}
		name := strings.TrimSpace(line[closeIdx+1:])
		if name == "" {
			continue
		}
		devices = append(devices, avDevice{Index: index, Name: name})
	}
	return devices
}

// parseAVFoundationAudioDevices is the audio half of the AVFoundation list.
func parseAVFoundationAudioDevices(output string) []avDevice {
	return parseAVFoundationSection("audio", output)
}

// parseAVFoundationVideoDevices is the video half (camera) of the list.
func parseAVFoundationVideoDevices(output string) []avDevice {
	return parseAVFoundationSection("video", output)
}

// listAVFoundationDevices asks ffmpeg for the installed AVFoundation devices.
// ffmpeg exits non-zero for -list_devices by design, so the output is parsed
// regardless of the exit status.
func listAVFoundationDevices(ffmpeg string) ([]avDevice, error) {
	out, _ := exec.Command(ffmpeg,
		"-hide_banner", "-f", "avfoundation", "-list_devices", "true", "-i", "").CombinedOutput()
	devices := parseAVFoundationAudioDevices(string(out))
	if len(devices) == 0 {
		detail := strings.TrimSpace(string(out))
		if len(detail) > 200 {
			detail = detail[:200]
		}
		if detail == "" {
			return nil, fmt.Errorf("ffmpeg reported no AVFoundation audio devices")
		}
		return nil, fmt.Errorf("ffmpeg reported no AVFoundation audio devices: %s", detail)
	}
	return devices, nil
}

// selectAVFoundationDevice picks the device for a mode and index:
//
//   - "speaker" (loopback) only accepts a virtual audio device (BlackHole and
//     friends) and fails with an actionable message when none is installed,
//     instead of recording the room under a "speaker" label;
//   - "mic" uses real capture devices, falling back to AVFoundation input 0
//     when the list is empty;
//   - an out-of-range index names how many devices that mode offers.
func selectAVFoundationDevice(devices []avDevice, mode string, index int) (avDevice, error) {
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode == "" {
		mode = AudioModeMic
	}
	var candidates []avDevice
	switch mode {
	case AudioModeSpeaker:
		for _, device := range devices {
			if isDarwinLoopbackDevice(device.Name) {
				candidates = append(candidates, device)
			}
		}
		if len(candidates) == 0 {
			return avDevice{}, fmt.Errorf("no macOS loopback audio device found: install BlackHole (or another virtual audio device) and route the system output to it, or capture the microphone instead")
		}
	case AudioModeMic:
		for _, device := range devices {
			if !isDarwinLoopbackDevice(device.Name) {
				candidates = append(candidates, device)
			}
		}
		if len(candidates) == 0 {
			candidates = []avDevice{{Index: 0, Name: "default"}}
		}
	default:
		return avDevice{}, fmt.Errorf("audio mode must be mic or speaker")
	}
	if index < 0 || index >= len(candidates) {
		return avDevice{}, fmt.Errorf("audio device %d is not available (%d %s device(s) found)", index, len(candidates), mode)
	}
	return candidates[index], nil
}

// audioDeviceInfo is one selectable capture source, as sent to the console.
// On Windows/Linux Index is the position within the mode's filtered list (what
// audio_device means there); on macOS it is the AVFoundation audio index.
type audioDeviceInfo struct {
	Index    int    `json:"index"`
	Name     string `json:"name"`
	Loopback bool   `json:"loopback"`
}

// audioDevicesForMode enumerates what the agent would accept for a mode, so the
// console offers names instead of an index the operator has to guess. The
// filtering mirrors the three selectors exactly: a listed index is always a
// valid audio_device value for that mode.
func (s *agentWebRTCSession) audioDevicesForMode(mode string) ([]audioDeviceInfo, error) {
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode == "" {
		mode = AudioModeMic
	}
	if mode != AudioModeMic && mode != AudioModeSpeaker {
		return nil, fmt.Errorf("audio mode must be mic or speaker")
	}
	include := func(loopback bool) bool { return loopback == (mode == AudioModeSpeaker) }

	switch runtime.GOOS {
	case "windows":
		devices := listDShowAudioDevices(s.ffmpeg)
		out := make([]audioDeviceInfo, 0, len(devices))
		for _, name := range devices {
			loopback := isLoopbackDevice(name)
			if include(loopback) {
				out = append(out, audioDeviceInfo{Index: len(out), Name: name, Loopback: loopback})
			}
		}
		if len(out) == 0 && mode == AudioModeMic {
			for _, name := range devices {
				out = append(out, audioDeviceInfo{Index: len(out), Name: name, Loopback: isLoopbackDevice(name)})
			}
		}
		return out, nil
	case "linux":
		sources, err := listPulseSources()
		if err != nil {
			if mode == AudioModeSpeaker {
				return nil, err
			}
			return []audioDeviceInfo{{Index: 0, Name: "default"}}, nil
		}
		out := make([]audioDeviceInfo, 0, len(sources))
		for _, name := range sources {
			loopback := isPulseMonitorSource(name)
			if include(loopback) {
				out = append(out, audioDeviceInfo{Index: len(out), Name: name, Loopback: loopback})
			}
		}
		if len(out) == 0 && mode == AudioModeMic {
			out = append(out, audioDeviceInfo{Index: 0, Name: "default"})
		}
		return out, nil
	case "darwin":
		devices, err := listAVFoundationDevices(s.ffmpeg)
		if err != nil {
			if mode == AudioModeSpeaker {
				return nil, err
			}
			return []audioDeviceInfo{{Index: 0, Name: "default"}}, nil
		}
		out := make([]audioDeviceInfo, 0, len(devices))
		for _, device := range devices {
			loopback := isDarwinLoopbackDevice(device.Name)
			if include(loopback) {
				out = append(out, audioDeviceInfo{Index: device.Index, Name: device.Name, Loopback: loopback})
			}
		}
		if len(out) == 0 && mode == AudioModeMic {
			out = append(out, audioDeviceInfo{Index: 0, Name: "default"})
		}
		return out, nil
	default:
		return nil, fmt.Errorf("unsupported platform %s", runtime.GOOS)
	}
}

// parsePactlSources extracts source NAMES from `pactl list short sources`.
// The format is tab-separated: index, name, driver, sample spec, state (and
// optionally more columns). A line without at least two columns is not a
// source and is skipped rather than becoming a device called "0".
func parsePactlSources(output string) []string {
	var sources []string
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) < 2 {
			// Some builds separate columns with spaces instead of tabs; accept
			// "index name ..." only when the first field is numeric.
			fields = strings.Fields(line)
			if len(fields) < 2 {
				continue
			}
		}
		name := strings.TrimSpace(fields[1])
		if name == "" {
			continue
		}
		sources = append(sources, name)
	}
	return sources
}

// isPulseMonitorSource reports whether a PulseAudio source captures a sink's
// output (what the machine plays) rather than a microphone. PulseAudio names
// monitor sources with a ".monitor" suffix.
func isPulseMonitorSource(name string) bool {
	return strings.HasSuffix(strings.ToLower(strings.TrimSpace(name)), ".monitor")
}

// listPulseSources asks pactl for the available sources. A missing pactl is an
// error the caller may tolerate for microphone capture (PulseAudio's default
// source works without enumeration) but never for speaker capture.
func listPulseSources() ([]string, error) {
	if _, err := exec.LookPath("pactl"); err != nil {
		return nil, fmt.Errorf("pactl not found (install pulseaudio-utils to list monitor sources)")
	}
	out, err := exec.Command("pactl", "list", "short", "sources").Output()
	if err != nil {
		return nil, fmt.Errorf("pactl list short sources: %w", err)
	}
	return parsePactlSources(string(out)), nil
}

// selectPulseSource picks the PulseAudio source for a mode and index:
//
//   - "speaker" (loopback) only accepts ".monitor" sources, and fails with an
//     actionable message when the host exposes none, instead of recording the
//     room under a "speaker" label;
//   - "mic" uses real capture sources, falling back to PulseAudio's "default"
//     source when the list is empty (a host without pactl still captures);
//   - an out-of-range index names how many sources that mode offers.
func selectPulseSource(sources []string, mode string, index int) (string, error) {
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode == "" {
		mode = AudioModeMic
	}
	var candidates []string
	switch mode {
	case AudioModeSpeaker:
		for _, source := range sources {
			if isPulseMonitorSource(source) {
				candidates = append(candidates, source)
			}
		}
		if len(candidates) == 0 {
			return "", fmt.Errorf("no PulseAudio monitor source found: run \"pactl list short sources\" (a loaded sink exposes a .monitor source)")
		}
	case AudioModeMic:
		for _, source := range sources {
			if !isPulseMonitorSource(source) {
				candidates = append(candidates, source)
			}
		}
		if len(candidates) == 0 {
			candidates = []string{"default"}
		}
	default:
		return "", fmt.Errorf("audio mode must be mic or speaker")
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
	mode := strings.ToLower(strings.TrimSpace(settings.AudioMode))
	if mode == "" {
		mode = AudioModeMic
	}
	device := ""
	switch runtime.GOOS {
	case "windows":
		devices := listDShowAudioDevices(s.ffmpeg)
		selected, err := selectAudioDevice(devices, mode, settings.AudioDevice)
		if err != nil {
			return err
		}
		device = selected
	case "linux":
		sources, listErr := listPulseSources()
		if listErr != nil && mode == AudioModeSpeaker {
			// Without enumeration a speaker capture would silently record the
			// room, which is exactly what the mode exists to avoid.
			return listErr
		}
		selected, err := selectPulseSource(sources, mode, settings.AudioDevice)
		if err != nil {
			return err
		}
		device = selected
	case "darwin":
		devices, listErr := listAVFoundationDevices(s.ffmpeg)
		if listErr != nil {
			if mode == AudioModeSpeaker {
				return listErr
			}
			// Microphone capture still works on AVFoundation's default input.
			device = ""
			break
		}
		selected, err := selectAVFoundationDevice(devices, mode, settings.AudioDevice)
		if err != nil {
			return err
		}
		device = strconv.Itoa(selected.Index)
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
	s.audioSource = device
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

	_ = s.sendJSON(map[string]any{"type": "audio", "enabled": true, "mode": mode, "device": settings.AudioDevice, "source": device})
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
	s.audioSource = ""
	s.audioMu.Unlock()
	if conn != nil {
		_ = conn.Close()
	}
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}

// audioSourceSnapshot reports the source name the running capture resolved to
// (a dshow device, a PulseAudio source, or "" on platforms without a selector).
func (s *agentWebRTCSession) audioSourceSnapshot() string {
	s.audioMu.Lock()
	defer s.audioMu.Unlock()
	return s.audioSource
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
		// Already running with this mode/device: just confirm, naming the source
		// that is actually being captured.
		_ = s.sendJSON(map[string]any{"type": "audio", "enabled": true, "mode": curMode, "device": curDevice, "source": s.audioSourceSnapshot()})
		return
	}
	s.stopAudio()
	if err := s.startAudioPipeline(); err != nil {
		_ = s.sendJSON(map[string]any{"type": "audio", "enabled": false, "mode": curMode, "device": curDevice, "reason": err.Error()})
	}
}
