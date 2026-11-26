package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/r0si/sovereign/internal/db"
)

func TestLateralHistoryIncludesSpecializedMovementTypes(t *testing.T) {
	s := mustTenantServer(t)
	seedTenantAgent(t, s, "lateral-source", 1)
	for _, taskType := range []string{"lateral", "ssh_lateral", "lateral_wmi", "lateral_list"} {
		if err := s.db.Create(&db.Task{AgentID: "lateral-source", TenantID: 1, Type: taskType, Command: "wmi|10.0.0.9|alice|secret|whoami", Status: "completed"}).Error; err != nil {
			t.Fatal(err)
		}
	}
	c, w := missingAPICtx(http.MethodGet, "/api/lateral/history/all", "", gin.Params{{Key: "id", Value: "all"}})
	s.handleLateralHistory(c)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	var payload struct {
		Tasks []LateralHistoryRow `json:"tasks"`
		Total int                 `json:"total"`
	}
	unmarshalData(t, w.Body.Bytes(), &payload)
	if payload.Total != 3 || len(payload.Tasks) != 3 {
		t.Fatalf("specialized lateral history = %+v", payload)
	}
	for _, row := range payload.Tasks {
		if row.Type == "lateral_list" {
			t.Fatal("enumeration task was reported as a movement")
		}
	}
}

func TestLateralResultAuditTracesSuccessfulAndFailedMovements(t *testing.T) {
	s, database := newBeaconFinalityTestServer(t)
	agent := db.Implant{ID: "lateral-audit-source", TenantID: 1}
	if err := database.Create(&agent).Error; err != nil {
		t.Fatal(err)
	}
	for _, result := range []taskResult{
		{Type: "lateral", Output: "moved", ResultID: "lateral-ok"},
		{Type: "lateral", Error: "access denied", ResultID: "lateral-failed"},
	} {
		task := db.Task{AgentID: agent.ID, TenantID: 1, Type: "lateral", Command: "wmi|10.0.0.9|alice|DO_NOT_LOG|whoami", Status: "running"}
		if err := database.Create(&task).Error; err != nil {
			t.Fatal(err)
		}
		result.TaskID = task.ID
		s.processTaskResults(agent, []taskResult{result}, agent.ID, time.Now())
	}
	var rows []db.AuditLog
	if err := database.Where("action = ?", "lateral_result").Order("id").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("lateral result audit rows = %d", len(rows))
	}
	for _, row := range rows {
		if !strings.Contains(row.Details, "source=lateral-audit-source") || !strings.Contains(row.Details, "method=wmi") || !strings.Contains(row.Details, "target=10.0.0.9") {
			t.Fatalf("lateral audit omitted edge metadata: %+v", row)
		}
		if strings.Contains(row.Details, "DO_NOT_LOG") {
			t.Fatal("lateral audit leaked command material")
		}
	}
	if rows[0].Success != true || rows[1].Success != false {
		t.Fatalf("lateral audit outcomes = %+v", rows)
	}
}

func TestLateralHistoryRedactionStillHidesMovementDetails(t *testing.T) {
	s, _ := newBeaconFinalityTestServer(t)
	if err := s.db.Create(&db.User{Username: "reader", Role: "restricted", TenantID: 1, IsActive: true}).Error; err != nil {
		t.Fatal(err)
	}
	seedTenantAgent(t, s, "lateral-redacted-source", 1)
	if err := s.db.Create(&db.Task{AgentID: "lateral-redacted-source", TenantID: 1, Type: "lateral", Command: "wmi|10.0.0.9|alice|secret|whoami", Status: "completed", Result: "secret-result"}).Error; err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/lateral/history/all", nil)
	c.Set("user", "reader")
	c.Set("user_role", "restricted")
	c.Set("user_id", uint(1))
	c.Params = gin.Params{{Key: "id", Value: "all"}}
	s.handleLateralHistory(c)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	var payload struct {
		Tasks         []LateralHistoryRow `json:"tasks"`
		TasksRedacted bool                `json:"tasks_redacted"`
	}
	unmarshalData(t, w.Body.Bytes(), &payload)
	if !payload.TasksRedacted || len(payload.Tasks) != 1 || payload.Tasks[0].Target != "" || payload.Tasks[0].Result != "" {
		t.Fatalf("redacted lateral row = %+v body=%s", payload, w.Body.String())
	}
}
