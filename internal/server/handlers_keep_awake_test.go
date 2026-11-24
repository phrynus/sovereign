package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/r0si/sovereign/internal/db"
)

func keepAwakeCtx(t *testing.T, body string) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Params = gin.Params{{Key: "id", Value: "aaaaaaaa-bbbb-4333-8444-cccccccccccc"}}
	c.Request = httptest.NewRequest(http.MethodPost, "/agents/id/keep_awake", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set("user", "admin1")
	return c, w
}

func TestHandleKeepAwakeQueuesBoundedTask(t *testing.T) {
	s, database := v2TestServer(t)
	if err := database.Create(&db.Implant{
		ID: "aaaaaaaa-bbbb-4333-8444-cccccccccccc", Hostname: "keep-awake", Status: "online",
	}).Error; err != nil {
		t.Fatal(err)
	}

	c, w := keepAwakeCtx(t, `{"seconds":600}`)
	s.handleKeepAwake(c)
	if c.Writer.Status() != http.StatusOK {
		t.Fatalf("status = %d (%s)", c.Writer.Status(), w.Body.String())
	}
	var task db.Task
	if err := database.Where("type = ?", "keep_awake").Order("id DESC").First(&task).Error; err != nil {
		t.Fatal(err)
	}
	if task.Command != "600" {
		t.Fatalf("queued command = %q, want 600", task.Command)
	}

	bad, _ := keepAwakeCtx(t, `{"seconds":99999}`)
	s.handleKeepAwake(bad)
	if bad.Writer.Status() != http.StatusBadRequest {
		t.Fatalf("invalid seconds status = %d", bad.Writer.Status())
	}
}
