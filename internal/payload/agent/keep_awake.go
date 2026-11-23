package main

// keep_awake holds the Windows sleep-prevention request for one agent. The
// command is a duration in seconds; 0 releases the request. A timer always
// releases it, so a task that asks for "1 hour" cannot leave the target awake
// forever if the operator forgets to send the release.
//
// No WebRTC auto-hook here: keeping a console session alive is a separate
// policy decision from a task the operator explicitly queues.

import (
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	keepAwakeDefaultSeconds = 300
	keepAwakeMaxSeconds     = 86400
)

var (
	keepAwakeMu     sync.Mutex
	keepAwakeTimer  *time.Timer
	keepAwakeGen    uint64
	keepAwakeActive bool
	keepAwakeUntil  time.Time

	// Overridden by tests; production uses the platform implementation.
	setKeepAwakeStateFunc = setKeepAwakeState
)

func parseKeepAwake(command string) (int, error) {
	arg := strings.TrimSpace(command)
	if arg == "" {
		return keepAwakeDefaultSeconds, nil
	}
	seconds, err := strconv.Atoi(arg)
	if err != nil || seconds < 0 || seconds > keepAwakeMaxSeconds {
		return 0, fmt.Errorf("keep_awake: seconds must be 0 (release) or 1-%d, got %q", keepAwakeMaxSeconds, arg)
	}
	return seconds, nil
}

// keep_awake with "status" reports the active window without changing it.
func keepAwakeStatus(res *TaskResult) {
	keepAwakeMu.Lock()
	active, remaining := keepAwakeActive, 0
	if active {
		remaining = int(time.Until(keepAwakeUntil).Seconds())
		if remaining < 0 {
			remaining = 0
		}
	}
	keepAwakeMu.Unlock()
	if active {
		res.Output = fmt.Sprintf("keep_awake: active, %d seconds remaining", remaining)
		return
	}
	res.Output = "keep_awake: inactive"
}

func handleKeepAwake(task Task, res *TaskResult) {
	if strings.EqualFold(strings.TrimSpace(task.Command), "status") {
		keepAwakeStatus(res)
		return
	}
	seconds, err := parseKeepAwake(task.Command)
	if err != nil {
		res.Error = err.Error()
		return
	}

	keepAwakeMu.Lock()
	defer keepAwakeMu.Unlock()

	if seconds == 0 {
		if keepAwakeTimer != nil {
			keepAwakeTimer.Stop()
			keepAwakeTimer = nil
		}
		keepAwakeGen++
		keepAwakeActive = false
		keepAwakeUntil = time.Time{}
		if err := setKeepAwakeStateFunc(false); err != nil {
			res.Error = err.Error()
			return
		}
		res.Output = "keep_awake: sleep allowed again"
		return
	}

	if err := setKeepAwakeStateFunc(true); err != nil {
		res.Error = err.Error()
		return
	}
	if keepAwakeTimer != nil {
		keepAwakeTimer.Stop()
	}
	keepAwakeGen++
	gen := keepAwakeGen
	keepAwakeActive = true
	keepAwakeUntil = time.Now().Add(time.Duration(seconds) * time.Second)
	keepAwakeTimer = time.AfterFunc(time.Duration(seconds)*time.Second, func() {
		keepAwakeMu.Lock()
		defer keepAwakeMu.Unlock()
		// A newer task replaced this one; its timer owns the release.
		if gen != keepAwakeGen {
			return
		}
		keepAwakeTimer = nil
		keepAwakeActive = false
		keepAwakeUntil = time.Time{}
		_ = setKeepAwakeStateFunc(false)
	})
	res.Output = fmt.Sprintf("keep_awake: system awake for %d seconds; send 0 to release early", seconds)
}
