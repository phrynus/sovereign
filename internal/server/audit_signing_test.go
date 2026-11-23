package server

import (
	"bufio"
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/r0si/sovereign/internal/auditchain"
	"github.com/r0si/sovereign/internal/db"
)

const auditTestSeed = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func TestAuditSigningSignsAndVerifiesRows(t *testing.T) {
	s := anchorTestServer(t)
	s.auditSigningSeed = auditTestSeed
	rows := seedAuditChain(t, s, 3)

	for i := range rows {
		if rows[i].Signature == "" {
			t.Fatalf("row %d was written without a signature", rows[i].ID)
		}
		valid, present := auditchain.VerifyRow(auditChainRow(&rows[i]))
		if !present || !valid {
			t.Fatalf("row %d has an invalid signature", rows[i].ID)
		}
	}
	if v := s.verifyAuditChain(100); !v.OK {
		t.Fatalf("signed chain failed verification: %+v", v)
	}

	// A full rewrite that recomputes the unkeyed hash must still fail on the
	// signature attached to the edited row.
	last := rows[len(rows)-1]
	last.Details = "rewritten by an attacker"
	last.EntryHash = auditEntryHash(&last)
	if err := s.db.Model(&db.AuditLog{}).Where("id = ?", last.ID).
		Updates(map[string]interface{}{"details": last.Details, "entry_hash": last.EntryHash}).Error; err != nil {
		t.Fatal(err)
	}
	v := s.verifyAuditChain(100)
	if v.OK || !strings.Contains(v.Reason, "signature") {
		t.Fatalf("rewritten signed row was not reported: %+v", v)
	}
}

func TestAuditSignedAnchorDetectsFileRewrite(t *testing.T) {
	s := anchorTestServer(t)
	s.auditSigningSeed = auditTestSeed
	seedAuditChain(t, s, 2)

	path := s.auditAnchorPath()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	fields := strings.Fields(string(data))
	if len(fields) < 4 || fields[3] == "" {
		t.Fatalf("anchor line is not signed: %q", string(data))
	}
	// Flip one character of the signature while keeping the head intact.
	tampered := append([]byte(nil), data...)
	tampered[len(tampered)-2] = '0'
	if tampered[len(tampered)-2] == fields[3][len(fields[3])-1] {
		tampered[len(tampered)-2] = '1'
	}
	if err := os.WriteFile(path, tampered, 0o600); err != nil {
		t.Fatal(err)
	}
	v := s.verifyAuditChain(100)
	if v.OK || !strings.Contains(v.Reason, "anchor") {
		t.Fatalf("tampered anchor was not reported: %+v", v)
	}
}

func TestAuditLegacyUnsignedRowsStillVerify(t *testing.T) {
	s := anchorTestServer(t)
	rows := seedAuditChain(t, s, 2)
	for i := range rows {
		if rows[i].Signature != "" {
			t.Fatalf("legacy row %d unexpectedly signed", rows[i].ID)
		}
	}
	if v := s.verifyAuditChain(100); !v.OK {
		t.Fatalf("legacy chain rejected: %+v", v)
	}
}

func TestAuditExportCarriesRowSignatures(t *testing.T) {
	s := anchorTestServer(t)
	s.auditSigningSeed = auditTestSeed
	seedAuditChain(t, s, 2)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/audit/export", nil)
	c.Set("user", "admin1")
	c.Set("user_role", db.RoleAdmin)
	c.Set("user_id", uint(1))
	s.handleExportAuditChain(c)
	if w.Code != http.StatusOK {
		t.Fatalf("export status = %d", w.Code)
	}

	rows := 0
	scanner := bufio.NewScanner(bytes.NewReader(w.Body.Bytes()))
	for scanner.Scan() {
		var row auditChainExportRow
		if err := json.Unmarshal(scanner.Bytes(), &row); err != nil {
			t.Fatal(err)
		}
		rows++
		if row.Signature == "" {
			t.Fatalf("exported row %d has no signature", row.ID)
		}
		valid, present := auditchain.VerifyRow(auditchain.Row{
			User: row.User, Action: row.Action, Resource: row.Resource,
			AgentID: row.AgentID, IP: row.IP, Success: row.Success,
			Error: row.Error, Details: row.Details, TenantID: row.TenantID,
			PrevHash: row.PrevHash, EntryHash: row.EntryHash, Signature: row.Signature,
		})
		if !present || !valid {
			t.Fatalf("exported row %d has an invalid signature", row.ID)
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if rows != 2 {
		t.Fatalf("exported %d rows, want 2", rows)
	}
}
