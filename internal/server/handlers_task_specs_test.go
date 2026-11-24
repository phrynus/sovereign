package server

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/r0si/sovereign/pkg/protocol"
)

func TestHandleTaskSpecsReturnsProtocolRegistry(t *testing.T) {
	s := &Server{}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	s.handleTaskSpecs(c)

	var body struct {
		Success bool `json:"success"`
		Data    struct {
			Count int `json:"count"`
			Specs []struct {
				Type string `json:"type"`
			} `json:"specs"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("invalid response: %v (%s)", err, w.Body.String())
	}
	if !body.Success {
		t.Fatalf("response is not a success envelope: %s", w.Body.String())
	}
	if body.Data.Count != len(protocol.AllSpecs()) || len(body.Data.Specs) != body.Data.Count {
		t.Fatalf("catalog count = %d/%d, registry = %d", body.Data.Count, len(body.Data.Specs), len(protocol.AllSpecs()))
	}
	seen := make(map[string]bool, len(body.Data.Specs))
	for _, spec := range body.Data.Specs {
		seen[spec.Type] = true
	}
	for _, want := range []string{"persistence_add", "keep_awake", "watchdog_start"} {
		if !seen[want] {
			t.Errorf("catalog omitted dispatchable task %q", want)
		}
	}
}
