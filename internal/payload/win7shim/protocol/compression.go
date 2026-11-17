package protocol

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
)

// Beacon payload compression runs BEFORE AEAD encryption (ciphertext is not
// compressible). Both sides advertise "gzip" in the plaintext JSON envelope;
// the peer that sees the advertisement compresses its next plaintext. The
// compressed plaintext carries a magic prefix INSIDE the ciphertext, so the
// indicator is authenticated by the same AES-GCM AAD (uuid + seq) as the data
// and cannot be stripped or flipped by a MITM.
//
// A peer that does not understand the field never advertises, so it never
// receives compressed data — no protocol version bump is needed.
const (
	compressionMagic           = "FCZ1"
	maxDecompressedBeaconBytes = 16 << 20
	// Small/compressible-poor frames are not worth a gzip header. Sending the
	// original bytes keeps the latency and CPU cost at zero for ordinary beacons.
	minCompressibleBeaconBytes = 512
	minCompressionSavingsPct   = 5
)

// CompressBeacon gzip-compresses a plaintext beacon frame and prefixes it with
// compressionMagic. It returns (original, false) when disabled, too small, or
// when compression would not save at least minCompressionSavingsPct.
func CompressBeacon(plain []byte, enabled bool) ([]byte, bool) {
	if !enabled || len(plain) < minCompressibleBeaconBytes {
		return plain, false
	}
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(plain); err != nil {
		return plain, false
	}
	if err := zw.Close(); err != nil {
		return plain, false
	}
	out := buf.Bytes()
	// Require a real win: gzip of incompressible bytes expands slightly, and
	// paying for a header to send more data is worse than no compression.
	if len(out)*100 >= len(plain)*(100-minCompressionSavingsPct) {
		return plain, false
	}
	result := make([]byte, 0, len(compressionMagic)+len(out))
	result = append(result, compressionMagic...)
	result = append(result, out...)
	return result, true
}

// DecompressBeacon inflates a frame produced by CompressBeacon. Frames without
// the magic prefix are returned untouched (legacy peers / auth responses).
// A hard output cap turns a malicious compressed bomb into a clean failure
// rather than unbounded allocation.
func DecompressBeacon(data []byte) ([]byte, error) {
	if !bytes.HasPrefix(data, []byte(compressionMagic)) {
		return data, nil
	}
	zr, err := gzip.NewReader(bytes.NewReader(data[len(compressionMagic):]))
	if err != nil {
		return nil, fmt.Errorf("beacon gzip header: %w", err)
	}
	defer zr.Close()
	out, err := io.ReadAll(io.LimitReader(zr, maxDecompressedBeaconBytes+1))
	if err != nil {
		return nil, fmt.Errorf("beacon gzip body: %w", err)
	}
	if len(out) > maxDecompressedBeaconBytes {
		return nil, fmt.Errorf("beacon gzip frame expands beyond %d bytes", maxDecompressedBeaconBytes)
	}
	return out, nil
}
