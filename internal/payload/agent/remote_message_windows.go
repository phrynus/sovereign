//go:build windows
// +build windows

package main

import (
	"fmt"
	"syscall"
	"unsafe"
)

var procMessageBoxW = user32.NewProc("MessageBoxW")

const (
	mbOK              = 0x00000000
	mbIconInformation = 0x00000040
	mbSetForeground   = 0x00010000
	mbTopMost         = 0x00040000
)

// showRemoteMessage puts an operator message in front of the target user.
// MessageBox blocks until the user dismisses it, so the caller runs it on a
// goroutine and the signaling read loop stays responsive.
func showRemoteMessage(title, text string) error {
	titlePtr, err := syscall.UTF16PtrFromString(title)
	if err != nil {
		return fmt.Errorf("message title: %w", err)
	}
	textPtr, err := syscall.UTF16PtrFromString(text)
	if err != nil {
		return fmt.Errorf("message text: %w", err)
	}
	flags := uintptr(mbOK | mbIconInformation | mbTopMost | mbSetForeground)
	ret, _, _ := procMessageBoxW.Call(
		0,
		uintptr(unsafe.Pointer(textPtr)),
		uintptr(unsafe.Pointer(titlePtr)),
		flags,
	)
	// MessageBox returns 0 only when it failed (for MB_OK a dismissal is 1).
	if ret == 0 {
		return fmt.Errorf("MessageBoxW failed")
	}
	return nil
}
