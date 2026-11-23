package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/r0si/sovereign/internal/db"
)

// TestDashboardTaskGanttUsesHostname pins the preload the gantt relies on: the
// query now selects six task columns and preloads only id/hostname from the
// agent, so a regression that drops the association (or forgets agent_id in
// the select) would silently fall back to the truncated agent id.
func TestDashboardTaskGanttUsesHostname(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := &Server{db: newContractDB(t)}

	agent := db.Implant{ID: "11111111-2222-4333-8444-555555555555", Hostname: "WS-GANTT", Status: "online"}
	if err := s.db.Create(&agent).Error; err != nil {
		t.Fatalf("seed agent: %v", err)
	}
	now := time.Now()
	task := db.Task{AgentID: agent.ID, Type: "shell", Status: "completed", CreatedAt: now.Add(-2 * time.Minute), UpdatedAt: now}
	if err := s.db.Create(&task).Error; err != nil {
		t.Fatalf("seed task: %v", err)
	}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/dashboard/task-gantt?range=24h", nil)
	s.handleDashboardTaskGantt(c)

	var env struct {
		Success bool            `json:"success"`
		Data    []TaskGanttItem `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode gantt: %v (%s)", err, w.Body.String())
	}
	if !env.Success || len(env.Data) != 1 {
		t.Fatalf("gantt items = %d (success=%v), want 1", len(env.Data), env.Success)
	}
	if env.Data[0].Agent != "WS-GANTT" {
		t.Fatalf("gantt agent = %q, want the preloaded hostname WS-GANTT", env.Data[0].Agent)
	}
	if env.Data[0].Task != "shell" || env.Data[0].Status != "completed" {
		t.Fatalf("gantt item = %+v", env.Data[0])
	}
}
