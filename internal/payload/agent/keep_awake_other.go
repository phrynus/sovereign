//go:build !windows
// +build !windows

package main

import "fmt"

// setKeepAwakeState is Windows-specific (SetThreadExecutionState). Other
// platforms report the limitation instead of pretending the task succeeded.
func setKeepAwakeState(active bool) error {
	return fmt.Errorf("keep_awake: only Windows supports SetThreadExecutionState")
}
