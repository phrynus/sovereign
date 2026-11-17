package protocol

import (
	"bytes"
	"testing"
)

// TestDatagramFragSplitSizes proves a congestion-aware caller can change the
// part size and the peer still reassembles the exact body, including a stream
// whose parts are larger than the historical 512-byte default.
func TestDatagramFragSplitSizes(t *testing.T) {
	body := bytes.Repeat([]byte("payload-"), 700) // ~5.6 KiB
	for _, size := range []int{256, 512, 768, 1200, 5000 /* clamped to 1400 */} {
		frags := DatagramFragSplitWithSize(body, size)
		if len(frags) < 2 {
			t.Fatalf("size %d: body was not fragmented", size)
		}
		want := size
		if want < 64 {
			want = 64
		}
		if want > 1400 {
			want = 1400
		}
		if len(frags[0]) != ICMPFragHeaderSize+want {
			t.Fatalf("size %d: first part is %d bytes, want %d", size, len(frags[0]), ICMPFragHeaderSize+want)
		}
		asm := NewDatagramAssembler()
		var out []byte
		for i, frag := range frags {
			id, total, index, payload, ok := DatagramFragParse(frag)
			if !ok || total != len(frags) || index != i {
				t.Fatalf("size %d, part %d: header invalid (total=%d index=%d ok=%v)", size, i, total, index, ok)
			}
			got, err := asm.Add(string(rune(id)), total, index, payload)
			if err != nil {
				t.Fatalf("size %d, part %d: assemble: %v", size, i, err)
			}
			if got != nil {
				out = got
			}
		}
		if !bytes.Equal(out, body) {
			t.Fatalf("size %d: reassembled %d bytes, want %d", size, len(out), len(body))
		}
	}
}

// TestDatagramFragSplitLargeBody guards a real edge: with 512-byte parts a
// body above 128 KiB needed >256 parts, which the parser rejects. The splitter
// must grow parts automatically instead of emitting an unassemblable stream.
func TestDatagramFragSplitLargeBody(t *testing.T) {
	body := bytes.Repeat([]byte("x"), 512*260) // 260 parts at the default size
	frags := ICMPFragSplit(body)
	if len(frags) == 0 {
		t.Fatal("large body produced no fragments")
	}
	if len(frags) > 256 {
		t.Fatalf("splitter emitted %d parts, above the 256-part cap", len(frags))
	}
	for _, frag := range frags {
		if _, _, _, _, ok := DatagramFragParse(frag); !ok {
			t.Fatal("a part of a large body failed the parser")
		}
	}
	// A body beyond the framing's absolute capacity is refused, not sent.
	if got := DatagramFragSplitWithSize(bytes.Repeat([]byte("y"), 1400*256+1), 1400); got != nil {
		t.Fatalf("oversized body produced %d fragments", len(got))
	}
}
