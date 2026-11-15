package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/gorilla/websocket"
)

// newTestWSPair returns the server-side and browser-side ends of one real
// WebSocket connection. Real sockets are what make "the client was closed"
// observable rather than inferred from a counter.
func newTestWSPair(t *testing.T) (serverSide, clientSide *websocket.Conn) {
	t.Helper()
	up := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	connCh := make(chan *websocket.Conn, 1)
	release := make(chan struct{})
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		connCh <- c
		<-release
	}))
	t.Cleanup(func() {
		close(release)
		ts.Close()
	})

	c, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(ts.URL, "http"), nil)
	if err != nil {
		t.Fatalf("dial test ws: %v", err)
	}
	t.Cleanup(func() { c.Close() })

	select {
	case s := <-connCh:
		return s, c
	case <-time.After(3 * time.Second):
		t.Fatal("test ws server never produced a connection")
		return nil, nil
	}
}

// newSlowClientServer wires one /ws client whose send queue is already full
// and whose socket is real, so both overflow policies are observable.
func newSlowClientServer(t *testing.T) (*Server, *wsClientConn, *websocket.Conn) {
	t.Helper()
	serverSide, clientSide := newTestWSPair(t)
	s := &Server{
		ctx:       context.Background(),
		wsClients: make(map[*websocket.Conn]*wsClientConn),
	}
	client := &wsClientConn{
		conn:    serverSide,
		session: UserSession{Username: "slow-operator"},
		ch:      make(chan []byte, 1),
		done:    make(chan struct{}),
	}
	client.ch <- []byte(`{"type":"filler"}`) // full queue
	s.wsClients[serverSide] = client
	return s, client, clientSide
}

// TestReliableFanoutClosesSlowClientInsteadOfDropping pins the reliability
// contract for terminal events: a frame the client cannot rebuild from polling
// must never be discarded silently. A full queue costs that client its socket,
// so the browser reconnects and resyncs instead of showing a task as running
// forever.
func TestReliableFanoutClosesSlowClientInsteadOfDropping(t *testing.T) {
	s, client, clientSide := newSlowClientServer(t)

	s.broadcastToClientsReliable([]byte(`{"type":"task_update","status":"completed"}`))

	if got := s.wsSlowClientDrops.Load(); got != 1 {
		t.Fatalf("slow-client drops = %d, want 1", got)
	}
	// The queue kept its old contents: the frame was not queued, the socket
	// was closed instead.
	select {
	case msg := <-client.ch:
		if string(msg) != `{"type":"filler"}` {
			t.Fatalf("unexpected queued frame %q", msg)
		}
	default:
		t.Fatal("the queue was drained, want the original filler to remain")
	}

	clientSide.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, _, err := clientSide.ReadMessage(); err == nil {
		t.Fatal("the slow client's socket is still open; the reliable frame was dropped silently")
	}
}

// TestDroppableFanoutKeepsSlowClientConnected is the other half of the policy:
// an ephemeral frame (screen frame, output chunk, progress tick) may be
// dropped, and must NOT cost the operator their socket.
func TestDroppableFanoutKeepsSlowClientConnected(t *testing.T) {
	s, _, clientSide := newSlowClientServer(t)

	s.broadcastToClients([]byte(`{"type":"task_output","chunk":"...","done":false}`))

	if got := s.wsSlowClientDrops.Load(); got != 0 {
		t.Fatalf("slow-client drops = %d, want 0 for a droppable frame", got)
	}
	// A write still succeeds, which it cannot do on a closed conn.
	clientSide.SetReadDeadline(time.Now().Add(400 * time.Millisecond))
	if _, _, err := clientSide.ReadMessage(); err == nil {
		t.Fatal("expected the read to time out (no frame, socket still open)")
	} else if !strings.Contains(err.Error(), "timeout") && !strings.Contains(err.Error(), "deadline") {
		t.Fatalf("slow client socket was closed by a droppable frame: %v", err)
	}
}

// TestTenantFanoutReachesOperatorSessions pins both halves of the /ws/operator
// gap: the endpoint used to receive no agent-scoped events at all, and adding
// them without a tenant filter would disclose another tenant's hostnames and
// task types.
func TestTenantFanoutReachesOperatorSessions(t *testing.T) {
	database := newContractDB(t)
	agent := db.Implant{ID: "agent-tenant-2", Hostname: "h2", Status: "online", TenantID: 2, LastSeen: time.Now()}
	if err := database.Create(&agent).Error; err != nil {
		t.Fatalf("create agent: %v", err)
	}
	s := &Server{db: database, ctx: context.Background()}
	s.operatorSessions = &operatorSessionTracker{sessions: make(map[uint]*WSOperatorSession)}

	sameTenant := newTestOperatorSession(1, "tenant-2-op")
	sameTenant.TenantID = 2
	otherTenant := newTestOperatorSession(2, "tenant-1-op")
	otherTenant.TenantID = 1
	legacy := newTestOperatorSession(3, "legacy-op")
	legacy.TenantID = 0
	s.operatorSessions.add(sameTenant)
	s.operatorSessions.add(otherTenant)
	s.operatorSessions.add(legacy)

	s.broadcastToTenant([]byte(`{"type":"task_update","agent_id":"agent-tenant-2"}`), agent.ID)

	if got := drainSession(sameTenant); got == "" {
		t.Fatal("the agent's own tenant session received no task event over /ws/operator")
	}
	if got := drainSession(legacy); got == "" {
		t.Fatal("a legacy (tenant 0) session received no task event over /ws/operator")
	}
	if got := drainSession(otherTenant); got != "" {
		t.Fatalf("another tenant's session received %q: cross-tenant disclosure", got)
	}
}

// TestScreenshotFanoutRespectsScreenGateOnOperatorSessions keeps the
// agents.write gate on live screen frames effective on both endpoints. A
// read-only custom role could otherwise watch a desktop through /ws/operator.
func TestScreenshotFanoutRespectsScreenGateOnOperatorSessions(t *testing.T) {
	database := newContractDB(t)
	agent := db.Implant{ID: "agent-screen", Hostname: "h", Status: "online"}
	if err := database.Create(&agent).Error; err != nil {
		t.Fatalf("create agent: %v", err)
	}
	s := &Server{db: database, ctx: context.Background()}
	s.operatorSessions = &operatorSessionTracker{sessions: make(map[uint]*WSOperatorSession)}

	viewer := newTestOperatorSession(1, "viewer")
	viewer.CanViewScreen = true
	readOnly := newTestOperatorSession(2, "read-only")
	s.operatorSessions.add(viewer)
	s.operatorSessions.add(readOnly)

	s.broadcastScreenshotToTenant([]byte(`{"type":"screen_frame"}`), agent.ID)

	if got := drainSession(viewer); got == "" {
		t.Fatal("an agents.write operator received no screen frame")
	}
	if got := drainSession(readOnly); got != "" {
		t.Fatalf("a read-only operator received a screen frame: %q", got)
	}
}

// newTestOperatorSession builds a session with a live-looking send queue and no
// socket. Conn == nil is deliberate: enqueue treats it as "no socket", so a
// unit test never needs a real dial. Close behaviour is covered by the legacy
// hub tests (which use real sockets).
func newTestOperatorSession(id uint, username string) *WSOperatorSession {
	return &WSOperatorSession{
		UserID:   id,
		Username: username,
		send:     make(chan []byte, 8),
		done:     make(chan struct{}),
	}
}

func drainSession(s *WSOperatorSession) string {
	select {
	case msg := <-s.send:
		return string(msg)
	default:
		return ""
	}
}
