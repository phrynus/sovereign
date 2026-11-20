package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/r0si/sovereign/pkg/protocol"
)

// TestAgentBeaconCompression covers the implant's half of the negotiation:
// it always advertises gzip, only compresses once the server advertised it,
// and can read the capability out of an authenticated compressed reply.
func TestAgentBeaconCompression(t *testing.T) {
	old := peerCanInflate.Load()
	t.Cleanup(func() { peerCanInflate.Store(old) })

	req := BeaconRequest{
		UUID:        "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee",
		Seq:         11,
		Compression: "gzip",
		Results: []TaskResult{{
			TaskID: 3,
			Type:   "shell",
			Output: strings.Repeat("result bytes ", 700),
		}},
	}
	body, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(body), `"comp":"gzip"`) {
		t.Fatalf("request does not advertise gzip: %s", body[:min(160, len(body))])
	}

	// An old server never advertises: the implant sends the exact same bytes.
	peerCanInflate.Store(false)
	if wire, used := protocol.CompressBeacon(body, peerCanInflate.Load()); used || !bytes.Equal(wire, body) {
		t.Fatal("legacy request was changed by compression")
	}

	// A server that advertised gzip gets compressed plaintext.
	peerCanInflate.Store(true)
	wire, used := protocol.CompressBeacon(body, peerCanInflate.Load())
	if !used {
		t.Fatal("capable server did not get a compressed request")
	}
	plain, err := protocol.DecompressBeacon(wire)
	if err != nil {
		t.Fatalf("decompress: %v", err)
	}
	var got BeaconRequest
	if err := json.Unmarshal(plain, &got); err != nil {
		t.Fatalf("decoded request: %v", err)
	}
	if got.Compression != "gzip" || len(got.Results) != 1 || got.Results[0].Output != req.Results[0].Output {
		t.Fatalf("compressed request changed: %+v", got)
	}
	t.Logf("request: %d B plain -> %d B on the wire", len(body), len(wire))

	// The agent learns the capability from the decrypted reply, not from any
	// cleartext field; a legacy reply carries no comp and keeps the old value.
	reply := []byte(`{"comp":"gzip","tasks":[{"id":1,"type":"shell","command":"` +
		strings.Repeat("echo x; ", 100) + `"}]}`)
	compressedReply, compressed := protocol.CompressBeacon(reply, true)
	if !compressed {
		t.Fatal("test reply did not compress")
	}
	decoded, err := protocol.DecompressBeacon(compressedReply)
	if err != nil {
		t.Fatalf("reply decompress: %v", err)
	}
	var resp BeaconResponse
	if err := json.Unmarshal(decoded, &resp); err != nil {
		t.Fatalf("reply decode: %v", err)
	}
	if resp.Compression != "gzip" {
		t.Fatalf("reply capability = %q", resp.Compression)
	}
	legacyReply := []byte(`{"tasks":[]}`)
	decoded, err = protocol.DecompressBeacon(legacyReply)
	if err != nil || !bytes.Equal(decoded, legacyReply) {
		t.Fatalf("legacy reply failed: %q err=%v", decoded, err)
	}
	var legacy BeaconResponse
	if err := json.Unmarshal(decoded, &legacy); err != nil || legacy.Compression != "" {
		t.Fatalf("legacy capability wrongly enabled: %v %q", err, legacy.Compression)
	}
}
