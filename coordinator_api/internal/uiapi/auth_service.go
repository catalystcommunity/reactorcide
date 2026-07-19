package uiapi

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/auth"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/authz"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/config"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/uiapi/csilapi"
)

// AuthService implements csilapi.ReactorcideAuth against Deps' auth.LoginService
// and auth.Sessions.
type AuthService struct {
	deps *Deps
}

// NewAuthService constructs an AuthService.
func NewAuthService(deps *Deps) *AuthService {
	return &AuthService{deps: deps}
}

var _ csilapi.ReactorcideAuth = (*AuthService)(nil)

// hasGlobalAdmin reports whether any global/admin role_assignments row
// exists. Small, deliberately duplicated instead of exporting
// LoginService's private equivalent — see login_service.go's
// hasGlobalAdmin, which this mirrors.
func (s *AuthService) hasGlobalAdmin(ctx context.Context) (bool, error) {
	assignments, err := s.deps.Store.ListRoleAssignmentsByScope(ctx, models.ScopeTypeGlobal, nil)
	if err != nil {
		return false, err
	}
	for _, a := range assignments {
		if a.Role == models.RoleAdmin {
			return true, nil
		}
	}
	return false, nil
}

// GetAuthConfig reports the configured auth mode and whether login/bootstrap
// are available. Safe for anonymous callers.
func (s *AuthService) GetAuthConfig(ctx context.Context, req csilapi.GetAuthConfigRequest) (csilapi.GetAuthConfigResponse, error) {
	hasAdmin, err := s.hasGlobalAdmin(ctx)
	if err != nil {
		return csilapi.GetAuthConfigResponse{}, NewServiceError("internal", "an internal error occurred")
	}
	return csilapi.GetAuthConfigResponse{
		AuthMode:                string(auth.CurrentMode()),
		BootstrapAdminAvailable: config.BootstrapAdminToken != "" && !hasAdmin,
		HasGlobalAdmin:          hasAdmin,
	}, nil
}

// callbackURL builds the LinkKeys login callback URL pointing at the
// webapp's callback route. See config.UICallbackURL's doc comment for why
// this isn't part of the request.
func callbackURL() (string, error) {
	base := strings.TrimSpace(config.UICallbackURL)
	if base == "" {
		return "", errors.New("REACTORCIDE_UI_CALLBACK_URL is not configured")
	}
	return strings.TrimRight(base, "/") + "/app/auth/callback", nil
}

// mapLoginErr maps an error from LoginService.StartLogin/FinishLogin to a
// caller-facing ServiceErr, per the stable code vocabulary
// (unauthorized/forbidden/not_found/invalid_argument/login_disabled/
// conflict/internal).
func mapLoginErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, auth.ErrLoginDisabled):
		return NewServiceError("login_disabled", "login is disabled")
	case errors.Is(err, auth.ErrNotAdmitted):
		return NewServiceError("forbidden", "this identity is not on the admission list")
	case errors.Is(err, auth.ErrAttemptExpired):
		return NewServiceError("invalid_argument", "login attempt has expired; please start over")
	case errors.Is(err, auth.ErrAssertionNotVerified):
		return NewServiceError("forbidden", "login could not be verified")
	case errors.Is(err, store.ErrNotFound):
		// ConsumeLoginAttempt's not-found case: an unknown, already-used
		// (replayed), or expired-and-swept attempt token.
		return NewServiceError("invalid_argument", "attempt_token is invalid or has already been used")
	default:
		return NewServiceError("internal", "an internal error occurred completing login")
	}
}

// BeginLogin starts a LinkKeys login for req.IdentityHint (a "[handle@]domain"
// selector; required — there is no way to know where to redirect the
// browser without it).
func (s *AuthService) BeginLogin(ctx context.Context, req csilapi.BeginLoginRequest) (csilapi.BeginLoginResponse, error) {
	if auth.CurrentMode() == auth.ModeNone {
		return csilapi.BeginLoginResponse{}, NewServiceError("login_disabled", "login is disabled")
	}
	hint := ""
	if req.IdentityHint != nil {
		hint = strings.TrimSpace(*req.IdentityHint)
	}
	if hint == "" {
		return csilapi.BeginLoginResponse{}, NewServiceError("invalid_argument", "identity_hint (domain or handle@domain) is required")
	}

	cb, err := callbackURL()
	if err != nil {
		return csilapi.BeginLoginResponse{}, NewServiceError("internal", "login is not fully configured on this server")
	}

	started, err := s.deps.LoginService.StartLogin(ctx, hint, cb)
	if err != nil {
		return csilapi.BeginLoginResponse{}, mapLoginErr(err)
	}
	return csilapi.BeginLoginResponse{
		RedirectUrl:  started.RedirectURL,
		AttemptToken: started.AttemptToken,
	}, nil
}

// arrivedURLFor synthesizes a minimal URL carrying only the
// "encrypted_token" query parameter LoginBackend.CompleteLogin extracts.
// CompleteLoginRequest carries the already-extracted encrypted token
// directly (the webapp's callback route parses the real arrived URL), not
// a full URL, so this reconstructs just enough of one for the backend's
// extractEncryptedToken helper.
func arrivedURLFor(encryptedToken string) string {
	u := url.URL{Path: "/auth/callback"}
	q := u.Query()
	q.Set("encrypted_token", encryptedToken)
	u.RawQuery = q.Encode()
	return u.String()
}

// buildAuthenticatedIdentity loads the auth identity + role assignments for
// user and maps them to the generated AuthenticatedIdentity type.
func (s *AuthService) buildAuthenticatedIdentity(ctx context.Context, user *models.User) (csilapi.AuthenticatedIdentity, error) {
	ai := csilapi.AuthenticatedIdentity{
		UserId:      user.UserID,
		DisplayName: user.Username,
	}

	if identity, err := s.deps.Store.GetAuthIdentityByUserID(ctx, user.UserID); err == nil {
		ai.Subject = identity.Subject
		ai.Handle = identity.Handle
		ai.Domain = identity.Domain
		if identity.DisplayName != "" {
			ai.DisplayName = identity.DisplayName
		}
	}

	globalAdmin, err := s.deps.Resolver.IsGlobalAdmin(ctx, authz.UserIdentity(user.UserID))
	if err != nil {
		return csilapi.AuthenticatedIdentity{}, err
	}
	ai.IsGlobalAdmin = globalAdmin

	groups, err := s.deps.Store.ListGroupsForUser(ctx, user.UserID)
	if err != nil {
		return csilapi.AuthenticatedIdentity{}, err
	}
	groupIDs := make([]string, len(groups))
	for i, g := range groups {
		groupIDs[i] = g.GroupID
	}
	assignments, err := s.deps.Store.ListRoleAssignmentsForPrincipal(ctx, user.UserID, groupIDs)
	if err != nil {
		return csilapi.AuthenticatedIdentity{}, err
	}
	ai.Roles = make([]csilapi.RoleSummary, len(assignments))
	for i, a := range assignments {
		ai.Roles[i] = csilapi.RoleSummary{ScopeType: a.ScopeType, ScopeId: a.ScopeID, Role: a.Role}
	}
	return ai, nil
}

// CompleteLogin finishes a pending login and mints a session.
func (s *AuthService) CompleteLogin(ctx context.Context, req csilapi.CompleteLoginRequest) (csilapi.CompleteLoginResponse, error) {
	if err := requireNonEmpty("attempt_token", req.AttemptToken, 512); err != nil {
		return csilapi.CompleteLoginResponse{}, err
	}
	if err := requireNonEmpty("encrypted_token", req.EncryptedToken, 65536); err != nil {
		return csilapi.CompleteLoginResponse{}, err
	}

	token, user, err := s.deps.LoginService.FinishLogin(ctx, req.AttemptToken, arrivedURLFor(req.EncryptedToken))
	if err != nil {
		return csilapi.CompleteLoginResponse{}, mapLoginErr(err)
	}

	ai, err := s.buildAuthenticatedIdentity(ctx, user)
	if err != nil {
		return csilapi.CompleteLoginResponse{}, NewServiceError("internal", "an internal error occurred")
	}

	return csilapi.CompleteLoginResponse{
		SessionToken: token,
		ExpiresAt:    formatTime(time.Now().Add(auth.SessionExpiry)),
		Identity:     ai,
	}, nil
}

// Authenticate resolves the envelope's auth token (if any) to an identity.
// Never errors: an absent/invalid/expired session simply reports
// Authenticated=false.
func (s *AuthService) Authenticate(ctx context.Context, req csilapi.AuthenticateRequest) (csilapi.AuthenticateResponse, error) {
	_, user := s.deps.resolveIdentity(ctx)
	if user == nil {
		return csilapi.AuthenticateResponse{Authenticated: false}, nil
	}
	ai, err := s.buildAuthenticatedIdentity(ctx, user)
	if err != nil {
		return csilapi.AuthenticateResponse{}, NewServiceError("internal", "an internal error occurred")
	}
	return csilapi.AuthenticateResponse{Authenticated: true, Identity: &ai}, nil
}

// Logout revokes the caller's session, if any. Idempotent.
func (s *AuthService) Logout(ctx context.Context, req csilapi.LogoutRequest) (csilapi.LogoutResponse, error) {
	token, _ := AuthTokenFromContext(ctx)
	if err := s.deps.Sessions.RevokeSession(ctx, token); err != nil {
		return csilapi.LogoutResponse{}, NewServiceError("internal", "an internal error occurred")
	}
	return csilapi.LogoutResponse{Ok: true}, nil
}

// BootstrapAdmin honors REACTORCIDE_BOOTSTRAP_ADMIN_TOKEN: a constant-time
// comparison; any failure (not configured, wrong token, admin already
// exists) is reported uniformly as "unauthorized" so there is no oracle for
// probing the feature (see LoginService.BootstrapAdminSession's doc
// comment).
func (s *AuthService) BootstrapAdmin(ctx context.Context, req csilapi.BootstrapAdminRequest) (csilapi.BootstrapAdminResponse, error) {
	token, err := s.deps.LoginService.BootstrapAdminSession(ctx, req.BootstrapToken)
	if err != nil {
		return csilapi.BootstrapAdminResponse{}, NewServiceError("internal", "an internal error occurred")
	}
	if token == "" {
		return csilapi.BootstrapAdminResponse{}, NewServiceError("unauthorized", "bootstrap admin is not available")
	}
	return csilapi.BootstrapAdminResponse{
		SessionToken: token,
		ExpiresAt:    formatTime(time.Now().Add(auth.SessionExpiry)),
	}, nil
}
