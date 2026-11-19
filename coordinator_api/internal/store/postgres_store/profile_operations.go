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

// UpdateExecutionProfile writes the profile. GORM Updates with a struct
// skips zero values, so a false boolean would keep the stored true. The
// boolean columns are written explicitly; the other columns keep the
// "zero keeps the stored value" rule that the callers use for absent lists.
func (ps PostgresDbStore) UpdateExecutionProfile(ctx context.Context, profile *models.ExecutionProfile) error {
	return ps.getDB(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(profile).Omit("profile_id", "org_id", "name", "created_at").Updates(profile).Error; err != nil {
			return err
		}
		return tx.Model(&models.ExecutionProfile{}).Where("profile_id = ?", profile.ProfileID).Updates(map[string]interface{}{
			"deny_secrets":         profile.DenySecrets,
			"may_run_as_root":      profile.MayRunAsRoot,
			"trusted_cache_writes": profile.TrustedCacheWrites,
			"egress_control":       profile.EgressControl,
			"updated_at":           gorm.Expr("timezone('utc', now())"),
		}).Error
	})
}

func (ps PostgresDbStore) DeleteExecutionProfile(ctx context.Context, orgID, name string) error {
	if name == "standard" || name == "pr-untrusted" {
		return fmt.Errorf("built-in execution profiles cannot be deleted")
	}
	return ps.getDB(ctx).Where("org_id = ? AND name = ?", orgID, name).Delete(&models.ExecutionProfile{}).Error
}
