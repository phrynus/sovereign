package server

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/r0si/sovereign/pkg/protocol"
)

// TestBeaconCompressionNegotiation pins both halves of the transport change:
// the server echoes only the capability the agent advertised, and the response
// plaintext is actually compressed INSIDE the AEAD envelope (so a MITM cannot
// add/remove the gzip prefix and the agent sees it only after decrypting).
func TestBeaconCompressionNegotiation(t *testing.T) {
	s, _ := v2TestServer(t)
	agentID := "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"

	withCapability := s.processBeaconWithBudget(beaconRequest{UUID: agentID, Compression: "gzip"}, "", 0, 0)
	if withCapability.Compression != "gzip" {
		t.Fatalf("capability not echoed: %+v", withCapability)
	}
	withoutCapability := s.processBeaconWithBudget(beaconRequest{UUID: agentID}, "", 0, 0)
	if withoutCapability.Compression != "" {
		t.Fatalf("compression enabled for a legacy peer: %+v", withoutCapability)
	}

	// Establish a session so buildBeaconResponse can encrypt the reply.
	priv, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("agent key: %v", err)
	}
	if err := s.sessionManager.EstablishSession(agentID, priv.PublicKey().Bytes()); err != nil {
		t.Fatalf("establish: %v", err)
	}

	longCommand := strings.Repeat("echo hello; ", 900)
	resp := beaconResponse{
		Compression: "gzip",
		Tasks:       []task{{ID: 5, Type: "shell", Command: longCommand}},
	}
	raw, ok := s.buildBeaconResponse(agentID, 9, resp)
	if !ok {
		t.Fatal("build compressed response failed")
	}
	var env beaconEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("envelope: %v", err)
	}
	if env.CipherB64 == "" {
		t.Fatal("encrypted response missing ciphertext")
	}
	plain, err := s.sessionManager.DecryptWithAADB64(agentID, env.CipherB64,
		[]byte(agentID+"\x00"+strconv.FormatUint(9, 10)))
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if !strings.HasPrefix(string(plain[:4]), protocolCompressionMagicForTest) {
		t.Fatalf("response plaintext is not gzip-framed: %q", plain[:min(16, len(plain))])
	}
	decoded, err := protocol.DecompressBeacon(plain)
	if err != nil {
		t.Fatalf("decompress: %v", err)
	}
	compressedWireBytes := len(plain)
	var got beaconResponse
	if err := json.Unmarshal(decoded, &got); err != nil {
		t.Fatalf("decoded response: %v", err)
	}
	if got.Compression != "gzip" || len(got.Tasks) != 1 || got.Tasks[0].Command != longCommand {
		t.Fatalf("compressed response changed: comp=%q tasks=%d", got.Compression, len(got.Tasks))
	}

	// A legacy peer gets plain JSON (no FCZ1 prefix) even for a large payload.
	legacyRaw, ok := s.buildBeaconResponse(agentID, 10, beaconResponse{Tasks: resp.Tasks})
	if !ok {
		t.Fatal("build legacy response failed")
	}
	if err := json.Unmarshal(legacyRaw, &env); err != nil {
		t.Fatalf("legacy envelope: %v", err)
	}
	legacyPlain, err := s.sessionManager.DecryptWithAADB64(agentID, env.CipherB64,
		[]byte(agentID+"\x00"+strconv.FormatUint(10, 10)))
	if err != nil {
		t.Fatalf("legacy decrypt: %v", err)
	}
	if strings.HasPrefix(string(legacyPlain[:4]), protocolCompressionMagicForTest) || legacyPlain[0] != '{' {
		t.Fatalf("legacy peer received compressed bytes: %q", legacyPlain[:min(16, len(legacyPlain))])
	}
	if len(legacyPlain) <= compressedWireBytes {
		t.Fatalf("compression did not save bytes: %d -> %d", len(legacyPlain), compressedWireBytes)
	}
	t.Logf("beacon response plaintext: %d B legacy -> %d B compressed", len(legacyPlain), compressedWireBytes)
}

// The prefix is unexported in pkg/protocol; this test only needs the constant
// value, pinned by its own round-trip tests.
const protocolCompressionMagicForTest = "FCZ1"
