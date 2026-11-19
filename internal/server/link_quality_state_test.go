package server

import (
	"testing"
	"time"
)

func TestParseLinkQualityInfo(t *testing.T) {
	snap := parseLinkQualityInfo(map[string]string{
		"link_rtt_ms":  "187",
		"link_loss":    "0.125",
		"link_samples": "8",
		"link_total":   "42",
	})
	if snap.RTTCms != 187 || snap.LossEWMA != 0.125 || snap.Samples != 8 || snap.Total != 42 {
		t.Fatalf("parsed snapshot = %+v", snap)
	}
	if !snap.hasLinkQuality() {
		t.Fatal("measurement should be present")
	}

	// Missing/malformed fields yield zero, never an error, and out-of-range
	// loss is rejected rather than trusted.
	empty := parseLinkQualityInfo(map[string]string{"link_loss": "5.0", "link_rtt_ms": "-1"})
	if empty.hasLinkQuality() || empty.LossEWMA != 0 || empty.RTTCms != 0 {
		t.Fatalf("bad fields should be ignored: %+v", empty)
	}
}

func TestLinkQualityRecordGetAndSweep(t *testing.T) {
	s := &Server{}
	snap := parseLinkQualityInfo(map[string]string{"link_rtt_ms": "95", "link_samples": "8"})
	s.recordLinkQuality("agent-a", snap)

	got, ok := s.getLinkQuality("agent-a")
	if !ok || got.RTTCms != 95 {
		t.Fatalf("get = %+v, %v", got, ok)
	}
	if got.At == 0 {
		t.Fatal("snapshot was not stamped")
	}
	if _, ok := s.getLinkQuality("agent-b"); ok {
		t.Fatal("unknown agent returned a snapshot")
	}

	// A fresh snapshot survives; an old one is swept.
	cutoff := StaleMapCleanupAge
	if n := s.linkQualitySweep(cutoff); n != 0 {
		t.Fatalf("fresh snapshot swept: %d", n)
	}
	s.linkQualityMu.Lock()
	s.linkQuality["agent-a"] = linkQualitySnapshot{RTTCms: 10, At: time.Now().Add(-2 * cutoff).Unix()}
	s.linkQualityMu.Unlock()
	if n := s.linkQualitySweep(cutoff); n != 1 {
		t.Fatalf("stale snapshot not swept: %d", n)
	}
	if _, ok := s.getLinkQuality("agent-a"); ok {
		t.Fatal("stale snapshot still present")
	}
}
