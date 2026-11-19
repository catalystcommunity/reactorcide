package authz

import (
	"context"
	"errors"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/checkauth"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/tokencaps"
)

// ErrPrincipalInactive is returned when the principal of a token is disabled,
// merged, or missing.
var ErrPrincipalInactive = errors.New("authz: the principal of the token is not active")

// principalLookup is the optional store surface that reads a principal.
type principalLookup interface {
	GetPrincipal(ctx context.Context, principalID string) (*models.Principal, error)
}

// readCapabilities is what an organization member role allows: read, and
// the post of diagnosis entries (docs/diagnosis.md). A diagnosis entry only
// adds text that is labelled with its author; it changes no run.
var readCapabilities = []string{
	tokencaps.OrganizationsRead, tokencaps.ProjectsRead, tokencaps.JobsRead,
	tokencaps.LogsRead, tokencaps.WorkflowsRead, tokencaps.DiagnosisWrite,
}

// NarrowServicePrincipalToken applies the roles of a service principal to
// its token, so that every handler that checks the token scope directly
// (HasOrganization, HasCapability) gets the intersection of the token
// capabilities and the principal roles.
//
// The rules (fail closed):
//   - A disabled, merged, or missing principal makes the token invalid.
//   - A global admin role keeps the token scope.
//   - Otherwise the organizations of the token are only the organizations
//     where the principal has an organization role (admin or member).
//   - The capabilities stay as issued when the principal is an admin of every
//     remaining organization. Otherwise they are only the read capabilities
//     and diagnosis:write.
//   - Project roles do not widen the token scope here. The authz resolver
//     honors them where a decision names the project.
//
// The function changes p in place. It does nothing for other credentials.
func (r *Resolver) NarrowServicePrincipalToken(ctx context.Context, p *checkauth.Principal) error {
	if !p.IsServicePrincipalToken() {
		return nil
	}
	p.RecordIssuedCapabilities()
	if lookup, ok := r.store.(principalLookup); ok {
		record, err := lookup.GetPrincipal(ctx, p.PrincipalID)
		if err != nil || record == nil || !record.IsActive() || record.PrincipalID != p.PrincipalID ||
			record.Kind != models.PrincipalKindService {
			return ErrPrincipalInactive
		}
	}
	loaded, err := r.loadFor(ctx, Identity{PrincipalID: p.PrincipalID})
	if err != nil {
		return err
	}
	if loaded.hasGlobalAdmin() {
		return nil
	}
	candidates := map[string]bool{}
	for _, assignment := range loaded.assignments {
		if assignment.ScopeType == models.ScopeTypeOrg && assignment.ScopeID != nil {
			candidates[*assignment.ScopeID] = true
		}
	}
	allowed := []string{}
	allAdmin := true
	for orgID := range candidates {
		if !p.HasOrganization(orgID) {
			continue
		}
		allowed = append(allowed, orgID)
		if !loaded.hasOrgRole(orgID, models.RoleAdmin) {
			allAdmin = false
		}
	}
	p.AllOrganizations = false
	p.OrganizationIDs = allowed
	if p.OwnerOrgID != "" && !candidates[p.OwnerOrgID] {
		p.OwnerOrgID = ""
	}
	if len(allowed) == 0 {
		p.AllCapabilities = false
		p.Capabilities, _ = tokencaps.New()
		return nil
	}
	if allAdmin {
		return nil
	}
	issued := tokenCapabilities(Identity{Token: p})
	narrowed := []string{}
	for _, capability := range readCapabilities {
		if issued.Has(capability) {
			narrowed = append(narrowed, capability)
		}
	}
	p.AllCapabilities = false
	p.Capabilities, _ = tokencaps.New(narrowed...)
	return nil
}
