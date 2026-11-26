package server

import (
	"strings"
	"testing"

	"github.com/r0si/sovereign/internal/config"
	"github.com/r0si/sovereign/internal/db"
)

func TestAgentAdvertisedTaskTypesGateDispatch(t *testing.T) {
	s := newTasksTestServer(t)
	s.cfg = &config.Config{}
	seedAgent(t, s, "capability-agent")
	if err := s.db.Model(&db.Implant{}).Where("id = ?", "capability-agent").Update("task_types", "shell,ls").Error; err != nil {
		t.Fatal(err)
	}
	if _, err := s.createTask("capability-agent", "shell", "whoami", "cmd", "", "", 0, 0); err != nil {
		t.Fatalf("advertised shell task rejected: %v", err)
	}
	if _, err := s.createTask("capability-agent", "ps", "", "", "", "", 0, 0); err == nil || !strings.Contains(err.Error(), "does not support task type") {
		t.Fatalf("unadvertised ps task error = %v", err)
	}
	if _, err := s.createTask("capability-agent", "abort", "1", "", "", "", 0, 0, withEmergencyAbortReservation()); err != nil {
		t.Fatalf("emergency abort was blocked by capabilities: %v", err)
	}
}

func TestLegacyAgentWithoutCapabilitiesRemainsDispatchable(t *testing.T) {
	s := newTasksTestServer(t)
	s.cfg = &config.Config{}
	seedAgent(t, s, "legacy-capability-agent")
	if _, err := s.createTask("legacy-capability-agent", "ps", "", "", "", "", 0, 0); err != nil {
		t.Fatalf("legacy agent was restricted by an empty capability set: %v", err)
	}
}

func TestSanitizeInfoPreservesTaskCapabilityList(t *testing.T) {
	types := strings.Repeat("task_type,", 80) + "shell"
	got := sanitizeInfo(map[string]string{"task_types": types, "unknown": "discard"})
	if got["task_types"] != types {
		t.Fatalf("task capability list was truncated: got %d bytes want %d", len(got["task_types"]), len(types))
	}
	if _, ok := got["unknown"]; ok {
		t.Fatal("unknown info key survived sanitization")
	}
}

func TestValidateTaskArgumentsUsesExpectedSHA256Declaration(t *testing.T) {
	hash := strings.Repeat("a", 64)
	if err := validateTaskArguments("write", "C:\\Temp\\a.txt", "", "C:\\Temp\\a.txt", "", ""); err == nil || !strings.Contains(err.Error(), "expected_sha256") {
		t.Fatalf("missing expected_sha256 error = %v", err)
	}
	if err := validateTaskArguments("write", "C:\\Temp\\a.txt", "", "C:\\Temp\\a.txt", "", hash); err != nil {
		t.Fatalf("declared expected_sha256 rejected: %v", err)
	}
}
