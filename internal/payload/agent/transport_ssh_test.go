//go:build linux || windows || darwin
// +build linux windows darwin

package main

import (
	"bytes"
	"errors"
	"io"
	"testing"
)

// shortWriter returns at most limit bytes per Write with a nil error, which is
// legal for io.Writer and is what exposed the old single-Write SSH delivery.
type shortWriter struct {
	buf   bytes.Buffer
	limit int
}

func (w *shortWriter) Write(p []byte) (int, error) {
	if len(p) > w.limit {
		p = p[:w.limit]
	}
	return w.buf.Write(p)
}

func TestWriteSSHBody(t *testing.T) {
	body := bytes.Repeat([]byte("v2-envelope-payload "), 40)
	for _, limit := range []int{1, 7, 64, 4096} {
		w := &shortWriter{limit: limit}
		if err := writeSSHBody(w, body); err != nil {
			t.Fatalf("limit=%d: %v", limit, err)
		}
		if !bytes.Equal(w.buf.Bytes(), body) {
			t.Fatalf("limit=%d: delivered %d bytes, want %d", limit, w.buf.Len(), len(body))
		}
	}
	if err := writeSSHBody(&shortWriter{limit: 8}, nil); err != nil {
		t.Fatalf("empty body: %v", err)
	}
}

type failingWriter struct{ calls int }

func (w *failingWriter) Write([]byte) (int, error) {
	w.calls++
	return 0, errors.New("channel closed")
}

type zeroWriter struct{}

func (zeroWriter) Write([]byte) (int, error) { return 0, nil }

func TestWriteSSHBodyErrors(t *testing.T) {
	fw := &failingWriter{}
	if err := writeSSHBody(fw, []byte("abc")); err == nil || fw.calls != 1 {
		t.Fatalf("error writer: err=%v calls=%d", err, fw.calls)
	}
	if err := writeSSHBody(zeroWriter{}, []byte("abc")); !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("zero writer error = %v, want ErrShortWrite", err)
	}
}
