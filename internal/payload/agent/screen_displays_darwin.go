//go:build darwin
// +build darwin

package main

// nativeScreenRects is empty on macOS: the avfoundation capture path in this
// build has no per-display region selector, so the console offers only "all
// displays" and the agent refuses a specific index rather than capturing the
// wrong screen.
func nativeScreenRects() []screenRect {
	return nil
}
