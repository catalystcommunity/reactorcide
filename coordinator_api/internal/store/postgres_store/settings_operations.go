package postgres_store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// GetGlobalSetting retrieves a single global setting by key.
func (ps PostgresDbStore) GetGlobalSetting(ctx context.Context, key string) (*models.GlobalSetting, error) {
	var setting models.GlobalSetting
	if err := ps.getDB(ctx).Where("key = ?", key).First(&setting).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, store.ErrNotFound
		}
		return nil, fmt.Errorf("failed to get global setting %q: %w", key, err)
	}
	return &setting, nil
}

// SetGlobalSetting creates or replaces a global setting's value.
func (ps PostgresDbStore) SetGlobalSetting(ctx context.Context, key string, value models.JSONValue) error {
	setting := &models.GlobalSetting{
		Key:       key,
		Value:     value,
		UpdatedAt: time.Now().UTC(),
	}
	err := ps.getDB(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "key"}},
		DoUpdates: clause.AssignmentColumns([]string{"value", "updated_at"}),
	}).Create(setting).Error
	if err != nil {
		return fmt.Errorf("failed to set global setting %q: %w", key, err)
	}
	return nil
}

// ListGlobalSettings returns every global setting.
func (ps PostgresDbStore) ListGlobalSettings(ctx context.Context) ([]models.GlobalSetting, error) {
	var settings []models.GlobalSetting
	if err := ps.getDB(ctx).Order("key ASC").Find(&settings).Error; err != nil {
		return nil, fmt.Errorf("failed to list global settings: %w", err)
	}
	return settings, nil
}
