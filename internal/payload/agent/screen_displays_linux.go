//go:build linux
// +build linux

package main

import (
	"os/exec"
	"strconv"
	"strings"
)

// nativeScreenRect returns the X11 display size used by the ffmpeg x11grab
// input. linuxScreenSize() already parses the first connected xrandr output.
func nativeScreenRect() (x, y, w, h int, ok bool) {
	size := strings.TrimSpace(linuxScreenSize())
	parts := strings.SplitN(size, "x", 2)
	if len(parts) != 2 {
		return 0, 0, 0, 0, false
	}
	width, errW := strconv.Atoi(parts[0])
	height, errH := strconv.Atoi(parts[1])
	if errW != nil || errH != nil || width <= 0 || height <= 0 {
		return 0, 0, 0, 0, false
	}
	return 0, 0, width, height, true
}

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
