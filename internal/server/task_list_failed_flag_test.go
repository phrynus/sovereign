package server

import (
	"encoding/json"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/r0si/sovereign/internal/db"
)

// TestTaskHistoryHasFailedTasksFlag pins the boolean that replaced the
// COUNT(*): the "retry all" button only needs to know whether a failed task
// exists, and the probe must flip once one does.
func TestTaskHistoryHasFailedTasksFlag(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := newAgentTestServer(t)

	read := func() bool {
		c, w := listCtx(0)
		s.handleTaskHistory(c)
		var payload map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
			t.Fatalf("decode payload: %v", err)
		}
		flag, _ := payload["has_failed_tasks"].(bool)
		return flag
	}

	if read() {
		t.Fatal("has_failed_tasks is true with no failed task")
	}
	if err := s.db.Create(&db.Task{AgentID: "aaaaaaaa-0000-4000-8000-000000000001", Type: "shell", Status: "failed"}).Error; err != nil {
		t.Fatalf("seed failed task: %v", err)
	}
	if !read() {
		t.Fatal("has_failed_tasks stayed false after a failed task was queued")
	}
}
