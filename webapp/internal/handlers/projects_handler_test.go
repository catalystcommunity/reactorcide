package handlers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/catalystcommunity/reactorcide/webapp/internal/uiclient/csilapi"
)

// withCapabilities registers a static get-capabilities fakeOp: every
// GetCapabilities call (unscoped nav check, project-scoped, org-scoped)
// gets the same caps back. Good enough for capability-gating tests, which
// only care about one field being true/false at a time.
func withCapabilities(fc *fakeCoordinator, caps csilapi.GetCapabilitiesResponse) {
	fc.handle("ReactorcideUi", "get-capabilities", func(_ []byte, _ string, _ bool) ([]byte, string, bool) {
		return csilapi.EncodeGetCapabilitiesResponse(caps), "GetCapabilitiesResponse", false
	})
}

func withListProjects(fc *fakeCoordinator, projects []csilapi.ProjectSummary) {
	fc.handle("ReactorcideUi", "list-projects", func(_ []byte, _ string, _ bool) ([]byte, string, bool) {
		return csilapi.EncodeListProjectsResponse(csilapi.ListProjectsResponse{Projects: projects}), "ListProjectsResponse", false
	})
}

func TestProjectsList_ShowsNewProjectButtonWhenCapable(t *testing.T) {
	fc := newFakeCoordinator()
	withAuthMode(fc, "none", false, true)
	withCapabilities(fc, csilapi.GetCapabilitiesResponse{CreateProject: true})
	withListProjects(fc, []csilapi.ProjectSummary{{ProjectId: "p1", Name: "widget-factory", IsPrivate: false, Enabled: true}})
	h := newTestWebHandler(t, fc)

	req := httptest.NewRequest(http.MethodGet, "/app/projects", nil)
	rec := httptest.NewRecorder()
	h.withSession(h.ProjectsList)(rec, req)

	body := rec.Body.String()
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, body)
	}
	if !strings.Contains(body, "widget-factory") {
		t.Errorf("expected project name in list, got: %s", body)
	}
	if !strings.Contains(body, `href="/app/projects/new"`) {
		t.Errorf("expected New project link for a capable session, got: %s", body)
	}
}

func TestProjectsList_HidesNewProjectButtonWhenIncapable(t *testing.T) {
	fc := newFakeCoordinator()
	withAuthMode(fc, "none", false, true)
	withCapabilities(fc, csilapi.GetCapabilitiesResponse{})
	withListProjects(fc, []csilapi.ProjectSummary{{ProjectId: "p1", Name: "widget-factory"}})
	h := newTestWebHandler(t, fc)

	req := httptest.NewRequest(http.MethodGet, "/app/projects", nil)
	rec := httptest.NewRecorder()
	h.withSession(h.ProjectsList)(rec, req)

	body := rec.Body.String()
	if strings.Contains(body, `href="/app/projects/new"`) {
		t.Errorf("New project link should not render for an incapable session, got: %s", body)
	}
}

func TestProjectNewForm_ForbiddenWithoutCreateProjectCap(t *testing.T) {
	fc := newFakeCoordinator()
	withAuthMode(fc, "none", false, true)
	withCapabilities(fc, csilapi.GetCapabilitiesResponse{CreateProject: false})
	h := newTestWebHandler(t, fc)

	req := httptest.NewRequest(http.MethodGet, "/app/projects/new", nil)
	rec := httptest.NewRecorder()
	h.withSession(h.ProjectNewForm)(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body=%s", rec.Code, rec.Body.String())
	}
}

func TestProjectCreate_HappyPathHitsFakeWithFields(t *testing.T) {
	fc := newFakeCoordinator()
	withAuthMode(fc, "none", false, true)
	withCapabilities(fc, csilapi.GetCapabilitiesResponse{CreateProject: true})
	// A global admin session so the posted org_id (rather than the
	// caller's own org) is honored — see ProjectCreate's "if
	// !si.IsGlobalAdmin { orgID = si.UserID }".
	withAuthenticatedSession(fc, "admin-token", csilapi.AuthenticatedIdentity{UserId: "admin-1", IsGlobalAdmin: true})
	var seen csilapi.CreateProjectRequest
	fc.handle("ReactorcideUi", "create-project", func(payload []byte, _ string, _ bool) ([]byte, string, bool) {
		req, err := csilapi.DecodeCreateProjectRequest(payload)
		if err != nil {
			return fakeServiceErrorPayload("bad_request", err.Error()), "ServiceError", true
		}
		seen = req
		resp := csilapi.CreateProjectResponse{Project: csilapi.ProjectDetail{ProjectId: "new-proj-1", Name: req.Name, OrgId: req.OrgId}}
		return csilapi.EncodeCreateProjectResponse(resp), "CreateProjectResponse", false
	})
	h := newTestWebHandler(t, fc)

	form := strings.NewReader("org_id=org-1&name=widgets&repo_url=https%3A%2F%2Fexample.com%2Fw.git&is_private=on")
	req := httptest.NewRequest(http.MethodPost, "/app/projects", form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "admin-token"})
	rec := httptest.NewRecorder()
	h.withSession(h.ProjectCreate)(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302; body=%s", rec.Code, rec.Body.String())
	}
	if loc := rec.Header().Get("Location"); loc != "/app/projects/new-proj-1?msg=Project+created" {
		t.Errorf("Location = %q, want redirect to the new project with a flash message", loc)
	}
	if seen.OrgId != "org-1" || seen.Name != "widgets" || seen.RepoUrl != "https://example.com/w.git" {
		t.Errorf("fake coordinator did not receive expected fields: %+v", seen)
	}
	if seen.IsPrivate == nil || !*seen.IsPrivate {
		t.Errorf("expected is_private=true to be sent explicitly when the checkbox is checked, got %+v", seen.IsPrivate)
	}
}

func TestProjectCreate_OmitsIsPrivateWhenUnchecked(t *testing.T) {
	fc := newFakeCoordinator()
	withAuthMode(fc, "none", false, true)
	withCapabilities(fc, csilapi.GetCapabilitiesResponse{CreateProject: true})
	withAuthenticatedSession(fc, "admin-token", csilapi.AuthenticatedIdentity{UserId: "admin-1", IsGlobalAdmin: true})
	var seen csilapi.CreateProjectRequest
	fc.handle("ReactorcideUi", "create-project", func(payload []byte, _ string, _ bool) ([]byte, string, bool) {
		req, _ := csilapi.DecodeCreateProjectRequest(payload)
		seen = req
		resp := csilapi.CreateProjectResponse{Project: csilapi.ProjectDetail{ProjectId: "p2"}}
		return csilapi.EncodeCreateProjectResponse(resp), "CreateProjectResponse", false
	})
	h := newTestWebHandler(t, fc)

	form := strings.NewReader("org_id=org-1&name=widgets&repo_url=https%3A%2F%2Fexample.com%2Fw.git")
	req := httptest.NewRequest(http.MethodPost, "/app/projects", form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "admin-token"})
	rec := httptest.NewRecorder()
	h.withSession(h.ProjectCreate)(rec, req)

	if seen.IsPrivate != nil {
		t.Errorf("is_private should be omitted (nil) when the checkbox is unchecked, got %v", *seen.IsPrivate)
	}
}

func TestProjectCreate_InvalidArgumentFlashesMessageOnForm(t *testing.T) {
	fc := newFakeCoordinator()
	withAuthMode(fc, "none", false, true)
	withCapabilities(fc, csilapi.GetCapabilitiesResponse{CreateProject: true})
	withAuthenticatedSession(fc, "admin-token", csilapi.AuthenticatedIdentity{UserId: "admin-1", IsGlobalAdmin: true})
	fc.handle("ReactorcideUi", "create-project", func(_ []byte, _ string, _ bool) ([]byte, string, bool) {
		return fakeServiceErrorPayload("invalid_argument", "repo_url must not be empty"), "ServiceError", true
	})
	h := newTestWebHandler(t, fc)

	form := strings.NewReader("org_id=org-1&name=widgets&repo_url=https%3A%2F%2Fexample.com%2Fw.git")
	req := httptest.NewRequest(http.MethodPost, "/app/projects", form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "admin-token"})
	rec := httptest.NewRecorder()
	h.withSession(h.ProjectCreate)(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302 back to the form; body=%s", rec.Code, rec.Body.String())
	}
	loc := rec.Header().Get("Location")
	if !strings.HasPrefix(loc, "/app/projects/new?") || !strings.Contains(loc, "err=") {
		t.Fatalf("Location = %q, want a redirect back to the new-project form with an err flash", loc)
	}

	// Follow the redirect: the message must render next to the form.
	req2 := httptest.NewRequest(http.MethodGet, loc, nil)
	rec2 := httptest.NewRecorder()
	h.withSession(h.ProjectNewForm)(rec2, req2)
	if !strings.Contains(rec2.Body.String(), "repo_url must not be empty") {
		t.Errorf("expected the invalid_argument message to render on the form, got: %s", rec2.Body.String())
	}
}

func TestProjectDetail_SecurityFormsGatedByCapability(t *testing.T) {
	fc := newFakeCoordinator()
	withAuthMode(fc, "none", false, true)
	fc.handle("ReactorcideUi", "get-project", func(_ []byte, _ string, _ bool) ([]byte, string, bool) {
		resp := csilapi.GetProjectResponse{Project: csilapi.ProjectDetail{ProjectId: "p1", Name: "widgets", OrgId: "org-1"}}
		return csilapi.EncodeGetProjectResponse(resp), "GetProjectResponse", false
	})

	t.Run("incapable session sees no security forms", func(t *testing.T) {
		withCapabilities(fc, csilapi.GetCapabilitiesResponse{})
		h := newTestWebHandler(t, fc)
		req := httptest.NewRequest(http.MethodGet, "/app/projects/p1", nil)
		req.SetPathValue("id", "p1")
		rec := httptest.NewRecorder()
		h.withSession(h.ProjectDetail)(rec, req)

		body := rec.Body.String()
		if strings.Contains(body, `action="/app/projects/p1/webhook-secrets"`) {
			t.Errorf("webhook secret form should not render without ManageWebhookSecrets, got: %s", body)
		}
		if strings.Contains(body, `action="/app/projects/p1/settings"`) {
			t.Errorf("settings form should not render without ManageProjectSettings, got: %s", body)
		}
		if strings.Contains(body, `action="/app/projects/p1/delete"`) {
			t.Errorf("delete form should not render without DeleteProject, got: %s", body)
		}
	})

	t.Run("capable session sees security forms with empty secret inputs", func(t *testing.T) {
		withCapabilities(fc, csilapi.GetCapabilitiesResponse{
			ManageWebhookSecrets: true, ManageVcsCredentials: true, ManageProjectSettings: true, DeleteProject: true,
		})
		fc.handle("ReactorcideUi", "list-webhook-secrets", func(_ []byte, _ string, _ bool) ([]byte, string, bool) {
			return csilapi.EncodeListWebhookSecretsResponse(csilapi.ListWebhookSecretsResponse{}), "ListWebhookSecretsResponse", false
		})
		fc.handle("ReactorcideUi", "list-vcs-credentials", func(_ []byte, _ string, _ bool) ([]byte, string, bool) {
			return csilapi.EncodeListVcsCredentialsResponse(csilapi.ListVcsCredentialsResponse{}), "ListVcsCredentialsResponse", false
		})
		h := newTestWebHandler(t, fc)
		req := httptest.NewRequest(http.MethodGet, "/app/projects/p1", nil)
		req.SetPathValue("id", "p1")
		rec := httptest.NewRecorder()
		h.withSession(h.ProjectDetail)(rec, req)

		body := rec.Body.String()
		if !strings.Contains(body, `action="/app/projects/p1/webhook-secrets"`) {
			t.Errorf("expected webhook secret form to render, got: %s", body)
		}
		if !strings.Contains(body, `action="/app/projects/p1/settings"`) {
			t.Errorf("expected settings form to render, got: %s", body)
		}
		if !strings.Contains(body, `action="/app/projects/p1/delete"`) {
			t.Errorf("expected delete form to render, got: %s", body)
		}
		if !strings.Contains(body, `<input type="password" name="value" required autocomplete="new-password">`) {
			t.Errorf("expected an empty (unfilled) password field for the secret value, got: %s", body)
		}
	})
}

func TestWebhookSecretAdd_HappyPathValueNeverEchoed(t *testing.T) {
	fc := newFakeCoordinator()
	withAuthMode(fc, "none", false, true)
	const secretValue = "super-secret-webhook-value-xyz"
	var seenValue string
	fc.handle("ReactorcideUi", "add-webhook-secret", func(payload []byte, _ string, _ bool) ([]byte, string, bool) {
		req, err := csilapi.DecodeAddWebhookSecretRequest(payload)
		if err != nil {
			return fakeServiceErrorPayload("bad_request", err.Error()), "ServiceError", true
		}
		seenValue = req.Value
		resp := csilapi.AddWebhookSecretResponse{Secret: csilapi.WebhookSecretSummary{Id: "wh-1", ProjectId: req.ProjectId, Provider: req.Provider, Name: req.Name, IsActive: true}}
		return csilapi.EncodeAddWebhookSecretResponse(resp), "AddWebhookSecretResponse", false
	})
	h := newTestWebHandler(t, fc)

	form := strings.NewReader("provider=github&name=main&value=" + secretValue)
	req := httptest.NewRequest(http.MethodPost, "/app/projects/p1/webhook-secrets", form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetPathValue("id", "p1")
	rec := httptest.NewRecorder()
	h.withSession(h.WebhookSecretAdd)(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302; body=%s", rec.Code, rec.Body.String())
	}
	if seenValue != secretValue {
		t.Errorf("fake coordinator did not receive the secret value")
	}
	if strings.Contains(rec.Header().Get("Location"), secretValue) {
		t.Errorf("secret value must not be echoed into the redirect Location")
	}
	if strings.Contains(rec.Body.String(), secretValue) {
		t.Errorf("secret value must not appear in the response body")
	}
}

func TestVcsCredentialDelete_HappyPathHitsFakeWithFields(t *testing.T) {
	fc := newFakeCoordinator()
	withAuthMode(fc, "none", false, true)
	var seen csilapi.DeleteVcsCredentialRequest
	fc.handle("ReactorcideUi", "delete-vcs-credential", func(payload []byte, _ string, _ bool) ([]byte, string, bool) {
		req, err := csilapi.DecodeDeleteVcsCredentialRequest(payload)
		if err != nil {
			return fakeServiceErrorPayload("bad_request", err.Error()), "ServiceError", true
		}
		seen = req
		return csilapi.EncodeDeleteVcsCredentialResponse(csilapi.DeleteVcsCredentialResponse{Deleted: true}), "DeleteVcsCredentialResponse", false
	})
	h := newTestWebHandler(t, fc)

	req := httptest.NewRequest(http.MethodPost, "/app/projects/p1/vcs-credentials/vc-1/delete", nil)
	req.SetPathValue("id", "p1")
	req.SetPathValue("sid", "vc-1")
	rec := httptest.NewRecorder()
	h.withSession(h.VcsCredentialDelete)(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302; body=%s", rec.Code, rec.Body.String())
	}
	if seen.Id != "vc-1" {
		t.Errorf("fake coordinator did not receive expected id: %+v", seen)
	}
	if loc := rec.Header().Get("Location"); loc != "/app/projects/p1?msg=VCS+credential+deleted" {
		t.Errorf("Location = %q, want redirect to the project page with a flash", loc)
	}
}

func TestProjectDetail_RendersVcsCredentialDeleteButton(t *testing.T) {
	fc := newFakeCoordinator()
	withAuthMode(fc, "none", false, true)
	withCapabilities(fc, csilapi.GetCapabilitiesResponse{ManageVcsCredentials: true})
	fc.handle("ReactorcideUi", "get-project", func(_ []byte, _ string, _ bool) ([]byte, string, bool) {
		resp := csilapi.GetProjectResponse{Project: csilapi.ProjectDetail{ProjectId: "p1", Name: "widgets", OrgId: "org-1"}}
		return csilapi.EncodeGetProjectResponse(resp), "GetProjectResponse", false
	})
	fc.handle("ReactorcideUi", "list-vcs-credentials", func(_ []byte, _ string, _ bool) ([]byte, string, bool) {
		resp := csilapi.ListVcsCredentialsResponse{Credentials: []csilapi.VcsCredentialSummary{
			{Id: "vc-1", ProjectId: "p1", Provider: "github", Name: "primary", IsActive: true},
		}}
		return csilapi.EncodeListVcsCredentialsResponse(resp), "ListVcsCredentialsResponse", false
	})
	h := newTestWebHandler(t, fc)

	req := httptest.NewRequest(http.MethodGet, "/app/projects/p1", nil)
	req.SetPathValue("id", "p1")
	rec := httptest.NewRecorder()
	h.withSession(h.ProjectDetail)(rec, req)

	body := rec.Body.String()
	if !strings.Contains(body, `action="/app/projects/p1/vcs-credentials/vc-1/delete"`) {
		t.Errorf("expected a vcs credential delete form, got: %s", body)
	}
}

func TestProjectDelete_NameMismatchDoesNotCallDelete(t *testing.T) {
	fc := newFakeCoordinator()
	withAuthMode(fc, "none", false, true)
	fc.handle("ReactorcideUi", "delete-project", func(_ []byte, _ string, _ bool) ([]byte, string, bool) {
		return csilapi.EncodeDeleteProjectResponse(csilapi.DeleteProjectResponse{Deleted: true}), "DeleteProjectResponse", false
	})
	h := newTestWebHandler(t, fc)

	form := strings.NewReader("expected_name=widgets&confirm_name=wrong-name")
	req := httptest.NewRequest(http.MethodPost, "/app/projects/p1/delete", form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetPathValue("id", "p1")
	rec := httptest.NewRecorder()
	h.withSession(h.ProjectDelete)(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302 back to the project page; body=%s", rec.Code, rec.Body.String())
	}
	if n := fc.callCount("ReactorcideUi", "delete-project"); n != 0 {
		t.Errorf("delete-project should not be called on a name mismatch, called %d times", n)
	}
	if loc := rec.Header().Get("Location"); !strings.Contains(loc, "err=") {
		t.Errorf("Location = %q, want an err flash explaining the mismatch", loc)
	}
}

func TestProjectDelete_HappyPath(t *testing.T) {
	fc := newFakeCoordinator()
	withAuthMode(fc, "none", false, true)
	fc.handle("ReactorcideUi", "delete-project", func(_ []byte, _ string, _ bool) ([]byte, string, bool) {
		return csilapi.EncodeDeleteProjectResponse(csilapi.DeleteProjectResponse{Deleted: true}), "DeleteProjectResponse", false
	})
	h := newTestWebHandler(t, fc)

	form := strings.NewReader("expected_name=widgets&confirm_name=widgets")
	req := httptest.NewRequest(http.MethodPost, "/app/projects/p1/delete", form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetPathValue("id", "p1")
	rec := httptest.NewRecorder()
	h.withSession(h.ProjectDelete)(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302; body=%s", rec.Code, rec.Body.String())
	}
	if loc := rec.Header().Get("Location"); loc != "/app/projects?msg=Project+deleted" {
		t.Errorf("Location = %q, want redirect to the projects list with a flash", loc)
	}
	if n := fc.callCount("ReactorcideUi", "delete-project"); n != 1 {
		t.Errorf("expected delete-project to be called exactly once, got %d", n)
	}
}

func TestProjectDelete_ForbiddenRendersErrorPage(t *testing.T) {
	fc := newFakeCoordinator()
	withAuthMode(fc, "none", false, true)
	fc.handle("ReactorcideUi", "delete-project", func(_ []byte, _ string, _ bool) ([]byte, string, bool) {
		return fakeServiceErrorPayload("forbidden", "you do not have permission to delete this project"), "ServiceError", true
	})
	h := newTestWebHandler(t, fc)

	form := strings.NewReader("expected_name=widgets&confirm_name=widgets")
	req := httptest.NewRequest(http.MethodPost, "/app/projects/p1/delete", form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetPathValue("id", "p1")
	rec := httptest.NewRecorder()
	h.withSession(h.ProjectDelete)(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body=%s", rec.Code, rec.Body.String())
	}
}
