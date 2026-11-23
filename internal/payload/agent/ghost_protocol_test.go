package main

import (
	"testing"
	"time"
)

func ghostTestSetup(t *testing.T, timeout time.Duration) {
	t.Helper()
	oldEnabled, oldTimeout := ghostModeEnabled, ghostModeTimeout
	ghostModeEnabled = true
	ghostModeTimeout = timeout
	exitGhostMode()
	t.Cleanup(func() {
		exitGhostMode()
		ghostModeEnabled = oldEnabled
		ghostModeTimeout = oldTimeout
	})
}

func TestGhostModeStaleTimerDoesNotExitNewSession(t *testing.T) {
	ghostTestSetup(t, 40*time.Millisecond)

	enterGhostMode("first")
	exitGhostMode()
	// Only a stale timer can end this session during the test.
	ghostModeTimeout = 500 * time.Millisecond
	enterGhostMode("second")
	time.Sleep(150 * time.Millisecond)

	if !isInGhostMode() {
		t.Fatal("the first session's timer ended the second session")
	}
}

func TestGhostModeAutoExitsItsOwnSession(t *testing.T) {
	ghostTestSetup(t, 40*time.Millisecond)

	enterGhostMode("only")
	time.Sleep(150 * time.Millisecond)

	if isInGhostMode() {
		t.Fatal("the timer did not end its own ghost session")
	}
}
