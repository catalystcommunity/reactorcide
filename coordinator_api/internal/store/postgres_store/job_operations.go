package postgres_store

import (
	"context"
	"fmt"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"gorm.io/gorm"
)

// GetJobsByUser retrieves jobs for a specific user with pagination
func (ps PostgresDbStore) GetJobsByUser(ctx context.Context, userID string, limit, offset int) ([]models.Job, error) {
	var jobs []models.Job

	query := ps.getDB(ctx).Where("user_id = ?", userID).
		Order("created_at DESC").
		Limit(limit).
		Offset(offset)

	if err := query.Find(&jobs).Error; err != nil {
		return nil, fmt.Errorf("failed to get jobs for user %s: %w", userID, err)
	}

	return jobs, nil
}

// GetJobByID retrieves a job by its ID
func (ps PostgresDbStore) GetJobByID(ctx context.Context, jobID string) (*models.Job, error) {
	if !isValidUUID(jobID) {
		return nil, store.ErrNotFound
	}

	var job models.Job

	if err := ps.getDB(ctx).Where("job_id = ?", jobID).First(&job).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, store.ErrNotFound
		}
		return nil, fmt.Errorf("failed to get job %s: %w", jobID, err)
	}

	return &job, nil
}

// CreateJob creates a new job
func (ps PostgresDbStore) CreateJob(ctx context.Context, job *models.Job) error {
	if err := ps.getDB(ctx).Create(job).Error; err != nil {
		return fmt.Errorf("failed to create job: %w", err)
	}
	return nil
}

// UpdateJob updates an existing job
func (ps PostgresDbStore) UpdateJob(ctx context.Context, job *models.Job) error {
	result := ps.getDB(ctx).Save(job)
	if result.Error != nil {
		return fmt.Errorf("failed to update job %s: %w", job.JobID, result.Error)
	}
	if result.RowsAffected == 0 {
		return store.ErrNotFound
	}
	return nil
}

// DeleteJob deletes a job by its ID
func (ps PostgresDbStore) DeleteJob(ctx context.Context, jobID string) error {
	if !isValidUUID(jobID) {
		return store.ErrNotFound
	}

	result := ps.getDB(ctx).Where("job_id = ?", jobID).Delete(&models.Job{})
	if result.Error != nil {
		return fmt.Errorf("failed to delete job %s: %w", jobID, result.Error)
	}
	if result.RowsAffected == 0 {
		return store.ErrNotFound
	}
	return nil
}

// ListJobs retrieves jobs with optional filters and pagination
func (ps PostgresDbStore) ListJobs(ctx context.Context, filters map[string]interface{}, limit, offset int) ([]models.Job, error) {
	var jobs []models.Job

	query := ps.getDB(ctx).Model(&models.Job{})

	// Apply filters
	for key, value := range filters {
		switch key {
		case "status":
			query = query.Where("status = ?", value)
		case "user_id":
			query = query.Where("user_id = ?", value)
		case "queue_name":
			query = query.Where("queue_name = ?", value)
		case "source_type":
			query = query.Where("source_type = ?", value)
		}
	}

	// Apply pagination and ordering
	query = query.Order("created_at DESC").
		Limit(limit).
		Offset(offset)

	if err := query.Find(&jobs).Error; err != nil {
		return nil, fmt.Errorf("failed to list jobs: %w", err)
	}

	return jobs, nil
}
