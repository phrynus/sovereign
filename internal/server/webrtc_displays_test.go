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
	forgecrypto "github.com/r0si/sovereign/internal/crypto"
	"github.com/r0si/sovereign/internal/db"
)

// TestNormalizeWebRTCStartDisplayBounds keeps the monitor selector inside the
// same bound the agent enforces: 0 is "all displays", 1..Max is one monitor.
func TestNormalizeWebRTCStartDisplayBounds(t *testing.T) {
	for _, display := range []int{0, 1, MaxWebRTCDisplayIndex} {
		opts, err := normalizeWebRTCStart(webrtcStartRequest{Display: display})
		if err != nil {
			t.Fatalf("display %d refused: %v", display, err)
		}
		if opts.Display != display {
			t.Fatalf("display = %d, want %d", opts.Display, display)
		}
	}
	for _, display := range []int{-1, MaxWebRTCDisplayIndex + 1, 99} {
		if _, err := normalizeWebRTCStart(webrtcStartRequest{Display: display}); err == nil {
			t.Fatalf("display %d accepted", display)
		}
	}
}

// TestWebRTCStartCarriesDisplayIndex proves the selector survives the whole
// path the agent reads: POST body → normalise → screen_webrtc_start task data.
func TestWebRTCStartCarriesDisplayIndex(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := mustTenantServer(t)
	s.wsUpgrader = websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	s.cfg.WebRTC.Enabled = true
	// Task payloads are encrypted at rest; the vault has to exist before a
	// screen_webrtc_start task can be written.
	forgecrypto.InitLootEncryption(testStorageKeyHex)
	// Bind the embedded TURN relay on an ephemeral port so the test does not
	// fight another test (or the host) for UDP 3478.
	s.cfg.WebRTC.TurnAddr = "127.0.0.1:0"
	s.cfg.WebRTC.PublicIP = "127.0.0.1"
	agent := db.Implant{ID: "rd-display", TenantID: 1, Hostname: "display", Status: "online", LastSeen: time.Now()}
	if err := s.db.Create(&agent).Error; err != nil {
		t.Fatalf("seed agent: %v", err)
	}

	engine := gin.New()
	engine.POST("/api/agents/:id/webrtc/start", func(c *gin.Context) {
		c.Set("user_role", db.RoleAdmin)
		c.Set("user_id", uint(7))
		s.handleWebRTCStart(c)
	})
	req := httptest.NewRequest(http.MethodPost, "/api/agents/rd-display/webrtc/start",
		strings.NewReader(`{"width":1280,"height":720,"fps":30,"display":2}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	engine.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("start = %d: %s", w.Code, w.Body.String())
	}
	var response struct {
		Data struct {
			SessionID string `json:"session_id"`
			Settings  struct {
				Display int `json:"display"`
			} `json:"settings"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Data.Settings.Display != 2 {
		t.Fatalf("response display = %d, want 2", response.Data.Settings.Display)
	}
	t.Cleanup(func() {
		if sess := s.webRTCHub().byAgentSession("rd-display"); sess != nil {
			sess.end("test")
		}
	})

	// The agent receives the index in the task payload, which is what actually
	// chooses the capture region.
	var task db.Task
	if err := s.db.Where("agent_id = ? AND type = ?", "rd-display", "screen_webrtc_start").First(&task).Error; err != nil {
		t.Fatalf("load start task: %v", err)
	}
	var payload struct {
		SessionID string `json:"session_id"`
		Settings  struct {
			Display int `json:"display"`
		} `json:"settings"`
	}
	if err := json.Unmarshal([]byte(task.Data), &payload); err != nil {
		t.Fatalf("task data is not the agent payload: %v (%s)", err, task.Data)
	}
	if payload.SessionID != response.Data.SessionID {
		t.Fatalf("task session = %q, want %q", payload.SessionID, response.Data.SessionID)
	}
	if payload.Settings.Display != 2 {
		t.Fatalf("task payload display = %d, want 2", payload.Settings.Display)
	}
}

// TestRequestScreenshotCarriesDisplayIndex proves a screenshot task names the
// monitor too: the agent crops that display out of its full capture.
func TestRequestScreenshotCarriesDisplayIndex(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := mustTenantServer(t)
	agent := db.Implant{ID: "shot-display", TenantID: 1, Hostname: "shot", Status: "online", LastSeen: time.Now()}
	if err := s.db.Create(&agent).Error; err != nil {
		t.Fatalf("seed agent: %v", err)
	}

	engine := gin.New()
	engine.POST("/agents/:id/screenshot", func(c *gin.Context) {
		c.Set("user_role", db.RoleAdmin)
		c.Set("user_id", uint(7))
		s.handleRequestScreenshot(c)
	})
	body := strings.NewReader(`{"display":3}`)
	req := httptest.NewRequest(http.MethodPost, "/agents/shot-display/screenshot", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	engine.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("screenshot = %d: %s", w.Code, w.Body.String())
	}
	var task db.Task
	if err := s.db.Where("agent_id = ? AND type = ?", "shot-display", "screenshot").First(&task).Error; err != nil {
		t.Fatalf("load task: %v", err)
	}
	if task.Command != "display=3" {
		t.Fatalf("task command = %q, want display=3", task.Command)
	}

	// An out-of-range index is refused before a task exists.
	bad := httptest.NewRequest(http.MethodPost, "/agents/shot-display/screenshot",
		strings.NewReader(`{"display":99}`))
	bad.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	engine.ServeHTTP(w, bad)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("display 99 = %d, want 400", w.Code)
	}
}
