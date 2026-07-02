package postgres_store

import (
	"context"
	"fmt"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
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

func (ps PostgresDbStore) DeleteSecretGrant(ctx context.Context, grantID string, projectID *string) error {
	query := ps.getDB(ctx).Where("grant_id = ?", grantID)
	if projectID == nil || *projectID == "" {
		query = query.Where("project_id IS NULL")
	} else {
		query = query.Where("project_id = ?", *projectID)
	}
	result := query.Delete(&models.SecretGrant{})
	if result.Error != nil {
		return fmt.Errorf("failed to delete secret grant: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return store.ErrNotFound
	}
	return nil
}

// ListSecretGrantsForJob returns grants that may apply to a job.
func (ps PostgresDbStore) ListSecretGrantsForJob(ctx context.Context, userID string, projectID *string, jobName, jobFile string) ([]models.SecretGrant, error) {
	db := ps.getDB(ctx)
	query := db.Where("user_id = ?", userID)
	if projectID == nil || *projectID == "" {
		query = query.Where("project_id IS NULL")
	} else {
		query = query.Where("(project_id IS NULL OR project_id = ?)", *projectID)
	}
	if jobName == "" {
		query = query.Where("job_name = '' OR job_name IS NULL")
	} else {
		query = query.Where("job_name = '' OR job_name IS NULL OR job_name = ?", jobName)
	}
	if jobFile == "" {
		query = query.Where("job_file = '' OR job_file IS NULL")
	} else {
		query = query.Where("job_file = '' OR job_file IS NULL OR job_file = ?", jobFile)
	}

	var grants []models.SecretGrant
	if err := query.Order("created_at ASC").Find(&grants).Error; err != nil {
		return nil, fmt.Errorf("failed to list secret grants: %w", err)
	}
	return grants, nil
}
