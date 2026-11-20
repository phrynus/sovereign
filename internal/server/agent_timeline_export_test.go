package server

import (
	"encoding/csv"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/r0si/sovereign/internal/db"
	"github.com/r0si/sovereign/internal/server/middleware"
)

// TestAgentTimelineCSVExport covers ?format=csv on the activity timeline.
//
// Every other list in the console can be taken away, and the timeline is the one an analyst reads IN ORDER - which is what a
// spreadsheet is good at. The export must carry the same events as the JSON view, with the same order, and nothing that a
// spreadsheet cannot hold.
func TestAgentTimelineCSVExport(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := mustTenantServer(t)

	now := time.Now().Truncate(time.Second)
	if err := s.db.Create(&db.Implant{
		ID: "a-1", TenantID: 1, Hostname: "web-01", Status: "online", LastSeen: now,
	}).Error; err != nil {
		t.Fatalf("seed agent: %v", err)
	}
	// Two tasks at different times, so the order is checkable. Their ids are
	// captured: the ordering assertion below compares ref ids, never dates —
	// comparing a formatted date made this test pass or fail depending on the
	// wall-clock hour and the host timezone (it failed at 00:40 UTC, when the
	// newest task fell on the previous day).
	var taskIDs []uint
	for i, age := range []time.Duration{time.Hour, 2 * time.Hour} {
		task := db.Task{
			AgentID: "a-1", TenantID: 1, Type: "shell", Command: "whoami", Status: "completed",
			Result: "x", CreatedAt: now.Add(-age), UpdatedAt: now.Add(-age),
		}
		if i == 1 {
			task.Status = "failed"
			task.Error = "context deadline exceeded"
		}
		if err := s.db.Create(&task).Error; err != nil {
			t.Fatalf("seed task: %v", err)
		}
		taskIDs = append(taskIDs, task.ID)
	}
	// Older than both tasks: the timeline mixes kinds, and a status event
	// newer than the newest task legitimately sorts first.
	if err := s.db.Create(&db.AgentStatusEvent{
		AgentID: "a-1", Status: "online", Timestamp: now.Add(-3 * time.Hour),
	}).Error; err != nil {
		t.Fatalf("seed status event: %v", err)
	}

	call := func(query string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/api/agents/a-1/timeline"+query, nil)
		c.Params = gin.Params{{Key: "id", Value: "a-1"}}
		c.Set("user", "op")
		c.Set("user_role", db.RoleAdmin)
		c.Set("user_id", uint(1))
		c.Set(middleware.TenantIDContextKey, uint(1))
		s.handleAgentTimeline(c)
		return w
	}

	// 1. The CSV is a file, not JSON.
	w := call("?format=csv")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d (%s)", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/csv") {
		t.Fatalf("Content-Type = %q", ct)
	}
	if cd := w.Header().Get("Content-Disposition"); !strings.HasSuffix(cd, `.csv"`) {
		t.Fatalf("Content-Disposition = %q", cd)
	}

	records, err := csv.NewReader(strings.NewReader(w.Body.String())).ReadAll()
	if err != nil {
		t.Fatalf("csv: %v", err)
	}
	if len(records) < 4 {
		t.Fatalf("rows = %d, want a header, both tasks, and the status event", len(records))
	}
	if got := strings.Join(records[0], ","); got != "Time,Kind,Type,Title,Detail,Status,Ref ID" {
		t.Fatalf("header = %q", got)
	}
	// Newest first, like the JSON view. Compared by ref id and by the parsed
	// timestamp, so the assertion does not depend on the host timezone or on
	// where the wall clock sits relative to midnight.
	if records[1][6] != strconv.FormatUint(uint64(taskIDs[0]), 10) {
		t.Fatalf("the newest task is not first: %v (want ref %d)", records[1], taskIDs[0])
	}
	if records[2][6] != strconv.FormatUint(uint64(taskIDs[1]), 10) {
		t.Fatalf("the second task is not second: %v (want ref %d)", records[2], taskIDs[1])
	}
	prev := time.Time{}
	for i, row := range records[1:] {
		parsed, err := time.Parse("2006-01-02 15:04:05", row[0])
		if err != nil {
			t.Fatalf("row %d time %q is not sortable: %v", i+1, row[0], err)
		}
		if i > 0 && parsed.After(prev) {
			t.Fatalf("rows are not reverse-chronological at row %d: %s after %s", i+1, parsed, prev)
		}
		prev = parsed
	}
	// The failed one carries its status, because that is what a reader scans for.
	foundFailed := false
	for _, row := range records[1:] {
		if row[5] == "failed" {
			foundFailed = true
		}
	}
	if !foundFailed {
		t.Fatalf("no row reports the failed status: %v", records)
	}
	foundStatus := false
	for _, row := range records[1:] {
		if row[1] == "status" && row[3] == "online" {
			foundStatus = true
		}
	}
	if !foundStatus {
		t.Fatalf("agent status history is missing from the export: %v", records)
	}

	// 2. The default is still JSON, with the same events.
	jsonResponse := call("")
	if ct := jsonResponse.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Fatalf("default Content-Type = %q", ct)
	}
	if !strings.Contains(jsonResponse.Body.String(), `"events"`) {
		t.Fatalf("the default shape changed: %s", jsonResponse.Body.String())
	}
	if !strings.Contains(jsonResponse.Body.String(), `"kind":"status"`) {
		t.Fatalf("agent status history is missing from JSON: %s", jsonResponse.Body.String())
	}

	// A kind filter is also a query-planning contract: excluded sources must
	// not be queried or leak into the response.
	statusOnly := call("?kinds=status")
	if statusOnly.Code != http.StatusOK {
		t.Fatalf("status-only response = %d (%s)", statusOnly.Code, statusOnly.Body.String())
	}
	if strings.Contains(statusOnly.Body.String(), `"kind":"task"`) {
		t.Fatalf("status-only response leaked tasks: %s", statusOnly.Body.String())
	}
	if !strings.Contains(statusOnly.Body.String(), `"kind":"status"`) {
		t.Fatalf("status-only response lost status history: %s", statusOnly.Body.String())
	}
}

// TestAgentTimelineQueryFailureIsNotPartial proves that each database-backed
// source fails closed. Returning 200 after dropping one source makes an
// incomplete activity history indistinguishable from a complete one.
func TestAgentTimelineQueryFailureIsNotPartial(t *testing.T) {
	tests := []struct {
		name  string
		table string
		query string
	}{
		{name: "tasks", table: "tasks", query: "?kinds=task"},
		{name: "credentials", table: "credential_entries", query: "?kinds=credential"},
		{name: "status", table: "agent_status_events", query: "?kinds=status"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := mustTenantServer(t)
			if err := s.db.Create(&db.Implant{ID: "a-1", TenantID: 1, Hostname: "web-01"}).Error; err != nil {
				t.Fatalf("seed agent: %v", err)
			}
			if err := s.db.Migrator().DropTable(tc.table); err != nil {
				t.Fatalf("drop %s: %v", tc.table, err)
			}

			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest(http.MethodGet, "/api/agents/a-1/timeline"+tc.query, nil)
			c.Params = gin.Params{{Key: "id", Value: "a-1"}}
			c.Set("user", "op")
			c.Set("user_role", db.RoleAdmin)
			c.Set("user_id", uint(1))
			c.Set(middleware.TenantIDContextKey, uint(1))

			s.handleAgentTimeline(c)

			if w.Code != http.StatusInternalServerError {
				t.Fatalf("status = %d, want 500; body=%s", w.Code, w.Body.String())
			}
			if strings.Contains(w.Body.String(), `"events"`) {
				t.Fatalf("failed source returned a partial timeline: %s", w.Body.String())
			}
		})
	}
}
