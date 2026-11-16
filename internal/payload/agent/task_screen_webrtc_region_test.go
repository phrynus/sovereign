package main

import (
	"image"
	"testing"
)

// solidFrame builds an RGBA frame with one rectangle painted a different
// colour, so the tile math has something deterministic to compare.
func solidFrame(w, h int, box image.Rectangle) []byte {
	pix := make([]byte, w*h*4)
	for i := 0; i < len(pix); i += 4 {
		pix[i], pix[i+1], pix[i+2], pix[i+3] = 16, 24, 32, 255
	}
	for y := box.Min.Y; y < box.Max.Y; y++ {
		for x := box.Min.X; x < box.Max.X; x++ {
			i := (y*w + x) * 4
			pix[i], pix[i+1], pix[i+2] = 200, 60, 40
		}
	}
	return pix
}

// TestTileDiffRects pins the change-detection math: identical frames produce no
// tiles, a small edit produces exactly the tiles it touches, and a screen-wide
// change produces the whole grid.
func TestTileDiffRects(t *testing.T) {
	const w, h, tile = 256, 128, 64
	base := solidFrame(w, h, image.Rect(0, 0, 0, 0))

	if rects := tileDiffRects(base, base, w, h, tile, regionTileThreshold); len(rects) != 0 {
		t.Fatalf("identical frames produced %d tiles", len(rects))
	}

	// A 40x20 box inside tile (64,64) touches exactly one tile.
	edited := solidFrame(w, h, image.Rect(70, 70, 110, 90))
	rects := tileDiffRects(base, edited, w, h, tile, regionTileThreshold)
	if len(rects) != 1 {
		t.Fatalf("one-tile edit produced %d tiles: %+v", len(rects), rects)
	}
	if rects[0] != (tileRect{X: 64, Y: 64, W: 64, H: 64}) {
		t.Fatalf("changed tile = %+v, want 64,64,64,64", rects[0])
	}

	// A box straddling the 64-px boundary touches four tiles.
	straddle := solidFrame(w, h, image.Rect(50, 50, 80, 80))
	if got := tileDiffRects(base, straddle, w, h, tile, regionTileThreshold); len(got) != 4 {
		t.Fatalf("straddling edit produced %d tiles: %+v", len(got), got)
	}

	// A full-screen change touches every tile (4x2 here).
	full := solidFrame(w, h, image.Rect(0, 0, w, h))
	if got := tileDiffRects(base, full, w, h, tile, regionTileThreshold); len(got) != (w/tile)*(h/tile) {
		t.Fatalf("full change produced %d tiles, want %d", len(got), (w/tile)*(h/tile))
	}

	// Degenerate inputs are refused rather than panicking.
	if got := tileDiffRects(nil, full, w, h, tile, regionTileThreshold); got != nil {
		t.Fatalf("nil buffer produced %+v", got)
	}
	if got := tileDiffRects(base, full, w, h, 0, regionTileThreshold); got != nil {
		t.Fatalf("zero tile size produced %+v", got)
	}
}

// TestEncodeRegionTiles proves the payload rule: small changes become a few
// tiles, a full-screen change becomes ONE full frame (cheaper than hundreds of
// tile headers), and the frame fits the signaling budget.
func TestEncodeRegionTiles(t *testing.T) {
	img := &image.RGBA{Pix: solidFrame(1280, 720, image.Rect(0, 0, 0, 0)), Stride: 1280 * 4, Rect: image.Rect(0, 0, 1280, 720)}
	small := []tileRect{{X: 64, Y: 64, W: 64, H: 64}, {X: 128, Y: 64, W: 64, H: 64}}
	tiles, full, err := encodeRegionTiles(img, small)
	if err != nil || full {
		t.Fatalf("small change: full=%v err=%v", full, err)
	}
	if len(tiles) != 2 || tiles[0].W != 64 || tiles[0].Data == "" {
		t.Fatalf("small change tiles = %+v", tiles)
	}

	// The whole screen: one full frame, marked as such.
	tiles, full, err = encodeRegionTiles(img, []tileRect{{X: 0, Y: 0, W: 1280, H: 720}})
	if err != nil || !full || len(tiles) != 1 || tiles[0].W != 1280 {
		t.Fatalf("full change: full=%v tiles=%d err=%v", full, len(tiles), err)
	}
	if got := len(tiles[0].Data); got == 0 || got > regionMaxFrameBytes {
		t.Fatalf("full frame payload = %d bytes, want 0 < n <= %d", got, regionMaxFrameBytes)
	}
}

// TestRegionModeBandwidth is the measurement behind the feature: a synthetic
// 1280x720 screen with a small moving box, compared with sending whole frames.
// Static content emits nothing; a 120x40 box costs two tiles per tick.
func TestRegionModeBandwidth(t *testing.T) {
	const w, h = 1280, 720
	base := solidFrame(w, h, image.Rect(0, 0, 0, 0))
	moved := solidFrame(w, h, image.Rect(200, 300, 320, 340))
	img := &image.RGBA{Pix: moved, Stride: w * 4, Rect: image.Rect(0, 0, w, h)}

	rects := tileDiffRects(base, moved, w, h, regionTileSize, regionTileThreshold)
	// A 120x40 box at (200,300) spans two tile columns and two tile rows.
	if len(rects) != 4 {
		t.Fatalf("120x40 box touched %d tiles, want 4", len(rects))
	}
	tiles, _, err := encodeRegionTiles(img, rects)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	regionBytes := 0
	for _, tile := range tiles {
		regionBytes += len(tile.Data)
	}

	// One whole frame at the same quality, for comparison.
	fullTiles, _, err := encodeRegionTiles(img, []tileRect{{X: 0, Y: 0, W: w, H: h}})
	if err != nil {
		t.Fatalf("encode full: %v", err)
	}
	fullBytes := len(fullTiles[0].Data)

	if regionBytes >= fullBytes {
		t.Fatalf("region payload %d bytes is not smaller than a full frame %d", regionBytes, fullBytes)
	}
	// A static screen sends nothing at all after the first frame.
	if static := tileDiffRects(moved, moved, w, h, regionTileSize, regionTileThreshold); len(static) != 0 {
		t.Fatalf("a static screen produced %d tiles", len(static))
	}
	t.Logf("region: %d tiles = %d B per tick; full frame = %d B; at %d fps that is %.1f KiB/s vs %.1f KiB/s",
		len(rects), regionBytes, fullBytes, regionFPS,
		float64(regionBytes*regionFPS)/1024, float64(fullBytes*regionFPS)/1024)
}

// TestFlattenRGBANormalizesACroppedCapture proves the multi-monitor case: a
// SubImage keeps the parent stride and a non-zero origin, so width-based tile
// indexing would read the wrong rows without normalisation.
func TestFlattenRGBANormalizesACroppedCapture(t *testing.T) {
	parent := image.NewRGBA(image.Rect(0, 0, 8, 4))
	for y := 0; y < 4; y++ {
		for x := 0; x < 8; x++ {
			i := parent.PixOffset(x, y)
			parent.Pix[i] = uint8(x * 10)
			parent.Pix[i+3] = 255
		}
	}
	cropped := parent.SubImage(image.Rect(4, 0, 8, 4)).(*image.RGBA)
	if cropped.Stride != parent.Stride {
		t.Fatalf("test premise: SubImage stride = %d, parent = %d", cropped.Stride, parent.Stride)
	}
	flat := flattenRGBA(cropped)
	if flat.Stride != 4*4 {
		t.Fatalf("flattened stride = %d, want %d", flat.Stride, 4*4)
	}
	if flat.Rect.Min != (image.Point{}) {
		t.Fatalf("flattened origin = %v, want 0,0", flat.Rect.Min)
	}
	// Column 0 of the crop is parent column 4 (value 40).
	if got := flat.Pix[flat.PixOffset(0, 0)]; got != 40 {
		t.Fatalf("flattened pixel = %d, want 40", got)
	}
	if got := flat.Pix[flat.PixOffset(3, 3)]; got != 70 {
		t.Fatalf("flattened last pixel = %d, want 70", got)
	}
	// An already-packed image is returned as-is (no per-frame copy cost).
	whole := flattenRGBA(parent)
	if whole != parent {
		t.Fatal("a packed full image was copied unnecessarily")
	}
	if flattenRGBA(nil) != nil {
		t.Fatal("nil input produced a frame")
	}
}

// TestRegionDiffOnACroppedCapture is the end-to-end version: two cropped
// captures of the same monitor diff to nothing, and one changed pixel is
// reported against the CROP's coordinates, not the parent's.
func TestRegionDiffOnACroppedCapture(t *testing.T) {
	const padW, cropW, h = 100, 64, 64
	left := image.NewRGBA(image.Rect(0, 0, padW+cropW, h))
	right := image.NewRGBA(image.Rect(0, 0, padW+cropW, h))
	for i := 0; i < len(left.Pix); i += 4 {
		left.Pix[i], left.Pix[i+3] = 16, 255
		right.Pix[i], right.Pix[i+3] = 16, 255
	}
	// Paint 8x8 inside the crop (the crop starts at x=padW) on the right frame.
	for y := 10; y < 18; y++ {
		for x := padW + 10; x < padW+18; x++ {
			i := right.PixOffset(x, y)
			right.Pix[i], right.Pix[i+1], right.Pix[i+2] = 220, 40, 40
		}
	}
	prev := flattenRGBA(left.SubImage(image.Rect(padW, 0, padW+cropW, h)).(*image.RGBA))
	cur := flattenRGBA(right.SubImage(image.Rect(padW, 0, padW+cropW, h)).(*image.RGBA))
	rects := tileDiffRects(prev.Pix, cur.Pix, cropW, h, 64, regionTileThreshold)
	if len(rects) != 1 {
		t.Fatalf("cropped diff produced %d tiles: %+v", len(rects), rects)
	}
	if rects[0] != (tileRect{X: 0, Y: 0, W: 64, H: 64}) {
		t.Fatalf("changed crop tile = %+v, want the crop's own 0,0,64,64", rects[0])
	}
}
