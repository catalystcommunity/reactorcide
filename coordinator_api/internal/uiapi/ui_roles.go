package uiapi

import (
	"context"
	"regexp"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/authz"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/tokencaps"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/uiapi/csilapi"
)

func validScopeType(s string) bool {
	switch s {
	case models.ScopeTypeGlobal, models.ScopeTypeOrg, models.ScopeTypeProject, models.ScopeTypeWorkflow:
		return true
	default:
		return false
	}
}

var workflowSecurityIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

func validPrincipalType(s string) bool {
	return s == models.PrincipalTypeUser || s == models.PrincipalTypeGroup || s == models.PrincipalTypePrincipal
}

func validRole(s string) bool {
	switch s {
	case models.RoleAdmin, models.RoleOwner, models.RoleMember:
		return true
	default:
		return false
	}
}

// authorizeRoleScope requires org admin (of the scope's owning org) or global
// admin to manage a role assignment at (scopeType, scopeID)).
// authorizeRoleScope always returns either nil or an already-mapped
// *ServiceErr (invalid_argument/not_found/forbidden/internal), so callers
// never need to distinguish a validation failure from a permission failure
// from a store failure themselves.
func (s *UiService) authorizeRoleScope(ctx context.Context, id authz.Identity, scopeType string, scopeID *string, capabilities ...string) error {
	switch scopeType {
	case models.ScopeTypeGlobal:
		return s.requireGlobalAdminGrant(ctx, id, capabilities...)
	case models.ScopeTypeOrg:
		if scopeID == nil || *scopeID == "" {
			return NewServiceError("invalid_argument", "scope_id is required for scope_type=org")
		}
		return s.requireOrgAdminGrant(ctx, id, *scopeID, capabilities...)
	case models.ScopeTypeProject, models.ScopeTypeWorkflow:
		// A workflow scope is managed like its project (scope_id is the
		// project): org admin of the owning org, or global admin.
		if scopeID == nil || *scopeID == "" {
			return NewServiceError("invalid_argument", "scope_id is required for scope_type="+scopeType)
		}
		project, err := s.deps.Store.GetProjectByID(ctx, *scopeID)
		if err != nil {
			return mapStoreErr(err, "project not found")
		}
		orgID := project.OwnershipOrgID()
		if orgID == "" {
			return s.requireGlobalAdminGrant(ctx, id, capabilities...)
		}
		return s.requireOrgAdminGrant(ctx, id, orgID, capabilities...)
	default:
		return NewServiceError("invalid_argument", "scope_type must be one of global, org, project, workflow")
	}
}

// validateScopeWorkflow checks scope_workflow against the scope type: a
// workflow scope needs a workflow security ID and the owner role; any other
// scope must not name a workflow.
func validateScopeWorkflow(scopeType string, scopeWorkflow *string, role string) error {
	value := ""
	if scopeWorkflow != nil {
		value = *scopeWorkflow
	}
	if scopeType != models.ScopeTypeWorkflow {
		if value != "" {
			return NewServiceError("invalid_argument", "scope_workflow is only valid with scope_type=workflow")
		}
		return nil
	}
	if !workflowSecurityIDPattern.MatchString(value) {
		return NewServiceError("invalid_argument", "scope_workflow must be a workflow security ID (the YAML id)")
	}
	if role != "" && role != models.RoleOwner {
		return NewServiceError("invalid_argument", "a workflow role is owner")
	}
	return nil
}

func roleAssignmentToCsil(a *models.RoleAssignment) csilapi.RoleAssignment {
	return csilapi.RoleAssignment{
		AssignmentId:  a.AssignmentID,
		PrincipalType: a.PrincipalType,
		PrincipalId:   a.PrincipalID,
		ScopeType:     a.ScopeType,
		ScopeId:       a.ScopeID,
		ScopeWorkflow: a.ScopeWorkflow,
		Role:          a.Role,
		CreatedAt:     formatTime(a.CreatedAt),
		CreatedBy:     a.CreatedBy,
	}
}

// ListRoleAssignments requires scope_type and lists (optionally further
// filtered by principal_id) the assignments at that scope. Authorization
// mirrors authorizeRoleScope: viewing a scope's assignments requires the same
// org-admin/global-admin capability managing it does.
func (s *UiService) ListRoleAssignments(ctx context.Context, req csilapi.ListRoleAssignmentsRequest) (csilapi.ListRoleAssignmentsResponse, error) {
	ctx, id, _, authErr := s.deps.requireManagementIdentity(ctx)
	if authErr != nil {
		return csilapi.ListRoleAssignmentsResponse{}, authErr
	}
	if req.ScopeType == nil || *req.ScopeType == "" {
		return csilapi.ListRoleAssignmentsResponse{}, NewServiceError("invalid_argument", "scope_type is required")
	}
	if !validScopeType(*req.ScopeType) {
		return csilapi.ListRoleAssignmentsResponse{}, NewServiceError("invalid_argument", "scope_type must be one of global, org, project, workflow")
	}
	if err := s.authorizeRoleScope(ctx, id, *req.ScopeType, req.ScopeId, orgReadCapabilities...); err != nil {
		return csilapi.ListRoleAssignmentsResponse{}, err
	}

	assignments, err := s.deps.Store.ListRoleAssignmentsByScope(ctx, *req.ScopeType, req.ScopeId)
	if err != nil {
		return csilapi.ListRoleAssignmentsResponse{}, NewServiceError("internal", "an internal error occurred")
	}

	out := make([]csilapi.RoleAssignment, 0, len(assignments))
	for i := range assignments {
		if req.PrincipalId != nil && *req.PrincipalId != "" && assignments[i].PrincipalID != *req.PrincipalId {
			continue
		}
		if req.ScopeWorkflow != nil && *req.ScopeWorkflow != "" &&
			(assignments[i].ScopeWorkflow == nil || *assignments[i].ScopeWorkflow != *req.ScopeWorkflow) {
			continue
		}
		out = append(out, roleAssignmentToCsil(&assignments[i]))
	}
	return csilapi.ListRoleAssignmentsResponse{Assignments: out}, nil
}

// AssignRole grants a role to a principal at a scope. Requires org
// admin/global admin of the target scope (see authorizeRoleScope).
func (s *UiService) AssignRole(ctx context.Context, req csilapi.AssignRoleRequest) (csilapi.AssignRoleResponse, error) {
	ctx, id, _, authErr := s.deps.requireManagementIdentity(ctx)
	if authErr != nil {
		return csilapi.AssignRoleResponse{}, authErr
	}
	if !validPrincipalType(req.PrincipalType) {
		return csilapi.AssignRoleResponse{}, NewServiceError("invalid_argument", "principal_type must be one of user, group, principal")
	}
	if err := requireNonEmpty("principal_id", req.PrincipalId, 64); err != nil {
		return csilapi.AssignRoleResponse{}, err
	}
	if !validScopeType(req.ScopeType) {
		return csilapi.AssignRoleResponse{}, NewServiceError("invalid_argument", "scope_type must be one of global, org, project, workflow")
	}
	if !validRole(req.Role) {
		return csilapi.AssignRoleResponse{}, NewServiceError("invalid_argument", "role must be one of admin, owner, member")
	}
	if err := validateScopeWorkflow(req.ScopeType, req.ScopeWorkflow, req.Role); err != nil {
		return csilapi.AssignRoleResponse{}, err
	}

	if err := s.authorizeRoleScope(ctx, id, req.ScopeType, req.ScopeId, tokencaps.OrganizationsManage); err != nil {
		return csilapi.AssignRoleResponse{}, err
	}

	if req.PrincipalType == models.PrincipalTypeUser {
		if _, err := s.deps.Store.GetUserByID(ctx, req.PrincipalId); err != nil {
			return csilapi.AssignRoleResponse{}, NewServiceError("invalid_argument", "principal_id does not refer to a known user")
		}
	} else if req.PrincipalType == models.PrincipalTypePrincipal {
		// Any principal (a service principal, or a human). The system
		// principal has no privileges and takes no role.
		ps, ok := s.deps.Store.(principalStore)
		if !ok || req.PrincipalId == models.SystemPrincipalID {
			return csilapi.AssignRoleResponse{}, NewServiceError("invalid_argument", "principal_id does not refer to a principal that can hold a role")
		}
		if principal, err := ps.GetPrincipal(ctx, req.PrincipalId); err != nil || principal.PrincipalID != req.PrincipalId {
			return csilapi.AssignRoleResponse{}, NewServiceError("invalid_argument", "principal_id does not refer to a known principal")
		}
	} else {
		if _, err := s.deps.Store.GetGroupByID(ctx, req.PrincipalId); err != nil {
			return csilapi.AssignRoleResponse{}, NewServiceError("invalid_argument", "principal_id does not refer to a known group")
		}
	}

	assignment := &models.RoleAssignment{
		PrincipalType: req.PrincipalType,
		PrincipalID:   req.PrincipalId,
		ScopeType:     req.ScopeType,
		ScopeID:       req.ScopeId,
		Role:          req.Role,
		CreatedBy:     actorUserID(id),
	}
	if req.ScopeType == models.ScopeTypeWorkflow {
		workflow := *req.ScopeWorkflow
		assignment.ScopeWorkflow = &workflow
	}
	if err := s.deps.Store.CreateRoleAssignment(ctx, assignment); err != nil {
		return csilapi.AssignRoleResponse{}, NewServiceError("internal", "failed to create role assignment")
	}
	orgID := auditOrgForRole(ctx, s, assignment)
	s.recordAudit(ctx, orgID, "role_assignment.create", "role_assignment", assignment.AssignmentID, models.JSONB{"principal_type": assignment.PrincipalType, "principal_id": assignment.PrincipalID, "scope_type": assignment.ScopeType, "scope_id": assignment.ScopeID, "scope_workflow": assignment.ScopeWorkflow, "role": assignment.Role})
	return csilapi.AssignRoleResponse{Assignment: roleAssignmentToCsil(assignment)}, nil
}

// RevokeRole deletes a role assignment by ID. Requires org admin/global admin
// of the assignment's own scope (loaded first, since the request carries only
// the assignment ID).
func (s *UiService) RevokeRole(ctx context.Context, req csilapi.RevokeRoleRequest) (csilapi.RevokeRoleResponse, error) {
	ctx, id, _, authErr := s.deps.requireManagementIdentity(ctx)
	if authErr != nil {
		return csilapi.RevokeRoleResponse{}, authErr
	}
	if err := requireNonEmpty("assignment_id", req.AssignmentId, 64); err != nil {
		return csilapi.RevokeRoleResponse{}, err
	}

	assignment, err := s.deps.Store.GetRoleAssignmentByID(ctx, req.AssignmentId)
	if err != nil {
		return csilapi.RevokeRoleResponse{}, mapStoreErr(err, "role assignment not found")
	}
	if err := s.authorizeRoleScope(ctx, id, assignment.ScopeType, assignment.ScopeID, tokencaps.OrganizationsManage); err != nil {
		return csilapi.RevokeRoleResponse{}, err
	}

	if err := s.deps.Store.DeleteRoleAssignment(ctx, req.AssignmentId); err != nil {
		return csilapi.RevokeRoleResponse{}, mapStoreErr(err, "role assignment not found")
	}
	s.recordAudit(ctx, auditOrgForRole(ctx, s, assignment), "role_assignment.delete", "role_assignment", assignment.AssignmentID, models.JSONB{"principal_type": assignment.PrincipalType, "principal_id": assignment.PrincipalID, "scope_type": assignment.ScopeType, "scope_id": assignment.ScopeID, "scope_workflow": assignment.ScopeWorkflow, "role": assignment.Role})
	return csilapi.RevokeRoleResponse{Revoked: true}, nil
}

func auditOrgForRole(ctx context.Context, s *UiService, assignment *models.RoleAssignment) string {
	if assignment.ScopeID == nil {
		return ""
	}
	if assignment.ScopeType == models.ScopeTypeOrg {
		return *assignment.ScopeID
	}
	if assignment.ScopeType == models.ScopeTypeProject || assignment.ScopeType == models.ScopeTypeWorkflow {
		if project, err := s.deps.Store.GetProjectByID(ctx, *assignment.ScopeID); err == nil {
			return project.OwnershipOrgID()
		}
	}
	return ""
}
