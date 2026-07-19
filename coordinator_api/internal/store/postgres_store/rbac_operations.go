package postgres_store

import (
	"context"
	"errors"
	"fmt"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"gorm.io/gorm"
)

// CreateGroup creates a new group.
func (ps PostgresDbStore) CreateGroup(ctx context.Context, group *models.Group) error {
	if err := ps.getDB(ctx).Create(group).Error; err != nil {
		return fmt.Errorf("failed to create group: %w", err)
	}
	return nil
}

// GetGroupByID retrieves a group by its ID.
func (ps PostgresDbStore) GetGroupByID(ctx context.Context, groupID string) (*models.Group, error) {
	if !isValidUUID(groupID) {
		return nil, store.ErrNotFound
	}

	var group models.Group
	if err := ps.getDB(ctx).Where("group_id = ?", groupID).First(&group).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, store.ErrNotFound
		}
		return nil, fmt.Errorf("failed to get group: %w", err)
	}
	return &group, nil
}

// ListGroupsByOrg lists all groups belonging to an org.
func (ps PostgresDbStore) ListGroupsByOrg(ctx context.Context, orgID string) ([]models.Group, error) {
	var groups []models.Group
	if err := ps.getDB(ctx).Where("org_id = ?", orgID).Order("name ASC").Find(&groups).Error; err != nil {
		return nil, fmt.Errorf("failed to list groups: %w", err)
	}
	return groups, nil
}

// UpdateGroup updates an existing group.
func (ps PostgresDbStore) UpdateGroup(ctx context.Context, group *models.Group) error {
	if err := ps.getDB(ctx).Save(group).Error; err != nil {
		return fmt.Errorf("failed to update group: %w", err)
	}
	return nil
}

// DeleteGroup deletes a group by its ID. Group members and role assignments
// referencing the group_id are removed by ON DELETE CASCADE for members;
// role_assignments has no FK (principal_id is polymorphic) so callers should
// also clean up assignments for the group if desired.
func (ps PostgresDbStore) DeleteGroup(ctx context.Context, groupID string) error {
	if !isValidUUID(groupID) {
		return store.ErrNotFound
	}

	result := ps.getDB(ctx).Where("group_id = ?", groupID).Delete(&models.Group{})
	if result.Error != nil {
		return fmt.Errorf("failed to delete group: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return store.ErrNotFound
	}
	return nil
}

// AddGroupMember adds a user to a group. Idempotent: adding an existing
// member is a no-op.
func (ps PostgresDbStore) AddGroupMember(ctx context.Context, groupID, userID string) error {
	member := &models.GroupMember{GroupID: groupID, UserID: userID}
	if err := ps.getDB(ctx).
		Where("group_id = ? AND user_id = ?", groupID, userID).
		FirstOrCreate(member).Error; err != nil {
		return fmt.Errorf("failed to add group member: %w", err)
	}
	return nil
}

// RemoveGroupMember removes a user from a group.
func (ps PostgresDbStore) RemoveGroupMember(ctx context.Context, groupID, userID string) error {
	result := ps.getDB(ctx).
		Where("group_id = ? AND user_id = ?", groupID, userID).
		Delete(&models.GroupMember{})
	if result.Error != nil {
		return fmt.Errorf("failed to remove group member: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return store.ErrNotFound
	}
	return nil
}

// ListGroupMembers lists the users belonging to a group.
func (ps PostgresDbStore) ListGroupMembers(ctx context.Context, groupID string) ([]models.User, error) {
	var users []models.User
	if err := ps.getDB(ctx).
		Joins("JOIN group_members ON group_members.user_id = users.user_id").
		Where("group_members.group_id = ?", groupID).
		Order("users.username ASC").
		Find(&users).Error; err != nil {
		return nil, fmt.Errorf("failed to list group members: %w", err)
	}
	return users, nil
}

// ListGroupsForUser lists the groups a user belongs to.
func (ps PostgresDbStore) ListGroupsForUser(ctx context.Context, userID string) ([]models.Group, error) {
	var groups []models.Group
	if err := ps.getDB(ctx).
		Joins("JOIN group_members ON group_members.group_id = groups.group_id").
		Where("group_members.user_id = ?", userID).
		Order("groups.name ASC").
		Find(&groups).Error; err != nil {
		return nil, fmt.Errorf("failed to list groups for user: %w", err)
	}
	return groups, nil
}

// CreateRoleAssignment creates a new role assignment.
func (ps PostgresDbStore) CreateRoleAssignment(ctx context.Context, assignment *models.RoleAssignment) error {
	if err := ps.getDB(ctx).Create(assignment).Error; err != nil {
		return fmt.Errorf("failed to create role assignment: %w", err)
	}
	return nil
}

// GetRoleAssignmentByID retrieves a role assignment by its ID. Added for
// Task G (the CSIL UI service's RevokeRole op, which identifies its target
// by assignment ID alone): callers must load the row first to discover its
// scope before they can authorize the request.
func (ps PostgresDbStore) GetRoleAssignmentByID(ctx context.Context, assignmentID string) (*models.RoleAssignment, error) {
	if !isValidUUID(assignmentID) {
		return nil, store.ErrNotFound
	}

	var assignment models.RoleAssignment
	if err := ps.getDB(ctx).Where("assignment_id = ?", assignmentID).First(&assignment).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, store.ErrNotFound
		}
		return nil, fmt.Errorf("failed to get role assignment: %w", err)
	}
	return &assignment, nil
}

// DeleteRoleAssignment deletes a role assignment by its ID.
func (ps PostgresDbStore) DeleteRoleAssignment(ctx context.Context, assignmentID string) error {
	if !isValidUUID(assignmentID) {
		return store.ErrNotFound
	}

	result := ps.getDB(ctx).Where("assignment_id = ?", assignmentID).Delete(&models.RoleAssignment{})
	if result.Error != nil {
		return fmt.Errorf("failed to delete role assignment: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return store.ErrNotFound
	}
	return nil
}

// ListRoleAssignmentsByScope lists all role assignments at a given scope.
// scopeID is nil for the global scope.
func (ps PostgresDbStore) ListRoleAssignmentsByScope(ctx context.Context, scopeType string, scopeID *string) ([]models.RoleAssignment, error) {
	query := ps.getDB(ctx).Where("scope_type = ?", scopeType)
	if scopeID == nil || *scopeID == "" {
		query = query.Where("scope_id IS NULL")
	} else {
		query = query.Where("scope_id = ?", *scopeID)
	}

	var assignments []models.RoleAssignment
	if err := query.Order("created_at ASC").Find(&assignments).Error; err != nil {
		return nil, fmt.Errorf("failed to list role assignments: %w", err)
	}
	return assignments, nil
}

// ListRoleAssignmentsForPrincipal lists every role assignment that applies to
// a user, whether granted directly to the user or to any group the user
// belongs to. This is the hot path for authz resolution.
func (ps PostgresDbStore) ListRoleAssignmentsForPrincipal(ctx context.Context, userID string, groupIDs []string) ([]models.RoleAssignment, error) {
	db := ps.getDB(ctx)

	query := db.Where(
		"(principal_type = ? AND principal_id = ?)",
		models.PrincipalTypeUser, userID,
	)
	if len(groupIDs) > 0 {
		query = db.Where(
			"(principal_type = ? AND principal_id = ?) OR (principal_type = ? AND principal_id IN ?)",
			models.PrincipalTypeUser, userID,
			models.PrincipalTypeGroup, groupIDs,
		)
	}

	var assignments []models.RoleAssignment
	if err := query.Find(&assignments).Error; err != nil {
		return nil, fmt.Errorf("failed to list role assignments for principal: %w", err)
	}
	return assignments, nil
}
