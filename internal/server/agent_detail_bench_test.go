package server

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/r0si/sovereign/internal/db"
	"github.com/r0si/sovereign/internal/testutil"
)

// BenchmarkAgentDetail measures the per-agent detail page: one wide agent row,
// its children list, up to 200 unlinked agents for the link picker and 50
// tasks. `response_bytes` covers the payload the console parses; the children
// and unlinked arrays are the reason the handler scans into small row structs
// instead of serializing whole implant models.
func BenchmarkAgentDetail(b *testing.B) {
	gin.SetMode(gin.TestMode)
	database := testutil.SetupTestDB(b)
	s, _ := initBeaconTestServer(b, database)
	s.ctx = nil
	s.cfg.Server.DataDir = b.TempDir()

	now := time.Now()
	parent := db.Implant{ID: "aaaaaaaa-0000-4000-8000-000000000001", Hostname: "parent", Status: "online", LastSeen: now}
	if err := s.db.Create(&parent).Error; err != nil {
		b.Fatalf("seed parent: %v", err)
	}
	children := make([]db.Implant, 0, 200)
	for i := 0; i < 200; i++ {
		children = append(children, db.Implant{
			ID:       fmt.Sprintf("bbbbbbbb-0000-4000-8000-%012d", i),
			Hostname: fmt.Sprintf("child-%d", i),
			Status:   "online",
			LastSeen: now,
			ParentID: parent.ID,
		})
	}
	if err := s.db.CreateInBatches(children, 100).Error; err != nil {
		b.Fatalf("seed children: %v", err)
	}
	unlinked := make([]db.Implant, 0, 200)
	for i := 0; i < 200; i++ {
		unlinked = append(unlinked, db.Implant{
			ID:       fmt.Sprintf("cccccccc-0000-4000-8000-%012d", i),
			Hostname: fmt.Sprintf("free-%d", i),
			Status:   "offline",
			LastSeen: now,
		})
	}
	if err := s.db.CreateInBatches(unlinked, 100).Error; err != nil {
		b.Fatalf("seed unlinked: %v", err)
	}
	tasks := make([]db.Task, 0, 50)
	for i := 0; i < 50; i++ {
		tasks = append(tasks, db.Task{AgentID: parent.ID, Type: "shell", Command: "whoami", Status: "completed", CreatedAt: now.Add(-time.Duration(i) * time.Minute)})
	}
	if err := s.db.CreateInBatches(tasks, 50).Error; err != nil {
		b.Fatalf("seed tasks: %v", err)
	}

	b.ResetTimer()
	var bodyLen int
	for b.Loop() {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/agents/"+parent.ID+"?include_screenshots=false&format=json", nil)
		c.Params = gin.Params{{Key: "id", Value: parent.ID}}
		c.Set("user_role", db.RoleAdmin)
		s.handleAgentDetail(c)
		if w.Code != http.StatusOK {
			b.Fatalf("detail: status=%d body=%s", w.Code, w.Body.String())
		}
		bodyLen = w.Body.Len()
	}
	b.ReportMetric(float64(bodyLen), "response_bytes")
}
