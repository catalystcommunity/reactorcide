package workerapi

import (
	"context"
	"fmt"
	"time"

	"github.com/catalystcommunity/app-utils-go/logging"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/audit"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/metrics"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/worker"
)

// effectiveInfraRetries returns the infra_retries setting in force for job.
// A store without the setting, or a failed read, gives the default.
func (s *WorkerService) effectiveInfraRetries(ctx context.Context, job *models.Job) int {
	settings, ok := s.deps.Store.(store.InfraRetryStore)
	if !ok {
		return models.DefaultInfraRetries
	}
	value, err := settings.EffectiveInfraRetries(ctx, job.OrgID, job.ProjectID)
	if err != nil {
		logging.Log.WithError(err).WithField("job_id", job.JobID).Warn("Could not read infra_retries setting; using the default")
		return models.DefaultInfraRetries
	}
	return value
}

// shouldRetryInfra reports whether job, which failed with class, gets an
// automatic retry, and the budget that decided it. retry_count counts every
// automatic retry of the job (lost-job requeues and platform retries).
func (s *WorkerService) shouldRetryInfra(ctx context.Context, job *models.Job, class string) (bool, int) {
	if models.InfraRetryBudget(class, models.MaxInfraRetries) == 0 {
		return false, 0
	}
	effective := s.effectiveInfraRetries(ctx, job)
	return models.ShouldRetryInfraFailure(class, job.RetryCount, effective), models.InfraRetryBudget(class, effective)
}

// requeueInfraFailure puts job back in the queue with a new corndogs task
// after an infrastructure failure of class. It is the one requeue path of
// the lost-job reconciler and of a reported infra.platform failure. The
// job-row write is guarded from fromStatuses, and owns (when set) must
// accept the locked row; else nothing changes and it returns false.
//
// When the replacement task cannot be submitted, the job goes back to the
// status it had, with a "lost:" error and no lease: the lost-job reconciler
// tries again on its next pass, inside the same retry budget.
func (s *WorkerService) requeueInfraFailure(ctx context.Context, job *models.Job, class, reason string, budget int, fromStatuses []string, owns func(*models.Job) bool) bool {
	logger := logging.Log.WithFields(map[string]interface{}{"job_id": job.JobID, "retry_count": job.RetryCount, "failure_class": class})
	if s.deps.CorndogsClient == nil {
		return false
	}
	var previous models.Job
	applied := false
	requeued, matched, err := s.deps.Store.UpdateJobStatusGuarded(ctx, job.JobID, fromStatuses, func(j *models.Job) {
		if owns != nil && !owns(j) {
			return
		}
		applied = true
		previous = *j
		j.Status = "submitted"
		j.StartedAt = nil
		j.SchedulingStartedAt = nil
		j.WorkerID = nil
		j.CorndogsTaskID = nil
		j.ExitCode = nil
		j.CompletedAt = nil
		j.RetryCount++
		j.LastError = fmt.Sprintf("requeued: %s (automatic retry %d of %d): %s", class, j.RetryCount, budget, reason)
		j.SetFailure("", "")
	})
	if err != nil || !matched || !applied {
		if err != nil {
			logger.WithError(err).Warn("Failed to requeue job after infrastructure failure")
		}
		return false
	}
	metrics.RecordInfraRetry(class)
	details := models.JSONB{"failure_class": class, "reason": reason, "retry_count": requeued.RetryCount, "budget": budget}
	audit.Record(ctx, s.deps.Store, requeued.OrgID, "job.infra_retry", "job", requeued.JobID, details)
	if tokenStore, ok := s.deps.Store.(jobTokenStore); ok {
		if err := tokenStore.RevokeJobTokens(ctx, job.JobID); err != nil {
			logger.WithError(err).Warn("Failed to revoke job token before infrastructure retry")
		}
	}

	task, err := s.deps.CorndogsClient.SubmitTaskToQueue(ctx, requeued.QueueName, worker.BuildTaskPayload(requeued), int64(requeued.Priority))
	if err != nil {
		// Back to the prior status: the next reconciler pass tries again,
		// and retry_count already counts this attempt.
		logger.WithError(err).Warn("Failed to submit replacement corndogs task after infrastructure failure")
		if _, _, revertErr := s.deps.Store.UpdateJobStatusGuarded(ctx, job.JobID, []string{"submitted"}, func(j *models.Job) {
			j.Status = previous.Status
			j.SchedulingStartedAt = previous.SchedulingStartedAt
			j.StartedAt = previous.StartedAt
			j.LastError = "lost: replacement task submission failed"
		}); revertErr != nil {
			logger.WithError(revertErr).Warn("Failed to return job to its prior status after submission failure")
		}
		return true
	}
	taskID := task.Uuid
	if _, _, err := s.deps.Store.UpdateJobStatusGuarded(ctx, job.JobID, []string{"submitted", "queued"}, func(j *models.Job) {
		if j.CorndogsTaskID == nil || *j.CorndogsTaskID == "" {
			j.CorndogsTaskID = &taskID
		}
	}); err != nil {
		logger.WithError(err).WithField("task_id", taskID).Warn("Failed to record replacement corndogs task")
	}
	logger.WithField("task_id", taskID).Warn("Requeued job after infrastructure failure")
	s.publishJobUpdate(ctx, requeued, time.Now().UTC())
	return true
}
