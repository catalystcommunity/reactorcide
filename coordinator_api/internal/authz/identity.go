// Package authz is the role-resolution, capability-computation, and
// visibility-filtering core for the reactorcide management UI (Task D). It
// implements the "Identity & RBAC model", the permission matrix, and the
// "Visibility" section of UI_AUTH_PLAN.md.
//
// This package deliberately knows nothing about HTTP, CSIL-RPC, or session
// tokens — callers (REST handlers today, the CSIL UI service in Wave 3) are
// responsible for resolving a caller down to an authz.Identity (see
// IdentityFromUser) and passing it in on every call. Every store dependency
// here is a narrow, consumer-defined interface (this repo's convention —
// see handlers/project_handler.go, worker/secret_authorization.go)
// satisfied by *postgres_store.PostgresDbStore in production and by
// hand-rolled fakes in this package's tests.
package authz

import "github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"

// Identity is the caller identity every authz decision is made against.
// Anonymous identities carry no UserID; the zero value is anonymous (safe
// default: no privileges).
type Identity struct {
	// Anonymous is true for callers with no resolved session/user (a
	// not-logged-in browser in local-rp/rp auth mode, or any caller at all
	// when REACTORCIDE_UI_AUTH_MODE=none since there is no session
	// machinery in that mode).
	Anonymous bool
	// UserID is the resolved user's primary key. Only meaningful when
	// Anonymous is false.
	UserID string
}

// AnonymousIdentity returns the identity for a caller with no resolved
// session or user.
func AnonymousIdentity() Identity {
	return Identity{Anonymous: true}
}

// UserIdentity returns the identity for a resolved, logged-in user.
func UserIdentity(userID string) Identity {
	return Identity{UserID: userID}
}

// IdentityFromUser adapts the legacy checkauth/API-token identity
// (*models.User, possibly nil) to an Identity. Convenience for REST
// handlers, which resolve a *models.User (never a bare session token)
// today. A nil user maps to AnonymousIdentity so callers never need a
// separate nil check before calling into this package.
func IdentityFromUser(user *models.User) Identity {
	if user == nil || user.UserID == "" {
		return AnonymousIdentity()
	}
	return UserIdentity(user.UserID)
}
