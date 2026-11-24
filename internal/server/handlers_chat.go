package server

import (
	"context"
	"errors"
	"fmt"
	"github.com/gin-gonic/gin"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/r0si/sovereign/internal/db"
	"gorm.io/gorm"
)

// Operator chat is engagement-scoped: a channel belongs to exactly one
// tenant/engagement pair, and every read is checked through the same tenant
// scope as the rest of the operator API. The client never chooses the
// sender, kind or tenant — those come from the authenticated context.

const (
	chatHistoryDefaultLimit = 50
	chatHistoryMaxLimit     = 200
	chatSearchScanCap       = 2000
	chatCustomChannelMax    = 50
)

var (
	chatSlugPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)
)

// chatDB avoids passing a nil context into GORM from bare test servers.
func chatDB(s *Server) *gorm.DB {
	if s == nil || s.db == nil {
		return nil
	}
	if s.ctx == nil {
		return s.db.WithContext(context.Background())
	}
	return s.db.WithContext(s.ctx)
}

func parseChatID(raw string) (uint, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, false
	}
	n, err := strconv.ParseUint(raw, 10, 64)
	if err != nil || n == 0 || n > uint64(^uint(0)>>1) {
		return 0, false
	}
	return uint(n), true
}

// chatCaller resolves both the stable user ID (for per-user preferences) and
// the username (for creator checks and the message author). A context that
// carries only one of the two is completed from the users table; requests
// with no identity at all are refused rather than attributed to "system".
func (s *Server) chatCaller(c *gin.Context) (uint, string, error) {
	username := usernameOrEmpty(c)
	uid, ok := currentUserID(c)
	if ok && uid != 0 {
		if username == "" {
			var user db.User
			if err := chatDB(s).Select("username").Where("id = ?", uid).First(&user).Error; err == nil {
				username = user.Username
			}
		}
		return uid, username, nil
	}
	if username == "" {
		return 0, "", errors.New("no authenticated operator")
	}
	var user db.User
	if err := chatDB(s).Select("id").Where("username = ?", username).First(&user).Error; err != nil {
		return 0, username, err
	}
	return user.ID, username, nil
}

// findVisibleEngagement returns an engagement only when the caller's tenant
// scope admits it. A missing or foreign row is indistinguishable (404).
func (s *Server) findVisibleEngagement(c *gin.Context, id uint) (db.Engagement, error) {
	var engagement db.Engagement
	query := s.tenantScope(chatDB(s).Model(&db.Engagement{}), c)
	if err := query.Where("id = ?", id).First(&engagement).Error; err != nil {
		return engagement, err
	}
	return engagement, nil
}

// findVisibleChatChannel is the single owner gate for every channel-scoped
// handler (including mute/read preferences, whose model has no tenant column
// of its own). It re-checks the owning engagement so a preference cannot be
// attached to a channel whose engagement the caller may not see.
func (s *Server) findVisibleChatChannel(c *gin.Context, id uint) (db.ChatChannel, error) {
	var channel db.ChatChannel
	query := s.tenantScope(chatDB(s).Model(&db.ChatChannel{}), c)
	if err := query.Where("id = ?", id).First(&channel).Error; err != nil {
		return channel, err
	}
	if channel.EngagementID != nil && *channel.EngagementID != 0 {
		if _, err := s.findVisibleEngagement(c, *channel.EngagementID); err != nil {
			return channel, err
		}
	}
	return channel, nil
}

// respondChatLookupError keeps "row absent" (404) distinct from "read failed"
// (500): a closed/failing database must never look like an empty chat.
func respondChatLookupError(c *gin.Context, err error, entity string) {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		respondError(c, http.StatusNotFound, entity+" not found")
		return
	}
	handleQueryError(c, err, "Failed to query chat "+entity)
}

// channelBelongsTo rejects a channel ID from another engagement even when
// both engagements are visible to the same tenant.
func channelBelongsTo(channel db.ChatChannel, engagement db.Engagement) bool {
	return channel.TenantID == engagement.TenantID && channel.EngagementID != nil && *channel.EngagementID == engagement.ID
}

type chatChannelSpec struct {
	Name string
	Slug string
	Kind string
}

var chatSystemChannels = []chatChannelSpec{
	{Name: "General", Slug: "general", Kind: "general"},
	{Name: "Approvals", Slug: "approvals", Kind: "approvals"},
	{Name: "System", Slug: "system", Kind: "system"},
}

// ensureChatChannelsFor creates the three built-in rooms once. A tenant 0
// engagement of nil is the legacy scope used by the migration.
func ensureChatChannelsFor(database *gorm.DB, tenantID uint, engagementID *uint, creator string) error {
	if database == nil {
		return errors.New("no database for chat channels")
	}
	return database.Transaction(func(tx *gorm.DB) error {
		for _, spec := range chatSystemChannels {
			var channel db.ChatChannel
			query := tx.Where("tenant_id = ? AND slug = ?", tenantID, spec.Slug)
			if engagementID == nil {
				query = query.Where("engagement_id IS NULL")
			} else {
				query = query.Where("engagement_id = ?", *engagementID)
			}
			attrs := db.ChatChannel{
				TenantID:     tenantID,
				EngagementID: engagementID,
				Name:         spec.Name,
				Slug:         spec.Slug,
				Kind:         spec.Kind,
				CreatedBy:    creator,
			}
			if err := query.FirstOrCreate(&channel, attrs).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

// ensureChatChannels is called after an engagement is created (and once for
// existing engagements at migration time). It is idempotent, so a retry after
// a partial failure does not duplicate rooms.
func ensureChatChannels(database *gorm.DB, tenantID uint, engagementID uint, creator string) error {
	if engagementID == 0 {
		return errors.New("engagement id is required")
	}
	return ensureChatChannelsFor(database, tenantID, &engagementID, creator)
}

// migrateLegacyChatMessages backfills pre-engagement rows into the tenant 0
// general room. Raw SQL avoids re-running the ChatMessage encryption hook on
// rows that are already encrypted at rest.
func migrateLegacyChatMessages(database *gorm.DB) error {
	if err := ensureChatChannelsFor(database, 0, nil, "system"); err != nil {
		return err
	}
	var general db.ChatChannel
	if err := database.Where("tenant_id = 0 AND engagement_id IS NULL AND slug = ?", "general").First(&general).Error; err != nil {
		return err
	}
	return database.Exec(`UPDATE chat_messages SET tenant_id = 0, engagement_id = NULL, channel_id = ?,
		kind = CASE WHEN kind IS NULL OR kind = '' THEN 'user' ELSE kind END
		WHERE channel_id IS NULL OR channel_id = 0`, general.ID).Error
}

func findChatChannelBySlug(database *gorm.DB, tenantID, engagementID uint, slug string) (db.ChatChannel, error) {
	var channel db.ChatChannel
	query := database.Where("tenant_id = ? AND slug = ?", tenantID, slug)
	if engagementID == 0 {
		query = query.Where("engagement_id IS NULL")
	} else {
		query = query.Where("engagement_id = ?", engagementID)
	}
	err := query.First(&channel).Error
	return channel, err
}

// postSystemChat inserts a system/approval card WITHOUT broadcasting: callers
// on a Server should use postSystemChatToTenant instead. Approval cards are
// keyed by (task_id, kind) so re-delivering the same approval never stacks
// duplicate cards in the channel. The message body is never logged.
func postSystemChat(database *gorm.DB, tenantID, engagementID uint, kind, message string, taskID *uint, metadata string) error {
	if database == nil {
		return errors.New("no database for system chat")
	}
	if kind != "system" && kind != "approval" {
		return fmt.Errorf("invalid system chat kind %q", kind)
	}
	if len(message) > MaxChatMessageBytes {
		return fmt.Errorf("system chat message too long (max %d bytes)", MaxChatMessageBytes)
	}
	if kind == "approval" && taskID == nil {
		return errors.New("approval chat requires a task id")
	}
	var engagementIDValue *uint
	if engagementID != 0 {
		engagementIDValue = &engagementID
	}
	slug := "system"
	if kind == "approval" {
		slug = "approvals"
	}
	channel, err := findChatChannelBySlug(database, tenantID, engagementID, slug)
	if err != nil {
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		// Main normally calls ensureChatChannels at creation; the fallback
		// keeps system posts from failing on engagements created before chat.
		if err := ensureChatChannelsFor(database, tenantID, engagementIDValue, "system"); err != nil {
			return err
		}
		channel, err = findChatChannelBySlug(database, tenantID, engagementID, slug)
		if err != nil {
			return err
		}
	}

	if kind == "approval" && taskID != nil {
		var existing db.ChatMessage
		err := database.Where("tenant_id = ? AND channel_id = ? AND kind = ? AND task_id = ?", tenantID, channel.ID, kind, *taskID).
			First(&existing).Error
		if err == nil {
			existing.Message = message
			existing.Metadata = metadata
			// Save runs the encryption hook on the plaintext body.
			return database.Save(&existing).Error
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
	}

	entry := db.ChatMessage{
		TenantID:     tenantID,
		EngagementID: engagementIDValue,
		ChannelID:    &channel.ID,
		Username:     "system",
		Message:      message,
		Kind:         kind,
		TaskID:       taskID,
		Metadata:     metadata,
		CreatedAt:    time.Now().UTC(),
	}
	if err := database.Create(&entry).Error; err != nil {
		return err
	}
	// Ids only: the body may contain task parameters and must never enter the audit trail.
	slog.Info("chat_system_message", "kind", kind, "tenant_id", tenantID,
		"engagement_id", engagementID, "channel_id", channel.ID, "message_id", entry.ID, "task_id", taskID)
	return nil
}

// postSystemChatToTenant is the Server-facing wrapper: persist the card, then
// fan out the plaintext frame to exactly the tenant that owns the engagement.
func (s *Server) postSystemChatToTenant(tenantID, engagementID uint, kind, message string, taskID *uint, metadata string) error {
	if s == nil || s.db == nil {
		return errors.New("no database for system chat")
	}
	if err := postSystemChat(s.db, tenantID, engagementID, kind, message, taskID, metadata); err != nil {
		return err
	}
	frame := map[string]interface{}{
		"type":          "chat",
		"tenant_id":     tenantID,
		"engagement_id": engagementID,
		"channel_slug":  "system",
		"kind":          kind,
		"message":       message,
		"username":      "system",
		"created_at":    time.Now().UTC().Format(time.RFC3339),
	}
	if kind == "approval" {
		frame["channel_slug"] = "approvals"
	}
	if taskID != nil {
		frame["task_id"] = *taskID
	}
	if payload, ok := marshalJSONSafe(frame); ok {
		s.broadcastToTenantID(payload, tenantID, true)
	}
	return nil
}

func chatMessageView(message db.ChatMessage) gin.H {
	view := gin.H{
		"id":            message.ID,
		"tenant_id":     message.TenantID,
		"engagement_id": message.EngagementID,
		"channel_id":    message.ChannelID,
		"user_id":       message.UserID,
		"username":      message.Username,
		"message":       message.Message,
		"kind":          message.Kind,
		"metadata":      message.Metadata,
		"created_at":    message.CreatedAt.UTC().Format(time.RFC3339),
	}
	if message.TaskID != nil {
		view["task_id"] = *message.TaskID
	}
	return view
}

// handleAPIChatHistory returns a descending keyset page. Search filters the
// DECRYPTED messages in the service layer; the encrypted column is never
// matched with SQL LIKE. A search that needs more than the scan cap is
// refused with 420 instead of silently returning partial results.
func (s *Server) handleAPIChatHistory(c *gin.Context) {
	if s == nil || s.db == nil {
		respondError(c, http.StatusInternalServerError, "chat unavailable")
		return
	}
	engagementID, ok := parseChatID(c.Query("engagement_id"))
	if !ok {
		respondError(c, http.StatusBadRequest, "engagement_id is required")
		return
	}
	engagement, err := s.findVisibleEngagement(c, engagementID)
	if err != nil {
		respondChatLookupError(c, err, "engagement")
		return
	}
	// channel_id is optional: omitting it selects the engagement's general
	// room, which keeps older clients and query-failure probes working.
	var channel db.ChatChannel
	if raw := strings.TrimSpace(c.Query("channel_id")); raw != "" {
		channelID, ok := parseChatID(raw)
		if !ok {
			respondError(c, http.StatusBadRequest, "invalid channel_id")
			return
		}
		channel, err = s.findVisibleChatChannel(c, channelID)
		if err != nil {
			respondChatLookupError(c, err, "channel")
			return
		}
	} else {
		channel, err = findChatChannelBySlug(chatDB(s), engagement.TenantID, engagement.ID, "general")
		if err != nil {
			respondChatLookupError(c, err, "channel")
			return
		}
	}
	if !channelBelongsTo(channel, engagement) {
		respondError(c, http.StatusNotFound, "channel not found")
		return
	}

	limit := chatHistoryDefaultLimit
	if raw := strings.TrimSpace(c.Query("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > chatHistoryMaxLimit {
			respondError(c, http.StatusBadRequest, fmt.Sprintf("limit must be between 1 and %d", chatHistoryMaxLimit))
			return
		}
		limit = parsed
	}

	query := chatDB(s).Model(&db.ChatMessage{}).
		Where("tenant_id = ? AND engagement_id = ? AND channel_id = ?", engagement.TenantID, engagement.ID, channel.ID)
	if raw := strings.TrimSpace(c.Query("before_id")); raw != "" {
		beforeID, ok := parseChatID(raw)
		if !ok {
			respondError(c, http.StatusBadRequest, "invalid before_id")
			return
		}
		var before db.ChatMessage
		// Use a fresh statement for the cursor lookup: GORM mutates the
		// query chain, and keeping `id = ?` would exclude every later page.
		if err := chatDB(s).Model(&db.ChatMessage{}).
			Where("tenant_id = ? AND engagement_id = ? AND channel_id = ?", engagement.TenantID, engagement.ID, channel.ID).
			Where("id = ?", beforeID).First(&before).Error; err != nil {
			respondError(c, http.StatusBadRequest, "invalid before_id")
			return
		}
		query = query.Where("created_at < ? OR (created_at = ? AND id < ?)", before.CreatedAt, before.CreatedAt, before.ID)
	}
	query = query.Order("created_at DESC").Order("id DESC")

	search := strings.TrimSpace(c.Query("q"))
	var rows []db.ChatMessage
	var nextBeforeID uint
	if search != "" {
		if err := query.Limit(chatSearchScanCap + 1).Find(&rows).Error; err != nil {
			handleQueryError(c, err, "Failed to query chat history")
			return
		}
		if len(rows) > chatSearchScanCap {
			respondError(c, 420, "query_too_broad")
			return
		}
		needle := strings.ToLower(search)
		matches := make([]db.ChatMessage, 0, limit+1)
		for _, row := range rows {
			if strings.Contains(strings.ToLower(row.Message), needle) {
				matches = append(matches, row)
				if len(matches) > limit {
					break
				}
			}
		}
		hasMore := len(matches) > limit
		if hasMore {
			matches = matches[:limit]
			nextBeforeID = matches[len(matches)-1].ID
		}
		rows = matches
	} else {
		if err := query.Limit(limit + 1).Find(&rows).Error; err != nil {
			handleQueryError(c, err, "Failed to query chat history")
			return
		}
		if len(rows) > limit {
			rows = rows[:limit]
			nextBeforeID = rows[len(rows)-1].ID
		}
	}

	messages := make([]gin.H, 0, len(rows))
	for _, row := range rows {
		messages = append(messages, chatMessageView(row))
	}
	respond(c, gin.H{"messages": messages, "next_before_id": nextBeforeID})
}

// handleAPIChatChannels lists the engagement's rooms with per-user unread
// counts. Archived rooms stay in the list (flagged) so an operator can still
// read history; sending to them is refused.
func (s *Server) handleAPIChatChannels(c *gin.Context) {
	if s == nil || s.db == nil {
		respondError(c, http.StatusInternalServerError, "chat unavailable")
		return
	}
	engagementID, ok := parseChatID(c.Query("engagement_id"))
	if !ok {
		respondError(c, http.StatusBadRequest, "engagement_id is required")
		return
	}
	engagement, err := s.findVisibleEngagement(c, engagementID)
	if err != nil {
		respondChatLookupError(c, err, "engagement")
		return
	}
	var channels []db.ChatChannel
	if err := chatDB(s).Where("tenant_id = ? AND engagement_id = ?", engagement.TenantID, engagement.ID).
		Order("kind ASC").Order("name ASC").Find(&channels).Error; err != nil {
		handleQueryError(c, err, "Failed to query chat channels")
		return
	}
	userID, _, _ := s.chatCaller(c)
	views := make([]gin.H, 0, len(channels))
	for _, channel := range channels {
		// check:owner-scope-ok preferences are read only for channels already
		// resolved through findVisibleEngagement + tenant/engagement filters.
		var preference db.ChatChannelPreference
		err := chatDB(s).Where("user_id = ? AND channel_id = ?", userID, channel.ID).First(&preference).Error
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			handleQueryError(c, err, "Failed to query chat preferences")
			return
		}
		var unread int64
		if !preference.Muted {
			countQuery := chatDB(s).Model(&db.ChatMessage{}).
				Where("tenant_id = ? AND engagement_id = ? AND channel_id = ?", engagement.TenantID, engagement.ID, channel.ID)
			if preference.LastReadMessageID != 0 {
				countQuery = countQuery.Where("id > ?", preference.LastReadMessageID)
			}
			if err := countQuery.Count(&unread).Error; err != nil {
				handleQueryError(c, err, "Failed to count unread chat messages")
				return
			}
		}
		view := gin.H{
			"id":                   channel.ID,
			"channel":              channel.Slug,
			"name":                 channel.Name,
			"slug":                 channel.Slug,
			"kind":                 channel.Kind,
			"created_by":           channel.CreatedBy,
			"muted":                preference.Muted,
			"unread_count":         unread,
			"last_read_message_id": preference.LastReadMessageID,
			"archived":             channel.ArchivedAt != nil,
		}
		if channel.ArchivedAt != nil {
			view["archived_at"] = channel.ArchivedAt.UTC().Format(time.RFC3339)
		}
		views = append(views, view)
	}
	respond(c, gin.H{"engagement_id": engagement.ID, "channels": views})
}

// handleAPISendChatMessage keeps the legacy path but now requires explicit
// engagement/channel IDs. Only the message text is client-controlled.
func (s *Server) handleAPISendChatMessage(c *gin.Context) {
	if s == nil || s.db == nil {
		respondError(c, http.StatusInternalServerError, "chat unavailable")
		return
	}
	var req struct {
		EngagementID uint   `json:"engagement_id"`
		ChannelID    uint   `json:"channel_id"`
		Message      string `json:"message"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		respondError(c, http.StatusBadRequest, "invalid request")
		return
	}
	if req.EngagementID == 0 || req.ChannelID == 0 {
		respondError(c, http.StatusBadRequest, "engagement_id and channel_id are required")
		return
	}
	message := strings.TrimSpace(req.Message)
	if message == "" {
		respondError(c, http.StatusBadRequest, "message is required")
		return
	}
	if len(message) > MaxChatMessageBytes {
		respondError(c, http.StatusBadRequest, fmt.Sprintf("message too long (max %d bytes)", MaxChatMessageBytes))
		return
	}
	engagement, err := s.findVisibleEngagement(c, req.EngagementID)
	if err != nil {
		respondChatLookupError(c, err, "engagement")
		return
	}
	channel, err := s.findVisibleChatChannel(c, req.ChannelID)
	if err != nil {
		respondChatLookupError(c, err, "channel")
		return
	}
	if !channelBelongsTo(channel, engagement) || channel.ArchivedAt != nil {
		respondError(c, http.StatusNotFound, "channel not found")
		return
	}
	userID, username, err := s.chatCaller(c)
	if err != nil {
		respondError(c, http.StatusUnauthorized, "operator identity required")
		return
	}
	identity := username
	if identity == "" {
		identity = strconv.FormatUint(uint64(userID), 10)
	}
	if !allowChatSend(identity) {
		respondError(c, http.StatusTooManyRequests, "chat rate limit exceeded")
		return
	}

	now := time.Now().UTC()
	entry := db.ChatMessage{
		TenantID:     engagement.TenantID,
		EngagementID: &engagement.ID,
		ChannelID:    &channel.ID,
		UserID:       userID,
		Username:     username,
		Message:      message,
		Kind:         "user",
		CreatedAt:    now,
	}
	if err := chatDB(s).Create(&entry).Error; err != nil {
		respondError(c, http.StatusInternalServerError, sanitizeError(err, "Chat message"))
		return
	}
	frame := map[string]interface{}{
		"type":          "chat",
		"id":            entry.ID,
		"tenant_id":     engagement.TenantID,
		"engagement_id": engagement.ID,
		"channel_id":    channel.ID,
		"channel_slug":  channel.Slug,
		"user_id":       userID,
		"username":      username,
		"message":       message,
		"kind":          "user",
		"created_at":    now.Format(time.RFC3339),
	}
	if payload, ok := marshalJSONSafe(frame); ok {
		s.broadcastToTenantID(payload, engagement.TenantID, true)
	}
	// Ids and sizes only — never the message body.
	s.LogAuditRecord(c, "chat_send", "chat_channel", strconv.FormatUint(uint64(channel.ID), 10),
		fmt.Sprintf("engagement=%d channel=%d message=%d", engagement.ID, channel.ID, len(message)), true, nil)
	respond(c, gin.H{"id": entry.ID, "channel_id": channel.ID, "engagement_id": engagement.ID})
}

func deriveChatSlug(name string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '_', r == '-':
			if r == '-' && dash {
				continue
			}
			b.WriteRune(r)
			dash = r == '-'
		default:
			if b.Len() > 0 && !dash {
				b.WriteByte('-')
				dash = true
			}
		}
	}
	slug := strings.Trim(b.String(), "-_")
	if slug == "" {
		return "channel"
	}
	if slug[0] < 'a' || slug[0] > 'z' {
		slug = "c" + slug
	}
	if len(slug) > 64 {
		slug = slug[:64]
	}
	return strings.Trim(slug, "-_")
}

// handleAPICreateChatChannel adds a custom room. The slug is validated (or
// derived) server-side and uniqueness is checked inside the tenant/engagement.
func (s *Server) handleAPICreateChatChannel(c *gin.Context) {
	if s == nil || s.db == nil {
		respondError(c, http.StatusInternalServerError, "chat unavailable")
		return
	}
	var req struct {
		EngagementID uint   `json:"engagement_id"`
		Name         string `json:"name"`
		Slug         string `json:"slug"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		respondError(c, http.StatusBadRequest, "invalid request")
		return
	}
	if req.EngagementID == 0 {
		respondError(c, http.StatusBadRequest, "engagement_id is required")
		return
	}
	engagement, err := s.findVisibleEngagement(c, req.EngagementID)
	if err != nil {
		respondChatLookupError(c, err, "engagement")
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" || len([]rune(name)) > 64 {
		respondError(c, http.StatusBadRequest, "name is required (max 64 characters)")
		return
	}
	slug := strings.TrimSpace(req.Slug)
	if slug == "" {
		slug = deriveChatSlug(name)
	}
	if !chatSlugPattern.MatchString(slug) {
		respondError(c, http.StatusBadRequest, "slug must match ^[a-z0-9][a-z0-9_-]{0,63}$")
		return
	}
	_, username, err := s.chatCaller(c)
	if err != nil {
		respondError(c, http.StatusUnauthorized, "operator identity required")
		return
	}

	channel, status, err := createChatCustomChannel(chatDB(s), engagement, name, slug, username)
	if err != nil {
		respondError(c, status, err.Error())
		return
	}
	s.LogAuditRecord(c, "chat_channel_create", "chat_channel", strconv.FormatUint(uint64(channel.ID), 10),
		fmt.Sprintf("engagement=%d channel=%d", engagement.ID, channel.ID), true, nil)
	respond(c, gin.H{"channel": gin.H{
		"id": channel.ID, "channel": channel.Slug, "name": channel.Name,
		"slug": channel.Slug, "kind": channel.Kind, "archived": false,
	}})
}

func createChatCustomChannel(database *gorm.DB, engagement db.Engagement, name, slug, creator string) (db.ChatChannel, int, error) {
	var channel db.ChatChannel
	var count int64
	if err := database.Model(&db.ChatChannel{}).
		Where("tenant_id = ? AND engagement_id = ? AND kind = ?", engagement.TenantID, engagement.ID, "custom").
		Count(&count).Error; err != nil {
		return channel, http.StatusInternalServerError, errors.New("failed to query chat channels")
	}
	if count >= chatCustomChannelMax {
		return channel, http.StatusBadRequest, fmt.Errorf("at most %d custom channels are allowed per engagement", chatCustomChannelMax)
	}
	var existing int64
	if err := database.Model(&db.ChatChannel{}).
		Where("tenant_id = ? AND engagement_id = ? AND slug = ?", engagement.TenantID, engagement.ID, slug).
		Count(&existing).Error; err != nil {
		return channel, http.StatusInternalServerError, errors.New("failed to query chat channels")
	}
	if existing > 0 {
		return channel, http.StatusConflict, errors.New("channel slug already exists")
	}
	channel = db.ChatChannel{
		TenantID:     engagement.TenantID,
		EngagementID: &engagement.ID,
		Name:         name,
		Slug:         slug,
		Kind:         "custom",
		CreatedBy:    creator,
	}
	if err := database.Create(&channel).Error; err != nil {
		return channel, http.StatusInternalServerError, errors.New("failed to create chat channel")
	}
	return channel, http.StatusOK, nil
}

// handleAPIArchiveChatChannel is a global (not per-user) action. Only the
// engagement creator or an admin may archive a custom room; built-in rooms
// are part of the engagement record and cannot be removed.
func (s *Server) handleAPIArchiveChatChannel(c *gin.Context) {
	if s == nil || s.db == nil {
		respondError(c, http.StatusInternalServerError, "chat unavailable")
		return
	}
	channelID, ok := parseChatID(c.Param("id"))
	if !ok {
		respondError(c, http.StatusBadRequest, "invalid channel id")
		return
	}
	channel, err := s.findVisibleChatChannel(c, channelID)
	if err != nil {
		respondChatLookupError(c, err, "channel")
		return
	}
	if channel.Kind != "custom" {
		respondError(c, http.StatusBadRequest, "system channels cannot be archived")
		return
	}
	if channel.EngagementID == nil {
		respondError(c, http.StatusNotFound, "channel not found")
		return
	}
	engagement, err := s.findVisibleEngagement(c, *channel.EngagementID)
	if err != nil {
		respondChatLookupError(c, err, "engagement")
		return
	}
	_, username, err := s.chatCaller(c)
	if err != nil {
		respondError(c, http.StatusUnauthorized, "operator identity required")
		return
	}
	if !s.isAdmin(c) && username != engagement.CreatedBy {
		respondError(c, http.StatusForbidden, "only the engagement creator or an admin may archive channels")
		return
	}
	if channel.ArchivedAt != nil {
		respond(c, gin.H{"archived": true, "channel_id": channel.ID})
		return
	}
	now := time.Now().UTC()
	if err := chatDB(s).Model(&db.ChatChannel{}).Where("id = ? AND archived_at IS NULL", channel.ID).
		Updates(map[string]interface{}{"archived_at": now, "updated_at": now}).Error; err != nil {
		handleQueryError(c, err, "Failed to archive chat channel")
		return
	}
	s.LogAuditRecord(c, "chat_channel_archive", "chat_channel", strconv.FormatUint(uint64(channel.ID), 10),
		fmt.Sprintf("engagement=%d channel=%d", engagement.ID, channel.ID), true, nil)
	respond(c, gin.H{"archived": true, "channel_id": channel.ID})
}

// handleAPIMuteChatChannel updates the caller's own preference. The channel
// is re-checked through the visibility helper before touching a preference.
func (s *Server) handleAPIMuteChatChannel(c *gin.Context) {
	if s == nil || s.db == nil {
		respondError(c, http.StatusInternalServerError, "chat unavailable")
		return
	}
	channelID, ok := parseChatID(c.Param("id"))
	if !ok {
		respondError(c, http.StatusBadRequest, "invalid channel id")
		return
	}
	if _, err := s.findVisibleChatChannel(c, channelID); err != nil {
		respondChatLookupError(c, err, "channel")
		return
	}
	var req struct {
		Muted bool `json:"muted"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		respondError(c, http.StatusBadRequest, "invalid request")
		return
	}
	userID, _, err := s.chatCaller(c)
	if err != nil {
		respondError(c, http.StatusUnauthorized, "operator identity required")
		return
	}
	preference := db.ChatChannelPreference{UserID: userID, ChannelID: channelID}
	if err := chatDB(s).Where("user_id = ? AND channel_id = ?", userID, channelID).FirstOrCreate(&preference).Error; err != nil {
		handleQueryError(c, err, "Failed to save chat preference")
		return
	}
	if err := chatDB(s).Model(&db.ChatChannelPreference{}).Where("id = ?", preference.ID).
		Updates(map[string]interface{}{"muted": req.Muted, "updated_at": time.Now().UTC()}).Error; err != nil {
		handleQueryError(c, err, "Failed to save chat preference")
		return
	}
	respond(c, gin.H{"channel_id": channelID, "muted": req.Muted})
}

// handleAPIMarkChatRead advances the caller's read watermark. A stale or
// foreign message ID cannot move it backwards.
func (s *Server) handleAPIMarkChatRead(c *gin.Context) {
	if s == nil || s.db == nil {
		respondError(c, http.StatusInternalServerError, "chat unavailable")
		return
	}
	channelID, ok := parseChatID(c.Param("id"))
	if !ok {
		respondError(c, http.StatusBadRequest, "invalid channel id")
		return
	}
	channel, err := s.findVisibleChatChannel(c, channelID)
	if err != nil {
		respondChatLookupError(c, err, "channel")
		return
	}
	var req struct {
		MessageID uint `json:"message_id"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.MessageID == 0 {
		respondError(c, http.StatusBadRequest, "message_id is required")
		return
	}
	messageQuery := chatDB(s).Model(&db.ChatMessage{}).Where("id = ?", req.MessageID)
	if channel.EngagementID != nil {
		messageQuery = messageQuery.Where("tenant_id = ? AND engagement_id = ? AND channel_id = ?", channel.TenantID, *channel.EngagementID, channel.ID)
	} else {
		messageQuery = messageQuery.Where("tenant_id = ? AND channel_id = ?", channel.TenantID, channel.ID)
	}
	var exists int64
	if err := messageQuery.Count(&exists).Error; err != nil {
		handleQueryError(c, err, "Failed to query chat message")
		return
	}
	if exists == 0 {
		respondError(c, http.StatusNotFound, "message not found")
		return
	}
	userID, _, err := s.chatCaller(c)
	if err != nil {
		respondError(c, http.StatusUnauthorized, "operator identity required")
		return
	}
	preference := db.ChatChannelPreference{UserID: userID, ChannelID: channelID}
	if err := chatDB(s).Where("user_id = ? AND channel_id = ?", userID, channelID).FirstOrCreate(&preference).Error; err != nil {
		handleQueryError(c, err, "Failed to save chat preference")
		return
	}
	if err := chatDB(s).Model(&db.ChatChannelPreference{}).
		Where("id = ? AND last_read_message_id < ?", preference.ID, req.MessageID).
		Updates(map[string]interface{}{"last_read_message_id": req.MessageID, "updated_at": time.Now().UTC()}).Error; err != nil {
		handleQueryError(c, err, "Failed to save chat preference")
		return
	}
	respond(c, gin.H{"channel_id": channelID, "last_read_message_id": req.MessageID})
}
