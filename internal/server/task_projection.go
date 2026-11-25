package server

import (
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/r0si/sovereign/internal/crypto"
	"github.com/r0si/sovereign/internal/db"
	"github.com/r0si/sovereign/internal/server/middleware"
	"github.com/r0si/sovereign/pkg/protocol"
)

// taskSummary is the only task shape exposed to operators without tasks.read.
// Keep this deliberately independent from db.Task so adding a storage field can
// never accidentally widen the redacted response.
type taskSummary struct {
	ID        uint      `json:"id"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func summarizeTask(task db.Task) taskSummary {
	return taskSummary{
		ID:        task.ID,
		Status:    task.Status,
		CreatedAt: task.CreatedAt,
		UpdatedAt: task.UpdatedAt,
	}
}

// taskForOperator applies the request's task-read authority at the storage
// boundary. A caller without tasks.read receives only the fixed summary shape.
func taskForOperator(c *gin.Context, task db.Task) any {
	if c == nil || !middleware.HasPermission(c, db.PermTasksRead) {
		return summarizeTask(task)
	}
	return taskForAuthorizedOperator(task)
}

// tasksForOperator projects a task list without changing its order or length.
// The concrete slice type changes with authority so redacted rows cannot carry
// zero-valued secret fields from db.Task.
func tasksForOperator(c *gin.Context, tasks []db.Task) any {
	if c != nil && middleware.HasPermission(c, db.PermTasksRead) {
		for i := range tasks {
			tasks[i] = taskForAuthorizedOperator(tasks[i])
		}
		return tasks
	}
	summaries := make([]taskSummary, len(tasks))
	for i := range tasks {
		summaries[i] = summarizeTask(tasks[i])
	}
	return summaries
}

// fileTaskTypes is the single server-side classification reused by the
// agent-scoped task status/output exception now and the files.read/files.write
// route split in the next hardening step. Only real agent task types belong
// here; route and transfer-control names must never grant this exception.
var fileTaskTypes = map[string]bool{
	protocol.TaskTypeLS:          true,
	protocol.TaskTypeFind:        true,
	protocol.TaskTypeDrives:      true,
	protocol.TaskTypeRead:        true,
	protocol.TaskTypeDownload:    true,
	protocol.TaskTypeWrite:       true,
	protocol.TaskTypeUpload:      true,
	protocol.TaskTypeDownloadURL: true,
	protocol.TaskTypeDelete:      true,
	protocol.TaskTypeMkdir:       true,
	protocol.TaskTypeRename:      true,
	protocol.TaskTypeChmod:       true,
	protocol.TaskTypeFileHunt:    true,
	protocol.TaskTypeFileSearch:  true,
	"file_scan":                  true,
	"file_thumb":                 true,
	"usb_enum":                   true,
}

func isFileTask(taskType string) bool {
	return fileTaskTypes[taskType]
}

// canReadTaskDetails governs agent-scoped task status/output. File tasks keep
// their planned files.read exception; every other task requires tasks.read.
func canReadTaskDetails(c *gin.Context, taskType string) bool {
	return middleware.HasPermission(c, db.PermTasksRead) ||
		(isFileTask(taskType) && middleware.HasPermission(c, db.PermFilesRead))
}

// taskForAuthorizedOperator is the final storage-boundary guard for task
// output. Most DB reads are decrypted by db.Task.AfterFind, but result objects
// created by map updates or passed directly from write paths may still contain
// FC2ENC values. Never expose that at-rest representation through REST or WS.
func taskForAuthorizedOperator(task db.Task) db.Task {
	for fieldName, field := range map[string]*string{
		"result":  &task.Result,
		"error":   &task.Error,
		"command": &task.Command,
		"shell":   &task.Shell,
		"data":    &task.Data,
	} {
		if !strings.HasPrefix(*field, "FC2ENC:") {
			continue
		}
		plain, err := crypto.DecryptLoot(*field)
		if err != nil {
			slog.Error("Failed to decrypt task field for operator response", "task_id", task.ID, "field", fieldName, "error", err)
			*field = ""
			continue
		}
		*field = plain
	}
	// A referenced task's real arguments never leave the server. The safe
	// template and the human-readable vault label are the only public fields.
	if task.CredentialID != nil && *task.CredentialID != 0 {
		task.Command = fmt.Sprintf("@cred:%d", *task.CredentialID)
		task.Shell = ""
		task.Data = ""
		return task
	}
	// File bytes and editor drafts are held in Data at rest; the dedicated
	// revisions endpoint is the only place that may return them.
	if task.Type == "upload" || task.Type == "write" {
		task.Data = ""
	}
	if db.SensitiveShellTypes[task.Type] {
		task.Shell = ""
	}
	return task
}
