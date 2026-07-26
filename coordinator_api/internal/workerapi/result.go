package workerapi

import (
	"context"
	"time"

	"github.com/catalystcommunity/app-utils-go/logging"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/uiapi"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/workerapi/csilapi"
)

// terminalStatuses are the job statuses ReportResult may land a job on.
var terminalStatuses = map[string]bool{"completed": true, "failed": true, "cancelled": true}

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
	if err != nil || lease.WorkerID != wkr.WorkerID {
		return csilapi.ReportResultResponse{}, uiapi.NewServiceError("not_found", "lease not found for this worker")
	}

	job, err := s.deps.Store.GetJobByID(ctx, lease.JobID)
	if err != nil {
		return csilapi.ReportResultResponse{}, uiapi.NewServiceError("not_found", "job not found for this lease")
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

	now := time.Now().UTC()
	exitCode := int(req.ExitCode)
	finalized, matched, err := s.deps.Store.UpdateJobStatusGuarded(ctx, job.JobID, []string{"running", "cancelling"}, func(j *models.Job) {
		j.Status = finalStatus
		j.LastError = lastError
		j.CompletedAt = &now
		j.ExitCode = &exitCode
	})
	if err != nil {
		logging.Log.WithError(err).WithField("job_id", job.JobID).Error("Failed to finalize job status")
		return csilapi.ReportResultResponse{}, uiapi.NewServiceError("internal", "failed to finalize job")
	}
	if matched {
		job = finalized
		s.publishJobUpdate(ctx, job, now)
	}

	s.finalizeCorndogsTask(ctx, job, finalStatus)

	if err := s.deps.Store.ReleaseWorkerLease(ctx, lease.LeaseID, finalStatus); err != nil {
		logging.Log.WithError(err).WithField("lease_id", lease.LeaseID).Warn("Failed to release worker lease")
	}
	s.secrets.delete(lease.LeaseID)

	return csilapi.ReportResultResponse{Ok: true}, nil
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
