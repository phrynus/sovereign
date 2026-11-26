package server

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/r0si/sovereign/internal/db"
)

func TestInteractiveShellOutputEventContract(t *testing.T) {
	s, database := newBeaconFinalityTestServer(t)
	agent := db.Implant{ID: "shell-event-agent", TenantID: 1, Hostname: "SHELL-01"}
	if err := database.Create(&agent).Error; err != nil {
		t.Fatal(err)
	}
	allowed := &wsClientConn{tenantID: 1, canViewTasks: true, ch: make(chan []byte, 2), done: make(chan struct{})}
	foreign := &wsClientConn{tenantID: 2, canViewTasks: true, ch: make(chan []byte, 2), done: make(chan struct{})}
	noPermission := &wsClientConn{tenantID: 1, canViewTasks: false, ch: make(chan []byte, 2), done: make(chan struct{})}
	s.wsClients[new(websocket.Conn)] = allowed
	s.wsClients[new(websocket.Conn)] = foreign
	s.wsClients[new(websocket.Conn)] = noPermission
	events := make(chan Event, 2)
	s.eventManager.On(EventShellOutput, func(evt Event) { events <- evt })

	s.processTaskResults(agent, []taskResult{{
		TaskID: 0, Type: "shell_output", Path: "sh_7", Output: "cHJvbXB0Pg==", Encoding: "base64",
	}}, agent.ID, time.Now())

	select {
	case raw := <-allowed.ch:
		var frame map[string]interface{}
		if err := json.Unmarshal(raw, &frame); err != nil {
			t.Fatal(err)
		}
		if frame["type"] != "shell_output" || frame["agent_id"] != agent.ID || frame["shell_id"] != "sh_7" ||
			frame["data"] != "cHJvbXB0Pg==" || frame["encoding"] != "base64" {
			t.Fatalf("shell output frame = %+v", frame)
		}
	case <-time.After(time.Second):
		t.Fatal("authorized operator received no shell output")
	}
	select {
	case raw := <-foreign.ch:
		t.Fatalf("foreign tenant received shell output: %s", raw)
	case raw := <-noPermission.ch:
		t.Fatalf("tasks.read-denied operator received shell output: %s", raw)
	case <-time.After(50 * time.Millisecond):
	}
	select {
	case evt := <-events:
		if evt.TenantID != agent.TenantID || evt.AgentID != agent.ID || evt.Data["shell_id"] != "sh_7" {
			t.Fatalf("shell output event = %+v", evt)
		}
	case <-time.After(time.Second):
		t.Fatal("shell output lifecycle event was not emitted")
	}
}

func TestInteractiveShellOutputRequiresSessionAndData(t *testing.T) {
	s, database := newBeaconFinalityTestServer(t)
	agent := db.Implant{ID: "shell-event-invalid", TenantID: 1}
	if err := database.Create(&agent).Error; err != nil {
		t.Fatal(err)
	}
	client := &wsClientConn{tenantID: 1, canViewTasks: true, ch: make(chan []byte, 1), done: make(chan struct{})}
	s.wsClients[new(websocket.Conn)] = client
	s.processTaskResults(agent, []taskResult{{TaskID: 0, Type: "shell_output", Output: "data"}}, agent.ID, time.Now())
	select {
	case raw := <-client.ch:
		t.Fatalf("shell output without a session id was broadcast: %s", raw)
	case <-time.After(50 * time.Millisecond):
	}
}
