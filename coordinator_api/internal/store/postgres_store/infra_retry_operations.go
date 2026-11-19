package postgres_store

import (
	"context"
	"fmt"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"gorm.io/gorm"
)

var _ store.InfraRetryStore = PostgresDbStore{}

// EffectiveInfraRetries returns the project override when it is set, else
// the organization value.
func (ps PostgresDbStore) EffectiveInfraRetries(ctx context.Context, orgID string, projectID *string) (int, error) {
	var orgSetting, projectSetting *int
	if isValidUUID(orgID) {
		var row struct{ InfraRetries *int }
		if err := ps.getDB(ctx).Raw(`SELECT infra_retries FROM organizations WHERE org_id = ?`, orgID).Scan(&row).Error; err != nil {
			return 0, fmt.Errorf("reading organization infra_retries: %w", err)
		}
		orgSetting = row.InfraRetries
	}
	if projectID != nil && isValidUUID(*projectID) {
		var row struct{ InfraRetries *int }
		if err := ps.getDB(ctx).Raw(`SELECT infra_retries FROM projects WHERE project_id = ?`, *projectID).Scan(&row).Error; err != nil {
			return 0, fmt.Errorf("reading project infra_retries: %w", err)
		}
		projectSetting = row.InfraRetries
	}
	return models.EffectiveInfraRetries(projectSetting, orgSetting), nil
}

// SetOrganizationInfraRetries stores the organization infra_retries value.
func (ps PostgresDbStore) SetOrganizationInfraRetries(ctx context.Context, orgID string, value int) error {
	if !models.IsValidInfraRetries(value) {
		return fmt.Errorf("infra_retries must be between 0 and %d: %w", models.MaxInfraRetries, store.ErrInvalidInput)
	}
	if !isValidUUID(orgID) {
		return store.ErrNotFound
	}
	result := ps.getDB(ctx).Model(&models.Organization{}).Where("org_id = ?", orgID).
		Updates(map[string]any{"infra_retries": value, "updated_at": gorm.Expr("timezone('utc', now())")})
	if result.Error != nil {
		return fmt.Errorf("failed to update organization infra_retries: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return store.ErrNotFound
	}
	return nil
}
