package server

import (
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestLegacyFileRoutesAreRemoved(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	s := &Server{}
	s.registerAgentCommandRoutes(router.Group("/"))

	removed := map[string]bool{
		http.MethodPost + " /agents/:id/upload":               true,
		http.MethodPost + " /agents/:id/files/push":           true,
		http.MethodPost + " /agents/:id/files/pull":           true,
		http.MethodPost + " /agents/:id/files/upload":         true,
		http.MethodGet + " /agents/:id/files/exfil/:filename": true,
	}
	registered := make(map[string]bool, len(router.Routes()))
	for _, route := range router.Routes() {
		registered[route.Method+" "+route.Path] = true
	}
	for route := range removed {
		if registered[route] {
			t.Fatalf("legacy file route still registered: %s", route)
		}
	}
}
