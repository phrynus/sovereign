package server

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"io"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const credentialMetadataMaxBytes = 1 << 20

type parsedCredentialMetadata struct {
	Subtype    string
	ExpiresAt  time.Time
	RenewUntil *time.Time
}

// parseCredentialMetadata extracts display-only validity metadata from a JWT or
// a base64-encoded MIT ccache v4. JWT signatures are deliberately not verified:
// these values describe operator-supplied attack material and must never be
// used as evidence of identity, authorization, or trust. Malformed input returns
// false without logging or exposing the credential value.
func parseCredentialMetadata(value string) (parsedCredentialMetadata, bool) {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > credentialMetadataMaxBytes {
		return parsedCredentialMetadata{}, false
	}

	if strings.Count(value, ".") == 2 {
		claims := jwt.MapClaims{}
		if _, _, err := jwt.NewParser().ParseUnverified(value, claims); err == nil {
			expiresAt, err := claims.GetExpirationTime()
			if err == nil && expiresAt != nil && expiresAt.Time.Unix() > 0 {
				return parsedCredentialMetadata{
					Subtype:   "jwt",
					ExpiresAt: expiresAt.Time,
				}, true
			}
		}
	}

	decoded, err := base64.StdEncoding.DecodeString(value)
	if err != nil {
		decoded, err = base64.RawStdEncoding.DecodeString(value)
	}
	if err != nil || len(decoded) > credentialMetadataMaxBytes {
		return parsedCredentialMetadata{}, false
	}
	return parseCCacheV4Metadata(decoded)
}

func parseCCacheV4Metadata(data []byte) (parsedCredentialMetadata, bool) {
	if len(data) < 4 || data[0] != 0x05 || data[1] != 0x04 {
		return parsedCredentialMetadata{}, false
	}
	reader := bytes.NewReader(data)
	var version, headerLen uint16
	if binary.Read(reader, binary.BigEndian, &version) != nil || version != 0x0504 ||
		binary.Read(reader, binary.BigEndian, &headerLen) != nil ||
		!skipCCacheBytes(reader, uint32(headerLen)) ||
		!skipCCachePrincipal(reader) {
		return parsedCredentialMetadata{}, false
	}

	// Only the first credential is inspected. Its client/server principals and
	// key precede the four big-endian uint32 timestamps.
	if !skipCCachePrincipal(reader) {
		return parsedCredentialMetadata{}, false
	}
	if !skipCCachePrincipal(reader) {
		return parsedCredentialMetadata{}, false
	}
	var keyType, encryptionType, keyLen uint16
	if binary.Read(reader, binary.BigEndian, &keyType) != nil ||
		binary.Read(reader, binary.BigEndian, &encryptionType) != nil ||
		binary.Read(reader, binary.BigEndian, &keyLen) != nil ||
		!skipCCacheBytes(reader, uint32(keyLen)) {
		return parsedCredentialMetadata{}, false
	}
	var authTime, startTime, endTime, renewTill uint32
	if binary.Read(reader, binary.BigEndian, &authTime) != nil ||
		binary.Read(reader, binary.BigEndian, &startTime) != nil ||
		binary.Read(reader, binary.BigEndian, &endTime) != nil ||
		binary.Read(reader, binary.BigEndian, &renewTill) != nil {
		return parsedCredentialMetadata{}, false
	}

	metadata := parsedCredentialMetadata{
		Subtype:   "ccache",
		ExpiresAt: time.Unix(int64(endTime), 0).UTC(),
	}
	if renewTill > endTime {
		renewUntil := time.Unix(int64(renewTill), 0).UTC()
		metadata.RenewUntil = &renewUntil
	}
	return metadata, true
}

func skipCCachePrincipal(reader *bytes.Reader) bool {
	var nameType, componentCount uint32
	if binary.Read(reader, binary.BigEndian, &nameType) != nil ||
		binary.Read(reader, binary.BigEndian, &componentCount) != nil {
		return false
	}
	// Each component needs at least its four-byte length. Reject impossible
	// counts before looping so a tiny malformed cache cannot force work
	// proportional to an attacker-controlled uint32.
	if uint64(componentCount) > uint64(reader.Len()/4) {
		return false
	}
	var length uint32
	if binary.Read(reader, binary.BigEndian, &length) != nil || !skipCCacheBytes(reader, length) {
		return false
	}
	for range componentCount {
		if binary.Read(reader, binary.BigEndian, &length) != nil || !skipCCacheBytes(reader, length) {
			return false
		}
	}
	return true
}

func skipCCacheBytes(reader *bytes.Reader, length uint32) bool {
	if uint64(length) > uint64(reader.Len()) {
		return false
	}
	_, err := reader.Seek(int64(length), io.SeekCurrent)
	return err == nil
}
