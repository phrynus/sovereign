package server

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/r0si/sovereign/internal/crypto"
	"github.com/r0si/sovereign/internal/db"
)

func signedMetadataJWT(t *testing.T, claims jwt.MapClaims) string {
	t.Helper()
	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte("any-key"))
	if err != nil {
		t.Fatalf("sign JWT: %v", err)
	}
	return token
}

func TestParseCredentialMetadataJWT(t *testing.T) {
	now := time.Unix(2_000_000_000, 0).UTC()
	expiresAt := now.Add(2 * time.Hour)
	token := signedMetadataJWT(t, jwt.MapClaims{"exp": expiresAt.Unix(), "iat": now.Unix()})

	metadata, ok := parseCredentialMetadata(" \n" + token + "\t")
	if !ok {
		t.Fatal("expected JWT metadata")
	}
	if metadata.Subtype != "jwt" {
		t.Fatalf("subtype = %q, want jwt", metadata.Subtype)
	}
	if !metadata.ExpiresAt.Equal(expiresAt) {
		t.Fatalf("expires_at = %v, want %v", metadata.ExpiresAt, expiresAt)
	}
	if metadata.RenewUntil != nil {
		t.Fatalf("renew_until = %v, want nil", metadata.RenewUntil)
	}

	if _, err := jwt.NewParser().ParseWithClaims(token, jwt.MapClaims{}, func(*jwt.Token) (any, error) {
		return []byte("wrong-key"), nil
	}); err == nil {
		t.Fatal("wrong signing key unexpectedly verified the JWT")
	}
	if _, ok := parseCredentialMetadata(token); !ok {
		t.Fatal("display metadata must not depend on signature verification")
	}

	expired := signedMetadataJWT(t, jwt.MapClaims{"exp": now.Add(-time.Hour).Unix()})
	if metadata, ok := parseCredentialMetadata(expired); !ok || !metadata.ExpiresAt.Equal(now.Add(-time.Hour)) {
		t.Fatalf("expired JWT should still expose its expiry: metadata=%+v ok=%v", metadata, ok)
	}

	invalid := map[string]string{
		"two segments":  "one.two",
		"four segments": "one.two.three.four",
		"malformed JWT": "one.two.three",
		"missing exp":   signedMetadataJWT(t, jwt.MapClaims{"iat": now.Unix()}),
		"string exp":    signedMetadataJWT(t, jwt.MapClaims{"exp": now.Format(time.RFC3339)}),
		"zero exp":      signedMetadataJWT(t, jwt.MapClaims{"exp": 0}),
		"negative exp":  signedMetadataJWT(t, jwt.MapClaims{"exp": -1}),
		"empty":         "",
		"whitespace":    " \n\t ",
		"over one MiB":  strings.Repeat("A", credentialMetadataMaxBytes+1),
	}
	for name, value := range invalid {
		t.Run(name, func(t *testing.T) {
			if metadata, ok := parseCredentialMetadata(value); ok {
				t.Fatalf("expected parse failure, got %+v", metadata)
			}
		})
	}
}

func writeMetadataUint16(t *testing.T, buffer *bytes.Buffer, value uint16) {
	t.Helper()
	if err := binary.Write(buffer, binary.BigEndian, value); err != nil {
		t.Fatalf("write uint16: %v", err)
	}
}

func writeMetadataUint32(t *testing.T, buffer *bytes.Buffer, value uint32) {
	t.Helper()
	if err := binary.Write(buffer, binary.BigEndian, value); err != nil {
		t.Fatalf("write uint32: %v", err)
	}
}

func writeMetadataBytes(t *testing.T, buffer *bytes.Buffer, value []byte) {
	t.Helper()
	if _, err := buffer.Write(value); err != nil {
		t.Fatalf("write bytes: %v", err)
	}
}

func writeMetadataCountedBytes(t *testing.T, buffer *bytes.Buffer, value []byte) {
	t.Helper()
	writeMetadataUint32(t, buffer, uint32(len(value)))
	writeMetadataBytes(t, buffer, value)
}

func writeMetadataPrincipal(t *testing.T, buffer *bytes.Buffer, realm string, components ...string) {
	t.Helper()
	writeMetadataUint32(t, buffer, 1)
	writeMetadataUint32(t, buffer, uint32(len(components)))
	writeMetadataCountedBytes(t, buffer, []byte(realm))
	for _, component := range components {
		writeMetadataCountedBytes(t, buffer, []byte(component))
	}
}

func metadataCCacheFixture(t *testing.T, endTime, renewTill uint32) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writeMetadataUint16(t, &buffer, 0x0504)
	writeMetadataUint16(t, &buffer, 12)
	writeMetadataUint16(t, &buffer, 1)
	writeMetadataUint16(t, &buffer, 8)
	writeMetadataBytes(t, &buffer, []byte{0xff, 0xff, 0xff, 0xff, 0, 0, 0, 0})
	writeMetadataPrincipal(t, &buffer, "EXAMPLE.TEST", "alice")
	writeMetadataPrincipal(t, &buffer, "EXAMPLE.TEST", "alice")
	writeMetadataPrincipal(t, &buffer, "EXAMPLE.TEST", "krbtgt", "EXAMPLE.TEST")
	writeMetadataUint16(t, &buffer, 18)
	writeMetadataUint16(t, &buffer, 23)
	writeMetadataUint16(t, &buffer, 32)
	writeMetadataBytes(t, &buffer, bytes.Repeat([]byte{0x42}, 32))
	authTime, startTime := endTime, endTime
	if endTime >= 7200 {
		authTime = endTime - 7200
	}
	if endTime >= 3600 {
		startTime = endTime - 3600
	}
	writeMetadataUint32(t, &buffer, authTime)
	writeMetadataUint32(t, &buffer, startTime)
	writeMetadataUint32(t, &buffer, endTime)
	writeMetadataUint32(t, &buffer, renewTill)
	writeMetadataBytes(t, &buffer, []byte{0})
	writeMetadataUint32(t, &buffer, 0x40000000)
	writeMetadataUint32(t, &buffer, 0)
	writeMetadataUint32(t, &buffer, 0)
	writeMetadataCountedBytes(t, &buffer, []byte{1, 2, 3, 4})
	writeMetadataCountedBytes(t, &buffer, nil)
	return buffer.Bytes()
}

func TestParseCredentialMetadataCCache(t *testing.T) {
	endTime := uint32(2_000_007_200)
	renewTill := uint32(2_000_086_400)
	data := metadataCCacheFixture(t, endTime, renewTill)

	for name, encoded := range map[string]string{
		"padded":   base64.StdEncoding.EncodeToString(data),
		"unpadded": base64.RawStdEncoding.EncodeToString(data),
	} {
		t.Run(name, func(t *testing.T) {
			metadata, ok := parseCredentialMetadata(encoded)
			if !ok {
				t.Fatal("expected ccache metadata")
			}
			if metadata.Subtype != "ccache" {
				t.Fatalf("subtype = %q, want ccache", metadata.Subtype)
			}
			wantExpiry := time.Unix(int64(endTime), 0).UTC()
			if !metadata.ExpiresAt.Equal(wantExpiry) {
				t.Fatalf("expires_at = %v, want %v", metadata.ExpiresAt, wantExpiry)
			}
			if metadata.RenewUntil == nil || !metadata.RenewUntil.Equal(time.Unix(int64(renewTill), 0).UTC()) {
				t.Fatalf("renew_until = %v, want %v", metadata.RenewUntil, time.Unix(int64(renewTill), 0).UTC())
			}
		})
	}

	withoutRenewal, ok := parseCredentialMetadata(base64.StdEncoding.EncodeToString(metadataCCacheFixture(t, endTime, endTime)))
	if !ok || withoutRenewal.RenewUntil != nil {
		t.Fatalf("renew_till <= endtime must omit renewal: metadata=%+v ok=%v", withoutRenewal, ok)
	}

	epochExpiry, ok := parseCredentialMetadata(base64.StdEncoding.EncodeToString(metadataCCacheFixture(t, 0, 0)))
	if !ok || !epochExpiry.ExpiresAt.Equal(time.Unix(0, 0).UTC()) || epochExpiry.RenewUntil != nil {
		t.Fatalf("zero endtime should expose epoch expiry without renewal: metadata=%+v ok=%v", epochExpiry, ok)
	}

	metadataEnd := len(data) - 25 // is_skey, flags, counts, and two counted tickets.
	truncated := data[:metadataEnd-1]
	v3 := append([]byte(nil), data...)
	v3[1] = 0x03
	for name, value := range map[string]string{
		"truncated":  base64.StdEncoding.EncodeToString(truncated),
		"version 3":  base64.StdEncoding.EncodeToString(v3),
		"not base64": "not!base64",
	} {
		t.Run(name, func(t *testing.T) {
			if metadata, ok := parseCredentialMetadata(value); ok {
				t.Fatalf("expected parse failure, got %+v", metadata)
			}
		})
	}

	for length := range metadataEnd {
		encoded := base64.StdEncoding.EncodeToString(data[:length])
		if _, ok := parseCredentialMetadata(encoded); ok {
			t.Fatalf("prefix of %d bytes unexpectedly parsed", length)
		}
	}
}

func TestHandleAddCredentialParsesMetadata(t *testing.T) {
	s := mustTenantServer(t)
	crypto.InitLootEncryption(testStorageKeyHex)
	now := time.Now().Truncate(time.Second)
	expiresAt := now.Add(2 * time.Hour)
	token := signedMetadataJWT(t, jwt.MapClaims{"exp": expiresAt.Unix()})

	postCredential := func(hash, credentialType string) uint {
		t.Helper()
		form := url.Values{}
		form.Set("username", "alice")
		form.Set("hash", hash)
		form.Set("type", credentialType)
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Set("user_role", db.RoleAdmin)
		c.Request = httptest.NewRequest(http.MethodPost, "/credentials/add", strings.NewReader(form.Encode()))
		c.Request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		s.handleAddCredential(c)
		if w.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
		}
		var response struct {
			ID uint `json:"id"`
		}
		unmarshalData(t, w.Body.Bytes(), &response)
		return response.ID
	}

	id := postCredential(token, "kerberos")
	var saved db.CredentialEntry
	if err := s.db.First(&saved, id).Error; err != nil {
		t.Fatalf("reload credential: %v", err)
	}
	if saved.Subtype != "jwt" {
		t.Fatalf("subtype = %q, want jwt", saved.Subtype)
	}
	if !saved.ExpiresAt.Equal(expiresAt) {
		t.Fatalf("expires_at = %v, want %v", saved.ExpiresAt, expiresAt)
	}
	if saved.RenewUntil != nil {
		t.Fatalf("renew_until = %v, want nil", saved.RenewUntil)
	}
	if saved.Type != "kerberos" {
		t.Fatalf("operator-selected type changed to %q", saved.Type)
	}

	id = postCredential("not-a-token", "cleartext")
	var fallback db.CredentialEntry
	if err := s.db.First(&fallback, id).Error; err != nil {
		t.Fatalf("reload fallback credential: %v", err)
	}
	if fallback.Subtype != "" {
		t.Fatalf("subtype = %q, want empty", fallback.Subtype)
	}
	wantReview := time.Now().AddDate(0, 0, 30)
	if delta := fallback.ExpiresAt.Sub(wantReview); delta < -time.Minute || delta > time.Minute {
		t.Fatalf("default review expiry = %v, want approximately %v", fallback.ExpiresAt, wantReview)
	}
}

func TestParseCredentialsFromTextJWT(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	expiresAt := now.Add(time.Hour)
	token := signedMetadataJWT(t, jwt.MapClaims{"exp": expiresAt.Unix()})

	entries := parseCredentialsFromText(token, "agent-jwt", 42)
	if len(entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(entries))
	}
	entry := entries[0]
	if entry.Type != "jwt" || entry.Source != "task_parse" || entry.Subtype != "jwt" {
		t.Fatalf("unexpected JWT entry: %+v", entry)
	}
	if entry.Password != token || entry.AgentID != "agent-jwt" || entry.TaskID != 42 {
		t.Fatalf("JWT provenance mismatch: %+v", entry)
	}
	if !entry.ExpiresAt.Equal(expiresAt) {
		t.Fatalf("expires_at = %v, want %v", entry.ExpiresAt, expiresAt)
	}

	withExistingPassword := "Username : alice\nPassword : " + token + "\n\n" + token
	entries = parseCredentialsFromText(withExistingPassword, "agent-jwt", 42)
	if len(entries) != 1 {
		t.Fatalf("duplicate JWT produced %d entries, want 1", len(entries))
	}
	if entries[0].Password != token || entries[0].Source != "mimikatz" {
		t.Fatalf("existing password entry was replaced: %+v", entries[0])
	}
}

func TestCredentialExpiryStateBoundaries(t *testing.T) {
	now := time.Unix(2_000_000_000, 0).UTC()
	windowEnd := now.Add(credentialExpiringWindow)
	for name, test := range map[string]struct {
		expiresAt   time.Time
		expiryKnown bool
		want        string
	}{
		"zero":               {time.Time{}, false, "unknown"},
		"year one":           {time.Date(1, 1, 1, 0, 0, 0, 0, time.UTC), true, "unknown"},
		"horizon only":       {now.Add(-time.Hour), false, "unknown"},
		"before now":         {now.Add(-time.Nanosecond), true, "expired"},
		"exactly now":        {now, true, "expiring"},
		"exactly window end": {windowEnd, true, "expiring"},
		"after window":       {windowEnd.Add(time.Nanosecond), true, "valid"},
	} {
		t.Run(name, func(t *testing.T) {
			cred := db.CredentialEntry{ExpiresAt: test.expiresAt, ExpiryKnown: test.expiryKnown}
			if got := credentialExpiryState(cred, now); got != test.want {
				t.Fatalf("state = %q, want %q", got, test.want)
			}
		})
	}
}

func TestHandleCredentialsPageExpiryStateAndFilter(t *testing.T) {
	s := mustTenantServer(t)
	crypto.InitLootEncryption(testStorageKeyHex)
	now := time.Now()
	entries := []db.CredentialEntry{
		{Username: "expired-user", Type: "cleartext", ExpiresAt: now.Add(-time.Hour)},
		{Username: "expiring-user", Type: "cleartext", ExpiresAt: now.Add(24 * time.Hour)},
		{Username: "valid-user", Type: "cleartext", ExpiresAt: now.Add(30 * 24 * time.Hour)},
	}
	if err := s.db.Create(&entries).Error; err != nil {
		t.Fatalf("seed credentials: %v", err)
	}

	request := func(query string) []struct {
		Username    string `json:"username"`
		ExpiryState string `json:"expiry_state"`
	} {
		t.Helper()
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Set("user_role", db.RoleAdmin)
		c.Request = httptest.NewRequest(http.MethodGet, "/credentials?"+query, nil)
		s.handleCredentialsPage(c)
		if w.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
		}
		var response struct {
			VaultEntries []struct {
				Username    string `json:"username"`
				ExpiryState string `json:"expiry_state"`
			} `json:"vault_entries"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		return response.VaultEntries
	}

	all := request("format=json")
	if len(all) != 3 {
		t.Fatalf("all entries = %d, want 3", len(all))
	}
	wantStates := map[string]string{
		"expired-user":  "expired",
		"expiring-user": "expiring",
		"valid-user":    "valid",
	}
	for _, entry := range all {
		if want := wantStates[entry.Username]; entry.ExpiryState != want {
			t.Fatalf("%s state = %q, want %q", entry.Username, entry.ExpiryState, want)
		}
	}

	for filter, want := range map[string]string{"expired": "expired-user", "expiring": "expiring-user"} {
		filtered := request("format=json&expiry=" + filter)
		if len(filtered) != 1 || filtered[0].Username != want {
			t.Fatalf("expiry=%s returned %+v, want only %s", filter, filtered, want)
		}
	}
}
