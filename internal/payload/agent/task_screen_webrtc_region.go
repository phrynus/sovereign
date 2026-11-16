package main

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image"
	"image/jpeg"
	"time"
)

const (
	// regionTileSize is the refresh granularity. 64 px keeps a single tile's
	// JPEG small (~1-2 KiB) while still covering typical UI deltas (a caret,
	// a text field, a button hover) with one or two tiles.
	regionTileSize = 64
	// regionTileThreshold is the per-pixel difference (sum of |ΔR|+|ΔG|+|ΔB|,
	// 0-765) at which a pixel counts as changed. It sits above capture noise.
	regionTileThreshold = 24
	// regionMinChangedFrac is the fraction of a tile that must change before
	// the tile is re-sent. A caret or a one-pixel border is ~0.5% of a 64x64
	// tile, so 0.2% keeps small UI edits visible; noise never reaches the
	// per-pixel threshold at all.
	regionMinChangedFrac = 0.002
	// regionMaxFrameBytes bounds one tiles frame. A change larger than this is
	// sent as a single full-frame JPEG instead - cheaper than hundreds of
	// tile headers, and the browser repaints everything either way.
	regionMaxFrameBytes = 256 << 10
	// regionFPS is the sampling rate. Screen content changes far slower than
	// video, and a static screen costs nothing (no frame at all).
	regionFPS         = 10
	regionJPEGQuality = 60
	regionFullQuality = 55
)

// flattenRGBA returns a packed, zero-based copy of img's pixels.
//
// A cropped capture is a SubImage: it keeps the PARENT stride and a non-zero
// Rect origin. Diffing and encoding such an image with width-based indexing
// reads the wrong rows, and tile rectangles in 0..width do not intersect its
// bounds at all. Normalising once per frame keeps both operations honest.
func flattenRGBA(img *image.RGBA) *image.RGBA {
	if img == nil {
		return nil
	}
	bounds := img.Bounds()
	w, h := bounds.Dx(), bounds.Dy()
	if w <= 0 || h <= 0 {
		return nil
	}
	if bounds.Min == (image.Point{}) && img.Stride == w*4 {
		return img
	}
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		src := img.PixOffset(bounds.Min.X, bounds.Min.Y+y)
		copy(dst.Pix[y*dst.Stride:y*dst.Stride+w*4], img.Pix[src:src+w*4])
	}
	return dst
}

// tileRect is one changed tile, clipped to the image.
type tileRect struct {
	X int `json:"x"`
	Y int `json:"y"`
	W int `json:"w"`
	H int `json:"h"`
}

// tileDiffRects compares two RGBA buffers of the same size and returns the
// tiles whose mean absolute per-channel difference reaches threshold.
//
// This is the change-detection math the whole region mode rests on: unchanged
// tiles are not encoded at all, so bandwidth tracks the changed AREA instead of
// the frame size. Sampling every other pixel keeps a 1080p comparison under a
// few hundred microseconds; the threshold is far above any aliasing error it
// introduces.
func tileDiffRects(prev, cur []byte, width, height, tile, threshold int) []tileRect {
	if tile <= 0 || width <= 0 || height <= 0 || threshold < 0 {
		return nil
	}
	if len(prev) < width*height*4 || len(cur) < width*height*4 {
		return nil
	}
	var rects []tileRect
	for ty := 0; ty < height; ty += tile {
		th := tile
		if ty+th > height {
			th = height - ty
		}
		for tx := 0; tx < width; tx += tile {
			tw := tile
			if tx+tw > width {
				tw = width - tx
			}
			if tileChanged(prev, cur, width, tx, ty, tw, th, threshold) {
				rects = append(rects, tileRect{X: tx, Y: ty, W: tw, H: th})
			}
		}
	}
	return rects
}

// tileChanged reports whether the fraction of changed pixels inside one tile
// reaches regionMinChangedFrac. Counting CHANGED PIXELS (not a tile-wide mean)
// is what keeps a caret, a one-pixel border or a small icon visible: a mean
// over 4096 pixels would drown a 20-pixel edit.
func tileChanged(prev, cur []byte, stride, x0, y0, w, h, threshold int) bool {
	if threshold <= 0 {
		return !bytes.Equal(prev[tileIndex(stride, x0, y0):tileIndex(stride, x0+w-1, y0+h-1)+4],
			cur[tileIndex(stride, x0, y0):tileIndex(stride, x0+w-1, y0+h-1)+4])
	}
	changed := 0
	samples := 0
	for y := y0; y < y0+h; y++ {
		row := tileIndex(stride, x0, y)
		for x := x0; x < x0+w; x++ {
			i := row + (x-x0)*4
			if absInt(int(prev[i])-int(cur[i]))+
				absInt(int(prev[i+1])-int(cur[i+1]))+
				absInt(int(prev[i+2])-int(cur[i+2])) >= threshold {
				changed++
			}
			samples++
		}
	}
	if samples == 0 {
		return false
	}
	return float64(changed)/float64(samples) >= regionMinChangedFrac
}

func tileIndex(stride, x, y int) int { return (y*stride + x) * 4 }

func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// regionTilePayload is one encoded tile. Coordinates are in the captured
// image's pixel space; the browser draws it at that position.
type regionTilePayload struct {
	X    int    `json:"x"`
	Y    int    `json:"y"`
	W    int    `json:"w"`
	H    int    `json:"h"`
	Data string `json:"data"`
}

// encodeRegionTiles JPEG-encodes the changed tiles, and falls back to a single
// full-frame JPEG when the tile payload would exceed the frame budget. It
// returns the frame shape the console needs to place the tiles.
func encodeRegionTiles(img *image.RGBA, rects []tileRect) (tiles []regionTilePayload, full bool, err error) {
	if img == nil {
		return nil, false, fmt.Errorf("no frame")
	}
	bounds := img.Bounds()
	encode := func(r image.Rectangle) (string, error) {
		var buf bytes.Buffer
		if err := jpeg.Encode(&buf, img.SubImage(r), &jpeg.Options{Quality: regionJPEGQuality}); err != nil {
			return "", err
		}
		return base64.StdEncoding.EncodeToString(buf.Bytes()), nil
	}

	total := 0
	for _, rect := range rects {
		sub := image.Rect(rect.X, rect.Y, rect.X+rect.W, rect.Y+rect.H).Intersect(bounds)
		if sub.Empty() {
			continue
		}
		// A single rect covering the whole capture IS a full frame: say so, so
		// the browser repaints everything instead of compositing one giant
		// tile onto stale pixels.
		whole := sub == bounds && len(rects) == 1
		data, encErr := encode(sub)
		if encErr != nil {
			return nil, false, encErr
		}
		if whole {
			full = true
		}
		total += len(data)
		if total > regionMaxFrameBytes {
			// Too much changed at once: one full frame is smaller than the
			// headers and lets the browser repaint in one go.
			return encodeRegionFull(img)
		}
		tiles = append(tiles, regionTilePayload{X: sub.Min.X, Y: sub.Min.Y, W: sub.Dx(), H: sub.Dy(), Data: data})
	}
	return tiles, full, nil
}

// encodeRegionFull encodes the whole capture as one tile, with a lower quality
// because it is the expensive case (a full screen change).
func encodeRegionFull(img *image.RGBA) ([]regionTilePayload, bool, error) {
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: regionFullQuality}); err != nil {
		return nil, false, err
	}
	bounds := img.Bounds()
	return []regionTilePayload{{
		X: bounds.Min.X, Y: bounds.Min.Y, W: bounds.Dx(), H: bounds.Dy(),
		Data: base64.StdEncoding.EncodeToString(buf.Bytes()),
	}}, true, nil
}

// startRegionPipeline switches from H.264 video to change-only tile refresh.
// The tiles travel over the signaling socket, so no RTP/media plane is used.
func (s *agentWebRTCSession) startRegionPipeline() error {
	if s.ctx.Err() != nil {
		return fmt.Errorf("session is already closed")
	}
	s.regionMu.Lock()
	if s.regionOn {
		s.regionMu.Unlock()
		return nil
	}
	s.regionOn = true
	gen := s.regionGen
	s.regionMu.Unlock()

	// Video and tiles are mutually exclusive: both repaint the same canvas and
	// running ffmpeg for nothing wastes the target's CPU.
	s.stopCapture()
	go s.regionLoop(gen)
	_ = s.sendJSON(map[string]any{"type": "state", "state": "region"})
	return nil
}

// stopRegionPipeline stops the tile loop and (when a video session is alive)
// restores the H.264 stream.
func (s *agentWebRTCSession) stopRegionPipeline(restoreVideo bool) {
	s.regionMu.Lock()
	wasOn := s.regionOn
	s.regionOn = false
	s.regionGen++
	s.regionMu.Unlock()
	if !wasOn {
		return
	}
	_ = s.sendJSON(map[string]any{"type": "state", "state": "region_stopped"})
	if restoreVideo && s.ctx.Err() == nil {
		if err := s.startRTPPipeline(); err != nil {
			_ = s.sendJSON(map[string]any{"type": "error", "reason": "video restart: " + err.Error()})
			s.stop("video restart failed")
		}
	}
}

// setRegion switches the capture mode live.
func (s *agentWebRTCSession) setRegion(enabled bool) {
	s.settingsMu.Lock()
	changed := s.settings.Region != enabled
	s.settings.Region = enabled
	s.settingsMu.Unlock()
	_ = s.sendJSON(map[string]any{"type": "region", "enabled": enabled})
	if !changed {
		return
	}
	if enabled {
		if err := s.startRegionPipeline(); err != nil {
			_ = s.sendJSON(map[string]any{"type": "region", "enabled": false, "reason": err.Error()})
		}
		return
	}
	s.stopRegionPipeline(true)
}

// regionActive reports whether the tile loop owns the session's capture.
func (s *agentWebRTCSession) regionActive() bool {
	s.regionMu.Lock()
	defer s.regionMu.Unlock()
	return s.regionOn
}

// regionLoop captures at regionFPS, diffs against the previous frame and sends
// only the changed tiles.
func (s *agentWebRTCSession) regionLoop(gen int) {
	ticker := time.NewTicker(time.Second / regionFPS)
	defer ticker.Stop()

	sendFrame := func(img *image.RGBA, rects []tileRect) bool {
		tiles, full, err := encodeRegionTiles(img, rects)
		if err != nil || len(tiles) == 0 {
			return false
		}
		if err := s.sendJSON(map[string]any{
			"type":   "tiles",
			"width":  img.Bounds().Dx(),
			"height": img.Bounds().Dy(),
			"tile":   regionTileSize,
			"full":   full,
			"tiles":  tiles,
		}); err != nil {
			return false
		}
		return true
	}

	var prev []byte
	first := true
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
		}
		s.regionMu.Lock()
		if !s.regionOn || s.regionGen != gen {
			s.regionMu.Unlock()
			return
		}
		if s.regionReset {
			s.regionReset = false
			prev = nil
			first = true
		}
		s.regionMu.Unlock()

		raw, err := captureScreenForDisplay(s.settingsSnapshot().Display)
		if err != nil {
			continue
		}
		img := flattenRGBA(raw)
		if img == nil {
			continue
		}
		bounds := img.Bounds()
		w, h := bounds.Dx(), bounds.Dy()
		if first || prev == nil || len(prev) != len(img.Pix) {
			// Advance the baseline only when the frame was actually sent: a
			// failed write must be retried, not silently acknowledged.
			if sendFrame(img, []tileRect{{X: 0, Y: 0, W: w, H: h}}) {
				prev = append(prev[:0], img.Pix...)
				first = false
			}
			continue
		}
		rects := tileDiffRects(prev, img.Pix, w, h, regionTileSize, regionTileThreshold)
		if len(rects) == 0 {
			continue
		}
		if sendFrame(img, rects) {
			copy(prev, img.Pix)
		}
	}
}
