//go:build linux || windows || darwin
// +build linux windows darwin

package main

import "testing"

// TestDNSDialAddress pins the DNS endpoint normalizer used by both the UDP
// and TCP beacon paths. A configured non-default port must survive unchanged;
// a bare host or IPv6 literal gets :53 appended/bracketed.
func TestDNSDialAddress(t *testing.T) {
	cases := map[string]string{
		"8.8.8.8":          "8.8.8.8:53",
		"  8.8.8.8  ":      "8.8.8.8:53",
		"8.8.8.8:5353":     "8.8.8.8:5353",
		"dns.example":      "dns.example:53",
		"dns.example:5353": "dns.example:5353",
		"::1":              "[::1]:53",
		"2001:db8::1":      "[2001:db8::1]:53",
		"[::1]:5353":       "[::1]:5353",
		"[2001:db8::1]:53": "[2001:db8::1]:53",
	}
	for input, want := range cases {
		if got := dnsDialAddress(input); got != want {
			t.Errorf("dnsDialAddress(%q) = %q, want %q", input, got, want)
		}
	}
	if got := dnsDialAddress("   "); got != "" {
		t.Fatalf("blank server = %q, want empty", got)
	}
}
