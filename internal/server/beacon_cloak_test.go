package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	promtestutil "github.com/prometheus/client_golang/prometheus/testutil"
)

// TestBeaconCloakRedirectCollapsesFailures pins the no-oracle contract: once
// cloaking is enabled, every unauthenticated failure shape must be
// indistinguishable to a probe, while the default stays a 400.
func TestBeaconCloakRedirectCollapsesFailures(t *testing.T) {
	s, _ := v2TestServer(t)
	s.metrics = NewMetricsCollector(s)

	baseline := v2Post(t, s, "not-json")
	if baseline.Code != http.StatusBadRequest {
		t.Fatalf("cloak disabled: got %d, want 400", baseline.Code)
	}

	const target = "https://www.example.com/"
	s.configMu.Lock()
	s.cfg.Server.BeaconCloakRedirect = target
	s.configMu.Unlock()

	bodies := []string{
		"not-json",
		`{}`,
		`{"type":"beacon","uuid":"22222222-3333-4333-8444-666666666666","cipher_b64":"!!!"}`,
	}
	var first *httptest.ResponseRecorder
	for _, body := range bodies {
		w := v2Post(t, s, body)
		if w.Code != http.StatusFound {
			t.Errorf("body %q: got %d, want 302", body, w.Code)
		}
		if got := w.Header().Get("Location"); got != target {
			t.Errorf("body %q: Location = %q, want %q", body, got, target)
		}
		if first == nil {
			first = w
			continue
		}
		if w.Body.String() != first.Body.String() {
			t.Errorf("body %q produced a distinguishable response: %q vs %q", body, w.Body.String(), first.Body.String())
		}
	}
	if got := promtestutil.ToFloat64(s.metrics.BeaconCloakedTotal.WithLabelValues("frame")); got != 3 {
		t.Fatalf("cloaked frame counter = %v, want 3", got)
	}
}
