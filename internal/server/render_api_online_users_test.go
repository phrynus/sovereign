package server

import (
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

// TestAddUserToDataPreservesOnlineUsersNavStat pins the false-zero fix: a page
// payload must report the online count as a number. It used to be overwritten
// with an empty array after the handler had merged getNavStats, so every page
// said "0 online" while operators were connected.
func TestAddUserToDataPreservesOnlineUsersNavStat(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := &Server{}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/tasks", nil)

	withStat := gin.H{"online_users": int64(3)}
	s.addUserToData(c, withStat)
	if got, ok := withStat["online_users"].(int64); !ok || got != 3 {
		t.Fatalf("online_users = %#v, want int64(3) (nav stat must not be clobbered)", withStat["online_users"])
	}

	withoutStat := gin.H{}
	s.addUserToData(c, withoutStat)
	if _, ok := withoutStat["online_users"].(int64); !ok {
		t.Fatalf("online_users = %#v, want a number on a page that did not merge nav stats", withoutStat["online_users"])
	}
}
