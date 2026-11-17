package protocol

import (
	"bytes"
	"encoding/json"
	"math/rand"
	"strings"
	"testing"
)

// TestBeaconCompressionRoundTrip pins the FCZ1 framing and proves a realistic
// beacon frame actually shrinks (the whole point of the transport change).
func TestBeaconCompressionRoundTrip(t *testing.T) {
	req := BeaconRequest{
		UUID:        "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee",
		Seq:         42,
		Compression: "gzip",
		Results: []TaskResult{{
			TaskID: 7,
			Type:   "shell",
			Output: strings.Repeat("line of beacon output\n", 400),
		}},
	}
	plain, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	compressed, used := CompressBeacon(plain, true)
	if !used {
		t.Fatalf("a %d-byte beacon frame was not compressed", len(plain))
	}
	if !bytes.HasPrefix(compressed, []byte(compressionMagic)) {
		t.Fatalf("compressed frame is missing the FCZ1 prefix")
	}
	decoded, err := DecompressBeacon(compressed)
	if err != nil {
		t.Fatalf("decompress: %v", err)
	}
	var got BeaconRequest
	if err := json.Unmarshal(decoded, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Seq != req.Seq || got.Compression != "gzip" || got.Results[0].Output != req.Results[0].Output {
		t.Fatalf("round-trip changed the frame: seq=%d comp=%q output bytes=%d",
			got.Seq, got.Compression, len(got.Results[0].Output))
	}
	t.Logf("beacon frame: %d B plain -> %d B compressed (%.1f%%)", len(plain), len(compressed),
		100*float64(len(compressed))/float64(len(plain)))
	if len(compressed) >= len(plain)/2 {
		t.Fatalf("compression saved too little: %d -> %d", len(plain), len(compressed))
	}
}

// TestBeaconCompressionNoOp proves old peers and small/beacon-poor frames keep
// their exact bytes, so the change is wire-compatible and not a CPU tax.
func TestBeaconCompressionNoOp(t *testing.T) {
	legacy := []byte(`{"uuid":"x","seq":1}`)
	if got, used := CompressBeacon(legacy, true); used || !bytes.Equal(got, legacy) {
		t.Fatalf("small frame changed: used=%v got=%q", used, got)
	}
	if got, used := CompressBeacon(bytes.Repeat([]byte("a"), 2048), false); used || !bytes.Equal(got, bytes.Repeat([]byte("a"), 2048)) {
		t.Fatal("disabled compression changed the bytes")
	}
	// Incompressible (random-ish) data must not be wrapped in a gzip header.
	incompressible := make([]byte, 4096)
	rand.New(rand.NewSource(1)).Read(incompressible)
	if got, used := CompressBeacon(incompressible, true); used || !bytes.Equal(got, incompressible) {
		t.Fatalf("incompressible frame was wrapped (grew to %d bytes)", len(got))
	}
	if got, err := DecompressBeacon(legacy); err != nil || !bytes.Equal(got, legacy) {
		t.Fatalf("legacy frame failed passthrough: %q err=%v", got, err)
	}
}

// TestBeaconDecompressionBounds fails closed on a corrupt or oversized gzip
// stream instead of allocating whatever a frame claims.
func TestBeaconDecompressionBounds(t *testing.T) {
	if _, err := DecompressBeacon([]byte(compressionMagic + "not-gzip")); err == nil {
		t.Fatal("a corrupt gzip frame was accepted")
	}
	// A tiny compressed bomb expands far past the cap; the cap must fire.
	plain := bytes.Repeat([]byte{0}, maxDecompressedBeaconBytes+1024)
	bomb, used := CompressBeacon(plain, true)
	if !used {
		t.Fatal("test bomb did not compress")
	}
	if _, err := DecompressBeacon(bomb); err == nil || !strings.Contains(err.Error(), "expands beyond") {
		t.Fatalf("decompression bomb error = %v", err)
	}
}
