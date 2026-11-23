package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/r0si/sovereign/internal/db"
)

func taskTypesCtx(tid uint) *gin.Context {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/tasks", nil)
	c.Set(tenantIDContextKey, tenantResolution{tid: tid, ok: true})
	return c
}

// TestCachedTaskTypesPerTenant proves the cache is keyed by tenant, returns a
// copy, and refreshes once the TTL elapses.
func TestCachedTaskTypesPerTenant(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := newAgentTestServer(t)
	for _, seed := range []struct {
		agent, taskType string
		tenant          uint
	}{
		{"aaaaaaaa-0000-4000-8000-000000000001", "alpha", 1},
		{"bbbbbbbb-0000-4000-8000-000000000002", "beta", 2},
	} {
		if err := s.db.Create(&db.Task{AgentID: seed.agent, TenantID: seed.tenant, Type: seed.taskType, Status: "completed"}).Error; err != nil {
			t.Fatalf("seed task: %v", err)
		}
	}

	tenant1 := s.cachedTaskTypes(taskTypesCtx(1))
	if containsString(tenant1, "beta") {
		t.Fatalf("tenant 1 saw another tenant's task type: %v", tenant1)
	}
	if !containsString(tenant1, "alpha") {
		t.Fatalf("tenant 1 missing its own task type: %v", tenant1)
	}

	// A new type is not visible until the TTL elapses.
	if err := s.db.Create(&db.Task{AgentID: "aaaaaaaa-0000-4000-8000-000000000001", TenantID: 1, Type: "gamma", Status: "completed"}).Error; err != nil {
		t.Fatalf("seed gamma: %v", err)
	}
	if got := s.cachedTaskTypes(taskTypesCtx(1)); containsString(got, "gamma") {
		t.Fatalf("cached list already contains a type queued after it was cached: %v", got)
	}

	s.taskTypesCacheMu.Lock()
	entry := s.taskTypesCache[1]
	entry.at = time.Now().Add(-2 * taskTypesCacheTTL)
	s.taskTypesCache[1] = entry
	s.taskTypesCacheMu.Unlock()

	if got := s.cachedTaskTypes(taskTypesCtx(1)); !containsString(got, "gamma") {
		t.Fatalf("type cached before the TTL was not refreshed: %v", got)
	}
	if got := s.cachedTaskTypes(taskTypesCtx(2)); containsString(got, "alpha") || !containsString(got, "beta") {
		t.Fatalf("tenant 2 got the wrong list: %v", got)
	}
}

// TestCachedTaskTypesReturnsCopy makes sure a caller cannot mutate the cached
// slice (the same list is served to every operator of the tenant).
func TestCachedTaskTypesReturnsCopy(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := newAgentTestServer(t)
	if err := s.db.Create(&db.Task{AgentID: "aaaaaaaa-0000-4000-8000-000000000001", TenantID: 1, Type: "alpha", Status: "completed"}).Error; err != nil {
		t.Fatalf("seed task: %v", err)
	}
	first := s.cachedTaskTypes(taskTypesCtx(1))
	for i := range first {
		first[i] = "mutated"
	}
	second := s.cachedTaskTypes(taskTypesCtx(1))
	if containsString(second, "mutated") {
		t.Fatalf("cached list was mutated through the returned slice: %v", second)
	}
}
