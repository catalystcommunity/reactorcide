package handlers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/catalystcommunity/reactorcide/webapp/internal/uiclient/csilapi"
)

// withAuthenticatedSession registers authenticate to resolve the given
// cookie token to an identity, and returns a request carrying that cookie so
// handlers gated on si.IsGlobalAdmin (not a capability call) can be tested.
func withAuthenticatedSession(fc *fakeCoordinator, token string, identity csilapi.AuthenticatedIdentity) {
	fc.handle("ReactorcideAuth", "authenticate", func(_ []byte, auth string, hasAuth bool) ([]byte, string, bool) {
		if !hasAuth || auth != token {
			return csilapi.EncodeAuthenticateResponse(csilapi.AuthenticateResponse{Authenticated: false}), "AuthenticateResponse", false
		}
		resp := csilapi.AuthenticateResponse{Authenticated: true, Identity: &identity}
		return csilapi.EncodeAuthenticateResponse(resp), "AuthenticateResponse", false
	})
}

func TestAdminPage_ForbiddenForNonGlobalAdmin(t *testing.T) {
	fc := newFakeCoordinator()
	withAuthMode(fc, "local-rp", false, true)
	withCapabilities(fc, csilapi.GetCapabilitiesResponse{})
	h := newTestWebHandler(t, fc)

	req := httptest.NewRequest(http.MethodGet, "/app/admin", nil)
	rec := httptest.NewRecorder()
	h.withSession(h.AdminPage)(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body=%s", rec.Code, rec.Body.String())
	}
}

func TestAdminPage_RendersForGlobalAdmin(t *testing.T) {
	fc := newFakeCoordinator()
	withAuthMode(fc, "local-rp", false, true)
	withCapabilities(fc, csilapi.GetCapabilitiesResponse{IsGlobalAdmin: true, ManageGlobalSettings: true})
	withAuthenticatedSession(fc, "admin-token", csilapi.AuthenticatedIdentity{UserId: "u1", DisplayName: "Admin", IsGlobalAdmin: true})
	fc.handle("ReactorcideUi", "get-global-settings", func(_ []byte, _ string, _ bool) ([]byte, string, bool) {
		resp := csilapi.GetGlobalSettingsResponse{NewProjectsPrivate: true}
		return csilapi.EncodeGetGlobalSettingsResponse(resp), "GetGlobalSettingsResponse", false
	})
	fc.handle("ReactorcideUi", "list-trusted-identities", func(_ []byte, _ string, _ bool) ([]byte, string, bool) {
		resp := csilapi.ListTrustedIdentitiesResponse{Identities: []csilapi.TrustedIdentity{{Domain: "example.com", Source: "admin"}}}
		return csilapi.EncodeListTrustedIdentitiesResponse(resp), "ListTrustedIdentitiesResponse", false
	})
	fc.handle("ReactorcideUi", "list-trusted-domain-patterns", func(_ []byte, _ string, _ bool) ([]byte, string, bool) {
		resp := csilapi.ListTrustedDomainPatternsResponse{Patterns: []csilapi.TrustedDomainPattern{{PatternId: "pat1", Pattern: "^.*\\.example\\.com$"}}}
		return csilapi.EncodeListTrustedDomainPatternsResponse(resp), "ListTrustedDomainPatternsResponse", false
	})
	h := newTestWebHandler(t, fc)

	req := httptest.NewRequest(http.MethodGet, "/app/admin", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "admin-token"})
	rec := httptest.NewRecorder()
	h.withSession(h.AdminPage)(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "example.com") {
		t.Errorf("expected trusted identity domain, got: %s", body)
	}
	if !strings.Contains(body, "^.*\\.example\\.com$") {
		t.Errorf("expected trusted domain pattern, got: %s", body)
	}
	if !strings.Contains(body, `action="/app/admin/global-settings"`) {
		t.Errorf("expected global settings form, got: %s", body)
	}
}

func TestTrustedDomainPatternAdd_HappyPathHitsFakeWithFields(t *testing.T) {
	fc := newFakeCoordinator()
	withAuthMode(fc, "local-rp", false, true)
	var seen csilapi.AddTrustedDomainPatternRequest
	fc.handle("ReactorcideUi", "add-trusted-domain-pattern", func(payload []byte, _ string, _ bool) ([]byte, string, bool) {
		req, err := csilapi.DecodeAddTrustedDomainPatternRequest(payload)
		if err != nil {
			return fakeServiceErrorPayload("bad_request", err.Error()), "ServiceError", true
		}
		seen = req
		resp := csilapi.AddTrustedDomainPatternResponse{Pattern: csilapi.TrustedDomainPattern{PatternId: "pat2", Pattern: req.Pattern}}
		return csilapi.EncodeAddTrustedDomainPatternResponse(resp), "AddTrustedDomainPatternResponse", false
	})
	h := newTestWebHandler(t, fc)

	form := strings.NewReader("pattern=" + strings.ReplaceAll("^.*\\.corp\\.example\\.com$", "\\", "%5C"))
	req := httptest.NewRequest(http.MethodPost, "/app/admin/trusted-domain-patterns", form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.withSession(h.TrustedDomainPatternAdd)(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302; body=%s", rec.Code, rec.Body.String())
	}
	if seen.Pattern != "^.*\\.corp\\.example\\.com$" {
		t.Errorf("fake coordinator did not receive the expected pattern, got %q", seen.Pattern)
	}
}

func TestTrustedDomainPatternAdd_InvalidArgumentFlashesMessage(t *testing.T) {
	fc := newFakeCoordinator()
	withAuthMode(fc, "local-rp", false, true)
	fc.handle("ReactorcideUi", "add-trusted-domain-pattern", func(_ []byte, _ string, _ bool) ([]byte, string, bool) {
		return fakeServiceErrorPayload("invalid_argument", "pattern does not compile"), "ServiceError", true
	})
	h := newTestWebHandler(t, fc)

	form := strings.NewReader("pattern=%28unbalanced")
	req := httptest.NewRequest(http.MethodPost, "/app/admin/trusted-domain-patterns", form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.withSession(h.TrustedDomainPatternAdd)(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302; body=%s", rec.Code, rec.Body.String())
	}
	loc := rec.Header().Get("Location")
	if !strings.Contains(loc, "err=") {
		t.Errorf("Location = %q, want an err flash", loc)
	}

	req2 := httptest.NewRequest(http.MethodGet, loc, nil)
	req2.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "admin-token"})
	rec2 := httptest.NewRecorder()
	withCapabilities(fc, csilapi.GetCapabilitiesResponse{IsGlobalAdmin: true})
	withAuthenticatedSession(fc, "admin-token", csilapi.AuthenticatedIdentity{UserId: "u1", IsGlobalAdmin: true})
	fc.handle("ReactorcideUi", "get-global-settings", func(_ []byte, _ string, _ bool) ([]byte, string, bool) {
		return csilapi.EncodeGetGlobalSettingsResponse(csilapi.GetGlobalSettingsResponse{}), "GetGlobalSettingsResponse", false
	})
	fc.handle("ReactorcideUi", "list-trusted-identities", func(_ []byte, _ string, _ bool) ([]byte, string, bool) {
		return csilapi.EncodeListTrustedIdentitiesResponse(csilapi.ListTrustedIdentitiesResponse{}), "ListTrustedIdentitiesResponse", false
	})
	fc.handle("ReactorcideUi", "list-trusted-domain-patterns", func(_ []byte, _ string, _ bool) ([]byte, string, bool) {
		return csilapi.EncodeListTrustedDomainPatternsResponse(csilapi.ListTrustedDomainPatternsResponse{}), "ListTrustedDomainPatternsResponse", false
	})
	h.withSession(h.AdminPage)(rec2, req2)
	if !strings.Contains(rec2.Body.String(), "pattern does not compile") {
		t.Errorf("expected the invalid_argument message to render on the admin page, got: %s", rec2.Body.String())
	}
}
