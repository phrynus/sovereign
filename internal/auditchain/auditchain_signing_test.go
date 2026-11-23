package auditchain

import (
	"encoding/hex"
	"strings"
	"testing"
)

func signingSeed() string {
	return hex.EncodeToString([]byte("0123456789abcdef0123456789abcdef"))
}

func signedTestRow() Row {
	row := Row{
		ID: 7, TenantID: 3, User: "alice", Action: "login", Resource: "auth",
		AgentID: "agent-1", IP: "192.0.2.7", Success: true, Details: "ok",
		PrevHash: strings.Repeat("a", 64), CreatedAt: "2026-11-23T00:00:00Z",
	}
	row.EntryHash = HashRow(row)
	return row
}

func TestRowSignatureDetectsFullRewrite(t *testing.T) {
	row := signedTestRow()
	sig, err := SignRow(row, signingSeed())
	if err != nil {
		t.Fatal(err)
	}
	row.Signature = sig
	if valid, present := VerifyRow(row); !present || !valid {
		t.Fatal("fresh signature did not verify")
	}
	if verdict := Verify([]Row{row}); !verdict.OK {
		t.Fatalf("valid signed row rejected: %+v", verdict)
	}

	// The attacker recomputes the unkeyed hash after editing the row: the
	// chain stays internally consistent but the signature must fail.
	tampered := row
	tampered.Details = "admin=true"
	tampered.EntryHash = HashRow(tampered)
	if valid, present := VerifyRow(tampered); !present || valid {
		t.Fatal("rewritten row passed signature verification")
	}
	verdict := Verify([]Row{tampered})
	if verdict.OK || verdict.Reason != "row signature is invalid" {
		t.Fatalf("rewrite not reported: %+v", verdict)
	}
}

func TestLegacyUnsignedRowStillVerifies(t *testing.T) {
	row := signedTestRow()
	if verdict := Verify([]Row{row}); !verdict.OK {
		t.Fatalf("legacy row rejected: %+v", verdict)
	}
}

func TestAnchorSignatureDetectsRewrittenHead(t *testing.T) {
	seed := signingSeed()
	const ts = "2026-11-23T00:00:00Z"
	sig, err := SignAnchor(strings.Repeat("b", 64), 9, ts, seed)
	if err != nil {
		t.Fatal(err)
	}
	if !VerifyAnchor(strings.Repeat("b", 64), 9, ts, sig) {
		t.Fatal("fresh anchor signature did not verify")
	}
	if VerifyAnchor(strings.Repeat("c", 64), 9, ts, sig) {
		t.Fatal("rewritten head passed anchor verification")
	}
	if !VerifyAnchor(strings.Repeat("c", 64), 9, ts, "") {
		t.Fatal("legacy unsigned anchor line must stay accepted")
	}
}
