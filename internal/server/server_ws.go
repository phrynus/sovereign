package server

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/r0si/sovereign/internal/db"
	"github.com/r0si/sovereign/internal/server/middleware"
)

// ── Operator WebSocket hub + broadcast fan-out ────────────────────────────

// handleWebSocket handles WebSocket connections for real-time notifications
// Auth is validated inside the handler (from cookie or query token) so this
// endpoint does NOT need to be behind the AuthRequired middleware.
func (s *Server) handleWebSocket(c *gin.Context) {
	origin := c.GetHeader("Origin")
	if origin != "" && !s.wsUpgrader.CheckOrigin(c.Request) {
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"success": false, "error": "origin not allowed"})
		return
	}

	claims, tokenStr, ok := s.authenticateOperatorWS(c)
	if !ok {
		return
	}
	username := claims.Username
	tenantID, tenantOK := s.tenantIDForUserStrict(username)
	if !tenantOK {
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"success": false, "error": "tenant_unavailable"})
		return
	}

	conn, err := s.wsUpgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		slog.Error("Failed to upgrade WebSocket", "err", err)
		return
	}

	conn.SetReadLimit(WSMaxMessageSize)
	conn.SetReadDeadline(time.Now().Add(WSReadDeadline))
	conn.SetPongHandler(func(string) error {
		conn.SetReadDeadline(time.Now().Add(WSReadDeadline))
		return nil
	})

	canViewTasks := s.userCanViewTasks(username)
	session := UserSession{Username: username, ConnectedAt: time.Now()}
	client := &wsClientConn{
		conn:          conn,
		session:       session,
		tenantID:      tenantID,
		canViewScreen: s.userCanViewScreen(username),
		canViewTasks:  canViewTasks,
		ch:            make(chan []byte, wsWriteChanSize),
		done:          make(chan struct{}),
	}

	s.wsMutex.Lock()
	if len(s.wsClients) >= MaxWSConnections {
		s.wsMutex.Unlock()
		slog.Warn("WebSocket connection limit reached", "current", len(s.wsClients), "limit", MaxWSConnections)
		conn.Close()
		return
	}
	s.wsClients[conn] = client
	s.wsMutex.Unlock()

	// Re-check force-logout/disable/revoke while the socket is live.
	s.startWSAuthRecheck(tokenStr, claims, client.done, client.canViewTasks, func() {
		conn.Close()
	})

	// Writer goroutine: drains the buffered channel and writes to the socket.
	// Tracked by the shutdown WaitGroup: hijacked WS conns are invisible to
	// http.Server.Shutdown, so without wg tracking the DB could be closed
	// while these pumps were still running.
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer func() {
			close(client.done)
		}()
		ticker := time.NewTicker(WSPingInterval)
		defer ticker.Stop()
		for {
			select {
			case <-s.ctx.Done():
				return
			case msg, ok := <-client.ch:
				if !ok {
					return
				}
				conn.SetWriteDeadline(time.Now().Add(WSWriteDeadline))
				if err := conn.WriteMessage(websocket.TextMessage, msg); err != nil {
					slog.Debug("Failed to send WebSocket message", "user", username, "err", err)
					// Close so the blocked reader exits immediately instead of
					// lingering for the full 60s read deadline while holding a
					// MaxWSConnections slot.
					conn.Close()
					return
				}
			case <-ticker.C:
				conn.SetWriteDeadline(time.Now().Add(WSWriteDeadline))
				if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
					conn.Close()
					return
				}
			}
		}
	}()

	slog.Info("WebSocket client connected", "user", username)
	// Reconnect sync: push the current build snapshot so a dashboard that was
	// offline (reconnect) or connected mid-build converges instantly. Routed
	// through the writer goroutine, never written from the reader.
	builds := s.buildJobSnapshotsForTenant(tenantID)
	if msg, ok := marshalJSONSafe(gin.H{"type": "sync", "builds": builds}); ok {
		select {
		case client.ch <- msg:
		default:
		}
	}
	s.broadcastUserEvent("user_online", username, session)

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer func() {
			if r := recover(); r != nil {
				slog.Error("WebSocket reader panicked", "user", username, "recover", r)
			}
			s.wsMutex.Lock()
			delete(s.wsClients, conn)
			s.wsMutex.Unlock()
			close(client.ch)
			s.broadcastUserEvent("user_offline", username, session)
			conn.Close()
			slog.Info("WebSocket client disconnected", "user", username)
		}()

		for {
			conn.SetReadDeadline(time.Now().Add(WSReadDeadline))
			_, data, err := conn.ReadMessage()
			if err != nil {
				return
			}
			// Answer application-level heartbeats. The frontend tracks liveness
			// with {type:"ping"}/{type:"pong"} application messages; without a
			// pong it closes the socket every HEARTBEAT_TIMEOUT and reconnects.
			// Route the reply through the writer goroutine: gorilla/websocket
			// allows only one concurrent writer, and the reader goroutine must
			// never WriteMessage on the same conn.
			var msg struct {
				Type string `json:"type"`
			}
			if len(data) > 0 && data[0] == '{' && json.Unmarshal(data, &msg) == nil && msg.Type == "ping" {
				select {
				case client.ch <- []byte(`{"type":"pong"}`):
				default:
					// Writer queue full — drop the pong rather than block the
					// read loop; the frontend will reconnect on heartbeat timeout.
				}
			}
		}
	}()
}

// UserSession holds metadata about a connected operator WebSocket session.
type UserSession struct {
	Username    string    `json:"username"`
	ConnectedAt time.Time `json:"connected_at"`
}

// wsClientConn wraps a WebSocket connection with a buffered write channel
// so that broadcastToClients never blocks on slow readers.
type wsClientConn struct {
	conn    *websocket.Conn
	session UserSession
	// tenantID scopes agent-bound broadcasts: 0 (legacy operators) receives
	// everything, matching tenantScope semantics.
	tenantID uint
	// canViewScreen mirrors the operator gate on the screenshot APIs
	// (agents.write): live screen frames are pushed over this same socket, so
	// a read-only custom role must not receive them just by connecting /ws.
	canViewScreen bool
	// canViewTasks mirrors the tasks.read gate on task HTTP APIs. Task
	// updates and output frames must never reach a subscriber without it.
	canViewTasks bool
	ch           chan []byte
	done         chan struct{}
}

const wsWriteChanSize = 64

// getOnlineUsers returns the list of currently connected operator sessions.
func (s *Server) getOnlineUsers() []UserSession {
	s.wsMutex.RLock()
	defer s.wsMutex.RUnlock()
	users := make([]UserSession, 0, len(s.wsClients))
	seen := make(map[string]bool)
	for _, client := range s.wsClients {
		if !seen[client.session.Username] {
			seen[client.session.Username] = true
			users = append(users, client.session)
		}
	}
	return users
}

// broadcastUserEvent sends a user online/offline event to all WebSocket clients.
func (s *Server) broadcastUserEvent(eventType, username string, session UserSession) {
	msg, ok := marshalJSONSafe(map[string]interface{}{
		"type":         eventType,
		"username":     username,
		"connected_at": session.ConnectedAt,
		"online_users": s.getOnlineUsers(),
	})
	if !ok {
		return
	}
	s.broadcastToClients(msg)
}

// wsSubscriber is the visibility-relevant identity of one connected operator
// socket, resolved once at connect time. BOTH operator endpoints (/ws and
// /ws/operator) expose it, so the fan-out applies ONE set of tenant/screen
// rules instead of two that can drift apart.
type wsSubscriber struct {
	// TenantID 0 is the legacy operator: it sees every tenant, matching
	// tenantScope semantics.
	TenantID uint
	// CanViewScreen mirrors the screenshot APIs' agents.write gate.
	CanViewScreen bool
	// CanViewTasks mirrors the tasks.read gate on task HTTP APIs.
	CanViewTasks bool
}

func (c *wsClientConn) subscriber() wsSubscriber {
	return wsSubscriber{TenantID: c.tenantID, CanViewScreen: c.canViewScreen, CanViewTasks: c.canViewTasks}
}

func (s *WSOperatorSession) subscriber() wsSubscriber {
	return wsSubscriber{TenantID: s.TenantID, CanViewScreen: s.CanViewScreen, CanViewTasks: s.CanViewTasks}
}

// broadcastToClients sends a message to all connected WebSocket clients.
// Uses buffered channels so the caller never blocks on slow readers.
func (s *Server) broadcastToClients(message []byte) {
	s.broadcastFanout(message, false, nil)
}

// broadcastToClientsReliable sends a frame the client CANNOT rebuild from
// polling (a terminal task state, an agent going offline, an alert). If a
// client's queue is full the socket is closed instead of the frame being
// dropped, so the browser reconnects and resyncs rather than merging events
// around a hole it will never notice.
func (s *Server) broadcastToClientsReliable(message []byte) {
	s.broadcastFanout(message, true, nil)
}

// userCanViewScreen reports whether a username may receive live screen frames
// on the dashboard socket, computed once at connect time. It mirrors the
// operator gate + agents.write permission of the screenshot endpoints
// (RoleHasPermissionDB covers custom roles too); lookup errors fail closed.
func (s *Server) userCanViewScreen(username string) bool {
	var user db.User
	if err := s.db.Select("role").Where("username = ?", username).First(&user).Error; err != nil {
		return false
	}
	// Same operator gate as the HTTP side (unknown roles are refused there
	// too) plus the agents.write permission.
	if !s.roleGrantsOperator(user.Role) {
		return false
	}
	return db.RoleHasPermissionDB(s.db, user.Role, db.PermAgentsWrite)
}

// userCanViewTasks reports whether a username may receive task frames on an
// operator socket. It uses the same operator-role gate and current DB-backed
// permission resolution as the HTTP APIs; lookup failures fail closed.
func (s *Server) userCanViewTasks(username string) bool {
	var user db.User
	if err := s.db.Select("role").Where("username = ?", username).First(&user).Error; err != nil {
		return false
	}
	if !s.roleGrantsOperator(user.Role) {
		return false
	}
	allowed, err := middleware.UserHasPermission(s.db, username, db.PermTasksRead)
	return err == nil && allowed
}

// broadcastToTenant sends an agent-scoped message only to clients allowed to
// see the agent: legacy (tenant 0) operators plus the agent's own tenant.
// Previously every agent/task event fanned out to all connected operators,
// disclosing other tenants' hostnames, IDs and task types over the socket.
func (s *Server) broadcastToTenant(message []byte, agentID string) {
	s.broadcastToTenantWhere(message, agentID, false, nil)
}

// broadcastToTenantReliable is broadcastToTenant for a frame the client cannot
// recover from polling. A client whose queue is full is disconnected (it
// resyncs on reconnect) instead of silently missing a terminal state.
func (s *Server) broadcastToTenantReliable(message []byte, agentID string) {
	s.broadcastToTenantWhere(message, agentID, true, nil)
}

// broadcastScreenshotToTenant delivers live screen frames only to clients
// that could fetch the same frame over HTTP (agents.write + operator role).
// The tenant fan-out alone was not enough: a read-only custom role with
// agents.read could watch a desktop through /ws even though the screenshot
// endpoints require the write permission.
func (s *Server) broadcastScreenshotToTenant(message []byte, agentID string) {
	s.broadcastToTenantWhere(message, agentID, false, func(sub wsSubscriber) bool {
		return sub.CanViewScreen
	})
}

// broadcastToTenantWhere is broadcastToTenant with a reliability flag and an
// extra per-subscriber filter (nil keeps every subscriber in the resolved
// tenant set).
func (s *Server) broadcastToTenantWhere(message []byte, agentID string, reliable bool, allow func(wsSubscriber) bool) {
	var agent db.Implant
	if err := s.db.Select("tenant_id").Where("id = ?", agentID).First(&agent).Error; err != nil {
		// Agent row gone (or DB hiccup): limit to legacy clients rather than
		// dropping (availability) or fanning out (disclosure).
		slog.Warn("Tenant broadcast: agent lookup failed, limiting to legacy clients", "agent_id", agentID, "error", err)
		s.broadcastFanout(message, reliable, func(sub wsSubscriber) bool {
			return sub.TenantID == 0 && (allow == nil || allow(sub))
		})
		return
	}
	if agent.TenantID == 0 {
		s.broadcastFanout(message, reliable, allow)
		return
	}
	tid := agent.TenantID
	s.broadcastFanout(message, reliable, func(sub wsSubscriber) bool {
		if sub.TenantID != 0 && sub.TenantID != tid {
			return false
		}
		return allow == nil || allow(sub)
	})
}

// shutdownDone is the server's shutdown signal, nil when no context is
// installed (tests that build a bare Server). A nil channel blocks forever,
// which is exactly "this process never shuts down" for a select.
func (s *Server) shutdownDone() <-chan struct{} {
	if s == nil || s.ctx == nil {
		return nil
	}
	return s.ctx.Done()
}

// broadcastFanout delivers one frame to every operator socket that the filter
// admits, across BOTH endpoints (/ws and /ws/operator) and both tenants rules.
//
// reliable selects the overflow policy. A droppable frame (live screen frame,
// output chunk, progress tick) is discarded when a client's queue is full: the
// next one supersedes it. A reliable frame is not: the client is disconnected
// so it reconnects and resyncs, because the state it carries (task finished,
// agent offline) may never be produced again and the UI would spin forever.
func (s *Server) broadcastFanout(message []byte, reliable bool, allow func(wsSubscriber) bool) {
	s.wsMutex.RLock()
	clients := make([]*wsClientConn, 0, len(s.wsClients))
	for _, client := range s.wsClients {
		if allow == nil || allow(client.subscriber()) {
			clients = append(clients, client)
		}
	}
	s.wsMutex.RUnlock()

	for _, client := range clients {
		s.deliverToLegacyClient(client, message, reliable)
	}

	if s.operatorSessions != nil {
		for _, session := range s.operatorSessions.snapshot() {
			if allow == nil || allow(session.subscriber()) {
				s.deliverToOperatorSession(session, message, reliable)
			}
		}
	}
}

// deliverToLegacyClient non-blockingly queues one frame for a /ws client.
func (s *Server) deliverToLegacyClient(client *wsClientConn, message []byte, reliable bool) {
	defer func() {
		if r := recover(); r != nil {
			// The reader goroutine closed the channel between the snapshot
			// and this send; the client is already gone.
			slog.Debug("Operator broadcast to closed channel", "user", client.session.Username)
		}
	}()
	select {
	case <-s.shutdownDone():
		return
	case client.ch <- message:
	default:
		if !reliable {
			slog.Debug("WebSocket write channel full, dropping message", "user", client.session.Username)
			return
		}
		s.wsSlowClientDrops.Add(1)
		slog.Warn("WebSocket client cannot keep up; closing so it resyncs on reconnect",
			"user", client.session.Username)
		client.conn.Close()
	}
}

// deliverToOperatorSession non-blockingly queues one frame for a /ws/operator
// session, with the same overflow policy as the legacy hub.
func (s *Server) deliverToOperatorSession(session *WSOperatorSession, message []byte, reliable bool) {
	if _, shed := session.enqueue(message, reliable); shed {
		s.wsSlowClientDrops.Add(1)
	}
}

// broadcastAgentOnline pushes agent online events to all WebSocket clients.
func (s *Server) broadcastAgentOnline(agent db.Implant, isNew bool) {
	if !isNew && !s.suppressAgentStatusEvent(agent.ID) {
		return
	}
	payload := map[string]interface{}{
		"type":     "agent_online",
		"agent_id": agent.ID,
		"hostname": agent.Hostname,
		"username": agent.Username,
		"ip":       agent.IP,
		"new":      isNew,
	}
	notification, err := json.Marshal(payload)
	if err != nil {
		slog.Error("Failed to marshal agent online notification", "err", err)
		return
	}
	// Reliable: a lost "agent online" leaves the fleet view showing the agent
	// as offline until something else forces a refetch.
	s.broadcastToTenantReliable(notification, agent.ID)
}

// broadcastAgentOffline pushes agent offline events to all WebSocket clients.
// status is the new server-side status (the call sites set it in the same
// pass): "stale" on a live-connection drop, "offline" when the reaper gives
// up — clients that merge the event into badges can show the exact state
// instead of assuming the worst.
func (s *Server) broadcastAgentOffline(agent db.Implant, status string) {
	if !s.suppressAgentStatusEvent(agent.ID) {
		return
	}
	payload := map[string]string{
		"type":     "agent_offline",
		"agent_id": agent.ID,
		"hostname": agent.Hostname,
		"ip":       agent.IP,
		"status":   status,
	}
	notification, err := json.Marshal(payload)
	if err != nil {
		slog.Error("Failed to marshal agent offline notification", "err", err)
		return
	}
	// Reliable: a lost "agent offline" leaves a ghost entry in the fleet view
	// (green badge for a host that is gone) until something forces a refetch.
	s.broadcastToTenantReliable(notification, agent.ID)
}

// suppressAgentStatusEvent ensures at most one status event per agent every 60 seconds.
// Returns true if the event should proceed, false if it should be suppressed.
func (s *Server) suppressAgentStatusEvent(agentID string) bool {
	s.agentStatusCooldownMu.Lock()
	defer s.agentStatusCooldownMu.Unlock()
	last, ok := s.agentStatusCooldown[agentID]
	now := time.Now()
	if ok && now.Sub(last) < AgentStatusCooldown {
		return false
	}
	s.agentStatusCooldown[agentID] = now
	return true
}

func (s *Server) handleWSBeaconDisconnect(agentID string) {
	slog.Info("WebSocket beacon disconnected", "agent_id", agentID)
	var agent db.Implant
	if err := s.db.Where("id = ?", agentID).First(&agent).Error; err != nil {
		return
	}
	if agent.Status != "offline" {
		if err := s.db.Model(&agent).Update("status", "stale").Error; err != nil {
			slog.Error("Failed to mark agent stale", "agent_id", agentID, "error", err)
		}
		s.recordAgentStatusEvent(agentID, "stale")
		if s.suppressAgentStatusEvent(agentID) {
			s.broadcastAgentOffline(agent, "stale")
		}
	}
}

// broadcastAgentDataUpdate pushes agent data changes to all WebSocket clients.
func (s *Server) broadcastAgentDataUpdate(agentID string, data map[string]interface{}) {
	payload := map[string]interface{}{
		"type":     "agent_data_update",
		"agent_id": agentID,
		"data":     data,
	}
	notification, err := json.Marshal(payload)
	if err != nil {
		slog.Error("Failed to marshal agent data update", "err", err)
		return
	}
	s.broadcastToTenant(notification, agentID)
}

// broadcastShellOutput delivers unsolicited interactive-shell bytes. These
// frames have no task row to poll, so they are tenant/task-permission scoped
// and reliable: a full queue reconnects the client instead of silently
// punching a hole in the terminal stream.
func (s *Server) broadcastShellOutput(agentID, shellID, data, encoding string) {
	payload := map[string]interface{}{
		"type": "shell_output", "agent_id": agentID, "shell_id": shellID,
		"data": data, "encoding": encoding,
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		slog.Error("Failed to marshal shell output", "agent_id", agentID, "error", err)
		return
	}
	s.broadcastToTenantWhere(raw, agentID, true, func(sub wsSubscriber) bool {
		return sub.CanViewTasks
	})
}

// broadcastTaskUpdate pushes task status (and result if completed) to WS clients
func (s *Server) broadcastTaskUpdate(agentID string, task db.Task) {
	task = taskForAuthorizedOperator(task)
	payload := map[string]interface{}{
		"type":       "task_update",
		"agent_id":   agentID,
		"task_id":    task.ID,
		"task_type":  task.Type,
		"status":     task.Status,
		"command":    task.Command,
		"created_by": task.CreatedBy,
	}
	if task.Result != "" && task.Type == "shell" && task.Status == "completed" {
		if len(task.Result) > TaskOutputStreamThreshold {
			// Large shell output is streamed in ordered frames. Do not attach a
			// second truncated value that can be mistaken for the full result.
			s.broadcastTaskOutputFrames(agentID, task)
			payload["result_complete"] = false
		} else {
			// Small interactive-shell results fit comfortably in one WS frame.
			// Mark this value explicitly so clients can distinguish it from the
			// previews used by task list notifications.
			payload["result"] = task.Result
			payload["result_complete"] = true
		}
	} else if task.Result != "" {
		payload["result"] = truncateString(task.Result, 200)
		payload["result_complete"] = false
	}
	if task.Error != "" {
		payload["error"] = task.Error
	}
	if task.LateResult {
		payload["late_result"] = true
		payload["late_result_status"] = task.LateResultStatus
		if task.LateResultAt != nil {
			payload["late_result_at"] = task.LateResultAt.UTC().Format(time.RFC3339)
		}
		if task.Result != "" {
			payload["result"] = truncateString(task.Result, 200)
			payload["result_complete"] = false
		}
	}
	notification, err := json.Marshal(payload)
	if err != nil {
		slog.Error("Failed to marshal task update", "err", err)
		return
	}
	// Reliable: this frame is the only signal that a task finished. Dropping it
	// leaves the operator's spinner (and the live shell stream) waiting forever,
	// so a full client queue costs that client its socket instead.
	s.broadcastToTenantWhere(notification, agentID, true, func(sub wsSubscriber) bool {
		return sub.CanViewTasks
	})
}

// TaskOutputStreamThreshold: results above this size are chunked into
// task_output frames instead of a single task_update payload.
const TaskOutputStreamThreshold = 4 * 1024

// TaskOutputFrameSize: target size per streamed frame.
const TaskOutputFrameSize = 16 * 1024

// broadcastTaskOutputFrames pushes a completed shell result as ordered
// "task_output" frames, each ending on a line boundary when possible. The
// final frame carries done:true so clients know the stream is complete.
func (s *Server) broadcastTaskOutputFrames(agentID string, task db.Task) {
	result := task.Result
	for start := 0; start < len(result); {
		end := start + TaskOutputFrameSize
		if end >= len(result) {
			end = len(result)
		} else {
			// Prefer a chunk boundary that does not split a line or a
			// multi-byte UTF-8 rune.
			for i := end - 1; i > start; i-- {
				if result[i] == '\n' {
					end = i + 1
					break
				}
			}
			if end <= start {
				end = start + TaskOutputFrameSize
			}
		}
		frame := map[string]interface{}{
			"type":     "task_output",
			"agent_id": agentID,
			"task_id":  task.ID,
			"chunk":    result[start:end],
			"done":     end >= len(result),
		}
		raw, err := json.Marshal(frame)
		if err != nil {
			slog.Error("Failed to marshal task output frame", "err", err)
			return
		}
		s.broadcastToTenantWhere(raw, agentID, false, func(sub wsSubscriber) bool {
			return sub.CanViewTasks
		})
		start = end
	}
}

func truncateString(s string, max int) string {
	if len(s) > max {
		return s[:max] + "..."
	}
	return s
}

func (s *Server) pushBulkResult(r BulkResult) {
	s.bulkHistoryMu.Lock()
	defer s.bulkHistoryMu.Unlock()
	if len(s.bulkHistory) >= maxBulkHistory {
		s.bulkHistory = s.bulkHistory[1:]
	}
	r.ID = len(s.bulkHistory) + 1
	s.bulkHistory = append(s.bulkHistory, r)
}
