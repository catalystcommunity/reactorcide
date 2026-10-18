package workerapi

import (
	"context"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/uiapi"
)

// Worker and node clocks can differ slightly. Reject invalid timestamps instead
// of silently replacing runtime measurements with coordinator receipt times.
func executionTimestamp(raw *string, lease *models.WorkerLease) (*time.Time, error) {
	if raw == nil {
		return nil, nil
	}
	at, err := time.Parse(time.RFC3339Nano, *raw)
	if err != nil || at.IsZero() || at.Before(lease.AcquiredAt.Add(-5*time.Minute)) || at.After(time.Now().Add(5*time.Minute)) {
		return nil, uiapi.NewServiceError("invalid_argument", "execution timestamp is invalid or outside the lease interval")
	}
	at = at.UTC()
	return &at, nil
}

func leaseOwnsJob(lease *models.WorkerLease, job *models.Job) bool {
	return lease.IsActive() && job.WorkerID != nil && *job.WorkerID == lease.WorkerID &&
		(job.SchedulingStartedAt == nil || job.SchedulingStartedAt.Equal(lease.AcquiredAt))
}

func (s *WorkerService) recordExecutionStart(ctx context.Context, lease *models.WorkerLease, job *models.Job, raw *string) error {
	if job.StartedAt != nil || !leaseOwnsJob(lease, job) {
		return nil
	}
	at, err := executionTimestamp(raw, lease)
	if err != nil || at == nil {
		return err
	}
	changed := false
	updated, matched, err := s.deps.Store.UpdateJobStatusGuarded(ctx, job.JobID, []string{"scheduling", "cancelling"}, func(j *models.Job) {
		if !leaseOwnsJob(lease, j) || j.StartedAt != nil {
			return
		}
		j.StartedAt = at
		if j.Status == "scheduling" {
			j.Status = "running"
		}
		changed = true
	})
	if err != nil {
		return errUnavailable()
	}
	if !matched || !changed {
		return nil
	}
	s.publishJobUpdate(ctx, updated, time.Now().UTC())
	if updated.Status == "running" && s.deps.WorkflowFinalizer != nil && updated.WorkflowID != nil {
		if err := s.deps.WorkflowFinalizer.ProcessWorkflowJobStarted(ctx, updated); err != nil {
			return errUnavailable()
		}
	}
	return nil
}
