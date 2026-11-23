// Package auditchain verifies an exported audit log offline.
//
// The export endpoint has promised since it was written that "a third party can verify offline", but the hash and the
// linkage only existed in the server: anyone holding the file needed a running console to check it, which for a third
// party means not checking it at all. The hash lives here so the server and `cmd/verify-audit` compute the SAME digest -
// two implementations would produce a false tampering alarm on one side.
//
// The legacy hash is unkeyed and prev_hash is not an input, so unsigned rows
// detect PARTIAL edits only. New rows may additionally carry an Ed25519
// signature when the operator configured a signing seed; those rows resist a
// full rewrite that recomputes every hash. Saying which applies is part of the
// verdict, not a footnote.
package auditchain

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// Row is one exported audit entry, in the shape the export writes.
type Row struct {
	ID        uint   `json:"id"`
	TenantID  uint   `json:"tenant_id"`
	User      string `json:"user"`
	Action    string `json:"action"`
	Resource  string `json:"resource"`
	AgentID   string `json:"agent_id"`
	IP        string `json:"ip"`
	Success   bool   `json:"success"`
	Error     string `json:"error"`
	Details   string `json:"details"`
	PrevHash  string `json:"prev_hash"`
	EntryHash string `json:"entry_hash"`
	CreatedAt string `json:"created_at"`
	Signature string `json:"signature,omitempty"`
}

// Hash is the digest of one entry's fields.
//
// The exact byte layout is load-bearing: it must match what every historical row was written with, or every export
// appears tampered. Fields are joined with "|" and the boolean rendered by %t, exactly as the server has always done.
func Hash(user, action, resource, agentID, ip string, success bool, errText, details string) string {
	var buf []byte
	buf = fmt.Appendf(buf, "%s|%s|%s|%s|%s|%t|%s|%s",
		user, action, resource, agentID, ip, success, errText, details)
	sum := sha256.Sum256(buf)
	return fmt.Sprintf("%x", sum)
}

// HashRow renders the digest the row claims to have.
func HashRow(row Row) string {
	return Hash(row.User, row.Action, row.Resource, row.AgentID, row.IP, row.Success, row.Error, row.Details)
}

// Problem is one row that does not check out.
type Problem struct {
	ID      uint   `json:"id"`
	Problem string `json:"problem"`
}

// Verdict is the result of walking an export.
type Verdict struct {
	OK         bool      `json:"ok"`
	Checked    int       `json:"checked"`
	Problems   []Problem `json:"problems"`
	FirstBadID uint      `json:"first_bad_id,omitempty"`
	// Discontinuities counts id gaps, which a purge legitimately leaves behind (it writes a marker row).
	Discontinuities int `json:"discontinuities"`
	// Reason summarises the first failure for a one-line answer.
	Reason string `json:"reason,omitempty"`
	// SelectionKnown reports whether the file itself said whether it is a filtered selection. The newline-delimited
	// export keeps that in response headers, which a file does not have, so "unknown" is a real answer here.
	SelectionKnown bool `json:"selection_known"`
	// Filtered reports the file's own admission that the rows are a selection rather than the whole chain.
	Filtered bool `json:"filtered"`
	// SignatureKind says which of this product's four "signatures" this verdict is about. The word is overloaded enough that a
	// reader can take this chain - an UNKEYED hash - for a keyed signature, and then believe the log is authenticated when it
	// is only partially tamper-evident. Carried in the verdict so the correction travels with the data.
	SignatureKind string `json:"signature_kind"`
	// AnchorVerified is true when a supplied out-of-band chain head matched both
	// the export and its own signature (when present).
	AnchorVerified bool `json:"anchor_verified,omitempty"`
	// AnchorSigned is false for a legacy unsigned anchor line.
	AnchorSigned bool `json:"anchor_signed,omitempty"`
}

// SignatureKind is the description every audit verdict carries.
//
// It lives here, beside the verifier, because both the server and cmd/verify-audit report this verdict and neither should have
// to import the other to say the same sentence.
const SignatureKind = "audit hash chain: legacy rows are unkeyed (a partial-edit detector that does not resist a full rewrite); new rows may carry Ed25519 signatures when enabled"

// SigningPayload is the canonical, unambiguous byte sequence a row signature
// covers. CreatedAt and ID are absent: the database assigns both after the row
// is built, so including them would make fresh signatures unverifiable.
func SigningPayload(row Row) []byte {
	var buf []byte
	for _, field := range []string{row.User, row.Action, row.Resource, row.AgentID, row.IP, row.Error, row.Details} {
		buf = fmt.Appendf(buf, "%d:%s", len(field), field)
	}
	buf = fmt.Appendf(buf, "|%t|%d|%s|%s", row.Success, row.TenantID, row.PrevHash, row.EntryHash)
	return buf
}

// SignRow returns "<public key hex>.<signature hex>". Carrying the public key
// lets a rotated key still verify old rows offline; trust comes from the
// out-of-band archived chain head, not from a key inside the same export.
func SignRow(row Row, seedHex string) (string, error) {
	seed, err := hex.DecodeString(seedHex)
	if err != nil || len(seed) != ed25519.SeedSize {
		return "", fmt.Errorf("audit signing key must be %d hex bytes", ed25519.SeedSize)
	}
	priv := ed25519.NewKeyFromSeed(seed)
	pub := priv.Public().(ed25519.PublicKey)
	return hex.EncodeToString(pub) + "." + hex.EncodeToString(ed25519.Sign(priv, SigningPayload(row))), nil
}

// VerifyRow reports whether a row carries a valid signature and whether it
// carries one at all. An empty signature is a pre-keying legacy row: callers
// fall back to the unkeyed hash check for those.
func VerifyRow(row Row) (valid bool, present bool) {
	if row.Signature == "" {
		return false, false
	}
	parts := strings.Split(row.Signature, ".")
	if len(parts) != 2 {
		return false, true
	}
	pub, errPub := hex.DecodeString(parts[0])
	sig, errSig := hex.DecodeString(parts[1])
	if errPub != nil || errSig != nil || len(pub) != ed25519.PublicKeySize || len(sig) != ed25519.SignatureSize {
		return false, true
	}
	return ed25519.Verify(ed25519.PublicKey(pub), SigningPayload(row), sig), true
}

// AnchorPayload and its helpers protect the external head file with the same
// key, so an attacker who can also write that file cannot forge a head for a
// rewritten table.
func AnchorPayload(head string, id uint, timestamp string) []byte {
	return []byte(head + "|" + strconv.FormatUint(uint64(id), 10) + "|" + timestamp)
}

func SignAnchor(head string, id uint, timestamp, seedHex string) (string, error) {
	seed, err := hex.DecodeString(seedHex)
	if err != nil || len(seed) != ed25519.SeedSize {
		return "", fmt.Errorf("audit signing key must be %d hex bytes", ed25519.SeedSize)
	}
	priv := ed25519.NewKeyFromSeed(seed)
	pub := priv.Public().(ed25519.PublicKey)
	return hex.EncodeToString(pub) + "." + hex.EncodeToString(ed25519.Sign(priv, AnchorPayload(head, id, timestamp))), nil
}

func VerifyAnchor(head string, id uint, timestamp, signature string) bool {
	if signature == "" {
		return true // legacy unsigned anchor line
	}
	parts := strings.Split(signature, ".")
	if len(parts) != 2 {
		return false
	}
	pub, errPub := hex.DecodeString(parts[0])
	sig, errSig := hex.DecodeString(parts[1])
	if errPub != nil || errSig != nil || len(pub) != ed25519.PublicKeySize || len(sig) != ed25519.SignatureSize {
		return false
	}
	return ed25519.Verify(ed25519.PublicKey(pub), AnchorPayload(head, id, timestamp), sig)
}

// Verify walks the rows in id order and checks every digest and every link.
//
// The rows are sorted by id rather than trusted in file order: an export is a list, and a verifier that followed the
// order of the file could be walked backwards.
func Verify(rows []Row) Verdict {
	ordered := append([]Row(nil), rows...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].ID < ordered[j].ID })

	verdict := Verdict{OK: true, Problems: []Problem{}}
	var previous *Row
	for i := range ordered {
		row := &ordered[i]
		verdict.Checked++

		if got := HashRow(*row); got != row.EntryHash {
			verdict.record(row.ID, "row content does not match its entry_hash: "+got+" != "+row.EntryHash)
			continue
		}
		if valid, present := VerifyRow(*row); present && !valid {
			verdict.record(row.ID, "row signature is invalid")
			continue
		}
		if previous != nil {
			// A gap in ids is what a legitimate purge leaves; a broken link is not. Both are reported, only the
			// second fails.
			if row.ID != previous.ID+1 {
				verdict.Discontinuities++
			}
			if row.PrevHash != previous.EntryHash {
				verdict.record(row.ID, "prev_hash does not match the previous entry")
				continue
			}
		}
		previous = row
	}
	return verdict
}

// record notes a failure without stopping the walk, so a report lists what is wrong rather than only the first.
func (v *Verdict) record(id uint, problem string) {
	v.OK = false
	v.Problems = append(v.Problems, Problem{ID: id, Problem: problem})
	if v.FirstBadID == 0 {
		v.FirstBadID = id
		v.Reason = problem
	}
}

// Anchor is the JSON shape served by GET /audit/chain/anchor. Archive that
// document out of band: a key carried inside the export proves nothing about
// origin, and a whole-table rewrite can carry a matching row signature set.
type Anchor struct {
	ID              uint   `json:"id"`
	EntryHash       string `json:"entry_hash"`
	Signature       string `json:"signature,omitempty"`
	AnchorID        uint   `json:"anchor_id,omitempty"`
	AnchorTimestamp string `json:"anchor_timestamp,omitempty"`
	AnchorSignature string `json:"anchor_signature,omitempty"`
}

// Check verifies the archived head against the export: its signature, and its
// agreement with the newest row. A non-empty problem means the anchor cannot be
// trusted; an unsigned legacy anchor is accepted but reported as such.
func (a Anchor) Check(rows []Row) (signed bool, problem string) {
	if a.EntryHash == "" {
		return false, "anchor has no entry hash"
	}
	signed = a.AnchorSignature != ""
	if signed && !VerifyAnchor(a.EntryHash, a.AnchorID, a.AnchorTimestamp, a.AnchorSignature) {
		return false, "anchor signature is invalid"
	}
	if len(rows) == 0 {
		return signed, ""
	}
	ordered := append([]Row(nil), rows...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].ID < ordered[j].ID })
	newest := ordered[len(ordered)-1]
	if newest.EntryHash != a.EntryHash {
		return signed, "anchor head does not match the newest exported row"
	}
	if a.ID != 0 && newest.ID != a.ID {
		return signed, "anchor row id does not match the newest exported row"
	}
	if a.Signature != "" && newest.Signature != a.Signature {
		return signed, "anchor row signature differs from the newest exported row"
	}
	return signed, ""
}

// Record adds a finding to a verdict from a caller outside this package (the
// CLI uses it when an archived anchor disagrees with the export).
func (v *Verdict) Record(id uint, problem string) {
	v.record(id, problem)
}
