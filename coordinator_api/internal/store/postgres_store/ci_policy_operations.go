package postgres_store

import (
	"context"
	"fmt"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func (ps PostgresDbStore) GetCIPolicyByProject(ctx context.Context, projectID string) (*models.CIPolicy, error) {
	if !isValidUUID(projectID) {
		return nil, store.ErrNotFound
	}
	var policy models.CIPolicy
	if err := ps.getDB(ctx).Where("project_id = ?", projectID).First(&policy).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, store.ErrNotFound
		}
		return nil, fmt.Errorf("get CI policy: %w", err)
	}
	return &policy, nil
}

func (ps PostgresDbStore) UpsertCIPolicy(ctx context.Context, policy *models.CIPolicy, expectedRevision *string) error {
	if expectedRevision != nil {
		result := ps.getDB(ctx).Model(&models.CIPolicy{}).
			Where("project_id = ? AND revision = ?", policy.ProjectID, *expectedRevision).
			Updates(map[string]interface{}{
				"document": policy.Document, "revision": policy.Revision,
				"updated_by": policy.UpdatedBy, "updated_at": gorm.Expr("timezone('utc', now())"),
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return store.ErrConflict
		}
		return nil
	}
	return ps.getDB(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "project_id"}},
		DoUpdates: clause.Assignments(map[string]interface{}{
			"document":   policy.Document,
			"revision":   policy.Revision,
			"updated_by": policy.UpdatedBy,
			"updated_at": gorm.Expr("timezone('utc', now())"),
		}),
	}).Create(policy).Error
}

func (ps PostgresDbStore) DeleteCIPolicy(ctx context.Context, projectID string, expectedRevision *string) error {
	query := ps.getDB(ctx).Where("project_id = ?", projectID)
	if expectedRevision != nil {
		query = query.Where("revision = ?", *expectedRevision)
	}
	result := query.Delete(&models.CIPolicy{})
	if result.Error != nil {
		return fmt.Errorf("delete CI policy: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		if expectedRevision != nil {
			return store.ErrConflict
		}
		return store.ErrNotFound
	}
	return nil
}
