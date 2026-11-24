//go:build windows
// +build windows

package main

import (
	"fmt"

	"golang.org/x/sys/windows"
)

// replaceFileAtomically overwrites an existing file on Windows. os.Rename
// cannot replace a live destination there, so MoveFileExW with
// MOVEFILE_REPLACE_EXISTING is the documented atomic replacement primitive.
func replaceFileAtomically(tempPath, destPath string) error {
	from, err := windows.UTF16PtrFromString(tempPath)
	if err != nil {
		return fmt.Errorf("replace file: %w", err)
	}
	to, err := windows.UTF16PtrFromString(destPath)
	if err != nil {
		return fmt.Errorf("replace file: %w", err)
	}
	if err := windows.MoveFileEx(from, to, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH); err != nil {
		return fmt.Errorf("replace file: %w", err)
	}
	return nil
}
