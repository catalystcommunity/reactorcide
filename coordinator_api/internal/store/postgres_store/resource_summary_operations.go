package postgres_store

import (
	"context"
	"fmt"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"gorm.io/gorm/clause"
)

var _ store.ResourceSummaryStore = PostgresDbStore{}

// ListJobRunsWithSummaries returns finished runs of one job, newest first,
// with their resource summaries. Two queries, not a join, so the job rows
// load through the normal Job model.
func (ps PostgresDbStore) ListJobRunsWithSummaries(ctx context.Context, filter store.JobRunHistoryFilter) ([]models.JobRunWithSummary, error) {
	if filter.Limit <= 0 {
		return nil, nil
	}
	query := ps.getDB(ctx).
		Where("org_id = ?", filter.OrgID).
		Where("name = ?", filter.Name).
		Where("status IN ?", store.TerminalJobStatuses)
	if filter.ProjectID == nil {
		query = query.Where("project_id IS NULL")
	} else {
		query = query.Where("project_id = ?", *filter.ProjectID)
	}
	if filter.From != nil {
		query = query.Where("created_at >= ?", *filter.From)
	}
	if filter.To != nil {
		query = query.Where("created_at <= ?", *filter.To)
	}
	var jobs []models.Job
	if err := query.Order("created_at DESC").Limit(filter.Limit).Find(&jobs).Error; err != nil {
		return nil, fmt.Errorf("failed to list job runs for %q: %w", filter.Name, err)
	}
	if len(jobs) == 0 {
		return []models.JobRunWithSummary{}, nil
	}
	ids := make([]string, 0, len(jobs))
	for _, job := range jobs {
		ids = append(ids, job.JobID)
	}
	var summaries []models.JobResourceSummary
	if err := ps.getDB(ctx).Where("job_id IN ?", ids).Find(&summaries).Error; err != nil {
		return nil, fmt.Errorf("failed to load job resource summaries: %w", err)
	}
	byID := make(map[string]*models.JobResourceSummary, len(summaries))
	for i := range summaries {
		byID[summaries[i].JobID] = &summaries[i]
	}
	runs := make([]models.JobRunWithSummary, 0, len(jobs))
	for _, job := range jobs {
		runs = append(runs, models.JobRunWithSummary{Job: job, Summary: byID[job.JobID]})
	}
	return runs, nil
}

// UpsertJobResourceSummary inserts or replaces the summary of one job.
func (ps PostgresDbStore) UpsertJobResourceSummary(ctx context.Context, summary *models.JobResourceSummary) error {
	if !isValidUUID(summary.JobID) {
		return store.ErrNotFound
	}
	err := ps.getDB(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "job_id"}},
		UpdateAll: true,
	}).Create(summary).Error
	if err != nil {
		return fmt.Errorf("failed to store resource summary for job %s: %w", summary.JobID, err)
	}
	return nil
}
