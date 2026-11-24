package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/r0si/sovereign/internal/db"
)

type navigatorLayerTechnique struct {
	TechniqueID string `json:"techniqueID"`
	Tactic      string `json:"tactic"`
	Color       string `json:"color"`
	Comment     string `json:"comment"`
	Metadata    []struct {
		Name  string `json:"name"`
		Value string `json:"value"`
	} `json:"metadata"`
}

type navigatorLayer struct {
	Versions   map[string]string         `json:"versions"`
	Techniques []navigatorLayerTechnique `json:"techniques"`
}

func callMitreNavigator(t *testing.T, s *Server, target string) (*httptest.ResponseRecorder, navigatorLayer) {
	t.Helper()
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, target, nil)
	s.handleMitreNavigator(c)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d (%s)", w.Code, w.Body.String())
	}
	var body struct {
		Success bool `json:"success"`
		Data    struct {
			Layer navigatorLayer `json:"layer"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("invalid response: %v (%s)", err, w.Body.String())
	}
	if !body.Success {
		t.Fatalf("response is not a success envelope: %s", w.Body.String())
	}
	return w, body.Data.Layer
}

func navigatorTechnique(layer navigatorLayer, id, tactic string) *navigatorLayerTechnique {
	for i := range layer.Techniques {
		if layer.Techniques[i].TechniqueID == id && layer.Techniques[i].Tactic == tactic {
			return &layer.Techniques[i]
		}
	}
	return nil
}

func TestHandleMitreNavigator(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := mustTenantServer(t)

	now := time.Now().Truncate(time.Second)
	if err := s.db.Create(&db.Task{
		TenantID: 1, AgentID: "a-1", Type: "shell", Command: "whoami", Status: "completed",
		CreatedBy: "alice", CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed task: %v", err)
	}

	_, layer := callMitreNavigator(t, s, "/api/mitre/navigator")
	if layer.Versions["navigator"] != "4.5" || layer.Versions["attack"] != "14" {
		t.Fatalf("versions = %v", layer.Versions)
	}

	idPattern := regexp.MustCompile(`^T[0-9]{4}$`)
	seen := map[string]bool{}
	for _, tech := range layer.Techniques {
		if !idPattern.MatchString(tech.TechniqueID) {
			t.Errorf("techniqueID %q is not a MITRE technique id", tech.TechniqueID)
		}
		key := tech.TechniqueID + "|" + tech.Tactic
		if seen[key] {
			t.Errorf("duplicate technique %s", key)
		}
		seen[key] = true
	}

	// T1053 is listed in both Execution and Persistence; both belong in the
	// layer, but neither may be claimed as covered by an unrelated entry.
	if layer.Techniques == nil || !seen["T1053|execution"] || !seen["T1053|persistence"] {
		t.Fatalf("T1053 tactics missing from layer: %v", seen)
	}
	shell := navigatorTechnique(layer, "T1059", "execution")
	if shell == nil {
		t.Fatal("T1059/execution missing")
	}
	if shell.Color != "#2f8f46" || !strings.Contains(shell.Comment, "shell") {
		t.Fatalf("T1059/execution = %+v, want covered in green with shell in the comment", shell)
	}
	if len(shell.Metadata) != 1 || shell.Metadata[0].Name != "task_types" || !strings.Contains(shell.Metadata[0].Value, "shell") {
		t.Fatalf("T1059/execution metadata = %+v", shell.Metadata)
	}
	uncovered := navigatorTechnique(layer, "T1053", "execution")
	if uncovered == nil {
		t.Fatal("T1053/execution missing")
	}
	if uncovered.Color != "#c7c7c7" {
		t.Fatalf("T1053/execution = %+v, want uncovered grey", uncovered)
	}

	// Filtering to another agent must turn the same technique grey rather
	// than falling back to the deployment-wide census.
	_, other := callMitreNavigator(t, s, "/api/mitre/navigator?agent_id=a-other")
	filtered := navigatorTechnique(other, "T1059", "execution")
	if filtered == nil {
		t.Fatal("T1059/execution missing from filtered layer")
	}
	if filtered.Color != "#c7c7c7" {
		t.Fatalf("filtered T1059/execution = %+v, want uncovered grey", filtered)
	}
}

func TestHandleMitreNavigatorQueryFailureIs500(t *testing.T) {
	assertQueryFailureIs500(t, "mitre navigator", "/api/mitre/navigator", func(s *Server, c *gin.Context) {
		s.handleMitreNavigator(c)
	})
}
