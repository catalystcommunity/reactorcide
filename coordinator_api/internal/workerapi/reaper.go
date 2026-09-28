package workerapi

import (
	"context"
	"time"

	"github.com/catalystcommunity/app-utils-go/logging"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/worker"
)

// leaseReapInterval is how often the lease reaper scans for stale open
// worker_leases rows: once immediately on Start, then on this ticker --
// mirroring internal/worker/corndogs_worker.go's runCancellingReaper cadence.
const leaseReapInterval = 60 * time.Second

// leaseStaleAfter bounds how old an open lease's acquired_at must be before
// the reaper treats it as orphaned. Generous relative to HeartbeatInterval: a
// lease this old with no release almost certainly belongs to a worker that
// stopped heartbeating, whose corndogs task has ALREADY timed out and
// requeued on its own. The reaper's only job is bookkeeping: mark the
// display/audit row released so it stops showing as an open lease for a
// worker that is, in fact, gone.
const leaseStaleAfter = 15 * time.Minute

// lostJobGrace is how long a job may sit at running with no lease (after it
// started, or after its last lease was released) before the reconciler
// treats it as lost. Longer than one claim commit and one ReportResult.
const lostJobGrace = 2 * time.Minute

// maxLostJobRetries is how many times the reconciler puts a lost job back in
// the queue before it fails the job. retry_count counts these requeues.
const maxLostJobRetries = 2

// lostJobStore is the optional store surface of the lost-job reconciler.
// PostgresDbStore implements it (job_guarded_operations.go).
type lostJobStore interface {
	ListLostRunningJobs(ctx context.Context, cutoff time.Time) ([]models.Job, error)
	ListFinalJobsWithOpenWorkflowNode(ctx context.Context, cutoff time.Time) ([]models.Job, error)
}

// RunLeaseReaper drives reapStaleLeases and reconcileLostJobs on
// leaseReapInterval until ctx is cancelled, running once immediately on
// entry. Wire this alongside the coordinator's other background loops (see
// handlers/router.go). Stale-lease reaping is bookkeeping only; the
// reconciler repairs the job rows that no worker and no claim will ever
// drive again.
func (s *WorkerService) RunLeaseReaper(ctx context.Context) {
	s.reapStaleLeases(ctx)
	s.reconcileLostJobs(ctx)

	ticker := time.NewTicker(leaseReapInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.reapStaleLeases(ctx)
			s.reconcileLostJobs(ctx)
		}
	}
}

// reapStaleLeases marks every worker_leases row whose worker stopped
// heartbeating before leaseStaleAfter as released with outcome
// "reaped:stale", and drops its cached secret values (see leaseSecretCache).
// It never touches corndogs or the job row. The job is still at running, and
// a claim accepts only submitted/queued, so a requeued corndogs task cannot
// drive it: reconcileLostJobs repairs the job once lostJobGrace has passed.
func (s *WorkerService) reapStaleLeases(ctx context.Context) {
	threshold := time.Now().Add(-leaseStaleAfter)
	stale, err := s.deps.Store.ListStaleActiveLeases(ctx, threshold)
	if err != nil {
		logging.Log.WithError(err).Warn("Failed to list stale active worker leases for reaper")
		return
	}

	for i := range stale {
		lease := &stale[i]
		if err := s.deps.Store.ReleaseWorkerLease(ctx, lease.LeaseID, "reaped:stale"); err != nil {
			logging.Log.WithError(err).WithField("lease_id", lease.LeaseID).Warn("Failed to release stale worker lease")
			continue
		}
		s.secrets.delete(lease.LeaseID)
		logging.Log.WithFields(map[string]interface{}{
			"lease_id":  lease.LeaseID,
			"worker_id": lease.WorkerID,
			"job_id":    lease.JobID,
		}).Warn("Reaped stale worker lease with no active heartbeat")
	}
}

// reconcileLostJobs repairs two kinds of state that nothing else ever
// finishes:
//
//   - a job at running that no worker holds (no open lease for lostJobGrace):
//     see recoverLostJob.
//   - a terminal job whose workflow node is still submitted or running: the
//     node gets its completion again, so the workflow instance and its VCS
//     check reach a final state.
func (s *WorkerService) reconcileLostJobs(ctx context.Context) {
	ls, ok := s.deps.Store.(lostJobStore)
	if !ok {
		return
	}
	cutoff := time.Now().Add(-lostJobGrace)

	lost, err := ls.ListLostRunningJobs(ctx, cutoff)
	if err != nil {
		logging.Log.WithError(err).Warn("Failed to list lost running jobs for reconciler")
	}
	for i := range lost {
		s.recoverLostJob(ctx, &lost[i])
	}

	final, err := ls.ListFinalJobsWithOpenWorkflowNode(ctx, cutoff)
	if err != nil {
		logging.Log.WithError(err).Warn("Failed to list final jobs with open workflow nodes for reconciler")
	}
	for i := range final {
		logging.Log.WithFields(map[string]interface{}{"job_id": final[i].JobID, "job_status": final[i].Status}).Warn("Completing workflow node of a final job")
		s.advanceWorkflowAfterCoordinatorFinalization(ctx, &final[i])
	}
}

// recoverLostJob puts a lost job back in the queue with a new corndogs task,
// or fails it once maxLostJobRetries requeues are used. Every job-row write
// is guarded from "running", so a result that arrives late (and finalizes
// the job first) wins.
func (s *WorkerService) recoverLostJob(ctx context.Context, job *models.Job) {
	logger := logging.Log.WithFields(map[string]interface{}{"job_id": job.JobID, "retry_count": job.RetryCount})
	if s.deps.CorndogsClient == nil {
		return
	}
	// The old task can come back from its claim timeout. Cancel it where it
	// can still be cancelled; a claim of a task that is not the job's
	// current task discards it anyway (see RequestJob).
	if job.CorndogsTaskID != nil && *job.CorndogsTaskID != "" {
		s.cancelLiveTask(ctx, *job.CorndogsTaskID)
	}

	if job.RetryCount >= maxLostJobRetries {
		s.failLostJob(ctx, job, "lost: no lease after coordinator error; retries exhausted")
		return
	}

	requeued, matched, err := s.deps.Store.UpdateJobStatusGuarded(ctx, job.JobID, []string{"running"}, func(j *models.Job) {
		j.Status = "submitted"
		j.StartedAt = nil
		j.WorkerID = nil
		j.CorndogsTaskID = nil
		j.RetryCount++
		j.LastError = "requeued: lost with no lease after coordinator error"
	})
	if err != nil || !matched {
		if err != nil {
			logger.WithError(err).Warn("Failed to requeue lost job")
		}
		return
	}

	task, err := s.deps.CorndogsClient.SubmitTaskToQueue(ctx, requeued.QueueName, worker.BuildTaskPayload(requeued), int64(requeued.Priority))
	if err != nil {
		// Back to running: the next pass tries again, and retry_count
		// already counts this attempt.
		logger.WithError(err).Warn("Failed to submit replacement corndogs task for lost job")
		if _, _, revertErr := s.deps.Store.UpdateJobStatusGuarded(ctx, job.JobID, []string{"submitted"}, func(j *models.Job) {
			j.Status = "running"
			j.LastError = "lost: replacement task submission failed"
		}); revertErr != nil {
			logger.WithError(revertErr).Warn("Failed to return lost job to running after submission failure")
		}
		return
	}
	taskID := task.Uuid
	if _, _, err := s.deps.Store.UpdateJobStatusGuarded(ctx, job.JobID, []string{"submitted", "queued"}, func(j *models.Job) {
		if j.CorndogsTaskID == nil || *j.CorndogsTaskID == "" {
			j.CorndogsTaskID = &taskID
		}
	}); err != nil {
		logger.WithError(err).WithField("task_id", taskID).Warn("Failed to record replacement corndogs task on lost job")
	}
	logger.WithField("task_id", taskID).Warn("Requeued lost job with a new corndogs task")
	s.publishJobUpdate(ctx, requeued, time.Now().UTC())
}

// failLostJob finalizes a lost job as failed and advances everything that
// waits on it: the corndogs task, the job token, the workflow node, and the
// job's own VCS check.
func (s *WorkerService) failLostJob(ctx context.Context, job *models.Job, reason string) {
	now := time.Now().UTC()
	failed, matched, err := s.deps.Store.UpdateJobStatusGuarded(ctx, job.JobID, []string{"running"}, func(j *models.Job) {
		j.Status = "failed"
		j.LastError = reason
		j.CompletedAt = &now
	})
	if err != nil || !matched {
		if err != nil {
			logging.Log.WithError(err).WithField("job_id", job.JobID).Warn("Failed to fail lost job")
		}
		return
	}
	logging.Log.WithField("job_id", job.JobID).Warn("Failed lost job: " + reason)
	if tokenStore, ok := s.deps.Store.(jobTokenStore); ok {
		if err := tokenStore.RevokeJobTokens(ctx, job.JobID); err != nil {
			logging.Log.WithError(err).WithField("job_id", job.JobID).Warn("Failed to revoke job token of lost job")
		}
	}
	s.publishJobUpdate(ctx, failed, now)
	s.advanceWorkflowAfterCoordinatorFinalization(ctx, failed)
	if s.deps.JobStatusReporter != nil && failed.WorkflowNodeID == nil {
		if err := s.deps.JobStatusReporter.UpdateJobStatus(ctx, failed); err != nil {
			logging.Log.WithError(err).WithField("job_id", job.JobID).Warn("Failed to update VCS status of lost job")
		}
	}
}

// liveTaskStates are the corndogs states a reactorcide task holds before it
// finishes: queued, claimed (GetNextTaskGroup's auto target), and
// processing (after RequestJob).
var liveTaskStates = []string{"processing", "submitted-working", "submitted"}

// cancelLiveTask cancels a task in whichever live state it is in. Corndogs
// cancels only on a current-state match, and GetTaskByID reads only the
// default queue, so each live state is tried in turn. A task that is already
// finished (or gone) matches none of them, which is the wanted result.
func (s *WorkerService) cancelLiveTask(ctx context.Context, taskID string) {
	for _, state := range liveTaskStates {
		if _, err := s.deps.CorndogsClient.CancelTask(ctx, taskID, state); err == nil {
			return
		}
	}
}
