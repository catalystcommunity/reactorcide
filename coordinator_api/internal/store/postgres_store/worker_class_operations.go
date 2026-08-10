package postgres_store

import (
	"context"
	"errors"
	"fmt"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func (ps PostgresDbStore) CreateWorkerClass(ctx context.Context, class *models.WorkerClass) error {
	if _, err := models.NormalizeOrganizationName(class.Name); err != nil {
		return fmt.Errorf("invalid worker class name: %w", err)
	}
	if err := ps.getDB(ctx).Create(class).Error; err != nil {
		if isUniqueViolation(err) {
			return store.ErrAlreadyExists
		}
		return fmt.Errorf("failed to create worker class: %w", err)
	}
	return nil
}

func (ps PostgresDbStore) GetWorkerClass(ctx context.Context, orgID, name string) (*models.WorkerClass, error) {
	var class models.WorkerClass
	if err := ps.getDB(ctx).Where("org_id = ? AND name = ?", orgID, name).First(&class).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, store.ErrNotFound
		}
		return nil, fmt.Errorf("failed to get worker class: %w", err)
	}
	return &class, nil
}

func (ps PostgresDbStore) ListWorkerClasses(ctx context.Context, orgID string) ([]models.WorkerClass, error) {
	var classes []models.WorkerClass
	if err := ps.getDB(ctx).Where("org_id = ?", orgID).Order("name").Find(&classes).Error; err != nil {
		return nil, fmt.Errorf("failed to list worker classes: %w", err)
	}
	return classes, nil
}

func (ps PostgresDbStore) UpdateWorkerClass(ctx context.Context, class *models.WorkerClass) error {
	result := ps.getDB(ctx).Model(&models.WorkerClass{}).Where("class_id = ?", class.ClassID).Updates(map[string]any{"protected": class.Protected, "updated_at": gorm.Expr("timezone('utc', now())")})
	if result.Error != nil {
		return fmt.Errorf("failed to update worker class: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return store.ErrNotFound
	}
	return nil
}

func (ps PostgresDbStore) DeleteWorkerClass(ctx context.Context, classID string) error {
	result := ps.getDB(ctx).Where("class_id = ? AND name <> 'default'", classID).Delete(&models.WorkerClass{})
	if result.Error != nil {
		return fmt.Errorf("failed to delete worker class: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return store.ErrNotFound
	}
	return nil
}

func (ps PostgresDbStore) GrantWorkerClassPool(ctx context.Context, classID, poolID string) error {
	var class models.WorkerClass
	if err := ps.getDB(ctx).Where("class_id = ?", classID).First(&class).Error; err != nil {
		return store.ErrNotFound
	}
	pool, err := ps.GetWorkerPoolByID(ctx, poolID)
	if err != nil {
		return err
	}
	if pool.OrgID != nil && *pool.OrgID != class.OrgID {
		return fmt.Errorf("organization-owned pool cannot serve another organization: %w", store.ErrForbidden)
	}
	if err := ps.getDB(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&models.WorkerClassPool{ClassID: classID, PoolID: poolID}).Error; err != nil {
		return fmt.Errorf("failed to grant worker class to pool: %w", err)
	}
	return nil
}

func (ps PostgresDbStore) RevokeWorkerClassPool(ctx context.Context, classID, poolID string) error {
	result := ps.getDB(ctx).Where("class_id = ? AND pool_id = ?", classID, poolID).Delete(&models.WorkerClassPool{})
	if result.Error != nil {
		return fmt.Errorf("failed to revoke worker class pool: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return store.ErrNotFound
	}
	return nil
}

func (ps PostgresDbStore) ListPoolsForWorkerClass(ctx context.Context, classID string) ([]models.WorkerPool, error) {
	var pools []models.WorkerPool
	err := ps.getDB(ctx).Table("worker_pools p").Joins("JOIN worker_class_pools wcp ON wcp.pool_id = p.pool_id AND wcp.class_id = ?", classID).Select("p.*").Order("p.name").Find(&pools).Error
	if err != nil {
		return nil, fmt.Errorf("failed to list pools for worker class: %w", err)
	}
	return pools, nil
}
