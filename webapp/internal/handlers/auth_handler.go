package handlers

import (
	"encoding/json"
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
// coordinator's 30-day session expiry.
const defaultSessionTTL = 30 * 24 * time.Hour

// LoginSubmit handles POST /app/login: validates the identity selector,
// calls Auth.BeginLogin, and redirects the browser to the coordinator's
// returned redirect_url (an external LinkKeys IDP page, or a local-rp
// equivalent). The generated BeginLoginRequest carries no callback URL —
// the coordinator owns its own trusted callback destination so a browser
// can't redirect a login token to an attacker-controlled URL — so nothing
// is derived or sent here beyond the identity hint.
func (h *WebHandler) LoginSubmit(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		writeAuthError(w, http.StatusBadRequest, "Invalid form submission")
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
	if resp.AttemptToken == "" {
		h.redirectLoginError(w, r, "login did not return an attempt token", identity)
		return
	}

	// Stash the attempt token before leaving for the IDP. The IDP appends
	// only `encrypted_token` to the callback URL, so this cookie is the sole
	// way GET /app/auth/callback can present the other half that
	// complete-login requires. Never logged, never rendered.
	h.setLoginAttemptCookie(w, resp.AttemptToken)

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
// completing the external login flow, carrying the encrypted token the
// IDP/local-rp minted. The matching attempt token comes from the cookie
// LoginSubmit set — the IDP appends only `encrypted_token`, so it is never in
// the URL (an `attempt` query parameter is still honored for a caller that
// supplies one deliberately, e.g. a test harness). Trading the pair for a
// session token is Auth.CompleteLogin's job; success sets the session cookie
// and sends the browser on to /app/. Neither the attempt nor the encrypted
// token, nor the resulting session token, is ever logged.
func (h *WebHandler) AuthCallback(w http.ResponseWriter, r *http.Request) {
	attempt := h.loginAttemptToken(r)
	if attempt == "" {
		attempt = r.URL.Query().Get("attempt")
	}
	encryptedToken := r.URL.Query().Get("encrypted_token")
	if attempt == "" || encryptedToken == "" {
		// The attempt is single-use and may simply have expired mid-login;
		// clear it so a retry starts from a clean slate.
		h.clearLoginAttemptCookie(w)
		h.redirectLoginFailure(w, r, "Missing login callback parameters")
		return
	}
	if h.uiClients == nil {
		h.redirectLoginFailure(w, r, "Login is not available")
		return
	}

	resp, err := h.uiClients.Auth.CompleteLogin(r.Context(), csilapi.CompleteLoginRequest{
		AttemptToken:   attempt,
		EncryptedToken: encryptedToken,
	})
	// The coordinator consumes the attempt whatever the outcome, so the
	// cookie is spent either way.
	h.clearLoginAttemptCookie(w)
	if err != nil {
		var svcErr *uiclient.ServiceCallError
		if errors.As(err, &svcErr) {
			h.redirectLoginFailure(w, r, "Login failed: "+svcErr.Message)
			return
		}
		logrus.WithError(err).Warn("uiclient: complete-login failed")
		h.redirectLoginFailure(w, r, "Login failed")
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

// BootstrapSubmit handles POST /app/bootstrap. The token is form-only: never
// logged, never echoed back into the re-rendered form on error.
func (h *WebHandler) BootstrapSubmit(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		writeAuthError(w, http.StatusBadRequest, "Invalid form submission")
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
	writeAuthError(w, http.StatusBadRequest, msg)
}

// redirectLoginFailure sends the browser back into the SPA with the reason in
// the query string.
//
// The IDP callback is a top-level NAVIGATION, not a fetch, so this one path
// must answer with a redirect rather than JSON: the browser is following a
// redirect chain and there is no client-side code listening for a response
// body. The SPA reads ?login_error= on its sign-in route and shows it.
func (h *WebHandler) redirectLoginFailure(w http.ResponseWriter, r *http.Request, msg string) {
	v := url.Values{}
	v.Set("login_error", msg)
	http.Redirect(w, r, "/app/signin?"+v.Encode(), http.StatusFound)
}

// writeAuthError answers a fetch from the SPA.
func writeAuthError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
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
