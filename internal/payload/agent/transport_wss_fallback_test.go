//go:build linux || windows || darwin
// +build linux windows darwin

package main

import (
	"encoding/binary"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestWSSFallbackPadsBodyOnce pins a cross-transport regression: sendWSSBeacon
// applies the body-length jitter to websocket frames, but the HTTP fallback
// (sendToC2) applies it again. The server strips only one prefix, so the
// envelope started with 8 random bytes and every fallback beacon was rejected
// as malformed JSON whenever ContentLengthJitter was enabled.
func TestWSSFallbackPadsBodyOnce(t *testing.T) {
	oldJitter := ContentLengthJitter
	oldURLs, oldIdx := c2URLsSnapshot(), currentC2Idx.Load()
	t.Cleanup(func() {
		ContentLengthJitter = oldJitter
		c2URLsStore(oldURLs, oldIdx)
		wssInvalidate()
	})
	wssInvalidate()
	ContentLengthJitter = 64

	validBody := make(chan bool, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		ok := err == nil && len(raw) >= 8
		if ok {
			n := binary.BigEndian.Uint64(raw[:8])
			ok = n >= 16 && n <= uint64(len(raw)-8) && json.Valid(raw[8:8+n])
		}
		validBody <- ok
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"data":{}}`))
	}))
	defer srv.Close()

	// The same endpoint is tried as a websocket first (handshake fails against
	// the plain HTTP handler), then as an HTTP beacon by the fallback path.
	c2URLsStore([]string{srv.URL}, 0)
	resp := sendWSSBeacon([]byte(`{"uuid":"aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee","seq":1}`))
	if resp == nil {
		t.Fatal("HTTP fallback returned no response")
	}
	select {
	case ok := <-validBody:
		if !ok {
			t.Fatal("HTTP fallback delivered a doubly-padded envelope (server would reject it)")
		}
	default:
		t.Fatal("HTTP fallback never reached the probe server")
	}
}
