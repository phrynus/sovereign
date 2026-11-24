package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/r0si/sovereign/internal/testutil"
)

func TestHandleAPISendChatMessage_SizeLimit(t *testing.T) {
	f := newChatTestFixtures(t)
	send := func(msg string) *httptest.ResponseRecorder {
		return f.send(t, "op1", 1, "general", msg)
	}

	if w := send(strings.Repeat("a", MaxChatMessageBytes)); w.Code != http.StatusOK {
		t.Fatalf("boundary message (exactly max) rejected: %d %s", w.Code, w.Body.String())
	}
	if w := send(strings.Repeat("a", MaxChatMessageBytes+1)); w.Code != http.StatusBadRequest {
		t.Fatalf("oversized message accepted: %d %s", w.Code, w.Body.String())
	}
}

func TestHandleSocks_RejectsInvalidPort(t *testing.T) {
	s := &Server{db: testutil.SetupTestDB(t)}

	for _, port := range []string{"0", "-1", "65536", "abc", "1e3"} {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request, _ = http.NewRequest(http.MethodPost, "/agents/a1/socks",
			strings.NewReader("port="+port))
		c.Request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		c.Set("user_role", "operator")
		c.Params = gin.Params{{Key: "id", Value: "a1"}}
		s.handleSocks(c)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("port %q: expected 400, got %d; body=%s", port, w.Code, w.Body.String())
		}
	}
}
