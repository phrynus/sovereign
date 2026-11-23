package server

import (
	"testing"
	"time"
)

func TestBeaconWarnfRateLimitsPerReason(t *testing.T) {
	oldInterval := beaconWarnInterval
	beaconWarnInterval = time.Hour
	beaconWarnMu.Lock()
	clear(beaconWarnLast)
	beaconWarnMu.Unlock()
	defer func() {
		beaconWarnInterval = oldInterval
		beaconWarnMu.Lock()
		clear(beaconWarnLast)
		beaconWarnMu.Unlock()
	}()

	if !beaconWarnf("reason-a") {
		t.Fatal("first warning in an interval must be emitted")
	}
	if beaconWarnf("reason-a") {
		t.Fatal("duplicate warning was emitted inside the interval")
	}
	if !beaconWarnf("reason-b") {
		t.Fatal("a different reason must get its own budget")
	}
}
