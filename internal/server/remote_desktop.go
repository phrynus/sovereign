package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

// ── Remote input validation + rate limiting ────────────────────────────────

// remoteInputRequest is the wire shape of one operator input event
// (POST /api/agents/:id/input and the remote-desktop websocket / WebRTC
// signaling relay).
//
// The type carries the action, and the extra fields only parameterise it:
//
//	move          x,y
//	click         x,y [count] [mods]        left button
//	click_right   x,y [count] [mods]
//	click_middle  x,y [count] [mods]
//	wheel         x,y delta                 delta in notches, +away from user
//	key           key [mods]
//
// Right/middle clicks and the wheel are SEPARATE types on purpose. An older
// agent build rejects an unknown type (the operator sees a failed task), while
// it would silently ignore a `button` field and left-click instead - a wrong
// action on the target is worse than a refusal.
type remoteInputRequest struct {
	Type  string   `json:"type"`
	X     int      `json:"x"`
	Y     int      `json:"y"`
	Key   string   `json:"key,omitempty"`
	Count int      `json:"count,omitempty"`
	Delta int      `json:"delta,omitempty"`
	Mods  []string `json:"mods,omitempty"`
}

// coordsInRange reports whether x/y are inside the accepted coordinate range.
func (r *remoteInputRequest) coordsInRange() bool {
	return r.X >= -MaxRemoteInputCoord && r.X <= MaxRemoteInputCoord &&
		r.Y >= -MaxRemoteInputCoord && r.Y <= MaxRemoteInputCoord
}

// normalizeMods canonicalises the modifier list (lowercase, deduplicated,
// allowlisted). Modifiers are held down around the key/click, so an unknown
// name would have to be guessed on the agent - refuse it at the boundary.
func (r *remoteInputRequest) normalizeMods() error {
	if len(r.Mods) == 0 {
		r.Mods = nil
		return nil
	}
	if len(r.Mods) > MaxRemoteInputMods {
		return fmt.Errorf("at most %d modifiers are allowed", MaxRemoteInputMods)
	}
	seen := make(map[string]bool, len(r.Mods))
	out := make([]string, 0, len(r.Mods))
	for _, mod := range r.Mods {
		mod = strings.ToLower(strings.TrimSpace(mod))
		switch mod {
		case "ctrl", "alt", "shift", "meta":
		default:
			return errors.New("mods must be ctrl, alt, shift or meta")
		}
		if seen[mod] {
			continue
		}
		seen[mod] = true
		out = append(out, mod)
	}
	r.Mods = out
	return nil
}

// normalize canonicalizes and bounds one input event. The agent-side
// dispatcher understands the same types and injects coordinates straight into
// the OS, so the API boundary is the last line that can reject garbage
// (unknown types used to be queued as tasks that could only fail on the agent).
func (r *remoteInputRequest) normalize() error {
	r.Type = strings.ToLower(strings.TrimSpace(r.Type))
	switch r.Type {
	case "move":
		if !r.coordsInRange() {
			return fmt.Errorf("x/y must be between -%d and %d", MaxRemoteInputCoord, MaxRemoteInputCoord)
		}
		r.Key, r.Count, r.Delta, r.Mods = "", 0, 0, nil
	case "click", "click_right", "click_middle":
		if !r.coordsInRange() {
			return fmt.Errorf("x/y must be between -%d and %d", MaxRemoteInputCoord, MaxRemoteInputCoord)
		}
		if r.Count == 0 {
			r.Count = 1
		}
		if r.Count < 1 || r.Count > MaxRemoteInputClickCount {
			return fmt.Errorf("count must be 1-%d", MaxRemoteInputClickCount)
		}
		if err := r.normalizeMods(); err != nil {
			return err
		}
		r.Key, r.Delta = "", 0
	case "wheel":
		if !r.coordsInRange() {
			return fmt.Errorf("x/y must be between -%d and %d", MaxRemoteInputCoord, MaxRemoteInputCoord)
		}
		if r.Delta == 0 || r.Delta < -MaxRemoteInputWheelDelta || r.Delta > MaxRemoteInputWheelDelta {
			return fmt.Errorf("delta must be -%d..%d and not zero", MaxRemoteInputWheelDelta, MaxRemoteInputWheelDelta)
		}
		r.Key, r.Count, r.Mods = "", 0, nil
	case "key":
		r.Key = strings.ToLower(strings.TrimSpace(r.Key))
		if r.Key == "" || len(r.Key) > MaxRemoteInputKeyLen {
			return fmt.Errorf("key must be 1-%d characters", MaxRemoteInputKeyLen)
		}
		if err := r.normalizeMods(); err != nil {
			return err
		}
		r.X, r.Y, r.Count, r.Delta = 0, 0, 0, 0
	default:
		return errors.New("type must be move, click, click_right, click_middle, wheel, or key")
	}
	return nil
}

// remoteInputRequestFromMap decodes one websocket input event. The event type
// comes from the envelope; coordinates/key live in the data map as untyped
// JSON (numbers decode to float64).
func remoteInputRequestFromMap(evType string, data map[string]interface{}) (remoteInputRequest, error) {
	if data == nil {
		return remoteInputRequest{}, errors.New("data is required")
	}
	req := remoteInputRequest{Type: evType}
	readInt := func(key string) (int, bool, error) {
		v, ok := data[key]
		if !ok {
			return 0, false, nil
		}
		switch n := v.(type) {
		case float64:
			if n != float64(int(n)) {
				return 0, true, fmt.Errorf("%s must be an integer", key)
			}
			return int(n), true, nil
		case int:
			return n, true, nil
		default:
			return 0, true, fmt.Errorf("%s must be a number", key)
		}
	}
	var err error
	if req.X, _, err = readInt("x"); err != nil {
		return req, err
	}
	if req.Y, _, err = readInt("y"); err != nil {
		return req, err
	}
	if req.Count, _, err = readInt("count"); err != nil {
		return req, err
	}
	if req.Delta, _, err = readInt("delta"); err != nil {
		return req, err
	}
	if v, ok := data["key"].(string); ok {
		req.Key = v
	}
	if raw, ok := data["mods"]; ok && raw != nil {
		list, ok := raw.([]interface{})
		if !ok {
			return req, errors.New("mods must be an array")
		}
		if len(list) > MaxRemoteInputMods {
			return req, fmt.Errorf("at most %d modifiers are allowed", MaxRemoteInputMods)
		}
		mods := make([]string, 0, len(list))
		for _, item := range list {
			s, ok := item.(string)
			if !ok {
				return req, errors.New("mods must be strings")
			}
			mods = append(mods, s)
		}
		req.Mods = mods
	}
	return req, nil
}

// fixedWindowLimiter is a small keyed fixed-window counter used to throttle
// remote desktop input events per (operator, agent): every accepted event
// inserts a task row plus an audit row, so an unbounded stream is a
// write-amplification primitive, not just a UI annoyance.
type fixedWindowLimiter struct {
	mu    sync.Mutex
	hits  map[string]int
	start map[string]time.Time
}

// allow reports whether key may proceed at now: at most limit events per
// window. The window resets lazily on the first event past its start.
func (l *fixedWindowLimiter) allow(key string, limit int, window time.Duration, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.hits == nil {
		l.hits = make(map[string]int)
		l.start = make(map[string]time.Time)
	}
	since, ok := l.start[key]
	if !ok || now.Sub(since) >= window {
		l.start[key] = now
		l.hits[key] = 1
		return true
	}
	if l.hits[key] >= limit {
		return false
	}
	l.hits[key]++
	return true
}

// sweep drops windows that started before cutoff (called from the stale-map
// cleanup ticker so attacker-chosen keys cannot pin memory forever).
func (l *fixedWindowLimiter) sweep(cutoff time.Time) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	removed := 0
	for k, since := range l.start {
		if since.Before(cutoff) {
			delete(l.start, k)
			delete(l.hits, k)
			removed++
		}
	}
	return removed
}

// allowRemoteInput consumes one event from the budget for its class. Cursor
// moves are cheap and plentiful; everything else changes state on the target
// and keeps the smaller action budget. Splitting them means a drag cannot
// starve the keystrokes typed during it (the single counter used to drop them
// silently, with no UI feedback).
func (s *Server) allowRemoteInput(operatorID, agentID, inputType string) bool {
	key := operatorID + ":" + strings.ToLower(agentID)
	limit := MaxRemoteInputEventsPerWindow
	if inputType == "move" {
		limit = MaxRemotePointerEventsPerWindow
		key += ":move"
	}
	return s.remoteInputLimiter.allow(key, limit, RemoteInputWindow, time.Now())
}

// ── Remote desktop session hub ─────────────────────────────────────────────

// rdOperator is one operator connection in a remote desktop session. The
// per-connection write mutex serializes writers (keepalive ping ticker and
// frame broadcasts): gorilla/websocket allows only one concurrent writer.
type rdOperator struct {
	conn    *websocket.Conn
	writeMu sync.Mutex
}

// RDSession represents a remote desktop session for an agent.
type RDSession struct {
	AgentID   string
	Operators map[string]*rdOperator
	mu        sync.RWMutex
}

// RDHub manages all remote desktop sessions.
type RDHub struct {
	sessions map[string]*RDSession
	// operatorCount tracks live operator connections for the global cap.
	// Guarded by mu.
	operatorCount int
	mu            sync.RWMutex
}

var rdHub = &RDHub{sessions: make(map[string]*RDSession)}

// errRDHubFull is returned when either the per-agent or the global remote
// desktop connection cap is reached.
var errRDHubFull = errors.New("remote desktop connection limit reached")

// Join adds an operator connection. Re-joining with an operatorID that is
// already present replaces (and returns) the previous connection so the
// caller can close it — two tabs from one operator must not leave an orphan
// reader behind. A nil replaced conn means a fresh registration.
func (h *RDHub) Join(agentID, operatorID string, conn *websocket.Conn) (*rdOperator, *websocket.Conn, error) {
	if conn == nil || operatorID == "" || agentID == "" {
		return nil, nil, errors.New("agent id, operator id and connection are required")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	session, exists := h.sessions[agentID]
	if !exists {
		session = &RDSession{AgentID: agentID, Operators: make(map[string]*rdOperator)}
		h.sessions[agentID] = session
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	existing, hasExisting := session.Operators[operatorID]
	if !hasExisting {
		if len(session.Operators) >= MaxRDOperatorsPerAgent || h.operatorCount >= MaxRDOperatorConnections {
			if !exists {
				delete(h.sessions, agentID)
			}
			return nil, nil, errRDHubFull
		}
		h.operatorCount++
	}
	op := &rdOperator{conn: conn}
	session.Operators[operatorID] = op
	if hasExisting {
		return op, existing.conn, nil
	}
	return op, nil, nil
}

// Leave removes an operator connection. It only deletes the registration when
// conn is still the live connection for that operator: a replaced
// connection's deferred Leave must not evict its successor.
func (h *RDHub) Leave(agentID, operatorID string, conn *websocket.Conn) {
	h.mu.Lock()
	defer h.mu.Unlock()
	session, exists := h.sessions[agentID]
	if !exists {
		return
	}
	session.mu.Lock()
	op, ok := session.Operators[operatorID]
	if ok && (conn == nil || op.conn == conn) {
		delete(session.Operators, operatorID)
		if h.operatorCount > 0 {
			h.operatorCount--
		}
	}
	empty := len(session.Operators) == 0
	session.mu.Unlock()
	if empty {
		delete(h.sessions, agentID)
	}
}

// OperatorCount returns the number of live operator connections.
func (h *RDHub) OperatorCount() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.operatorCount
}

// SessionOperatorCount returns the number of operators viewing one agent.
func (h *RDHub) SessionOperatorCount(agentID string) int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	session, exists := h.sessions[agentID]
	if !exists {
		return 0
	}
	session.mu.RLock()
	defer session.mu.RUnlock()
	return len(session.Operators)
}

// HasOperator reports whether operatorID already holds a slot for the agent
// (a reconnect/replacement must not be rejected by the caps).
func (h *RDHub) HasOperator(agentID, operatorID string) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	session, exists := h.sessions[agentID]
	if !exists {
		return false
	}
	session.mu.RLock()
	defer session.mu.RUnlock()
	_, ok := session.Operators[operatorID]
	return ok
}

// SendInput sends an input event to the agent via the beacon WebSocket.
func (h *RDHub) SendInput(s *Server, agentID string, inputType string, data map[string]interface{}) {
	if beacon := s.wsHub.Get(agentID); beacon != nil {
		payload := map[string]interface{}{
			"type":       "remote_input",
			"input_type": inputType,
			"data":       data,
		}
		if payloadJSON, err := json.Marshal(payload); err == nil {
			func() {
				defer func() {
					if r := recover(); r != nil {
						slog.Warn("Remote desktop send to closed channel", "agent_id", agentID)
					}
				}()
				select {
				case beacon.Send <- payloadJSON:
				default:
				}
			}()
		}
	}
}

// handleRDWebSocket handles operator WebSocket connections for remote desktop.
// handleAgentControl exposes the per-agent exclusive-control lease. GET reads
// who holds it; POST takes it explicitly (audited) or releases your own.
func (s *Server) handleAgentControl(c *gin.Context) {
	if !s.requireOperator(c) {
		return
	}
	id := c.Param("id")
	if _, ok := s.getAgentOrFail(c, id); !ok {
		return
	}
	uid, ok := currentUserID(c)
	if !ok {
		respondError(c, http.StatusUnauthorized, "unauthorized")
		return
	}
	username := c.GetString("user")

	if c.Request.Method == http.MethodPost {
		var req struct {
			Action string `json:"action"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			respondError(c, http.StatusBadRequest, "invalid json: expected {action}")
			return
		}
		switch strings.ToLower(strings.TrimSpace(req.Action)) {
		case "take":
			prev, _ := s.controlHolderFor(id)
			holder, _ := s.claimControl(id, uid, username, true)
			if prev.OperatorID != 0 && prev.OperatorID != uid {
				s.LogAuditRecord(c, "remote_control_take", "agent", id,
					fmt.Sprintf("%s took control from %s", holder.Username, prev.Username), true, nil)
			}
			respond(c, gin.H{"holder": holder.Username, "you": true, "since": holder.Since.UTC()})
		case "release":
			s.releaseControl(id, uid)
			s.LogAuditRecord(c, "remote_control_release", "agent", id, "control released", true, nil)
			respond(c, gin.H{"holder": "", "you": false})
		default:
			respondError(c, http.StatusBadRequest, "action must be take or release")
		}
		return
	}

	holder, held := s.controlHolderFor(id)
	payload := gin.H{"held": held, "you": false}
	if held {
		payload["holder"] = holder.Username
		payload["since"] = holder.Since.UTC()
		payload["expires_in_seconds"] = int(ControlLeaseTTL.Seconds() - time.Since(holder.Renewed).Seconds())
		payload["you"] = holder.OperatorID == uid
	}
	respond(c, payload)
}

func (s *Server) handleRDWebSocket(c *gin.Context) {
	// Remote desktop streams screen content and relays operator input to
	// agents, so viewer-role users must not access it.
	if !s.requireOperator(c) {
		return
	}

	agentID := c.Query("agent_id")
	if agentID == "" {
		respondError(c, http.StatusBadRequest, "agent_id required")
		return
	}
	// Tenant scoping before upgrade: a cross-tenant operator must not get
	// a live frame stream (or be able to inject input) for an invisible agent.
	if _, ok := s.getAgentOrFail(c, agentID); !ok {
		return
	}
	// operatorID comes from the authenticated session, never from a query
	// string — otherwise one operator can overwrite another's map entry
	// (steal frames / drop their connection) by spoofing user_id.
	uid, ok := currentUserID(c)
	if !ok {
		respondError(c, http.StatusUnauthorized, "unauthorized")
		return
	}
	operatorID := strconv.FormatUint(uint64(uid), 10)

	// Connection caps, checked before the upgrade so the client gets a real
	// HTTP status instead of an immediately-closed socket. A replacement for
	// an existing slot is exempt (net-zero connection count).
	if !rdHub.HasOperator(agentID, operatorID) {
		if rdHub.SessionOperatorCount(agentID) >= MaxRDOperatorsPerAgent {
			respondError(c, http.StatusTooManyRequests, "too many remote desktop viewers for this agent")
			return
		}
		if rdHub.OperatorCount() >= MaxRDOperatorConnections {
			respondError(c, http.StatusTooManyRequests, "remote desktop connection limit reached")
			return
		}
	}

	// Origin check matches the dashboard socket: browsers attach session
	// cookies to cross-site WebSocket handshakes, so an unchecked upgrade is
	// CSRF-able (the previous shared agent upgrader accepted any browser).
	if origin := c.GetHeader("Origin"); origin != "" && !s.wsUpgrader.CheckOrigin(c.Request) {
		respondError(c, http.StatusForbidden, "origin not allowed")
		return
	}

	conn, err := s.wsUpgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		slog.Error("RD WebSocket upgrade failed", "error", err)
		return
	}

	op, replaced, err := rdHub.Join(agentID, operatorID, conn)
	if err != nil {
		slog.Warn("RD WebSocket join refused", "agent_id", agentID, "user", operatorID, "error", err)
		conn.Close()
		return
	}
	if replaced != nil {
		// Same operator opened a second stream: keep the newest connection
		// (it carries the live cursor) and drop the stale socket, whose
		// reader would otherwise hang for the full read deadline.
		replaced.Close()
	}
	defer rdHub.Leave(agentID, operatorID, conn)
	s.LogAuditRecord(c, "remote_desktop_open", "agent", agentID, "Remote desktop stream opened", true, nil)
	defer s.LogAuditRecord(c, "remote_desktop_close", "agent", agentID, "Remote desktop stream closed", true, nil)

	conn.SetReadLimit(WSMaxMessageSize)
	conn.SetReadDeadline(time.Now().Add(RemoteDesktopReadDeadline))
	conn.SetPongHandler(func(string) error {
		conn.SetReadDeadline(time.Now().Add(RemoteDesktopReadDeadline))
		return nil
	})

	// Server-side keepalive: the read deadline only extends on pongs, so
	// without pings every session died after five minutes of operator
	// silence even while the operator was watching.
	pingDone := make(chan struct{})
	defer close(pingDone)
	ctx := s.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		ticker := time.NewTicker(RemoteDesktopPingInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-pingDone:
				return
			case <-ticker.C:
				op.writeMu.Lock()
				_ = conn.SetWriteDeadline(time.Now().Add(RemoteDesktopWriteDeadline))
				pingErr := conn.WriteMessage(websocket.PingMessage, nil)
				op.writeMu.Unlock()
				if pingErr != nil {
					conn.Close()
					return
				}
			}
		}
	}()

	// Listen for input events from the operator. Every event is normalized,
	// bounded, and rate-limited per connection before it reaches the beacon.
	var winStart time.Time
	var winCount int
	for {
		_, message, err := conn.ReadMessage()
		if err != nil {
			break
		}

		var inputEvent struct {
			Type string                 `json:"type"`
			Data map[string]interface{} `json:"data"`
		}
		if err := json.Unmarshal(message, &inputEvent); err != nil {
			continue
		}
		req, err := remoteInputRequestFromMap(inputEvent.Type, inputEvent.Data)
		if err != nil {
			continue
		}
		if err := req.normalize(); err != nil {
			continue
		}
		now := time.Now()
		if now.Sub(winStart) >= RemoteInputWindow {
			winStart = now
			winCount = 0
		}
		if winCount >= MaxRemoteInputEventsPerWindow {
			continue
		}
		winCount++
		rdHub.SendInput(s, agentID, req.Type, map[string]interface{}{
			"x": req.X, "y": req.Y, "key": req.Key,
		})
	}
}

// handleRDAPIScreenshot triggers a screenshot request for an agent.
func (s *Server) handleRDAPIScreenshot(c *gin.Context) {
	if !s.requireOperator(c) {
		return
	}
	id := c.Param("id")
	// Tenant scoping before issuing a task against the agent.
	if _, ok := s.getAgentOrFail(c, id); !ok {
		return
	}
	task := s.issueAgentTask(c, id, TaskSpec{Type: "screenshot", Command: "screenshot"})
	if task == nil {
		return
	}

	s.LogAuditRecord(c, "screenshot_request", "agent", id, "Remote desktop screenshot requested", true, nil)
	s.broadcastTaskUpdate(id, *task)
	respond(c, gin.H{"task_id": task.ID, "message": "screenshot requested"})
}
