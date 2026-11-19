package postgres_store

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// maxMergeDepth limits how many merged_into links GetPrincipal follows. A
// merge chain is short (normally one link); the limit stops a cycle.
const maxMergeDepth = 8

// GetPrincipal returns a principal by ID. It follows merged_into, so the
// result is the principal that the ID resolves to now.
func (ps PostgresDbStore) GetPrincipal(ctx context.Context, principalID string) (*models.Principal, error) {
	if !isValidUUID(principalID) {
		return nil, store.ErrNotFound
	}
	current := principalID
	for depth := 0; depth < maxMergeDepth; depth++ {
		var principal models.Principal
		if err := ps.getDB(ctx).Where("principal_id = ?", current).First(&principal).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil, store.ErrNotFound
			}
			return nil, fmt.Errorf("failed to get principal: %w", err)
		}
		if principal.MergedInto == nil || *principal.MergedInto == "" {
			return &principal, nil
		}
		current = *principal.MergedInto
	}
	return nil, fmt.Errorf("principal %s has a merge chain that is too long", principalID)
}

// GetPrincipalsByIDs returns the principals with the given IDs (without
// following merges), keyed by ID. Unknown IDs are absent from the result.
func (ps PostgresDbStore) GetPrincipalsByIDs(ctx context.Context, principalIDs []string) (map[string]models.Principal, error) {
	valid := make([]string, 0, len(principalIDs))
	for _, id := range principalIDs {
		if isValidUUID(id) {
			valid = append(valid, id)
		}
	}
	result := make(map[string]models.Principal, len(valid))
	if len(valid) == 0 {
		return result, nil
	}
	var rows []models.Principal
	if err := ps.getDB(ctx).Where("principal_id IN ?", valid).Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("failed to get principals: %w", err)
	}
	for _, row := range rows {
		result[row.PrincipalID] = row
	}
	return result, nil
}

// ResolvePrincipalIdentity returns the principal that has the identity.
func (ps PostgresDbStore) ResolvePrincipalIdentity(ctx context.Context, identityType, value string) (*models.Principal, error) {
	value = models.NormalizeIdentityValue(identityType, value)
	if !models.ValidIdentityType(identityType) || value == "" {
		return nil, store.ErrNotFound
	}
	var identity models.PrincipalIdentity
	if err := ps.getDB(ctx).Where("identity_type = ? AND identity_value = ?", identityType, value).First(&identity).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, store.ErrNotFound
		}
		return nil, fmt.Errorf("failed to resolve principal identity: %w", err)
	}
	return ps.GetPrincipal(ctx, identity.PrincipalID)
}

// ListPrincipalIdentities returns every identity of a principal.
func (ps PostgresDbStore) ListPrincipalIdentities(ctx context.Context, principalID string) ([]models.PrincipalIdentity, error) {
	if !isValidUUID(principalID) {
		return []models.PrincipalIdentity{}, nil
	}
	var identities []models.PrincipalIdentity
	if err := ps.getDB(ctx).Where("principal_id = ?", principalID).
		Order("identity_type ASC, identity_value ASC").Find(&identities).Error; err != nil {
		return nil, fmt.Errorf("failed to list principal identities: %w", err)
	}
	return identities, nil
}

// ListPrincipals lists principals. orgID limits the list to one
// organization's service principals and to humans that are members of the
// organization's groups or hold a role in it; kind limits the kind. An empty
// orgID lists every principal.
func (ps PostgresDbStore) ListPrincipals(ctx context.Context, orgID, kind string) ([]models.Principal, error) {
	query := ps.getDB(ctx).Model(&models.Principal{}).Where("merged_into IS NULL")
	if kind != "" {
		query = query.Where("kind = ?", kind)
	}
	if orgID != "" {
		if !isValidUUID(orgID) {
			return []models.Principal{}, nil
		}
		query = query.Where(`org_id = ? OR principal_id IN (
			SELECT gm.principal_id FROM group_members gm JOIN groups g ON g.group_id = gm.group_id WHERE g.org_id = ?
			UNION SELECT ra.principal_id FROM role_assignments ra
			 WHERE ra.principal_type IN ('user', 'principal') AND ra.scope_type = 'org' AND ra.scope_id = ?
			UNION SELECT ra.principal_id FROM role_assignments ra JOIN projects p ON p.project_id = ra.scope_id
			 WHERE ra.principal_type IN ('user', 'principal') AND ra.scope_type = 'project' AND p.org_id = ?)`,
			orgID, orgID, orgID, orgID)
	}
	var principals []models.Principal
	if err := query.Order("kind ASC, name ASC").Find(&principals).Error; err != nil {
		return nil, fmt.Errorf("failed to list principals: %w", err)
	}
	return principals, nil
}

// GetServicePrincipalByName returns a service principal of an organization
// by name. An empty orgID looks up an instance service principal.
func (ps PostgresDbStore) GetServicePrincipalByName(ctx context.Context, orgID, name string) (*models.Principal, error) {
	query := ps.getDB(ctx).Where("kind = ? AND name = ?", models.PrincipalKindService, name)
	if orgID == "" {
		query = query.Where("org_id IS NULL")
	} else {
		if !isValidUUID(orgID) {
			return nil, store.ErrNotFound
		}
		query = query.Where("org_id = ?", orgID)
	}
	var principal models.Principal
	if err := query.First(&principal).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, store.ErrNotFound
		}
		return nil, fmt.Errorf("failed to get service principal: %w", err)
	}
	return &principal, nil
}

// CreateServicePrincipal creates a service principal. The name must be
// unique in the organization.
func (ps PostgresDbStore) CreateServicePrincipal(ctx context.Context, principal *models.Principal) error {
	principal.Kind = models.PrincipalKindService
	if !models.ValidServicePrincipalName(principal.Name) {
		return store.ErrInvalidInput
	}
	if principal.Status == "" {
		principal.Status = models.PrincipalStatusActive
	}
	orgID := ""
	if principal.OrgID != nil {
		orgID = *principal.OrgID
	}
	if existing, err := ps.GetServicePrincipalByName(ctx, orgID, principal.Name); err == nil && existing != nil {
		return store.ErrAlreadyExists
	}
	if err := ps.getDB(ctx).Create(principal).Error; err != nil {
		if strings.Contains(err.Error(), "principals_service_name_idx") {
			return store.ErrAlreadyExists
		}
		return fmt.Errorf("failed to create service principal: %w", err)
	}
	return nil
}

// SetPrincipalStatus enables or disables a principal. The system principal
// cannot change.
func (ps PostgresDbStore) SetPrincipalStatus(ctx context.Context, principalID, status string) error {
	if principalID == models.SystemPrincipalID {
		return store.ErrForbidden
	}
	if status != models.PrincipalStatusActive && status != models.PrincipalStatusDisabled {
		return store.ErrInvalidInput
	}
	result := ps.getDB(ctx).Model(&models.Principal{}).Where("principal_id = ?", principalID).
		Updates(map[string]any{"status": status, "updated_at": gorm.Expr("timezone('utc', now())")})
	if result.Error != nil {
		return fmt.Errorf("failed to set principal status: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return store.ErrNotFound
	}
	return nil
}

// EnsureVCSPrincipal returns the principal of a VCS login. When Reactorcide
// has not seen the login before, it creates a human principal with the
// login as its vcs identity (verified by a signed webhook). Two concurrent
// calls for the same login get the same principal.
func (ps PostgresDbStore) EnsureVCSPrincipal(ctx context.Context, provider, login string) (*models.Principal, bool, error) {
	value := models.VCSIdentityValue(provider, login)
	if value == "" {
		return nil, false, store.ErrInvalidInput
	}
	if existing, err := ps.ResolvePrincipalIdentity(ctx, models.IdentityTypeVCS, value); err == nil {
		return existing, false, nil
	} else if !errors.Is(err, store.ErrNotFound) {
		return nil, false, err
	}
	created := false
	err := ps.getDB(ctx).Transaction(func(tx *gorm.DB) error {
		principal := &models.Principal{Kind: models.PrincipalKindHuman, Name: value,
			DisplayName: strings.TrimSpace(login), Status: models.PrincipalStatusActive}
		if err := tx.Create(principal).Error; err != nil {
			return err
		}
		identity := &models.PrincipalIdentity{IdentityType: models.IdentityTypeVCS, IdentityValue: value,
			PrincipalID: principal.PrincipalID, VerifiedBy: models.IdentityVerifiedByWebhook}
		result := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(identity)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			// Another delivery created the identity first. Drop this row.
			return tx.Where("principal_id = ?", principal.PrincipalID).Delete(&models.Principal{}).Error
		}
		created = true
		return nil
	})
	if err != nil {
		return nil, false, fmt.Errorf("failed to create VCS principal: %w", err)
	}
	principal, err := ps.ResolvePrincipalIdentity(ctx, models.IdentityTypeVCS, value)
	return principal, created, err
}

// LinkPrincipalIdentity links an identity to a principal. When the identity
// belongs to another principal that only a webhook made (see
// principals_webhook_only), that principal is merged into a human target. Otherwise an identity of another principal is a conflict.
// user and token identities are structural and cannot be linked here.
func (ps PostgresDbStore) LinkPrincipalIdentity(ctx context.Context, identity *models.PrincipalIdentity) error {
	identity.IdentityValue = models.NormalizeIdentityValue(identity.IdentityType, identity.IdentityValue)
	if !models.ValidIdentityType(identity.IdentityType) || identity.IdentityValue == "" ||
		identity.IdentityType == models.IdentityTypeUser || identity.IdentityType == models.IdentityTypeToken {
		return store.ErrInvalidInput
	}
	if identity.PrincipalID == models.SystemPrincipalID {
		return store.ErrForbidden
	}
	if identity.VerifiedBy == "" {
		identity.VerifiedBy = models.IdentityVerifiedByAdmin
	}
	return ps.getDB(ctx).Transaction(func(tx *gorm.DB) error {
		var target models.Principal
		if err := tx.Where("principal_id = ?", identity.PrincipalID).First(&target).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return store.ErrNotFound
			}
			return err
		}
		var existing models.PrincipalIdentity
		err := tx.Where("identity_type = ? AND identity_value = ?", identity.IdentityType, identity.IdentityValue).First(&existing).Error
		switch {
		case err == nil && existing.PrincipalID == identity.PrincipalID:
			return nil
		case err == nil:
			// principals_webhook_only (migration 000048): a human with no
			// organization, no user/token/federated identity, no group
			// membership, and no role assignment. Any other owner keeps
			// its identity; a merge would move its authority.
			var webhookOnly bool
			if err := tx.Raw("SELECT principals_webhook_only(?)", existing.PrincipalID).Scan(&webhookOnly).Error; err != nil {
				return err
			}
			if !webhookOnly || target.Kind != models.PrincipalKindHuman {
				return store.ErrConflict
			}
			return tx.Exec("SELECT principals_merge(?, ?)", existing.PrincipalID, identity.PrincipalID).Error
		case !errors.Is(err, gorm.ErrRecordNotFound):
			return err
		}
		return tx.Create(identity).Error
	})
}

// UnlinkPrincipalIdentity removes an identity from its principal. user and
// token identities are structural and cannot be removed here.
func (ps PostgresDbStore) UnlinkPrincipalIdentity(ctx context.Context, identityType, value string) error {
	value = models.NormalizeIdentityValue(identityType, value)
	if identityType == models.IdentityTypeUser || identityType == models.IdentityTypeToken || !models.ValidIdentityType(identityType) {
		return store.ErrInvalidInput
	}
	result := ps.getDB(ctx).Where("identity_type = ? AND identity_value = ?", identityType, value).Delete(&models.PrincipalIdentity{})
	if result.Error != nil {
		return fmt.Errorf("failed to unlink identity: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return store.ErrNotFound
	}
	return nil
}

// GetPrincipalIdentity returns one identity row.
func (ps PostgresDbStore) GetPrincipalIdentity(ctx context.Context, identityType, value string) (*models.PrincipalIdentity, error) {
	value = models.NormalizeIdentityValue(identityType, value)
	var identity models.PrincipalIdentity
	if err := ps.getDB(ctx).Where("identity_type = ? AND identity_value = ?", identityType, value).First(&identity).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, store.ErrNotFound
		}
		return nil, fmt.Errorf("failed to get identity: %w", err)
	}
	return &identity, nil
}

// AddGroupMemberPrincipal adds any principal to a group. Idempotent.
func (ps PostgresDbStore) AddGroupMemberPrincipal(ctx context.Context, groupID, principalID string) error {
	if !isValidUUID(groupID) || !isValidUUID(principalID) || principalID == models.SystemPrincipalID {
		return store.ErrInvalidInput
	}
	member := &models.GroupMember{GroupID: groupID, PrincipalID: principalID}
	if err := ps.getDB(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(member).Error; err != nil {
		return fmt.Errorf("failed to add group member: %w", err)
	}
	// A member that a linked VCS team gave becomes manual (group_vcs_operations.go).
	if err := makeMembershipManual(ps.getDB(ctx), groupID, principalID); err != nil {
		return fmt.Errorf("failed to add group member: %w", err)
	}
	return nil
}

// RemoveGroupMemberPrincipal removes a principal from a group. A member that
// a linked VCS team still has stays as a synced member; a member that only a
// linked team gives is store.ErrConflict.
func (ps PostgresDbStore) RemoveGroupMemberPrincipal(ctx context.Context, groupID, principalID string) error {
	if !isValidUUID(groupID) || !isValidUUID(principalID) {
		return store.ErrNotFound
	}
	var member models.GroupMember
	if err := ps.getDB(ctx).Where("group_id = ? AND principal_id = ?", groupID, principalID).First(&member).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return store.ErrNotFound
		}
		return fmt.Errorf("failed to remove group member: %w", err)
	}
	if err := removeManualMembership(ps.getDB(ctx), groupID, &member); err != nil {
		if errors.Is(err, store.ErrConflict) {
			return err
		}
		return fmt.Errorf("failed to remove group member: %w", err)
	}
	return nil
}

// ListGroupMemberPrincipals lists every principal in a group.
func (ps PostgresDbStore) ListGroupMemberPrincipals(ctx context.Context, groupID string) ([]models.Principal, error) {
	if !isValidUUID(groupID) {
		return []models.Principal{}, nil
	}
	var principals []models.Principal
	if err := ps.getDB(ctx).
		Joins("JOIN group_members ON group_members.principal_id = principals.principal_id").
		Where("group_members.group_id = ?", groupID).
		Order("principals.kind ASC, principals.name ASC").
		Find(&principals).Error; err != nil {
		return nil, fmt.Errorf("failed to list group member principals: %w", err)
	}
	return principals, nil
}

// ListGroupsForPrincipal lists the groups a principal belongs to.
func (ps PostgresDbStore) ListGroupsForPrincipal(ctx context.Context, principalID string) ([]models.Group, error) {
	if !isValidUUID(principalID) {
		return []models.Group{}, nil
	}
	var groups []models.Group
	if err := ps.getDB(ctx).
		Joins("JOIN group_members ON group_members.group_id = groups.group_id").
		Where("group_members.principal_id = ?", principalID).
		Order("groups.name ASC").
		Find(&groups).Error; err != nil {
		return nil, fmt.Errorf("failed to list groups for principal: %w", err)
	}
	return groups, nil
}

// ListRoleAssignmentsForPrincipalID lists the role assignments of a principal
// that is not a user: assignments to principal_type 'principal' and to its
// groups.
func (ps PostgresDbStore) ListRoleAssignmentsForPrincipalID(ctx context.Context, principalID string, groupIDs []string) ([]models.RoleAssignment, error) {
	db := ps.getDB(ctx)
	query := db.Where("principal_type = ? AND principal_id = ?", models.PrincipalTypePrincipal, principalID)
	if len(groupIDs) > 0 {
		query = db.Where("(principal_type = ? AND principal_id = ?) OR (principal_type = ? AND principal_id IN ?)",
			models.PrincipalTypePrincipal, principalID, models.PrincipalTypeGroup, groupIDs)
	}
	var assignments []models.RoleAssignment
	if err := query.Find(&assignments).Error; err != nil {
		return nil, fmt.Errorf("failed to list role assignments for principal: %w", err)
	}
	return assignments, nil
}

// ListAPITokensForPrincipal lists the tokens bound to a principal.
func (ps PostgresDbStore) ListAPITokensForPrincipal(ctx context.Context, principalID string) ([]models.APIToken, error) {
	if !isValidUUID(principalID) {
		return []models.APIToken{}, nil
	}
	var tokens []models.APIToken
	if err := ps.getDB(ctx).Where("principal_id = ?", principalID).Order("created_at ASC").Find(&tokens).Error; err != nil {
		return nil, fmt.Errorf("failed to list principal tokens: %w", err)
	}
	return tokens, nil
}
