//go:build windows
// +build windows

package main

import (
	"os/exec"
	"syscall"
	"unsafe"
)

const (
	watchdogProcessQueryLimitedInformation = 0x1000
	watchdogStillActive                    = 259 // STILL_ACTIVE
	watchdogDetachedProcess                = 0x00000008
	watchdogCreateNewProcessGroup          = 0x00000200
)

var (
	watchdogKernel32           = syscall.NewLazyDLL("kernel32.dll")
	watchdogOpenProcess        = watchdogKernel32.NewProc("OpenProcess")
	watchdogGetExitCodeProcess = watchdogKernel32.NewProc("GetExitCodeProcess")
	watchdogCloseHandle        = watchdogKernel32.NewProc("CloseHandle")
)

// watchdogProcessAlive opens the process (query rights only) and treats
// STILL_ACTIVE as alive. A zero handle means the pid is gone or unreadable.
func watchdogProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	handle, _, _ := watchdogOpenProcess.Call(watchdogProcessQueryLimitedInformation, 0, uintptr(pid))
	if handle == 0 {
		return false
	}
	defer watchdogCloseHandle.Call(handle)
	var code uint32
	ok, _, _ := watchdogGetExitCodeProcess.Call(handle, uintptr(unsafe.Pointer(&code)))
	if ok == 0 {
		return false
	}
	return code == watchdogStillActive
}

// detachWatchdogProcess keeps the watcher independent of the parent's console
// and process group so it survives the parent's death.
func detachWatchdogProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: watchdogDetachedProcess | watchdogCreateNewProcessGroup,
	}
}
