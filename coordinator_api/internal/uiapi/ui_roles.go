package uiapi

import (
	"context"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/authz"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/uiapi/csilapi"
)

func validScopeType(s string) bool {
	switch s {
	case models.ScopeTypeGlobal, models.ScopeTypeOrg, models.ScopeTypeProject:
		return true
	default:
		return false
	}
}

func validPrincipalType(s string) bool {
	return s == models.PrincipalTypeUser || s == models.PrincipalTypeGroup
}

func validRole(s string) bool {
	switch s {
	case models.RoleAdmin, models.RoleOwner, models.RoleMember:
		return true
	default:
		return false
	}
}

// authorizeRoleScope requires org admin (of the scope's owning org) or
// global admin to manage a role assignment at (scopeType, scopeID) —
// UI_AUTH_PLAN.md's "manage groups / assign roles" matrix row is org
// admin/global admin only at every scope, including project scope (a plain
// project owner may not grant themselves or others further roles).
// authorizeRoleScope always returns either nil or an already-mapped
// *ServiceErr (invalid_argument/not_found/forbidden/internal), so callers
// never need to distinguish a validation failure from a permission failure
// from a store failure themselves.
func (s *UiService) authorizeRoleScope(ctx context.Context, id authz.Identity, scopeType string, scopeID *string) error {
	switch scopeType {
	case models.ScopeTypeGlobal:
		return mapPermissionErr(s.deps.Resolver.RequireGlobalAdmin(ctx, id))
	case models.ScopeTypeOrg:
		if scopeID == nil || *scopeID == "" {
			return NewServiceError("invalid_argument", "scope_id is required for scope_type=org")
		}
		return mapPermissionErr(s.deps.Resolver.RequireOrgAdmin(ctx, id, *scopeID))
	case models.ScopeTypeProject:
		if scopeID == nil || *scopeID == "" {
			return NewServiceError("invalid_argument", "scope_id is required for scope_type=project")
		}
		project, err := s.deps.Store.GetProjectByID(ctx, *scopeID)
		if err != nil {
			return mapStoreErr(err, "project not found")
		}
		if project.UserID == nil {
			return mapPermissionErr(s.deps.Resolver.RequireGlobalAdmin(ctx, id))
		}
		return mapPermissionErr(s.deps.Resolver.RequireOrgAdmin(ctx, id, *project.UserID))
	default:
		return NewServiceError("invalid_argument", "scope_type must be one of global, org, project")
	}
}

func roleAssignmentToCsil(a *models.RoleAssignment) csilapi.RoleAssignment {
	return csilapi.RoleAssignment{
		AssignmentId:  a.AssignmentID,
		PrincipalType: a.PrincipalType,
		PrincipalId:   a.PrincipalID,
		ScopeType:     a.ScopeType,
		ScopeId:       a.ScopeID,
		Role:          a.Role,
		CreatedAt:     formatTime(a.CreatedAt),
		CreatedBy:     a.CreatedBy,
	}
}

// ListRoleAssignments requires scope_type (a bare "list every assignment"
// with no scope is not supported — see UI_AUTH_PLAN.md task G's "keep it
// simple" guidance) and lists (optionally further filtered by
// principal_id) the assignments at that scope. Authorization mirrors
// authorizeRoleScope: viewing a scope's assignments requires the same
// org-admin/global-admin capability managing it does.
func (s *UiService) ListRoleAssignments(ctx context.Context, req csilapi.ListRoleAssignmentsRequest) (csilapi.ListRoleAssignmentsResponse, error) {
	id, _, authErr := s.deps.requireUser(ctx)
	if authErr != nil {
		return csilapi.ListRoleAssignmentsResponse{}, authErr
	}
	if req.ScopeType == nil || *req.ScopeType == "" {
		return csilapi.ListRoleAssignmentsResponse{}, NewServiceError("invalid_argument", "scope_type is required")
	}
	if !validScopeType(*req.ScopeType) {
		return csilapi.ListRoleAssignmentsResponse{}, NewServiceError("invalid_argument", "scope_type must be one of global, org, project")
	}
	if err := s.authorizeRoleScope(ctx, id, *req.ScopeType, req.ScopeId); err != nil {
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
		out = append(out, roleAssignmentToCsil(&assignments[i]))
	}
	return csilapi.ListRoleAssignmentsResponse{Assignments: out}, nil
}

// AssignRole grants a role to a principal at a scope. Requires org
// admin/global admin of the target scope (see authorizeRoleScope).
func (s *UiService) AssignRole(ctx context.Context, req csilapi.AssignRoleRequest) (csilapi.AssignRoleResponse, error) {
	id, user, authErr := s.deps.requireUser(ctx)
	if authErr != nil {
		return csilapi.AssignRoleResponse{}, authErr
	}
	if !validPrincipalType(req.PrincipalType) {
		return csilapi.AssignRoleResponse{}, NewServiceError("invalid_argument", "principal_type must be one of user, group")
	}
	if err := requireNonEmpty("principal_id", req.PrincipalId, 64); err != nil {
		return csilapi.AssignRoleResponse{}, err
	}
	if !validScopeType(req.ScopeType) {
		return csilapi.AssignRoleResponse{}, NewServiceError("invalid_argument", "scope_type must be one of global, org, project")
	}
	if !validRole(req.Role) {
		return csilapi.AssignRoleResponse{}, NewServiceError("invalid_argument", "role must be one of admin, owner, member")
	}

	if err := s.authorizeRoleScope(ctx, id, req.ScopeType, req.ScopeId); err != nil {
		return csilapi.AssignRoleResponse{}, err
	}

	if req.PrincipalType == models.PrincipalTypeUser {
		if _, err := s.deps.Store.GetUserByID(ctx, req.PrincipalId); err != nil {
			return csilapi.AssignRoleResponse{}, NewServiceError("invalid_argument", "principal_id does not refer to a known user")
		}
	} else {
		if _, err := s.deps.Store.GetGroupByID(ctx, req.PrincipalId); err != nil {
			return csilapi.AssignRoleResponse{}, NewServiceError("invalid_argument", "principal_id does not refer to a known group")
		}
	}

	createdBy := user.UserID
	assignment := &models.RoleAssignment{
		PrincipalType: req.PrincipalType,
		PrincipalID:   req.PrincipalId,
		ScopeType:     req.ScopeType,
		ScopeID:       req.ScopeId,
		Role:          req.Role,
		CreatedBy:     &createdBy,
	}
	if err := s.deps.Store.CreateRoleAssignment(ctx, assignment); err != nil {
		return csilapi.AssignRoleResponse{}, NewServiceError("internal", "failed to create role assignment")
	}
	return csilapi.AssignRoleResponse{Assignment: roleAssignmentToCsil(assignment)}, nil
}

// RevokeRole deletes a role assignment by ID. Requires org admin/global
// admin of the assignment's own scope (loaded first, since the request
// carries only the assignment ID).
func (s *UiService) RevokeRole(ctx context.Context, req csilapi.RevokeRoleRequest) (csilapi.RevokeRoleResponse, error) {
	id, _, authErr := s.deps.requireUser(ctx)
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
	if err := s.authorizeRoleScope(ctx, id, assignment.ScopeType, assignment.ScopeID); err != nil {
		return csilapi.RevokeRoleResponse{}, err
	}

	if err := s.deps.Store.DeleteRoleAssignment(ctx, req.AssignmentId); err != nil {
		return csilapi.RevokeRoleResponse{}, mapStoreErr(err, "role assignment not found")
	}
	return csilapi.RevokeRoleResponse{Revoked: true}, nil
}
