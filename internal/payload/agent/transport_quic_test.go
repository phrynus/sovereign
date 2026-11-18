//go:build linux || windows || darwin
// +build linux windows darwin

package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/quic-go/quic-go"
)

func quicTestTLS(t *testing.T) *tls.Config {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := tls.X509KeyPair(
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}),
	)
	if err != nil {
		t.Fatal(err)
	}
	return &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS13, NextProtos: []string{"h3", "fc2"}}
}

func TestQUICConnectionReuseAndRecovery(t *testing.T) {
	oldSkip := SkipTLSVerify
	SkipTLSVerify = true
	t.Cleanup(func() {
		SkipTLSVerify = oldSkip
		quicInvalidateConn()
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ln, err := quic.ListenAddr("127.0.0.1:0", quicTestTLS(t), &quic.Config{MaxIdleTimeout: 10 * time.Second})
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	var connections atomic.Int32
	go func() {
		for {
			conn, err := ln.Accept(ctx)
			if err != nil {
				return
			}
			connections.Add(1)
			go func(c *quic.Conn) {
				for {
					stream, err := c.AcceptStream(ctx)
					if err != nil {
						return
					}
					body, err := io.ReadAll(stream)
					if err == nil {
						_, _ = stream.Write(append([]byte("echo:"), body...))
					}
					_ = stream.Close()
				}
			}(conn)
		}
	}()

	addr := ln.Addr().String()
	for _, payload := range []string{"first", "second"} {
		got := sendQUICBeaconAt(addr, []byte(payload))
		if !bytes.Equal(got, []byte("echo:"+payload)) {
			t.Fatalf("%s reply = %q", payload, got)
		}
	}
	if n := connections.Load(); n != 1 {
		t.Fatalf("two beacons opened %d connections, want 1", n)
	}

	// A dead cached connection is invalidated and redialed once.
	cached := quicCachedConn(addr)
	if cached == nil {
		t.Fatal("no cached QUIC connection after successful beacons")
	}
	_ = cached.CloseWithError(0, "simulated stale connection")
	got := sendQUICBeaconAt(addr, []byte("third"))
	if !bytes.Equal(got, []byte("echo:third")) {
		t.Fatalf("reply after stale connection = %q", got)
	}
	if n := connections.Load(); n != 2 {
		t.Fatalf("stale recovery opened %d total connections, want 2", n)
	}
}
