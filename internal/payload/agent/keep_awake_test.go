package main

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestParseKeepAwake(t *testing.T) {
	cases := []struct {
		command string
		want    int
		bad     bool
	}{
		{"", keepAwakeDefaultSeconds, false},
		{"   ", keepAwakeDefaultSeconds, false},
		{"0", 0, false},
		{"60", 60, false},
		{"86400", keepAwakeMaxSeconds, false},
		{"86401", 0, true},
		{"-1", 0, true},
		{"abc", 0, true},
	}
	for _, c := range cases {
		got, err := parseKeepAwake(c.command)
		if c.bad {
			if err == nil {
				t.Errorf("parseKeepAwake(%q) accepted an invalid value: %d", c.command, got)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("parseKeepAwake(%q) = %d, %v; want %d", c.command, got, err, c.want)
		}
	}
}

func TestKeepAwakeHandlerReleasesOnZero(t *testing.T) {
	original := setKeepAwakeStateFunc
	defer func() { setKeepAwakeStateFunc = original }()

	var calls []bool
	setKeepAwakeStateFunc = func(active bool) error {
		calls = append(calls, active)
		return nil
	}
	// Clean state from any earlier test in this package.
	keepAwakeMu.Lock()
	if keepAwakeTimer != nil {
		keepAwakeTimer.Stop()
	}
	keepAwakeTimer, keepAwakeActive, keepAwakeGen = nil, false, keepAwakeGen+1
	keepAwakeMu.Unlock()

	var res TaskResult
	handleKeepAwake(Task{Command: "60"}, &res)
	if res.Error != "" || !strings.Contains(res.Output, "60 seconds") {
		t.Fatalf("activate: %#v", res)
	}
	handleKeepAwake(Task{Command: "0"}, &res)
	if res.Error != "" || !strings.Contains(res.Output, "sleep allowed") {
		t.Fatalf("release: %#v", res)
	}
	if !reflect.DeepEqual(calls, []bool{true, false}) {
		t.Fatalf("setter calls: %#v", calls)
	}
	keepAwakeMu.Lock()
	defer keepAwakeMu.Unlock()
	if keepAwakeTimer != nil || keepAwakeActive {
		t.Fatal("release left a live timer or active state")
	}
}

func TestKeepAwakeHandlerRejectsWithoutChangingState(t *testing.T) {
	original := setKeepAwakeStateFunc
	defer func() { setKeepAwakeStateFunc = original }()

	called := false
	setKeepAwakeStateFunc = func(bool) error {
		called = true
		return nil
	}
	var res TaskResult
	handleKeepAwake(Task{Command: "99999"}, &res)
	if res.Error == "" {
		t.Fatal("invalid duration accepted")
	}
	if called {
		t.Fatal("invalid duration reached the platform setter")
	}
}

func TestKeepAwakeStatus(t *testing.T) {
	original := setKeepAwakeStateFunc
	defer func() { setKeepAwakeStateFunc = original }()
	setKeepAwakeStateFunc = func(bool) error { return nil }

	keepAwakeMu.Lock()
	if keepAwakeTimer != nil {
		keepAwakeTimer.Stop()
	}
	keepAwakeTimer, keepAwakeActive, keepAwakeGen = nil, false, keepAwakeGen+1
	keepAwakeUntil = time.Time{}
	keepAwakeMu.Unlock()

	var res TaskResult
	handleKeepAwake(Task{Command: "status"}, &res)
	if res.Output != "keep_awake: inactive" {
		t.Fatalf("inactive status: %#v", res)
	}
	handleKeepAwake(Task{Command: "60"}, &res)
	handleKeepAwake(Task{Command: "status"}, &res)
	if !strings.Contains(res.Output, "active") || !strings.Contains(res.Output, "remaining") {
		t.Fatalf("active status: %#v", res)
	}
	handleKeepAwake(Task{Command: "0"}, &res)
	handleKeepAwake(Task{Command: "status"}, &res)
	if res.Output != "keep_awake: inactive" {
		t.Fatalf("status after release: %#v", res)
	}
}
