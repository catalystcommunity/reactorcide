package uiapi

import (
	"context"
	"errors"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/authz"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/jobcontrol"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/uiapi/csilapi"
)

// mapJobControlErr maps a jobcontrol package error to a ServiceErr.
func mapJobControlErr(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, jobcontrol.ErrNotCancellable) {
		return NewServiceError("conflict", "job cannot be cancelled in its current state")
	}
	if errors.Is(err, jobcontrol.ErrNotRetryable) {
		return NewServiceError("conflict", "job cannot be retried in its current state")
	}
	if errors.Is(err, jobcontrol.ErrWorkflowNotRetryable) {
		return NewServiceError("conflict", "workflow cannot be retried in its current state")
	}
	if errors.Is(err, jobcontrol.ErrWorkflowsUnsupported) {
		return NewServiceError("internal", "workflows are not supported by this server's store configuration")
	}
	return NewServiceError("internal", "an internal error occurred")
}

// jobControlScope is the authz.Scope every job-control op authorizes at.
//
// When the resource has a project, only ProjectID is set, so that
// authz.Capabilities resolves the owning org from the project itself
// (project.OwnershipOrgID(), the org the projects table actually records).
// Capabilities skips that resolution whenever Scope.OrgID is non-nil, even
// when it points at an empty string (see the `orgID == nil && scope.ProjectID
// != nil` branch in authz/capabilities.go). The previous code passed
// `OrgID: &job.UserID`; a service-token-created job has an empty user_id, so
// the org resolved to "" and an org admin of the project's owning org was
// denied cancel/retry/kill on it.
//
// A project-less resource falls back to its OwnershipOrgID (org_id, or the
// legacy user_id column when org_id is empty). A resource with neither gets
// the global scope, where only a global admin holds any capability.
func jobControlScope(projectID *string, ownershipOrgID string) authz.Scope {
	if projectID != nil && *projectID != "" {
		return authz.Scope{ProjectID: projectID}
	}
	if ownershipOrgID != "" {
		org := ownershipOrgID
		return authz.Scope{OrgID: &org}
	}
	return authz.Scope{}
}

// jobScope is jobControlScope for a job.
func jobScope(job *models.Job) authz.Scope {
	return jobControlScope(job.ProjectID, job.OwnershipOrgID())
}

// workflowScope is jobControlScope for a workflow instance.
func workflowScope(wf *models.WorkflowInstance) authz.Scope {
	return jobControlScope(wf.ProjectID, wf.OwnershipOrgID())
}

// requireJobVisible answers "not found" for a job the caller cannot view.
// Every job-control op runs this BEFORE its capability check: a "forbidden"
// on a by-id op confirms the id exists, which turns the op into an existence
// oracle for jobs in private projects. This matches GetJob (ui_jobs.go).
// It also closes the mode-none hole where an anonymous caller (who may
// cancel/retry by design) could act on a job in a private project it could
// not even list.
func (s *UiService) requireJobVisible(ctx context.Context, id authz.Identity, job *models.Job) error {
	visible, err := s.deps.Resolver.CanViewJob(ctx, id, job)
	if err != nil {
		return NewServiceError("internal", "an internal error occurred")
	}
	if !visible {
		return NewServiceError("not_found", "job not found")
	}
	return nil
}

// requireWorkflowVisible is requireJobVisible for a workflow instance; see
// GetWorkflow (ui_workflows.go) for the matching read-side rule.
func (s *UiService) requireWorkflowVisible(ctx context.Context, id authz.Identity, wf *models.WorkflowInstance) error {
	visible, err := s.deps.Resolver.CanViewWorkflowInstance(ctx, id, wf)
	if err != nil {
		return NewServiceError("internal", "an internal error occurred")
	}
	if !visible {
		return NewServiceError("not_found", "workflow not found")
	}
	return nil
}

// CancelJob requests a graceful cancel (cleanup hooks run).CurrentMode);
// everywhere else the caller needs at least project owner (of the job's
// project) or org admin (of the job's org) — exactly what
// authz.Capabilities.Cancel already encodes, so this is a single Capabilities
// check with no separate anonymous-mode branch needed.
func (s *UiService) CancelJob(ctx context.Context, req csilapi.CancelJobRequest) (csilapi.CancelJobResponse, error) {
	if err := requireNonEmpty("job_id", req.JobId, 64); err != nil {
		return csilapi.CancelJobResponse{}, err
	}
	job, err := s.deps.Store.GetJobByID(ctx, req.JobId)
	if err != nil {
		return csilapi.CancelJobResponse{}, mapStoreErr(err, "job not found")
	}

	id, _ := s.deps.resolveIdentity(ctx)
	if err := s.requireJobVisible(ctx, id, job); err != nil {
		return csilapi.CancelJobResponse{}, err
	}
	caps, err := s.deps.Resolver.Capabilities(ctx, id, jobScope(job))
	if err != nil {
		return csilapi.CancelJobResponse{}, NewServiceError("internal", "an internal error occurred")
	}
	if !caps.Cancel {
		return csilapi.CancelJobResponse{}, NewServiceError("forbidden", "you do not have permission to cancel this job")
	}

	updated, err := jobcontrol.CancelJob(ctx, s.deps.Store, s.deps.CorndogsClient, job)
	if err != nil {
		return csilapi.CancelJobResponse{}, mapJobControlErr(err)
	}
	return csilapi.CancelJobResponse{JobId: updated.JobID, Status: updated.Status}, nil
}

// KillJob requests an immediate forced kill (no cleanup guarantee). Always
// requires org admin (of the job's owning org) or global admin — never
// available to an anonymous caller in any auth mode, and never to a project
// owner: authz.Caps.Kill is granted only by orgAdminCaps() (see
// authz/capabilities.go), which the anonymous mode-none row and the
// project-owner row do not receive. Using Capabilities here, rather than
// RequireOrgAdmin(job.UserID), lets the owning org be resolved from the
// job's project (see jobControlScope).
func (s *UiService) KillJob(ctx context.Context, req csilapi.KillJobRequest) (csilapi.KillJobResponse, error) {
	if err := requireNonEmpty("job_id", req.JobId, 64); err != nil {
		return csilapi.KillJobResponse{}, err
	}
	job, err := s.deps.Store.GetJobByID(ctx, req.JobId)
	if err != nil {
		return csilapi.KillJobResponse{}, mapStoreErr(err, "job not found")
	}

	id, _ := s.deps.resolveIdentity(ctx)
	if err := s.requireJobVisible(ctx, id, job); err != nil {
		return csilapi.KillJobResponse{}, err
	}
	caps, err := s.deps.Resolver.Capabilities(ctx, id, jobScope(job))
	if err != nil {
		return csilapi.KillJobResponse{}, NewServiceError("internal", "an internal error occurred")
	}
	if !caps.Kill {
		return csilapi.KillJobResponse{}, NewServiceError("forbidden", "you do not have permission to kill this job")
	}

	updated, err := jobcontrol.KillJob(ctx, s.deps.Store, s.deps.CorndogsClient, job)
	if err != nil {
		return csilapi.KillJobResponse{}, mapJobControlErr(err)
	}
	return csilapi.KillJobResponse{JobId: updated.JobID, Status: updated.Status}, nil
}

// workflowInstanceGetter is the narrow store capability CancelWorkflow needs
// to load a workflow instance for the pre-mutation authz check, mirroring
// handlers/workflow_handler.go's own workflowInstanceGetter (workflow
// persistence is a postgres_store-only capability reached via type assertion,
// not part of store.Store or this package's DataStore).
type workflowInstanceGetter interface {
	GetWorkflowInstance(ctx context.Context, workflowID string) (*models.WorkflowInstance, error)
}

// CancelWorkflow requests a graceful cancel of every non-terminal node's job
// in a workflow. Authorization mirrors CancelJob's Capabilities.Cancel check,
// scoped to the workflow's own org/project.
func (s *UiService) CancelWorkflow(ctx context.Context, req csilapi.CancelWorkflowRequest) (csilapi.CancelWorkflowResponse, error) {
	if err := requireNonEmpty("workflow_instance_id", req.WorkflowInstanceId, 64); err != nil {
		return csilapi.CancelWorkflowResponse{}, err
	}

	wig, ok := s.deps.Store.(workflowInstanceGetter)
	if !ok {
		return csilapi.CancelWorkflowResponse{}, NewServiceError("internal", "workflows are not supported by this server's store configuration")
	}
	wf, err := wig.GetWorkflowInstance(ctx, req.WorkflowInstanceId)
	if err != nil {
		return csilapi.CancelWorkflowResponse{}, mapStoreErr(err, "workflow not found")
	}

	id, _ := s.deps.resolveIdentity(ctx)
	if err := s.requireWorkflowVisible(ctx, id, wf); err != nil {
		return csilapi.CancelWorkflowResponse{}, err
	}
	caps, err := s.deps.Resolver.Capabilities(ctx, id, workflowScope(wf))
	if err != nil {
		return csilapi.CancelWorkflowResponse{}, NewServiceError("internal", "an internal error occurred")
	}
	if !caps.Cancel {
		return csilapi.CancelWorkflowResponse{}, NewServiceError("forbidden", "you do not have permission to cancel this workflow")
	}

	updated, err := jobcontrol.CancelWorkflow(ctx, s.deps.Store, s.deps.CorndogsClient, req.WorkflowInstanceId, false)
	if err != nil {
		return csilapi.CancelWorkflowResponse{}, mapJobControlErr(err)
	}
	return csilapi.CancelWorkflowResponse{WorkflowInstanceId: updated.WorkflowID, Status: updated.Status}, nil
}

// RetryJob requests a retry of a single failed/cancelled job, in place (same
// workflow/node, if any) — see jobcontrol.RetryJob. Authorization is
// identical to CancelJob's: the same permission tier (project owner/org
// admin/global admin, plus anonymous in REACTORCIDE_UI_AUTH_MODE=none) is
// exposed as its own authz.Caps.Retry so the UI can gate the retry button
// independently of the cancel button.
func (s *UiService) RetryJob(ctx context.Context, req csilapi.RetryJobRequest) (csilapi.RetryJobResponse, error) {
	if err := requireNonEmpty("job_id", req.JobId, 64); err != nil {
		return csilapi.RetryJobResponse{}, err
	}
	job, err := s.deps.Store.GetJobByID(ctx, req.JobId)
	if err != nil {
		return csilapi.RetryJobResponse{}, mapStoreErr(err, "job not found")
	}

	id, _ := s.deps.resolveIdentity(ctx)
	if err := s.requireJobVisible(ctx, id, job); err != nil {
		return csilapi.RetryJobResponse{}, err
	}
	caps, err := s.deps.Resolver.Capabilities(ctx, id, jobScope(job))
	if err != nil {
		return csilapi.RetryJobResponse{}, NewServiceError("internal", "an internal error occurred")
	}
	if !caps.Retry {
		return csilapi.RetryJobResponse{}, NewServiceError("forbidden", "you do not have permission to retry this job")
	}

	newJob, err := jobcontrol.RetryJob(ctx, s.deps.Store, s.deps.CorndogsClient, job)
	if err != nil {
		return csilapi.RetryJobResponse{}, mapJobControlErr(err)
	}
	return csilapi.RetryJobResponse{JobId: newJob.JobID, Status: newJob.Status}, nil
}

// RetryWorkflow requests a retry of an entire failed/cancelled workflow as a
// brand-new instance (old instance/nodes/jobs untouched) — see
// jobcontrol.RetryWorkflow. Authorization mirrors CancelWorkflow's
// Capabilities.Retry check, scoped to the workflow's own org/project.
func (s *UiService) RetryWorkflow(ctx context.Context, req csilapi.RetryWorkflowRequest) (csilapi.RetryWorkflowResponse, error) {
	if err := requireNonEmpty("workflow_instance_id", req.WorkflowInstanceId, 64); err != nil {
		return csilapi.RetryWorkflowResponse{}, err
	}

	wig, ok := s.deps.Store.(workflowInstanceGetter)
	if !ok {
		return csilapi.RetryWorkflowResponse{}, NewServiceError("internal", "workflows are not supported by this server's store configuration")
	}
	wf, err := wig.GetWorkflowInstance(ctx, req.WorkflowInstanceId)
	if err != nil {
		return csilapi.RetryWorkflowResponse{}, mapStoreErr(err, "workflow not found")
	}

	id, _ := s.deps.resolveIdentity(ctx)
	if err := s.requireWorkflowVisible(ctx, id, wf); err != nil {
		return csilapi.RetryWorkflowResponse{}, err
	}
	caps, err := s.deps.Resolver.Capabilities(ctx, id, workflowScope(wf))
	if err != nil {
		return csilapi.RetryWorkflowResponse{}, NewServiceError("internal", "an internal error occurred")
	}
	if !caps.Retry {
		return csilapi.RetryWorkflowResponse{}, NewServiceError("forbidden", "you do not have permission to retry this workflow")
	}

	newWf, err := jobcontrol.RetryWorkflow(ctx, s.deps.Store, s.deps.CorndogsClient, req.WorkflowInstanceId)
	if err != nil {
		return csilapi.RetryWorkflowResponse{}, mapJobControlErr(err)
	}
	return csilapi.RetryWorkflowResponse{WorkflowInstanceId: newWf.WorkflowID, Status: newWf.Status}, nil
}

// workflowNodesGetter is the narrow store capability RetryUnsuccessfulJobs
// needs, beyond workflowInstanceGetter, to compute skipped_count for its
// response: a best-effort count of member-job nodes that jobcontrol's own
// RetryUnsuccessfulJobs pass didn't end up retrying (not retryable, no job,
// or an individual retry failure — jobcontrol intentionally doesn't
// distinguish these cases, see retry.go's doc comment on partial success).
type workflowNodesGetter interface {
	ListWorkflowNodes(ctx context.Context, workflowID string) ([]models.WorkflowNode, error)
}

// RetryUnsuccessfulJobs requests a retry of every failed/cancelled member job
// of a workflow, in place (same instance, same nodes) — see
// jobcontrol.RetryUnsuccessfulJobs. Authorization mirrors CancelWorkflow's
// Capabilities.Retry check. A partial failure inside jobcontrol (some jobs
// retried, others errored) is not surfaced as a ServiceError: the response's
// job_ids/retried_count reflect what actually succeeded, and skipped_count
// covers everything else, so the caller always gets a usable result instead
// of an all-or-nothing failure. Only a total failure (workflow not found, or
// the store not supporting workflows) is returned as an error.
func (s *UiService) RetryUnsuccessfulJobs(ctx context.Context, req csilapi.RetryUnsuccessfulJobsRequest) (csilapi.RetryUnsuccessfulJobsResponse, error) {
	if err := requireNonEmpty("workflow_instance_id", req.WorkflowInstanceId, 64); err != nil {
		return csilapi.RetryUnsuccessfulJobsResponse{}, err
	}

	wig, ok := s.deps.Store.(workflowInstanceGetter)
	if !ok {
		return csilapi.RetryUnsuccessfulJobsResponse{}, NewServiceError("internal", "workflows are not supported by this server's store configuration")
	}
	wf, err := wig.GetWorkflowInstance(ctx, req.WorkflowInstanceId)
	if err != nil {
		return csilapi.RetryUnsuccessfulJobsResponse{}, mapStoreErr(err, "workflow not found")
	}

	id, _ := s.deps.resolveIdentity(ctx)
	if err := s.requireWorkflowVisible(ctx, id, wf); err != nil {
		return csilapi.RetryUnsuccessfulJobsResponse{}, err
	}
	caps, err := s.deps.Resolver.Capabilities(ctx, id, workflowScope(wf))
	if err != nil {
		return csilapi.RetryUnsuccessfulJobsResponse{}, NewServiceError("internal", "an internal error occurred")
	}
	if !caps.Retry {
		return csilapi.RetryUnsuccessfulJobsResponse{}, NewServiceError("forbidden", "you do not have permission to retry this workflow's jobs")
	}

	totalWithJob := 0
	if wng, ok := s.deps.Store.(workflowNodesGetter); ok {
		if nodes, err := wng.ListWorkflowNodes(ctx, req.WorkflowInstanceId); err == nil {
			for _, n := range nodes {
				if n.JobID != nil && *n.JobID != "" {
					totalWithJob++
				}
			}
		}
	}

	retried, err := jobcontrol.RetryUnsuccessfulJobs(ctx, s.deps.Store, s.deps.CorndogsClient, req.WorkflowInstanceId)
	if err != nil && errors.Is(err, jobcontrol.ErrWorkflowsUnsupported) {
		return csilapi.RetryUnsuccessfulJobsResponse{}, mapJobControlErr(err)
	}

	jobIDs := make([]string, len(retried))
	for i, j := range retried {
		jobIDs[i] = j.JobID
	}
	skipped := totalWithJob - len(retried)
	if skipped < 0 {
		skipped = 0
	}

	return csilapi.RetryUnsuccessfulJobsResponse{
		JobIds:       jobIDs,
		RetriedCount: int64(len(retried)),
		SkippedCount: int64(skipped),
	}, nil
}
