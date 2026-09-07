package handlers

import (
	"context"
	"net/http"
	"time"

	"github.com/catalystcommunity/reactorcide/webapp/internal/config"
	"github.com/catalystcommunity/reactorcide/webapp/internal/uiclient"
	"github.com/catalystcommunity/reactorcide/webapp/internal/uiclient/csilapi"
	"github.com/sirupsen/logrus"
)

// sessionCookieName is the browser-facing cookie holding the opaque session
// token. It never carries anything else (no user data), is HttpOnly so page
// JS cannot read it, and is scoped to the whole app (path=/).
const sessionCookieName = "rc_session"

// loginAttemptCookieName is the browser-facing cookie that carries the
// opaque attempt token begin-login issued, from POST /app/login to GET
// /app/auth/callback. The coordinator deliberately keeps the pending LinkKeys
// login server-side (in auth_login_attempts, keyed by the SHA-256 hash of
// this token) and hands the browser only this single-use token, so the
// callback needs some way to present it again. The IDP's callback redirect
// carries the encrypted token and nothing else, which is why this cannot ride
// in the URL.
const loginAttemptCookieName = "rc_login_attempt"

// loginAttemptCookieTTL is how long the attempt cookie survives. It matches
// the coordinator's own LoginAttemptExpiry (auth.LoginAttemptExpiry, 5
// minutes): the attempt is dead server-side after that, so keeping the
// cookie longer only leaves a useless value in the browser.
const loginAttemptCookieTTL = 5 * time.Minute

// authConfigTTL bounds how often GET /app/login and every page render
// re-fetch get-auth-config from the coordinator. Auth mode is operator
// config, not per-user state, so a short cache is safe and cuts an RPC off
// most page loads without risking stale login-availability for long.
const authConfigTTL = 5 * time.Second

// SessionInfo is the display-only summary of the current browser session,
// threaded into every page's template data as .Session and available to
// handlers via (*WebHandler).sessionInfo. It is never authoritative — the
// coordinator is the sole authorizer of every mutating action; SessionInfo
// only drives what the webapp renders (the nav bar and which
// management buttons/forms to show at all). It deliberately does not carry
// the raw session token; handlers needing to make an authenticated CSIL call
// on behalf of the browser should use (*WebHandler).authContext instead.
type SessionInfo struct {
	// LoggedIn is true when the request's session cookie resolved to a
	// valid, non-expired session on the coordinator.
	LoggedIn bool
	// DisplayName is a human-friendly label for the nav bar: the identity's
	// display name, falling back to handle@domain, falling back to subject.
	DisplayName string
	UserID      string
	// IsGlobalAdmin mirrors AuthenticatedIdentity.IsGlobalAdmin for quick
	// checks; Caps.IsGlobalAdmin carries the same bit (kept for symmetry
	// with the other Is* capability flags).
	IsGlobalAdmin bool
	// Roles mirrors AuthenticatedIdentity.Roles: every explicit role grant
	// the caller holds, as (scope_type, scope_id, role) triples. The SPA
	// derives org- and project-level nav gating from these (e.g. "show the
	// management links when the caller holds admin on any org"), because
	// Caps below is the GLOBAL-scope set and says nothing about org
	// membership. Empty (never nil after resolveSession) for anonymous
	// callers.
	Roles []csilapi.RoleSummary

	// Caps is the coordinator's UNSCOPED capability computation for this
	// session, i.e. the global-scope set: its Manage*/Create*/Delete* bits
	// are true only for a global admin (anonymous sessions get anonymous
	// capabilities). Org- and project-level bits are deliberately NOT
	// folded in here; see capabilitiesRequestForNav. Every field is a
	// display-only hint: the backend re-checks on every mutating op.
	Caps csilapi.GetCapabilitiesResponse

	// AuthMode is the coordinator's configured REACTORCIDE_UI_AUTH_MODE
	// ("none" | "local-rp" | "rp"), or "" if it could not be determined
	// (coordinator unreachable, or this WebHandler has no uiClients — e.g.
	// in template-only tests).
	AuthMode string
	// LoginEnabled is AuthMode not in {"", "none"} — gates whether the nav
	// bar shows a "Sign in" link at all.
	LoginEnabled bool
	// BootstrapAvailable mirrors GetAuthConfigResponse.BootstrapAdminAvailable:
	// whether POST /app/bootstrap can currently succeed (no global admin
	// exists yet and a bootstrap token is configured).
	BootstrapAvailable bool
	HasGlobalAdmin     bool
}

// sessionInfoKey is the context key SessionMiddleware stores a computed
// SessionInfo under, so a single request only resolves its session once even
// if multiple handlers/templates consult it.
type sessionInfoKey struct{}

func withSessionInfo(ctx context.Context, si SessionInfo) context.Context {
	return context.WithValue(ctx, sessionInfoKey{}, si)
}

func sessionInfoFromContext(ctx context.Context) (SessionInfo, bool) {
	si, ok := ctx.Value(sessionInfoKey{}).(SessionInfo)
	return si, ok
}

// withSession wraps an http.HandlerFunc so SessionInfo is resolved exactly
// once per request and made available to the handler (and to h.render /
// h.renderError, which read it back out via sessionInfo) through the request
// context. Apply this to every /app/ route, including the auth routes
// themselves, so the nav bar renders consistently everywhere.
func (h *WebHandler) withSession(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		si := h.resolveSession(r)
		next(w, r.WithContext(withSessionInfo(r.Context(), si)))
	}
}

// sessionInfo returns the current request's SessionInfo, using the value
// SessionMiddleware already cached on the context when available. Falling
// back to a fresh resolveSession call keeps this safe to call from handlers
// or tests that invoke a handler directly without going through the mux
// (at the cost of a second round-trip in that uncommon path).
func (h *WebHandler) sessionInfo(r *http.Request) SessionInfo {
	if si, ok := sessionInfoFromContext(r.Context()); ok {
		return si
	}
	return h.resolveSession(r)
}

// resolveSession does the actual coordinator round-trips: get-auth-config
// (cached briefly, see authConfigTTL), authenticate (only if a session
// cookie is present), and get-capabilities (always, so anonymous capability
// hints render correctly too). A nil h.uiClients (page-rendering unit tests
// that don't wire up a fake coordinator) short-circuits to an anonymous,
// auth-mode-"none" SessionInfo without making any calls.
func (h *WebHandler) resolveSession(r *http.Request) SessionInfo {
	si := SessionInfo{Roles: []csilapi.RoleSummary{}}
	if h.uiClients == nil {
		si.AuthMode = "none"
		return si
	}

	if cfg, err := h.getAuthConfig(r.Context()); err != nil {
		logrus.WithError(err).Debug("uiclient: get-auth-config failed")
	} else {
		si.AuthMode = cfg.AuthMode
		si.LoginEnabled = cfg.AuthMode != "" && cfg.AuthMode != "none"
		si.BootstrapAvailable = cfg.BootstrapAdminAvailable
		si.HasGlobalAdmin = cfg.HasGlobalAdmin
	}

	authCtx := h.authContext(r)
	if _, hasToken := h.sessionToken(r); hasToken {
		resp, err := h.uiClients.Auth.Authenticate(authCtx, csilapi.AuthenticateRequest{})
		if err != nil {
			logrus.WithError(err).Debug("uiclient: authenticate failed")
		} else if resp.Authenticated && resp.Identity != nil {
			si.LoggedIn = true
			si.UserID = resp.Identity.UserId
			si.DisplayName = displayNameFor(*resp.Identity)
			si.IsGlobalAdmin = resp.Identity.IsGlobalAdmin
			if resp.Identity.Roles != nil {
				si.Roles = resp.Identity.Roles
			}
		}
	}

	if caps, err := h.uiClients.Ui.GetCapabilities(authCtx, capabilitiesRequestForNav(si)); err != nil {
		logrus.WithError(err).Debug("uiclient: get-capabilities failed")
	} else {
		si.Caps = caps
	}

	return si
}

// capabilitiesRequestForNav builds the request-wide GetCapabilities call
// resolveSession makes (whose result becomes SessionInfo.Caps and the
// session JSON's "capabilities"). It is always UNSCOPED: no org_id, no
// project_id. The coordinator answers an unscoped request with the
// GLOBAL-scope capability set, whose Manage*/Create*/Delete* bits are true
// only for a global admin (see coordinator_api/internal/authz/capabilities.go).
//
// Org- and project-level decisions are NOT made from this set. The SPA
// derives them from SessionInfo.Roles (the caller's explicit role grants,
// e.g. "admin on org X" => show the management nav links and offer org X in
// the org picker) and from project-scoped get-capabilities calls it makes
// itself for detail pages (get-capabilities {project_id}).
//
// History, so nobody reintroduces the old behaviour: this used to scope a
// logged-in non-global-admin's request to org_id = their own user_id, on the
// premise "user_id IS the org id everywhere". That premise does not hold for
// how projects are actually stored. Projects live in real organizations rows
// (REST-created projects land in the default org), while a login-provisioned
// user's user_id has no organizations row at all, so the "own org" was a
// phantom. Worse, at that scope the coordinator treats the caller as the
// org's owner and grants the full org-admin capability set, so EVERY
// logged-in user looked like an org admin and saw every management link,
// with the org pages then managing the phantom org. Scoping the nav call to
// anything but global is therefore wrong; the SessionInfo argument is kept
// only so call sites read naturally and future per-session tweaks have a
// home.
func capabilitiesRequestForNav(_ SessionInfo) csilapi.GetCapabilitiesRequest {
	return csilapi.GetCapabilitiesRequest{}
}

func displayNameFor(id csilapi.AuthenticatedIdentity) string {
	if id.DisplayName != "" {
		return id.DisplayName
	}
	if id.Handle != "" && id.Domain != "" {
		return id.Handle + "@" + id.Domain
	}
	return id.Subject
}

// getAuthConfig fetches get-auth-config, cached for authConfigTTL. Auth mode
// is a coordinator-wide setting, not per-user, so sharing this cache across
// all requests/users on this WebHandler is safe.
func (h *WebHandler) getAuthConfig(ctx context.Context) (csilapi.GetAuthConfigResponse, error) {
	if h.uiClients == nil {
		return csilapi.GetAuthConfigResponse{AuthMode: "none"}, nil
	}

	h.authConfigMu.Lock()
	if !h.authConfigAt.IsZero() && time.Since(h.authConfigAt) < authConfigTTL {
		v, err := h.authConfigVal, h.authConfigErr
		h.authConfigMu.Unlock()
		return v, err
	}
	h.authConfigMu.Unlock()

	v, err := h.uiClients.Auth.GetAuthConfig(ctx, csilapi.GetAuthConfigRequest{})

	h.authConfigMu.Lock()
	h.authConfigVal, h.authConfigErr, h.authConfigAt = v, err, time.Now()
	h.authConfigMu.Unlock()

	return v, err
}

// sessionToken reads the raw session token from the browser's cookie, if
// any. Never log this value.
func (h *WebHandler) sessionToken(r *http.Request) (string, bool) {
	return sessionTokenFromRequest(r)
}

// sessionTokenFromRequest is sessionToken without a receiver, for handlers that
// are not WebHandler methods (the CSIL-RPC bridge and the WebSocket proxy).
// Never log this value.
func sessionTokenFromRequest(r *http.Request) (string, bool) {
	c, err := r.Cookie(sessionCookieName)
	if err != nil || c.Value == "" {
		return "", false
	}
	return c.Value, true
}

// authContext returns r's context, augmented with the browser's session
// token (if any) via uiclient.WithAuthToken so a CSIL call made with it
// carries the caller's identity in the envelope "auth" field. A request with
// no session cookie gets back r.Context() unchanged, i.e. an anonymous call.
func (h *WebHandler) authContext(r *http.Request) context.Context {
	token, ok := h.sessionToken(r)
	if !ok {
		return r.Context()
	}
	return uiclient.WithAuthToken(r.Context(), token)
}

// setSessionCookie sets the browser's session cookie. HttpOnly (no page JS
// access), SameSite=Lax (survives top-level navigation from the LinkKeys
// callback redirect while still blocking cross-site POST/fetch CSRF),
// Secure unless REACTORCIDE_WEB_COOKIE_INSECURE is set (local http dev),
// path=/ so it's sent on every /app/ route.
func (h *WebHandler) setSessionCookie(w http.ResponseWriter, token string, expiresAt time.Time) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    token,
		Path:     "/",
		Expires:  expiresAt,
		HttpOnly: true,
		Secure:   !config.WebCookieInsecure,
		SameSite: http.SameSiteLaxMode,
	})
}

// clearSessionCookie removes the session cookie (logout, or a callback/
// bootstrap failure that must not leave a stale cookie behind). Flags must
// match setSessionCookie's or some browsers won't delete the existing
// cookie.
func (h *WebHandler) clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		Expires:  time.Unix(0, 0),
		HttpOnly: true,
		Secure:   !config.WebCookieInsecure,
		SameSite: http.SameSiteLaxMode,
	})
}

// loginAttemptToken reads the attempt token the login form stashed. Never
// log this value: it is the redeemable half of a pending login.
func (h *WebHandler) loginAttemptToken(r *http.Request) string {
	c, err := r.Cookie(loginAttemptCookieName)
	if err != nil {
		return ""
	}
	return c.Value
}

// setLoginAttemptCookie stashes the attempt token for the callback. Same
// flags as the session cookie — HttpOnly, Secure unless dev, SameSite=Lax so
// it survives the IDP's top-level redirect back to /app/auth/callback — but
// short-lived, since it is redeemable exactly once and only for the next few
// minutes.
func (h *WebHandler) setLoginAttemptCookie(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     loginAttemptCookieName,
		Value:    token,
		Path:     "/",
		Expires:  time.Now().Add(loginAttemptCookieTTL),
		MaxAge:   int(loginAttemptCookieTTL.Seconds()),
		HttpOnly: true,
		Secure:   !config.WebCookieInsecure,
		SameSite: http.SameSiteLaxMode,
	})
}

// clearLoginAttemptCookie removes the attempt cookie. Called on every
// callback outcome, success or failure: the token is single-use server-side
// either way, so leaving it set would only feed a confusing second attempt.
// Flags must match setLoginAttemptCookie's or some browsers won't delete it.
func (h *WebHandler) clearLoginAttemptCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     loginAttemptCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		Expires:  time.Unix(0, 0),
		HttpOnly: true,
		Secure:   !config.WebCookieInsecure,
		SameSite: http.SameSiteLaxMode,
	})
}

// capabilitiesForProject fetches get-capabilities scoped to a specific
// project (falling back to unscoped/global capabilities when projectID is
// nil/empty), for pages — job/workflow detail — that need capability bits
// specific to that project's owner/org rather than the request-wide
// SessionInfo.Caps. Used today only to compute CanCancel/CanKill for
// job and workflow detail template data. Project pages can reuse it the
// same way. Errors are logged and treated as "no capabilities" (safe
// default: nothing renders as allowed).
func (h *WebHandler) capabilitiesForProject(r *http.Request, projectID *string) csilapi.GetCapabilitiesResponse {
	if h.uiClients == nil {
		return csilapi.GetCapabilitiesResponse{}
	}
	req := csilapi.GetCapabilitiesRequest{}
	if projectID != nil && *projectID != "" {
		req.ProjectId = projectID
	}
	caps, err := h.uiClients.Ui.GetCapabilities(h.authContext(r), req)
	if err != nil {
		logrus.WithError(err).Debug("uiclient: get-capabilities (scoped) failed")
		return csilapi.GetCapabilitiesResponse{}
	}
	return caps
}
