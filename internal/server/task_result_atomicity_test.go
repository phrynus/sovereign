package server

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/r0si/sovereign/internal/db"
	"gorm.io/gorm"
)

func failTaskResultWrites(t *testing.T, database *gorm.DB) {
	t.Helper()
	const callback = "test:fail_task_result_write"
	if err := database.Callback().Update().Before("gorm:update").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table != "tasks" {
			return
		}
		if fields, ok := tx.Statement.Dest.(map[string]interface{}); ok {
			if _, writesResult := fields["result"]; writesResult {
				tx.AddError(errors.New("injected task result write failure"))
			}
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Callback().Update().Remove(callback) })
}

func TestTaskResultWriteFailureRollsBackFinalization(t *testing.T) {
	s, database := newBeaconFinalityTestServer(t)
	oldEncrypt := encryptTaskFieldsFn
	encryptTaskFieldsFn = func(*db.Task) error { return nil }
	t.Cleanup(func() { encryptTaskFieldsFn = oldEncrypt })
	failTaskResultWrites(t, database)

	const agentID = "atomic-result-agent"
	agent := db.Implant{ID: agentID, Hostname: "ATOMIC-01"}
	if err := database.Create(&agent).Error; err != nil {
		t.Fatal(err)
	}
	task := db.Task{AgentID: agentID, Type: "shell", Command: "whoami", Status: "running"}
	if err := database.Create(&task).Error; err != nil {
		t.Fatal(err)
	}
	s.agentPendingTasks[agentID] = 1

	client := &wsClientConn{ch: make(chan []byte, 4), done: make(chan struct{}), canViewTasks: true}
	s.wsClients[nil] = client
	completed := make(chan Event, 1)
	failed := make(chan Event, 1)
	s.eventManager.On(EventTaskComplete, func(evt Event) { completed <- evt })
	s.eventManager.On(EventTaskFail, func(evt Event) { failed <- evt })

	s.processTaskResults(agent, []taskResult{{
		TaskID: task.ID, Type: "shell", Output: "administrator", ResultID: "rid-atomic-fail",
	}}, agentID, time.Now())

	var stored db.Task
	if err := database.First(&stored, task.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.Status != "running" || stored.Result != "" || stored.LastResultID != "" {
		t.Fatalf("failed write left a partial final result: %+v", stored)
	}
	if got := s.agentPendingTasks[agentID]; got != 1 {
		t.Fatalf("failed write released pending slot: %d", got)
	}
	select {
	case raw := <-client.ch:
		t.Fatalf("failed write broadcast a task update: %s", raw)
	case <-time.After(50 * time.Millisecond):
	}
	select {
	case evt := <-completed:
		t.Fatalf("failed write emitted completion: %+v", evt)
	case evt := <-failed:
		t.Fatalf("failed write emitted failure: %+v", evt)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestPartialResultWriteFailureDoesNotBroadcast(t *testing.T) {
	s, database := newBeaconFinalityTestServer(t)
	oldEncrypt := encryptTaskFieldsFn
	encryptTaskFieldsFn = func(*db.Task) error { return nil }
	t.Cleanup(func() { encryptTaskFieldsFn = oldEncrypt })
	failTaskResultWrites(t, database)

	const agentID = "atomic-partial-agent"
	agent := db.Implant{ID: agentID, Hostname: "ATOMIC-02"}
	if err := database.Create(&agent).Error; err != nil {
		t.Fatal(err)
	}
	task := db.Task{AgentID: agentID, Type: "shell", Command: "long-running", Status: "running"}
	if err := database.Create(&task).Error; err != nil {
		t.Fatal(err)
	}
	client := &wsClientConn{ch: make(chan []byte, 1), done: make(chan struct{}), canViewTasks: true}
	s.wsClients[nil] = client

	s.processTaskResults(agent, []taskResult{{
		TaskID: task.ID, Type: "shell", Output: "partial", Partial: true,
	}}, agentID, time.Now())
	var stored db.Task
	if err := database.First(&stored, task.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.Result != "" {
		t.Fatalf("failed partial write changed stored result: %q", stored.Result)
	}
	select {
	case raw := <-client.ch:
		var message map[string]interface{}
		_ = json.Unmarshal(raw, &message)
		t.Fatalf("failed partial write broadcast an update: %s", raw)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestRelayedTaskResultWriteFailureRollsBackFinalization(t *testing.T) {
	s, database := newBeaconFinalityTestServer(t)
	oldEncrypt := encryptTaskFieldsFn
	encryptTaskFieldsFn = func(*db.Task) error { return nil }
	t.Cleanup(func() { encryptTaskFieldsFn = oldEncrypt })
	failTaskResultWrites(t, database)

	const parentID = "relay-atomic-parent"
	const childID = "relay-atomic-child"
	child := db.Implant{ID: childID, Hostname: "RELAY-ATOMIC", ParentID: parentID}
	if err := database.Create(&child).Error; err != nil {
		t.Fatal(err)
	}
	task := db.Task{AgentID: childID, Type: "shell", Command: "whoami", Status: "running"}
	if err := database.Create(&task).Error; err != nil {
		t.Fatal(err)
	}
	s.agentPendingTasks[childID] = 1
	client := &wsClientConn{ch: make(chan []byte, 2), done: make(chan struct{}), canViewTasks: true}
	s.wsClients[nil] = client
	completed := make(chan Event, 1)
	s.eventManager.On(EventTaskComplete, func(evt Event) { completed <- evt })

	s.processRelayedResults([]relayedData{{
		AgentID: childID,
		Results: []taskResult{{TaskID: task.ID, Type: "shell", Output: "child-admin", ResultID: "rid-relay-fail"}},
	}}, parentID, time.Now())

	var stored db.Task
	if err := database.First(&stored, task.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.Status != "running" || stored.Result != "" || stored.LastResultID != "" {
		t.Fatalf("failed relayed write left a partial final result: %+v", stored)
	}
	if got := s.agentPendingTasks[childID]; got != 1 {
		t.Fatalf("failed relayed write released pending slot: %d", got)
	}
	select {
	case raw := <-client.ch:
		t.Fatalf("failed relayed write broadcast a task update: %s", raw)
	case <-time.After(50 * time.Millisecond):
	}
	select {
	case evt := <-completed:
		t.Fatalf("failed relayed write emitted completion: %+v", evt)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestRelayedTaskResultUsesSharedLifecyclePipeline(t *testing.T) {
	s, database := newBeaconFinalityTestServer(t)
	oldEncrypt := encryptTaskFieldsFn
	encryptTaskFieldsFn = func(*db.Task) error { return nil }
	t.Cleanup(func() { encryptTaskFieldsFn = oldEncrypt })

	const parentID = "relay-lifecycle-parent"
	const childID = "relay-lifecycle-child"
	child := db.Implant{ID: childID, Hostname: "RELAY-LIFECYCLE", ParentID: parentID}
	if err := database.Create(&child).Error; err != nil {
		t.Fatal(err)
	}
	task := db.Task{AgentID: childID, Type: "shell", Command: "whoami", Status: "running"}
	if err := database.Create(&task).Error; err != nil {
		t.Fatal(err)
	}
	s.agentPendingTasks[childID] = 1
	completed := make(chan Event, 1)
	s.eventManager.On(EventTaskComplete, func(evt Event) { completed <- evt })

	s.processRelayedResults([]relayedData{{
		AgentID: childID,
		Results: []taskResult{{TaskID: task.ID, Type: "shell", Output: "child-admin", ResultID: "rid-relay-ok"}},
	}}, parentID, time.Now())

	evt := waitForEvent(t, completed, EventTaskComplete)
	if evt.TaskID != task.ID || evt.AgentID != childID {
		t.Fatalf("relayed lifecycle event = %+v", evt)
	}
	var stored db.Task
	if err := database.First(&stored, task.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.Status != "completed" || stored.Result != "child-admin" || stored.LastResultID != "rid-relay-ok" {
		t.Fatalf("relayed result not persisted through shared pipeline: %+v", stored)
	}
	if got := s.agentPendingTasks[childID]; got != 0 {
		t.Fatalf("relayed completion did not release pending slot: %d", got)
	}
}
