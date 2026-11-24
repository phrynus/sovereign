package server

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/r0si/sovereign/pkg/protocol"
)

// handleTaskSpecs exposes the shared protocol registry to the console and to
// automation. It is the same source the generated command reference and the
// agent's dispatch table use, so a task discovered here is really dispatchable
// (and its parameter names match the wire contract).
func (s *Server) handleTaskSpecs(c *gin.Context) {
	if s == nil {
		respondError(c, http.StatusServiceUnavailable, "server is not initialized")
		return
	}
	specs := protocol.AllSpecs()
	respond(c, gin.H{"specs": specs, "count": len(specs)})
}
