package main

import (
	"image"
	"testing"
)

func TestSelectScreenRect(t *testing.T) {
	left := screenRect{X: -1920, Y: 0, Width: 1920, Height: 1080}
	primary := screenRect{X: 0, Y: 0, Width: 2560, Height: 1440, Primary: true}
	virtual := screenRect{X: -1920, Y: 0, Width: 4480, Height: 1440}

	cases := []struct {
		name       string
		rects      []screenRect
		virtual    screenRect
		hasVirtual bool
		display    int
		want       screenRect
		wantOK     bool
	}{
		{"all displays", []screenRect{left, primary}, virtual, true, 0, virtual, true},
		{"negative is all displays", []screenRect{left, primary}, virtual, true, -1, virtual, true},
		{"first monitor", []screenRect{left, primary}, virtual, true, 1, left, true},
		{"second monitor", []screenRect{left, primary}, virtual, true, 2, primary, true},
		{"missing monitor", []screenRect{left, primary}, virtual, true, 3, screenRect{}, false},
		{"no enumeration falls back to the virtual desktop", nil, virtual, true, 0, virtual, true},
		{"no enumeration cannot pick a monitor", nil, virtual, true, 1, screenRect{}, false},
		{"no virtual desktop uses the first monitor", []screenRect{primary}, screenRect{}, false, 0, primary, true},
		{"nothing known", nil, screenRect{}, false, 0, screenRect{}, false},
		{"degenerate rect rejected", []screenRect{{X: 0, Y: 0, Width: 0, Height: 10}}, virtual, true, 1, screenRect{}, false},
	}
	for _, tc := range cases {
		got, ok := selectScreenRect(tc.rects, tc.virtual, tc.hasVirtual, tc.display)
		if ok != tc.wantOK || (ok && got != tc.want) {
			t.Fatalf("%s: got %+v ok=%v, want %+v ok=%v", tc.name, got, ok, tc.want, tc.wantOK)
		}
	}
}

func TestSortScreenRectsReadingOrder(t *testing.T) {
	rects := []screenRect{
		{X: 1920, Y: 0, Width: 1920, Height: 1080},             // top-right
		{X: 0, Y: 1080, Width: 1280, Height: 1024},             // bottom-left
		{X: 0, Y: 0, Width: 1920, Height: 1080, Primary: true}, // top-left
	}
	sortScreenRects(rects)
	if rects[0].X != 0 || rects[0].Y != 0 {
		t.Fatalf("first = %+v, want the top-left monitor", rects[0])
	}
	if rects[1].X != 1920 || rects[1].Y != 0 {
		t.Fatalf("second = %+v, want the top-right monitor", rects[1])
	}
	if rects[2].Y != 1080 {
		t.Fatalf("third = %+v, want the bottom monitor", rects[2])
	}
	if !rects[0].Primary {
		t.Fatal("the primary flag was lost in sorting")
	}
}

func TestDisplayDescriptorsAreOneBased(t *testing.T) {
	descriptors := displayDescriptors([]screenRect{
		{X: 0, Y: 0, Width: 1920, Height: 1080, Primary: true},
		{X: 1920, Y: 0, Width: 1280, Height: 1024},
	})
	if len(descriptors) != 2 {
		t.Fatalf("descriptors = %d, want 2", len(descriptors))
	}
	if descriptors[0].Index != 1 || descriptors[1].Index != 2 {
		t.Fatalf("indices = %d,%d, want 1,2 (the console sends 1-based values)", descriptors[0].Index, descriptors[1].Index)
	}
	if !descriptors[0].Primary || descriptors[1].Primary {
		t.Fatalf("primary flags = %+v", descriptors)
	}
}

// fillTestImage paints every pixel with its x coordinate so a crop's offset is
// observable.
func fillTestImage(w, h int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, image.White)
			img.Pix[y*img.Stride+x*4] = uint8(x) // red channel = column
		}
	}
	return img
}

func TestCropScreenToRect(t *testing.T) {
	virtual := screenRect{X: 0, Y: 0, Width: 8, Height: 8}
	img := fillTestImage(8, 8)

	// Second monitor on the right: pixel 4 of the capture.
	cropped := cropScreenToRect(img, virtual, screenRect{X: 4, Y: 0, Width: 4, Height: 8})
	if got := cropped.Bounds(); got.Min.X != 4 || got.Max.X != 8 {
		t.Fatalf("crop bounds = %v, want x 4..8", got)
	}
	if got := cropped.Pix[cropped.PixOffset(4, 0)]; got != 4 {
		t.Fatalf("first cropped column = %d, want 4", got)
	}

	// A monitor left of the primary origin sits at pixel 0 of the capture even
	// though its desktop X is negative.
	leftVirtual := screenRect{X: -4, Y: 0, Width: 8, Height: 8}
	left := cropScreenToRect(img, leftVirtual, screenRect{X: -4, Y: 0, Width: 4, Height: 8})
	if got := left.Bounds(); got.Min.X != 0 || got.Max.X != 4 {
		t.Fatalf("left monitor crop = %v, want x 0..4", got)
	}

	// The whole area is returned unchanged when the rect covers the capture.
	if got := cropScreenToRect(img, virtual, virtual); got != img {
		t.Fatal("a full-area crop should not allocate a sub-image")
	}
	// A rect outside the capture cannot panic or return an empty image.
	if got := cropScreenToRect(img, virtual, screenRect{X: 40, Y: 40, Width: 4, Height: 4}); got == nil {
		t.Fatal("out-of-range crop returned nil")
	}
}

// xrandr --query for a two-monitor desktop with one disconnected output. The
// primary sits at +0+0, the secondary to its right, and DP-3 is connected but
// has no geometry (xrandr prints "connected (normal left inverted right x
// axis y axis)" without a mode).
const xrandrSample = `Screen 0: minimum 8 x 8, current 4480 x 1440, maximum 32767 x 32767
eDP-1 connected primary 2560x1440+0+0 (normal left inverted right x axis y axis) 344mm x 194mm
   2560x1440     60.00*+
HDMI-1 connected 1920x1080+2560+0 (normal left inverted right x axis y axis) 527mm x 296mm
   1920x1080     60.00*+
DP-3 connected (normal left inverted right x axis y axis)
DP-4 disconnected (normal left inverted right x axis y axis)
`

func TestParseXRandRMonitors(t *testing.T) {
	rects := parseXRandRMonitors(xrandrSample)
	if len(rects) != 2 {
		t.Fatalf("rects = %d, want 2 connected outputs with geometry (%+v)", len(rects), rects)
	}
	if rects[0].X != 0 || rects[0].Width != 2560 || !rects[0].Primary {
		t.Fatalf("first monitor = %+v, want the primary 2560x1440 at 0,0", rects[0])
	}
	if rects[1].X != 2560 || rects[1].Width != 1920 || rects[1].Primary {
		t.Fatalf("second monitor = %+v, want 1920x1080 at x=2560", rects[1])
	}
	if got := parseXRandRMonitors("Screen 0: minimum 8 x 8\n"); len(got) != 0 {
		t.Fatalf("no outputs parsed as %+v", got)
	}
}

func TestParseDisplayArg(t *testing.T) {
	cases := map[string]int{
		"":                        0,
		"display=2":               2,
		"5,medium,1280,display=3": 3,
		"display=2;quality=80":    2,
		"DISPLAY=4":               4,
		"display=99":              0,
		"display=x":               0,
		"quality=80":              0,
	}
	for command, want := range cases {
		if got := parseDisplayArg(command); got != want {
			t.Fatalf("parseDisplayArg(%q) = %d, want %d", command, got, want)
		}
	}
}
