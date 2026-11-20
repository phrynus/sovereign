package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/r0si/sovereign/internal/db"
)

// TestRDWebRTCSignalRequiresSessionAndOwnership covers the refusals that run
// before the upgrade: a missing session id, and a session owned by a different
// operator. Both must be answered as HTTP errors, never as an upgraded socket.
func TestRDWebRTCSignalRequiresSessionAndOwnership(t *testing.T) {
	s := mustTenantServer(t)
	s.wsUpgrader = websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	if err := s.db.Create(&db.Implant{ID: "rd-agent", TenantID: 1, Hostname: "rd", Status: "online"}).Error; err != nil {
		t.Fatal(err)
	}
	s.cfg.WebRTC.Enabled = true
	sess, _, err := s.webRTCHub().createSession("rd-agent", 7, 1, webrtcStartOptions{}, 10*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	defer sess.end("test")

	serve := func(uid uint) *httptest.Server {
		gin.SetMode(gin.TestMode)
		engine := gin.New()
		engine.GET("/probe", func(c *gin.Context) {
			c.Set("user_role", db.RoleAdmin)
			c.Set("user_id", uid)
			s.handleRDWebRTCSignal(c)
		})
		srv := httptest.NewServer(engine)
		t.Cleanup(srv.Close)
		return srv
	}

	wsBase := func(srv *httptest.Server) string {
		return "ws" + strings.TrimPrefix(srv.URL, "http")
	}

	// Missing session parameter → 400, no upgrade.
	owner := serve(7)
	_, resp, err := websocket.DefaultDialer.Dial(wsBase(owner)+"/probe", nil)
	if err == nil {
		t.Fatal("a dial without a session id was allowed")
	}
	if resp == nil || resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("missing session status = %v", resp)
	}

	// A session owned by operator 7 is invisible to operator 8 → 404.
	foreign := serve(8)
	_, resp, err = websocket.DefaultDialer.Dial(wsBase(foreign)+"/probe?session="+sess.ID, nil)
	if err == nil {
		t.Fatal("a foreign operator was allowed to attach")
	}
	if resp == nil || resp.StatusCode != http.StatusNotFound {
		t.Fatalf("foreign session status = %v", resp)
	}

	// The owner gets past the guards: the socket upgrades and the session
	// reports the agent as not yet attached (the normal order: the browser
	// connects first). Before the attachOperator fix this path closed the
	// operator socket immediately, so no frame ever arrived.
	conn, _, err := websocket.DefaultDialer.Dial(wsBase(owner)+"/probe?session="+sess.ID, nil)
	if err != nil {
		t.Fatalf("owner dial: %v", err)
	}
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, raw, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("operator received no frame: %v", err)
	}
	var frame map[string]any
	if err := json.Unmarshal(raw, &frame); err != nil {
		t.Fatalf("decode frame %s: %v", raw, err)
	}
	if frame["type"] != "waiting" {
		t.Fatalf("first frame = %+v, want waiting while the agent is absent", frame)
	}
}

// TestRDWebRTCInputRelayCarriesExtendedFields proves the live signaling relay
// forwards the whole input contract, not just move/left-click: the console's
// right click, click count, wheel delta and modifier chord must reach the agent
// unchanged (normalised), and an invalid event must be answered rather than
// silently dropped.
func TestRDWebRTCInputRelayCarriesExtendedFields(t *testing.T) {
	s := mustTenantServer(t)
	s.wsUpgrader = websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	if err := s.db.Create(&db.Implant{ID: "rd-input", TenantID: 1, Hostname: "input", Status: "online"}).Error; err != nil {
		t.Fatal(err)
	}
	s.cfg.WebRTC.Enabled = true
	sess, _, err := s.webRTCHub().createSession("rd-input", 7, 1, webrtcStartOptions{}, 10*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	defer sess.end("test")

	// A real paired socket stands in for the agent's signaling connection, so
	// the relay's write path is the one under test.
	agentSide, agentPeer := newTestWSPair(t)
	if _, ok := sess.attachAgent(agentSide); !ok {
		t.Fatal("attachAgent refused a fresh socket")
	}

	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.GET("/probe", func(c *gin.Context) {
		c.Set("user_role", db.RoleAdmin)
		c.Set("user_id", uint(7))
		s.handleRDWebRTCSignal(c)
	})
	srv := httptest.NewServer(engine)
	defer srv.Close()

	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http")+"/probe?session="+sess.ID, nil)
	if err != nil {
		t.Fatalf("dial operator: %v", err)
	}
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, raw, err := conn.ReadMessage(); err != nil {
		t.Fatalf("read opening frame: %v", err)
	} else if !strings.Contains(string(raw), "agent_ready") {
		t.Fatalf("opening frame = %s, want agent_ready", raw)
	}

	agentPeer.SetReadDeadline(time.Now().Add(5 * time.Second))
	readAgentInput := func() map[string]any {
		t.Helper()
		_, raw, err := agentPeer.ReadMessage()
		if err != nil {
			t.Fatalf("agent read: %v", err)
		}
		var frame struct {
			Type  string          `json:"type"`
			Event json.RawMessage `json:"event"`
		}
		if err := json.Unmarshal(raw, &frame); err != nil {
			t.Fatalf("decode relayed frame %s: %v", raw, err)
		}
		if frame.Type != "input" {
			t.Fatalf("relayed frame type = %q, want input (%s)", frame.Type, raw)
		}
		var ev map[string]any
		if err := json.Unmarshal(frame.Event, &ev); err != nil {
			t.Fatalf("decode event %s: %v", frame.Event, err)
		}
		return ev
	}
	send := func(payload string) {
		t.Helper()
		if err := conn.WriteMessage(websocket.TextMessage, []byte(payload)); err != nil {
			t.Fatalf("operator write: %v", err)
		}
	}

	send(`{"type":"input","event":{"type":"CLICK_RIGHT","x":640,"y":360,"count":2,"mods":["CTRL","shift"]}}`)
	ev := readAgentInput()
	if ev["type"] != "click_right" {
		t.Fatalf("relayed type = %v, want click_right", ev["type"])
	}
	if ev["count"] != float64(2) || ev["x"] != float64(640) || ev["y"] != float64(360) {
		t.Fatalf("relayed event lost count/coords: %+v", ev)
	}
	mods, _ := ev["mods"].([]any)
	if len(mods) != 2 || mods[0] != "ctrl" || mods[1] != "shift" {
		t.Fatalf("relayed mods = %v, want [ctrl shift]", ev["mods"])
	}

	send(`{"type":"input","event":{"type":"wheel","x":10,"y":20,"delta":-3}}`)
	ev = readAgentInput()
	if ev["type"] != "wheel" || ev["delta"] != float64(-3) {
		t.Fatalf("relayed wheel = %+v", ev)
	}

	// An invalid event is answered with input_rejected instead of being
	// dropped at a bare continue (the operator used to see nothing happen).
	send(`{"type":"input","event":{"type":"key","key":"a","mods":["hyper"]}}`)
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, raw, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read rejection: %v", err)
	}
	if !strings.Contains(string(raw), "input_rejected") {
		t.Fatalf("invalid event answer = %s, want input_rejected", raw)
	}

	// A live monitor switch reaches the agent, and an out-of-range index is
	// refused at the server so the agent never has to guess.
	send(`{"type":"display","display":2}`)
	agentPeer.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, rawAgent, err := agentPeer.ReadMessage()
	if err != nil {
		t.Fatalf("agent read display switch: %v", err)
	}
	if !strings.Contains(string(rawAgent), `"type":"display"`) || !strings.Contains(string(rawAgent), `"display":2`) {
		t.Fatalf("display switch frame = %s", rawAgent)
	}
	send(`{"type":"display","display":99}`)
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, raw, err = conn.ReadMessage()
	if err != nil {
		t.Fatalf("read display rejection: %v", err)
	}
	if !strings.Contains(string(raw), "invalid display index") {
		t.Fatalf("invalid display answer = %s", raw)
	}
}

// TestRDWebRTCInputRelayRateLimitsActions proves the live path uses the action
// budget: a flood of clicks is refused with input_rejected once the window is
// spent, rather than injecting an unbounded stream into the target.
func TestRDWebRTCInputRelayRateLimitsActions(t *testing.T) {
	s := mustTenantServer(t)
	s.wsUpgrader = websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	if err := s.db.Create(&db.Implant{ID: "rd-flood", TenantID: 1, Hostname: "flood", Status: "online"}).Error; err != nil {
		t.Fatal(err)
	}
	s.cfg.WebRTC.Enabled = true
	sess, _, err := s.webRTCHub().createSession("rd-flood", 7, 1, webrtcStartOptions{}, 10*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	defer sess.end("test")

	agentSide, agentPeer := newTestWSPair(t)
	if _, ok := sess.attachAgent(agentSide); !ok {
		t.Fatal("attachAgent refused a fresh socket")
	}
	// Drain the relayed events on a goroutine so the write never blocks.
	go func() {
		for {
			if _, _, err := agentPeer.ReadMessage(); err != nil {
				return
			}
		}
	}()

	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.GET("/probe", func(c *gin.Context) {
		c.Set("user_role", db.RoleAdmin)
		c.Set("user_id", uint(7))
		s.handleRDWebRTCSignal(c)
	})
	srv := httptest.NewServer(engine)
	defer srv.Close()
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http")+"/probe?session="+sess.ID, nil)
	if err != nil {
		t.Fatalf("dial operator: %v", err)
	}
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, _, err := conn.ReadMessage(); err != nil { // agent_ready
		t.Fatalf("opening frame: %v", err)
	}

	rejected := false
	for i := 0; i < MaxRemoteInputEventsPerWindow+5; i++ {
		if err := conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"input","event":{"type":"click","x":1,"y":2}}`)); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}
	// The refusals arrive after the accepted events; read until one is seen.
	for i := 0; i < 6; i++ {
		_, raw, err := conn.ReadMessage()
		if err != nil {
			break
		}
		if strings.Contains(string(raw), "rate limit") {
			rejected = true
			break
		}
	}
	if !rejected {
		t.Fatalf("no rate-limit rejection among %d clicks (budget %d)", MaxRemoteInputEventsPerWindow+5, MaxRemoteInputEventsPerWindow)
	}
}
func TestRDWebRTCSignalOperatorLoop(t *testing.T) {
	s := mustTenantServer(t)
	s.wsUpgrader = websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	if err := s.db.Create(&db.Implant{ID: "rd-loop", TenantID: 1, Hostname: "loop", Status: "online"}).Error; err != nil {
		t.Fatal(err)
	}
	s.cfg.WebRTC.Enabled = true
	sess, _, err := s.webRTCHub().createSession("rd-loop", 7, 1, webrtcStartOptions{}, 10*time.Minute)
	if err != nil {
		t.Fatal(err)
	}

	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.GET("/probe", func(c *gin.Context) {
		c.Set("user_role", db.RoleAdmin)
		c.Set("user_id", uint(7))
		s.handleRDWebRTCSignal(c)
	})
	srv := httptest.NewServer(engine)
	defer srv.Close()

	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http")+"/probe?session="+sess.ID, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))

	readFrame := func() map[string]any {
		_, raw, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("read frame: %v", err)
		}
		var m map[string]any
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatalf("decode %s: %v", raw, err)
		}
		return m
	}
	send := func(payload string) {
		if err := conn.WriteMessage(websocket.TextMessage, []byte(payload)); err != nil {
			t.Fatalf("write %s: %v", payload, err)
		}
	}

	if frame := readFrame(); frame["type"] != "waiting" {
		t.Fatalf("initial frame = %+v", frame)
	}

	send(`{"type":"ping"}`)
	if frame := readFrame(); frame["type"] != "pong" {
		t.Fatalf("ping frame = %+v", frame)
	}

	send(`{"type":"teleport"}`)
	if frame := readFrame(); frame["type"] != "error" {
		t.Fatalf("unknown type frame = %+v", frame)
	}

	// An offer with no agent attached is acknowledged with waiting and buffered.
	send(`{"type":"offer","sdp":"v=0"}`)
	if frame := readFrame(); frame["type"] != "waiting" {
		t.Fatalf("offer frame = %+v", frame)
	}
	// The "waiting" frame is written BEFORE the offer is appended to the
	// buffer, so reading `pending` straight after the frame is a race. Poll
	// briefly instead of failing on a scheduler hiccup.
	deadline := time.Now().Add(2 * time.Second)
	for {
		sess.mu.Lock()
		pending := len(sess.pending)
		sess.mu.Unlock()
		if pending == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("buffered frames = %d, want the offer", pending)
		}
		time.Sleep(5 * time.Millisecond)
	}

	// An empty SDP is rejected instead of buffered.
	send(`{"type":"offer","sdp":""}`)
	if frame := readFrame(); frame["type"] != "error" {
		t.Fatalf("empty offer frame = %+v", frame)
	}
}
