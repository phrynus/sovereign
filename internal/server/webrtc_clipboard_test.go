package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	forgecrypto "github.com/forgec2/forgec2/internal/crypto"
	"github.com/forgec2/forgec2/internal/db"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

// TestRDWebRTCMessageRelay drives the operator chat path: the message reaches
// the agent bounded and sanitized, an oversized one is refused without being
// forwarded, the dialog rate limit bites, and the agent's delivery report
// comes back to the operator.
func TestRDWebRTCMessageRelay(t *testing.T) {
	s := mustTenantServer(t)
	s.wsUpgrader = websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	if err := s.db.Create(&db.Implant{ID: "rd-msg", TenantID: 1, Hostname: "msg", Status: "online"}).Error; err != nil {
		t.Fatal(err)
	}
	s.cfg.WebRTC.Enabled = true
	sess, token, err := s.webRTCHub().createSession("rd-msg", 7, 1, webrtcStartOptions{}, 10*time.Minute)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	defer sess.end("test")

	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.GET("/probe", func(c *gin.Context) {
		c.Set("user_role", db.RoleAdmin)
		c.Set("user_id", uint(7))
		s.handleRDWebRTCSignal(c)
	})
	engine.GET("/agent", s.handleRDWebRTCAgent)
	srv := httptest.NewServer(engine)
	defer srv.Close()

	agentPeer, _, err := websocket.DefaultDialer.Dial(
		"ws"+strings.TrimPrefix(srv.URL, "http")+"/agent?session="+sess.ID+"&token="+token+"&agent=rd-msg", nil)
	if err != nil {
		t.Fatalf("dial agent: %v", err)
	}
	defer agentPeer.Close()
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http")+"/probe?session="+sess.ID, nil)
	if err != nil {
		t.Fatalf("dial operator: %v", err)
	}
	defer conn.Close()
	if _, _, err := conn.ReadMessage(); err != nil {
		t.Fatalf("opening frame: %v", err)
	}

	send := func(payload string) {
		t.Helper()
		if err := conn.WriteMessage(websocket.TextMessage, []byte(payload)); err != nil {
			t.Fatalf("operator write: %v", err)
		}
	}
	readJSON := func(c *websocket.Conn, wantType string, timeout time.Duration) map[string]any {
		t.Helper()
		deadline := time.Now().Add(timeout)
		for {
			_ = c.SetReadDeadline(deadline)
			_, raw, err := c.ReadMessage()
			if err != nil {
				t.Fatalf("read %s: %v", wantType, err)
			}
			var frame map[string]any
			if err := json.Unmarshal(raw, &frame); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if frame["type"] == wantType {
				return frame
			}
			if time.Now().After(deadline) {
				t.Fatalf("no %s frame (last: %s)", wantType, raw)
			}
		}
	}

	send(`{"type":"message","title":"Notice","text":"please check the ticket"}`)
	frame := readJSON(agentPeer, "message", 3*time.Second)
	if frame["text"] != "please check the ticket" || frame["title"] != "Notice" {
		t.Fatalf("relayed message = %+v", frame)
	}

	// The agent reports the dialog outcome; the operator sees it.
	if err := agentPeer.WriteMessage(websocket.TextMessage,
		[]byte(`{"type":"message_status","status":"shown"}`)); err != nil {
		t.Fatalf("agent write: %v", err)
	}
	status := readJSON(conn, "message_status", 3*time.Second)
	if status["status"] != "shown" {
		t.Fatalf("message status = %+v", status)
	}

	// Oversized text is refused before the agent sees a dialog.
	big := strings.Repeat("a", MaxWebRTCMessageBytes+1)
	send(fmt.Sprintf(`{"type":"message","text":%q}`, big))
	rejected := readJSON(conn, "message_status", 3*time.Second)
	if !strings.Contains(fmt.Sprint(rejected["reason"]), "exceeds") {
		t.Fatalf("oversize refusal = %+v", rejected)
	}
	// Fill the rest of the window, then the next dialog is refused.
	for i := 1; i < MaxRemoteMessagesPerWindow; i++ {
		send(`{"type":"message","text":"ok"}`)
	}
	send(`{"type":"message","text":"one too many"}`)
	limited := readJSON(conn, "message_status", 3*time.Second)
	if !strings.Contains(fmt.Sprint(limited["reason"]), "rate limit") {
		t.Fatalf("rate-limit refusal = %+v", limited)
	}
}

// TestRDWebRTCKeyFeedRelay drives the live keystroke channel: the toggle
// reaches the agent, a keystroke delta reaches the operator, an oversized frame
// is dropped rather than relayed, and disabling reaches the agent.
func TestRDWebRTCKeyFeedRelay(t *testing.T) {
	s := mustTenantServer(t)
	s.wsUpgrader = websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	if err := s.db.Create(&db.Implant{ID: "rd-keys", TenantID: 1, Hostname: "keys", Status: "online"}).Error; err != nil {
		t.Fatal(err)
	}
	s.cfg.WebRTC.Enabled = true
	sess, token, err := s.webRTCHub().createSession("rd-keys", 7, 1, webrtcStartOptions{}, 10*time.Minute)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	defer sess.end("test")

	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.GET("/probe", func(c *gin.Context) {
		c.Set("user_role", db.RoleAdmin)
		c.Set("user_id", uint(7))
		s.handleRDWebRTCSignal(c)
	})
	engine.GET("/agent", s.handleRDWebRTCAgent)
	srv := httptest.NewServer(engine)
	defer srv.Close()

	agentPeer, _, err := websocket.DefaultDialer.Dial(
		"ws"+strings.TrimPrefix(srv.URL, "http")+"/agent?session="+sess.ID+"&token="+token+"&agent=rd-keys", nil)
	if err != nil {
		t.Fatalf("dial agent: %v", err)
	}
	defer agentPeer.Close()
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http")+"/probe?session="+sess.ID, nil)
	if err != nil {
		t.Fatalf("dial operator: %v", err)
	}
	defer conn.Close()
	if _, _, err := conn.ReadMessage(); err != nil {
		t.Fatalf("opening frame: %v", err)
	}

	send := func(payload string) {
		t.Helper()
		if err := conn.WriteMessage(websocket.TextMessage, []byte(payload)); err != nil {
			t.Fatalf("operator write: %v", err)
		}
	}
	readJSON := func(c *websocket.Conn, wantType string, timeout time.Duration) map[string]any {
		t.Helper()
		deadline := time.Now().Add(timeout)
		for {
			_ = c.SetReadDeadline(deadline)
			_, raw, err := c.ReadMessage()
			if err != nil {
				t.Fatalf("read %s: %v", wantType, err)
			}
			var frame map[string]any
			if err := json.Unmarshal(raw, &frame); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if frame["type"] == wantType {
				return frame
			}
			if time.Now().After(deadline) {
				t.Fatalf("no %s frame (last: %s)", wantType, raw)
			}
		}
	}

	send(`{"type":"keys","enabled":true}`)
	on := readJSON(agentPeer, "keys", 3*time.Second)
	if on["enabled"] != true {
		t.Fatalf("enable frame = %+v", on)
	}

	if err := agentPeer.WriteMessage(websocket.TextMessage, []byte(`{"type":"keys","text":"whoami\r"}`)); err != nil {
		t.Fatalf("agent write: %v", err)
	}
	delta := readJSON(conn, "keys", 3*time.Second)
	if delta["text"] != "whoami\r" {
		t.Fatalf("keystroke delta = %+v", delta)
	}

	// A frame beyond the keystroke bound is not relayed.
	huge := fmt.Sprintf(`{"type":"keys","text":%q}`, strings.Repeat("k", MaxWebRTCKeysFrameBytes))
	if err := agentPeer.WriteMessage(websocket.TextMessage, []byte(huge)); err != nil {
		t.Fatalf("agent write huge: %v", err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(400 * time.Millisecond))
	if _, raw, err := conn.ReadMessage(); err == nil {
		t.Fatalf("an oversized keystroke frame was relayed: %d bytes", len(raw))
	}

	send(`{"type":"keys","enabled":false}`)
	off := readJSON(agentPeer, "keys", 3*time.Second)
	if off["enabled"] != false {
		t.Fatalf("disable frame = %+v", off)
	}
}

// TestRDWebRTCDifferentialToggle drives the live change-only mode switch: the
// operator's toggle reaches the agent, the agent's confirmation reaches the
// operator, and a missing flag is refused.
func TestRDWebRTCDifferentialToggle(t *testing.T) {
	s := mustTenantServer(t)
	s.wsUpgrader = websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	if err := s.db.Create(&db.Implant{ID: "rd-diff", TenantID: 1, Hostname: "diff", Status: "online"}).Error; err != nil {
		t.Fatal(err)
	}
	s.cfg.WebRTC.Enabled = true
	sess, token, err := s.webRTCHub().createSession("rd-diff", 7, 1, webrtcStartOptions{Differential: true}, 10*time.Minute)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	defer sess.end("test")

	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.GET("/probe", func(c *gin.Context) {
		c.Set("user_role", db.RoleAdmin)
		c.Set("user_id", uint(7))
		c.Set("user", "alice")
		s.handleRDWebRTCSignal(c)
	})
	engine.GET("/agent", s.handleRDWebRTCAgent)
	srv := httptest.NewServer(engine)
	defer srv.Close()

	agentPeer, _, err := websocket.DefaultDialer.Dial(
		"ws"+strings.TrimPrefix(srv.URL, "http")+"/agent?session="+sess.ID+"&token="+token+"&agent=rd-diff", nil)
	if err != nil {
		t.Fatalf("dial agent: %v", err)
	}
	defer agentPeer.Close()
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http")+"/probe?session="+sess.ID, nil)
	if err != nil {
		t.Fatalf("dial operator: %v", err)
	}
	defer conn.Close()
	if _, _, err := conn.ReadMessage(); err != nil {
		t.Fatalf("opening frame: %v", err)
	}

	send := func(payload string) {
		t.Helper()
		if err := conn.WriteMessage(websocket.TextMessage, []byte(payload)); err != nil {
			t.Fatalf("operator write: %v", err)
		}
	}
	readJSON := func(c *websocket.Conn, wantType string, timeout time.Duration) map[string]any {
		t.Helper()
		deadline := time.Now().Add(timeout)
		for {
			_ = c.SetReadDeadline(deadline)
			_, raw, err := c.ReadMessage()
			if err != nil {
				t.Fatalf("read %s: %v", wantType, err)
			}
			var frame map[string]any
			if err := json.Unmarshal(raw, &frame); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if frame["type"] == wantType {
				return frame
			}
			if time.Now().After(deadline) {
				t.Fatalf("no %s frame (last: %s)", wantType, raw)
			}
		}
	}

	send(`{"type":"differential","enabled":true}`)
	on := readJSON(agentPeer, "differential", 3*time.Second)
	if on["enabled"] != true {
		t.Fatalf("differential toggle = %+v", on)
	}

	// The agent's confirmation reaches the operator.
	if err := agentPeer.WriteMessage(websocket.TextMessage,
		[]byte(`{"type":"differential","enabled":true}`)); err != nil {
		t.Fatalf("agent write: %v", err)
	}
	confirmed := readJSON(conn, "differential", 3*time.Second)
	if confirmed["enabled"] != true {
		t.Fatalf("confirmation = %+v", confirmed)
	}

	// A toggle without the flag is refused instead of restarting the pipeline.
	send(`{"type":"differential"}`)
	rejected := readJSON(conn, "differential", 3*time.Second)
	if !strings.Contains(fmt.Sprint(rejected["reason"]), "enabled") {
		t.Fatalf("missing-flag refusal = %+v", rejected)
	}
}

// TestWebRTCStartCarriesDifferential proves the start request's change-only
// flag survives into the encrypted task payload the agent reads.
func TestWebRTCStartCarriesDifferential(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := mustTenantServer(t)
	s.wsUpgrader = websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	s.cfg.WebRTC.Enabled = true
	s.cfg.WebRTC.TurnAddr = "127.0.0.1:0"
	s.cfg.WebRTC.PublicIP = "127.0.0.1"
	forgecrypto.InitLootEncryption(testStorageKeyHex)
	agent := db.Implant{ID: "rd-diff-start", TenantID: 1, Hostname: "diff", Status: "online", LastSeen: time.Now()}
	if err := s.db.Create(&agent).Error; err != nil {
		t.Fatalf("seed agent: %v", err)
	}

	engine := gin.New()
	engine.POST("/api/agents/:id/webrtc/start", func(c *gin.Context) {
		c.Set("user_role", db.RoleAdmin)
		c.Set("user_id", uint(7))
		s.handleWebRTCStart(c)
	})
	req := httptest.NewRequest(http.MethodPost, "/api/agents/rd-diff-start/webrtc/start",
		strings.NewReader(`{"width":1280,"height":720,"fps":30,"differential":true}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	engine.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("start = %d: %s", w.Code, w.Body.String())
	}
	t.Cleanup(func() {
		if sess := s.webRTCHub().byAgentSession("rd-diff-start"); sess != nil {
			sess.end("test")
		}
	})

	var task db.Task
	if err := s.db.Where("agent_id = ? AND type = ?", "rd-diff-start", "screen_webrtc_start").First(&task).Error; err != nil {
		t.Fatalf("load start task: %v", err)
	}
	var payload struct {
		Settings struct {
			Differential bool `json:"differential"`
		} `json:"settings"`
	}
	if err := json.Unmarshal([]byte(task.Data), &payload); err != nil {
		t.Fatalf("task data: %v (%s)", err, task.Data)
	}
	if !payload.Settings.Differential {
		t.Fatal("differential flag did not survive into the agent payload")
	}
}

// TestSanitizeClipboardPayload pins the server-side rules: NUL and invalid
// UTF-8 are stripped, and an oversized PASTE is refused rather than truncated
// (half a script pasted into a remote shell executes a command nobody wrote).
func TestSanitizeClipboardPayload(t *testing.T) {
	text, err := sanitizeClipboardPayload("hello\x00world")
	if err != nil || text != "helloworld" {
		t.Fatalf("sanitize = %q err=%v", text, err)
	}
	if _, err := sanitizeClipboardPayload(strings.Repeat("a", MaxWebRTCClipboardBytes+1)); err == nil {
		t.Fatal("an oversized paste was accepted")
	}
	if _, err := sanitizeClipboardPayload(strings.Repeat("a", MaxWebRTCClipboardBytes)); err != nil {
		t.Fatalf("an exactly-capped paste was refused: %v", err)
	}
}

// TestRDWebRTCClipboardRelay drives the clipboard path end to end over the
// signaling sockets: an operator paste reaches the agent sanitized, an agent
// clipboard frame reaches the operator, a get is relayed, an oversized paste is
// refused without reaching the agent, and the per-(operator, agent) window
// throttles a flood.
func TestRDWebRTCClipboardRelay(t *testing.T) {
	s := mustTenantServer(t)
	s.wsUpgrader = websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	if err := s.db.Create(&db.Implant{ID: "rd-clip", TenantID: 1, Hostname: "clip", Status: "online"}).Error; err != nil {
		t.Fatal(err)
	}
	s.cfg.WebRTC.Enabled = true
	sess, token, err := s.webRTCHub().createSession("rd-clip", 7, 1, webrtcStartOptions{}, 10*time.Minute)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	defer sess.end("test")

	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.GET("/probe", func(c *gin.Context) {
		c.Set("user_role", db.RoleAdmin)
		c.Set("user_id", uint(7))
		s.handleRDWebRTCSignal(c)
	})
	engine.GET("/agent", s.handleRDWebRTCAgent)
	srv := httptest.NewServer(engine)
	defer srv.Close()

	// The agent connects first, through the real handler, so its read loop is
	// what forwards an agent-side clipboard change to the operator.
	agentPeer, _, err := websocket.DefaultDialer.Dial(
		"ws"+strings.TrimPrefix(srv.URL, "http")+"/agent?session="+sess.ID+"&token="+token+"&agent=rd-clip", nil)
	if err != nil {
		t.Fatalf("dial agent: %v", err)
	}
	defer agentPeer.Close()

	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http")+"/probe?session="+sess.ID, nil)
	if err != nil {
		t.Fatalf("dial operator: %v", err)
	}
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, raw, err := conn.ReadMessage(); err != nil {
		t.Fatalf("opening frame: %v", err)
	} else if !strings.Contains(string(raw), "agent_ready") {
		t.Fatalf("opening frame = %s", raw)
	}

	send := func(payload string) {
		t.Helper()
		if err := conn.WriteMessage(websocket.TextMessage, []byte(payload)); err != nil {
			t.Fatalf("operator write: %v", err)
		}
	}
	readJSON := func(c *websocket.Conn, wantType string, timeout time.Duration) map[string]any {
		t.Helper()
		deadline := time.Now().Add(timeout)
		for {
			_ = c.SetReadDeadline(deadline)
			_, raw, err := c.ReadMessage()
			if err != nil {
				t.Fatalf("read %s: %v", wantType, err)
			}
			var frame map[string]any
			if err := json.Unmarshal(raw, &frame); err != nil {
				t.Fatalf("decode %s: %v", raw, err)
			}
			if frame["type"] == wantType {
				return frame
			}
			if time.Now().After(deadline) {
				t.Fatalf("no %s frame before the deadline (last: %s)", wantType, raw)
			}
		}
	}

	// Operator paste: the server strips the NUL before the agent ever sees it.
	send(`{"type":"clipboard_set","text":"hello\u0000world"}`)
	ev := readJSON(agentPeer, "clipboard_set", 3*time.Second)
	if ev["text"] != "helloworld" {
		t.Fatalf("relayed paste = %q, want the sanitized text", ev["text"])
	}

	// Agent → operator: a clipboard change reaches the console.
	if err := agentPeer.WriteMessage(websocket.TextMessage,
		[]byte(`{"type":"clipboard","text":"from target","source":"agent"}`)); err != nil {
		t.Fatalf("agent write: %v", err)
	}
	op := readJSON(conn, "clipboard", 3*time.Second)
	if op["text"] != "from target" || op["source"] != "agent" {
		t.Fatalf("operator clipboard frame = %+v", op)
	}

	// A get is relayed to the agent.
	send(`{"type":"clipboard_get"}`)
	readJSON(agentPeer, "clipboard_get", 3*time.Second)

	// An oversized paste is refused at the server and never reaches the agent.
	big := strings.Repeat("a", MaxWebRTCClipboardBytes+1)
	send(fmt.Sprintf(`{"type":"clipboard_set","text":%q}`, big))
	rejected := readJSON(conn, "clipboard_error", 3*time.Second)
	if !strings.Contains(fmt.Sprint(rejected["reason"]), "exceeds") {
		t.Fatalf("oversize rejection = %+v", rejected)
	}
	_ = agentPeer.SetReadDeadline(time.Now().Add(400 * time.Millisecond))
	if _, raw, err := agentPeer.ReadMessage(); err == nil {
		t.Fatalf("the oversized paste was relayed anyway: %s", raw)
	}

	// The window allows MaxClipboardSetsPerWindow pastes; one of them is
	// already spent, so fill the rest and expect the next to be refused.
	for i := 1; i < MaxClipboardSetsPerWindow; i++ {
		send(`{"type":"clipboard_set","text":"ok"}`)
	}
	send(`{"type":"clipboard_set","text":"one too many"}`)
	limited := readJSON(conn, "clipboard_error", 3*time.Second)
	if !strings.Contains(fmt.Sprint(limited["reason"]), "rate limit") {
		t.Fatalf("rate-limit rejection = %+v", limited)
	}
}
