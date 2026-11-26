package server

import (
	"net/http"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/r0si/sovereign/internal/db"
)

func TestComputeTaskStatsConvergesPendingApproval(t *testing.T) {
	s := newTasksTestServer(t)
	seedAgent(t, s, "stats-agent")
	for _, status := range []string{"pending", TaskStatusPendingApproval, "running", "completed", "failed"} {
		if err := s.db.Create(&db.Task{AgentID: "stats-agent", Type: "shell", Status: status}).Error; err != nil {
			t.Fatal(err)
		}
	}
	stats := computeTaskStats(s.db, []string{"stats-agent"})["stats-agent"]
	if stats == nil || stats.Pending != 2 || stats.Running != 1 || stats.Completed != 1 || stats.Failed != 1 {
		t.Fatalf("converged task stats = %+v", stats)
	}
}

func TestExpiredApprovalCancellationReleasesCounterOnce(t *testing.T) {
	s := newTasksTestServer(t)
	seedAgent(t, s, "expiry-counter-agent")
	past := time.Now().Add(-time.Minute)
	task := db.Task{AgentID: "expiry-counter-agent", Type: "shell", Status: TaskStatusPendingApproval, ApprovalExpiresAt: &past}
	if err := s.db.Create(&task).Error; err != nil {
		t.Fatal(err)
	}
	s.agentPendingTasks[task.AgentID] = 1
	cancelled, ok := s.maybeCancelExpiredApproval(task.ID, time.Now())
	if !ok || cancelled.Status != "cancelled" {
		t.Fatalf("expired approval = %+v, ok=%v", cancelled, ok)
	}
	if got := s.agentPendingTasks[task.AgentID]; got != 0 {
		t.Fatalf("expiry cancellation leaked pending slot: %d", got)
	}
	if _, ok := s.maybeCancelExpiredApproval(task.ID, time.Now()); ok {
		t.Fatal("expired approval was cancelled twice")
	}
}

func TestApprovalClearsExpiryAndPersistsApproverTime(t *testing.T) {
	s := mustTenantServer(t)
	seedMissingAPIAdmin(t, s)
	seedTenantAgent(t, s, "approval-state-agent", 1)
	future := time.Now().Add(time.Hour)
	task := db.Task{AgentID: "approval-state-agent", TenantID: 1, Type: "shell", Command: "whoami", Status: TaskStatusPendingApproval, CreatedBy: "creator", ApprovalExpiresAt: &future}
	if err := s.db.Create(&task).Error; err != nil {
		t.Fatal(err)
	}
	s.agentPendingTasks[task.AgentID] = 1
	c, w := missingAPICtx(http.MethodPost, "/tasks/"+uintString(task.ID)+"/approve", "", gin.Params{{Key: "taskId", Value: uintString(task.ID)}})
	s.handleApproveTask(c)
	if w.Code != http.StatusOK {
		t.Fatalf("approve status = %d, body = %s", w.Code, w.Body.String())
	}
	var stored db.Task
	if err := s.db.First(&stored, task.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.Status != "pending" || stored.ApprovalExpiresAt != nil || stored.ApprovedAt == nil || stored.ApprovedBy != "admin" {
		t.Fatalf("approval state did not converge: %+v", stored)
	}
	if got := s.agentPendingTasks[task.AgentID]; got != 1 {
		t.Fatalf("approval changed pending accounting: %d", got)
	}
}
