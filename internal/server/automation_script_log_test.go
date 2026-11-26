package server

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

// TestAutomationScriptFailureIsLogged pins the fix for a silent-failure shape: the ActionRunScript
// branch threw away the ExecutionResult, so a script that failed on every event (a typo, a
// permission the automation role lacks, a timeout) left no trace at all - the rule "ran", the event
// was consumed, and the only symptom was whatever the script was supposed to do not happening.
func TestAutomationScriptFailureIsLoggedAndReturned(t *testing.T) {
	s := mustTenantServer(t)
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(previous) })
	run := func(action RuleAction, evt Event) error {
		t.Helper()
		return s.executeAction(AutomationRule{ID: "test"}, action, evt)
	}

	err := run(RuleAction{Type: ActionRunScript, Params: []byte(`{"code":"throw new Error('boom')"}`)},
		Event{Type: EventImplantCheckin, AgentID: "a-1", TenantID: 1})
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("failing script error = %v", err)
	}
	out := logs.String()
	if !strings.Contains(out, "script failed") || !strings.Contains(out, "boom") || !strings.Contains(out, "a-1") {
		t.Fatalf("failing automation script log is incomplete: %q", out)
	}

	logs.Reset()
	err = run(RuleAction{Type: ActionCreateTask, Params: []byte(`{"type":"shell","command":"whoami"}`)},
		Event{Type: EventImplantCheckin, TenantID: 1})
	if err == nil || !strings.Contains(err.Error(), "create_task") {
		t.Fatalf("targetless create_task error = %v", err)
	}
	err = run(RuleAction{Type: ActionSetSleep, Params: []byte(`{"agent_id":"a-1","interval":0}`)},
		Event{Type: EventImplantCheckin, AgentID: "a-1", TenantID: 1})
	if err == nil || !strings.Contains(err.Error(), "set_sleep") {
		t.Fatalf("invalid set_sleep error = %v", err)
	}

	logs.Reset()
	if err := run(RuleAction{Type: ActionRunScript, Params: []byte(`{"code":"1 + 1"}`)},
		Event{Type: EventImplantCheckin, AgentID: "a-1", TenantID: 1}); err != nil {
		t.Fatalf("successful automation script returned error: %v", err)
	}
	if !strings.Contains(logs.String(), "script ran") {
		t.Fatalf("a successful automation script was not logged at debug: %q", logs.String())
	}
}
