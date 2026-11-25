package server

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	forgecrypto "github.com/r0si/sovereign/internal/crypto"
	"github.com/r0si/sovereign/internal/db"
)

// collectorCtx mirrors the operator context the browser-credential handlers
// run under: seeded user (so tenant scope resolves), post-form body.
func collectorCtx(s *Server, t *testing.T, username, body string) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	forgecrypto.InitLootEncryption(tenantVisibilityMasterHex)
	c, w := tenantScopedAdminContext(s, t, username, 1)
	c.Request = httptest.NewRequest(http.MethodPost, "/", bytes.NewReader([]byte(body)))
	c.Request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	c.Set("user_role", "operator")
	var user db.User
	if err := s.db.Where("username = ?", username).First(&user).Error; err != nil {
		t.Fatalf("seed caller: %v", err)
	}
	c.Set("user_id", user.ID)
	return c, w
}

// TestBrowserCollectorsCrossTenant404 proves all five collectors refuse
// another tenant's agent. handleWifiCreds used to bypass issueAgentTask with
// a bare Create, so it accepted any agent id; this locks the regression.
func TestBrowserCollectorsCrossTenant404(t *testing.T) {
	cases := []struct {
		name   string
		invoke func(s *Server, c *gin.Context)
	}{
		{"cookie_export", func(s *Server, c *gin.Context) { s.handleCookieExport(c) }},
		{"sccm_recon", func(s *Server, c *gin.Context) { s.handleSccmRecon(c) }},
		{"entra_prt", func(s *Server, c *gin.Context) { s.handleEntraPRT(c) }},
		{"vpn_creds", func(s *Server, c *gin.Context) { s.handleVpnCreds(c) }},
		{"wifi_creds", func(s *Server, c *gin.Context) { s.handleWifiCreds(c) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := mustTenantServer(t)
			seedTenantAgent(t, s, "bc-x", 2)

			c, w := collectorCtx(s, t, "bc-viewer", "")
			c.Params = gin.Params{{Key: "id", Value: "bc-x"}}
			tc.invoke(s, c)

			if w.Code != http.StatusNotFound {
				t.Fatalf("status=%d body=%s, want 404 for another tenant's agent", w.Code, w.Body.String())
			}
			assertNoTasksForAgent(t, s, "bc-x")
		})
	}
}

// TestBrowserCollectorsSameTenantDispatch proves each collector queues the
// right task type against an own-tenant agent (cookie_export carries the
// browser selector, defaulting to "all").
func TestBrowserCollectorsSameTenantDispatch(t *testing.T) {
	cases := []struct {
		name    string
		invoke  func(s *Server, c *gin.Context)
		wantTyp string
	}{
		{"cookie_export", func(s *Server, c *gin.Context) { s.handleCookieExport(c) }, "cookie_export"},
		{"sccm_recon", func(s *Server, c *gin.Context) { s.handleSccmRecon(c) }, "sccm_recon"},
		{"entra_prt", func(s *Server, c *gin.Context) { s.handleEntraPRT(c) }, "entra_prt"},
		{"vpn_creds", func(s *Server, c *gin.Context) { s.handleVpnCreds(c) }, "vpn_creds"},
		{"wifi_creds", func(s *Server, c *gin.Context) { s.handleWifiCreds(c) }, "wifi_creds"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := mustTenantServer(t)
			seedTenantAgent(t, s, "bc-ok", 1)

			c, w := collectorCtx(s, t, "bc-op", "")
			c.Params = gin.Params{{Key: "id", Value: "bc-ok"}}
			tc.invoke(s, c)

			if w.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s, want 200", w.Code, w.Body.String())
			}
			var task db.Task
			if err := s.db.Where("agent_id = ?", "bc-ok").First(&task).Error; err != nil {
				t.Fatalf("no task queued: %v", err)
			}
			if task.Type != tc.wantTyp {
				t.Fatalf("task type = %q, want %q", task.Type, tc.wantTyp)
			}
		})
	}
}

// TestCookieExportDefaultsBrowserAll proves the browser selector defaults to
// "all" (sent as the task command).
func TestCookieExportDefaultsBrowserAll(t *testing.T) {
	s := mustTenantServer(t)
	seedTenantAgent(t, s, "bc-cookie", 1)

	c, w := collectorCtx(s, t, "bc-op", "")
	c.Params = gin.Params{{Key: "id", Value: "bc-cookie"}}
	s.handleCookieExport(c)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d", w.Code)
	}
	var task db.Task
	if err := s.db.Where("agent_id = ?", "bc-cookie").First(&task).Error; err != nil {
		t.Fatalf("no task: %v", err)
	}
	if task.Command != "all" {
		t.Fatalf("command = %q, want the default all", task.Command)
	}
}

// TestBeaconBrowserResultFailsClosedWithoutVault proves the authenticated
// result pipeline stores neither a credential row nor plaintext task output
// when the vault is unavailable.
func TestBeaconBrowserResultFailsClosedWithoutVault(t *testing.T) {
	if !lootUninitialized() {
		t.Skip("loot key already initialized by another test in this binary")
	}
	gin.SetMode(gin.TestMode)
	s, database := newBeaconFinalityTestServer(t)
	seedTenantAgent(t, s, "bc-res", 1)
	task := db.Task{AgentID: "bc-res", TenantID: 1, Type: "browser_steal", Status: "running"}
	if err := database.Create(&task).Error; err != nil {
		t.Fatalf("seed task: %v", err)
	}
	payload, err := json.Marshal(map[string]interface{}{
		"credentials": []string{"browser|https://x|alice|pw123|c=1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	agent := db.Implant{ID: "bc-res", TenantID: 1}
	s.processTaskResults(agent, []taskResult{{
		TaskID: task.ID, Type: "browser_steal", Output: base64.StdEncoding.EncodeToString(payload),
		Encoding: "base64", ResultID: "browser-no-vault",
	}}, agent.ID, time.Now())

	var n int64
	if err := database.Model(&db.CredentialEntry{}).Count(&n).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Fatalf("vault failure stored %d credential row(s); fail-closed is broken", n)
	}
	var stored db.Task
	if err := database.First(&stored, task.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.Status != "failed" || stored.Result != "" {
		t.Fatalf("vault failure did not fail closed: status=%q result=%q", stored.Status, stored.Result)
	}
}

// TestBeaconBrowserResultStoresEncrypted proves an authenticated browser
// result still populates the vault with encrypted notes and finalizes its task.
func TestBeaconBrowserResultStoresEncrypted(t *testing.T) {
	gin.SetMode(gin.TestMode)
	forgecrypto.InitLootEncryption(tenantVisibilityMasterHex)
	s, database := newBeaconFinalityTestServer(t)
	seedTenantAgent(t, s, "bc-res2", 1)
	task := db.Task{AgentID: "bc-res2", TenantID: 1, Type: "browser_steal", Status: "running"}
	if err := database.Create(&task).Error; err != nil {
		t.Fatalf("seed task: %v", err)
	}

	const raw = "browser|https://x|alice|pw123|c=1"
	payload, err := json.Marshal(map[string]interface{}{
		"credentials": []string{raw, "browser|https://y|bob|pw456"},
	})
	if err != nil {
		t.Fatal(err)
	}
	agent := db.Implant{ID: "bc-res2", TenantID: 1}
	s.processTaskResults(agent, []taskResult{{
		TaskID: task.ID, Type: "browser_steal", Output: base64.StdEncoding.EncodeToString(payload),
		Encoding: "base64", ResultID: "browser-encrypted",
	}}, agent.ID, time.Now())

	var rows []db.CredentialEntry
	if err := database.Find(&rows).Error; err != nil {
		t.Fatalf("find: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("stored %d rows, want 2", len(rows))
	}
	for _, r := range rows {
		if r.Notes == "" {
			t.Fatal("notes empty; the raw credential was not captured")
		}
		if bytes.Contains([]byte(r.Notes), []byte("pw123")) || bytes.Contains([]byte(r.Notes), []byte("browser|")) {
			t.Fatalf("notes stored the raw credential in cleartext: %q", r.Notes)
		}
		if r.TenantID != 1 {
			t.Fatalf("tenant = %d, want inheritance from the agent", r.TenantID)
		}
	}
	if dec := decryptCredNotes(rows[0].Notes); dec != raw {
		t.Fatalf("round-trip mismatch: %q", dec)
	}
	var stored db.Task
	if err := database.First(&stored, task.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.Status != "completed" {
		t.Fatalf("task status = %q, want completed", stored.Status)
	}
}

// TestBeaconWifiResultParsesSSIDWithColonPassword proves the authenticated
// WiFi result keeps colons inside passwords and skips malformed entries.
func TestBeaconWifiResultParsesSSIDWithColonPassword(t *testing.T) {
	gin.SetMode(gin.TestMode)
	forgecrypto.InitLootEncryption(tenantVisibilityMasterHex)
	s, database := newBeaconFinalityTestServer(t)
	seedTenantAgent(t, s, "bc-wifi", 1)
	task := db.Task{AgentID: "bc-wifi", TenantID: 1, Type: "wifi_creds", Status: "running"}
	if err := database.Create(&task).Error; err != nil {
		t.Fatalf("seed task: %v", err)
	}
	payload, err := json.Marshal(map[string]interface{}{
		"networks": []string{"HomeNet:p@ss:word", "NoColonHere", "Corp:secret"},
	})
	if err != nil {
		t.Fatal(err)
	}
	agent := db.Implant{ID: "bc-wifi", TenantID: 1}
	s.processTaskResults(agent, []taskResult{{
		TaskID: task.ID, Type: "wifi_creds", Output: base64.StdEncoding.EncodeToString(payload),
		Encoding: "base64", ResultID: "wifi-colon",
	}}, agent.ID, time.Now())

	var rows []db.CredentialEntry
	if err := database.Find(&rows).Error; err != nil {
		t.Fatalf("find: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("stored %d rows, want 2 (the colonless entry must be skipped)", len(rows))
	}
	var home db.CredentialEntry
	if err := database.Where("username = ?", "HomeNet").First(&home).Error; err != nil {
		t.Fatalf("HomeNet row missing: %v", err)
	}
	if home.Password != "p@ss:word" {
		t.Fatalf("password = %q, want the full p@ss:word (SplitN 2 must keep inner colons)", home.Password)
	}
}
