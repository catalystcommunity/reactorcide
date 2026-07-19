package handlers

import (
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/catalystcommunity/reactorcide/webapp/internal/uiclient"
	"github.com/catalystcommunity/reactorcide/webapp/internal/uiclient/csilapi"
	"github.com/sirupsen/logrus"
)

// defaultSessionTTL is the cookie lifetime used when a CompleteLogin/
// BootstrapAdmin response's expires_at can't be parsed. It mirrors the
// 30-day session expiry documented in UI_AUTH_PLAN.md so a parse failure
// degrades to the same lifetime the coordinator actually enforces server
// side, not something shorter or longer.
const defaultSessionTTL = 30 * 24 * time.Hour

// LoginPage renders GET /app/login: a login-disabled notice when the
// coordinator's auth mode is "none", otherwise an identity-selector form
// that posts to POST /app/login.
func (h *WebHandler) LoginPage(w http.ResponseWriter, r *http.Request) {
	si := h.sessionInfo(r)
	if si.LoggedIn {
		http.Redirect(w, r, "/app/", http.StatusFound)
		return
	}

	data := map[string]interface{}{
		"Title":         "Sign in",
		"LoginDisabled": !si.LoginEnabled,
		"AuthMode":      si.AuthMode,
		"FormError":     r.URL.Query().Get("error"),
		"Identity":      r.URL.Query().Get("identity"),
	}
	h.render(w, r, "login.html", data)
}

// LoginSubmit handles POST /app/login: validates the identity selector,
// calls Auth.BeginLogin, and redirects the browser to the coordinator's
// returned redirect_url (an external LinkKeys IDP page, or a local-rp
// equivalent). The generated BeginLoginRequest carries no callback URL —
// the coordinator owns its own trusted callback destination so a browser
// can't redirect a login token to an attacker-controlled URL — so nothing
// is derived or sent here beyond the identity hint.
func (h *WebHandler) LoginSubmit(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		h.renderError(w, r, http.StatusBadRequest, "Invalid form submission", nil)
		return
	}

	identity := strings.TrimSpace(r.FormValue("identity"))
	if identity == "" {
		h.redirectLoginError(w, r, "identity is required", identity)
		return
	}
	if h.uiClients == nil {
		h.redirectLoginError(w, r, "login is not available", identity)
		return
	}

	si := h.sessionInfo(r)
	if !si.LoginEnabled {
		h.redirectLoginError(w, r, "login is disabled", identity)
		return
	}

	resp, err := h.uiClients.Auth.BeginLogin(r.Context(), csilapi.BeginLoginRequest{IdentityHint: &identity})
	if err != nil {
		h.redirectLoginError(w, r, serviceErrorMessage(err, "begin-login", "login failed"), identity)
		return
	}
	if resp.RedirectUrl == "" {
		h.redirectLoginError(w, r, "login did not return a redirect", identity)
		return
	}

	http.Redirect(w, r, resp.RedirectUrl, http.StatusFound)
}

func (h *WebHandler) redirectLoginError(w http.ResponseWriter, r *http.Request, msg, identity string) {
	v := url.Values{}
	v.Set("error", msg)
	if identity != "" {
		v.Set("identity", identity)
	}
	http.Redirect(w, r, "/app/login?"+v.Encode(), http.StatusFound)
}

// AuthCallback handles GET /app/auth/callback: the browser lands here after
// completing the external login flow, carrying the attempt token (issued by
// begin-login) and the encrypted token the IDP/local-rp minted. Trading
// those for a session token is Auth.CompleteLogin's job; success sets the
// session cookie and sends the browser on to /app/. Neither the attempt nor
// the encrypted token, nor the resulting session token, is ever logged.
func (h *WebHandler) AuthCallback(w http.ResponseWriter, r *http.Request) {
	attempt := r.URL.Query().Get("attempt")
	encryptedToken := r.URL.Query().Get("encrypted_token")
	if attempt == "" || encryptedToken == "" {
		h.renderError(w, r, http.StatusBadRequest, "Missing login callback parameters", nil)
		return
	}
	if h.uiClients == nil {
		h.renderError(w, r, http.StatusServiceUnavailable, "Login is not available", nil)
		return
	}

	resp, err := h.uiClients.Auth.CompleteLogin(r.Context(), csilapi.CompleteLoginRequest{
		AttemptToken:   attempt,
		EncryptedToken: encryptedToken,
	})
	if err != nil {
		var svcErr *uiclient.ServiceCallError
		if errors.As(err, &svcErr) {
			h.renderError(w, r, http.StatusUnauthorized, "Login failed: "+svcErr.Message, nil)
			return
		}
		logrus.WithError(err).Warn("uiclient: complete-login failed")
		h.renderError(w, r, http.StatusBadGateway, "Login failed", nil)
		return
	}

	h.setSessionCookie(w, resp.SessionToken, parseExpiresAt(resp.ExpiresAt))
	http.Redirect(w, r, "/app/", http.StatusFound)
}

// Logout handles POST /app/logout: best-effort revokes the session on the
// coordinator, then always clears the browser cookie regardless of whether
// that call succeeded (an unreachable coordinator must not strand the
// browser in a logged-in-looking state).
func (h *WebHandler) Logout(w http.ResponseWriter, r *http.Request) {
	if token, ok := h.sessionToken(r); ok && h.uiClients != nil {
		ctx := uiclient.WithAuthToken(r.Context(), token)
		if _, err := h.uiClients.Auth.Logout(ctx, csilapi.LogoutRequest{}); err != nil {
			logrus.WithError(err).Debug("uiclient: logout failed")
		}
	}
	h.clearSessionCookie(w)
	http.Redirect(w, r, "/app/", http.StatusFound)
}

// BootstrapPage renders GET /app/bootstrap: a token form that only works
// while the coordinator has no global admin yet (get-auth-config's
// bootstrap_admin_available).
func (h *WebHandler) BootstrapPage(w http.ResponseWriter, r *http.Request) {
	si := h.sessionInfo(r)
	data := map[string]interface{}{
		"Title":       "Bootstrap admin",
		"Unavailable": !si.BootstrapAvailable,
		"FormError":   r.URL.Query().Get("error"),
	}
	h.render(w, r, "bootstrap.html", data)
}

// BootstrapSubmit handles POST /app/bootstrap. The token is form-only: never
// logged, never echoed back into the re-rendered form on error.
func (h *WebHandler) BootstrapSubmit(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		h.renderError(w, r, http.StatusBadRequest, "Invalid form submission", nil)
		return
	}

	token := r.FormValue("token")
	if token == "" {
		h.redirectBootstrapError(w, r, "bootstrap token is required")
		return
	}
	if h.uiClients == nil {
		h.redirectBootstrapError(w, r, "bootstrap is not available")
		return
	}

	resp, err := h.uiClients.Auth.BootstrapAdmin(r.Context(), csilapi.BootstrapAdminRequest{BootstrapToken: token})
	if err != nil {
		h.redirectBootstrapError(w, r, serviceErrorMessage(err, "bootstrap-admin", "bootstrap failed"))
		return
	}

	h.setSessionCookie(w, resp.SessionToken, parseExpiresAt(resp.ExpiresAt))
	http.Redirect(w, r, "/app/", http.StatusFound)
}

func (h *WebHandler) redirectBootstrapError(w http.ResponseWriter, r *http.Request, msg string) {
	v := url.Values{}
	v.Set("error", msg)
	http.Redirect(w, r, "/app/bootstrap?"+v.Encode(), http.StatusFound)
}

// serviceErrorMessage extracts a user-facing message from a uiclient call
// error: the coordinator's ServiceError.Message when there is one (safe to
// show — it's a fixed reason string, never a secret/token), otherwise a
// generic fallback after logging the real (transport-level) error.
func serviceErrorMessage(err error, op, fallback string) string {
	var svcErr *uiclient.ServiceCallError
	if errors.As(err, &svcErr) {
		return svcErr.Message
	}
	logrus.WithError(err).Warnf("uiclient: %s failed", op)
	return fallback
}

// parseExpiresAt parses a CompleteLogin/BootstrapAdmin expires_at (RFC 3339)
// into a cookie Expires time, falling back to defaultSessionTTL from now if
// it doesn't parse (the coordinator's actual session expiry still wins;
// this only affects when the browser stops sending an already-expired
// cookie).
func parseExpiresAt(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		logrus.WithError(err).Warn("uiclient: unparsable session expires_at, using default TTL")
		return time.Now().Add(defaultSessionTTL)
	}
	return t
}
