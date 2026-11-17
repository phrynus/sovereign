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

	"github.com/forgec2/forgec2/pkg/protocol"
)

// TestUDPBeaconFragmentation is a real datagram round trip: a request larger
// than one MTU is split, the peer answers with fragments in reverse order, and
// sendUDPBeaconAt must reassemble the reply before returning it.
func TestUDPBeaconFragmentation(t *testing.T) {
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer pc.Close()

	const requestMarker = "REQUEST-MARKER-"
	const replyMarker = "REPLY-MARKER-"
	request := []byte(strings.Repeat(requestMarker, 60)) // ~900 B -> 2+ fragments
	reply := []byte(strings.Repeat(replyMarker, 80))     // ~1040 B -> 2+ fragments
	if len(protocol.DatagramFragSplit(request)) < 2 || len(protocol.DatagramFragSplit(reply)) < 2 {
		t.Fatal("test fixtures must exceed one fragment")
	}

	gotRequest := make(chan []byte, 1)
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
				gotRequest <- data
				break
			}
			id, total, index, payload, ok := protocol.DatagramFragParse(data)
			if !ok {
				continue
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
}
