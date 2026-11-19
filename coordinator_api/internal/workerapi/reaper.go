package workerapi

import (
	"context"
	"time"

	"github.com/catalystcommunity/app-utils-go/logging"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/metrics"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
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

// maxLostJobRetries is the least number of times the reconciler puts a lost
// job back in the queue before it fails the job (infra.worker_lost). An
// infra_retries setting above it raises the budget; see
// models.InfraRetryBudget. retry_count counts these requeues together with
// the automatic retries of infra.platform failures.
const maxLostJobRetries = models.MinLostJobRetries

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
// or fails it (infra.worker_lost) once its retry budget is used. Every job-row write
// is guarded from "running", so a result that arrives late (and finalizes
// the job first) wins.
func (s *WorkerService) recoverLostJob(ctx context.Context, job *models.Job) {
	if s.deps.CorndogsClient == nil {
		return
	}
	// The old task can come back from its claim timeout. Cancel it where it
	// can still be cancelled; a claim of a task that is not the job's
	// current task discards it anyway (see RequestJob).
	if job.CorndogsTaskID != nil && *job.CorndogsTaskID != "" {
		s.cancelLiveTask(ctx, *job.CorndogsTaskID)
	}

	retry, budget := s.shouldRetryInfra(ctx, job, models.FailureClassWorkerLost)
	if !retry {
		s.failLostJob(ctx, job, models.LostJobErrorPrefix+" no lease after coordinator error; retries exhausted")
		return
	}
	s.requeueInfraFailure(ctx, job, models.FailureClassWorkerLost, "lost with no lease after coordinator error", budget, []string{"scheduling", "running"}, nil)
}

// failLostJob finalizes a lost job as failed and advances everything that
// waits on it: the corndogs task, the job token, the workflow node, and the
// job's own VCS check.
func (s *WorkerService) failLostJob(ctx context.Context, job *models.Job, reason string) {
	now := time.Now().UTC()
	failed, matched, err := s.deps.Store.UpdateJobStatusGuarded(ctx, job.JobID, []string{"scheduling", "running"}, func(j *models.Job) {
		j.Status = "failed"
		j.LastError = reason
		j.CompletedAt = &now
		j.SetFailure(models.FailureClassWorkerLost, "no lease")
	})
	if err != nil || !matched {
		if err != nil {
			logging.Log.WithError(err).WithField("job_id", job.JobID).Warn("Failed to fail lost job")
		}
		return
	}
	logging.Log.WithField("job_id", job.JobID).Warn("Failed lost job: " + reason)
	metrics.RecordJobFinished(failed.Status, failed.FailureClassValue(), failed.QueueName)
	if tokenStore, ok := s.deps.Store.(jobTokenStore); ok {
		if err := tokenStore.RevokeJobTokens(ctx, job.JobID); err != nil {
			logging.Log.WithError(err).WithField("job_id", job.JobID).Warn("Failed to revoke job token of lost job")
		}
	}
	s.publishJobUpdate(ctx, failed, now)
	s.advanceWorkflowAfterCoordinatorFinalization(ctx, failed)
	// A lost eval closes its CI event here: no worker result will arrive.
	// Before the status update, so "Reactorcide Checks" reads the failed event.
	markEvalEventFinished(ctx, s.deps.Store, failed, failed.Status)
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
