package server

import (
	"sort"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
)

// auditExportScopeHeader names the header that carries an audit export's filters, published by the capabilities document.
const auditExportScopeHeader = "X-Audit-Export-Scope"

// The filters an audit export applied, as one header.
//
// `X-Audit-Export-Filtered` already says that a selection was made - and the 199 warning says a filtered file is not a
// contiguous chain, which is the important caveat. What neither says is **which** selection produced this file, so an
// investigator holding `sovereign-audit-chain_filtered_....ndjson` cannot reproduce it. The scope header names every filter that
// shaped the rows, in the same format as the task export's.
//
// Format: `action=login;since=2026-10-01T00:00:00Z;tenant=7;user=admin` - semicolon-separated, sorted by key,
// or `all`. `since_id` is included because it changes which rows appear just as much as a time bound.
// A non-zero tenant is always included: tenant exports select from the single global hash chain.
func auditExportScope(c *gin.Context, tenantID uint) string {
	if c == nil {
		return ""
	}
	parts := make([]string, 0, 7)
	if tenantID != 0 {
		parts = append(parts, "tenant="+strconv.Itoa(int(tenantID)))
	}
	for _, name := range []string{"action", "user", "agent_id", "since", "until", "since_id"} {
		if value := strings.TrimSpace(c.Query(name)); value != "" {
			parts = append(parts, name+"="+value)
		}
	}
	if len(parts) == 0 {
		return ""
	}
	sort.Strings(parts)
	return strings.Join(parts, ";")
}
