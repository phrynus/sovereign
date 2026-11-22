package server

import (
	"strconv"
	"testing"
	"time"
)

// TestBeaconDedupEvictsOldestFirst pins the FIFO contract that replaced the
// per-beacon O(cache) oldest scan: once the cache reaches the cap, the keys
// inserted first are the ones that make room, and the frame being processed is
// inserted after the trim so it is never the eviction victim.
func TestBeaconDedupEvictsOldestFirst(t *testing.T) {
	s := &Server{beaconDedupCache: make(map[string]time.Time, MaxBeaconDedupEntries+1)}

	first := beaconRequest{UUID: "fifo-agent", Seq: 1}
	firstFP := beaconFingerprint(first)
	s.isDuplicateBeacon(first)
	for i := 2; i <= MaxBeaconDedupEntries; i++ {
		s.isDuplicateBeacon(beaconRequest{UUID: "fifo-agent", Seq: uint64(i)})
	}
	if got := len(s.beaconDedupCache); got != MaxBeaconDedupEntries {
		t.Fatalf("cache at cap holds %d entries, want %d", got, MaxBeaconDedupEntries)
	}

	// One past the cap: exactly one key is evicted, and it is the oldest.
	last := beaconRequest{UUID: "fifo-agent", Seq: MaxBeaconDedupEntries + 1}
	s.isDuplicateBeacon(last)
	if got := len(s.beaconDedupCache); got > MaxBeaconDedupEntries {
		t.Fatalf("cache grew to %d, cap is %d", got, MaxBeaconDedupEntries)
	}
	s.beaconDedupMu.Lock()
	_, oldestStillThere := s.beaconDedupCache[firstFP]
	_, newestThere := s.beaconDedupCache[beaconFingerprint(last)]
	s.beaconDedupMu.Unlock()
	if oldestStillThere {
		t.Fatal("the oldest key survived eviction")
	}
	if !newestThere {
		t.Fatal("the frame being processed was evicted by its own insert")
	}
}

// TestEvictDedupOldestSkipsReinsertedKey proves a re-inserted key is not
// deleted by the stale queue entry left by its first insertion.
func TestEvictDedupOldestSkipsReinsertedKey(t *testing.T) {
	now := time.Now()
	cache := map[string]time.Time{
		"a": now,           // re-inserted after the queue entry below
		"b": now.Add(-1e6), // older insertion
	}
	order := []dedupStamp{
		{key: "a", at: now.Add(-2 * time.Second)}, // stale entry for the first insert
		{key: "b", at: now.Add(-1e6)},
		{key: "a", at: now},
	}
	head := 0
	cache["c"] = now // simulate the insert that happens after the trim
	evictDedupOldest(cache, &order, &head, 2)

	if _, ok := cache["a"]; !ok {
		t.Fatal("the re-inserted key was evicted by its stale queue entry")
	}
	if _, ok := cache["b"]; ok {
		t.Fatal("the older key was not evicted")
	}
	if len(cache) > 2 {
		t.Fatalf("cache holds %d entries, limit is 2", len(cache))
	}
}

// TestEvictDedupOldestFallsBackWhenQueueEmpty keeps the memory bound honest
// when entries were inserted outside isDuplicateBeacon (a test fixture, a
// restored snapshot): the queue cannot free them, so the map is trimmed.
func TestEvictDedupOldestFallsBackWhenQueueEmpty(t *testing.T) {
	cache := make(map[string]time.Time, 5)
	for i := range 5 {
		cache["k"+strconv.Itoa(i)] = time.Now()
	}
	var order []dedupStamp
	head := 0
	evictDedupOldest(cache, &order, &head, 3)
	if len(cache) != 3 {
		t.Fatalf("cache holds %d entries, limit is 3", len(cache))
	}
}

// TestResultDedupEvictsOldestFirst is the same contract for the result cache.
func TestResultDedupEvictsOldestFirst(t *testing.T) {
	s := &Server{resultDedupeCache: make(map[string]time.Time, MaxBeaconDedupEntries+1)}
	firstKey := "fifo-agent/rid-1"
	s.isDuplicateResult("fifo-agent", taskResult{ResultID: "rid-1"})
	for i := 2; i <= MaxBeaconDedupEntries; i++ {
		s.isDuplicateResult("fifo-agent", taskResult{ResultID: "rid-" + strconv.Itoa(i)})
	}
	s.isDuplicateResult("fifo-agent", taskResult{ResultID: "rid-fresh"})

	s.resultDedupeMu.Lock()
	_, oldestStillThere := s.resultDedupeCache[firstKey]
	_, newestThere := s.resultDedupeCache["fifo-agent/rid-fresh"]
	size := len(s.resultDedupeCache)
	s.resultDedupeMu.Unlock()
	if oldestStillThere {
		t.Fatal("the oldest result id survived eviction")
	}
	if !newestThere {
		t.Fatal("the result being processed was evicted by its own insert")
	}
	if size > MaxBeaconDedupEntries {
		t.Fatalf("result cache grew to %d, cap is %d", size, MaxBeaconDedupEntries)
	}
}
