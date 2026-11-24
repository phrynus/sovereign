package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/r0si/sovereign/internal/crypto"
	"github.com/r0si/sovereign/internal/db"
	"github.com/r0si/sovereign/internal/server/middleware"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// CredentialTaskParams holds the NON-sensitive inputs of a vault-backed task.
// It is stored on the task (encrypted) so a rerun can resolve the reference
// again without the operator resubmitting anything.
type CredentialTaskParams struct {
	DC        string `json:"dc,omitempty"`
	DelayMS   string `json:"delay_ms,omitempty"`
	Usernames string `json:"usernames,omitempty"`
	LogonType string `json:"logon_type,omitempty"`
	Target    string `json:"target,omitempty"`
	Method    string `json:"method,omitempty"`
	Command   string `json:"command,omitempty"`
}

func encodeCredentialParams(p CredentialTaskParams) string {
	raw, err := json.Marshal(p)
	if err != nil {
		return "{}"
	}
	return string(raw)
}

func decodeCredentialParams(raw string) (CredentialTaskParams, error) {
	var p CredentialTaskParams
	if strings.TrimSpace(raw) == "" {
		return p, nil
	}
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		return p, fmt.Errorf("invalid credential_params: %w", err)
	}
	return p, nil
}

var (
	// errCredentialSnapshot is returned when the referenced vault entry can no
	// longer be used (deleted, moved, rotated past its fingerprint, expired or
	// unreadable). Approval maps it to 409 and fails the task.
	errCredentialSnapshot = errors.New("credential snapshot is no longer valid")
	// errCredentialReferenceConflict reports an idempotency key reused for a
	// different reference; silently returning the old task would run the wrong
	// credential.
	errCredentialReferenceConflict = errors.New("idempotency key belongs to a different credential reference")
)

// credentialReferenceTaskTypes is deliberately small: these four wire formats
// are the only ones the resolver knows how to materialize. Extending it means
// writing the materializer AND its separator validation, not just adding a
// name here.
func credentialReferenceTaskTypes(taskType string) bool {
	switch taskType {
	case "cred_check", "password_spray", "token_make", "lateral":
		return true
	default:
		return false
	}
}

// prepareCredentialTask resolves a vault id to the exact wire arguments an
// implant expects. It never returns the parsed secret to an HTTP handler for
// display: callers pass the result into createTask, which stores only the
// reference and keeps the resolved values in encrypted columns.
func (s *Server) prepareCredentialTask(agentID, taskType string, ref credentialTaskReference) (command, shell, path, data, fingerprint, label string, err error) {
	if !credentialReferenceTaskTypes(taskType) {
		return "", "", "", "", "", "", fmt.Errorf("credential references are not supported for task type %s", taskType)
	}
	if ref.ID == 0 {
		return "", "", "", "", "", "", fmt.Errorf("credential_id must be a positive integer")
	}
	params, err := decodeCredentialParams(ref.Params)
	if err != nil {
		return "", "", "", "", "", "", err
	}

	var agent db.Implant
	if err := s.db.Select("tenant_id").Where("id = ?", agentID).First(&agent).Error; err != nil {
		return "", "", "", "", "", "", fmt.Errorf("agent not found: %w", err)
	}
	var cred db.CredentialEntry
	if err := s.db.Where("id = ? AND tenant_id = ?", ref.ID, agent.TenantID).First(&cred).Error; err != nil {
		return "", "", "", "", "", "", fmt.Errorf("credential %d not found in tenant", ref.ID)
	}
	// AfterFind decrypts these fields. A leftover FC2ENC value means the vault
	// key is unavailable; never dispatch undecryptable material.
	if strings.HasPrefix(cred.Password, "FC2ENC:") || strings.HasPrefix(cred.Hash, "FC2ENC:") {
		return "", "", "", "", "", "", fmt.Errorf("credential snapshot is unreadable")
	}
	credType := strings.ToLower(strings.TrimSpace(cred.Type))
	if credType != "cleartext" && credType != "password" {
		return "", "", "", "", "", "", fmt.Errorf("credential type %q cannot be used for %s", cred.Type, taskType)
	}
	if cred.Password == "" {
		return "", "", "", "", "", "", fmt.Errorf("credential has no password (hash-only entries cannot be used for %s)", taskType)
	}
	if cred.ExpiryKnown && !cred.ExpiresAt.IsZero() && !cred.ExpiresAt.After(time.Now()) {
		return "", "", "", "", "", "", fmt.Errorf("credential has expired")
	}

	user := strings.TrimSpace(cred.Username)
	domain := strings.TrimSpace(cred.Domain)
	password := cred.Password
	dc := strings.TrimSpace(params.DC)

	// Every field joined with "|" must be checked before concatenation: the
	// implant splits on "|", so a separator inside one field shifts all later
	// fields and the task silently means something else.
	for name, value := range map[string]string{
		"username": user, "domain": domain, "password": password,
		"dc": dc, "delay_ms": params.DelayMS,
		"target": params.Target, "method": params.Method,
	} {
		if strings.Contains(value, "|") {
			return "", "", "", "", "", "", fmt.Errorf("%s must not contain the '|' separator", name)
		}
	}

	fingerprint = cred.DedupKey
	if fingerprint == "" {
		fingerprint = db.ComputeCredDedupKey(cred.AgentID, cred.Domain, cred.Username, cred.Password, cred.Hash)
	}
	label = user
	if domain != "" {
		label = domain + "\\" + user
	}
	if cred.Subtype != "" {
		label += " (" + cred.Subtype + ")"
	}

	if taskType == "cred_check" && s.credCheckFuse != nil && s.credCheckFuse.tripped(agentID, domain) {
		return "", "", "", "", "", "", fmt.Errorf("fuse_tripped")
	}
	switch taskType {
	case "cred_check":
		return strings.Join([]string{user, domain, password, dc}, "|"), "", "", "", fingerprint, label, nil
	case "password_spray":
		delay := params.DelayMS
		if delay == "" {
			delay = "0"
		}
		return strings.Join([]string{password, domain, dc, delay}, "|"), "", "", params.Usernames, fingerprint, label, nil
	case "token_make":
		principal := user
		if domain != "" {
			principal = domain + "\\" + user
		}
		logonType := params.LogonType
		if logonType == "" {
			logonType = "interactive"
		}
		return principal, password, logonType, "", fingerprint, label, nil
	case "lateral":
		spec, err := encodeLateralSpec(lateralSpec{
			Method: params.Method, Target: params.Target,
			Username: user, Password: password, Command: params.Command,
		})
		if err != nil {
			return "", "", "", "", "", "", err
		}
		return spec, "", "", "", fingerprint, label, nil
	}
	return "", "", "", "", "", "", fmt.Errorf("credential references are not supported for task type %s", taskType)
}

// credentialTaskReference is the resolved material passed from a TaskOption
// into createTask. Params is a JSON object of non-sensitive inputs.
type credentialTaskReference struct {
	ID          uint
	Params      string
	Command     string
	Shell       string
	Path        string
	Data        string
	Fingerprint string
	Label       string
}

// taskWireArgs returns the arguments an implant actually executes. Referenced
// tasks keep them in encrypted Resolved* columns; everything else uses the
// ordinary Command/Shell pair. Decryption failures fail closed.
func taskWireArgs(task db.Task) (command, shell string, err error) {
	if task.CredentialID == nil || *task.CredentialID == 0 {
		return task.Command, task.Shell, nil
	}
	for _, field := range []*string{&task.ResolvedCommand, &task.ResolvedShell} {
		if strings.HasPrefix(*field, "FC2ENC:") {
			plain, derr := crypto.DecryptLoot(*field)
			if derr != nil {
				return "", "", fmt.Errorf("cannot decrypt resolved credential arguments: %w", derr)
			}
			*field = plain
		}
	}
	if task.ResolvedCommand == "" {
		return "", "", fmt.Errorf("credential reference task has no resolved arguments")
	}
	return task.ResolvedCommand, task.ResolvedShell, nil
}

// validateCredentialSnapshot re-checks the referenced credential before a
// task becomes executable. It takes an explicit DB handle so callers inside a
// transaction use that transaction's (locked) view of the row.
func validateCredentialSnapshot(database *gorm.DB, task db.Task) error {
	if task.CredentialID == nil || *task.CredentialID == 0 {
		return nil
	}
	var cred db.CredentialEntry
	if err := database.Where("id = ? AND tenant_id = ?", *task.CredentialID, task.TenantID).First(&cred).Error; err != nil {
		return errCredentialSnapshot
	}
	fingerprint := cred.DedupKey
	if fingerprint == "" {
		fingerprint = db.ComputeCredDedupKey(cred.AgentID, cred.Domain, cred.Username, cred.Password, cred.Hash)
	}
	if fingerprint != task.CredentialFingerprint {
		return errCredentialSnapshot
	}
	if strings.HasPrefix(cred.Password, "FC2ENC:") || strings.HasPrefix(cred.Hash, "FC2ENC:") {
		return errCredentialSnapshot
	}
	if cred.ExpiryKnown && !cred.ExpiresAt.IsZero() && !cred.ExpiresAt.After(time.Now()) {
		return errCredentialSnapshot
	}
	return nil
}

// failCredentialSnapshot moves every non-terminal task using a stale snapshot
// to failed. The conditional UPDATE makes the pending-slot release happen at
// most once even when approval and beacon claim race.
func (s *Server) failCredentialSnapshot(task db.Task) bool {
	res := s.db.Model(&db.Task{}).
		Where("id = ? AND status IN ?", task.ID, []string{"pending", TaskStatusPendingApproval, "running"}).
		Updates(map[string]interface{}{
			"status": "failed",
			"error":  errCredentialSnapshot.Error(),
		})
	if res.Error != nil {
		slog.Error("Failed to invalidate stale credential snapshot", "task_id", task.ID, "err", res.Error)
		return false
	}
	if res.RowsAffected == 0 {
		return false
	}
	s.decPendingTasks(task.AgentID)
	task.Status = "failed"
	task.Error = errCredentialSnapshot.Error()
	s.broadcastTaskUpdate(task.AgentID, task)
	s.LogAuditRecord(nil, "credential_snapshot_invalid", "agent", task.AgentID,
		fmt.Sprintf("task #%d failed: credential snapshot no longer valid", task.ID), false, nil)
	return true
}

// recordCredentialUsageOnce appends one ledger row per (credential, task).
// The partial unique index in the schema makes concurrent and late duplicates
// harmless; ON CONFLICT DO NOTHING keeps that a no-op instead of an error.
func (s *Server) recordCredentialUsageOnce(task db.Task, action, result, detail string) error {
	if task.CredentialID == nil || *task.CredentialID == 0 {
		return nil
	}
	var existing int64
	if err := s.db.Model(&db.CredentialUsage{}).
		Where("credential_id = ? AND task_id = ?", *task.CredentialID, task.ID).Count(&existing).Error; err == nil && existing > 0 {
		return nil
	}
	entry := db.CredentialUsage{
		CredentialID: *task.CredentialID,
		TaskID:       task.ID,
		AgentID:      task.AgentID,
		Action:       action,
		Result:       result,
		Detail:       detail,
		Operator:     task.CreatedBy,
	}
	if err := s.db.Clauses(clause.OnConflict{DoNothing: true}).Create(&entry).Error; err != nil {
		s.LogAuditRecord(nil, "credential_usage_record_failed", "credential", task.AgentID,
			fmt.Sprintf("task #%d: %v", task.ID, err), false, err)
		return err
	}
	return nil
}

// credentialUsageForResult maps an agent result to a ledger action/result.
func credentialUsageForResult(taskType, errorText, output string) (action, result string) {
	switch taskType {
	case "cred_check":
		action = "verify"
	case "password_spray":
		action = "spray"
	case "lateral":
		action = "lateral"
	case "token_make":
		action = "manual"
	default:
		return "", ""
	}
	switch {
	case strings.Contains(strings.ToLower(errorText), "locked"):
		result = "locked"
	case errorText != "":
		result = "fail"
	case strings.Contains(strings.ToLower(output), `"status":"valid"`),
		strings.Contains(strings.ToLower(output), "success"):
		result = "ok"
	default:
		result = "unknown"
	}
	return action, result
}

// requireCredentialReferencePermission is the handler-level gate for every
// task creation path that accepts credential_id: route middleware may only
// require tasks.write, but referencing vault material also requires read
// access to the vault.
func requireCredentialReferencePermission(c *gin.Context, credentialID *uint) bool {
	if credentialID == nil || *credentialID == 0 {
		return true
	}
	return middleware.HasPermission(c, db.PermCredsRead)
}

// credentialReferenceOptionsStatus maps an expiry to a UI-safe state.
func credentialReferenceOptionsStatus(cred db.CredentialEntry, now time.Time) string {
	if !cred.ExpiryKnown || cred.ExpiresAt.IsZero() {
		return "unknown"
	}
	if cred.ExpiresAt.Before(now) {
		return "expired"
	}
	if !cred.ExpiresAt.After(now.Add(7 * 24 * time.Hour)) {
		return "expiring"
	}
	return "valid"
}

// handleCredentialOptions serves the picker without exposing secret material.
// GET /api/credentials/options?q=&limit=100
func (s *Server) handleCredentialOptions(c *gin.Context) {
	limit := 100
	if raw := c.Query("limit"); raw != "" {
		if n, err := strconvAtoi(raw); err == nil && n > 0 && n <= 500 {
			limit = n
		}
	}
	q := strings.TrimSpace(c.Query("q"))
	query := s.tenantScope(s.db.Model(&db.CredentialEntry{}), c)
	if q != "" {
		like := "%" + q + "%"
		query = query.Where("username LIKE ? OR domain LIKE ? OR subtype LIKE ?", like, like, like)
	}
	var creds []db.CredentialEntry
	if err := query.Order("id DESC").Limit(limit).Find(&creds).Error; err != nil {
		respondError(c, http.StatusInternalServerError, "failed to list credentials")
		return
	}
	now := time.Now()
	options := make([]gin.H, 0, len(creds))
	for _, cred := range creds {
		label := cred.Username
		if cred.Domain != "" {
			label = cred.Domain + "\\" + cred.Username
		}
		options = append(options, gin.H{
			"id":           cred.ID,
			"label":        label,
			"domain":       cred.Domain,
			"username":     cred.Username,
			"type":         cred.Type,
			"subtype":      cred.Subtype,
			"expiry_state": credentialReferenceOptionsStatus(cred, now),
			"expires_at":   credentialOptionExpiry(cred),
		})
	}
	respond(c, gin.H{"options": options})
}

func credentialOptionExpiry(cred db.CredentialEntry) any {
	if !cred.ExpiryKnown || cred.ExpiresAt.IsZero() {
		return nil
	}
	return cred.ExpiresAt.UTC().Format(time.RFC3339)
}

func strconvAtoi(raw string) (int, error) {
	n := 0
	for _, r := range raw {
		if r < '0' || r > '9' {
			return 0, fmt.Errorf("not a number")
		}
		n = n*10 + int(r-'0')
	}
	return n, nil
}

// validateCredentialStepReferences gates workflow saves: a step may carry
// only a reference the operator can read in the caller's tenant, and never an
// inline secret alongside it. Execution re-checks the creator's current
// authority and re-resolves the vault row.
func (s *Server) validateCredentialStepReferences(c *gin.Context, steps []db.WorkflowStep) bool {
	for _, step := range steps {
		if step.CredentialID == nil || *step.CredentialID == 0 {
			continue
		}
		if !requireCredentialReferencePermission(c, step.CredentialID) {
			respondError(c, http.StatusForbidden, "credentials.read is required to reference vault entries")
			return false
		}
		if !credentialReferenceTaskTypes(step.TaskType) || step.Command != "" {
			respondError(c, http.StatusBadRequest, "invalid credential reference in workflow step")
			return false
		}
		var count int64
		if err := s.tenantScope(s.db.Model(&db.CredentialEntry{}), c).
			Where("id = ?", *step.CredentialID).Count(&count).Error; err != nil || count != 1 {
			respondError(c, http.StatusNotFound, "credential not found in tenant")
			return false
		}
	}
	return true
}
