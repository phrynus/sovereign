//go:build linux || windows || darwin
// +build linux windows darwin

package main

import (
	"testing"

	"github.com/r0si/sovereign/pkg/protocol"
)

func TestValidateTransportMigrationAgent(t *testing.T) {
	oldURLs, oldIdx := c2URLsSnapshot(), currentC2Idx.Load()
	t.Cleanup(func() { c2URLsStore(oldURLs, oldIdx) })
	c2URLsStore([]string{"https://c2.example/collect", "tcp://127.0.0.1:4444"}, 0)

	cases := []struct {
		m       protocol.TransportMigration
		wantErr bool
	}{
		{protocol.TransportMigration{Transport: "tcp"}, false},
		{protocol.TransportMigration{Transport: " TCP "}, false},
		{protocol.TransportMigration{Transport: "http", C2URL: "https://c2.example/x"}, false},
		{protocol.TransportMigration{Transport: "nope"}, true},
		{protocol.TransportMigration{Transport: ""}, true},
		{protocol.TransportMigration{Transport: "tcp", C2URL: "nonsense"}, true},
		{protocol.TransportMigration{Transport: "tcp", C2URL: "https://c2.example/x"}, true},
	}
	for _, c := range cases {
		got, err := validateTransportMigration(&c.m)
		if (err != nil) != c.wantErr {
			t.Fatalf("validate(%+v) err=%v, wantErr=%v", c.m, err, c.wantErr)
		}
		if err == nil && got.Transport == "" {
			t.Fatal("valid migration returned an empty transport")
		}
	}
}

// TestApplyTransportMigrationPreservesSession is the continuity contract:
// switching transports must not re-register, re-key, advance the sequence or
// clear the fast path — only the dial target changes.
func TestApplyTransportMigrationPreservesSession(t *testing.T) {
	oldURLs, oldIdx := c2URLsSnapshot(), currentC2Idx.Load()
	oldC2URL := C2URL
	oldP, oldB := Protocol, BeaconTransport
	oldSeq, oldReg, oldFast := beaconSeq, registered, inFastMode.Load()
	t.Cleanup(func() {
		c2URLsStore(oldURLs, oldIdx)
		C2URL = oldC2URL
		Protocol, BeaconTransport = oldP, oldB
		beaconSeq, registered = oldSeq, oldReg
		inFastMode.Store(oldFast)
	})

	C2URL = "https://c2.example/collect,tcp://127.0.0.1:4444"
	c2URLsStore(splitC2URLList(C2URL), 0)
	beaconSeq = 4242
	registered = true
	inFastMode.Store(true)

	if err := applyTransportMigration(&protocol.TransportMigration{
		Transport: "tcp",
		C2URL:     "tcp://127.0.0.1:5555",
	}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if got := getProtocol(); got != "tcp" {
		t.Fatalf("protocol = %q, want tcp", got)
	}
	if got := getBeaconTransport(); got != "tcp" {
		t.Fatalf("transport = %q, want tcp", got)
	}
	if beaconSeq != 4242 || !registered || !inFastMode.Load() {
		t.Fatalf("session state changed: seq=%d registered=%v fast=%v", beaconSeq, registered, inFastMode.Load())
	}
	urls := c2URLsSnapshot()
	if len(urls) == 0 || urls[0] != "tcp://127.0.0.1:5555" || int(currentC2Idx.Load()) != 0 {
		t.Fatalf("migration URL was not made primary: urls=%v idx=%d", urls, currentC2Idx.Load())
	}
	// A migration to a transport with no endpoint fails closed and changes
	// nothing about the current transport.
	if err := applyTransportMigration(&protocol.TransportMigration{Transport: "icmp"}); err == nil {
		t.Fatal("migration to an unconfigured transport was accepted")
	}
	if got := getProtocol(); got != "tcp" {
		t.Fatalf("failed migration changed the transport to %q", got)
	}
}
