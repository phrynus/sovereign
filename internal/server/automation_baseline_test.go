package server

import (
	"testing"

	"github.com/forgec2/forgec2/internal/db"
)

// TestRegisterBuiltinBaselineCollection pins the auto-collect-on-check-in
// template: it is registered, targets the check-in event, uses a typed
// create_task action (not raw shell), and is disabled by default so an
// operator opts in deliberately.
func TestRegisterBuiltinBaselineCollection(t *testing.T) {
	s := mustTenantServer(t)
	s.registerBuiltinAutomations()

	var rule db.AutomationRule
	if err := s.db.Where("id = ?", "auto_baseline_collection").First(&rule).Error; err != nil {
		t.Fatalf("baseline rule not registered: %v", err)
	}
	if rule.Enabled {
		t.Fatal("baseline collection must be disabled by default")
	}
	if rule.EventType != string(EventImplantCheckin) {
		t.Fatalf("event type = %q, want %q", rule.EventType, EventImplantCheckin)
	}
	rules := s.loadAutomationRules()
	var found *AutomationRule
	for i := range rules {
		if rules[i].ID == "auto_baseline_collection" {
			found = &rules[i]
			break
		}
	}
	if found == nil {
		t.Fatal("baseline rule missing from cached rules")
	}
	if len(found.Actions) != 1 || found.Actions[0].Type != ActionCreateTask {
		t.Fatalf("actions = %+v, want a single create_task", found.Actions)
	}
	if string(found.Actions[0].Params) == "" {
		t.Fatal("create_task action has no params")
	}
}
