package authz

import (
	"context"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/tokencaps"
)

// CanPostDiagnosis reports whether id can post a diagnosis entry on a job or
// a workflow of orgID (and projectID, when it has one). See
// docs/diagnosis.md.
//
// The rules (fail closed):
//   - An anonymous caller and a job token can never post. A public
//     repository lets anyone read logs, but a post needs a principal.
//   - A token needs the organization in its scope and the capability
//     diagnosis:write.
//   - A token with no live roles (the admin token, a service or instance
//     token made before principals) has only its capabilities.
//   - A user or a service principal also needs a role in the scope: a
//     global admin role, any organization role (admin or member) in orgID,
//     any project role in projectID, or the organization of the user
//     itself. The token capability narrows the role; it never widens it.
//
// The caller must also be able to view the job or the workflow. The REST
// handler and the CSIL operation check that first.
func (r *Resolver) CanPostDiagnosis(ctx context.Context, id Identity, orgID string, projectID *string) (bool, error) {
	if id.Anonymous || orgID == "" {
		return false, nil
	}
	if id.Token != nil {
		if id.Token.CredentialType == "job_token" {
			return false, nil
		}
		if !id.tokenAllows(orgID, tokencaps.DiagnosisWrite) {
			return false, nil
		}
	}
	if !id.hasRoles() {
		return id.Token != nil, nil
	}
	if id.UserID != "" && id.UserID == orgID {
		return true, nil
	}
	p, err := r.loadFor(ctx, id)
	if err != nil {
		return false, err
	}
	if p.hasGlobalAdmin() || p.hasAnyOrgRole(orgID) {
		return true, nil
	}
	if projectID != nil && *projectID != "" && p.hasAnyProjectRole(*projectID) {
		return true, nil
	}
	return false, nil
}
