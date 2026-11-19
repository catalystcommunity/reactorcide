package postgres_store

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"gorm.io/gorm"
)

// waitingJobStatuses are the statuses of a job that waits for a worker.
var waitingJobStatuses = []string{"submitted", "queued"}

var _ store.AdmissionStore = PostgresDbStore{}

// ListWaitingQueueHeads returns the head job of each listed queue that has
// waiting work, in the Corndogs claim order: highest priority first, then
// oldest. It uses jobs_waiting_queue_idx (migration 000059).
func (ps PostgresDbStore) ListWaitingQueueHeads(ctx context.Context, queueUUIDs []string) ([]models.WaitingJob, error) {
	if len(queueUUIDs) == 0 {
		return nil, nil
	}
	var heads []models.WaitingJob
	err := ps.getDB(ctx).Raw(`
		SELECT DISTINCT ON (queue_name)
			job_id, queue_name, priority, created_at, resource_cpu_request, resource_memory_limit
		FROM jobs
		WHERE status IN ? AND queue_name IN ?
		ORDER BY queue_name, priority DESC, created_at ASC`,
		waitingJobStatuses, queueUUIDs).Scan(&heads).Error
	if err != nil {
		return nil, fmt.Errorf("failed to list waiting queue heads: %w", err)
	}
	return heads, nil
}

// ListWaitingJobGroups returns the waiting jobs grouped by queue and
// resource shape, with the count and the oldest creation time of each group.
func (ps PostgresDbStore) ListWaitingJobGroups(ctx context.Context) ([]models.WaitingJobGroup, error) {
	var groups []models.WaitingJobGroup
	err := ps.getDB(ctx).Raw(`
		SELECT queue_name, resource_cpu_request, resource_memory_limit,
			count(*) AS count, min(created_at) AS oldest_created_at
		FROM jobs
		WHERE status IN ?
		GROUP BY queue_name, resource_cpu_request, resource_memory_limit
		ORDER BY queue_name`,
		waitingJobStatuses).Scan(&groups).Error
	if err != nil {
		return nil, fmt.Errorf("failed to list waiting job groups: %w", err)
	}
	return groups, nil
}

// UpdateWorkerAdmission stores the newest admission report of a worker. A
// nil report clears it (the worker went back to concurrency mode).
func (ps PostgresDbStore) UpdateWorkerAdmission(ctx context.Context, workerID string, report *models.WorkerAdmission) error {
	if !isValidUUID(workerID) {
		return store.ErrNotFound
	}
	var value any
	if report != nil {
		encoded, err := json.Marshal(report)
		if err != nil {
			return fmt.Errorf("failed to encode worker admission: %w", err)
		}
		value = gorm.Expr("?::jsonb", string(encoded))
	}
	result := ps.getDB(ctx).Model(&models.Worker{}).Where("worker_id = ?", workerID).
		Update("admission", value)
	if result.Error != nil {
		return fmt.Errorf("failed to update worker admission: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return store.ErrNotFound
	}
	return nil
}
