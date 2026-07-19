// Package auth is the auth core for the reactorcide management UI:
// REACTORCIDE_UI_AUTH_MODE config, LinkKeys local-RP and regular-RP login
// backends, encrypted credential storage, trusted-identity/domain-pattern
// admission, single-use login attempts, identity->user provisioning
// (including first-admin and bootstrap-admin grants), and opaque session
// tokens. See UI_AUTH_PLAN.md's "Auth modes" and "Identity & RBAC model"
// sections for the architecture this package implements (Task C).
//
// This package deliberately knows nothing about HTTP, CSIL-RPC, or the
// coordinator's REST handlers — those are Wave 3 (Task G)'s job, wiring
// this package's LoginService/Sessions/Admission against the
// ReactorcideAuth CSIL service. Every store dependency here is a narrow,
// consumer-defined interface (this repo's convention — see
// handlers/project_handler.go, worker/secret_authorization.go) satisfied
// by *postgres_store.PostgresDbStore in production and by hand-rolled fakes
// in this package's tests.
package auth

import (
	"context"
	"errors"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/config"
)

// Mode is a validated UI auth mode. Values mirror config.UIAuthMode*
// 1:1 — see config.ValidateUIAuthMode, which must be called (and must
// succeed) before CurrentMode/NewBackend are used in anger.
type Mode string

const (
	ModeNone    Mode = Mode(config.UIAuthModeNone)
	ModeLocalRP Mode = Mode(config.UIAuthModeLocalRP)
	ModeRP      Mode = Mode(config.UIAuthModeRP)
)

// CurrentMode returns the configured Mode from config.UIAuthMode. Callers
// should have already called config.ValidateUIAuthMode() at startup; this
// does not re-validate and returns whatever string is configured, cast to
// Mode, even if it's not one of the three recognized values.
func CurrentMode() Mode {
	return Mode(config.UIAuthMode)
}

// Sentinel errors returned by LoginBackend implementations and LoginService.
var (
	// ErrLoginDisabled is returned by every LoginBackend method on the
	// "none"-mode sentinel backend, and by LoginService.StartLogin /
	// FinishLogin when the configured mode is none — callers can detect
	// "login is off" uniformly without special-casing a nil backend.
	ErrLoginDisabled = errors.New("auth: login is disabled (REACTORCIDE_UI_AUTH_MODE=none)")

	// ErrNotAdmitted is returned when a login (pre-check on the requested
	// selector, or post-check on the verified identity) does not match any
	// trusted-identity row or trusted-domain-pattern regex.
	ErrNotAdmitted = errors.New("auth: identity is not on the admission list")

	// ErrAttemptExpired is returned by LoginService.FinishLogin when the
	// consumed login attempt's expires_at has already passed.
	ErrAttemptExpired = errors.New("auth: login attempt has expired")

	// ErrAssertionNotVerified is returned by RPBackend.CompleteLogin when
	// Rp/verify-assertion round-trips successfully but reports
	// verified=false — a nil transport error alone must never be treated
	// as a trustworthy assertion (see example.md's step 5).
	ErrAssertionNotVerified = errors.New("auth: rp assertion did not verify against the issuing domain's published keys")
)

// VerifiedIdentity is the backend-agnostic result of a completed login:
// protocol facts only. Session creation, user provisioning, and
// authorization decisions are LoginService's job, never a LoginBackend's —
// mirroring the LinkKeys SDK's own "SDKs must not own application storage,
// sessions, database writes, or local user authorization" boundary.
type VerifiedIdentity struct {
	// Subject is the LinkKeys subject identifier: a uuid or handle, unique
	// within Domain (local-rp: VerifiedLocalLogin.UserID; rp:
	// IdentityAssertion.UserId).
	Subject string
	Domain  string
	// Handle is the verified "handle" claim, when the backend returned one
	// (local-rp: always requested by default; rp: fetched via
	// userinfo-fetch, best-effort). May be empty.
	Handle string
	// DisplayName is the verified display name, when available.
	DisplayName string
	// Claims is the raw claim-type -> claim-value map the backend
	// returned, for whatever the caller wants beyond handle/display_name
	// (e.g. "email").
	Claims map[string]string
}

// LoginBackend is the common interface both LinkKeys login modes
// (local-rp, rp) implement, plus a "none"-mode sentinel (NewNoneBackend)
// so callers never need a nil check.
type LoginBackend interface {
	// Mode reports which auth mode this backend implements.
	Mode() Mode
	// BeginLogin starts a login for identitySelector (a "[handle@]domain"
	// selector — see ParseSelector; the domain is where the browser gets
	// redirected, the handle if present is passed as a best-effort user
	// hint) and callbackURL (this app's own callback route, passed through
	// to the identity provider). Returns the URL to redirect the user's
	// browser to, and an opaque blob the caller must persist and pass
	// unchanged, once, to CompleteLogin.
	BeginLogin(ctx context.Context, identitySelector, callbackURL string) (redirectURL string, pendingBlob []byte, err error)
	// CompleteLogin verifies the callback the browser arrived with
	// (arrivedURL: the full URL the request actually arrived at, including
	// whatever query parameters the identity provider appended) against
	// the pendingBlob BeginLogin returned. Returns verified protocol facts
	// only — never a session, never a store write.
	CompleteLogin(ctx context.Context, pendingBlob []byte, arrivedURL string) (*VerifiedIdentity, error)
}

// noneBackend is the sentinel LoginBackend for ModeNone: every call fails
// with ErrLoginDisabled.
type noneBackend struct{}

// NewNoneBackend returns the sentinel LoginBackend for
// REACTORCIDE_UI_AUTH_MODE=none: every method returns ErrLoginDisabled, so
// callers (e.g. LoginService, or a CSIL op implementation in Wave 3) can
// treat "no backend configured" uniformly instead of nil-checking.
func NewNoneBackend() LoginBackend { return noneBackend{} }

func (noneBackend) Mode() Mode { return ModeNone }

func (noneBackend) BeginLogin(context.Context, string, string) (string, []byte, error) {
	return "", nil, ErrLoginDisabled
}

func (noneBackend) CompleteLogin(context.Context, []byte, string) (*VerifiedIdentity, error) {
	return nil, ErrLoginDisabled
}
