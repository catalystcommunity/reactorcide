// Package authz is the role-resolution, capability-computation, and
// visibility-filtering core for the reactorcide management UI. It implements
// the identity/RBAC model, the permission matrix, and resource visibility.
//
// This package deliberately knows nothing about HTTP, CSIL-RPC, or session
// tokens — callers (REST handlers today, the CSIL UI service in Wave 3) are
// responsible for resolving a caller down to an authz.Identity (see
// IdentityFromUser) and passing it in on every call. Every store dependency
// here is a narrow, consumer-defined interface (this repo's convention — see
// handlers/project_handler.go, worker/secret_authorization.go) satisfied by
// *postgres_store.PostgresDbStore in production and by hand-rolled fakes in
// this package's tests.
package authz

import (
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/checkauth"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/tokencaps"
)

// Identity is the caller identity every authz decision is made against.
// Anonymous identities carry no UserID; the zero value is anonymous (safe
// default: no privileges).
type Identity struct {
	// Anonymous is true for callers with no resolved session/user (a
	// not-logged-in browser in local-rp/rp auth mode, or any caller at all
	// when REACTORCIDE_UI_AUTH_MODE=none since there is no session machinery
	// in that mode).
	Anonymous bool
	// UserID is the resolved user's primary key. Only meaningful when
	// Anonymous is false.
	UserID string
	// Token is non-nil for a classified API-token subject. It is set for
	// user-delegated tokens too, so live RBAC can be intersected with token
	// scope on each request.
	Token *checkauth.Principal
}

func IdentityFromPrincipal(principal *checkauth.Principal, user *models.User) Identity {
	if principal == nil {
		return IdentityFromUser(user)
	}
	if principal.CredentialType == "user_token" {
		if user == nil || !user.IsActive() || user.UserID == "" || user.UserID != principal.UserID {
			return AnonymousIdentity()
		}
		return Identity{UserID: user.UserID, Token: principal}
	}
	if principal.CredentialType == "instance_token" || principal.CredentialType == "service_token" || principal.CredentialType == "job_token" {
		return Identity{Token: principal}
	}
	return AnonymousIdentity()
}

func (id Identity) isToken() bool { return id.Token != nil }

func (id Identity) tokenAllows(orgID, capability string) bool {
	return id.Token != nil && id.Token.HasOrganization(orgID) && id.Token.HasCapability(capability)
}

func (id Identity) tokenAllowsGlobal(capability string) bool {
	return id.Token != nil && id.Token.AllOrganizations && id.Token.HasCapability(capability)
}

func tokenCapabilities(id Identity) tokencaps.Set {
	if id.Token == nil {
		return nil
	}
	if id.Token.AllCapabilities {
		result, _ := tokencaps.New(tokencaps.Values()...)
		return result
	}
	return id.Token.Capabilities
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
// (*models.User, possibly nil) to an Identity. Convenience for REST handlers,
// which resolve a *models.User (never a bare session token) today. A nil user
// maps to AnonymousIdentity so callers never need a separate nil check before
// calling into this package.
func IdentityFromUser(user *models.User) Identity {
	if user == nil || user.UserID == "" {
		return AnonymousIdentity()
	}
	return UserIdentity(user.UserID)
}
