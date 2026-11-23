//go:build windows
// +build windows

package main

import "fmt"

const (
	esContinuous     = 0x80000000
	esSystemRequired = 0x00000001
)

// setKeepAwakeState asks the power manager to keep the system in the working
// state. ES_CONTINUOUS makes the request persist until it is explicitly
// released with the same flag alone (or the process exits).
func setKeepAwakeState(active bool) error {
	flags := uintptr(esContinuous)
	if active {
		flags |= esSystemRequired
	}
	ret, _, _ := k32.NewProc("SetThreadExecutionState").Call(flags)
	if ret == 0 {
		return fmt.Errorf("keep_awake: SetThreadExecutionState failed")
	}
	return nil
}
