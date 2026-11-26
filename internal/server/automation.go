package server

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"runtime/debug"
	"strings"
	"time"

	"github.com/r0si/sovereign/internal/db"
	"github.com/r0si/sovereign/internal/scripting"
	"github.com/r0si/sovereign/internal/server/middleware"
	"gorm.io/gorm"
)

type AutomationRule struct {
	ID            string          `json:"id"`
	Name          string          `json:"name"`
	Description   string          `json:"description"`
	Enabled       bool            `json:"enabled"`
	Priority      int             `json:"priority"` // higher = runs first
	EventType     string          `json:"event_type"`
	Conditions    []RuleCondition `json:"conditions"`
	Actions       []RuleAction    `json:"actions"`
	Cooldown      int             `json:"cooldown"` // seconds between triggers
	LastTriggered time.Time       `json:"last_triggered"`
	RunCount      int             `json:"run_count"`
	Schedule      string          `json:"schedule"` // cron/interval for event_type="schedule"
	AgentID       string          `json:"agent_id"`
	TaskType      string          `json:"task_type"`
	Command       string          `json:"command"`
	Params        string          `json:"params"`
	LastRun       time.Time       `json:"last_run"`
	NextRun       time.Time       `json:"next_run"`
	CreatedBy     string          `json:"created_by"`
	TenantID      uint            `json:"tenant_id"`
	CreatedAt     string          `json:"created_at"`
}

// EventSchedule is the pseudo event type used to mark schedule-driven rules.
const EventSchedule = "schedule"

// Action types for automation rules
const (
	ActionRunCommand = "command"
	ActionWebhook    = "webhook"
	ActionNotify     = "notify"
	ActionRunScript  = "script"
	ActionCreateTask = "create_task"
	ActionSetSleep   = "set_sleep"
	ActionRunMacro   = "run_macro"
)

type RuleCondition struct {
	Field    string `json:"field"`    // "agent.hostname", "data.*"
	Operator string `json:"operator"` // "contains", "equals", "regex"
	Value    string `json:"value"`
}

type RuleAction struct {
	Type   string          `json:"type"` // "command", "webhook", "notify"
	Params json.RawMessage `json:"params"`
}

func (s *Server) evaluateRule(evt Event, rule AutomationRule) {
	for _, cond := range rule.Conditions {
		if !s.matchCondition(cond, evt) {
			return
		}
	}
	// Cooldown throttle: without it a rule on agent.checkin fires on EVERY
	// beacon, and a task.complete+run_macro rule re-triggers itself forever
	// (macro step → new task → completion event → macro step ...). The
	// last-triggered stamp is persisted so restarts honor the window too.
	if rule.Cooldown > 0 && !rule.LastTriggered.IsZero() &&
		time.Since(rule.LastTriggered) < time.Duration(rule.Cooldown)*time.Second {
		return
	}
	for _, action := range rule.Actions {
		if err := s.executeAction(rule, action, evt); err != nil {
			recordRuleActionOutcome(rule.ID, false, err)
		} else if action.Type != ActionWebhook {
			// Webhook delivery is asynchronous; its goroutine records the
			// receiver's actual outcome instead of claiming success here.
			recordRuleActionOutcome(rule.ID, true, nil)
		}
	}
	// Stamp the firing unconditionally: run_count is operator-visible stats,
	// last_triggered powers the cooldown window above (survives restarts).
	s.persistScheduleState(rule.ID, map[string]interface{}{
		"last_triggered": time.Now(),
		"run_count":      gorm.Expr("run_count + 1"),
	})
}

// matchCondition reports whether one condition matches, failing CLOSED.
//
// Two defaults here used to fail open and are the reason this comment exists: an unrecognised operator returned true, and an empty field
// resolved to "" so `contains` matched everything. A misspelled operator therefore made a rule fire on every event of its type, which
// reads to an operator as "the rule works". Definitions are also refused at save time (validateRuleConditions); this is the second line,
// for rules stored before that check existed.
func (s *Server) matchCondition(cond RuleCondition, evt Event) bool {
	field := strings.TrimSpace(cond.Field)
	if field == "" {
		return false
	}
	var val string
	resolved := true
	switch field {
	case "agent.hostname":
		val = evt.AgentHost
	default:
		// An ABSENT key is not an empty value: with the value defaulting to "", `contains ""` matched every event that did not carry the
		// field at all, which is the same fail-open shape as an unknown operator.
		if v, ok := evt.Data[field]; ok {
			val = fmt.Sprintf("%v", v)
		} else {
			resolved = false
		}
	}
	if !resolved {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(cond.Operator)) {
	case "contains":
		return strings.Contains(strings.ToLower(val), strings.ToLower(cond.Value))
	case "equals":
		return val == cond.Value
	case "regex":
		// Compiled per evaluation: rules are few and events are not hot enough to justify a cache that would then need invalidation when
		// a rule is edited. A bad pattern is a false, not a panic and not a match.
		pattern, err := regexp.Compile(cond.Value)
		if err != nil {
			slog.Warn("Automation: condition has an invalid regex, treating it as non-matching", "pattern", cond.Value, "error", err)
			return false
		}
		return pattern.MatchString(val)
	default:
		return false
	}
}

// executeAction runs one action and reports the failures a rule owner needs to see.
//
// The return value covers DISPATCH failures: the task could not be created, the macro does not exist. Those are the ones
// that silently mean "the rule fired and nothing happened". Outcomes that happen later - a webhook answering 500 - are
// counted by the goroutine that learns about them, so this never claims success for something still in flight.
func (s *Server) authorizeAutomationAction(rule AutomationRule, action RuleAction) error {
	// Empty creators are legacy/internal rules from before attribution existed.
	// Operator-created rules are re-checked at fire time, so losing a
	// permission stops the action instead of escalating through automation.
	if strings.TrimSpace(rule.CreatedBy) == "" {
		return nil
	}
	permission := db.PermAutomationWrite
	switch action.Type {
	case ActionRunCommand, ActionCreateTask, ActionSetSleep, ActionRunMacro:
		permission = db.PermAgentsWrite
	case ActionNotify:
		permission = db.PermNotificationsWrite
	}
	allowed, err := middleware.UserHasPermission(s.db, rule.CreatedBy, permission)
	if err != nil || !allowed {
		return fmt.Errorf("automation action %s requires %s", action.Type, permission)
	}
	return nil
}

func (s *Server) executeAction(rule AutomationRule, action RuleAction, evt Event) error {
	if err := s.authorizeAutomationAction(rule, action); err != nil {
		return err
	}
	switch action.Type {
	case ActionRunCommand:
		var params struct {
			Command string `json:"command"`
			AgentID string `json:"agent_id"`
		}
		if err := json.Unmarshal(action.Params, &params); err != nil {
			return fmt.Errorf("run_command: %w", err)
		}
		targetAgent := s.automationTarget(evt, params.AgentID)
		if params.Command == "" || targetAgent == "" {
			return fmt.Errorf("run_command: command and a visible target agent are required")
		}
		expanded := s.expandTemplate(params.Command, evt)
		if _, err := s.createSystemTask(targetAgent, "shell", expanded, "", "pending", "automation"); err != nil {
			return fmt.Errorf("run_command: %w", err)
		}
	case ActionWebhook:
		var params struct {
			URL     string            `json:"url"`
			Method  string            `json:"method"`
			Headers map[string]string `json:"headers"`
			Secret  string            `json:"secret"`
		}
		if err := json.Unmarshal(action.Params, &params); err != nil {
			return fmt.Errorf("webhook: %w", err)
		}
		if params.URL == "" {
			return fmt.Errorf("webhook: url is required")
		}
		if err := webhookTargetValidator(params.URL); err != nil {
			return fmt.Errorf("webhook: %w", err)
		}
		go func() {
			defer func() {
				if r := recover(); r != nil {
					slog.Error("Automation: webhook goroutine panicked", "panic", r, "stack", string(debug.Stack()))
					recordRuleActionOutcome(rule.ID, false, fmt.Errorf("webhook panic: %v", r))
				}
			}()
			if err := s.executeWebhook(params, evt); err != nil {
				recordRuleActionOutcome(rule.ID, false, fmt.Errorf("webhook: %w", err))
			} else {
				recordRuleActionOutcome(rule.ID, true, nil)
			}
		}()
	case ActionNotify:
		var params struct {
			Message string `json:"message"`
			Channel string `json:"channel"`
		}
		if err := json.Unmarshal(action.Params, &params); err != nil {
			return fmt.Errorf("notify: %w", err)
		}
		if params.Message == "" {
			return fmt.Errorf("notify: message is required")
		}
		msg := s.expandTemplate(params.Message, evt)
		frame, ok := marshalJSONSafe(map[string]interface{}{"type": "notification", "message": msg, "source": "automation"})
		if !ok {
			return fmt.Errorf("notify: failed to marshal notification")
		}
		s.broadcastToClientsReliable(frame)
	case ActionRunScript:
		var params struct {
			ScriptID string `json:"script_id"`
			Code     string `json:"code"`
		}
		if err := json.Unmarshal(action.Params, &params); err != nil {
			return fmt.Errorf("script: %w", err)
		}
		if params.ScriptID == "" && params.Code == "" {
			return fmt.Errorf("script: script_id or code is required")
		}
		engine := scripting.GetEngine()
		dataCopy := make(map[string]interface{}, len(evt.Data))
		for k, v := range evt.Data {
			dataCopy[k] = v
		}
		scriptEvt := evt
		scriptEvt.Data = dataCopy
		context := map[string]interface{}{"event": scriptEvt, "agent_id": evt.AgentID}
		caller := scripting.Caller{Username: "automation", Role: db.RoleUser, TenantID: evt.TenantID}
		started := time.Now()
		var res scripting.ExecutionResult
		switch {
		case params.ScriptID != "":
			res = engine.Execute(params.ScriptID, context, caller)
		case params.Code != "":
			res = engine.ExecuteCode(params.Code, context, caller)
		}
		fields := []any{
			"script_id", params.ScriptID, "event", string(evt.Type), "agent_id", evt.AgentID,
			"tenant_id", evt.TenantID, "duration_ms", time.Since(started).Milliseconds(),
		}
		if res.Success {
			slog.Debug("Automation: script ran", fields...)
			return nil
		}
		slog.Warn("Automation: script failed", append(fields, "error", truncateString(res.Error, 300))...)
		return fmt.Errorf("script: %s", truncateString(res.Error, 300))
	case ActionCreateTask:
		var params struct {
			AgentID          string               `json:"agent_id"`
			Type             string               `json:"type"`
			Command          string               `json:"command"`
			CredentialID     *uint                `json:"credential_id,omitempty"`
			CredentialParams CredentialTaskParams `json:"credential_params,omitempty"`
		}
		if err := json.Unmarshal(action.Params, &params); err != nil {
			return fmt.Errorf("create_task: %w", err)
		}
		targetAgent := s.automationTarget(evt, params.AgentID)
		if targetAgent == "" || params.Type == "" {
			return fmt.Errorf("create_task: type and a visible target agent are required")
		}
		opts := []TaskOption{withSystemTaskMetadata("pending", "automation")}
		if params.CredentialID != nil && *params.CredentialID != 0 {
			allowed, permErr := middleware.UserHasPermission(s.db, rule.CreatedBy, db.PermCredsRead)
			if permErr != nil || !allowed {
				return fmt.Errorf("create_task: creator lacks credentials.read")
			}
			opts = append(opts, WithCredentialReference(*params.CredentialID, params.CredentialParams))
		}
		expanded := s.expandTemplate(params.Command, evt)
		if _, err := s.createTask(targetAgent, params.Type, expanded, "", "", "", 0, 0, opts...); err != nil {
			return fmt.Errorf("create_task: %w", err)
		}
	case ActionSetSleep:
		var params struct {
			AgentID  string `json:"agent_id"`
			Interval int    `json:"interval"`
			Jitter   int    `json:"jitter"`
		}
		if err := json.Unmarshal(action.Params, &params); err != nil {
			return fmt.Errorf("set_sleep: %w", err)
		}
		targetAgent := s.automationTarget(evt, params.AgentID)
		if targetAgent == "" || params.Interval <= 0 {
			return fmt.Errorf("set_sleep: a visible target agent and positive interval are required")
		}
		interval, jitter, clamped := s.clampSleepInts(params.Interval, params.Jitter)
		if clamped {
			s.LogAuditRecord(nil, "automation_sleep_clamped", "agent", targetAgent,
				"automation set_sleep raised to OPSEC floors", true, nil)
		}
		cmd := fmt.Sprintf("%d,%d", interval, jitter)
		if _, err := s.createSystemTask(targetAgent, "set_sleep", cmd, "", "pending", "automation"); err != nil {
			return fmt.Errorf("set_sleep: %w", err)
		}
	case ActionRunMacro:
		var params struct {
			MacroID     uint `json:"macro_id"`
			StopOnError bool `json:"stop_on_error"`
		}
		if err := json.Unmarshal(action.Params, &params); err != nil || params.MacroID == 0 {
			return fmt.Errorf("run_macro: valid macro_id is required")
		}
		var macro db.CommandMacro
		if err := s.db.First(&macro, params.MacroID).Error; err != nil {
			return fmt.Errorf("run_macro: macro %d not found", params.MacroID)
		}
		if evt.TenantID != 0 && macro.TenantID != evt.TenantID {
			s.LogAuditRecord(nil, "automation_blocked_cross_tenant", "macro", fmt.Sprint(params.MacroID),
				"run_macro referenced foreign-tenant macro", true, nil)
			return fmt.Errorf("run_macro: macro %d belongs to another tenant", params.MacroID)
		}
		targetAgent := evt.AgentID
		if targetAgent == "" {
			return fmt.Errorf("run_macro: event has no agent context")
		}
		macroCopy := macro
		if err := s.startMacroRun(&macroCopy, targetAgent, "automation", params.StopOnError); err != nil {
			return fmt.Errorf("run_macro: %w", err)
		}
		slog.Info("Automation: macro dispatched", "macro", macro.Name, "agent_id", targetAgent)
	default:
		return fmt.Errorf("unknown automation action type %q", action.Type)
	}
	return nil
}

// expandTemplate replaces template variables in a string with event data.
func (s *Server) expandTemplate(input string, evt Event) string {
	result := input
	result = strings.ReplaceAll(result, "{{agent_id}}", evt.AgentID)
	result = strings.ReplaceAll(result, "{{hostname}}", evt.AgentHost)
	result = strings.ReplaceAll(result, "{{ip}}", evt.AgentIP)
	result = strings.ReplaceAll(result, "{{os}}", evt.AgentOS)
	result = strings.ReplaceAll(result, "{{user}}", evt.User)
	result = strings.ReplaceAll(result, "{{event_type}}", string(evt.Type))
	return result
}

// executeWebhook sends a webhook request with optional HMAC signature.
func (s *Server) executeWebhook(params struct {
	URL     string            `json:"url"`
	Method  string            `json:"method"`
	Headers map[string]string `json:"headers"`
	Secret  string            `json:"secret"`
}, evt Event) error {
	if err := webhookTargetValidator(params.URL); err != nil {
		slog.Error("Automation: webhook URL rejected", "error", err)
		return err
	}
	body, err := json.Marshal(evt)
	if err != nil {
		slog.Error("Automation: marshal webhook body", "error", err)
		return err
	}
	method := params.Method
	if method == "" {
		method = "POST"
	}
	req, err := http.NewRequest(method, params.URL, bytes.NewReader(body))
	if err != nil {
		slog.Error("Automation: create webhook request", "error", err)
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Sovereign-Automation/1.0")
	for k, v := range params.Headers {
		req.Header.Set(k, v)
	}
	if params.Secret != "" {
		h := hmac.New(sha256.New, []byte(params.Secret))
		h.Write(body)
		req.Header.Set("X-Sovereign-Signature", hex.EncodeToString(h.Sum(nil)))
	}
	resp, err := ssrfSafeClient(s.httpClient).Do(req)
	if err != nil {
		slog.Error("Automation: webhook request failed", "error", deliveryErrorText(err))
		return fmt.Errorf("request failed: %s", deliveryErrorText(err))
	}
	defer resp.Body.Close()
	return logWebhookResponse(params.URL, evt, resp)
}

// logWebhookResponse records what the receiver said.
//
// The status code was never inspected, so a webhook endpoint answering 500 or 404 was treated exactly
// like a 200: the body - which is where a receiver puts "signature mismatch" or "unknown event" - went
// unread. A rule pointing at a receiver that had moved or started rejecting was silently dead.
func logWebhookResponse(targetURL string, evt Event, resp *http.Response) error {
	if resp == nil {
		return fmt.Errorf("webhook returned no response")
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	host := targetURL
	if u, err := url.Parse(targetURL); err == nil && u.Host != "" {
		host = u.Host
	}
	fields := []any{"host", host, "event", string(evt.Type), "status", resp.StatusCode}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		slog.Debug("Automation: webhook delivered", fields...)
		return nil
	}
	slog.Warn("Automation: webhook rejected by the receiver",
		append(fields, "body", truncateString(strings.TrimSpace(string(body)), 200))...)
	return fmt.Errorf("receiver returned HTTP %d", resp.StatusCode)
}

func (s *Server) loadAutomationRules() []AutomationRule {
	s.automationRulesMu.RLock()
	cached := s.automationRules
	age := time.Since(s.automationRulesAt)
	s.automationRulesMu.RUnlock()
	if cached != nil && age < 30*time.Second {
		return cached
	}

	var dbRules []db.AutomationRule
	if err := s.db.Limit(AutomationRuleLimit).Find(&dbRules).Error; err != nil {
		slog.Error("Automation: failed to load rules", "error", err)
		// Do NOT cache the empty result: a transient SQLite lock would
		// otherwise blind all event automation for the full 30s TTL. Return
		// the previous snapshot when one exists.
		s.automationRulesMu.RLock()
		stale := s.automationRules
		s.automationRulesMu.RUnlock()
		return stale
	}

	var rules []AutomationRule
	for _, dr := range dbRules {
		var conditions []RuleCondition
		if dr.Conditions != "" {
			if err := json.Unmarshal([]byte(dr.Conditions), &conditions); err != nil {
				// Corrupt conditions must NOT degrade to match-all: evaluateRule
				// treats a nil condition list as pass-through, so a partially
				// corrupt row would fire every action on every event. Skip it.
				slog.Warn("Automation: skipping rule with corrupt conditions", "rule", dr.Name, "error", err)
				continue
			}
		}
		var actions []RuleAction
		if dr.Actions != "" {
			if err := json.Unmarshal([]byte(dr.Actions), &actions); err != nil {
				slog.Warn("Automation: unmarshal actions", "rule", dr.Name, "error", err)
			}
		}
		if len(actions) == 0 {
			continue // nothing to do; don't even keep it for schedule state churn
		}
		rules = append(rules, AutomationRule{
			ID:            dr.ID,
			Name:          dr.Name,
			Enabled:       dr.Enabled,
			EventType:     dr.EventType,
			Conditions:    conditions,
			Actions:       actions,
			Cooldown:      dr.CooldownSeconds,
			Schedule:      dr.Schedule,
			AgentID:       dr.AgentID,
			TaskType:      dr.TaskType,
			Command:       dr.Command,
			Params:        dr.Params,
			LastRun:       dr.LastRun,
			NextRun:       dr.NextRun,
			RunCount:      dr.RunCount,
			LastTriggered: dr.LastTriggered,
			CreatedBy:     dr.CreatedBy,
			TenantID:      dr.TenantID,
			CreatedAt:     dr.CreatedAt.Format(time.RFC3339),
		})
	}

	s.automationRulesMu.Lock()
	s.automationRules = rules
	s.automationRulesAt = time.Now()
	s.automationRulesMu.Unlock()

	return rules
}

func (s *Server) invalidateAutomationCache() {
	s.automationRulesMu.Lock()
	s.automationRules = nil
	s.automationRulesAt = time.Time{}
	s.automationRulesMu.Unlock()
}

func (s *Server) saveAutomationRule(rule AutomationRule) error {
	conditionsData, err := json.Marshal(rule.Conditions)
	if err != nil {
		return fmt.Errorf("marshal conditions: %w", err)
	}
	actionsData, err := json.Marshal(rule.Actions)
	if err != nil {
		return fmt.Errorf("marshal actions: %w", err)
	}

	dbRule := db.AutomationRule{
		ID:              rule.ID,
		Name:            rule.Name,
		Enabled:         rule.Enabled,
		EventType:       rule.EventType,
		Conditions:      string(conditionsData),
		Actions:         string(actionsData),
		Schedule:        rule.Schedule,
		AgentID:         rule.AgentID,
		TaskType:        rule.TaskType,
		Command:         rule.Command,
		Params:          rule.Params,
		LastRun:         rule.LastRun,
		NextRun:         rule.NextRun,
		RunCount:        rule.RunCount,
		CooldownSeconds: rule.Cooldown,
		CreatedBy:       rule.CreatedBy,
		TenantID:        rule.TenantID,
	}
	if rule.LastTriggered.After(dbRule.LastTriggered) {
		dbRule.LastTriggered = rule.LastTriggered
	}

	if rule.CreatedAt != "" {
		if t, err := time.Parse(time.RFC3339, rule.CreatedAt); err == nil {
			dbRule.CreatedAt = t
		}
	}
	// Save() writes every column: when the caller's snapshot carries no
	// last_triggered (e.g. the toggle handler re-saving a cached rule),
	// carry the persisted stamp over so a toggle never resets an active
	// cooldown window.
	if rule.LastTriggered.IsZero() {
		var existing db.AutomationRule
		if err := s.db.Select("last_triggered").First(&existing, "id = ?", rule.ID).Error; err == nil {
			dbRule.LastTriggered = existing.LastTriggered
		}
	}

	if err := s.db.Save(&dbRule).Error; err != nil {
		return err
	}
	// Save() inserts when the row is new, and INSERT applies GORM defaults: the column carries
	// `default:true`, so a rule created DISABLED came back ENABLED and started firing on its own. Writing
	// the column explicitly after the insert is what persists the caller value - found by the automation
	// preview test, which asserted that a disabled rule would not run.
	if err := s.db.Model(&db.AutomationRule{}).Where("id = ?", dbRule.ID).
		Update("enabled", rule.Enabled).Error; err != nil {
		return err
	}
	s.invalidateAutomationCache()
	return nil
}

func (s *Server) deleteAutomationRule(id string) error {
	err := s.db.Delete(&db.AutomationRule{}, "id = ?", id).Error
	if err == nil {
		s.invalidateAutomationCache()
	}
	return err
}

func (s *Server) migrateAutomationRules() {
	var count int64
	if err := s.db.Model(&db.AutomationRule{}).Count(&count).Error; err != nil {
		slog.Error("Automation: failed to count rules", "error", err)
	}
	if count > 0 {
		return
	}

	raw := s.getConfigJSON("automation_rules")
	if raw == "" {
		return
	}

	var rules []AutomationRule
	if err := json.Unmarshal([]byte(raw), &rules); err != nil {
		return
	}

	for _, rule := range rules {
		if err := s.saveAutomationRule(rule); err != nil {
			slog.Warn("Automation: failed to import rule", "id", rule.ID, "error", err)
		}
	}

	if err := s.db.Where("key = ?", "automation_rules").Delete(&db.ServerConfig{}).Error; err != nil {
		slog.Error("Automation: failed to delete legacy config", "error", err)
	}
}

func (s *Server) getConfigJSON(key string) string {
	var cfg struct{ Value string }
	if err := s.db.Model(&db.ServerConfig{}).Where("key = ?", key).First(&cfg).Error; err != nil {
		slog.Debug("Config key not found", "key", key, "error", err)
	}
	return cfg.Value
}

func (s *Server) registerBuiltinAutomations() {
	rule := AutomationRule{
		ID:        "auto_dc_alert",
		Name:      "DC Login Alert",
		Enabled:   true,
		EventType: string(EventImplantCheckin),
		Conditions: []RuleCondition{
			{Field: "agent.hostname", Operator: "contains", Value: "DC"},
		},
		Actions: []RuleAction{
			{
				Type:   "command",
				Params: json.RawMessage(`{"command": "ldap_users"}`),
			},
			{
				Type:   "webhook",
				Params: json.RawMessage(`{"url": "", "method": "POST"}`),
			},
		},
		CreatedAt: time.Now().Format(time.RFC3339),
	}
	// auto_baseline_collection: on every check-in, queue a bounded "hostinfo"
	// collection so a freshly-checked-in agent gets a structured fingerprint
	// without an operator manually issuing it. This mirrors the in-the-wild
	// "collect on each interaction" behaviour (火绒 2060) but stays quiet:
	// hostinfo is an allowlisted, non-shell typed task, and the rule is created
	// DISABLED by default so an operator opts in deliberately.
	baseline := AutomationRule{
		ID:        "auto_baseline_collection",
		Name:      "Baseline collection on check-in",
		Enabled:   false,
		EventType: string(EventImplantCheckin),
		Actions: []RuleAction{
			{
				Type:   ActionCreateTask,
				Params: json.RawMessage(`{"type": "hostinfo", "command": ""}`),
			},
		},
		CreatedAt: time.Now().Format(time.RFC3339),
	}
	rules := s.loadAutomationRules()
	for _, seed := range []AutomationRule{rule, baseline} {
		exists := false
		for _, r := range rules {
			if r.ID == seed.ID {
				exists = true
				break
			}
		}
		if !exists {
			if err := s.saveAutomationRule(seed); err != nil {
				slog.Warn("Automation: failed to register builtin rule", "id", seed.ID, "error", err)
			}
		}
	}
}

// persistScheduleState writes ONLY the scheduler bookkeeping columns for a
// rule. The previous full-row saveAutomationRule() path rewrote command/
// actions/enabled from a cache snapshot up to 30s stale, silently reverting
// concurrent operator edits.
func (s *Server) persistScheduleState(ruleID string, updates map[string]interface{}) {
	if len(updates) == 0 {
		return
	}
	if err := s.db.Model(&db.AutomationRule{}).Where("id = ?", ruleID).Updates(updates).Error; err != nil {
		slog.Error("Automation: persist schedule state failed", "id", ruleID, "error", err)
	}
}

// schedulerLoop periodically dispatches schedule-driven automation rules
// (event_type="schedule"). It runs every 30s and only touches rules whose
// next_run is due, so interval and cron expressions both work.
func (s *Server) schedulerLoop() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
			s.dispatchScheduledRules()
		}
	}
}

func (s *Server) dispatchScheduledRules() {
	now := time.Now()
	rules := s.loadAutomationRules()
	for _, rule := range rules {
		if rule.EventType != EventSchedule || !rule.Enabled || strings.TrimSpace(rule.Schedule) == "" {
			continue
		}
		next := rule.NextRun
		if next.IsZero() {
			computed, err := nextScheduleTime(rule.Schedule, now)
			if err != nil {
				slog.Warn("Automation: bad schedule, disabling rule", "rule", rule.Name, "schedule", rule.Schedule, "error", err)
				continue
			}
			rule.NextRun = computed
			// Targeted write: a full-row Save here rewrote the rule from a
			// possibly stale cache snapshot and reverted operator edits.
			s.persistScheduleState(rule.ID, map[string]interface{}{"next_run": computed})
			continue
		}
		if next.After(now) {
			continue
		}

		evt := Event{
			Type:    EventSchedule,
			AgentID: rule.AgentID,
			// Targeted schedules inherit the rule's tenant; agent-less
			// schedules stay legacy 0 (ruleMayFireOn already gates firing).
			TenantID: rule.TenantID,
		}
		if rule.AgentID != "" {
			var implant db.Implant
			if err := s.db.First(&implant, "id = ?", rule.AgentID).Error; err == nil {
				evt.AgentHost = implant.Hostname
				evt.AgentIP = implant.IP
				evt.AgentOS = implant.OS
				evt.User = implant.Username
			}
		}
		// Tenant gate: a rule may only fire for events attributed to its
		// own tenant's agents. Agent-less schedule rules fire only when
		// legacy (tenant 0) — they cannot be attributed to any tenant.
		if !s.ruleMayFireOn(rule.TenantID, evt.AgentID) {
			continue
		}
		s.evaluateRule(evt, rule)

		rule.LastRun = now
		rule.RunCount++
		computed, err := nextScheduleTime(rule.Schedule, now)
		stateUpdates := map[string]interface{}{
			"last_run":  now,
			"run_count": rule.RunCount,
			"next_run":  computed,
		}
		if err != nil {
			slog.Warn("Automation: schedule broken after run, disabling rule", "rule", rule.Name, "error", err)
			rule.Enabled = false
			stateUpdates["enabled"] = false
			stateUpdates["next_run"] = time.Time{}
		}
		s.persistScheduleState(rule.ID, stateUpdates)
	}
}

// nextScheduleTime computes the next occurrence of schedule after `from`.
// Supported formats (case-insensitive):
//
//	"every N minutes" | "every N hours" | "hourly" | "daily HH:MM" |
//	"weekly DOW HH:MM" (DOW: mon..sun) | 5-field cron "m h dom mon dow"
func nextScheduleTime(schedule string, from time.Time) (time.Time, error) {
	s := strings.ToLower(strings.TrimSpace(schedule))
	if s == "" {
		return time.Time{}, fmt.Errorf("empty schedule")
	}

	if strings.HasPrefix(s, "every ") {
		var n int
		var unit string
		if _, err := fmt.Sscanf(strings.TrimPrefix(s, "every "), "%d %s", &n, &unit); err != nil {
			return time.Time{}, fmt.Errorf("invalid interval: %q", schedule)
		}
		unit = strings.TrimSuffix(unit, "s")
		switch unit {
		case "minute", "min":
			if n < 1 {
				return time.Time{}, fmt.Errorf("interval must be >= 1: %q", schedule)
			}
			return from.Add(time.Duration(n) * time.Minute), nil
		case "hour":
			if n < 1 {
				return time.Time{}, fmt.Errorf("interval must be >= 1: %q", schedule)
			}
			return from.Add(time.Duration(n) * time.Hour), nil
		default:
			return time.Time{}, fmt.Errorf("unsupported interval unit: %q", unit)
		}
	}

	if s == "hourly" {
		return from.Add(time.Hour), nil
	}

	if strings.HasPrefix(s, "daily ") {
		hhmm := strings.TrimSpace(strings.TrimPrefix(s, "daily "))
		next, err := nextDailyTime(hhmm, from)
		if err != nil {
			return time.Time{}, fmt.Errorf("invalid daily schedule: %q", schedule)
		}
		return next, nil
	}

	if strings.HasPrefix(s, "weekly ") {
		rest := strings.TrimSpace(strings.TrimPrefix(s, "weekly "))
		parts := strings.Fields(rest)
		if len(parts) != 2 {
			return time.Time{}, fmt.Errorf("weekly schedule needs 'DOW HH:MM': %q", schedule)
		}
		dow, ok := weekdayIndex(parts[0])
		if !ok {
			return time.Time{}, fmt.Errorf("unknown weekday: %q", parts[0])
		}
		next, err := nextDailyTime(parts[1], from)
		if err != nil {
			return time.Time{}, fmt.Errorf("invalid weekly schedule: %q", schedule)
		}
		for int(next.Weekday()) != dow {
			next = next.Add(24 * time.Hour)
		}
		return next, nil
	}

	// Fallback: standard 5-field cron expression.
	next, err := nextCronTime(s, from)
	if err != nil {
		return time.Time{}, fmt.Errorf("unsupported schedule: %q", schedule)
	}
	return next, nil
}

func nextDailyTime(hhmm string, from time.Time) (time.Time, error) {
	var h, m int
	if _, err := fmt.Sscanf(hhmm, "%d:%d", &h, &m); err != nil || h < 0 || h > 23 || m < 0 || m > 59 {
		return time.Time{}, fmt.Errorf("invalid time %q", hhmm)
	}
	candidate := time.Date(from.Year(), from.Month(), from.Day(), h, m, 0, 0, from.Location())
	if !candidate.After(from) {
		candidate = candidate.Add(24 * time.Hour)
	}
	return candidate, nil
}

func weekdayIndex(dow string) (int, bool) {
	names := []string{"sun", "mon", "tue", "wed", "thu", "fri", "sat"}
	for i, n := range names {
		if dow == n {
			return i, true
		}
	}
	return 0, false
}

type cronField struct {
	raw     string
	allowed map[int]bool
	step    int
}

func parseCronField(raw string, min, max int) (cronField, error) {
	f := cronField{raw: raw, step: 1, allowed: map[int]bool{}}
	if raw == "*" {
		for v := min; v <= max; v++ {
			f.allowed[v] = true
		}
		return f, nil
	}
	// step suffix */n or a-b/n
	step := 1
	stepPart := ""
	if idx := strings.IndexByte(raw, '/'); idx >= 0 {
		stepPart = raw[idx+1:]
		raw = raw[:idx]
		fmt.Sscanf(stepPart, "%d", &step)
		if step < 1 {
			step = 1
		}
		f.step = step
	}
	expand := func(from, to int) {
		for v := from; v <= to; v += step {
			f.allowed[v] = true
		}
	}
	for _, part := range strings.Split(raw, ",") {
		if part == "*" {
			for v := min; v <= max; v += step {
				f.allowed[v] = true
			}
			continue
		}
		if idx := strings.IndexByte(part, '-'); idx >= 0 {
			var a, b int
			if _, err := fmt.Sscanf(part, "%d-%d", &a, &b); err != nil || a < min || b > max || a > b {
				return f, fmt.Errorf("bad cron range %q", part)
			}
			expand(a, b)
			continue
		}
		var v int
		if _, err := fmt.Sscanf(part, "%d", &v); err != nil || v < min || v > max {
			return f, fmt.Errorf("bad cron value %q", part)
		}
		expand(v, v)
	}
	return f, nil
}

func cronMatches(field cronField, v int) bool {
	return field.allowed[v]
}

// expandWeekdayNames rewrites sun..sat to 0..6 inside a day-of-week cron
// field so the numeric parser can handle it.
func expandWeekdayNames(dow string) string {
	names := []string{"sun", "mon", "tue", "wed", "thu", "fri", "sat"}
	for i, n := range names {
		dow = strings.ReplaceAll(dow, n, fmt.Sprintf("%d", i))
	}
	return dow
}

func nextCronTime(expr string, from time.Time) (time.Time, error) {
	fields := strings.Fields(expr)
	if len(fields) != 5 {
		return time.Time{}, fmt.Errorf("cron must have 5 fields")
	}
	minuteF, err := parseCronField(fields[0], 0, 59)
	if err != nil {
		return time.Time{}, err
	}
	hourF, err := parseCronField(fields[1], 0, 23)
	if err != nil {
		return time.Time{}, err
	}
	domF, err := parseCronField(fields[2], 1, 31)
	if err != nil {
		return time.Time{}, err
	}
	monF, err := parseCronField(fields[3], 1, 12)
	if err != nil {
		return time.Time{}, err
	}
	dowF, err := parseCronField(expandWeekdayNames(fields[4]), 0, 6)
	if err != nil {
		return time.Time{}, err
	}

	t := from.Add(time.Minute).Truncate(time.Minute)
	// Standard cron semantics: if BOTH day-of-month and day-of-week are
	// restricted (non-`*`), a match on EITHER field satisfies the schedule.
	// Using AND here would make e.g. "0 9 15 * mon" never fire unless the
	// 15th happens to be a Monday.
	domWild := fields[2] == "*"
	dowWild := fields[4] == "*"
	for i := 0; i < 366*25; i++ {
		domOK := cronMatches(domF, t.Day())
		dowOK := cronMatches(dowF, int(t.Weekday()))
		dayOK := domOK
		if !domWild || !dowWild {
			dayOK = domOK || dowOK
		}
		if cronMatches(monF, int(t.Month())) && dayOK &&
			cronMatches(hourF, t.Hour()) && cronMatches(minuteF, t.Minute()) {
			return t, nil
		}
		t = t.Add(time.Minute)
	}
	return time.Time{}, fmt.Errorf("no cron match found")
}
