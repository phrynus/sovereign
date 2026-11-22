package server

import (
	"strconv"
	"testing"
	"time"
)

// BenchmarkBeaconDedupAtCapacity measures the per-beacon cost of dedup when the
// cache is full — i.e. what a large fleet pays on every check-in, not just when
// something unusual happens. The cache is filled through isDuplicateBeacon so
// the insertion FIFO exists, which is how production fills it.
func BenchmarkBeaconDedupAtCapacity(b *testing.B) {
	s := &Server{beaconDedupCache: make(map[string]time.Time, MaxBeaconDedupEntries+1)}
	for i := range MaxBeaconDedupEntries {
		s.isDuplicateBeacon(beaconRequest{UUID: "seed-agent", Seq: uint64(i) + 1})
	}
	seq := uint64(MaxBeaconDedupEntries)
	b.ResetTimer()
	for b.Loop() {
		seq++
		s.isDuplicateBeacon(beaconRequest{UUID: "bench-agent", Seq: seq})
	}
}

// BenchmarkResultDedupAtCapacity is the same shape for the per-result cache.
func BenchmarkResultDedupAtCapacity(b *testing.B) {
	s := &Server{resultDedupeCache: make(map[string]time.Time, MaxBeaconDedupEntries+1)}
	for i := range MaxBeaconDedupEntries {
		s.isDuplicateResult("seed-agent", taskResult{ResultID: "rid-" + strconv.Itoa(i)})
	}
	n := MaxBeaconDedupEntries
	b.ResetTimer()
	for b.Loop() {
		n++
		s.isDuplicateResult("bench-agent", taskResult{ResultID: "rid-" + strconv.Itoa(n)})
	}
}
