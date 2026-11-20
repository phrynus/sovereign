//go:build linux || windows || darwin
// +build linux windows darwin

package main

import (
	"bytes"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/r0si/sovereign/pkg/protocol"
)

// TestUDPBeaconFragmentation is a real datagram round trip: a request larger
// than one MTU is split, the peer answers with fragments in reverse order, and
// sendUDPBeaconAt must reassemble the reply before returning it.
func TestUDPBeaconRetriesLostFragment(t *testing.T) {
	oldTimeout := udpAttemptTimeout
	udpAttemptTimeout = 100 * time.Millisecond
	t.Cleanup(func() { udpAttemptTimeout = oldTimeout })

	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer pc.Close()

	const replyMarker = "RETRY-REPLY-"
	wantReply := []byte(strings.Repeat(replyMarker, 100))
	completed := make(chan []byte, 1)
	go func() {
		asm := protocol.NewDatagramAssembler()
		buf := make([]byte, 8192)
		dropped := false
		for {
			n, addr, readErr := pc.ReadFrom(buf)
			if readErr != nil {
				return
			}
			msgID, total, index, payload, ok := protocol.DatagramFragParse(buf[:n])
			if !ok {
				continue
			}
			// Drop one part of the first attempt: the sender must resend
			// the whole set with the same msg id.
			if !dropped && index == 0 {
				dropped = true
				continue
			}
			full, asmErr := asm.Add(strconv.FormatUint(uint64(msgID), 10), total, index, payload)
			if asmErr != nil || full == nil {
				continue
			}
			for _, frag := range protocol.DatagramFragSplitWithSize(wantReply, 512) {
				_, _ = pc.WriteTo(frag, addr)
			}
			completed <- full
			return
		}
	}()

	request := []byte(strings.Repeat("Q", 3000)) // several 512-byte parts
	got := sendUDPBeaconAt(pc.LocalAddr().String(), request)
	if !bytes.Equal(got, wantReply) {
		t.Fatalf("retry returned %d bytes, want the %d-byte reassembled reply", len(got), len(wantReply))
	}
	select {
	case assembled := <-completed:
		if !bytes.Equal(assembled, request) {
			t.Fatal("server assembled a different request after retransmission")
		}
	case <-time.After(time.Second):
		t.Fatal("retry never completed the request")
	}
}

func TestUDPBeaconFragmentation(t *testing.T) {
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer pc.Close()

	const requestMarker = "REQUEST-MARKER-"
	const replyMarker = "REPLY-MARKER-"
	request := []byte(strings.Repeat(requestMarker, 200)) // ~3 KiB -> 3 parts at 1200 B
	reply := []byte(strings.Repeat(replyMarker, 80))      // ~1040 B -> 2+ fragments
	if len(protocol.DatagramFragSplit(request)) < 2 || len(protocol.DatagramFragSplit(reply)) < 2 {
		t.Fatal("test fixtures must exceed one fragment")
	}

	resetLinkState()
	t.Cleanup(resetLinkState)
	// A healthy link makes the agent choose larger datagram parts.
	for i := 0; i < linkQualitySamples; i++ {
		observeBeaconResult(50*time.Millisecond, true)
	}

	gotRequest := make(chan []byte, 1)
	partSizes := make(chan int, 1)
	go func() {
		asm := protocol.NewDatagramAssembler()
		buf := make([]byte, 8192)
		var peer net.Addr
		for {
			n, addr, readErr := pc.ReadFrom(buf)
			if readErr != nil {
				return
			}
			peer = addr
			data := append([]byte(nil), buf[:n]...)
			if protocol.DatagramMaybePlain(data) {
				partSizes <- len(data)
				gotRequest <- data
				break
			}
			id, total, index, payload, ok := protocol.DatagramFragParse(data)
			if !ok {
				continue
			}
			if index == 0 {
				partSizes <- len(data)
			}
			full, asmErr := asm.Add(strconv.FormatUint(uint64(id), 10), total, index, payload)
			if asmErr != nil || full == nil {
				continue
			}
			gotRequest <- full
			break
		}
		// Reply out of order to prove reassembly does not assume arrival order.
		frags := protocol.DatagramFragSplit(reply)
		for i := len(frags) - 1; i >= 0; i-- {
			_, _ = pc.WriteTo(frags[i], peer)
		}
	}()

	got := sendUDPBeaconAt(pc.LocalAddr().String(), request)
	select {
	case sent := <-gotRequest:
		if !bytes.Equal(sent, request) {
			t.Fatalf("server saw %d bytes, want %d", len(sent), len(request))
		}
	case <-time.After(3 * time.Second):
		t.Fatal("server never received a complete request")
	}
	if !bytes.Equal(got, reply) {
		t.Fatalf("agent returned %d bytes, want reassembled %d-byte reply", len(got), len(reply))
	}
	select {
	case size := <-partSizes:
		if want := protocol.ICMPFragHeaderSize + udpFragLarge; size != want {
			t.Fatalf("healthy-link part = %d bytes, want %d", size, want)
		}
	case <-time.After(time.Second):
		t.Fatal("no part size was captured")
	}
}
