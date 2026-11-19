package authz

import (
	"context"
	"fmt"
	"strings"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/owners"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/subjects"
)

// Workflow-scoped roles (plan B5, docs/organizations-and-ci-policy.md).
//
// An owner of one workflow is:
//   - a holder of a workflow-scope role assignment (scope_type workflow,
//     scope_id the project, scope_workflow the workflow security ID), direct
//     or through a group; or
//   - a member of the owner group of the workflow, as the trusted owner
//     snapshot names it (reactorcide_group NAME in the workflow's org, or a
//     group linked to the vcs_team when the store supports it), or a
//     principal that an extra owner subject names (principal:UUID).
//
// A workflow owner gets workflowOwnerCaps for that one workflow. Nothing
// here changes a project or org decision.

// VCSTeamGroupStore is an optional store surface: the Reactorcide groups of
// an org that are linked to a VCS team ("ORG/SLUG"), plan B3. Without it, a
// vcs_team owner gives no authority in the UI (a VCS comment still proves
// team membership through the VCS).
type VCSTeamGroupStore interface {
	ListGroupsLinkedToVCSTeam(ctx context.Context, orgID, team string) ([]models.Group, error)
}

func (p *principal) hasWorkflowRole(projectID, securityID string) bool {
	for _, a := range p.assignments {
		if a.ScopeType == models.ScopeTypeWorkflow && a.Role == models.RoleOwner && a.ScopeID != nil && *a.ScopeID == projectID &&
			a.ScopeWorkflow != nil && *a.ScopeWorkflow == securityID {
			return true
		}
	}
	return false
}

func (p *principal) inGroup(orgID, name string) bool {
	for _, g := range p.groups {
		if g.OrgID == orgID && g.Name == name {
			return true
		}
	}
	return false
}

func (p *principal) inGroupID(groupID string) bool {
	for _, g := range p.groups {
		if g.GroupID == groupID {
			return true
		}
	}
	return false
}

// isWorkflowOwner reports whether the loaded principal p (of id) owns the
// workflow in scope.
func (r *Resolver) isWorkflowOwner(ctx context.Context, p *principal, id Identity, orgID, projectID string, scope WorkflowScope) (bool, error) {
	if scope.SecurityID == "" {
		return false, nil
	}
	if p.hasWorkflowRole(projectID, scope.SecurityID) {
		return true, nil
	}
	// Only a snapshot from a trusted ref gives authority (migration 000053).
	for _, subject := range owners.Subjects(scope.Owner.TrustedOwner()) {
		switch {
		case strings.HasPrefix(subject, subjects.PrefixGroup):
			if p.inGroup(orgID, strings.TrimPrefix(subject, subjects.PrefixGroup)) {
				return true, nil
			}
		case strings.HasPrefix(subject, subjects.PrefixVCSTeam):
			teams, ok := r.store.(VCSTeamGroupStore)
			if !ok {
				continue
			}
			groups, err := teams.ListGroupsLinkedToVCSTeam(ctx, orgID, strings.TrimPrefix(subject, subjects.PrefixVCSTeam))
			if err != nil {
				return false, fmt.Errorf("authz: loading groups of a VCS team: %w", err)
			}
			for _, g := range groups {
				if p.inGroupID(g.GroupID) {
					return true, nil
				}
			}
		case strings.HasPrefix(subject, subjects.PrefixPrincipal):
			principalID := strings.TrimPrefix(subject, subjects.PrefixPrincipal)
			if (id.UserID != "" && strings.EqualFold(id.UserID, principalID)) || (id.PrincipalID != "" && strings.EqualFold(id.PrincipalID, principalID)) {
				return true, nil
			}
		}
	}
	return false, nil
}

// IsWorkflowOwner reports whether id owns one workflow of a project (see
// above). Org admins and project owners are not "owners" here; callers that
// accept them use Capabilities(...).ManageWorkflow instead.
func (r *Resolver) IsWorkflowOwner(ctx context.Context, id Identity, projectID string, scope WorkflowScope) (bool, error) {
	if id.Anonymous || !id.hasRoles() || projectID == "" {
		return false, nil
	}
	project, err := r.store.GetProjectByID(ctx, projectID)
	if err != nil || project == nil {
		return false, err
	}
	orgID := project.OwnershipOrgID()
	if id.PrincipalID != "" && id.Token != nil && !id.Token.HasOrganization(orgID) {
		return false, nil
	}
	p, err := r.loadFor(ctx, id)
	if err != nil {
		return false, err
	}
	return r.isWorkflowOwner(ctx, p, id, orgID, projectID, scope)
}
