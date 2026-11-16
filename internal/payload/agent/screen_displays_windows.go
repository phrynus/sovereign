//go:build windows
// +build windows

package main

import (
	"sync"
	"syscall"
	"unsafe"
)

var (
	procEnumDisplayMonitors = user32.NewProc("EnumDisplayMonitors")
	procGetMonitorInfoW     = user32.NewProc("GetMonitorInfoW")
)

const monitorInfoPrimary = 0x00000001

type rect32 struct {
	Left, Top, Right, Bottom int32
}

// monitorInfo mirrors MONITORINFO. GetMonitorInfoW accepts it when cbSize is
// 40 (MONITORINFOEX is larger and only adds the device name).
type monitorInfo struct {
	cbSize    uint32
	rcMonitor rect32
	rcWork    rect32
	dwFlags   uint32
}

var (
	enumMonMu  sync.Mutex
	enumMonOut []screenRect
	// enumMonProc is created once: syscall.NewCallback allocates a trampoline,
	// and doing that per enumeration would leak one per websocket connect.
	enumMonProc = syscall.NewCallback(func(hMonitor, _ uintptr, _ uintptr, _ uintptr) uintptr {
		var info monitorInfo
		info.cbSize = uint32(unsafe.Sizeof(info))
		if ok, _, _ := procGetMonitorInfoW.Call(hMonitor, uintptr(unsafe.Pointer(&info))); ok == 0 {
			return 1 // continue enumeration
		}
		enumMonOut = append(enumMonOut, screenRect{
			X:       int(info.rcMonitor.Left),
			Y:       int(info.rcMonitor.Top),
			Width:   int(info.rcMonitor.Right - info.rcMonitor.Left),
			Height:  int(info.rcMonitor.Bottom - info.rcMonitor.Top),
			Primary: info.dwFlags&monitorInfoPrimary != 0,
		})
		return 1
	})
)

// nativeScreenRects enumerates the physical displays in the same coordinate
// space SetCursorPos uses, so a click mapped from one monitor's capture lands
// on that monitor.
func nativeScreenRects() []screenRect {
	setDPIAware()
	enumMonMu.Lock()
	defer enumMonMu.Unlock()
	enumMonOut = nil
	_, _, _ = procEnumDisplayMonitors.Call(0, 0, enumMonProc, 0)
	out := enumMonOut
	enumMonOut = nil
	if len(out) == 0 {
		return nil
	}
	sortScreenRects(out)
	return out
}
