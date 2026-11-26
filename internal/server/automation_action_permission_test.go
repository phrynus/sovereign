package server

import (
	"strings"
	"testing"

	"github.com/r0si/sovereign/internal/db"
)

func TestAutomationActionRechecksCreatorPermissions(t *testing.T) {
	s := mustTenantServer(t)
	if err := s.db.Create(&db.User{Username: "restricted", Role: "restricted", IsActive: true}).Error; err != nil {
		t.Fatal(err)
	}
	seedTenantAgent(t, s, "automation-permission-agent", 1)
	rule := AutomationRule{ID: "permission-rule", CreatedBy: "restricted", TenantID: 1}
	action := RuleAction{Type: ActionRunCommand, Params: []byte(`{"command":"whoami"}`)}
	err := s.executeAction(rule, action, Event{Type: EventImplantCheckin, AgentID: "automation-permission-agent", TenantID: 1})
	if err == nil || !strings.Contains(err.Error(), db.PermAgentsWrite) {
		t.Fatalf("permission loss error = %v", err)
	}
	var count int64
	if err := s.db.Model(&db.Task{}).Where("agent_id = ?", "automation-permission-agent").Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("permission-denied automation queued %d tasks", count)
	}
}

func TestAutomationActionAllowsCurrentCreatorPermission(t *testing.T) {
	s := mustTenantServer(t)
	seedMissingAPIAdmin(t, s)
	seedTenantAgent(t, s, "automation-allowed-agent", 1)
	rule := AutomationRule{ID: "allowed-rule", CreatedBy: "admin", TenantID: 1}
	action := RuleAction{Type: ActionRunCommand, Params: []byte(`{"command":"whoami"}`)}
	if err := s.executeAction(rule, action, Event{Type: EventImplantCheckin, AgentID: "automation-allowed-agent", TenantID: 1}); err != nil {
		t.Fatalf("authorized automation action failed: %v", err)
	}
	var count int64
	if err := s.db.Model(&db.Task{}).Where("agent_id = ?", "automation-allowed-agent").Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("authorized automation queued %d tasks", count)
	}
}
