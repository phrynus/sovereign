package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

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
