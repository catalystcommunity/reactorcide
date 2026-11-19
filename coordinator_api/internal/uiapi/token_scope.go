package uiapi

import (
	"context"
	"strings"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/authz"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/tokencaps"
)

// A browser session and an API token are equal callers here. The API
// enforces every permission; the webapp is only one more client. A token
// caller gets the role of its owner (a user, or a service principal)
// intersected with the token: the token must cover the organization, and it
// must have the capability of the operation.
//
// The role checks of authz.Resolver (RequireOrgAdmin, RequireGlobalAdmin,
// RequireProjectOwner) do not read the capabilities of a user-delegated
// token. Each operation that accepts a token and uses one of these role
// checks must also call requireTokenGrant with the capability of the
// operation.

// requireTokenGrant refuses an API token caller when the token does not
// cover orgID (nil: every organization, so the token must have all
// organizations) or has none of capabilities. A session caller has no token
// and always passes: its role decides.
func requireTokenGrant(id authz.Identity, orgID *string, capabilities ...string) error {
	if id.Token == nil {
		return nil
	}
	if orgID == nil {
		if !id.Token.AllOrganizations {
			return NewServiceError("forbidden", "the API token does not cover every organization")
		}
	} else if !id.Token.HasOrganization(*orgID) {
		return NewServiceError("forbidden", "the API token does not cover this organization")
	}
	for _, capability := range capabilities {
		if id.Token.HasCapability(capability) {
			return nil
		}
	}
	return NewServiceError("forbidden", "the API token does not have the "+strings.Join(capabilities, " or ")+" capability")
}

// tokenHasNoRoles reports whether id is a token whose capabilities alone
// decide (an instance, service, or job token with no service principal).
func tokenHasNoRoles(id authz.Identity) bool {
	return id.Token != nil && id.UserID == "" && id.PrincipalID == ""
}

// roleIdentity is id without its token: the role checks of the resolver
// then decide on the roles of the owner only. The token was checked
// before by requireTokenGrant. Without this, the resolver would also ask a
// service principal token for organizations:manage, so a read with
// organizations:read could never pass.
func roleIdentity(id authz.Identity) authz.Identity {
	if id.Token == nil {
		return id
	}
	return authz.Identity{UserID: id.UserID, PrincipalID: id.PrincipalID}
}

// requireGlobalAdminGrant requires a global admin. A token caller also needs
// a token for every organization with one of capabilities. With no
// capabilities given, organizations:manage (the global admin capability) is
// required. An instance token with no owner is decided by its capabilities,
// as REST principalAllowsGlobal does.
func (s *UiService) requireGlobalAdminGrant(ctx context.Context, id authz.Identity, capabilities ...string) error {
	if len(capabilities) == 0 {
		capabilities = []string{tokencaps.OrganizationsManage}
	}
	if err := requireTokenGrant(id, nil, capabilities...); err != nil {
		return err
	}
	if tokenHasNoRoles(id) {
		if id.Token.CredentialType == "instance_token" {
			return nil
		}
		// A legacy service token: the old rule (organizations:manage).
		if err := s.deps.Resolver.RequireGlobalAdmin(ctx, id); err != nil {
			return mapPermissionErr(err)
		}
		return nil
	}
	if err := s.deps.Resolver.RequireGlobalAdmin(ctx, roleIdentity(id)); err != nil {
		return mapPermissionErr(err)
	}
	return nil
}

// requireOrgAdminGrant requires an admin of orgID. A token caller also needs
// one of capabilities for orgID. A token with no owner is decided by its
// capabilities, as REST principalAllowsOrg does.
func (s *UiService) requireOrgAdminGrant(ctx context.Context, id authz.Identity, orgID string, capabilities ...string) error {
	if err := requireTokenGrant(id, &orgID, capabilities...); err != nil {
		return err
	}
	if tokenHasNoRoles(id) {
		return nil
	}
	if err := s.deps.Resolver.RequireOrgAdmin(ctx, roleIdentity(id), orgID); err != nil {
		return mapPermissionErr(err)
	}
	return nil
}

// orgReadCapabilities are the capabilities of an organization read: the read
// capability or the manage capability (owner decision: reads use
// organizations:read).
var orgReadCapabilities = []string{tokencaps.OrganizationsRead, tokencaps.OrganizationsManage}

// actorUserID is the user ID to record as the author of a change: the user
// of a session or of a user-delegated token, or nil for a token with no user
// (a service principal or an instance token).
func actorUserID(id authz.Identity) *string {
	if id.UserID == "" {
		return nil
	}
	value := id.UserID
	return &value
}
