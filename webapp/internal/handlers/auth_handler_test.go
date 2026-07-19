package handlers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/catalystcommunity/reactorcide/webapp/internal/config"
	"github.com/catalystcommunity/reactorcide/webapp/internal/uiclient"
	"github.com/catalystcommunity/reactorcide/webapp/internal/uiclient/csilapi"
)

func newTestWebHandler(t *testing.T, fc *fakeCoordinator) *WebHandler {
	t.Helper()
	srv := newTestServer(t, fc)
	return NewWebHandler(NewAPIClient(), uiclient.New(srv.URL))
}

// withAuthMode registers a get-auth-config fakeOp returning the given mode.
func withAuthMode(fc *fakeCoordinator, mode string, bootstrapAvailable, hasGlobalAdmin bool) {
	fc.handle("ReactorcideAuth", "get-auth-config", func(_ []byte, _ string, _ bool) ([]byte, string, bool) {
		resp := csilapi.GetAuthConfigResponse{
			AuthMode:                mode,
			BootstrapAdminAvailable: bootstrapAvailable,
			HasGlobalAdmin:          hasGlobalAdmin,
		}
		return csilapi.EncodeGetAuthConfigResponse(resp), "GetAuthConfigResponse", false
	})
}

// withNoopCapabilities registers a get-capabilities fakeOp returning all-false
// capabilities so tests that don't care about it don't need their own stub.
func withNoopCapabilities(fc *fakeCoordinator) {
	fc.handle("ReactorcideUi", "get-capabilities", func(_ []byte, _ string, _ bool) ([]byte, string, bool) {
		return csilapi.EncodeGetCapabilitiesResponse(csilapi.GetCapabilitiesResponse{}), "GetCapabilitiesResponse", false
	})
}

func TestLoginPage_LoginDisabledInNoneMode(t *testing.T) {
	fc := newFakeCoordinator()
	withAuthMode(fc, "none", false, true)
	withNoopCapabilities(fc)
	h := newTestWebHandler(t, fc)

	req := httptest.NewRequest(http.MethodGet, "/app/login", nil)
	rec := httptest.NewRecorder()
	h.withSession(h.LoginPage)(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Login is disabled") {
		t.Errorf("expected login-disabled notice, got: %s", body)
	}
	if strings.Contains(body, `action="/app/login"`) {
		t.Errorf("login form should not render when auth mode is none, got: %s", body)
	}
	if strings.Contains(body, "Sign in</a>") {
		t.Errorf("nav should not show Sign in link when auth mode is none")
	}
}

func TestLoginPage_FormRendersWhenLoginEnabled(t *testing.T) {
	fc := newFakeCoordinator()
	withAuthMode(fc, "local-rp", false, true)
	withNoopCapabilities(fc)
	h := newTestWebHandler(t, fc)

	req := httptest.NewRequest(http.MethodGet, "/app/login", nil)
	rec := httptest.NewRecorder()
	h.withSession(h.LoginPage)(rec, req)

	body := rec.Body.String()
	if !strings.Contains(body, `action="/app/login"`) {
		t.Errorf("expected a login form, got: %s", body)
	}
}

func TestLoginSubmit_BeginsLoginAndRedirects(t *testing.T) {
	fc := newFakeCoordinator()
	withAuthMode(fc, "local-rp", false, true)
	withNoopCapabilities(fc)
	fc.handle("ReactorcideAuth", "begin-login", func(payload []byte, _ string, _ bool) ([]byte, string, bool) {
		req, err := csilapi.DecodeBeginLoginRequest(payload)
		if err != nil {
			return fakeServiceErrorPayload("bad_request", err.Error()), "ServiceError", true
		}
		if req.IdentityHint == nil || *req.IdentityHint != "user@example.com" {
			return fakeServiceErrorPayload("bad_request", "unexpected identity hint"), "ServiceError", true
		}
		resp := csilapi.BeginLoginResponse{RedirectUrl: "https://idp.example/authorize?attempt=attempt-1", AttemptToken: "attempt-1"}
		return csilapi.EncodeBeginLoginResponse(resp), "BeginLoginResponse", false
	})
	h := newTestWebHandler(t, fc)

	form := strings.NewReader("identity=user%40example.com")
	req := httptest.NewRequest(http.MethodPost, "/app/login", form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.withSession(h.LoginSubmit)(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302; body=%s", rec.Code, rec.Body.String())
	}
	if loc := rec.Header().Get("Location"); loc != "https://idp.example/authorize?attempt=attempt-1" {
		t.Errorf("Location = %q, want the coordinator's redirect_url", loc)
	}
}

func TestLoginSubmit_EmptyIdentityRejectedServerSide(t *testing.T) {
	fc := newFakeCoordinator()
	withAuthMode(fc, "local-rp", false, true)
	withNoopCapabilities(fc)
	beginLoginCalled := false
	fc.handle("ReactorcideAuth", "begin-login", func(payload []byte, _ string, _ bool) ([]byte, string, bool) {
		beginLoginCalled = true
		return csilapi.EncodeBeginLoginResponse(csilapi.BeginLoginResponse{}), "BeginLoginResponse", false
	})
	h := newTestWebHandler(t, fc)

	form := strings.NewReader("identity=")
	req := httptest.NewRequest(http.MethodPost, "/app/login", form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.withSession(h.LoginSubmit)(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302 back to the login form; body=%s", rec.Code, rec.Body.String())
	}
	if loc := rec.Header().Get("Location"); !strings.HasPrefix(loc, "/app/login?") || !strings.Contains(loc, "error=") {
		t.Errorf("Location = %q, want a redirect back to /app/login with an error", loc)
	}
	if beginLoginCalled {
		t.Errorf("begin-login should not be called for an empty identity")
	}
}

func TestAuthCallback_HappyPathSetsCookieAndRedirects(t *testing.T) {
	fc := newFakeCoordinator()
	withAuthMode(fc, "local-rp", false, true)
	withNoopCapabilities(fc)
	expires := time.Now().Add(30 * 24 * time.Hour).UTC().Format(time.RFC3339)
	fc.handle("ReactorcideAuth", "complete-login", func(payload []byte, _ string, _ bool) ([]byte, string, bool) {
		req, err := csilapi.DecodeCompleteLoginRequest(payload)
		if err != nil {
			return fakeServiceErrorPayload("bad_request", err.Error()), "ServiceError", true
		}
		if req.AttemptToken != "attempt-1" || req.EncryptedToken != "enc-abc" {
			return fakeServiceErrorPayload("bad_request", "unexpected attempt/encrypted token"), "ServiceError", true
		}
		resp := csilapi.CompleteLoginResponse{
			SessionToken: "session-token-xyz",
			ExpiresAt:    expires,
			Identity: csilapi.AuthenticatedIdentity{
				UserId:      "user-1",
				Subject:     "user-1@example.com",
				Handle:      "user1",
				Domain:      "example.com",
				DisplayName: "User One",
			},
		}
		return csilapi.EncodeCompleteLoginResponse(resp), "CompleteLoginResponse", false
	})
	h := newTestWebHandler(t, fc)

	req := httptest.NewRequest(http.MethodGet, "/app/auth/callback?attempt=attempt-1&encrypted_token=enc-abc", nil)
	rec := httptest.NewRecorder()
	h.withSession(h.AuthCallback)(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302; body=%s", rec.Code, rec.Body.String())
	}
	if loc := rec.Header().Get("Location"); loc != "/app/" {
		t.Errorf("Location = %q, want /app/", loc)
	}

	cookies := rec.Result().Cookies()
	var sessionCookie *http.Cookie
	for _, c := range cookies {
		if c.Name == sessionCookieName {
			sessionCookie = c
		}
	}
	if sessionCookie == nil {
		t.Fatalf("expected a %s cookie to be set", sessionCookieName)
	}
	if sessionCookie.Value != "session-token-xyz" {
		t.Errorf("cookie value = %q, want the session token", sessionCookie.Value)
	}
	if !sessionCookie.HttpOnly {
		t.Errorf("session cookie must be HttpOnly")
	}
	if sessionCookie.SameSite != http.SameSiteLaxMode {
		t.Errorf("session cookie SameSite = %v, want Lax", sessionCookie.SameSite)
	}
	if sessionCookie.Path != "/" {
		t.Errorf("session cookie Path = %q, want /", sessionCookie.Path)
	}
	if !sessionCookie.Secure {
		t.Errorf("session cookie must be Secure by default (REACTORCIDE_WEB_COOKIE_INSECURE unset)")
	}
}

func TestAuthCallback_MissingParams(t *testing.T) {
	fc := newFakeCoordinator()
	withAuthMode(fc, "local-rp", false, true)
	withNoopCapabilities(fc)
	h := newTestWebHandler(t, fc)

	req := httptest.NewRequest(http.MethodGet, "/app/auth/callback", nil)
	rec := httptest.NewRecorder()
	h.withSession(h.AuthCallback)(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestAuthCallback_ServiceErrorShowsFriendlyPageWithoutTokens(t *testing.T) {
	fc := newFakeCoordinator()
	withAuthMode(fc, "local-rp", false, true)
	withNoopCapabilities(fc)
	fc.handle("ReactorcideAuth", "complete-login", func(_ []byte, _ string, _ bool) ([]byte, string, bool) {
		return fakeServiceErrorPayload("invalid_attempt", "login attempt expired or already used"), "ServiceError", true
	})
	h := newTestWebHandler(t, fc)

	req := httptest.NewRequest(http.MethodGet, "/app/auth/callback?attempt=attempt-1&encrypted_token=enc-secret-value", nil)
	rec := httptest.NewRecorder()
	h.withSession(h.AuthCallback)(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401; body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "login attempt expired or already used") {
		t.Errorf("expected the service error reason in the page, got: %s", body)
	}
	if strings.Contains(body, "enc-secret-value") {
		t.Errorf("error page must not echo the encrypted token, got: %s", body)
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name == sessionCookieName && c.Value != "" {
			t.Errorf("no session cookie should be set on a failed login")
		}
	}
}

func TestLogout_ClearsCookieAndCallsLogoutWithToken(t *testing.T) {
	fc := newFakeCoordinator()
	withAuthMode(fc, "local-rp", false, true)
	withNoopCapabilities(fc)
	fc.handle("ReactorcideAuth", "logout", func(_ []byte, auth string, hasAuth bool) ([]byte, string, bool) {
		if !hasAuth || auth != "session-token-xyz" {
			return fakeServiceErrorPayload("unauthenticated", "missing/wrong token"), "ServiceError", true
		}
		return csilapi.EncodeLogoutResponse(csilapi.LogoutResponse{Ok: true}), "LogoutResponse", false
	})
	h := newTestWebHandler(t, fc)

	req := httptest.NewRequest(http.MethodPost, "/app/logout", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "session-token-xyz"})
	rec := httptest.NewRecorder()
	h.withSession(h.Logout)(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302; body=%s", rec.Code, rec.Body.String())
	}
	if loc := rec.Header().Get("Location"); loc != "/app/" {
		t.Errorf("Location = %q, want /app/", loc)
	}

	call, ok := fc.lastCall()
	if !ok || call.Service != "ReactorcideAuth" || call.Op != "logout" {
		t.Fatalf("expected the fake to have received a logout call, got %+v (ok=%v)", call, ok)
	}
	if !call.HasAuth || call.Auth != "session-token-xyz" {
		t.Errorf("logout call should carry the cookie's token in the envelope auth field, got %+v", call)
	}

	var cleared *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == sessionCookieName {
			cleared = c
		}
	}
	if cleared == nil {
		t.Fatalf("expected a Set-Cookie clearing %s", sessionCookieName)
	}
	if cleared.Value != "" {
		t.Errorf("cleared cookie value = %q, want empty", cleared.Value)
	}
	if cleared.MaxAge >= 0 {
		t.Errorf("cleared cookie MaxAge = %d, want negative (delete)", cleared.MaxAge)
	}
}

func TestCookieInsecureFlag(t *testing.T) {
	old := config.WebCookieInsecure
	config.WebCookieInsecure = true
	defer func() { config.WebCookieInsecure = old }()

	fc := newFakeCoordinator()
	withAuthMode(fc, "local-rp", false, true)
	withNoopCapabilities(fc)
	expires := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	fc.handle("ReactorcideAuth", "complete-login", func(_ []byte, _ string, _ bool) ([]byte, string, bool) {
		resp := csilapi.CompleteLoginResponse{SessionToken: "tok", ExpiresAt: expires}
		return csilapi.EncodeCompleteLoginResponse(resp), "CompleteLoginResponse", false
	})
	h := newTestWebHandler(t, fc)

	req := httptest.NewRequest(http.MethodGet, "/app/auth/callback?attempt=a&encrypted_token=b", nil)
	rec := httptest.NewRecorder()
	h.withSession(h.AuthCallback)(rec, req)

	for _, c := range rec.Result().Cookies() {
		if c.Name == sessionCookieName && c.Secure {
			t.Errorf("cookie must not be Secure when REACTORCIDE_WEB_COOKIE_INSECURE is set")
		}
	}
}

func TestBootstrapPage_UnavailableWhenNotOffered(t *testing.T) {
	fc := newFakeCoordinator()
	withAuthMode(fc, "local-rp", false, true) // bootstrap_admin_available=false
	withNoopCapabilities(fc)
	h := newTestWebHandler(t, fc)

	req := httptest.NewRequest(http.MethodGet, "/app/bootstrap", nil)
	rec := httptest.NewRecorder()
	h.withSession(h.BootstrapPage)(rec, req)

	if !strings.Contains(rec.Body.String(), "Bootstrap is not available") {
		t.Errorf("expected unavailable notice, got: %s", rec.Body.String())
	}
}

func TestBootstrapSubmit_HappyPathSetsCookie(t *testing.T) {
	fc := newFakeCoordinator()
	withAuthMode(fc, "local-rp", true, false)
	withNoopCapabilities(fc)
	var seenToken string
	fc.handle("ReactorcideAuth", "bootstrap-admin", func(payload []byte, _ string, _ bool) ([]byte, string, bool) {
		req, err := csilapi.DecodeBootstrapAdminRequest(payload)
		if err != nil {
			return fakeServiceErrorPayload("bad_request", err.Error()), "ServiceError", true
		}
		seenToken = req.BootstrapToken
		resp := csilapi.BootstrapAdminResponse{SessionToken: "bootstrap-session", ExpiresAt: time.Now().Add(time.Hour).UTC().Format(time.RFC3339)}
		return csilapi.EncodeBootstrapAdminResponse(resp), "BootstrapAdminResponse", false
	})
	h := newTestWebHandler(t, fc)

	form := strings.NewReader("token=super-secret-bootstrap-token")
	req := httptest.NewRequest(http.MethodPost, "/app/bootstrap", form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.withSession(h.BootstrapSubmit)(rec, req)

	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/app/" {
		t.Fatalf("status=%d location=%q, want 302 to /app/; body=%s", rec.Code, rec.Header().Get("Location"), rec.Body.String())
	}
	if seenToken != "super-secret-bootstrap-token" {
		t.Errorf("fake coordinator did not receive the bootstrap token")
	}
	var sessionCookie *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == sessionCookieName {
			sessionCookie = c
		}
	}
	if sessionCookie == nil || sessionCookie.Value != "bootstrap-session" {
		t.Errorf("expected the session cookie to be set from bootstrap-admin's response")
	}
}

func TestBootstrapSubmit_ErrorDoesNotSetCookie(t *testing.T) {
	fc := newFakeCoordinator()
	withAuthMode(fc, "local-rp", true, false)
	withNoopCapabilities(fc)
	fc.handle("ReactorcideAuth", "bootstrap-admin", func(_ []byte, _ string, _ bool) ([]byte, string, bool) {
		return fakeServiceErrorPayload("invalid_token", "bootstrap token is invalid"), "ServiceError", true
	})
	h := newTestWebHandler(t, fc)

	form := strings.NewReader("token=wrong")
	req := httptest.NewRequest(http.MethodPost, "/app/bootstrap", form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.withSession(h.BootstrapSubmit)(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302 back to the bootstrap form", rec.Code)
	}
	loc := rec.Header().Get("Location")
	if !strings.HasPrefix(loc, "/app/bootstrap?") || !strings.Contains(loc, "error=") {
		t.Errorf("Location = %q, want a redirect back to /app/bootstrap with an error", loc)
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name == sessionCookieName && c.Value != "" {
			t.Errorf("no session cookie should be set on a failed bootstrap")
		}
	}
}

func TestSessionInfo_AnonymousHasNoIdentity(t *testing.T) {
	fc := newFakeCoordinator()
	withAuthMode(fc, "local-rp", false, true)
	withNoopCapabilities(fc)
	h := newTestWebHandler(t, fc)

	req := httptest.NewRequest(http.MethodGet, "/app/", nil)
	si := h.sessionInfo(req)

	if si.LoggedIn {
		t.Errorf("anonymous request (no cookie) should not be LoggedIn")
	}
	if !si.LoginEnabled {
		t.Errorf("LoginEnabled should be true when auth mode is local-rp")
	}
	if call, ok := fc.lastCall(); ok && call.Op == "authenticate" {
		t.Errorf("authenticate should not be called when there is no session cookie")
	}
}

func TestSessionInfo_LoggedInResolvesIdentity(t *testing.T) {
	fc := newFakeCoordinator()
	withAuthMode(fc, "local-rp", false, true)
	withNoopCapabilities(fc)
	fc.handle("ReactorcideAuth", "authenticate", func(_ []byte, auth string, hasAuth bool) ([]byte, string, bool) {
		if !hasAuth || auth != "valid-token" {
			resp := csilapi.AuthenticateResponse{Authenticated: false}
			return csilapi.EncodeAuthenticateResponse(resp), "AuthenticateResponse", false
		}
		resp := csilapi.AuthenticateResponse{
			Authenticated: true,
			Identity: &csilapi.AuthenticatedIdentity{
				UserId:      "u-42",
				Subject:     "u-42@example.com",
				DisplayName: "Jane Admin",
			},
		}
		return csilapi.EncodeAuthenticateResponse(resp), "AuthenticateResponse", false
	})
	h := newTestWebHandler(t, fc)

	req := httptest.NewRequest(http.MethodGet, "/app/", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "valid-token"})
	si := h.sessionInfo(req)

	if !si.LoggedIn {
		t.Fatalf("expected LoggedIn=true for a valid session cookie")
	}
	if si.UserID != "u-42" || si.DisplayName != "Jane Admin" {
		t.Errorf("unexpected identity: %+v", si)
	}
}

func TestSessionInfo_NilUIClientsIsAnonymousNoMode(t *testing.T) {
	h := NewWebHandler(NewAPIClient(), nil)
	req := httptest.NewRequest(http.MethodGet, "/app/", nil)
	si := h.sessionInfo(req)

	if si.LoggedIn || si.LoginEnabled {
		t.Errorf("nil uiClients should resolve to a fully anonymous, login-disabled session: %+v", si)
	}
	if si.AuthMode != "none" {
		t.Errorf("AuthMode = %q, want none", si.AuthMode)
	}
}

func TestCapabilitiesForProject_ScopesRequestAndThreadsResult(t *testing.T) {
	fc := newFakeCoordinator()
	withAuthMode(fc, "none", false, true)
	var sawProjectID *string
	fc.handle("ReactorcideUi", "get-capabilities", func(payload []byte, _ string, _ bool) ([]byte, string, bool) {
		req, err := csilapi.DecodeGetCapabilitiesRequest(payload)
		if err != nil {
			return fakeServiceErrorPayload("bad_request", err.Error()), "ServiceError", true
		}
		sawProjectID = req.ProjectId
		resp := csilapi.GetCapabilitiesResponse{CancelJob: true, KillJob: false}
		return csilapi.EncodeGetCapabilitiesResponse(resp), "GetCapabilitiesResponse", false
	})
	h := newTestWebHandler(t, fc)

	projectID := "proj-123"
	req := httptest.NewRequest(http.MethodGet, "/app/jobs/j1", nil)
	caps := h.capabilitiesForProject(req, &projectID)

	if sawProjectID == nil || *sawProjectID != "proj-123" {
		t.Fatalf("expected get-capabilities to receive project_id=proj-123, got %v", sawProjectID)
	}
	if !caps.CancelJob || caps.KillJob {
		t.Errorf("unexpected capabilities threaded through: %+v", caps)
	}
}

func TestGetAuthConfig_CachedWithinTTL(t *testing.T) {
	fc := newFakeCoordinator()
	withAuthMode(fc, "local-rp", false, true)
	withNoopCapabilities(fc)
	h := newTestWebHandler(t, fc)

	req := httptest.NewRequest(http.MethodGet, "/app/", nil)
	h.resolveSession(req)
	h.resolveSession(req)

	if n := fc.callCount("ReactorcideAuth", "get-auth-config"); n != 1 {
		t.Errorf("get-auth-config called %d times within the cache TTL, want 1", n)
	}
}
