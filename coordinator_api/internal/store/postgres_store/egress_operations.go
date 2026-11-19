package postgres_store

import (
	"context"
	"fmt"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"gorm.io/gorm"
)

var _ store.EgressControlStore = PostgresDbStore{}

// EffectiveEgressControl is true when the organization, the project, or the
// execution profile has egress control on. One setting cannot turn off
// another: the strictest one wins.
func (ps PostgresDbStore) EffectiveEgressControl(ctx context.Context, orgID string, projectID *string, profile string) (bool, error) {
	db := ps.getDB(ctx)
	var on struct{ Enabled bool }
	if isValidUUID(orgID) {
		if err := db.Raw(`SELECT egress_control AS enabled FROM organizations WHERE org_id = ?`, orgID).Scan(&on).Error; err != nil {
			return false, fmt.Errorf("reading organization egress_control: %w", err)
		}
		if on.Enabled {
			return true, nil
		}
		if profile != "" {
			on.Enabled = false
			if err := db.Raw(`SELECT egress_control AS enabled FROM execution_profiles WHERE org_id = ? AND name = ?`, orgID, profile).Scan(&on).Error; err != nil {
				return false, fmt.Errorf("reading execution profile egress_control: %w", err)
			}
			if on.Enabled {
				return true, nil
			}
		}
	}
	if projectID != nil && isValidUUID(*projectID) {
		on.Enabled = false
		if err := db.Raw(`SELECT egress_control AS enabled FROM projects WHERE project_id = ?`, *projectID).Scan(&on).Error; err != nil {
			return false, fmt.Errorf("reading project egress_control: %w", err)
		}
		if on.Enabled {
			return true, nil
		}
	}
	return false, nil
}

// SetOrganizationEgressControl sets the organization egress control.
func (ps PostgresDbStore) SetOrganizationEgressControl(ctx context.Context, orgID string, on bool) error {
	if !isValidUUID(orgID) {
		return store.ErrNotFound
	}
	result := ps.getDB(ctx).Model(&models.Organization{}).Where("org_id = ?", orgID).
		Updates(map[string]any{"egress_control": on, "updated_at": gorm.Expr("timezone('utc', now())")})
	if result.Error != nil {
		return fmt.Errorf("failed to set organization egress_control: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return store.ErrNotFound
	}
	return nil
}
