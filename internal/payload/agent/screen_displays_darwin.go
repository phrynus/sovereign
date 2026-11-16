//go:build darwin
// +build darwin

package main

// nativeScreenRect is unavailable for the avfoundation capture path in this
// pass: it has no region selector, so the agent captures what avfoundation
// gives it and pointer input is not scaled. It lives here (not in the optional
// WebRTC module) because the screenshot path consults it in every build.
func nativeScreenRect() (x, y, w, h int, ok bool) {
	return 0, 0, 0, 0, false
}

// nativeScreenRects is empty on macOS: the avfoundation capture path in this
// build has no per-display region selector, so the console offers only "all
// displays" and the agent refuses a specific index rather than capturing the
// wrong screen.
func nativeScreenRects() []screenRect {
	return nil
}
