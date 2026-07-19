package authz

import (
	"context"
	"errors"
	"fmt"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

// RoleStore is the narrow store surface Resolver consumes: group
// membership + role-assignment lookups (Task A's
// postgres_store/rbac_operations.go), plus the user/project lookups
// visibility and org-membership checks need. Satisfied by
// *postgres_store.PostgresDbStore in production; consumers (REST handlers)
// type-assert their store.Store onto this interface (this repo's
// consumer-defined-narrow-interface convention — see
// handlers/project_handler.go, worker/secret_authorization.go) rather than
// this package importing postgres_store directly.
type RoleStore interface {
	GetUserByID(ctx context.Context, userID string) (*models.User, error)
	GetProjectByID(ctx context.Context, projectID string) (*models.Project, error)
	ListGroupsForUser(ctx context.Context, userID string) ([]models.Group, error)
	ListRoleAssignmentsForPrincipal(ctx context.Context, userID string, groupIDs []string) ([]models.RoleAssignment, error)
}

// Resolver resolves role assignments (direct + via group membership) into
// role-tier and capability decisions. Safe for concurrent use — it holds no
// mutable state beyond the store it was constructed with.
type Resolver struct {
	store RoleStore
}

// NewResolver constructs a Resolver backed by s.
func NewResolver(s RoleStore) *Resolver {
	return &Resolver{store: s}
}

// principal is every role_assignments row that applies to one user (direct
// user grants union'd with grants on any group the user belongs to —
// ListRoleAssignmentsForPrincipal already does that union query-side).
// Loading it once per top-level call (Capabilities, the visibility batch)
// and deriving every boolean from the same slice is this package's
// "per-request memoization" — see UI_AUTH_PLAN.md task D's brief. It is not
// cached across calls: Resolver itself holds no state, so role-assignment
// changes are always picked up on the next call.
type principal struct {
	userID      string
	assignments []models.RoleAssignment
}

func (r *Resolver) loadPrincipal(ctx context.Context, userID string) (*principal, error) {
	groups, err := r.store.ListGroupsForUser(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("authz: loading groups for user: %w", err)
	}
	groupIDs := make([]string, len(groups))
	for i, g := range groups {
		groupIDs[i] = g.GroupID
	}
	assignments, err := r.store.ListRoleAssignmentsForPrincipal(ctx, userID, groupIDs)
	if err != nil {
		return nil, fmt.Errorf("authz: loading role assignments: %w", err)
	}
	return &principal{userID: userID, assignments: assignments}, nil
}

func (p *principal) hasGlobalAdmin() bool {
	for _, a := range p.assignments {
		if a.ScopeType == models.ScopeTypeGlobal && a.Role == models.RoleAdmin {
			return true
		}
	}
	return false
}

func (p *principal) hasOrgRole(orgID, role string) bool {
	for _, a := range p.assignments {
		if a.ScopeType == models.ScopeTypeOrg && a.Role == role && a.ScopeID != nil && *a.ScopeID == orgID {
			return true
		}
	}
	return false
}

func (p *principal) hasAnyOrgRole(orgID string) bool {
	for _, a := range p.assignments {
		if a.ScopeType == models.ScopeTypeOrg && a.ScopeID != nil && *a.ScopeID == orgID {
			return true
		}
	}
	return false
}

func (p *principal) hasProjectRole(projectID, role string) bool {
	for _, a := range p.assignments {
		if a.ScopeType == models.ScopeTypeProject && a.Role == role && a.ScopeID != nil && *a.ScopeID == projectID {
			return true
		}
	}
	return false
}

func (p *principal) hasAnyProjectRole(projectID string) bool {
	for _, a := range p.assignments {
		if a.ScopeType == models.ScopeTypeProject && a.ScopeID != nil && *a.ScopeID == projectID {
			return true
		}
	}
	return false
}

// IsGlobalAdmin reports whether id holds the global/admin role, directly or
// via a group. Anonymous identities are never global admins.
func (r *Resolver) IsGlobalAdmin(ctx context.Context, id Identity) (bool, error) {
	if id.Anonymous || id.UserID == "" {
		return false, nil
	}
	p, err := r.loadPrincipal(ctx, id.UserID)
	if err != nil {
		return false, err
	}
	return p.hasGlobalAdmin(), nil
}

// IsOrgAdmin reports whether id is an admin of orgID: global admin, an
// explicit org/admin role assignment (direct or via group), or orgID is
// id's own org. Users act as orgs in this schema (there is no first-class
// orgs table — "user_id IS the org id everywhere", per UI_AUTH_PLAN.md) so
// a user is always the admin of their own org; this preserves today's
// pre-authz behavior where any authenticated user freely manages resources
// they created under their own user_id.
func (r *Resolver) IsOrgAdmin(ctx context.Context, id Identity, orgID string) (bool, error) {
	if id.Anonymous || id.UserID == "" || orgID == "" {
		return false, nil
	}
	if orgID == id.UserID {
		return true, nil
	}
	p, err := r.loadPrincipal(ctx, id.UserID)
	if err != nil {
		return false, err
	}
	if p.hasGlobalAdmin() {
		return true, nil
	}
	return p.hasOrgRole(orgID, models.RoleAdmin), nil
}

// IsProjectOwner reports whether id is an owner of projectID: an explicit
// project/owner role assignment (direct or via group), the admin of the
// project's owning org, or a global admin. Per UI_AUTH_PLAN.md's role
// semantics, org admins and global admins imply project ownership.
func (r *Resolver) IsProjectOwner(ctx context.Context, id Identity, projectID string) (bool, error) {
	if id.Anonymous || id.UserID == "" || projectID == "" {
		return false, nil
	}
	p, err := r.loadPrincipal(ctx, id.UserID)
	if err != nil {
		return false, err
	}
	if p.hasGlobalAdmin() {
		return true, nil
	}
	if p.hasProjectRole(projectID, models.RoleOwner) {
		return true, nil
	}
	project, err := r.store.GetProjectByID(ctx, projectID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return false, nil
		}
		return false, err
	}
	if project.UserID == nil {
		return false, nil
	}
	if *project.UserID == id.UserID {
		return true, nil
	}
	return p.hasOrgRole(*project.UserID, models.RoleAdmin), nil
}

// EffectiveRoleForProject returns the highest role id holds at projectID:
// models.RoleAdmin (global admin or the owning org's admin),
// models.RoleOwner (project owner), models.RoleMember (any role assignment
// scoped directly to the project), or "" (none).
func (r *Resolver) EffectiveRoleForProject(ctx context.Context, id Identity, projectID string) (string, error) {
	if id.Anonymous || id.UserID == "" || projectID == "" {
		return "", nil
	}
	owner, err := r.IsProjectOwner(ctx, id, projectID)
	if err != nil {
		return "", err
	}
	if owner {
		// IsProjectOwner already folds in global-admin and org-admin
		// escalation; distinguish admin from plain owner for callers that
		// want to render the tier, without a second store round trip.
		if isGlobal, err := r.IsGlobalAdmin(ctx, id); err != nil {
			return "", err
		} else if isGlobal {
			return models.RoleAdmin, nil
		}
		p, err := r.loadPrincipal(ctx, id.UserID)
		if err != nil {
			return "", err
		}
		if p.hasProjectRole(projectID, models.RoleOwner) {
			return models.RoleOwner, nil
		}
		return models.RoleAdmin, nil // org-admin (or self-org) escalation
	}
	p, err := r.loadPrincipal(ctx, id.UserID)
	if err != nil {
		return "", err
	}
	if p.hasAnyProjectRole(projectID) {
		return models.RoleMember, nil
	}
	return "", nil
}

// EffectiveRoleForOrg returns the highest role id holds at orgID:
// models.RoleAdmin, models.RoleMember (any role assignment scoped directly
// to the org), or "" (none).
func (r *Resolver) EffectiveRoleForOrg(ctx context.Context, id Identity, orgID string) (string, error) {
	if id.Anonymous || id.UserID == "" || orgID == "" {
		return "", nil
	}
	if admin, err := r.IsOrgAdmin(ctx, id, orgID); err != nil {
		return "", err
	} else if admin {
		return models.RoleAdmin, nil
	}
	p, err := r.loadPrincipal(ctx, id.UserID)
	if err != nil {
		return "", err
	}
	if p.hasAnyOrgRole(orgID) {
		return models.RoleMember, nil
	}
	return "", nil
}

// PermissionError is the typed error every Require* guard returns when the
// caller lacks the needed capability. Use errors.As to detect it (or
// IsPermissionError) when a caller needs to distinguish "denied" from a
// store/transport failure — e.g. to map to HTTP 403 vs 500.
type PermissionError struct {
	// Capability names the guard that failed (e.g. "global_admin",
	// "org_admin", "project_owner").
	Capability string
	// Detail is optional extra context (e.g. the scope id checked).
	Detail string
}

func (e *PermissionError) Error() string {
	if e.Detail != "" {
		return fmt.Sprintf("authz: %s required (%s)", e.Capability, e.Detail)
	}
	return fmt.Sprintf("authz: %s required", e.Capability)
}

// IsPermissionError reports whether err is (or wraps) a *PermissionError.
func IsPermissionError(err error) bool {
	var pe *PermissionError
	return errors.As(err, &pe)
}

// RequireGlobalAdmin returns nil if id is a global admin, otherwise a
// *PermissionError. Store failures are returned unwrapped so callers can
// tell "denied" apart from "couldn't check".
func (r *Resolver) RequireGlobalAdmin(ctx context.Context, id Identity) error {
	ok, err := r.IsGlobalAdmin(ctx, id)
	if err != nil {
		return err
	}
	if !ok {
		return &PermissionError{Capability: "global_admin"}
	}
	return nil
}

// RequireOrgAdmin returns nil if id is an admin of orgID, otherwise a
// *PermissionError.
func (r *Resolver) RequireOrgAdmin(ctx context.Context, id Identity, orgID string) error {
	ok, err := r.IsOrgAdmin(ctx, id, orgID)
	if err != nil {
		return err
	}
	if !ok {
		return &PermissionError{Capability: "org_admin", Detail: "org:" + orgID}
	}
	return nil
}

// RequireProjectOwner returns nil if id is an owner of projectID (directly,
// via org admin, or via global admin), otherwise a *PermissionError.
func (r *Resolver) RequireProjectOwner(ctx context.Context, id Identity, projectID string) error {
	ok, err := r.IsProjectOwner(ctx, id, projectID)
	if err != nil {
		return err
	}
	if !ok {
		return &PermissionError{Capability: "project_owner", Detail: "project:" + projectID}
	}
	return nil
}
