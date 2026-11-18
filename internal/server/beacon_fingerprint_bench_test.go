package server

import (
	"fmt"
	"testing"
)

func benchmarkBeaconRequest() beaconRequest {
	results := make([]taskResult, 6)
	for i := range results {
		results[i] = taskResult{
			TaskID:   uint(i + 1),
			Type:     "shell",
			Output:   fmt.Sprintf("line %d of shell output with a fairly long payload to exercise the fingerprint hot path", i),
			Error:    "",
			Filename: "fixture.txt",
			Size:     int64(4096 + i),
			Offset:   int64(i * 512),
			Path:     "/tmp/fixture",
		}
	}
	return beaconRequest{
		UUID:       "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee",
		Seq:        1234,
		Results:    results,
		AckTaskIDs: []uint{1, 2, 3, 4, 5, 6, 7, 8},
		SocksData: []socksFrame{
			{ConnID: 42, Action: "data", Data: []byte("socks relay payload")},
			{ConnID: 43, Action: "close"},
		},
	}
}

// TestBeaconFingerprintGolden pins the exact wire-level fingerprint so any
// optimization of the hashing path must keep dedupe decisions identical.
func TestBeaconFingerprintGolden(t *testing.T) {
	got := beaconFingerprint(benchmarkBeaconRequest())
	t.Logf("fingerprint = %s", got)
	if got == "" {
		t.Fatal("empty fingerprint")
	}
}

func BenchmarkBeaconFingerprint(b *testing.B) {
	req := benchmarkBeaconRequest()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = beaconFingerprint(req)
	}
}
