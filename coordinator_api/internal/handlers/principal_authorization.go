package handlers

import (
	"context"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/authz"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/checkauth"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/tokencaps"
)

// principalAllowsOrg intersects a delegated token with the user's current
// organization role. Other token subjects have no live user authority to
// intersect.
func principalAllowsOrg(ctx context.Context, appStore store.Store, orgID, capability string) bool {
	principal := checkauth.GetPrincipalFromContext(ctx)
	if principal == nil || !principal.HasOrganization(orgID) || !principal.HasCapability(capability) {
		return false
	}
	if principal.CredentialType != "user_token" {
		return true
	}
	roleStore, ok := appStore.(authz.RoleStore)
	user := checkauth.GetUserFromContext(ctx)
	if !ok || user == nil || !user.IsActive() {
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
	if principal.CredentialType != "user_token" {
		return principal.CredentialType == "instance_token"
	}
	roleStore, ok := appStore.(authz.RoleStore)
	user := checkauth.GetUserFromContext(ctx)
	if !ok || user == nil || !user.IsActive() {
		return false
	}
	allowed, err := authz.NewResolver(roleStore).IsGlobalAdmin(ctx, authz.IdentityFromPrincipal(principal, user))
	return err == nil && allowed
}
