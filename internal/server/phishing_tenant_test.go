package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/r0si/sovereign/internal/db"
	"github.com/r0si/sovereign/internal/server/middleware"
)

// TestPhishingAssetsAreTenantScoped pins the scope on the phishing surface, which is the one
// where an unscoped row is not just visible but RE-TARGETABLE: the template holds the landing
// page that captures credentials, and the campaign holds the SMTP settings and the target list.
// Both tables carry a creator, so ownership runs through it.
func TestPhishingAssetsAreTenantScoped(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := mustTenantServer(t)

	if err := s.db.Create(&db.User{Username: "tenant1-admin", Role: db.RoleAdmin, TenantID: 1, IsActive: true}).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.db.Create(&db.PhishingTemplate{Name: "victim-tpl", Subject: "sekrit-subject", Body: "<p>sekrit-page</p>", CreatedBy: "tenant1-admin"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.db.Create(&db.PhishingCampaign{Name: "victim-camp", TargetList: "sekrit-target", SMTPHost: "sekrit.example", CreatedBy: "tenant1-admin"}).Error; err != nil {
		t.Fatal(err)
	}
	var tpl db.PhishingTemplate
	if err := s.db.Where("name = ?", "victim-tpl").First(&tpl).Error; err != nil {
		t.Fatal(err)
	}
	var camp db.PhishingCampaign
	if err := s.db.Where("name = ?", "victim-camp").First(&camp).Error; err != nil {
		t.Fatal(err)
	}

	ctxFor := func(method, target string, body []byte) (*gin.Context, *httptest.ResponseRecorder) {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		var r *http.Request
		if body != nil {
			r = httptest.NewRequest(method, target, bytes.NewReader(body))
		} else {
			r = httptest.NewRequest(method, target, nil)
		}
		r.Header.Set("Content-Type", "application/json")
		c.Request = r
		c.Set("user", "tenant2-admin")
		c.Set("user_role", db.RoleAdmin)
		c.Set("user_id", uint(9))
		c.Set(middleware.TenantIDContextKey, uint(2))
		return c, w
	}

	// Lists do not expose either row.
	c, w := ctxFor(http.MethodGet, "/api/phishing/templates", nil)
	s.handleAPIPhishingTemplates(c)
	if bytes.Contains(w.Body.Bytes(), []byte("sekrit-subject")) || bytes.Contains(w.Body.Bytes(), []byte("sekrit-page")) {
		t.Fatalf("tenant 2 listed tenant 1's template: %s", w.Body.String())
	}
	c, w = ctxFor(http.MethodGet, "/api/phishing/campaigns", nil)
	s.handleAPIPhishingCampaigns(c)
	if bytes.Contains(w.Body.Bytes(), []byte("sekrit-target")) {
		t.Fatalf("tenant 2 listed tenant 1's campaign: %s", w.Body.String())
	}

	// Update-by-id does not reach the template.
	patch, _ := json.Marshal(map[string]string{"subject": "attacker-subject", "html": "<p>attacker</p>"})
	// Only the DB is asserted below, so the recorder is discarded (`_`).
	c, _ = ctxFor(http.MethodPut, "/api/phishing/templates/"+uintString(tpl.ID), patch)
	c.Params = gin.Params{{Key: "id", Value: uintString(tpl.ID)}}
	s.handleAPIUpdatePhishingTemplate(c)
	var after db.PhishingTemplate
	if err := s.db.First(&after, tpl.ID).Error; err != nil {
		t.Fatal(err)
	}
	if bytes.Contains([]byte(after.Body), []byte("attacker")) {
		t.Fatalf("tenant 2 rewrote tenant 1's landing page: %q", after.Body)
	}

	// Delete-by-id does not remove it, and launching it is refused.
	c, _ = ctxFor(http.MethodDelete, "/api/phishing/templates/"+uintString(tpl.ID), nil)
	c.Params = gin.Params{{Key: "id", Value: uintString(tpl.ID)}}
	s.handleAPIDeletePhishingTemplate(c)
	var tplRemaining int64
	if err := s.db.Model(&db.PhishingTemplate{}).Where("id = ?", tpl.ID).Count(&tplRemaining).Error; err != nil {
		t.Fatal(err)
	}
	if tplRemaining != 1 {
		t.Fatal("tenant 2 deleted tenant 1's template")
	}

	c, w = ctxFor(http.MethodPost, "/api/phishing/campaigns/"+uintString(camp.ID)+"/launch", nil)
	c.Params = gin.Params{{Key: "id", Value: uintString(camp.ID)}}
	s.handleAPILaunchPhishingCampaign(c)
	if w.Code != http.StatusNotFound {
		t.Fatalf("tenant 2 launched tenant 1's campaign: %d (%s)", w.Code, w.Body.String())
	}
}

// TestPhishingCapturesAndTemplateRefsAreTenantScoped proves captures inherit
// campaign ownership and both campaign creation and launch reject a template
// owned by another tenant before persisting or starting any work.
func TestPhishingCapturesAndTemplateRefsAreTenantScoped(t *testing.T) {
	s := newPhishingTestServer(t)
	seedPhishingTenantUser(t, s, "capture-viewer", 1)
	seedPhishingTenantUser(t, s, "capture-outsider", 2)

	ownTemplate := db.PhishingTemplate{Name: "own-template", CreatedBy: "capture-viewer"}
	foreignTemplate := db.PhishingTemplate{Name: "foreign-template", CreatedBy: "capture-outsider"}
	for _, row := range []any{&ownTemplate, &foreignTemplate} {
		if err := s.db.Create(row).Error; err != nil {
			t.Fatalf("seed phishing template: %v", err)
		}
	}
	ownCampaign := db.PhishingCampaign{Name: "own-campaign", TemplateID: ownTemplate.ID, CreatedBy: "capture-viewer"}
	foreignCampaign := db.PhishingCampaign{Name: "foreign-campaign", TemplateID: foreignTemplate.ID, CreatedBy: "capture-outsider"}
	for _, row := range []any{&ownCampaign, &foreignCampaign} {
		if err := s.db.Create(row).Error; err != nil {
			t.Fatalf("seed phishing campaign: %v", err)
		}
	}
	for _, event := range []db.PhishingEvent{
		{CampaignID: ownCampaign.ID, EventType: "capture", Email: "own@example.test", Payload: `{"username":"own-user","password":"OWN-CAPTURE-SECRET"}`},
		{CampaignID: foreignCampaign.ID, EventType: "capture", Email: "foreign@example.test", Payload: `{"username":"foreign-user","password":"FOREIGN-CAPTURE-SECRET"}`},
	} {
		if err := s.db.Create(&event).Error; err != nil {
			t.Fatalf("seed capture: %v", err)
		}
	}

	c, w := tenantScopedAdminContext(s, t, "capture-viewer", 1)
	s.handleAPIPhishingCaptures(c)
	if w.Code != http.StatusOK {
		t.Fatalf("captures status=%d body=%s", w.Code, w.Body.String())
	}
	var captures []struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	unmarshalData(t, w.Body.Bytes(), &captures)
	if len(captures) != 1 || captures[0].Username != "own-user" || captures[0].Password != "OWN-CAPTURE-SECRET" {
		t.Fatalf("tenant captures = %+v", captures)
	}
	if bytes.Contains(w.Body.Bytes(), []byte("FOREIGN-CAPTURE-SECRET")) {
		t.Fatalf("foreign capture leaked: %s", w.Body.String())
	}

	body, _ := json.Marshal(map[string]any{"name": "cross-template", "template_id": foreignTemplate.ID})
	c, w = tenantScopedAdminContext(s, t, "capture-viewer", 1)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/phishing/campaigns", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	s.handleAPICreatePhishingCampaign(c)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("foreign template create status=%d body=%s", w.Code, w.Body.String())
	}
	var count int64
	if err := s.db.Model(&db.PhishingCampaign{}).Where("name = ?", "cross-template").Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("campaign referencing a foreign template was persisted")
	}

	// Legacy rows can already contain a bad reference. Launch must apply the
	// same scoped template lookup and leave the campaign in draft.
	foreignRef := db.PhishingCampaign{
		Name: "legacy-foreign-ref", TemplateID: foreignTemplate.ID, Status: "draft",
		TargetList: "target@example.test", SMTPHost: "127.0.0.1", CreatedBy: "capture-viewer",
	}
	if err := s.db.Create(&foreignRef).Error; err != nil {
		t.Fatal(err)
	}
	c, w = tenantScopedAdminContext(s, t, "capture-viewer", 1)
	c.Params = gin.Params{{Key: "id", Value: uintString(foreignRef.ID)}}
	s.handleAPILaunchPhishingCampaign(c)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("foreign template launch status=%d body=%s", w.Code, w.Body.String())
	}
	var after db.PhishingCampaign
	if err := s.db.First(&after, foreignRef.ID).Error; err != nil {
		t.Fatal(err)
	}
	if after.Status != "draft" {
		t.Fatalf("foreign template launch changed status to %q", after.Status)
	}
}

func TestPhishingCapturesQueryFailureIs500(t *testing.T) {
	s := closedDBServer(t)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/phishing/captures", nil)
	c.Set(tenantIDContextKey, tenantResolution{tid: 1, ok: true})
	s.handleAPIPhishingCaptures(c)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("captures query failure status=%d body=%s", w.Code, w.Body.String())
	}
}
