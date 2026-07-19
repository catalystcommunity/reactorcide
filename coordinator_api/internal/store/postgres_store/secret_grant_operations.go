package postgres_store

import (
	"context"
	"errors"
	"fmt"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"gorm.io/gorm"
)

func (ps PostgresDbStore) CreateSecretGrant(ctx context.Context, grant *models.SecretGrant) error {
	if err := ps.getDB(ctx).Create(grant).Error; err != nil {
		return fmt.Errorf("failed to create secret grant: %w", err)
	}
	return nil
}

func (ps PostgresDbStore) ListSecretGrants(ctx context.Context, userID string, projectID *string) ([]models.SecretGrant, error) {
	query := ps.getDB(ctx).Where("user_id = ?", userID)
	if projectID == nil || *projectID == "" {
		query = query.Where("project_id IS NULL")
	} else {
		query = query.Where("project_id = ?", *projectID)
	}

	var grants []models.SecretGrant
	if err := query.Order("created_at ASC").Find(&grants).Error; err != nil {
		return nil, fmt.Errorf("failed to list secret grants: %w", err)
	}
	return grants, nil
}

// GetSecretGrantByID retrieves a secret grant by its ID, with no user/project
// scoping. Added for Task G (the CSIL UI service's UpdateSecretGrant/
// DeleteSecretGrant ops, which identify their target by grant ID alone):
// callers must load the row first to discover its owning org before they can
// authorize the request.
func (ps PostgresDbStore) GetSecretGrantByID(ctx context.Context, grantID string) (*models.SecretGrant, error) {
	if !isValidUUID(grantID) {
		return nil, store.ErrNotFound
	}

	var grant models.SecretGrant
	if err := ps.getDB(ctx).Where("grant_id = ?", grantID).First(&grant).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, store.ErrNotFound
		}
		return nil, fmt.Errorf("failed to get secret grant: %w", err)
	}
	return &grant, nil
}

// ListSecretGrantsByOrg lists every secret grant owned by an org (both
// global, project_id IS NULL, and project-scoped rows), unlike ListSecretGrants
// which requires picking one or the other. Added for Task G's
// list-secret-grants CSIL op, whose request carries only an org_id.
func (ps PostgresDbStore) ListSecretGrantsByOrg(ctx context.Context, orgID string) ([]models.SecretGrant, error) {
	var grants []models.SecretGrant
	if err := ps.getDB(ctx).Where("user_id = ?", orgID).Order("created_at ASC").Find(&grants).Error; err != nil {
		return nil, fmt.Errorf("failed to list secret grants for org: %w", err)
	}
	return grants, nil
}

func (ps PostgresDbStore) GetSecretGrant(ctx context.Context, userID string, projectID *string, ref string) (*models.SecretGrant, error) {
	query := ps.scopedSecretGrantQuery(ctx, userID, projectID).Where("(grant_id::text = ? OR name = ?)", ref, ref)
	var grant models.SecretGrant
	if err := query.First(&grant).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, store.ErrNotFound
		}
		return nil, fmt.Errorf("failed to get secret grant: %w", err)
	}
	return &grant, nil
}

func (ps PostgresDbStore) UpdateSecretGrant(ctx context.Context, grant *models.SecretGrant) error {
	if err := ps.getDB(ctx).Save(grant).Error; err != nil {
		return fmt.Errorf("failed to update secret grant: %w", err)
	}
	return nil
}

func (ps PostgresDbStore) DeleteSecretGrant(ctx context.Context, userID string, projectID *string, ref string) error {
	query := ps.scopedSecretGrantQuery(ctx, userID, projectID).Where("(grant_id::text = ? OR name = ?)", ref, ref)
	result := query.Delete(&models.SecretGrant{})
	if result.Error != nil {
		return fmt.Errorf("failed to delete secret grant: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return store.ErrNotFound
	}
	return nil
}

func (ps PostgresDbStore) scopedSecretGrantQuery(ctx context.Context, userID string, projectID *string) *gorm.DB {
	query := ps.getDB(ctx).Where("user_id = ?", userID)
	if projectID == nil || *projectID == "" {
		query = query.Where("project_id IS NULL")
	} else {
		query = query.Where("project_id = ?", *projectID)
	}
	return query
}

// ListSecretGrantsForJob returns grants that may apply to a job.
func (ps PostgresDbStore) ListSecretGrantsForJob(ctx context.Context, userID string, projectID *string, jobName string) ([]models.SecretGrant, error) {
	db := ps.getDB(ctx)
	query := db.Where("user_id = ?", userID)
	if projectID == nil || *projectID == "" {
		query = query.Where("project_id IS NULL")
	} else {
		query = query.Where("(project_id IS NULL OR project_id = ?)", *projectID)
	}

	var grants []models.SecretGrant
	if err := query.Order("created_at ASC").Find(&grants).Error; err != nil {
		return nil, fmt.Errorf("failed to list secret grants: %w", err)
	}
	return grants, nil
}
