package server

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	forgecrypto "github.com/r0si/sovereign/internal/crypto"
	"github.com/r0si/sovereign/internal/db"
)

// missingAPICtx is an authenticated tenant-1 admin request.
func missingAPICtx(method, target, body string, params gin.Params) (*gin.Context, *httptest.ResponseRecorder) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(method, target, strings.NewReader(body))
	if body != "" {
		c.Request.Header.Set("Content-Type", "application/json")
	}
	c.Set("user", "admin")
	c.Set("user_role", db.RoleAdmin)
	c.Set("user_id", uint(1))
	c.Set("tenant_id", uint(1))
	c.Params = params
	return c, w
}

func seedMissingAPIAdmin(t *testing.T, s *Server) {
	t.Helper()
	if err := s.db.Create(&db.User{Username: "admin", Role: db.RoleAdmin, TenantID: 1, IsActive: true}).Error; err != nil {
		t.Fatalf("seed admin: %v", err)
	}
}

func TestAPIPackerEndpoints(t *testing.T) {
	s := mustTenantServer(t)
	seedMissingAPIAdmin(t, s)

	c, w := missingAPICtx(http.MethodGet, "/api/packer/templates", "", nil)
	s.handleAPIPackerTemplates(c)
	if w.Code != http.StatusOK {
		t.Fatalf("templates = %d", w.Code)
	}
	var templates struct {
		Templates []struct {
			Name string `json:"name"`
		} `json:"templates"`
	}
	unmarshalData(t, w.Body.Bytes(), &templates)
	if len(templates.Templates) == 0 {
		t.Fatal("no builtin packer templates advertised")
	}

	c, w = missingAPICtx(http.MethodGet, "/api/packer/info", "", nil)
	s.handleAPIPackerInfo(c)
	var info struct {
		EncodeTypes []string `json:"encode_types"`
		EntryPoints []string `json:"entry_points"`
		OutputTypes []string `json:"output_types"`
		CertOptions []string `json:"cert_options"`
	}
	unmarshalData(t, w.Body.Bytes(), &info)
	if len(info.EncodeTypes) == 0 || len(info.EntryPoints) == 0 || len(info.OutputTypes) == 0 {
		t.Fatalf("packer info incomplete: %+v", info)
	}
	// The endpoint advertises only implemented paths; "dll" is not one of them.
	for _, out := range info.OutputTypes {
		if out == "dll" {
			t.Fatalf("unimplemented output type advertised: %+v", info.OutputTypes)
		}
	}
}

func TestAPIPrivescTimelineChat(t *testing.T) {
	s := mustTenantServer(t)
	seedMissingAPIAdmin(t, s)
	// Sensitive task commands are encrypted at rest; without a loot key the
	// insert is refused (fail-closed), so the timeline test needs one.
	forgecrypto.InitLootEncryption(strings.Repeat("25", 32))
	seedTenantAgent(t, s, "api-agent", 1)

	// A sensitive command is encrypted at rest and must come back readable
	// through the timeline (this is why the query targets db.Task).
	secret := `net use \\dc01\c$ /user:corp\alice S3cret!`
	if err := s.db.Create(&db.Task{AgentID: "api-agent", TenantID: 1, Type: "lateral", Command: secret, Status: "completed"}).Error; err != nil {
		t.Fatal(err)
	}
	var rawAtRest string
	if err := s.db.Table("tasks").Select("command").Where("type = ?", "lateral").Scan(&rawAtRest).Error; err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(rawAtRest, "FC2ENC:") {
		t.Fatalf("sensitive command not encrypted at rest: %q", rawAtRest)
	}

	s.db.Create(&db.AuditLog{User: "admin", Action: "login", Details: "signed in", Success: true})
	s.db.Create(&db.Task{AgentID: "api-agent", TenantID: 1, Type: "privesc_check", Command: "whoami /priv", Status: "completed", Result: "SeDebugPrivilege enabled"})

	c, w := missingAPICtx(http.MethodGet, "/api/timeline", "", nil)
	s.handleAPITimelineData(c)
	if w.Code != http.StatusOK {
		t.Fatalf("timeline = %d %s", w.Code, w.Body.String())
	}
	var timeline struct {
		Events []struct {
			Type    string `json:"type"`
			Action  string `json:"action"`
			AgentID string `json:"agent_id"`
			Success bool   `json:"success"`
		} `json:"events"`
		Total int `json:"total"`
	}
	unmarshalData(t, w.Body.Bytes(), &timeline)
	if timeline.Total != len(timeline.Events) || timeline.Total < 3 {
		t.Fatalf("timeline total = %d events=%d", timeline.Total, len(timeline.Events))
	}
	kinds := map[string]int{}
	sawSecret := false
	for _, e := range timeline.Events {
		kinds[e.Type]++
		if strings.Contains(e.Action, "S3cret!") {
			sawSecret = true
		}
		if strings.Contains(e.Action, "FC2ENC:") {
			t.Fatalf("ciphertext leaked into the timeline: %s", e.Action)
		}
	}
	if kinds["audit"] != 1 || kinds["task"] < 2 {
		t.Fatalf("timeline kinds = %+v", kinds)
	}
	if !sawSecret {
		t.Fatal("decrypted command missing from the timeline")
	}

	// The privesc panel reads its own slice, decrypted the same way.
	c, w = missingAPICtx(http.MethodGet, "/api/privesc", "", nil)
	s.handleAPIPrivesc(c)
	var privesc struct {
		Results []struct {
			Command string `json:"command"`
			Result  string `json:"result"`
			Status  string `json:"status"`
		} `json:"results"`
	}
	unmarshalData(t, w.Body.Bytes(), &privesc)
	if len(privesc.Results) != 1 || privesc.Results[0].Result != "SeDebugPrivilege enabled" {
		t.Fatalf("privesc results = %+v", privesc.Results)
	}

	// Chat: engagement-scoped send and history in the general room.
	eng := db.Engagement{TenantID: 1, Name: "chat-flow", CreatedBy: "admin",
		StartsAt: time.Now().Add(-time.Hour), EndsAt: time.Now().Add(time.Hour)}
	if err := s.db.Create(&eng).Error; err != nil {
		t.Fatal(err)
	}
	if err := ensureChatChannels(s.db, 1, eng.ID, "admin"); err != nil {
		t.Fatal(err)
	}
	var channel db.ChatChannel
	if err := s.db.Where("tenant_id = ? AND engagement_id = ? AND slug = ?", 1, eng.ID, "general").First(&channel).Error; err != nil {
		t.Fatal(err)
	}
	chatPath := fmt.Sprintf("/api/chat/send?engagement_id=%d&channel_id=%d", eng.ID, channel.ID)
	sendBody := fmt.Sprintf(`{"engagement_id":%d,"channel_id":%d,"message":"hello team"}`, eng.ID, channel.ID)
	c, w = missingAPICtx(http.MethodPost, chatPath, sendBody, nil)
	s.handleAPISendChatMessage(c)
	if w.Code != http.StatusOK {
		t.Fatalf("send chat = %d %s", w.Code, w.Body.String())
	}
	c, w = missingAPICtx(http.MethodPost, chatPath, `{"message":""}`, nil)
	s.handleAPISendChatMessage(c)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("empty message = %d", w.Code)
	}
	historyPath := fmt.Sprintf("/api/chat/history?engagement_id=%d&channel_id=%d", eng.ID, channel.ID)
	c, w = missingAPICtx(http.MethodGet, historyPath, "", nil)
	s.handleAPIChatHistory(c)
	var history struct {
		Messages []struct {
			Message string `json:"message"`
			Kind    string `json:"kind"`
		} `json:"messages"`
	}
	unmarshalData(t, w.Body.Bytes(), &history)
	if len(history.Messages) != 1 || history.Messages[0].Message != "hello team" || history.Messages[0].Kind != "user" {
		t.Fatalf("history = %+v", history.Messages)
	}

	c, w = missingAPICtx(http.MethodGet, fmt.Sprintf("/api/chat/channels?engagement_id=%d", eng.ID), "", nil)
	s.handleAPIChatChannels(c)
	var channels struct {
		Channels []struct {
			Slug string `json:"slug"`
		} `json:"channels"`
	}
	unmarshalData(t, w.Body.Bytes(), &channels)
	if len(channels.Channels) != 3 {
		t.Fatalf("channels = %+v", channels.Channels)
	}
	found := false
	for _, ch := range channels.Channels {
		if ch.Slug == "general" {
			found = true
		}
	}
	if !found {
		t.Fatalf("general channel missing: %+v", channels.Channels)
	}
}

func TestAgentChainGraphAndCRUD(t *testing.T) {
	s := mustTenantServer(t)
	seedMissingAPIAdmin(t, s)
	seedTenantAgent(t, s, "chain-parent", 1)
	seedTenantAgent(t, s, "chain-child", 1)
	if err := s.db.Model(&db.Implant{}).Where("id = ?", "chain-child").Update("parent_agent_id", "chain-parent").Error; err != nil {
		t.Fatal(err)
	}

	for name, fn := range map[string]func(*gin.Context){
		"graph": s.handleAPIChainGraph,
		"list":  s.handleAPIChainList,
		"chain": s.handleAgentChainGet,
	} {
		c, w := missingAPICtx(http.MethodGet, "/api/chain", "", nil)
		fn(c)
		if w.Code != http.StatusOK {
			t.Fatalf("%s = %d %s", name, w.Code, w.Body.String())
		}
		if !strings.Contains(w.Body.String(), "chain-parent") {
			t.Fatalf("%s did not include the chain: %s", name, w.Body.String())
		}
	}

	// Set a parent through the path param (the body-only read used to 400).
	c, w := missingAPICtx(http.MethodPost, "/api/agents/x/chain", `{"parent_id":"chain-parent"}`,
		gin.Params{{Key: "id", Value: "chain-child"}})
	s.handleAgentChainSet(c)
	if w.Code != http.StatusOK {
		t.Fatalf("chain set = %d %s", w.Code, w.Body.String())
	}
	var child db.Implant
	if err := s.db.Where("id = ?", "chain-child").First(&child).Error; err != nil {
		t.Fatal(err)
	}
	if child.ParentAgentID != "chain-parent" {
		t.Fatalf("parent = %q", child.ParentAgentID)
	}

	// Self-parent and cycle are refused.
	c, w = missingAPICtx(http.MethodPost, "/api/agents/x/chain", `{"parent_id":"chain-child"}`,
		gin.Params{{Key: "id", Value: "chain-child"}})
	s.handleAgentChainSet(c)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("self-parent = %d", w.Code)
	}
	c, w = missingAPICtx(http.MethodPost, "/api/agents/x/chain", `{"parent_id":"chain-child"}`,
		gin.Params{{Key: "id", Value: "chain-parent"}})
	s.handleAgentChainSet(c)
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "cycle") {
		t.Fatalf("cycle = %d %s", w.Code, w.Body.String())
	}

	// Unknown agent and missing path id.
	c, w = missingAPICtx(http.MethodPost, "/api/agents/x/chain", `{"parent_id":"chain-parent"}`,
		gin.Params{{Key: "id", Value: "ghost"}})
	s.handleAgentChainSet(c)
	if w.Code != http.StatusNotFound {
		t.Fatalf("unknown agent = %d", w.Code)
	}

	// Clear removes the parent.
	c, w = missingAPICtx(http.MethodDelete, "/api/agents/x/chain", "",
		gin.Params{{Key: "id", Value: "chain-child"}})
	s.handleAgentChainClear(c)
	if w.Code != http.StatusOK {
		t.Fatalf("chain clear = %d %s", w.Code, w.Body.String())
	}
	if err := s.db.Where("id = ?", "chain-child").First(&child).Error; err != nil {
		t.Fatal(err)
	}
	if child.ParentAgentID != "" {
		t.Fatalf("parent survived clear: %q", child.ParentAgentID)
	}
}

func TestAPISendChatMessageTooLong(t *testing.T) {
	f := newChatTestFixtures(t)
	w := f.send(t, "op1", 1, "general", strings.Repeat("x", MaxChatMessageBytes+1))
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "too long") {
		t.Fatalf("oversize message = %d %s", w.Code, w.Body.String())
	}
	var count int64
	f.s.db.Model(&db.ChatMessage{}).Count(&count)
	if count != 0 {
		t.Fatalf("oversize message was stored: %d", count)
	}
}

// TestMissingAPISurfacesAreTenantScoped exercises the relationship-backed
// reads together: tasks/audits by tenant, mesh edges only when both endpoints
// are visible, chain implants by tenant, and chain/mesh parent writes only to
// visible agents.
func TestMissingAPISurfacesAreTenantScoped(t *testing.T) {
	forgecrypto.InitLootEncryption(tenantVisibilityMasterHex)
	s := mustTenantServer(t)
	seedMissingAPIAdmin(t, s)
	for _, id := range []string{"scope-own-a", "scope-own-b"} {
		seedTenantAgent(t, s, id, 1)
	}
	for _, id := range []string{"scope-foreign-a", "scope-foreign-b"} {
		seedTenantAgent(t, s, id, 2)
	}
	for _, task := range []db.Task{
		{AgentID: "scope-own-a", TenantID: 1, Type: "privesc_check", Command: "OWN-PRIVESC-MARKER", Status: "completed", Result: "OWN-PRIVESC-RESULT"},
		{AgentID: "scope-foreign-a", TenantID: 2, Type: "privesc_check", Command: "FOREIGN-PRIVESC-MARKER", Status: "completed", Result: "FOREIGN-PRIVESC-RESULT"},
		{AgentID: "scope-own-a", TenantID: 1, Type: "shell", Command: "OWN-TIMELINE-MARKER", Status: "completed", Result: "OWN-TIMELINE-RESULT"},
		{AgentID: "scope-foreign-a", TenantID: 2, Type: "shell", Command: "FOREIGN-TIMELINE-MARKER", Status: "completed", Result: "FOREIGN-TIMELINE-RESULT"},
	} {
		if err := s.db.Create(&task).Error; err != nil {
			t.Fatalf("seed task: %v", err)
		}
	}
	for _, audit := range []db.AuditLog{
		{User: "admin", TenantID: 1, Action: "own_audit", Details: "OWN-AUDIT-MARKER", Success: true},
		{User: "outsider", TenantID: 2, Action: "foreign_audit", Details: "FOREIGN-AUDIT-MARKER", Success: true},
		{User: "legacy", TenantID: 0, Action: "legacy_audit", Details: "LEGACY-AUDIT-MARKER", Success: true},
	} {
		if err := s.db.Create(&audit).Error; err != nil {
			t.Fatalf("seed audit: %v", err)
		}
	}

	c, w := missingAPICtx(http.MethodGet, "/api/privesc", "", nil)
	s.handleAPIPrivesc(c)
	if w.Code != http.StatusOK {
		t.Fatalf("privesc status=%d body=%s", w.Code, w.Body.String())
	}
	var privesc struct {
		Results []struct {
			AgentID string `json:"agent_id"`
			Result  string `json:"result"`
		} `json:"results"`
	}
	unmarshalData(t, w.Body.Bytes(), &privesc)
	if len(privesc.Results) != 1 || privesc.Results[0].AgentID != "scope-own-a" || privesc.Results[0].Result != "OWN-PRIVESC-RESULT" {
		t.Fatalf("tenant privesc results = %+v", privesc.Results)
	}
	if strings.Contains(w.Body.String(), "FOREIGN-PRIVESC") {
		t.Fatalf("foreign privesc task leaked: %s", w.Body.String())
	}

	c, w = missingAPICtx(http.MethodGet, "/api/timeline", "", nil)
	s.handleAPITimelineData(c)
	if w.Code != http.StatusOK {
		t.Fatalf("timeline status=%d body=%s", w.Code, w.Body.String())
	}
	for _, want := range []string{"OWN-TIMELINE-MARKER", "OWN-AUDIT-MARKER", "LEGACY-AUDIT-MARKER"} {
		if !strings.Contains(w.Body.String(), want) {
			t.Fatalf("visible timeline marker %q missing: %s", want, w.Body.String())
		}
	}
	for _, leak := range []string{"FOREIGN-TIMELINE-MARKER", "FOREIGN-AUDIT-MARKER"} {
		if strings.Contains(w.Body.String(), leak) {
			t.Fatalf("foreign timeline marker %q leaked: %s", leak, w.Body.String())
		}
	}

	// The page/export builder shares the same tenant contract and additionally
	// includes task output and agent hostnames.
	c, _ = missingAPICtx(http.MethodGet, "/timeline", "", nil)
	events, err := s.buildTimelineEvents(c, "", "", "", "", "")
	if err != nil {
		t.Fatalf("build timeline: %v", err)
	}
	var timeline strings.Builder
	for _, event := range events {
		timeline.WriteString(event.Action + "|" + event.Details + "|" + event.AgentName + "|")
	}
	if !strings.Contains(timeline.String(), "OWN-TIMELINE-RESULT") || !strings.Contains(timeline.String(), "LEGACY-AUDIT-MARKER") {
		t.Fatalf("visible shared timeline content missing: %s", timeline.String())
	}
	if strings.Contains(timeline.String(), "FOREIGN-TIMELINE") || strings.Contains(timeline.String(), "FOREIGN-AUDIT") || strings.Contains(timeline.String(), "H-scope-foreign") {
		t.Fatalf("foreign shared timeline content leaked: %s", timeline.String())
	}

	for _, peer := range []db.MeshPeer{
		{AgentID: "scope-own-a", PeerID: "scope-own-b"},
		{AgentID: "scope-own-a", PeerID: "scope-foreign-b"},
		{AgentID: "scope-foreign-a", PeerID: "scope-own-b"},
		{AgentID: "scope-foreign-a", PeerID: "scope-foreign-b"},
	} {
		if err := s.db.Create(&peer).Error; err != nil {
			t.Fatalf("seed mesh peer: %v", err)
		}
	}
	c, w = missingAPICtx(http.MethodGet, "/api/mesh/topology", "", nil)
	s.handleAPIMeshTopology(c)
	if w.Code != http.StatusOK {
		t.Fatalf("mesh topology status=%d body=%s", w.Code, w.Body.String())
	}
	var mesh struct {
		Nodes []struct {
			ID string `json:"id"`
		} `json:"nodes"`
		Edges []struct {
			From string `json:"from"`
			To   string `json:"to"`
		} `json:"edges"`
	}
	unmarshalData(t, w.Body.Bytes(), &mesh)
	if len(mesh.Nodes) != 2 || len(mesh.Edges) != 1 || mesh.Edges[0].From != "scope-own-a" || mesh.Edges[0].To != "scope-own-b" {
		t.Fatalf("mesh topology was not double-scoped: %+v", mesh)
	}

	c, _ = missingAPICtx(http.MethodGet, "/api/chain", "", nil)
	entries, err := s.buildChainEntries(c)
	if err != nil {
		t.Fatalf("build chain entries: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("chain entries = %+v, want only two visible implants", entries)
	}
	for _, entry := range entries {
		if entry["id"] == "scope-foreign-a" || entry["id"] == "scope-foreign-b" {
			t.Fatalf("foreign implant leaked into chain: %+v", entries)
		}
	}

	c, w = missingAPICtx(http.MethodPost, "/api/agents/scope-own-a/chain", `{"parent_id":"scope-foreign-a"}`,
		gin.Params{{Key: "id", Value: "scope-own-a"}})
	s.handleAgentChainSet(c)
	if w.Code != http.StatusNotFound {
		t.Fatalf("foreign chain parent status=%d body=%s", w.Code, w.Body.String())
	}
	var own db.Implant
	if err := s.db.First(&own, "id = ?", "scope-own-a").Error; err != nil {
		t.Fatal(err)
	}
	if own.ParentAgentID != "" {
		t.Fatalf("foreign chain parent landed: %q", own.ParentAgentID)
	}

	c, w = missingAPICtx(http.MethodPost, "/api/mesh/route/scope-own-a", `{"parent_id":"scope-foreign-a"}`,
		gin.Params{{Key: "agentId", Value: "scope-own-a"}})
	s.handleMeshRoute(c)
	if w.Code != http.StatusNotFound {
		t.Fatalf("foreign mesh parent status=%d body=%s", w.Code, w.Body.String())
	}
	if err := s.db.First(&own, "id = ?", "scope-own-a").Error; err != nil {
		t.Fatal(err)
	}
	if own.ParentID != "" {
		t.Fatalf("foreign mesh parent landed: %q", own.ParentID)
	}

	// A visible parent is accepted, but the returned peer list still excludes
	// relationships whose other endpoint is foreign.
	c, w = missingAPICtx(http.MethodPost, "/api/mesh/route/scope-own-a", `{"parent_id":"scope-own-b"}`,
		gin.Params{{Key: "agentId", Value: "scope-own-a"}})
	s.handleMeshRoute(c)
	if w.Code != http.StatusOK {
		t.Fatalf("visible mesh parent status=%d body=%s", w.Code, w.Body.String())
	}
	var route struct {
		Peers []db.MeshPeer `json:"peers"`
	}
	unmarshalData(t, w.Body.Bytes(), &route)
	if len(route.Peers) != 1 || route.Peers[0].AgentID != "scope-own-a" || route.Peers[0].PeerID != "scope-own-b" {
		t.Fatalf("mesh route peers = %+v", route.Peers)
	}
}

func TestPrivescQueryFailureIs500(t *testing.T) {
	s := closedDBServer(t)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/privesc", nil)
	c.Set(tenantIDContextKey, tenantResolution{tid: 1, ok: true})
	s.handleAPIPrivesc(c)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("privesc query failure status=%d body=%s", w.Code, w.Body.String())
	}
}
