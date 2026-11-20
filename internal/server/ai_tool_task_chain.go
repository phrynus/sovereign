package server

// task_chain runs a short sequence of typed tasks on one agent and feeds each
// step's output back so the assistant can decide the next step. It is the
// "AI reads the command echo and adapts" pattern from the 火绒 2084 report,
// expressed inside Sovereign's existing guardrails: every step is a normal task
// (approval-gated when flagged), the task type must be on a quiet-recon
// whitelist, and the chain is bounded in both length and wall time.

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/r0si/sovereign/pkg/protocol"
)

const (
	aiTaskChainMaxSteps = 5
	aiTaskChainWaitCap  = 6 // seconds per step (waitForTaskResult has its own cap)
)

// aiTaskChainStep is one item in the `tasks` array.
type aiTaskChainStep struct {
	Type    string `json:"type"`
	Command string `json:"command"`
}

// aiTaskChainAllowedTypes is a deliberately small whitelist of quiet,
// non-destructive typed tasks. Sensitive operations (shell, credential theft,
// persistence, injection) are NOT chainable — they always need the operator's
// direct approval flow, and a chain must not be a back door around that.
var aiTaskChainAllowedTypes = map[string]bool{
	protocol.TaskTypeHostInfo:   true,
	protocol.TaskTypePS:         true,
	protocol.TaskTypeNetstat:    true,
	protocol.TaskTypeServices:   true,
	protocol.TaskTypeUsers:      true,
	protocol.TaskTypeDrives:     true,
	protocol.TaskTypeAV:         true,
	protocol.TaskTypeScreenshot: true,
	protocol.TaskTypeBeaconNow:  true,
}

func (s *Server) aiTaskChainTool(reqCtx *aiReqCtx, argsJSON string) string {
	var args struct {
		AgentID string            `json:"agent_id"`
		Tasks   []aiTaskChainStep `json:"tasks"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return `{"error":"invalid task_chain arguments"}`
	}
	aid := s.resolveAIAgentID(reqCtx, args.AgentID)
	if aid == "" {
		return `{"error":"agent not found"}`
	}
	if len(args.Tasks) == 0 {
		return `{"error":"task_chain needs at least one task"}`
	}
	if len(args.Tasks) > aiTaskChainMaxSteps {
		return fmt.Sprintf(`{"error":"task_chain limited to %d steps"}`, aiTaskChainMaxSteps)
	}

	results := make([]map[string]interface{}, 0, len(args.Tasks))
	previous := ""
	for i, step := range args.Tasks {
		typ := strings.TrimSpace(step.Type)
		if !aiTaskChainAllowedTypes[typ] {
			b, _ := marshalJSONSafe(map[string]interface{}{
				"error":       "task type not allowed in a chain",
				"type":        typ,
				"step":        i + 1,
				"completed":   results,
				"hint":        "Use a single approval-gated command for sensitive types (shell, creds, persistence).",
				"last_output": previous,
			})
			return string(b)
		}
		// Resolve approval status exactly as the manual dispatch path does: a
		// flagged type stays pending_approval and the chain reports that instead
		// of executing unattended. This is the two-man rule, preserved.
		status := s.resolveInitialTaskStatus(typ)
		task, err := s.createSystemTask(aid, typ, step.Command, "", status, "ai")
		if err != nil {
			b, _ := marshalJSONSafe(map[string]interface{}{
				"error": sanitizeError(err, "task"), "step": i + 1, "completed": results,
			})
			return string(b)
		}
		if status == TaskStatusPendingApproval {
			b, _ := marshalJSONSafe(map[string]interface{}{
				"status": "pending_approval", "step": i + 1, "task_id": task.ID,
				"message":   "This step needs operator approval; approve it in the Tasks page to continue the chain manually.",
				"completed": results,
			})
			return string(b)
		}
		stepResult := s.waitForTaskResult(task.ID, aid)
		results = append(results, map[string]interface{}{
			"step":    i + 1,
			"type":    typ,
			"task_id": task.ID,
			"result":  json.RawMessage(stepResult),
		})
		previous = stepResult
		if strings.Contains(stepResult, `"error"`) {
			b, _ := marshalJSONSafe(map[string]interface{}{
				"status": "stopped_on_error", "step": i + 1, "completed": results,
			})
			return string(b)
		}
	}
	b, ok := marshalJSONSafe(map[string]interface{}{
		"status": "completed", "steps": len(results), "results": results,
	})
	if !ok {
		return `{"error":"failed to marshal chain result"}`
	}
	return string(b)
}
