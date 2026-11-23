//go:build linux || darwin
// +build linux darwin

package main

import (
	"os/exec"
	"syscall"
)

// watchdogProcessAlive reports whether pid exists. EPERM means it exists but
// belongs to another user, which still counts as alive.
func watchdogProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}

// detachWatchdogProcess puts the watcher in its own session so it survives the
// parent's death and has no controlling terminal.
func detachWatchdogProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}
