package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	forgecrypto "github.com/r0si/sovereign/internal/crypto"
	"github.com/r0si/sovereign/internal/db"
	"github.com/r0si/sovereign/internal/server/middleware"
)

type chatChannelViewResult struct {
	ID          uint `json:"id"`
	UnreadCount int  `json:"unread_count"`
	Muted       bool `json:"muted"`
}

func chatViewByID(channels []chatChannelViewResult, id uint) chatChannelViewResult {
	for _, channel := range channels {
		if channel.ID == id {
			return channel
		}
	}
	return chatChannelViewResult{}
}

// chatTestFixtures builds two tenants, each with one engagement and its three
// built-in rooms, plus operators whose roles/tenants mirror the auth middleware.
type chatTestFixtures struct {
	s     *Server
	users map[string]db.User
	engs  map[uint]db.Engagement
	chans map[uint]map[string]db.ChatChannel
}

func newChatTestFixtures(t *testing.T) *chatTestFixtures {
	forgecrypto.InitLootEncryption(tenantVisibilityMasterHex)
	t.Helper()
	s := mustTenantServer(t)
	f := &chatTestFixtures{s: s, users: map[string]db.User{}, engs: map[uint]db.Engagement{}, chans: map[uint]map[string]db.ChatChannel{}}
	for _, u := range []db.User{
		{Username: "op1", TenantID: 1, Role: db.RoleAdmin, IsActive: true},
		{Username: "op2", TenantID: 2, Role: db.RoleAdmin, IsActive: true},
		{Username: "legacy", TenantID: 0, Role: db.RoleAdmin, IsActive: true},
		{Username: "viewer1", TenantID: 1, Role: "user", IsActive: true},
		{Username: "owner1", TenantID: 1, Role: "user", IsActive: true},
		{Username: "flood", TenantID: 1, Role: db.RoleAdmin, IsActive: true},
	} {
		if err := s.db.Create(&u).Error; err != nil {
			t.Fatalf("seed user %s: %v", u.Username, err)
		}
		f.users[u.Username] = u
	}
	now := time.Now().UTC()
	for _, e := range []db.Engagement{
		{TenantID: 1, Name: "eng-one", CreatedBy: "owner1", StartsAt: now, EndsAt: now.Add(time.Hour)},
		{TenantID: 2, Name: "eng-two", CreatedBy: "op2", StartsAt: now, EndsAt: now.Add(time.Hour)},
	} {
		if err := s.db.Create(&e).Error; err != nil {
			t.Fatalf("seed engagement: %v", err)
		}
		if err := ensureChatChannels(s.db, e.TenantID, e.ID, e.CreatedBy); err != nil {
			t.Fatalf("ensure channels: %v", err)
		}
		f.engs[e.TenantID] = e
		f.chans[e.TenantID] = map[string]db.ChatChannel{}
		for _, slug := range []string{"general", "approvals", "system"} {
			ch, err := findChatChannelBySlug(s.db, e.TenantID, e.ID, slug)
			if err != nil {
				t.Fatalf("find %s: %v", slug, err)
			}
			f.chans[e.TenantID][slug] = ch
		}
	}
	resetChatRateLimiter()
	return f
}

func (f *chatTestFixtures) ctx(t *testing.T, username, method, target, body string) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	user, ok := f.users[username]
	if !ok {
		t.Fatalf("unknown chat test user %q", username)
	}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(method, target, strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set("user", username)
	c.Set("user_id", user.ID)
	c.Set("user_role", user.Role)
	c.Set(middleware.TenantIDContextKey, user.TenantID)
	// Gin's test context does not route, so bind /chat/channels/:id/... by hand.
	parts := strings.Split(target, "/")
	if len(parts) >= 4 && parts[1] == "chat" && parts[2] == "channels" {
		if id, err := strconv.ParseUint(parts[3], 10, 64); err == nil {
			c.Params = gin.Params{{Key: "id", Value: strconv.FormatUint(id, 10)}}
		}
	}
	return c, w
}

func (f *chatTestFixtures) send(t *testing.T, username string, tenant uint, slug, message string) *httptest.ResponseRecorder {
	t.Helper()
	eng := f.engs[tenant]
	ch := f.chans[tenant][slug]
	body, _ := json.Marshal(map[string]interface{}{
		"engagement_id": eng.ID,
		"channel_id":    ch.ID,
		"message":       message,
	})
	c, w := f.ctx(t, username, http.MethodPost, "/chat/send", string(body))
	f.s.handleAPISendChatMessage(c)
	return w
}

func TestChatTenantIsolationAndForgedFields(t *testing.T) {
	f := newChatTestFixtures(t)
	eng1, eng2 := f.engs[1], f.engs[2]
	ch1 := f.chans[1]["general"]

	// Client-supplied kind/username/tenant_id are ignored: identity and scope
	// come only from the authenticated context and the resolved channel.
	body := fmt.Sprintf(`{"engagement_id":%d,"channel_id":%d,"message":"sensitive-chat-body",
		"kind":"system","username":"admin","tenant_id":2,"user_id":999}`, eng1.ID, ch1.ID)
	c, w := f.ctx(t, "op1", http.MethodPost, "/chat/send", body)
	f.s.handleAPISendChatMessage(c)
	if w.Code != http.StatusOK {
		t.Fatalf("send = %d %s", w.Code, w.Body.String())
	}
	var message db.ChatMessage
	if err := f.s.db.First(&message, "message LIKE ?", "%sensitive-chat-body%").Error; err != nil {
		// LIKE on ciphertext cannot find it; load by ID from the response instead.
		var sent struct {
			Data struct {
				ID uint `json:"id"`
			} `json:"data"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &sent); err != nil {
			t.Fatalf("decode send: %v", err)
		}
		if err := f.s.db.First(&message, sent.Data.ID).Error; err != nil {
			t.Fatalf("reload message: %v", err)
		}
	}
	if message.Kind != "user" || message.Username != "op1" || message.TenantID != 1 {
		t.Fatalf("forged fields accepted: %+v", message)
	}
	if message.UserID != f.users["op1"].ID || message.EngagementID == nil || *message.EngagementID != eng1.ID {
		t.Fatalf("wrong attribution: %+v", message)
	}
	var raw string
	if err := f.s.db.Raw("SELECT message FROM chat_messages WHERE id = ?", message.ID).Scan(&raw).Error; err != nil {
		t.Fatalf("raw message: %v", err)
	}
	if !strings.HasPrefix(raw, "FC2ENC:") {
		t.Fatalf("chat body stored in plaintext: %q", raw)
	}

	// A tenant 2 operator cannot read, send to or archive tenant 1's engagement.
	target := fmt.Sprintf("/chat/history?engagement_id=%d&channel_id=%d", eng1.ID, ch1.ID)
	c, w = f.ctx(t, "op2", http.MethodGet, target, "")
	f.s.handleAPIChatHistory(c)
	if w.Code != http.StatusNotFound {
		t.Fatalf("cross-tenant history = %d %s", w.Code, w.Body.String())
	}
	c, w = f.ctx(t, "op2", http.MethodGet, fmt.Sprintf("/chat/channels?engagement_id=%d", eng1.ID), "")
	f.s.handleAPIChatChannels(c)
	if w.Code != http.StatusNotFound {
		t.Fatalf("cross-tenant channels = %d %s", w.Code, w.Body.String())
	}
	if w := f.send(t, "op2", 1, "general", "intrusion"); w.Code != http.StatusNotFound {
		t.Fatalf("cross-tenant send = %d %s", w.Code, w.Body.String())
	}
	// Nor can a tenant 1 operator see tenant 2's room.
	target = fmt.Sprintf("/chat/history?engagement_id=%d&channel_id=%d", eng2.ID, f.chans[2]["general"].ID)
	c, w = f.ctx(t, "op1", http.MethodGet, target, "")
	f.s.handleAPIChatHistory(c)
	if w.Code != http.StatusNotFound {
		t.Fatalf("foreign engagement history = %d %s", w.Code, w.Body.String())
	}
	// Mixing a visible channel with a different visible engagement is refused.
	target = fmt.Sprintf("/chat/history?engagement_id=%d&channel_id=%d", eng1.ID, f.chans[1]["system"].ID)
	c, w = f.ctx(t, "op1", http.MethodGet, target, "")
	f.s.handleAPIChatHistory(c)
	if w.Code != http.StatusOK {
		t.Fatalf("same-engagement system channel = %d %s", w.Code, w.Body.String())
	}
}

func TestChatHistoryUnreadRateLimitAndAudit(t *testing.T) {
	f := newChatTestFixtures(t)
	eng := f.engs[1]
	for _, msg := range []string{"alpha first", "beta second", "gamma third"} {
		if w := f.send(t, "op1", 1, "general", msg); w.Code != http.StatusOK {
			t.Fatalf("send %q = %d %s", msg, w.Code, w.Body.String())
		}
	}
	history := func(query string) ([]struct {
		ID      uint   `json:"id"`
		Message string `json:"message"`
		Kind    string `json:"kind"`
	}, int) {
		c, w := f.ctx(t, "viewer1", http.MethodGet, "/chat/history?"+query, "")
		f.s.handleAPIChatHistory(c)
		if w.Code != http.StatusOK {
			t.Fatalf("history %q = %d %s", query, w.Code, w.Body.String())
		}
		var response struct {
			Messages []struct {
				ID      uint   `json:"id"`
				Message string `json:"message"`
				Kind    string `json:"kind"`
			} `json:"messages"`
			NextBeforeID uint `json:"next_before_id"`
		}
		unmarshalData(t, w.Body.Bytes(), &response)
		return response.Messages, int(response.NextBeforeID)
	}
	// A client that only names the engagement gets the general room.
	generalPage, _ := history(fmt.Sprintf("engagement_id=%d", eng.ID))
	if len(generalPage) != 3 {
		t.Fatalf("default-channel history = %+v", generalPage)
	}

	base := fmt.Sprintf("engagement_id=%d&channel_id=%d", eng.ID, f.chans[1]["general"].ID)
	page, next := history(base + "&limit=2")
	if len(page) != 2 || next == 0 {
		t.Fatalf("first page = %+v next=%d", page, next)
	}
	if page[0].Message != "gamma third" || page[1].Message != "beta second" {
		t.Fatalf("keyset order = %+v", page)
	}
	second, _ := history(fmt.Sprintf("%s&limit=2&before_id=%d", base, next))
	if len(second) != 1 || second[0].Message != "alpha first" {
		t.Fatalf("second page = %+v", second)
	}
	found, _ := history(base + "&q=GAMMA")
	if len(found) != 1 || found[0].Message != "gamma third" {
		t.Fatalf("search = %+v", found)
	}
	c, w := f.ctx(t, "viewer1", http.MethodGet, "/chat/history?"+base+"&limit=201", "")
	f.s.handleAPIChatHistory(c)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("oversized limit = %d", w.Code)
	}

	channelView := func() []chatChannelViewResult {
		c, w := f.ctx(t, "viewer1", http.MethodGet, fmt.Sprintf("/chat/channels?engagement_id=%d", eng.ID), "")
		f.s.handleAPIChatChannels(c)
		if w.Code != http.StatusOK {
			t.Fatalf("channels = %d %s", w.Code, w.Body.String())
		}
		var response struct {
			Channels []chatChannelViewResult `json:"channels"`
		}
		unmarshalData(t, w.Body.Bytes(), &response)
		return response.Channels
	}
	channels := channelView()
	chID := f.chans[1]["general"].ID
	if len(channels) != 3 || chatViewByID(channels, chID).UnreadCount != 3 {
		t.Fatalf("unread channels = %+v", channels)
	}
	body := fmt.Sprintf(`{"message_id":%d}`, second[0].ID)
	c, w = f.ctx(t, "viewer1", http.MethodPost, fmt.Sprintf("/chat/channels/%d/read", chID), body)
	f.s.handleAPIMarkChatRead(c)
	if w.Code != http.StatusOK {
		t.Fatalf("read = %d %s", w.Code, w.Body.String())
	}
	channels = channelView()
	if chatViewByID(channels, chID).UnreadCount != 2 {
		t.Fatalf("unread after read = %+v", channels)
	}
	c, w = f.ctx(t, "viewer1", http.MethodPost, fmt.Sprintf("/chat/channels/%d/mute", chID), `{"muted":true}`)
	f.s.handleAPIMuteChatChannel(c)
	if w.Code != http.StatusOK {
		t.Fatalf("mute = %d %s", w.Code, w.Body.String())
	}
	channels = channelView()
	if general := chatViewByID(channels, chID); !general.Muted || general.UnreadCount != 0 {
		t.Fatalf("muted channel = %+v", channels)
	}

	// Ten sends per ten seconds are allowed; the eleventh is refused.
	resetChatRateLimiter()
	for i := range 10 {
		if w := f.send(t, "flood", 1, "approvals", fmt.Sprintf("flood-%d", i)); w.Code != http.StatusOK {
			t.Fatalf("send %d = %d %s", i, w.Code, w.Body.String())
		}
	}
	if w := f.send(t, "flood", 1, "approvals", "flood-11"); w.Code != http.StatusTooManyRequests {
		t.Fatalf("rate-limited send = %d %s", w.Code, w.Body.String())
	}

	var audits []db.AuditLog
	if err := f.s.db.Where("action = ?", "chat_send").Find(&audits).Error; err != nil {
		t.Fatalf("audit query: %v", err)
	}
	if len(audits) == 0 {
		t.Fatal("expected chat_send audit records")
	}
	for _, entry := range audits {
		if strings.Contains(entry.Details, "sensitive-chat-body") || strings.Contains(entry.Details, "gamma third") ||
			strings.Contains(entry.Error, "gamma third") {
			t.Fatalf("audit contains message body: %+v", entry)
		}
	}
}

func TestChatArchivePermissionsAndCustomChannels(t *testing.T) {
	f := newChatTestFixtures(t)
	eng := f.engs[1]
	create := func(username, name, slug string) *httptest.ResponseRecorder {
		body := fmt.Sprintf(`{"engagement_id":%d,"name":%q,"slug":%q}`, eng.ID, name, slug)
		c, w := f.ctx(t, username, http.MethodPost, "/chat/channels", body)
		f.s.handleAPICreateChatChannel(c)
		return w
	}
	if w := create("op1", "  War Room  ", "war-room"); w.Code != http.StatusOK {
		t.Fatalf("create = %d %s", w.Code, w.Body.String())
	}
	var channel db.ChatChannel
	if err := f.s.db.Where("tenant_id = ? AND engagement_id = ? AND slug = ?", 1, eng.ID, "war-room").First(&channel).Error; err != nil {
		t.Fatalf("reload channel: %v", err)
	}
	if channel.Name != "War Room" || channel.Kind != "custom" || channel.CreatedBy != "op1" {
		t.Fatalf("stored channel = %+v", channel)
	}
	for _, tc := range []struct{ name, slug string }{
		{"", "valid"}, {"Bad Slug", "Upper"}, {"ok", "x!"},
	} {
		if w := create("op1", tc.name, tc.slug); w.Code != http.StatusBadRequest {
			t.Fatalf("invalid channel %q/%q = %d", tc.name, tc.slug, w.Code)
		}
	}
	if w := create("op1", "duplicate", "war-room"); w.Code != http.StatusConflict {
		t.Fatalf("duplicate slug = %d %s", w.Code, w.Body.String())
	}
	if w := create("op1", "derived name", ""); w.Code != http.StatusOK {
		t.Fatalf("derived slug = %d %s", w.Code, w.Body.String())
	}

	archive := func(username string, id uint) *httptest.ResponseRecorder {
		c, w := f.ctx(t, username, http.MethodPost, fmt.Sprintf("/chat/channels/%d/archive", id), "")
		c.Params = gin.Params{{Key: "id", Value: fmt.Sprint(id)}}
		f.s.handleAPIArchiveChatChannel(c)
		return w
	}
	if w := archive("viewer1", channel.ID); w.Code != http.StatusForbidden {
		t.Fatalf("non-owner archive = %d %s", w.Code, w.Body.String())
	}
	if w := archive("owner1", channel.ID); w.Code != http.StatusOK {
		t.Fatalf("creator archive = %d %s", w.Code, w.Body.String())
	}
	if err := f.s.db.First(&channel, channel.ID).Error; err != nil || channel.ArchivedAt == nil {
		t.Fatalf("archive not persisted: %+v %v", channel, err)
	}
	if w := archive("legacy", f.chans[1]["system"].ID); w.Code != http.StatusBadRequest {
		t.Fatalf("system archive = %d %s", w.Code, w.Body.String())
	}

	// The fifty-custom-room cap is per engagement.
	for i := range chatCustomChannelMax {
		f.s.db.Create(&db.ChatChannel{TenantID: 1, EngagementID: &eng.ID, Name: fmt.Sprintf("room-%d", i),
			Slug: fmt.Sprintf("room-%d", i), Kind: "custom", CreatedBy: "op1"})
	}
	if w := create("op1", "too many", "too-many"); w.Code != http.StatusBadRequest {
		t.Fatalf("51st channel = %d %s", w.Code, w.Body.String())
	}
}

func TestApprovalChatUpsertAndTenantFanout(t *testing.T) {
	f := newChatTestFixtures(t)
	eng := f.engs[1]
	taskID := uint(77)
	summary := "task 77 | cred_check | host-1"
	metadata := `{"task_id":77,"type":"cred_check"}`
	if _, err := postSystemChat(f.s.db, 1, eng.ID, "approval", summary, &taskID, metadata); err != nil {
		t.Fatalf("post approval: %v", err)
	}
	if _, err := postSystemChat(f.s.db, 1, eng.ID, "approval", "changed-"+summary, &taskID, `{"task_id":77,"status":"approved"}`); err != nil {
		t.Fatalf("repost approval: %v", err)
	}
	var approvals []db.ChatMessage
	if err := f.s.db.Where("kind = ? AND task_id = ?", "approval", taskID).Find(&approvals).Error; err != nil {
		t.Fatalf("query approvals: %v", err)
	}
	if len(approvals) != 1 {
		t.Fatalf("approval cards = %d, want 1", len(approvals))
	}
	if approvals[0].Message != "changed-"+summary || !strings.Contains(approvals[0].Metadata, "approved") {
		t.Fatalf("approval not refreshed: %+v", approvals[0])
	}
	ch, err := findChatChannelBySlug(f.s.db, 1, eng.ID, "approvals")
	if err != nil || approvals[0].ChannelID == nil || *approvals[0].ChannelID != ch.ID {
		t.Fatalf("approval channel = %+v (%v), want %d", approvals[0].ChannelID, err, ch.ID)
	}
	var raw string
	if err := f.s.db.Raw("SELECT message FROM chat_messages WHERE id = ?", approvals[0].ID).Scan(&raw).Error; err != nil {
		t.Fatalf("raw approval: %v", err)
	}
	if !strings.HasPrefix(raw, "FC2ENC:") {
		t.Fatalf("approval body stored in plaintext: %q", raw)
	}

	// A chat frame must reach only legacy and the owning tenant, both on /ws.
	clients := map[*websocket.Conn]*wsClientConn{}
	queues := map[uint]chan []byte{}
	for _, tid := range []uint{0, 1, 2} {
		conn := &websocket.Conn{}
		queues[tid] = make(chan []byte, 4)
		clients[conn] = &wsClientConn{conn: conn, tenantID: tid, ch: queues[tid], done: make(chan struct{})}
	}
	f.s.wsMutex.Lock()
	f.s.wsClients = clients
	f.s.wsMutex.Unlock()
	if err := f.s.postSystemChatToTenant(1, eng.ID, "system", "engagement window warning", nil, `{}`); err != nil {
		t.Fatalf("broadcast system chat: %v", err)
	}
	for _, tid := range []uint{0, 1} {
		select {
		case frame := <-queues[tid]:
			var decoded map[string]interface{}
			if err := json.Unmarshal(frame, &decoded); err != nil {
				t.Fatalf("frame decode: %v", err)
			}
			if decoded["type"] != "chat" || decoded["tenant_id"] != float64(1) ||
				decoded["engagement_id"] != float64(eng.ID) || decoded["channel_slug"] != "system" ||
				decoded["id"] == nil || decoded["channel_id"] == nil || decoded["metadata"] == nil {
				t.Fatalf("unexpected frame: %s", frame)
			}
			if !strings.Contains(fmt.Sprint(decoded["message"]), "engagement window warning") {
				t.Fatalf("frame carries ciphertext or wrong body: %s", frame)
			}
		case <-time.After(time.Second):
			t.Fatalf("tenant %d did not receive chat frame", tid)
		}
	}
	select {
	case frame := <-queues[2]:
		t.Fatalf("foreign tenant received chat frame: %s", frame)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestChatLegacyBackfillIsIdempotent(t *testing.T) {
	s := mustTenantServer(t)
	database := s.db
	if err := database.Exec(`INSERT INTO chat_messages (username, message, kind, created_at) VALUES (?, ?, ?, ?)`,
		"legacy", "FC2ENC:old", "user", time.Now().UTC()).Error; err != nil {
		t.Fatalf("seed legacy row: %v", err)
	}
	if err := migrateLegacyChatMessages(database); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if err := migrateLegacyChatMessages(database); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
	var rows []db.ChatMessage
	if err := database.Where("username = ?", "legacy").Find(&rows).Error; err != nil {
		t.Fatalf("query backfilled rows: %v", err)
	}
	if len(rows) != 1 || rows[0].ChannelID == nil || rows[0].TenantID != 0 {
		t.Fatalf("backfill = %+v", rows)
	}
	var raw string
	if err := database.Raw("SELECT message FROM chat_messages WHERE id = ?", rows[0].ID).Scan(&raw).Error; err != nil {
		t.Fatalf("raw: %v", err)
	}
	if raw != "FC2ENC:old" {
		t.Fatalf("migration re-encrypted stored message: %q", raw)
	}
	var general int64
	if err := database.Model(&db.ChatChannel{}).Where("tenant_id = 0 AND engagement_id IS NULL AND slug = ?", "general").Count(&general).Error; err != nil || general != 1 {
		t.Fatalf("legacy general channels = %d (%v)", general, err)
	}
}

// Guard the central owner helper used by mute/read: a preference cannot be
// created through a channel ID from another tenant.
func TestChatPreferenceRejectsForeignChannel(t *testing.T) {
	f := newChatTestFixtures(t)
	foreign := f.chans[2]["general"].ID
	c, w := f.ctx(t, "op1", http.MethodPost, fmt.Sprintf("/chat/channels/%d/mute", foreign), `{"muted":true}`)
	c.Params = gin.Params{{Key: "id", Value: fmt.Sprint(foreign)}}
	f.s.handleAPIMuteChatChannel(c)
	if w.Code != http.StatusNotFound {
		t.Fatalf("foreign mute = %d %s", w.Code, w.Body.String())
	}
	var prefs int64
	if err := f.s.db.Model(&db.ChatChannelPreference{}).Count(&prefs).Error; err != nil || prefs != 0 {
		t.Fatalf("foreign preference created: %d (%v)", prefs, err)
	}
}

func TestChatQueryFailureIsNotAnEmptyList(t *testing.T) {
	s := closedDBServer(t)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/chat/history?engagement_id=1", nil)
	c.Set(tenantIDContextKey, tenantResolution{tid: 1, ok: true})
	s.handleAPIChatHistory(c)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("closed-DB history = %d %s, want 500", w.Code, w.Body.String())
	}

	w = httptest.NewRecorder()
	c, _ = gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/chat/channels?engagement_id=1", nil)
	c.Set(tenantIDContextKey, tenantResolution{tid: 1, ok: true})
	s.handleAPIChatChannels(c)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("closed-DB channels = %d %s, want 500", w.Code, w.Body.String())
	}
}
