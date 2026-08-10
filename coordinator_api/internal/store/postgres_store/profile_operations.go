package postgres_store

import (
	"context"
	"fmt"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"gorm.io/gorm"
)

func (ps PostgresDbStore) CreateExecutionProfile(ctx context.Context, profile *models.ExecutionProfile) error {
	return ps.getDB(ctx).Create(profile).Error
}

func (ps PostgresDbStore) GetExecutionProfile(ctx context.Context, orgID, name string) (*models.ExecutionProfile, error) {
	var profile models.ExecutionProfile
	if err := ps.getDB(ctx).Where("org_id = ? AND name = ?", orgID, name).First(&profile).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, store.ErrNotFound
		}
		return nil, fmt.Errorf("get execution profile: %w", err)
	}
	return &profile, nil
}

func (ps PostgresDbStore) ListExecutionProfiles(ctx context.Context, orgID string) ([]models.ExecutionProfile, error) {
	var profiles []models.ExecutionProfile
	err := ps.getDB(ctx).Where("org_id = ?", orgID).Order("name").Find(&profiles).Error
	return profiles, err
}

func (ps PostgresDbStore) UpdateExecutionProfile(ctx context.Context, profile *models.ExecutionProfile) error {
	return ps.getDB(ctx).Model(profile).Omit("profile_id", "org_id", "name", "created_at").Updates(profile).Error
}

func (ps PostgresDbStore) DeleteExecutionProfile(ctx context.Context, orgID, name string) error {
	if name == "standard" || name == "pr-untrusted" {
		return fmt.Errorf("built-in execution profiles cannot be deleted")
	}
	return ps.getDB(ctx).Where("org_id = ? AND name = ?", orgID, name).Delete(&models.ExecutionProfile{}).Error
}
