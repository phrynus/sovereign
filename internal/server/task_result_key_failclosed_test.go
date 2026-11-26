package server

import (
	"strings"
	"testing"
	"time"

	"github.com/r0si/sovereign/internal/db"
)

func TestDecryptTaskResultKeyFailsClosed(t *testing.T) {
	result := taskResult{TaskID: 7, Type: "shell", Output: "not-valid-ciphertext", EncryptedWithTaskKey: true}
	if err := decryptTaskResultKey("", &result); err == nil {
		t.Fatal("missing task key was accepted")
	}
	if result.Output != "" || result.EncryptedWithTaskKey || result.Error != "per-task result decryption failed" {
		t.Fatalf("decryption failure did not clear ciphertext: %+v", result)
	}
}

func TestProcessTaskResultDecryptionFailureIsTerminalAndEmpty(t *testing.T) {
	s, database := newBeaconFinalityTestServer(t)
	oldEncrypt := encryptTaskFieldsFn
	encryptTaskFieldsFn = func(*db.Task) error { return nil }
	t.Cleanup(func() { encryptTaskFieldsFn = oldEncrypt })
	agent := db.Implant{ID: "task-key-fail-agent", TenantID: 1}
	if err := database.Create(&agent).Error; err != nil {
		t.Fatal(err)
	}
	task := db.Task{AgentID: agent.ID, TenantID: 1, Type: "shell", Command: "whoami", Status: "running"}
	if err := database.Create(&task).Error; err != nil {
		t.Fatal(err)
	}
	s.agentPendingTasks[agent.ID] = 1
	s.processTaskResults(agent, []taskResult{{
		TaskID: task.ID, Type: "shell", Output: "Y2lwaGVydGV4dA==", EncryptedWithTaskKey: true,
	}}, agent.ID, time.Now())
	var stored db.Task
	if err := database.First(&stored, task.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.Status != "failed" || stored.Result != "" || !strings.Contains(stored.Error, "decryption failed") {
		t.Fatalf("undecryptable result was not failed closed: %+v", stored)
	}
	if got := s.agentPendingTasks[agent.ID]; got != 0 {
		t.Fatalf("failed result did not release its task slot: %d", got)
	}
}
