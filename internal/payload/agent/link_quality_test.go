//go:build linux || windows || darwin
// +build linux windows darwin

package main

import (
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
