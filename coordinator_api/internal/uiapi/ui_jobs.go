package uiapi

import (
	"context"
	"slices"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/imagealias"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/refclass"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/tokencaps"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/uiapi/csilapi"
)

// Job and workflow reads for the UI.
//
// These exist so the browser never needs the coordinator REST API. The webapp
// used to serve the job list and job detail by calling /api/v1/jobs with its
// own SERVICE token: the coordinator's visibility filter then ran against that
// service identity and passed every row, and the webapp did not filter again,
// so a logged-out browser could see private jobs. Every op here resolves the
// CALLER's own identity instead.

// defaultListLimit and maxListLimit bound a list page. The default matches
// handlers/base_handler.go's REST pagination default so the two surfaces
// behave the same.
const (
	defaultListLimit = 20
	maxListLimit     = 200
)

// jobsVisibleToStore is the narrow store capability that pushes the
// visibility predicate into SQL rather than filtering a page in Go. This is
// the repo's consumer-defined-narrow-interface convention, and the same
// interface handlers/job_handler.go declares for the REST path.
//
// It matters for correctness, not only speed: filtering AFTER LIMIT/OFFSET
// returns short pages and a wrong total whenever any row on the page is
// invisible. See postgres_store/visibility_operations.go.
type jobsVisibleToStore interface {
	ListJobsVisibleTo(ctx context.Context, viewerID string, isGlobalAdmin bool, filters map[string]interface{}, limit, offset int) ([]models.Job, int64, error)
}

// listPageBounds clamps a request's optional limit/offset.
func listPageBounds(limit, offset *int64) (int, int) {
	l := defaultListLimit
	if limit != nil && *limit > 0 {
		l = int(*limit)
		if l > maxListLimit {
			l = maxListLimit
		}
	}
	o := 0
	if offset != nil && *offset > 0 {
		o = int(*offset)
	}
	return l, o
}

// viewerScopeFor resolves the caller once into the (viewerID, isGlobalAdmin)
// pair the SQL visibility predicate needs.
//
// An anonymous caller gets ("", false), which is correct rather than a
// special case: the predicate's owner self-match compares against ” (no user
// id is empty) and its role-assignment EXISTS clauses look for a principal id
// of ” (none exists), so an anonymous caller matches only the public
// branches. Anonymous browsing of public jobs keeps working, and nothing
// private leaks.
//
// It also gives the organizations of an API token. A token never sees more than it is given: without capability (jobs:read,
// workflows:read) it sees only what an anonymous caller sees, and with it
// only the organizations of the token (orgIDs; nil means no limit). Callers
// put orgIDs in the "org_ids" filter.
func (s *UiService) viewerScopeFor(ctx context.Context, capability string) (string, bool, []string, error) {
	identity, _ := s.deps.resolveIdentity(ctx)
	if identity.Anonymous {
		return "", false, nil, nil
	}
	if identity.Token != nil && !identity.Token.HasCapability(capability) {
		return "", false, nil, nil
	}
	isGlobalAdmin, err := s.deps.Resolver.IsGlobalAdmin(ctx, identity)
	if err != nil {
		return "", false, nil, NewServiceError("internal", "an internal error occurred")
	}
	var orgIDs []string
	if identity.Token != nil && !identity.Token.AllOrganizations {
		orgIDs = append([]string{}, identity.Token.OrganizationIDs...)
	}
	return identity.UserID, isGlobalAdmin, orgIDs, nil
}

// sourceTypeString renders the optional source type enum. An unset type is
// empty rather than a placeholder: the wire field is `text`, not optional, and
// the UI already treats "" as "not recorded".
func sourceTypeString(t *models.SourceType) string {
	if t == nil {
		return ""
	}
	return string(*t)
}

func jobToSummary(job *models.Job) csilapi.JobSummary {
	summary := csilapi.JobSummary{
		JobId:               job.JobID,
		Name:                job.Name,
		Description:         job.Description,
		Status:              job.Status,
		LastError:           job.LastError,
		CreatedAt:           formatTime(job.CreatedAt),
		UpdatedAt:           formatTime(job.UpdatedAt),
		SchedulingStartedAt: formatTimePtr(job.SchedulingStartedAt),
		StartedAt:           formatTimePtr(job.StartedAt),
		CompletedAt:         formatTimePtr(job.CompletedAt),
		SourceUrl:           derefOr(job.SourceURL, ""),
		SourceRef:           derefOr(job.SourceRef, ""),
		SourceType:          sourceTypeString(job.SourceType),
		SourcePath:          derefOr(job.SourcePath, ""),
		RunnerImage:         job.RunnerImage,
		ImageAlias:          optionalText(derefOr(job.ImageAlias, "")),
		ImageDisplay:        optionalText(imagealias.DisplayJob(job)),
		ImageRetryOf:        optionalText(derefOr(job.ImageRetryOf, "")),
		QueueName:           job.QueueName,
		Priority:            int64(job.Priority),
		PriorityLane:        optionalText(job.PriorityLane),
		ParentJobId:         job.ParentJobID,
		ProjectId:           job.ProjectID,
		WorkflowId:          job.WorkflowID,
		WorkflowNodeName:    job.WorkflowNodeName,
		CiOrigin:            job.CIOrigin,
		ExecutionProfile:    job.ExecutionProfile,
		WorkerClass:         job.WorkerClass,
	}
	if job.PriorityReason != nil {
		reason := reasonToCSIL(*job.PriorityReason)
		summary.PriorityReason = &reason
	}
	if job.ExitCode != nil {
		code := int64(*job.ExitCode)
		summary.ExitCode = &code
	}
	if class := job.FailureClassValue(); class != "" {
		summary.FailureClass = &class
		if owner := models.FailureOwner(class); owner != "" {
			summary.FailureOwner = &owner
		}
		if job.FailureDetail != "" {
			detail := job.FailureDetail
			summary.FailureDetail = &detail
		}
	}
	if job.RetryCount > 0 {
		retries := int64(job.RetryCount)
		summary.RetryCount = &retries
	}
	summary.Actor = actorToWire(job.ActorPrincipalID, job.ActorIdentity)
	summary.RefClasses = refclass.ForJob(job)
	summary.Shifts = shiftsToWire(job.Shifts)
	summary.Owner = ownershipToWire(job.Ownership())
	return summary
}

// ListJobs returns the page of jobs visible to the caller, with an exact
// total. Filters are applied together with the visibility predicate in SQL.
func (s *UiService) ListJobs(ctx context.Context, req csilapi.ListJobsRequest) (csilapi.ListJobsResponse, error) {
	visibleStore, ok := s.deps.Store.(jobsVisibleToStore)
	if !ok {
		return csilapi.ListJobsResponse{}, NewServiceError("internal", "job listing is not available on this server")
	}
	viewerID, isGlobalAdmin, orgIDs, err := s.viewerScopeFor(ctx, tokencaps.JobsRead)
	if err != nil {
		return csilapi.ListJobsResponse{}, err
	}
	limit, offset := listPageBounds(req.Limit, req.Offset)

	filters := map[string]interface{}{}
	if orgIDs != nil {
		filters["org_ids"] = orgIDs
	}
	if req.Status != nil && *req.Status != "" {
		filters["status"] = *req.Status
	}
	if req.ProjectId != nil && *req.ProjectId != "" {
		filters["project_id"] = *req.ProjectId
	}
	if req.WorkflowId != nil && *req.WorkflowId != "" {
		filters["workflow_id"] = *req.WorkflowId
	}
	if req.QueueName != nil && *req.QueueName != "" {
		filters["queue_name"] = *req.QueueName
	}
	if req.FailureClass != nil && *req.FailureClass != "" {
		if !slices.Contains(models.FailureClasses, *req.FailureClass) {
			return csilapi.ListJobsResponse{}, NewServiceError("invalid_argument", "failure_class is not a known failure class")
		}
		filters["failure_class"] = *req.FailureClass
	}

	jobs, total, err := visibleStore.ListJobsVisibleTo(ctx, viewerID, isGlobalAdmin, filters, limit, offset)
	if err != nil {
		return csilapi.ListJobsResponse{}, NewServiceError("internal", "failed to list jobs")
	}
	response := csilapi.ListJobsResponse{
		Jobs:   make([]csilapi.JobSummary, 0, len(jobs)),
		Total:  total,
		Limit:  int64(limit),
		Offset: int64(offset),
	}
	for i := range jobs {
		response.Jobs = append(response.Jobs, jobToSummary(&jobs[i]))
	}
	s.describeActors(ctx, jobActors(response.Jobs)...)
	return response, nil
}

// GetJob returns one job after the caller passes the same visibility rule the
// list applies.
//
// A job the caller cannot view reports "not found", not "forbidden".
// "Forbidden" on a by-id lookup confirms the id exists, which turns this op
// into an existence oracle for private jobs.
func (s *UiService) GetJob(ctx context.Context, req csilapi.GetJobRequest) (csilapi.GetJobResponse, error) {
	if err := requireNonEmpty("job_id", req.JobId, 64); err != nil {
		return csilapi.GetJobResponse{}, err
	}
	identity, _ := s.deps.resolveIdentity(ctx)
	job, err := s.deps.Store.GetJobByID(ctx, req.JobId)
	if err != nil {
		return csilapi.GetJobResponse{}, mapStoreErr(err, "job not found")
	}
	visible, err := s.deps.Resolver.CanViewJob(ctx, identity, job)
	if err != nil {
		return csilapi.GetJobResponse{}, NewServiceError("internal", "an internal error occurred")
	}
	if !visible {
		return csilapi.GetJobResponse{}, NewServiceError("not_found", "job not found")
	}
	summary := jobToSummary(job)
	s.describeActors(ctx, summary.Actor)
	return csilapi.GetJobResponse{Job: summary}, nil
}
