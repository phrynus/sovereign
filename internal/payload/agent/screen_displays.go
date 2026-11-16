package main

import (
	"image"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// screenRect is one capture region in the coordinate space the platform's
// pointer injection uses: X/Y are the desktop origin (which is negative when a
// monitor sits left of / above the primary one) and Width/Height are pixels.
type screenRect struct {
	X       int  `json:"x"`
	Y       int  `json:"y"`
	Width   int  `json:"width"`
	Height  int  `json:"height"`
	Primary bool `json:"primary,omitempty"`
}

func (r screenRect) valid() bool { return r.Width > 0 && r.Height > 0 }

// displayDescriptor is what the agent announces to the console so a picker can
// name the monitors it may choose from.
type displayDescriptor struct {
	Index   int        `json:"index"` // 1-based, the value the operator sends back
	X       int        `json:"x"`
	Y       int        `json:"y"`
	Width   int        `json:"width"`
	Height  int        `json:"height"`
	Primary bool       `json:"primary,omitempty"`
	Virtual screenRect `json:"-"`
}

// sortScreenRects orders monitors in reading order (top row first, then left to
// right) so the index an operator sees matches a stable enumeration on every
// call. The primary flag travels separately.
func sortScreenRects(rects []screenRect) {
	sort.SliceStable(rects, func(i, j int) bool {
		if rects[i].Y != rects[j].Y {
			return rects[i].Y < rects[j].Y
		}
		return rects[i].X < rects[j].X
	})
}

// selectScreenRect resolves what to capture: display 0 (or negative) means the
// whole virtual desktop (all monitors side by side, the historical behaviour),
// and display N>=1 means monitor N in the agent's announced order.
func selectScreenRect(rects []screenRect, virtual screenRect, hasVirtual bool, display int) (screenRect, bool) {
	if display <= 0 {
		if hasVirtual && virtual.valid() {
			return virtual, true
		}
		if len(rects) > 0 && rects[0].valid() {
			return rects[0], true
		}
		return screenRect{}, false
	}
	if display > len(rects) {
		return screenRect{}, false
	}
	rect := rects[display-1]
	if !rect.valid() {
		return screenRect{}, false
	}
	return rect, true
}

// screenRectFromNative is the virtual desktop as a screenRect.
func screenRectFromNative() screenRect {
	x, y, w, h, _ := nativeScreenRect()
	return screenRect{X: x, Y: y, Width: w, Height: h}
}

// hasNativeScreenRect reports whether the platform could report a screen size.
func hasNativeScreenRect() bool {
	_, _, w, h, ok := nativeScreenRect()
	return ok && w > 0 && h > 0
}

// resolveCaptureRect is selectScreenRect against the live platform.
func resolveCaptureRect(display int) (screenRect, bool) {
	vx, vy, vw, vh, ok := nativeScreenRect()
	virtual := screenRect{X: vx, Y: vy, Width: vw, Height: vh}
	return selectScreenRect(nativeScreenRects(), virtual, ok, display)
}

// displayDescriptors renders the announced list, with the virtual-desktop size
// attached to the first entry for logging.
func displayDescriptors(rects []screenRect) []displayDescriptor {
	out := make([]displayDescriptor, 0, len(rects))
	for i, rect := range rects {
		out = append(out, displayDescriptor{
			Index:   i + 1,
			X:       rect.X,
			Y:       rect.Y,
			Width:   rect.Width,
			Height:  rect.Height,
			Primary: rect.Primary,
		})
	}
	return out
}

// xrandrGeom matches the geometry xrandr prints for a connected output, e.g.
// "1920x1080+0+0" or "2560x1440+1920+0".
var xrandrGeom = regexp.MustCompile(`(\d+)x(\d+)\+(-?\d+)\+(-?\d+)`)

// parseXRandRMonitors extracts the connected outputs from `xrandr --query`.
// A disconnected output has no geometry token and is skipped. The `primary`
// marker is captured when present. It lives here (not in the linux file) so the
// parser is testable on every platform; only the exec call is Linux-only.
func parseXRandRMonitors(output string) []screenRect {
	var rects []screenRect
	for _, line := range strings.Split(output, "\n") {
		if !strings.Contains(line, " connected") {
			continue
		}
		m := xrandrGeom.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		w, errW := strconv.Atoi(m[1])
		h, errH := strconv.Atoi(m[2])
		x, errX := strconv.Atoi(m[3])
		y, errY := strconv.Atoi(m[4])
		if errW != nil || errH != nil || errX != nil || errY != nil || w <= 0 || h <= 0 {
			continue
		}
		rects = append(rects, screenRect{
			X:       x,
			Y:       y,
			Width:   w,
			Height:  h,
			Primary: strings.Contains(line, " primary "),
		})
	}
	if len(rects) > 1 {
		sortScreenRects(rects)
	}
	return rects
}

// cropScreenToRect cuts one monitor out of a whole-virtual-desktop capture.
// The image's (0,0) pixel is the virtual origin, so the monitor's offset must
// subtract it: a second monitor at X=1920 on a desktop whose origin is 0 is
// pixel 1920, but the same monitor on a desktop whose origin is -1920 is
// pixel 3840. The rectangle is clamped to the captured area so a display that
// changed between enumeration and capture cannot panic.
func cropScreenToRect(img *image.RGBA, virtual, rect screenRect) *image.RGBA {
	bounds := img.Bounds()
	ox := rect.X - virtual.X
	oy := rect.Y - virtual.Y
	clip := image.Rect(ox, oy, ox+rect.Width, oy+rect.Height).Intersect(bounds)
	if clip.Empty() || clip == bounds {
		return img
	}
	return img.SubImage(clip).(*image.RGBA)
}
