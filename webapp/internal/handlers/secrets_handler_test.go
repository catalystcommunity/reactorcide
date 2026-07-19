package handlers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/catalystcommunity/reactorcide/webapp/internal/uiclient/csilapi"
)

func TestOrgSecretsPage_ForbiddenWithoutManageSecretsCap(t *testing.T) {
	fc := newFakeCoordinator()
	withAuthMode(fc, "none", false, true)
	withCapabilities(fc, csilapi.GetCapabilitiesResponse{ManageSecrets: false})
	h := newTestWebHandler(t, fc)

	req := httptest.NewRequest(http.MethodGet, "/app/org/secrets?org_id=org-1", nil)
	rec := httptest.NewRecorder()
	h.withSession(h.OrgSecretsPage)(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body=%s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), `action="/app/org/secrets"`) {
		t.Errorf("set-secret form must not render for an incapable session, got: %s", rec.Body.String())
	}
}

func TestOrgSecretsPage_RendersPathsAndGrantsForCapableSession(t *testing.T) {
	fc := newFakeCoordinator()
	withAuthMode(fc, "none", false, true)
	withCapabilities(fc, csilapi.GetCapabilitiesResponse{ManageSecrets: true})
	fc.handle("ReactorcideUi", "list-secret-paths", func(_ []byte, _ string, _ bool) ([]byte, string, bool) {
		resp := csilapi.ListSecretPathsResponse{Paths: []csilapi.SecretPathEntry{{Path: "ci/deploy", Keys: []string{"api_token"}}}}
		return csilapi.EncodeListSecretPathsResponse(resp), "ListSecretPathsResponse", false
	})
	fc.handle("ReactorcideUi", "list-secret-grants", func(_ []byte, _ string, _ bool) ([]byte, string, bool) {
		resp := csilapi.ListSecretGrantsResponse{Grants: []csilapi.SecretGrant{{GrantId: "g1", OrgId: "org-1", Name: "deploy-grant", SecretPathMatch: "prefix", SecretPathPattern: "ci/", JobNameMatch: "any"}}}
		return csilapi.EncodeListSecretGrantsResponse(resp), "ListSecretGrantsResponse", false
	})
	fc.handle("ReactorcideUi", "list-projects", func(_ []byte, _ string, _ bool) ([]byte, string, bool) {
		return csilapi.EncodeListProjectsResponse(csilapi.ListProjectsResponse{}), "ListProjectsResponse", false
	})
	h := newTestWebHandler(t, fc)

	req := httptest.NewRequest(http.MethodGet, "/app/org/secrets?org_id=org-1", nil)
	rec := httptest.NewRecorder()
	h.withSession(h.OrgSecretsPage)(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "ci/deploy") || !strings.Contains(body, "api_token") {
		t.Errorf("expected secret path/key in the page, got: %s", body)
	}
	if !strings.Contains(body, "deploy-grant") {
		t.Errorf("expected secret grant name in the page, got: %s", body)
	}
	if !strings.Contains(body, `action="/app/org/secrets"`) {
		t.Errorf("expected the set-secret form to render, got: %s", body)
	}
	if !strings.Contains(body, `<input type="password" name="value" required autocomplete="new-password">`) {
		t.Errorf("expected an empty (unfilled) password field for the secret value, got: %s", body)
	}
}

func TestSecretSet_HappyPathHitsFakeAndValueNeverEchoed(t *testing.T) {
	fc := newFakeCoordinator()
	withAuthMode(fc, "none", false, true)
	const secretValue = "sk-live-do-not-echo-this"
	var seen csilapi.SetSecretRequest
	fc.handle("ReactorcideUi", "set-secret", func(payload []byte, _ string, _ bool) ([]byte, string, bool) {
		req, err := csilapi.DecodeSetSecretRequest(payload)
		if err != nil {
			return fakeServiceErrorPayload("bad_request", err.Error()), "ServiceError", true
		}
		seen = req
		return csilapi.EncodeSetSecretResponse(csilapi.SetSecretResponse{Ok: true}), "SetSecretResponse", false
	})
	h := newTestWebHandler(t, fc)

	form := strings.NewReader("org_id=org-1&path=ci%2Fdeploy&key=api_token&value=" + secretValue)
	req := httptest.NewRequest(http.MethodPost, "/app/org/secrets", form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.withSession(h.SecretSet)(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302; body=%s", rec.Code, rec.Body.String())
	}
	if seen.OrgId != "org-1" || seen.Path != "ci/deploy" || seen.Key != "api_token" || seen.Value != secretValue {
		t.Errorf("fake coordinator did not receive expected fields: %+v", seen)
	}
	loc := rec.Header().Get("Location")
	if strings.Contains(loc, secretValue) {
		t.Errorf("secret value must not be echoed into the redirect Location, got: %s", loc)
	}
	if strings.Contains(rec.Body.String(), secretValue) {
		t.Errorf("secret value must not appear in the response body")
	}
}

func TestSecretDelete_InvalidArgumentFlashesMessage(t *testing.T) {
	fc := newFakeCoordinator()
	withAuthMode(fc, "none", false, true)
	fc.handle("ReactorcideUi", "delete-secret", func(_ []byte, _ string, _ bool) ([]byte, string, bool) {
		return fakeServiceErrorPayload("invalid_argument", "path is required"), "ServiceError", true
	})
	h := newTestWebHandler(t, fc)

	form := strings.NewReader("org_id=org-1&path=ci%2Fdeploy&key=api_token")
	req := httptest.NewRequest(http.MethodPost, "/app/org/secrets/delete", form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.withSession(h.SecretDelete)(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302; body=%s", rec.Code, rec.Body.String())
	}
	loc := rec.Header().Get("Location")
	if !strings.Contains(loc, "err=path+is+required") {
		t.Errorf("Location = %q, want an err flash with the invalid_argument message", loc)
	}
}
