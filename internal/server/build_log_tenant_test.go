package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/r0si/sovereign/internal/db"
)

func seedBuildLog(t *testing.T, s *Server, entry db.BuildLog) db.BuildLog {
	t.Helper()
	if err := s.db.Create(&entry).Error; err != nil {
		t.Fatalf("seed build log: %v", err)
	}
	return entry
}

func TestBuildLogDownloadIsTenantScoped(t *testing.T) {
	s := mustTenantServer(t)
	s.cfg.Server.DataDir = t.TempDir()
	writeArtifact := func(name, contents string) string {
		t.Helper()
		path := filepath.Join(s.implantDataDir(), name)
		if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
			t.Fatalf("write artifact: %v", err)
		}
		return path
	}
	own := seedBuildLog(t, s, db.BuildLog{TenantID: 1, Filename: "own.exe", OutputPath: writeArtifact("own.exe", "OWN_ARTIFACT"), Status: "success"})
	foreign := seedBuildLog(t, s, db.BuildLog{TenantID: 2, Filename: "foreign.exe", OutputPath: writeArtifact("foreign.exe", "FOREIGN_ARTIFACT"), Status: "success"})
	legacy := seedBuildLog(t, s, db.BuildLog{TenantID: 0, Filename: "legacy.exe", OutputPath: writeArtifact("legacy.exe", "LEGACY_ARTIFACT"), Status: "success"})

	download := func(username string, tenantID uint, id uint) *httptest.ResponseRecorder {
		t.Helper()
		c, w := tenantScopedAdminContext(s, t, username, tenantID)
		c.Params = gin.Params{{Key: "id", Value: strconv.FormatUint(uint64(id), 10)}}
		s.handleDownloadBuild(c)
		return w
	}
	if w := download("build-download-owner", 1, own.ID); w.Code != http.StatusOK || w.Body.String() != "OWN_ARTIFACT" {
		t.Fatalf("own download = %d %q", w.Code, w.Body.String())
	}
	for _, id := range []uint{foreign.ID, legacy.ID} {
		w := download("build-download-owner", 1, id)
		if w.Code != http.StatusNotFound {
			t.Fatalf("non-visible download %d = %d (%s)", id, w.Code, w.Body.String())
		}
		if strings.Contains(w.Body.String(), "ARTIFACT") {
			t.Fatalf("non-visible download leaked artifact contents: %s", w.Body.String())
		}
	}
	if w := download("build-download-global", 0, legacy.ID); w.Code != http.StatusOK || w.Body.String() != "LEGACY_ARTIFACT" {
		t.Fatalf("global legacy download = %d %q", w.Code, w.Body.String())
	}
}

func TestBuildLogListAndStatisticsAreTenantScoped(t *testing.T) {
	s := mustTenantServer(t)
	now := time.Now()
	seedBuildLog(t, s, db.BuildLog{TenantID: 1, Platform: "windows", Status: "success", Filename: "OWN_SUCCESS", CreatedAt: now})
	seedBuildLog(t, s, db.BuildLog{TenantID: 1, Platform: "linux", Status: "failed", Error: "OWN_COMPILE_FAILURE", CreatedAt: now})
	seedBuildLog(t, s, db.BuildLog{TenantID: 2, Platform: "windows", Status: "success", Filename: "FOREIGN_SUCCESS", CreatedAt: now})
	seedBuildLog(t, s, db.BuildLog{TenantID: 0, Platform: "windows", Status: "success", Filename: "LEGACY_SUCCESS", CreatedAt: now})

	c, w := tenantScopedAdminContext(s, t, "build-list-owner", 1)
	s.handleBuildLogs(c)
	if w.Code != http.StatusOK {
		t.Fatalf("list = %d (%s)", w.Code, w.Body.String())
	}
	var listed struct {
		Logs         []db.BuildLog `json:"logs"`
		Total        int           `json:"total"`
		SuccessCount int           `json:"success_count"`
		FailedCount  int           `json:"failed_count"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &listed); err != nil {
		t.Fatalf("decode list: %v (%s)", err, w.Body.String())
	}
	if listed.Total != 2 || listed.SuccessCount != 1 || listed.FailedCount != 1 || len(listed.Logs) != 2 {
		t.Fatalf("scoped list/counts = %+v", listed)
	}
	for _, entry := range listed.Logs {
		if entry.TenantID != 1 {
			t.Fatalf("scoped list leaked tenant %d", entry.TenantID)
		}
	}
	if strings.Contains(w.Body.String(), "FOREIGN_SUCCESS") || strings.Contains(w.Body.String(), "LEGACY_SUCCESS") {
		t.Fatalf("scoped list leaked another tenant: %s", w.Body.String())
	}

	c, w = tenantScopedAdminContext(s, t, "build-list-global", 0)
	s.handleBuildLogs(c)
	if w.Code != http.StatusOK {
		t.Fatalf("global list = %d (%s)", w.Code, w.Body.String())
	}
	listed = struct {
		Logs         []db.BuildLog `json:"logs"`
		Total        int           `json:"total"`
		SuccessCount int           `json:"success_count"`
		FailedCount  int           `json:"failed_count"`
	}{}
	if err := json.Unmarshal(w.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	if listed.Total != 4 || listed.SuccessCount != 3 || listed.FailedCount != 1 {
		t.Fatalf("global list/counts = %+v", listed)
	}

	c, w = tenantScopedAdminContext(s, t, "build-failure-owner", 1)
	s.handleBuildFailureStats(c)
	if w.Code != http.StatusOK {
		t.Fatalf("failure stats = %d (%s)", w.Code, w.Body.String())
	}
	var failures struct {
		TotalFailed int `json:"total_failed"`
		Causes      []struct {
			Code    string `json:"code"`
			Example string `json:"example"`
		} `json:"causes"`
	}
	unmarshalData(t, w.Body.Bytes(), &failures)
	if failures.TotalFailed != 1 || len(failures.Causes) != 1 || failures.Causes[0].Example != "OWN_COMPILE_FAILURE" {
		t.Fatalf("scoped failure stats = %+v", failures)
	}
}

func TestBuildEffectivenessIsTenantScoped(t *testing.T) {
	s := mustTenantServer(t)
	now := time.Now().Truncate(time.Second)
	own := seedBuildLog(t, s, db.BuildLog{TenantID: 1, Platform: "windows", Format: "exe", C2URL: "OWN_C2", Status: "success", CreatedAt: now.Add(-2 * time.Hour)})
	seedBuildLog(t, s, db.BuildLog{TenantID: 1, Platform: "linux", Format: "elf", Status: "failed", CreatedAt: now.Add(-time.Hour)})
	seedBuildLog(t, s, db.BuildLog{TenantID: 2, Platform: "windows", Format: "exe", C2URL: "FOREIGN_C2", Status: "success", CreatedAt: now.Add(-2 * time.Hour)})
	for _, agent := range []db.Implant{
		{ID: "build-effectiveness-own", TenantID: 1, Hostname: "OWN_AGENT", Status: "online", CreatedAt: now.Add(-time.Hour)},
		{ID: "build-effectiveness-foreign", TenantID: 2, Hostname: "FOREIGN_AGENT", Status: "online", CreatedAt: now.Add(-time.Hour)},
	} {
		if err := s.db.Create(&agent).Error; err != nil {
			t.Fatalf("seed implant: %v", err)
		}
	}

	c, w := tenantScopedAdminContext(s, t, "build-effectiveness-owner", 1)
	s.handleBuildEffectiveness(c)
	if w.Code != http.StatusOK {
		t.Fatalf("effectiveness = %d (%s)", w.Code, w.Body.String())
	}
	var result struct {
		Builds []struct {
			ID       uint `json:"id"`
			Deployed int  `json:"deployed"`
		} `json:"builds"`
		TotalSuccess int `json:"total_success"`
		TotalFailed  int `json:"total_failed"`
	}
	unmarshalData(t, w.Body.Bytes(), &result)
	if len(result.Builds) != 1 || result.Builds[0].ID != own.ID || result.Builds[0].Deployed != 1 || result.TotalSuccess != 1 || result.TotalFailed != 1 {
		t.Fatalf("scoped effectiveness = %+v", result)
	}
	if strings.Contains(w.Body.String(), "FOREIGN_C2") || strings.Contains(w.Body.String(), "FOREIGN_AGENT") {
		t.Fatalf("effectiveness leaked another tenant: %s", w.Body.String())
	}
}

func TestAsyncBuildLogsUseJobTenant(t *testing.T) {
	s := newBuildTestServer(t)
	cases := []struct {
		filename  string
		build     func() (string, error)
		jobStatus string
		logStatus string
	}{
		{filename: "tenant-success.elf", build: func() (string, error) { return filepath.Join(t.TempDir(), "ok.elf"), nil }, jobStatus: "completed", logStatus: "success"},
		{filename: "tenant-failure.elf", build: func() (string, error) { return "", errors.New("compile exploded") }, jobStatus: "failed", logStatus: "failed"},
		{filename: "tenant-panic.elf", build: func() (string, error) { panic("toolchain exploded") }, jobStatus: "failed", logStatus: "failed"},
	}
	for _, tc := range cases {
		job := s.startBuildJobWithProfile("linux", "elf", "https://c2.example", 1, tc.filename, "", "", "", 2)
		if !s.submitBuild(job, tc.build, "linux", "elf", "https://c2.example", 1, tc.filename) {
			t.Fatalf("queue rejected %s", tc.filename)
		}
		if done := waitForBuildJob(t, s, job.ID, 5*time.Second); done.Status != tc.jobStatus {
			t.Fatalf("%s status = %q, want %q", tc.filename, done.Status, tc.jobStatus)
		}
		var entry db.BuildLog
		if err := s.db.Where("filename = ?", tc.filename).First(&entry).Error; err != nil {
			t.Fatalf("load %s log: %v", tc.filename, err)
		}
		if entry.TenantID != 2 || entry.Status != tc.logStatus {
			t.Fatalf("%s log = %+v, want tenant 2 status %s", tc.filename, entry, tc.logStatus)
		}
	}
}
