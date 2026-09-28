package postgres_store

import (
	"context"
	"fmt"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// UpdateJobStatusGuarded performs a race-safe status transition on a job
// row: it loads the row FOR UPDATE inside a transaction (blocking any
// concurrent guarded update on the same row until this one commits), checks
// that the row's *current* status is still one of fromStatuses, and only
// then applies the caller's mutation and saves it. This closes the
// CancelJob TOCTOU race (see internal/jobcontrol.transitionJob and
// internal/worker/corndogs_worker.go's claim-path/terminal-write guards):
// a cancel request and a worker claiming the same job can no longer both
// succeed against a stale in-memory copy of the row.
//
// apply is called with the freshly loaded row (still carrying its pre-
// transition status/fields) and mutates it in place. It runs while the row
// lock is held, so it must not perform its own store I/O.
//
// Returns (nil, false, nil) — not an error — when the row's status wasn't
// in fromStatuses by the time the lock was acquired; this is an expected
// outcome under concurrency (someone else already moved the row on), not a
// failure. Returns store.ErrNotFound if the row doesn't exist at all.
func (ps PostgresDbStore) UpdateJobStatusGuarded(ctx context.Context, jobID string, fromStatuses []string, apply func(*models.Job)) (*models.Job, bool, error) {
	if !isValidUUID(jobID) {
		return nil, false, store.ErrNotFound
	}

	var result *models.Job
	matched := false

	err := ps.getDB(ctx).Transaction(func(tx *gorm.DB) error {
		var job models.Job
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("job_id = ?", jobID).First(&job).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				return store.ErrNotFound
			}
			return fmt.Errorf("failed to load job %s for guarded update: %w", jobID, err)
		}

		if !statusInSet(job.Status, fromStatuses) {
			// Row already moved past the state the caller expected — not an
			// error, just a no-op for this caller. Leave matched false and
			// result nil.
			return nil
		}

		apply(&job)
		job.UpdatedAt = time.Now()
		if err := saveWithOptionalUserID(tx, &job, job.UserID).Error; err != nil {
			return fmt.Errorf("failed to save guarded job update %s: %w", jobID, err)
		}
		matched = true
		result = &job
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	return result, matched, nil
}

// ListStaleCancellingJobs returns every job with status "cancelling" whose
// updated_at is older than olderThan. Used by the coordinator's lease
// reaper (internal/workerapi, Finding 2b) to find jobs orphaned by a worker
// that crashed or restarted mid-cancel and never finalized them.
func (ps PostgresDbStore) ListStaleCancellingJobs(ctx context.Context, olderThan time.Time) ([]models.Job, error) {
	var jobs []models.Job
	if err := ps.getDB(ctx).
		Where("status = ? AND updated_at < ?", "cancelling", olderThan).
		Find(&jobs).Error; err != nil {
		return nil, fmt.Errorf("failed to list stale cancelling jobs: %w", err)
	}
	return jobs, nil
}

// lostJobBatchSize bounds one reconciler pass, so a large backlog of lost
// jobs cannot hold the reaper loop for long.
const lostJobBatchSize = 100

// ListLostRunningJobs returns jobs at "running" that no worker holds: no open
// worker_leases row, and no lease released at or after cutoff. The job must
// also have started before cutoff. A job in this state is executed by
// nobody, and no claim can take it again (a claim accepts only
// submitted/queued), so the lease reaper's lost-job reconciler must repair
// it. The cutoff is a grace period: a claim commits the running transition
// and its lease in one transaction, and a reported result releases the lease
// in the same request that finalizes the job.
func (ps PostgresDbStore) ListLostRunningJobs(ctx context.Context, cutoff time.Time) ([]models.Job, error) {
	var jobs []models.Job
	if err := ps.getDB(ctx).
		Where("jobs.status = ?", "running").
		Where("COALESCE(jobs.started_at, jobs.updated_at) < ?", cutoff).
		Where(`NOT EXISTS (SELECT 1 FROM worker_leases wl WHERE wl.job_id = jobs.job_id AND (wl.released_at IS NULL OR wl.released_at >= ?))`, cutoff).
		Order("jobs.started_at ASC").
		Limit(lostJobBatchSize).
		Find(&jobs).Error; err != nil {
		return nil, fmt.Errorf("failed to list lost running jobs: %w", err)
	}
	return jobs, nil
}

// ListFinalJobsWithOpenWorkflowNode returns terminal jobs, completed before
// cutoff, whose workflow node is still submitted or running. The node missed
// its completion (for example, the workflow update failed while the store
// was unavailable), so the workflow instance and its VCS check never finish.
func (ps PostgresDbStore) ListFinalJobsWithOpenWorkflowNode(ctx context.Context, cutoff time.Time) ([]models.Job, error) {
	var jobs []models.Job
	if err := ps.getDB(ctx).
		Joins("JOIN workflow_nodes wn ON wn.job_id = jobs.job_id").
		Where("wn.status IN ?", []string{"submitted", "running"}).
		Where("jobs.status IN ?", []string{"completed", "failed", "cancelled", "timeout"}).
		Where("jobs.completed_at < ?", cutoff).
		Order("jobs.completed_at ASC").
		Limit(lostJobBatchSize).
		Find(&jobs).Error; err != nil {
		return nil, fmt.Errorf("failed to list final jobs with open workflow nodes: %w", err)
	}
	return jobs, nil
}

// statusInSet reports whether status appears in candidates.
func statusInSet(status string, candidates []string) bool {
	for _, c := range candidates {
		if status == c {
			return true
		}
	}
	return false
}
