//go:build linux || windows || darwin
// +build linux windows darwin

package main

import (
	"strings"
	"testing"
	"time"
)

func resetLinkState() {
	linkState.mu.Lock()
	linkState.rtts = nil
	linkState.lossEWMA = 0
	linkState.total = 0
	linkState.mu.Unlock()
}

func TestLinkQualityScore(t *testing.T) {
	// No completed exchange must be neutral, not "good" or "bad".
	neutral := linkQualityScore(linkStats{})
	if neutral != linkNeutralScore {
		t.Fatalf("empty score = %d, want %d", neutral, linkNeutralScore)
	}
	fast := linkQualityScore(linkStats{Samples: 8, Median: 120 * time.Millisecond})
	slow := linkQualityScore(linkStats{Samples: 8, Median: 6 * time.Second})
	if fast <= slow {
		t.Fatalf("score not RTT-monotonic: fast=%d slow=%d", fast, slow)
	}
	lost := linkQualityScore(linkStats{Samples: 8, Median: 120 * time.Millisecond, LossEWMA: 0.8})
	if lost >= fast {
		t.Fatalf("loss did not lower score: %d -> %d", fast, lost)
	}
	worst := linkQualityScore(linkStats{Samples: 8, Median: 30 * time.Second, LossEWMA: 1})
	if worst != 0 {
		t.Fatalf("worst score = %d, want 0", worst)
	}
}

func TestLinkQualityScaleBounds(t *testing.T) {
	for score := 0; score <= 100; score++ {
		scale := linkQualityScale(score)
		if scale < 0.8 || scale > 1.8 {
			t.Fatalf("score %d produced runaway scale %v", score, scale)
		}
	}
	if got := linkQualityScale(linkNeutralScore); got != 1.0 {
		t.Fatalf("neutral scale = %v, want 1.0", got)
	}
	if linkQualityScale(95) >= 1 || linkQualityScale(10) <= 1 {
		t.Fatal("scale not ordered good -> bad")
	}
}

// TestObserveBeaconResultAdapts drives the real state machine: a healthy link
// tightens the sleep, then a saturated/lossy one lengthens it, with the ring
// bounded to the last few samples.
func TestObserveBeaconResultAdapts(t *testing.T) {
	resetLinkState()
	t.Cleanup(resetLinkState)

	if got := currentLinkScale(); got != 1 {
		t.Fatalf("fresh agent scale = %v, want neutral 1", got)
	}
	for i := 0; i < 12; i++ {
		observeBeaconResult(80*time.Millisecond, true)
	}
	stats := snapshotLink()
	if stats.Samples != linkQualitySamples || stats.Total != 12 {
		t.Fatalf("ring not bounded: samples=%d total=%d", stats.Samples, stats.Total)
	}
	if good := currentLinkScale(); good >= 1 {
		t.Fatalf("healthy link did not tighten sleep: scale=%v", good)
	}

	// A bad stretch must override the healthy history: 8 slow successes plus
	// repeated transport failures push both median RTT and loss EWMA up.
	for i := 0; i < 20; i++ {
		observeBeaconResult(7*time.Second, i%2 == 0)
	}
	if stats = snapshotLink(); stats.LossEWMA < 0.3 {
		t.Fatalf("loss EWMA too low after failures: %v", stats.LossEWMA)
	}
	if bad := currentLinkScale(); bad <= 1.2 {
		t.Fatalf("degraded link did not back off: scale=%v", bad)
	}

	// Recovery is possible without a restart.
	for i := 0; i < 30; i++ {
		observeBeaconResult(60*time.Millisecond, true)
	}
	if recovered := currentLinkScale(); recovered > 1 {
		t.Fatalf("link did not recover: scale=%v", recovered)
	}
}

func TestVideoBitrateForLink(t *testing.T) {
	const base = 2500
	// No evidence or a healthy link keeps the operator's preset.
	if got := videoBitrateForLink(base, linkStats{}); got != base {
		t.Fatalf("no-sample bitrate = %d, want %d", got, base)
	}
	if got := videoBitrateForLink(base, linkStats{Samples: 8, Median: 80 * time.Millisecond}); got != base {
		t.Fatalf("healthy bitrate = %d, want %d", got, base)
	}
	medium := videoBitrateForLink(base, linkStats{Samples: 8, Median: 2 * time.Second})
	if medium != 1875 { // 75% of 2500
		t.Fatalf("medium bitrate = %d, want 1875", medium)
	}
	poor := videoBitrateForLink(base, linkStats{Samples: 8, Median: 9 * time.Second, LossEWMA: 0.5})
	if poor != 1250 { // 50% of 2500
		t.Fatalf("poor bitrate = %d, want 1250", poor)
	}
	if got := videoBitrateForLink(600, linkStats{Samples: 8, Median: 9 * time.Second}); got != 500 {
		t.Fatalf("floor = %d, want 500", got)
	}
	if got := videoBitrateForLink(9000, linkStats{Samples: 8, Median: 100 * time.Millisecond}); got != 8000 {
		t.Fatalf("ceiling = %d, want 8000", got)
	}

	// The adapted value actually reaches the encoder arguments.
	settings := webRTCSettings{Width: 1280, Height: 720, FPS: 30, Encoder: "libx264", BitrateKbps: poor}
	args, err := buildWebcamFFmpegArgsFor("linux", settings, 41000, "/dev/video0")
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if !strings.Contains(strings.Join(args, " "), "-b:v 1250k") {
		t.Fatalf("encoder did not get the adapted bitrate: %v", args)
	}
}

func TestUDPFragmentPayloadFromLink(t *testing.T) {
	resetLinkState()
	t.Cleanup(resetLinkState)

	// No evidence: stay at the conservative default.
	if got := udpFragmentPayload(snapshotLink()); got != udpFragSmall {
		t.Fatalf("no-sample size = %d, want %d", got, udpFragSmall)
	}
	for i := 0; i < linkQualitySamples; i++ {
		observeBeaconResult(60*time.Millisecond, true)
	}
	if got := udpFragmentPayload(snapshotLink()); got != udpFragLarge {
		t.Fatalf("healthy link size = %d, want %d", got, udpFragLarge)
	}
	// A middle-aged link (median ~2 s, no loss) uses the medium part size.
	for i := 0; i < linkQualitySamples; i++ {
		observeBeaconResult(2*time.Second, true)
	}
	if got := udpFragmentPayload(snapshotLink()); got != udpFragMedium {
		t.Fatalf("medium link size = %d, want %d", got, udpFragMedium)
	}
	// A degraded link drops back to 512 B parts.
	for i := 0; i < linkQualitySamples; i++ {
		observeBeaconResult(9*time.Second, i%2 == 0)
	}
	if got := udpFragmentPayload(snapshotLink()); got != udpFragSmall {
		t.Fatalf("degraded link size = %d, want %d", got, udpFragSmall)
	}
}
