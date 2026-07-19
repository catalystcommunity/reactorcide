package auth

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/config"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

// LoginAttemptExpiry is how long a single-use pending login attempt
// (StartLogin's persisted state) remains redeemable.
const LoginAttemptExpiry = 5 * time.Minute

// BootstrapAdminUsername is the username given to the find-or-created
// bootstrap admin user (see LoginService.BootstrapAdminSession).
const BootstrapAdminUsername = "bootstrap-admin"

// bootstrapAdminSubject/Domain identify the synthetic auth_identities row
// backing the bootstrap admin user. They're not a real LinkKeys identity —
// no login flow ever produces this subject/domain pair — chosen only so
// BootstrapAdminSession can reuse the same find-or-create provisioning path
// StartLogin/FinishLogin use for real logins instead of duplicating it.
const (
	bootstrapAdminSubject = "bootstrap-admin"
	bootstrapAdminDomain  = "reactorcide.local-bootstrap"
)

// UserProvisionStore is the narrow store surface LoginService's
// identity->user provisioning and first-admin/bootstrap-admin grants
// consume, satisfied by Task A's postgres_store/auth_operations.go and
// rbac_operations.go plus the existing user CRUD.
type UserProvisionStore interface {
	GetAuthIdentityBySubject(ctx context.Context, subject string) (*models.AuthIdentity, error)
	CreateAuthIdentity(ctx context.Context, identity *models.AuthIdentity) error
	UpdateAuthIdentityLogin(ctx context.Context, identityID string, displayName string) error
	CreateUser(ctx context.Context, user *models.User) error
	GetUserByID(ctx context.Context, userID string) (*models.User, error)
	ListRoleAssignmentsByScope(ctx context.Context, scopeType string, scopeID *string) ([]models.RoleAssignment, error)
	CreateRoleAssignment(ctx context.Context, assignment *models.RoleAssignment) error
}

// LoginAttemptStore is the narrow store surface for the single-use pending
// login attempts StartLogin/FinishLogin round-trip.
type LoginAttemptStore interface {
	CreateLoginAttempt(ctx context.Context, attempt *models.AuthLoginAttempt) error
	ConsumeLoginAttempt(ctx context.Context, attemptHash []byte) (*models.AuthLoginAttempt, error)
}

// LoginServiceStore is everything LoginService needs from the store: the
// admission list, single-use login attempts, identity->user provisioning,
// and sessions.
type LoginServiceStore interface {
	AdmissionStore
	LoginAttemptStore
	UserProvisionStore
	SessionStore
}

// LoginService orchestrates a full LinkKeys login: admission pre-check,
// backend BeginLogin/CompleteLogin, single-use attempt persistence,
// admission re-check on the verified identity, identity->user provisioning
// (including first-admin), and session minting. It also hosts
// BootstrapAdminSession, which shares the same provisioning path for a
// different entry point.
type LoginService struct {
	store     LoginServiceStore
	backend   LoginBackend
	admission *Admission
	sessions  *Sessions
	now       func() time.Time
}

// NewLoginService constructs a LoginService. backend is typically
// NewNoneBackend(), a *LocalRPBackend, or a *RPBackend, matching
// auth.CurrentMode().
func NewLoginService(store LoginServiceStore, backend LoginBackend) *LoginService {
	return &LoginService{
		store:     store,
		backend:   backend,
		admission: NewAdmission(store),
		sessions:  NewSessions(store),
		now:       time.Now,
	}
}

// StartedLogin is the result of StartLogin: the redirect URL for the
// browser, and an opaque attempt token the caller must round-trip back to
// FinishLogin (e.g. via a short-lived cookie). This is deliberately NOT the
// LinkKeys pending blob itself — that stays server-side in
// auth_login_attempts, keyed by the SHA-256 hash of this token, exactly the
// way ui_sessions stores session-token hashes rather than raw tokens.
type StartedLogin struct {
	RedirectURL  string
	AttemptToken string
}

// StartLogin begins a login for identitySelector ("[handle@]domain") and
// callbackURL. It pre-checks admission on the requested selector (a cheap
// reject before bothering the backend), delegates to the backend's
// BeginLogin, and persists a single-use, sha256-keyed, 5-minute-expiry
// pending attempt.
func (l *LoginService) StartLogin(ctx context.Context, identitySelector, callbackURL string) (*StartedLogin, error) {
	if l.backend.Mode() == ModeNone {
		return nil, ErrLoginDisabled
	}

	handle, domain, err := ParseSelector(identitySelector)
	if err != nil {
		return nil, err
	}
	admitted, err := l.admission.Admitted(ctx, domain, handle)
	if err != nil {
		return nil, err
	}
	if !admitted {
		return nil, ErrNotAdmitted
	}

	redirectURL, pendingBlob, err := l.backend.BeginLogin(ctx, identitySelector, callbackURL)
	if err != nil {
		return nil, err
	}

	attemptToken, err := generateToken()
	if err != nil {
		return nil, err
	}
	now := l.now()
	attempt := &models.AuthLoginAttempt{
		AttemptHash:  hashToken(attemptToken),
		PendingLogin: pendingBlob,
		CreatedAt:    now,
		ExpiresAt:    now.Add(LoginAttemptExpiry),
	}
	if err := l.store.CreateLoginAttempt(ctx, attempt); err != nil {
		return nil, fmt.Errorf("auth: persisting login attempt: %w", err)
	}

	return &StartedLogin{RedirectURL: redirectURL, AttemptToken: attemptToken}, nil
}

// FinishLogin consumes attemptToken (single-use: ConsumeLoginAttempt atomically
// fetches-and-deletes, so a replayed token fails with store.ErrNotFound),
// completes the login against the backend, re-checks admission on the now-
// VERIFIED identity (never trusting the pre-check alone — the requested
// selector and the identity that actually authenticated are not
// guaranteed to match until the backend says so), provisions a local user,
// and mints a session. Returns the raw session token and the provisioned
// user.
func (l *LoginService) FinishLogin(ctx context.Context, attemptToken, arrivedURL string) (string, *models.User, error) {
	if l.backend.Mode() == ModeNone {
		return "", nil, ErrLoginDisabled
	}

	attempt, err := l.store.ConsumeLoginAttempt(ctx, hashToken(attemptToken))
	if err != nil {
		return "", nil, err
	}
	if attempt.IsExpired() {
		return "", nil, ErrAttemptExpired
	}

	verified, err := l.backend.CompleteLogin(ctx, attempt.PendingLogin, arrivedURL)
	if err != nil {
		return "", nil, err
	}

	admitted, err := l.admission.Admitted(ctx, verified.Domain, verified.Handle)
	if err != nil {
		return "", nil, err
	}
	if !admitted {
		return "", nil, ErrNotAdmitted
	}

	user, err := l.provisionUser(ctx, verified)
	if err != nil {
		return "", nil, err
	}

	token, err := l.sessions.MintSession(ctx, user.UserID)
	if err != nil {
		return "", nil, err
	}
	return token, user, nil
}

// subjectFor builds the auth_identities.subject value for a verified
// identity: "subject@domain", unique per LinkKeys identity.
func subjectFor(v *VerifiedIdentity) string {
	return v.Subject + "@" + v.Domain
}

// usernameFor picks a users.username for a freshly provisioned user:
// prefer the verified handle (human-readable), fall back to the raw
// subject (e.g. a uuid) when no handle was returned.
func usernameFor(v *VerifiedIdentity) string {
	if v.Handle != "" {
		return v.Handle
	}
	return v.Subject
}

// provisionUser maps a VerifiedIdentity to a local users row:
// GetAuthIdentityBySubject -> reuse; else create a new users row + linked
// auth_identities row. Always stamps last_login_at/display_name, and checks
// (idempotently) for a first-admin grant.
func (l *LoginService) provisionUser(ctx context.Context, v *VerifiedIdentity) (*models.User, error) {
	subject := subjectFor(v)

	identity, err := l.store.GetAuthIdentityBySubject(ctx, subject)
	switch {
	case err == nil:
		if err := l.store.UpdateAuthIdentityLogin(ctx, identity.IdentityID, v.DisplayName); err != nil {
			return nil, fmt.Errorf("auth: updating identity login: %w", err)
		}
	case errors.Is(err, store.ErrNotFound):
		user := &models.User{
			Username: usernameFor(v),
			Email:    v.Claims["email"],
		}
		if err := l.store.CreateUser(ctx, user); err != nil {
			return nil, fmt.Errorf("auth: creating user: %w", err)
		}
		identity = &models.AuthIdentity{
			UserID:      user.UserID,
			Subject:     subject,
			Handle:      v.Handle,
			Domain:      v.Domain,
			DisplayName: v.DisplayName,
		}
		if err := l.store.CreateAuthIdentity(ctx, identity); err != nil {
			return nil, fmt.Errorf("auth: creating auth identity: %w", err)
		}
		if err := l.store.UpdateAuthIdentityLogin(ctx, identity.IdentityID, v.DisplayName); err != nil {
			return nil, fmt.Errorf("auth: stamping first login: %w", err)
		}
	default:
		return nil, fmt.Errorf("auth: looking up auth identity: %w", err)
	}

	user, err := l.store.GetUserByID(ctx, identity.UserID)
	if err != nil {
		return nil, fmt.Errorf("auth: loading provisioned user: %w", err)
	}

	if err := l.maybeGrantFirstAdmin(ctx, v, user.UserID); err != nil {
		return nil, err
	}

	return user, nil
}

// maybeGrantFirstAdmin grants global admin to userID exactly once: only
// when REACTORCIDE_FIRST_ADMIN is configured, v matches it, and no global
// admin role assignment exists yet. Safe to call on every login (a no-op
// once an admin exists).
func (l *LoginService) maybeGrantFirstAdmin(ctx context.Context, v *VerifiedIdentity, userID string) error {
	if strings.TrimSpace(config.FirstAdmin) == "" {
		return nil
	}
	if !matchesFirstAdmin(v) {
		return nil
	}
	hasAdmin, err := l.hasGlobalAdmin(ctx)
	if err != nil {
		return err
	}
	if hasAdmin {
		return nil
	}
	if err := l.grantGlobalAdmin(ctx, userID); err != nil {
		return fmt.Errorf("auth: granting first-admin role: %w", err)
	}
	return nil
}

// matchesFirstAdmin reports whether v is the identity named by
// REACTORCIDE_FIRST_ADMIN ("[handle@]domain" or "[uuid@]domain" — matched
// against both v.Handle and v.Subject since either may be what an operator
// wrote down). A bare-domain FIRST_ADMIN selector (no "@") matches any
// identity at that domain, mirroring the trusted-identity bare-domain
// wildcard semantics.
func matchesFirstAdmin(v *VerifiedIdentity) bool {
	handle, domain, err := ParseSelector(config.FirstAdmin)
	if err != nil {
		return false
	}
	if domain != v.Domain {
		return false
	}
	if handle == "" {
		return true
	}
	return handle == v.Handle || handle == v.Subject
}

func (l *LoginService) hasGlobalAdmin(ctx context.Context) (bool, error) {
	assignments, err := l.store.ListRoleAssignmentsByScope(ctx, models.ScopeTypeGlobal, nil)
	if err != nil {
		return false, fmt.Errorf("auth: listing global role assignments: %w", err)
	}
	for _, a := range assignments {
		if a.Role == models.RoleAdmin {
			return true, nil
		}
	}
	return false, nil
}

func (l *LoginService) grantGlobalAdmin(ctx context.Context, userID string) error {
	return l.store.CreateRoleAssignment(ctx, &models.RoleAssignment{
		PrincipalType: models.PrincipalTypeUser,
		PrincipalID:   userID,
		ScopeType:     models.ScopeTypeGlobal,
		Role:          models.RoleAdmin,
	})
}

// BootstrapAdminSession honors REACTORCIDE_BOOTSTRAP_ADMIN_TOKEN: while zero
// global-admin role assignments exist, a caller presenting the exact
// configured token (constant-time compared) gets a session for a
// find-or-created "bootstrap-admin" user holding a global admin role
// assignment.
//
// Returns ("", nil) — inert, not an error — if REACTORCIDE_BOOTSTRAP_ADMIN_TOKEN
// isn't configured, the presented token doesn't match, or a global admin
// already exists. Callers cannot distinguish those three cases from the
// return value alone; that's deliberate (no oracle for probing whether the
// feature is enabled or a token is close to correct).
func (l *LoginService) BootstrapAdminSession(ctx context.Context, presentedToken string) (string, error) {
	configured := config.BootstrapAdminToken
	if configured == "" {
		return "", nil
	}
	if subtle.ConstantTimeCompare([]byte(presentedToken), []byte(configured)) != 1 {
		return "", nil
	}

	hasAdmin, err := l.hasGlobalAdmin(ctx)
	if err != nil {
		return "", err
	}
	if hasAdmin {
		return "", nil
	}

	user, err := l.provisionUser(ctx, &VerifiedIdentity{
		Subject:     bootstrapAdminSubject,
		Domain:      bootstrapAdminDomain,
		Handle:      BootstrapAdminUsername,
		DisplayName: "Bootstrap Admin",
	})
	if err != nil {
		return "", fmt.Errorf("auth: provisioning bootstrap admin: %w", err)
	}

	// provisionUser already runs maybeGrantFirstAdmin (a no-op here unless
	// REACTORCIDE_FIRST_ADMIN happens to name this synthetic identity, which
	// it never legitimately would). Explicitly ensure the grant regardless,
	// re-checking under the lock-free "was another bootstrap admin session
	// racing us" case rather than assuming provisionUser's internal check
	// still holds.
	hasAdmin, err = l.hasGlobalAdmin(ctx)
	if err != nil {
		return "", err
	}
	if !hasAdmin {
		if err := l.grantGlobalAdmin(ctx, user.UserID); err != nil {
			return "", fmt.Errorf("auth: granting bootstrap admin role: %w", err)
		}
	}

	return l.sessions.MintSession(ctx, user.UserID)
}
