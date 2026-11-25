package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/r0si/sovereign/internal/db"
	"github.com/r0si/sovereign/internal/server/middleware"
)

// TestDashboardTaskChartsRequireTasksRead keeps the analysis view from becoming
// a second, unredacted task API for agents-only custom roles.
func TestDashboardTaskChartsRequireTasksRead(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := mustTenantServer(t)

	run := func(perms map[string]bool, path string) int {
		router := gin.New()
		auth := router.Group("/")
		auth.Use(func(c *gin.Context) {
			c.Set("user", "dashboard-viewer")
			c.Set("user_role", "dashboard-custom")
			c.Set("user_id", uint(2))
			c.Set(middleware.TenantIDContextKey, uint(1))
			c.Set("role_permissions", perms)
		})
		s.registerDashboardCharts(auth)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		return w.Code
	}

	for _, path := range []string{
		"/api/dashboard/activity-heatmap?range=24h",
		"/api/dashboard/task-status",
		"/api/dashboard/task-gantt?range=24h",
	} {
		if code := run(map[string]bool{db.PermAgentsRead: true}, path); code != http.StatusForbidden {
			t.Fatalf("agents-only %s = %d, want 403", path, code)
		}
		if code := run(map[string]bool{db.PermAgentsRead: true, db.PermTasksRead: true}, path); code != http.StatusOK {
			t.Fatalf("task-reader %s = %d, want 200", path, code)
		}
	}
}
