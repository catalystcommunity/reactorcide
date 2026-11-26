package handlers

import (
	"context"
	"net/http"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/authz"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/checkauth"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/tokencaps"
)

// principalAllowsOrg intersects a delegated token with the user's current
// organization role, and a service principal token with the principal's
// current roles. Other token subjects have no live authority to intersect.
func principalAllowsOrg(ctx context.Context, appStore store.Store, orgID, capability string) bool {
	principal := checkauth.GetPrincipalFromContext(ctx)
	if principal == nil || !principal.HasOrganization(orgID) || !principal.HasCapability(capability) {
		return false
	}
	if !principal.RolesApply() {
		return true
	}
	roleStore, ok := appStore.(authz.RoleStore)
	if !ok {
		return false
	}
	user := checkauth.GetUserFromContext(ctx)
	if !principal.IsServicePrincipalToken() && (user == nil || !user.IsActive()) {
		return false
	}
	resolver := authz.NewResolver(roleStore)
	identity := authz.IdentityFromPrincipal(principal, user)
	if capability == tokencaps.OrganizationsRead || capability == tokencaps.ProjectsRead ||
		capability == tokencaps.JobsRead || capability == tokencaps.LogsRead || capability == tokencaps.WorkflowsRead {
		role, err := resolver.EffectiveRoleForOrg(ctx, identity, orgID)
		return err == nil && role != ""
	}
	allowed, err := resolver.IsOrgAdmin(ctx, identity, orgID)
	return err == nil && allowed
}

func principalAllowsGlobal(ctx context.Context, appStore store.Store, capability string) bool {
	principal := checkauth.GetPrincipalFromContext(ctx)
	if principal == nil || !principal.AllOrganizations || !principal.HasCapability(capability) {
		return false
	}
	if !principal.RolesApply() {
		return principal.CredentialType == "instance_token"
	}
	roleStore, ok := appStore.(authz.RoleStore)
	if !ok {
		return false
	}
	user := checkauth.GetUserFromContext(ctx)
	if !principal.IsServicePrincipalToken() && (user == nil || !user.IsActive()) {
		return false
	}
	allowed, err := authz.NewResolver(roleStore).IsGlobalAdmin(ctx, authz.IdentityFromPrincipal(principal, user))
	return err == nil && allowed
}

// readOrManage is the token capability of a request: organizations:read
// (or organizations:manage) for a GET, organizations:manage otherwise.
func readOrManage(r *http.Request) []string {
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		return []string{tokencaps.OrganizationsRead, tokencaps.OrganizationsManage}
	}
	return []string{tokencaps.OrganizationsManage}
}

// principalHasAny reports whether the token has one of capabilities.
func principalHasAny(principal *checkauth.Principal, capabilities []string) bool {
	for _, capability := range capabilities {
		if principal.HasCapability(capability) {
			return true
		}
	}
	return false
}

// principalAllowsOrgAdmin requires an organization admin role (as
// principalAllowsOrg does for a manage capability) and one of capabilities
// in the token. A read with organizations:read keeps the admin role
// requirement; only the token capability is the read one.
func principalAllowsOrgAdmin(ctx context.Context, appStore store.Store, orgID string, capabilities []string) bool {
	principal := checkauth.GetPrincipalFromContext(ctx)
	if principal == nil || !principal.HasOrganization(orgID) || !principalHasAny(principal, capabilities) {
		return false
	}
	if !principal.RolesApply() {
		return true
	}
	roleStore, ok := appStore.(authz.RoleStore)
	if !ok {
		return false
	}
	user := checkauth.GetUserFromContext(ctx)
	if !principal.IsServicePrincipalToken() && (user == nil || !user.IsActive()) {
		return false
	}
	// The role only: the token capability was checked above.
	identity := authz.IdentityFromPrincipal(principal, user)
	allowed, err := authz.NewResolver(roleStore).IsOrgAdmin(ctx, authz.Identity{UserID: identity.UserID, PrincipalID: identity.PrincipalID}, orgID)
	return err == nil && allowed
}

// principalAllowsGlobalAdmin is principalAllowsGlobal with one of
// capabilities in the token and a global admin role.
func principalAllowsGlobalAdmin(ctx context.Context, appStore store.Store, capabilities []string) bool {
	principal := checkauth.GetPrincipalFromContext(ctx)
	if principal == nil || !principal.AllOrganizations || !principalHasAny(principal, capabilities) {
		return false
	}
	if !principal.RolesApply() {
		return principal.CredentialType == "instance_token"
	}
	roleStore, ok := appStore.(authz.RoleStore)
	if !ok {
		return false
	}
	user := checkauth.GetUserFromContext(ctx)
	if !principal.IsServicePrincipalToken() && (user == nil || !user.IsActive()) {
		return false
	}
	identity := authz.IdentityFromPrincipal(principal, user)
	allowed, err := authz.NewResolver(roleStore).IsGlobalAdmin(ctx, authz.Identity{UserID: identity.UserID, PrincipalID: identity.PrincipalID})
	return err == nil && allowed
}

// requireGlobalAdmin is the middleware of instance administration routes
// (the master keys). It accepts a global token with capability (the admin
// token, or a global service principal token), or a user with the admin role.
// A global token needs no user.
func requireGlobalAdmin(appStore store.Store, capability string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if principalAllowsGlobal(r.Context(), appStore, capability) || userHasAdminRole(r.Context(), capability) {
				next.ServeHTTP(w, r)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			w.Write([]byte(`{"error":"forbidden","message":"Requires the admin token, a global token with ` + capability + `, or a user with the admin role"}`))
		})
	}
}

// userHasAdminRole reports whether the request has an active user with the
// admin role. A token of that user must also have capability.
func userHasAdminRole(ctx context.Context, capability string) bool {
	user := checkauth.GetUserFromContext(ctx)
	if user == nil || !user.IsActive() {
		return false
	}
	if principal := checkauth.GetPrincipalFromContext(ctx); principal != nil && !principal.HasCapability(capability) {
		return false
	}
	for _, role := range user.Roles {
		if role == "admin" {
			return true
		}
	}
	return false
}
