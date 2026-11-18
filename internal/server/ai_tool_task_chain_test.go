package server

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/forgec2/forgec2/internal/db"
)

func TestAITaskChainTool(t *testing.T) {
	s := mustTenantServer(t)
	if err := s.db.Create(&db.Implant{ID: "chain-agent", TenantID: 1, Hostname: "chain-host", Status: "online"}).Error; err != nil {
		t.Fatal(err)
	}
	call := func(args map[string]interface{}) map[string]interface{} {
		raw, _ := json.Marshal(args)
		out := s.aiTaskChainTool(&aiReqCtx{Principal: aiPrincipal{UserID: 1, TenantID: 1, Username: "op", Role: db.RoleAdmin}}, string(raw))
		var m map[string]interface{}
		if err := json.Unmarshal([]byte(out), &m); err != nil {
			t.Fatalf("tool returned non-json: %q", out)
		}
		return m
	}
	// Missing agent short-circuits before any task is created.
	m := call(map[string]interface{}{"agent_id": "does-not-exist", "tasks": []map[string]string{{"type": "hostinfo"}}})
	if m["error"] != "agent not found" {
		t.Fatalf("missing agent: %v", m)
	}

	// Sensitive types are refused in a chain even though they are valid tasks.
	m = call(map[string]interface{}{"agent_id": "chain-agent", "tasks": []map[string]string{{"type": "shell", "command": "whoami"}}})
	if !strings.Contains(m["error"].(string), "not allowed") {
		t.Fatalf("sensitive type was chainable: %v", m)
	}

	// More than 5 steps is refused.
	steps := make([]map[string]string, 6)
	for i := range steps {
		steps[i] = map[string]string{"type": "hostinfo"}
	}
	m = call(map[string]interface{}{"agent_id": "chain-agent", "tasks": steps})
	if !strings.Contains(m["error"].(string), "5 steps") {
		t.Fatalf("oversized chain accepted: %v", m)
	}

	// Empty chain refused.
	m = call(map[string]interface{}{"agent_id": "chain-agent", "tasks": []map[string]string{}})
	if !strings.Contains(m["error"].(string), "at least one") {
		t.Fatalf("empty chain accepted: %v", m)
	}

	// The whitelist itself is the gate: shell/creds/persistence must be absent.
	for _, sensitive := range []string{"shell", "creds", "persistence_add", "mimikatz", "inject"} {
		if aiTaskChainAllowedTypes[sensitive] {
			t.Fatalf("sensitive type %q is chainable", sensitive)
		}
	}
}
