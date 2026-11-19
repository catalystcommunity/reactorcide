package uiapi

import (
	"context"
	"time"

	"github.com/catalystcommunity/app-utils-go/logging"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/jobtelemetry"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/resourcehistory"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/resources"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/uiapi/csilapi"
)

const (
	defaultResourceHistoryLimit = 200
	maxResourceHistoryLimit     = 1000
	// A history read computes at most this many missing summaries, inside
	// this budget, and reports the rest as pending. Each one reads every
	// telemetry object of a run, so an unbounded read of a job with a long
	// history from before summaries existed could take minutes.
	resourceHistoryRefreshPerRequest = 10
	resourceHistoryRefreshBudget     = 3 * time.Second
)

// GetJobResourceHistory returns the resource summaries of the finished runs of
// one job: every run with the same organization, project and name as job_id.
//
// Authorization is the reference job's visibility, the same rule as
// GetJobMetrics. Every returned run has the same organization and project, so
// the same rule covers them all.
func (s *UiService) GetJobResourceHistory(ctx context.Context, req csilapi.GetJobResourceHistoryRequest) (csilapi.GetJobResourceHistoryResponse, error) {
	identity, _ := s.deps.resolveIdentity(ctx)
	job, err := s.deps.Store.GetJobByID(ctx, req.JobId)
	if err != nil {
		return csilapi.GetJobResourceHistoryResponse{}, mapStoreErr(err, "job not found")
	}
	visible, err := s.deps.Resolver.CanViewJob(ctx, identity, job)
	if err != nil {
		return csilapi.GetJobResourceHistoryResponse{}, NewServiceError("internal", "an internal error occurred")
	}
	// not_found, not forbidden: see GetJobMetrics.
	if !visible {
		return csilapi.GetJobResourceHistoryResponse{}, NewServiceError("not_found", "job not found")
	}
	from, err := jobtelemetry.ParseOptionalTime(derefOr(req.FromTime, ""))
	if err != nil {
		return csilapi.GetJobResourceHistoryResponse{}, NewServiceError("invalid_argument", "from_time must be an RFC3339 timestamp")
	}
	to, err := jobtelemetry.ParseOptionalTime(derefOr(req.ToTime, ""))
	if err != nil {
		return csilapi.GetJobResourceHistoryResponse{}, NewServiceError("invalid_argument", "to_time must be an RFC3339 timestamp")
	}
	if from != nil && to != nil && from.After(*to) {
		return csilapi.GetJobResourceHistoryResponse{}, NewServiceError("invalid_argument", "from_time must not be after to_time")
	}
	limit := defaultResourceHistoryLimit
	if req.Limit != nil {
		if *req.Limit < 1 || *req.Limit > maxResourceHistoryLimit {
			return csilapi.GetJobResourceHistoryResponse{}, NewServiceError("invalid_argument", "limit must be between 1 and 1000")
		}
		limit = int(*req.Limit)
	}

	runs, err := s.deps.Store.ListJobRunsWithSummaries(ctx, store.JobRunHistoryFilter{
		OrgID: job.OrgID, ProjectID: job.ProjectID, Name: job.Name,
		From: from, To: to, Limit: limit,
	})
	if err != nil {
		logging.Log.WithError(err).WithField("job_id", job.JobID).Warn("Failed to list job runs for resource history")
		return csilapi.GetJobResourceHistoryResponse{}, NewServiceError("internal", "failed to read job resource history")
	}

	pending := s.refreshMissingSummaries(ctx, runs)

	response := csilapi.GetJobResourceHistoryResponse{
		ProjectId: job.ProjectID,
		Name:      job.Name,
		Runs:      make([]csilapi.JobRunResourceSummary, 0, len(runs)),
		Pending:   int64(pending),
		// Suggest reads the runs newest first, as the store returns them.
		Suggestion: resourceSuggestionToCsil(resourcehistory.Suggest(runs)),
	}
	// The store returns newest first; the response is oldest first. A run
	// with no current summary is left out: it is counted in pending.
	for i := len(runs) - 1; i >= 0; i-- {
		if !resourcehistory.Current(runs[i].Summary) {
			continue
		}
		response.Runs = append(response.Runs, runResourceSummary(&runs[i].Job, runs[i].Summary))
	}
	return response, nil
}

// refreshMissingSummaries computes, in place, the summaries of the newest runs
// that have none (or one from an older algorithm), inside the per-request
// bound. It returns how many runs still have no current summary.
func (s *UiService) refreshMissingSummaries(ctx context.Context, runs []models.JobRunWithSummary) int {
	if s.deps.ObjectStore == nil {
		pending := 0
		for _, run := range runs {
			if !resourcehistory.Current(run.Summary) {
				pending++
			}
		}
		return pending
	}
	budgetCtx, cancel := context.WithTimeout(ctx, resourceHistoryRefreshBudget)
	defer cancel()
	pending, refreshed := 0, 0
	for i := range runs {
		if resourcehistory.Current(runs[i].Summary) {
			continue
		}
		if refreshed >= resourceHistoryRefreshPerRequest || budgetCtx.Err() != nil {
			pending++
			continue
		}
		refreshed++
		summary, err := resourcehistory.Refresh(budgetCtx, s.deps.ObjectStore, s.deps.Store, runs[i].Job.JobID)
		if err != nil {
			if budgetCtx.Err() == nil {
				logging.Log.WithError(err).WithField("job_id", runs[i].Job.JobID).Warn("Failed to compute job resource summary")
			}
			pending++
			continue
		}
		runs[i].Summary = summary
	}
	return pending
}

func runResourceSummary(job *models.Job, summary *models.JobResourceSummary) csilapi.JobRunResourceSummary {
	out := csilapi.JobRunResourceSummary{
		JobId:                job.JobID,
		Status:               job.Status,
		CreatedAt:            formatTime(job.CreatedAt),
		StartedAt:            formatTimePtr(job.StartedAt),
		CompletedAt:          formatTimePtr(job.CompletedAt),
		SourceRef:            job.SourceRef,
		Capabilities:         []string(job.Capabilities),
		SampleCount:          int64(summary.SampleCount),
		CpuAvgMillicores:     summary.CPUAvgMillicores,
		CpuP95Millicores:     summary.CPUP95Millicores,
		CpuMaxMillicores:     summary.CPUMaxMillicores,
		CpuPeakAt:            formatTimePtr(summary.CPUPeakAt),
		CpuRequestMillicores: summary.CPURequestMillicores,
		CpuLimitMillicores:   summary.CPULimitMillicores,
		MemoryAvgBytes:       summary.MemoryAvgBytes,
		MemoryP95Bytes:       summary.MemoryP95Bytes,
		MemoryMaxBytes:       summary.MemoryMaxBytes,
		MemoryPeakAt:         formatTimePtr(summary.MemoryPeakAt),
		MemoryLimitBytes:     summary.MemoryLimitBytes,
		StorageMaxBytes:      summary.StorageMaxBytes,
	}
	if job.StartedAt != nil && job.CompletedAt != nil && !job.CompletedAt.Before(*job.StartedAt) {
		seconds := int64(job.CompletedAt.Sub(*job.StartedAt) / time.Second)
		out.DurationSeconds = &seconds
	}
	// Requests and limits are configuration, not measurements. When the
	// runtime reported none (Docker and containerd report no request), use
	// what the job asked for, so "requested versus used" has both sides.
	// Jobs have no memory request, so that one stays absent.
	if out.CpuRequestMillicores == nil {
		out.CpuRequestMillicores = parsedQuantity(job.ResourceCPURequest, resources.CPUMillicores)
	}
	if out.CpuLimitMillicores == nil {
		out.CpuLimitMillicores = parsedQuantity(job.ResourceCPULimit, resources.CPUMillicores)
	}
	if out.MemoryLimitBytes == nil {
		out.MemoryLimitBytes = parsedQuantity(job.ResourceMemoryLimit, resources.MemoryBytes)
	}
	return out
}

func parsedQuantity(value string, parse func(string) (int64, error)) *int64 {
	if value == "" {
		return nil
	}
	parsed, err := parse(value)
	if err != nil || parsed <= 0 {
		return nil
	}
	return &parsed
}
