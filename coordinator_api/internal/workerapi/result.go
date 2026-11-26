package workerapi

import (
	"context"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/evaloutcome"
	"time"

	"github.com/catalystcommunity/app-utils-go/logging"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/jobtelemetry"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/metrics"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/resourcehistory"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/uiapi"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/workerapi/csilapi"
)

// terminalStatuses are the job statuses ReportResult may land a job on.
var terminalStatuses = map[string]bool{"completed": true, "failed": true, "cancelled": true, "timeout": true}

// ReportResult finalizes a job a worker has finished executing: resolves
// the caller's session, verifies the lease belongs to it, guarded-
// transitions the job to its terminal status (mirroring internal/worker/
// corndogs_worker.go's own finalize step), updates the corndogs task
// (Complete/Cancel/fail), publishes the status change, and releases the
// worker_leases row. Always idempotent from the worker's perspective: a
// retried ReportResult for an already-finalized lease still returns ok.
func (s *WorkerService) ReportResult(ctx context.Context, req csilapi.ReportResultRequest) (csilapi.ReportResultResponse, error) {
	wkr, _, err := s.resolveSession(ctx)
	if err != nil {
		return csilapi.ReportResultResponse{}, err
	}

	lease, err := s.deps.Store.GetWorkerLeaseByID(ctx, req.LeaseId)
	if err != nil {
		return csilapi.ReportResultResponse{}, leaseLookupError(err, "lease not found for this worker")
	}
	if lease.WorkerID != wkr.WorkerID {
		return csilapi.ReportResultResponse{}, uiapi.NewServiceError("not_found", "lease not found for this worker")
	}

	job, err := s.deps.Store.GetJobByID(ctx, lease.JobID)
	if err != nil {
		return csilapi.ReportResultResponse{}, leaseLookupError(err, "job not found for this lease")
	}

	if !leaseOwnsJob(lease, job) {
		if lease.IsActive() {
			logging.Log.WithFields(map[string]any{"lease_id": lease.LeaseID, "job_id": job.JobID,
				"job_status": job.Status, "worker_id": wkr.WorkerID}).Warn("Discarding result of a lease that no longer owns its job")
			if err := s.deps.Store.ReleaseWorkerLease(ctx, lease.LeaseID, "superseded"); err != nil {
				return csilapi.ReportResultResponse{}, errUnavailable()
			}
			s.secrets.delete(lease.LeaseID)
		}
		return csilapi.ReportResultResponse{Ok: true}, nil
	}
	started, err := executionTimestamp(req.StartedAt, lease)
	if err != nil {
		return csilapi.ReportResultResponse{}, err
	}
	finished, err := executionTimestamp(req.FinishedAt, lease)
	if err != nil {
		return csilapi.ReportResultResponse{}, err
	}
	if job.StartedAt != nil {
		started = job.StartedAt
	}
	if finished != nil && (started == nil || finished.Before(*started)) {
		return csilapi.ReportResultResponse{}, uiapi.NewServiceError("invalid_argument", "finish time must follow the execution start")
	}

	finalStatus := req.Status
	if !terminalStatuses[finalStatus] {
		if req.ExitCode == 0 {
			finalStatus = "completed"
		} else {
			finalStatus = "failed"
		}
	}

	lastError := derefOrEmpty(req.Error)
	if lastError == "" {
		switch finalStatus {
		case "failed":
			lastError = "job execution failed"
		case "cancelled":
			if job.IsKillRequested() {
				lastError = "killed by admin"
			} else {
				lastError = "cancelled"
			}
		}
	}

	exitCode := int(req.ExitCode)
	failureClass, failureDetail, reportedClass := resultFailureClass(finalStatus, exitCode, lastError, req.FailureClass, req.FailureDetail)

	// An infra.platform failure goes back to the queue while the job's
	// retry budget lasts. It never applies to a job that is being
	// cancelled: the cancel wins. A class derived from an older worker's
	// error text is a guess, so only a class the worker reported is
	// retried. A retry runs the job command again, so it applies only
	// when the worker proves that the main container never started
	// (main_started false). An older worker that does not send
	// main_started never gets an automatic retry: an eviction or a lost
	// runtime after the start must not run a deploy twice.
	if reportedClass && neverStarted(req.MainStarted) && finalStatus == "failed" && (job.Status == "scheduling" || job.Status == "running") {
		if retry, budget := s.shouldRetryInfra(ctx, job, failureClass); retry {
			oldTask := job.CorndogsTaskID
			if s.requeueInfraFailure(ctx, job, failureClass, lastError, budget, []string{"scheduling", "running"}, func(j *models.Job) bool {
				return leaseOwnsJob(lease, j)
			}) {
				if s.deps.CorndogsClient != nil && oldTask != nil && *oldTask != "" {
					s.cancelLiveTask(ctx, *oldTask)
				}
				if err := s.deps.Store.ReleaseWorkerLease(ctx, lease.LeaseID, "requeued:"+failureClass); err != nil {
					logging.Log.WithError(err).WithField("lease_id", lease.LeaseID).Warn("Failed to release worker lease of a requeued job")
				}
				s.secrets.delete(lease.LeaseID)
				return csilapi.ReportResultResponse{Ok: true}, nil
			}
		}
	}

	now := time.Now().UTC()
	ownsFinalization := false
	finalized, matched, err := s.deps.Store.UpdateJobStatusGuarded(ctx, job.JobID, []string{"scheduling", "running", "cancelling"}, func(j *models.Job) {
		if !leaseOwnsJob(lease, j) {
			return
		}
		ownsFinalization = true
		j.Status = finalStatus
		j.LastError = lastError
		j.CompletedAt = &now
		if started != nil && j.StartedAt == nil {
			j.StartedAt = started
		}
		if finished != nil {
			j.CompletedAt = finished
		}
		j.ExitCode = &exitCode
		j.SetFailure(failureClass, failureDetail)
	})
	if err != nil {
		logging.Log.WithError(err).WithField("job_id", job.JobID).Error("Failed to finalize job status")
		if store.IsUnavailable(err) {
			return csilapi.ReportResultResponse{}, errUnavailable()
		}
		return csilapi.ReportResultResponse{}, uiapi.NewServiceError("internal", "failed to finalize job")
	}
	if matched && !ownsFinalization {
		return csilapi.ReportResultResponse{Ok: true}, nil
	}
	if matched {
		job = finalized
		metrics.RecordJobFinished(job.Status, job.FailureClassValue(), job.QueueName)
		s.publishJobUpdate(ctx, job, now)
	} else if current, getErr := s.deps.Store.GetJobByID(ctx, job.JobID); getErr == nil && !terminalStatuses[current.Status] && current.Status != "timeout" {
		// The job is not terminal and not ours to finish: the lost-job
		// reconciler requeued it after this lease went stale, and a new
		// claim now owns it (or will). Finishing the corndogs task or the
		// workflow node here would finish the NEW attempt. Only close this
		// lease.
		logging.Log.WithFields(map[string]interface{}{
			"job_id":     job.JobID,
			"lease_id":   lease.LeaseID,
			"job_status": current.Status,
		}).Warn("Ignoring result for a superseded lease")
		if err := s.deps.Store.ReleaseWorkerLease(ctx, lease.LeaseID, "superseded"); err != nil {
			logging.Log.WithError(err).WithField("lease_id", lease.LeaseID).Warn("Failed to release superseded worker lease")
		}
		s.secrets.delete(lease.LeaseID)
		return csilapi.ReportResultResponse{Ok: true}, nil
	}

	s.finalizeCorndogsTask(ctx, job, finalStatus)
	if tokenStore, ok := s.deps.Store.(jobTokenStore); ok {
		if err := tokenStore.RevokeJobTokens(ctx, job.JobID); err != nil {
			logging.Log.WithError(err).WithField("job_id", job.JobID).Warn("Failed to revoke job trigger token")
		}
	}

	// Advance the job's workflow (best-effort): mark this node terminal and
	// re-evaluate the workflow so ready downstream nodes get submitted, the
	// workflow_instances status rolls up (running -> success/failed/... with
	// completed_at), and the VCS check updates. Without this, a workflow's jobs
	// all complete but the workflow instance stays "running" forever. Empty
	// workspaceDir: the coordinator has no access to the job's workspace, so
	// workflow-output.json vars are not merged here -- status/DAG only. Does
	// nothing for non-workflow jobs.
	// Job links (build_settings.go), before the workflow advance so that the
	// PR comment update can show them.
	s.deps.recordJobLinks(ctx, job, req.WorkflowOutput)
	// Artifacts, test reports, and provenance (artifacts.go), before the
	// workflow advance so that the node and the PR comment see the test
	// summary.
	s.processJobOutputs(ctx, lease, job, req.WorkflowOutput)
	if s.deps.WorkflowFinalizer != nil && job.WorkflowID != nil && *job.WorkflowID != "" {
		var completionErr error
		if req.WorkflowOutput != nil {
			if finalizer, ok := s.deps.WorkflowFinalizer.(interface {
				ProcessWorkflowCompletionData(context.Context, []byte, *models.Job) error
			}); ok {
				completionErr = finalizer.ProcessWorkflowCompletionData(ctx, []byte(*req.WorkflowOutput), job)
			} else {
				completionErr = s.deps.WorkflowFinalizer.ProcessWorkflowCompletion(ctx, "", job)
			}
		} else {
			completionErr = s.deps.WorkflowFinalizer.ProcessWorkflowCompletion(ctx, "", job)
		}
		if completionErr != nil {
			logging.Log.WithError(completionErr).WithFields(map[string]interface{}{
				"job_id":      job.JobID,
				"workflow_id": *job.WorkflowID,
			}).Warn("Failed to advance workflow after job completion")
		}
	}

	// Close the eval's CI event first, so the status recompute below reads
	// the final event state.
	markEvalEventFinished(ctx, s.deps.Store, job, finalStatus)

	// Resolve the job's VCS status on completion. An eval of a CI event
	// recomputes the event's "Reactorcide Checks" status; a legacy eval
	// resolves its "reactorcide/eval" status, posted pending at creation.
	// Only for non-node jobs: workflow node jobs are covered by the aggregate
	// workflow status (updated via the finalizer above). UpdateJobStatus is a
	// no-op for jobs without VCS metadata, so standalone jobs are unaffected.
	if s.deps.JobStatusReporter != nil && job.WorkflowNodeID == nil {
		if err := s.deps.JobStatusReporter.UpdateJobStatus(ctx, job); err != nil {
			logging.Log.WithError(err).WithField("job_id", job.JobID).Warn("Failed to update job VCS status on completion")
		}
	}

	if err := s.deps.Store.ReleaseWorkerLease(ctx, lease.LeaseID, finalStatus); err != nil {
		logging.Log.WithError(err).WithField("lease_id", lease.LeaseID).Warn("Failed to release worker lease")
	}
	s.secrets.delete(lease.LeaseID)
	if s.deps.ObjectStore != nil {
		compactCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		if err := jobtelemetry.CompactJob(compactCtx, s.deps.ObjectStore, job.JobID, true); err != nil {
			logging.Log.WithError(err).WithField("job_id", job.JobID).Warn("Failed to compact terminal job telemetry")
		}
		cancel()
		// The run's resource summary, for the job resource history. Best
		// effort: a failure here leaves no row, and the history read computes
		// it later. It never fails the result.
		if summaries, ok := s.deps.Store.(store.ResourceSummaryStore); ok {
			summaryCtx, cancelSummary := context.WithTimeout(context.Background(), 15*time.Second)
			if _, err := resourcehistory.Refresh(summaryCtx, s.deps.ObjectStore, summaries, job.JobID); err != nil {
				logging.Log.WithError(err).WithField("job_id", job.JobID).Warn("Failed to store job resource summary")
			}
			cancelSummary()
		}
	}

	return csilapi.ReportResultResponse{Ok: true}, nil
}

// neverStarted reports whether the worker proved that the main container
// of the job never started. An absent value (an older worker, or a worker
// that cannot prove it) is not proof.
func neverStarted(mainStarted *bool) bool {
	return mainStarted != nil && !*mainStarted
}

// resultFailureClass decides the failure class of a reported result. A
// worker that knows the cause sends it; the class must agree with the
// status (cancelled and timeout have their own class, completed has none).
// A worker older than failure classes sends none, and the class is derived
// from the status, exit code and error text.
//
// reportedClass is true when the class is the worker's own.
func resultFailureClass(status string, exitCode int, lastError string, reported, reportedDetail *string) (class, detail string, reportedClass bool) {
	switch status {
	case "completed":
		return "", "", false
	case "cancelled":
		return models.FailureClassCancelled, "", reported != nil
	case "timeout":
		return models.FailureClassTimeout, "", reported != nil
	}
	detail = derefOrEmpty(reportedDetail)
	if reported != nil && models.IsValidFailureClass(*reported) &&
		*reported != models.FailureClassCancelled && *reported != models.FailureClassTimeout &&
		*reported != models.FailureClassWorkerLost {
		return *reported, detail, true
	}
	return models.DeriveFailureClass(status, &exitCode, lastError), detail, false
}

// finalizeCorndogsTask drives the corndogs task to match a job's terminal
// status, mirroring internal/worker/corndogs_worker.go's own
// Complete/Cancel/fail branching. Best-effort: the job's DB status is
// already authoritative regardless of this call's outcome.
func (s *WorkerService) finalizeCorndogsTask(ctx context.Context, job *models.Job, finalStatus string) {
	if s.deps.CorndogsClient == nil || job.CorndogsTaskID == nil || *job.CorndogsTaskID == "" {
		return
	}
	taskID := *job.CorndogsTaskID
	var err error
	switch finalStatus {
	case "completed":
		_, err = s.deps.CorndogsClient.CompleteTask(ctx, taskID, "processing")
	case "cancelled":
		_, err = s.deps.CorndogsClient.CancelTask(ctx, taskID, "processing")
	default:
		_, err = s.deps.CorndogsClient.UpdateTask(ctx, taskID, "processing", "failed", nil)
	}
	if err != nil {
		logging.Log.WithError(err).WithFields(map[string]interface{}{"job_id": job.JobID, "status": finalStatus}).Warn("Failed to finalize corndogs task")
	}
}

// markEvalEventFinished closes the CI event of an eval job. A successful eval
// that posted triggers already marked the event evaluated; one that found
// nothing to run did not, so success marks it here too. A failed eval marks
// the event failed. Best effort: it never fails the result.
func markEvalEventFinished(ctx context.Context, st any, job *models.Job, finalStatus string) {
	evaloutcome.Finish(ctx, st, job, finalStatus)
}
