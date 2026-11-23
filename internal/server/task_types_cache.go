package server

import (
	"log/slog"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/r0si/sovereign/internal/db"
)

// taskListSilentTypes are hidden from the task list and its type filter: the
// long-running stream control rows and the `ls` alias would otherwise crowd
// the dropdown.
var taskListSilentTypes = []string{"screen_stream_start", "screen_stream_stop", "ls"}

const (
	taskTypesCacheTTL        = 60 * time.Second
	taskTypesCacheMaxTenants = 512
)

// taskTypesCacheEntry is one tenant's cached distinct-type list.
type taskTypesCacheEntry struct {
	at    time.Time
	types []string
}

// cachedTaskTypes returns the distinct task types for the caller's tenant,
// cached for taskTypesCacheTTL. A newly queued type becomes visible within that
// window, which is fine for a filter dropdown; the DISTINCT scan measured
// ~410 µs over 4 000 tasks (pure-Go SQLite, even with the covering
// (tenant_id, type) index), making it the most expensive query on the page.
//
// The returned slice is a copy: the cached list is shared across goroutines and
// the caller embeds it in a JSON response.
func (s *Server) cachedTaskTypes(c *gin.Context) []string {
	tid := s.currentTenantID(c)
	now := time.Now()

	s.taskTypesCacheMu.Lock()
	if e, ok := s.taskTypesCache[tid]; ok && now.Sub(e.at) < taskTypesCacheTTL {
		types := append([]string(nil), e.types...)
		s.taskTypesCacheMu.Unlock()
		return types
	}
	s.taskTypesCacheMu.Unlock()

	var types []string
	if err := s.tenantScope(s.db.Model(&db.Task{}), c).
		Where("type NOT IN ?", taskListSilentTypes).
		Distinct("type").Pluck("type", &types).Error; err != nil {
		slog.Warn("Failed to pluck task types", "err", err)
		return []string{}
	}
	if types == nil {
		types = []string{}
	}

	s.taskTypesCacheMu.Lock()
	if s.taskTypesCache == nil {
		s.taskTypesCache = make(map[uint]taskTypesCacheEntry)
	}
	if len(s.taskTypesCache) >= taskTypesCacheMaxTenants {
		for id, e := range s.taskTypesCache {
			if now.Sub(e.at) >= taskTypesCacheTTL {
				delete(s.taskTypesCache, id)
			}
		}
	}
	s.taskTypesCache[tid] = taskTypesCacheEntry{at: now, types: types}
	s.taskTypesCacheMu.Unlock()

	return append([]string(nil), types...)
}
