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

// TestRDWebRTCInputRespectsControlLease proves the live relay honours the
// per-agent lease: the session owner is refused while another operator holds
// control, the refusal names them, an explicit take-over transfers it (and
// reaches the agent), and the next input flows.
func TestRDWebRTCInputRespectsControlLease(t *testing.T) {
	s := mustTenantServer(t)
	s.wsUpgrader = websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	if err := s.db.Create(&db.Implant{ID: "rd-lease", TenantID: 1, Hostname: "lease", Status: "online"}).Error; err != nil {
		t.Fatal(err)
	}
	s.cfg.WebRTC.Enabled = true
	sess, token, err := s.webRTCHub().createSession("rd-lease", 7, 1, webrtcStartOptions{}, 10*time.Minute)
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
		"ws"+strings.TrimPrefix(srv.URL, "http")+"/agent?session="+sess.ID+"&token="+token+"&agent=rd-lease", nil)
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

	// Operator 9 (another session/console) holds the beacon.
	if _, ok := s.claimControl("rd-lease", 9, "bob", false); !ok {
		t.Fatal("bob could not claim a free lease")
	}
	send(`{"type":"input","event":{"type":"click","x":1,"y":2}}`)
	rejected := readJSON(conn, "input_rejected", 3*time.Second)
	if !strings.Contains(fmt.Sprint(rejected["error"]), "bob") {
		t.Fatalf("refusal does not name the holder: %+v", rejected)
	}

	// Explicit take-over, then the same input reaches the agent.
	send(`{"type":"control","action":"take"}`)
	control := readJSON(conn, "control", 3*time.Second)
	if control["holder"] != "alice" || control["you"] != true {
		t.Fatalf("take-over frame = %+v", control)
	}
	send(`{"type":"input","event":{"type":"click","x":3,"y":4}}`)
	ev := readJSON(agentPeer, "input", 3*time.Second)
	if ev["event"] == nil {
		t.Fatalf("agent received no input after take-over: %+v", ev)
	}

	// Releasing frees the beacon for the other operator.
	send(`{"type":"control","action":"release"}`)
	rel := readJSON(conn, "control", 3*time.Second)
	if rel["holder"] != "" {
		t.Fatalf("release frame still names a holder: %+v", rel)
	}
	if _, held := s.controlHolderFor("rd-lease"); held {
		t.Fatal("release left the lease in place")
	}
}
