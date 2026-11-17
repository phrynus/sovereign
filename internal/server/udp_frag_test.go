package server

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/forgec2/forgec2/internal/crypto"
	"github.com/forgec2/forgec2/internal/db"
	"github.com/forgec2/forgec2/internal/testutil"
	"github.com/forgec2/forgec2/pkg/encoding"
	"github.com/forgec2/forgec2/pkg/protocol"
)

// TestUDPFragmentedBeaconAssembles proves the server does not parse a partial
// FC2I stream: an encrypted beacon spread over several datagrams yields a
// response only when the final part arrives, and that response parses normally.
func TestUDPFragmentedBeaconAssembles(t *testing.T) {
	ginSetTestMode(t)
	database := testutil.SetupTestDB(t)
	crypto.InitLootEncryption(strings.Repeat("22", 32))
	s := initDNSBeaconServer(t, database)
	s.configMu.Lock()
	s.cfg.Server.BeaconKey = "aabbccddeeff00112233445566778899aabbccddeeff00112233445566778899"
	s.configMu.Unlock()

	agent := v3TestAgent(t, s, "dddddddd-eeee-4333-8444-ffffffffffff")
	addr := udpTestAddr()

	// Register (small, unfragmented) and establish the session the encrypted
	// frame needs.
	regResp := s.handleUDPBeacon([]byte(agent.registerFrame()), addr)
	if len(regResp) == 0 {
		t.Fatal("registration produced no response")
	}
	var reg struct {
		Seq     uint64 `json:"seq"`
		RegOK   bool   `json:"reg_ok"`
		ECDHPub string `json:"ecdh_pub"`
		Mac     string `json:"mac"`
	}
	if err := encoding.Unmarshal(regResp, &reg); err != nil || !reg.RegOK {
		t.Fatalf("register response = %q err=%v", regResp, err)
	}
	if err := agent.establishFromServerKey(reg.ECDHPub); err != nil {
		t.Fatalf("establish session: %v", err)
	}

	// A large encrypted beacon exceeds one datagram.
	inner, _ := json.Marshal(map[string]interface{}{
		"uuid": agent.uuid,
		"pv":   2,
		"info": map[string]string{"hostname": "UDP-FRAG", "username": "u", "ip": "10.0.0.8"},
		"results": []map[string]interface{}{
			{"task_id": 1, "type": "shell", "output": strings.Repeat("fragmented-result ", 200)},
		},
	})
	frame := []byte(agent.encryptedFrame(inner))
	if len(frame) == 0 {
		t.Fatal("encrypted frame missing")
	}
	frags := protocol.DatagramFragSplit(frame)
	if len(frags) < 2 {
		t.Fatalf("encrypted frame of %d bytes must exceed one datagram", len(frame))
	}

	// Reverse arrival order; a partial set must never reach the beacon parser.
	for i := len(frags) - 1; i > 0; i-- {
		if resp := s.handleUDPBeacon(frags[i], addr); len(resp) != 0 {
			t.Fatalf("partial fragment %d produced a response", i)
		}
	}
	resp := s.handleUDPBeacon(frags[0], addr)
	if len(resp) == 0 {
		t.Fatal("complete fragmented beacon produced no response")
	}
	var encResp struct {
		CipherB64 string `json:"c"`
	}
	if err := encoding.Unmarshal(resp, &encResp); err != nil || encResp.CipherB64 == "" {
		t.Fatalf("fragmented beacon response = %q err=%v", resp, err)
	}

	var count int64
	database.Model(&db.Implant{}).Where("id = ?", agent.uuid).Count(&count)
	if count != 1 {
		t.Fatalf("fragmented beacon implanted %d rows, want 1", count)
	}

	// The return path splits a large response into datagram-sized parts.
	parts := protocol.DatagramFragSplit([]byte(strings.Repeat("R", protocol.DatagramFragMaxPayload*3+17)))
	if len(parts) != 4 {
		t.Fatalf("return path split into %d parts, want 4", len(parts))
	}
}
