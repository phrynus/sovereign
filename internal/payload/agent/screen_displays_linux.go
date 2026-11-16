//go:build linux
// +build linux

package main

import (
	"os/exec"
)

// nativeScreenRects enumerates the X11 outputs. x11grab addresses a region as
// DISPLAY+X,Y and the geometry xrandr reports is in the same framebuffer
// coordinate space, so the rects are directly usable for capture and for input
// scaling. Without xrandr the single root-window size is the only answer.
func nativeScreenRects() []screenRect {
	if out, err := exec.Command("xrandr", "--query").Output(); err == nil {
		if rects := parseXRandRMonitors(string(out)); len(rects) > 0 {
			return rects
		}
	}
	if x, y, w, h, ok := nativeScreenRect(); ok {
		return []screenRect{{X: x, Y: y, Width: w, Height: h, Primary: true}}
	}
	return nil
}
