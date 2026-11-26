package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/r0si/sovereign/internal/db"
	"github.com/r0si/sovereign/internal/server/middleware"
)

func TestAuditExportContainsAndResultFilters(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := mustTenantServer(t)
	rows := []db.AuditLog{
		{User: "alice", Action: "user_create", TenantID: 1, Success: true, PrevHash: "p1", EntryHash: "e1"},
		{User: "alice", Action: "agent_delete", TenantID: 1, Success: false, PrevHash: "p2", EntryHash: "e2"},
		{User: "bob", Action: "user_update", TenantID: 1, Success: false, PrevHash: "p3", EntryHash: "e3"},
	}
	for i := range rows {
		if err := s.db.Create(&rows[i]).Error; err != nil {
			t.Fatal(err)
		}
	}
	call := func(query string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/audit/export"+query, nil)
		c.Set("user", "admin")
		c.Set("user_role", db.RoleAdmin)
		c.Set("user_id", uint(1))
		c.Set(middleware.TenantIDContextKey, uint(1))
		s.handleExportAuditChain(c)
		return w
	}
	w := call("?action=user&match=contains&result=failed")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	var got []auditChainExportRow
	for _, line := range strings.Split(strings.TrimSpace(w.Body.String()), "\n") {
		var row auditChainExportRow
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			t.Fatal(err)
		}
		got = append(got, row)
	}
	if len(got) != 1 || got[0].Action != "user_update" {
		t.Fatalf("contains+result rows = %+v", got)
	}
	scope := w.Header().Get("X-Audit-Export-Scope")
	if !strings.Contains(scope, "action=user") || !strings.Contains(scope, "match=contains") || !strings.Contains(scope, "result=failed") {
		t.Fatalf("scope omitted real filters: %q", scope)
	}
	if call("?match=regex").Code != http.StatusBadRequest {
		t.Fatal("invalid match mode was accepted")
	}
	if call("?result=maybe").Code != http.StatusBadRequest {
		t.Fatal("invalid result filter was accepted")
	}
}
