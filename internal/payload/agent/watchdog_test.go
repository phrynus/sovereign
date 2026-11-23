//go:build linux || windows || darwin
// +build linux windows darwin

package main

import (
	"testing"
	"time"
)

func TestClampWatchdogParams(t *testing.T) {
	cases := []struct {
		intervalSec, maxRestarts int
		wantInterval             time.Duration
		wantRestarts             int
	}{
		{0, 0, watchdogDefaultInterval, watchdogDefaultRestarts},
		{1, 1, watchdogMinInterval, 1},
		{15, 10, 15 * time.Second, 10},
		{600, 999, watchdogMaxInterval, watchdogMaxRestarts},
		{-5, -1, watchdogDefaultInterval, watchdogDefaultRestarts},
	}
	for _, tc := range cases {
		gotInterval, gotRestarts := clampWatchdogParams(tc.intervalSec, tc.maxRestarts)
		if gotInterval != tc.wantInterval || gotRestarts != tc.wantRestarts {
			t.Fatalf("clampWatchdogParams(%d, %d) = (%s, %d), want (%s, %d)",
				tc.intervalSec, tc.maxRestarts, gotInterval, gotRestarts, tc.wantInterval, tc.wantRestarts)
		}
	}
}

func TestParseWatchdogEnvRoundTrip(t *testing.T) {
	want := watchdogParams{parentPID: 4242, interval: 20 * time.Second, maxRestarts: 7, token: "0123456789abcdef"}
	got, ok := parseWatchdogEnv(formatWatchdogEnv(want))
	if !ok {
		t.Fatal("round-tripped env value did not parse")
	}
	if got != want {
		t.Fatalf("parsed %+v, want %+v", got, want)
	}
	for _, bad := range []string{"", "v2|1|15|10|0123456789abcdef", "v1|0|15|10|0123456789abcdef", "v1|x|15|10|0123456789abcdef", "v1|1|15|10|short"} {
		if _, ok := parseWatchdogEnv(bad); ok {
			t.Fatalf("parseWatchdogEnv(%q) accepted an invalid value", bad)
		}
	}
}

func TestWatchdogArgs(t *testing.T) {
	if d, n := watchdogArgs(""); d != watchdogDefaultInterval || n != watchdogDefaultRestarts {
		t.Fatalf("defaults = (%s, %d)", d, n)
	}
	if d, n := watchdogArgs("30:5"); d != 30*time.Second || n != 5 {
		t.Fatalf("30:5 parsed as (%s, %d)", d, n)
	}
	if d, n := watchdogArgs("1:999"); d != watchdogMinInterval || n != watchdogMaxRestarts {
		t.Fatalf("1:999 clamped to (%s, %d)", d, n)
	}
}

// TestRunWatchdogLoopRestartsUntilMax pins the fork-bomb bound: the loop
// restarts a dead parent exactly max_restarts times and then exits.
func TestRunWatchdogLoopRestartsUntilMax(t *testing.T) {
	oldDir := watchdogStateDir
	watchdogStateDir = t.TempDir()
	defer func() { watchdogStateDir = oldDir }()

	spawns := 0
	ops := watchdogOps{
		alive:         func(int) bool { return false },
		spawn:         func() (int, error) { spawns++; return 1000 + spawns, nil },
		stopRequested: func() bool { return false },
		sleep:         func(time.Duration) {},
	}
	p := watchdogParams{parentPID: 100, interval: time.Second, maxRestarts: 3, token: "0123456789abcdef"}
	if got := runWatchdogLoop(p, ops); got != 3 {
		t.Fatalf("restarts = %d, want 3", got)
	}
	if spawns != 3 {
		t.Fatalf("spawns = %d, want 3", spawns)
	}
	if pid := readWatchdogPID(p.token); pid != 1003 {
		t.Fatalf("pid file holds %d, want the last spawned pid 1003", pid)
	}
}

func TestRunWatchdogLoopStopsOnMarker(t *testing.T) {
	oldDir := watchdogStateDir
	watchdogStateDir = t.TempDir()
	defer func() { watchdogStateDir = oldDir }()

	spawns := 0
	ops := watchdogOps{
		alive:         func(int) bool { return false },
		spawn:         func() (int, error) { spawns++; return 1, nil },
		stopRequested: func() bool { return true },
		sleep:         func(time.Duration) {},
	}
	p := watchdogParams{parentPID: 100, interval: time.Second, maxRestarts: 5, token: "0123456789abcdef"}
	if got := runWatchdogLoop(p, ops); got != 0 {
		t.Fatalf("restarts = %d, want 0 after a stop request", got)
	}
	if spawns != 0 {
		t.Fatalf("spawns = %d, want 0", spawns)
	}
}

func TestWithoutWatchdogEnvDropsOnlyTheMarker(t *testing.T) {
	marker := s(SWatchdogEnv) + "=v1|1|15|10|0123456789abcdef"
	env := []string{"FORGEC2_PORT=8000", marker, "TZ=UTC"}
	out := withoutWatchdogEnv(env)
	if len(out) != 2 || out[0] != "FORGEC2_PORT=8000" || out[1] != "TZ=UTC" {
		t.Fatalf("withoutWatchdogEnv = %v", out)
	}
}

func TestWatchdogTokenStableForSameUUID(t *testing.T) {
	old := agentUUID
	defer func() { agentUUID = old }()
	agentUUID = "11111111-2222-4333-8444-555555555555"
	first := watchdogToken()
	if len(first) != 16 {
		t.Fatalf("token length = %d, want 16", len(first))
	}
	if again := watchdogToken(); again != first {
		t.Fatalf("token changed between calls: %q vs %q", first, again)
	}
	agentUUID = "99999999-2222-4333-8444-555555555555"
	if watchdogToken() == first {
		t.Fatal("different agents derived the same watchdog token")
	}
}
