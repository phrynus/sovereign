package server

import (
	"testing"

	"github.com/r0si/sovereign/internal/config"
	"github.com/r0si/sovereign/internal/crypto"
	"github.com/r0si/sovereign/internal/testutil"
)

func TestOperatorResultInjectionRoutesAndSpecAreRemoved(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Server.JWTSecret = "result-route-removal-secret-at-least-32chars"
	setServerTestKeys(cfg)
	crypto.InitLootEncryption(cfg.Crypto.LootKey)
	s := New(cfg, testutil.SetupTestDB(t))
	s.SetupRoutes()

	removed := map[string]bool{
		"/api/scan/result":    true,
		"/api/browser/result": true,
		"/api/wifi/result":    true,
		"/api/lateral/result": true,
		"/api/privesc/result": true,
	}
	for _, route := range s.router.Routes() {
		if removed[route.Path] {
			t.Errorf("forgeable result route is still registered: %s %s", route.Method, route.Path)
		}
	}
	spec := readSpec(t)
	for path := range removed {
		if spec.has(path) {
			t.Errorf("forgeable result path is still documented: %s", path)
		}
	}
}
