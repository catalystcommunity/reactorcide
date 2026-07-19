// Retry mechanics for jobs and workflows. See jobcontrol.go's package doc
// for why this lives alongside cancel/kill: REST (internal/handlers) and —
// in a later wave — the CSIL-RPC UI service both need to agree on exactly
// what a retry request does to the DB rows and to Corndogs, so this package
// is the one place that decides it.
//
// Three retry shapes, matching the feature spec:
//
//   - RetryJob: retries a single job in place — same workflow, same
//     workflow node (if any) — by cloning its spec into a brand-new job row
//     and resubmitting. Only valid for a job that IsRetryable() (failed,
//     cancelled, or timeout).
//   - RetryWorkflow: retries an entire workflow as a brand-new instance
//     (fresh workflow_id, fresh nodes, fresh jobs) — the old instance is
//     left untouched for history/audit. Only valid for a workflow instance
//     that IsRetryable() (failed or cancelled — a WorkflowInstance's Status
//     is never "timeout", see models.WorkflowInstance.IsRetryable's doc
//     comment).
//   - RetryUnsuccessfulJobs: bulk-applies RetryJob to every failed/
//     cancelled/timeout node's job in a workflow, in place (same workflow,
//     same instance) — the workflow-scoped analog of "retry all the red
//     jobs" without starting a whole new run.
//
// None of these three take a VCS/comment status-updater dependency, same as
// CancelJob/CancelWorkflow above: they only ever mutate job/workflow/node
// rows and resubmit to Corndogs, and trust the worker's existing
// ProcessWorkflowJobStarted/ProcessWorkflowCompletion hooks (and, for loose
// non-workflow jobs, internal/worker/corndogs_worker.go's completion-time
// VCS push) to bring a retried job's PR comment/commit-status row up to
// date once it actually starts/finishes — see docs/ui-auth.md's "Retry and
// PR comment updates" section for the full trace and why threading a
// statusUpdater through REST+CSIL wasn't worth it for what is, at worst, a
// brief staleness window rather than an incorrect final result.
package jobcontrol

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/catalystcommunity/app-utils-go/logging"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/corndogs"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/worker"
	"github.com/google/uuid"
	"github.com/lib/pq"
)

// ErrNotRetryable is returned when the target job is not in a retryable
// state — see models.Job.IsRetryable. Mirrors ErrNotCancellable's role for
// the cancel/kill flow.
var ErrNotRetryable = errors.New("job cannot be retried in its current state")

// ErrWorkflowNotRetryable is ErrNotRetryable's workflow-instance analog —
// see models.WorkflowInstance.IsRetryable.
var ErrWorkflowNotRetryable = errors.New("workflow cannot be retried in its current state")

// workflowRetryStore is workflowControlStore plus the create operations
// RetryWorkflow needs to persist a brand-new workflow instance and its
// nodes. Kept separate from workflowControlStore (rather than folding these
// two methods into it) because CancelJob/CancelWorkflow only ever mutate
// existing rows — only the retry path creates new ones.
type workflowRetryStore interface {
	workflowControlStore
	CreateWorkflowInstance(ctx context.Context, wf *models.WorkflowInstance) error
	CreateWorkflowNode(ctx context.Context, node *models.WorkflowNode) error
}

// RetryJob retries a single job in place: validates job.IsRetryable()
// (failed, cancelled, or timeout), clones its full spec into a brand-new job row
// (fresh JobID, status "submitted", every execution field zeroed —
// started/completed/exit_code/last_error/cancel_mode/logs/artifacts keys/
// corndogs_task_id/worker_id), sets ParentJobID to the original job's ID and
// RetryCount to original.RetryCount+1, and resubmits to Corndogs mirroring
// internal/worker/trigger_processor.go's createAndSubmitJob /
// internal/worker/workflow_runtime.go's submitWorkflowNode submission shape
// (via worker.BuildTaskPayload, so all three call sites can't drift on what
// Corndogs is told).
//
// If the original job belongs to a workflow node (WorkflowNodeID set), the
// node is rebound to the new job (JobID updated, status back to
// "submitted", CompletedAt cleared) and the workflow instance's status is
// forced back to "running" so the UI stops showing it as failed/cancelled/
// timeout while the retried node is in flight, and so the normal
// ProcessWorkflowJobStarted/ProcessWorkflowCompletion hooks re-evaluate
// dependents once the retried job finishes (see rebindWorkflowNodeForRetry
// for why this is a direct assignment rather than a
// worker.ComputeWorkflowStatus recompute).
func RetryJob(ctx context.Context, st store.Store, corndogsClient corndogs.ClientInterface, job *models.Job) (*models.Job, error) {
	if job == nil || !job.IsRetryable() {
		return nil, ErrNotRetryable
	}

	newJob := cloneJobForRetry(job)
	if err := st.CreateJob(ctx, newJob); err != nil {
		return nil, fmt.Errorf("failed to create retried job: %w", err)
	}

	if corndogsClient != nil {
		payload := worker.BuildTaskPayload(newJob)
		task, err := corndogsClient.SubmitTask(ctx, payload, int64(newJob.Priority))
		if err != nil {
			logging.Log.WithError(err).WithField("job_id", newJob.JobID).
				Error("Failed to submit retried job to Corndogs")
			newJob.Status = "failed"
			newJob.LastError = fmt.Sprintf("failed to submit to Corndogs: %v", err)
		} else {
			taskID := task.Uuid
			newJob.CorndogsTaskID = &taskID
			newJob.Status = task.CurrentState
		}
		if err := st.UpdateJob(ctx, newJob); err != nil {
			return newJob, fmt.Errorf("failed to update retried job after Corndogs submission: %w", err)
		}
	}

	if job.WorkflowNodeID != nil && *job.WorkflowNodeID != "" {
		if err := rebindWorkflowNodeForRetry(ctx, st, job, newJob); err != nil {
			return newJob, fmt.Errorf("job retried but failed to rebind workflow node: %w", err)
		}
	}

	return newJob, nil
}

// RetryWorkflow retries an entire workflow as a brand-new instance:
// validates wf.IsRetryable() (failed or cancelled only), creates a fresh
// WorkflowInstance copying the old one's definition fields (name, project/
// user, VCS provider/repo/PR/commit, status_context, comment_marker,
// parent_job_id), creates fresh nodes copying each old node's definition
// (name, depends_on, condition, job_spec, matrix item fields) with statuses
// reset to "pending" and no JobID, then drives initial submission via
// worker.TriggerProcessor.EvaluateWorkflow — the same node-readiness/
// condition evaluation and submission path a brand-new workflow created
// from triggers.json goes through (see workflow_runtime.go's
// ProcessTriggersFromData). The old instance and its nodes/jobs are left
// completely untouched: this is a new run from scratch, not an in-place
// mutation (compare RetryUnsuccessfulJobs, which is in-place).
//
// Provenance: the new instance's ParentJobID is copied from the old
// instance rather than pointing at anything retry-specific — there is no
// schema change here (no migration), so "this instance is a retry of
// workflow X" is not separately recorded; the caller can infer it from
// matching Name/VCSRepo/CommitSHA/CommentMarker against the old instance if
// needed. See the retry feature's REST/CSIL layer for anything wanting a
// dedicated audit trail.
func RetryWorkflow(ctx context.Context, st store.Store, corndogsClient corndogs.ClientInterface, workflowID string) (*models.WorkflowInstance, error) {
	ws, ok := st.(workflowRetryStore)
	if !ok {
		return nil, ErrWorkflowsUnsupported
	}

	old, err := ws.GetWorkflowInstance(ctx, workflowID)
	if err != nil {
		return nil, err
	}
	if !old.IsRetryable() {
		return old, ErrWorkflowNotRetryable
	}
	oldNodes, err := ws.ListWorkflowNodes(ctx, workflowID)
	if err != nil {
		return old, err
	}

	newWf := &models.WorkflowInstance{
		UserID:        old.UserID,
		ProjectID:     old.ProjectID,
		ParentJobID:   old.ParentJobID,
		Name:          old.Name,
		Status:        "evaluating",
		QueueName:     old.QueueName,
		VCSProvider:   old.VCSProvider,
		VCSRepo:       old.VCSRepo,
		PRNumber:      cloneIntPtr(old.PRNumber),
		CommitSHA:     old.CommitSHA,
		StatusContext: old.StatusContext,
		CommentMarker: old.CommentMarker,
	}
	if err := ws.CreateWorkflowInstance(ctx, newWf); err != nil {
		return nil, fmt.Errorf("failed to create retried workflow instance: %w", err)
	}

	for i := range oldNodes {
		on := &oldNodes[i]
		node := &models.WorkflowNode{
			WorkflowID:  newWf.WorkflowID,
			Name:        on.Name,
			DisplayName: on.DisplayName,
			Status:      "pending",
			DependsOn:   append(pq.StringArray(nil), on.DependsOn...),
			Condition:   on.Condition,
			JobSpec:     cloneJSONB(on.JobSpec),
			ItemIndex:   cloneIntPtr(on.ItemIndex),
			ItemValue:   cloneJSONB(on.ItemValue),
			ItemVar:     on.ItemVar,
		}
		if err := ws.CreateWorkflowNode(ctx, node); err != nil {
			return newWf, fmt.Errorf("failed to create retried workflow node %q: %w", on.Name, err)
		}
	}

	tp := worker.NewTriggerProcessor(st, corndogsClient)
	if _, err := tp.EvaluateWorkflow(ctx, newWf); err != nil {
		return newWf, fmt.Errorf("failed to evaluate retried workflow: %w", err)
	}

	if reloaded, err := ws.GetWorkflowInstance(ctx, newWf.WorkflowID); err == nil {
		return reloaded, nil
	}
	return newWf, nil
}

// RetryUnsuccessfulJobs job-retries every member job of workflowID that is
// failed, cancelled, or timeout, in place (same workflow instance, same nodes — no
// new workflow is created; compare RetryWorkflow). Nodes with no job, or
// whose job isn't retryable (still running, or already terminal-success),
// are skipped without error. Individual RetryJob failures don't abort the
// batch: this continues past them and returns both the jobs that succeeded
// and an aggregated error describing what failed, so a caller can show
// partial success rather than an all-or-nothing failure.
func RetryUnsuccessfulJobs(ctx context.Context, st store.Store, corndogsClient corndogs.ClientInterface, workflowID string) ([]*models.Job, error) {
	ws, ok := st.(workflowControlStore)
	if !ok {
		return nil, ErrWorkflowsUnsupported
	}

	nodes, err := ws.ListWorkflowNodes(ctx, workflowID)
	if err != nil {
		return nil, err
	}

	var retried []*models.Job
	var errs []error
	for i := range nodes {
		node := &nodes[i]
		if node.JobID == nil || *node.JobID == "" {
			continue
		}
		job, err := st.GetJobByID(ctx, *node.JobID)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				continue
			}
			errs = append(errs, fmt.Errorf("node %q: failed to load job %s: %w", node.DisplayName, *node.JobID, err))
			continue
		}
		if !job.IsRetryable() {
			continue
		}
		newJob, err := RetryJob(ctx, st, corndogsClient, job)
		if err != nil {
			errs = append(errs, fmt.Errorf("node %q (job %s): %w", node.DisplayName, job.JobID, err))
			continue
		}
		retried = append(retried, newJob)
	}

	if len(errs) > 0 {
		return retried, fmt.Errorf("retry-unsuccessful-jobs: %d of %d retries failed: %w",
			len(errs), len(errs)+len(retried), errors.Join(errs...))
	}
	return retried, nil
}

// rebindWorkflowNodeForRetry points the workflow node that owned oldJob at
// newJob instead, resets its status/completion so it reads as freshly
// resubmitted, and forces the parent workflow instance's status directly to
// "running".
//
// That last part is a direct assignment rather than a
// worker.ComputeWorkflowStatus(nodes) recompute (the way
// jobcontrol.CancelWorkflow derives its own post-cascade status) because
// computeWorkflowStatus fail-fasts on ANY node still sitting in "failed"/
// "timeout": if this retry is for just one node out of several failed
// siblings (RetryJob called directly, as opposed to RetryUnsuccessfulJobs
// retrying all of them), recomputing would immediately re-derive "failed"
// even though the retried node is actively back in flight — exactly the
// stale-status the retry was supposed to fix. Forcing "running" here is
// safe because it's always true the moment a node is rebound to a fresh
// "submitted" job: the normal ProcessWorkflowJobStarted/
// ProcessWorkflowCompletion hooks (internal/worker/workflow_runtime.go) take
// over from there and use the normal computeWorkflowStatus rule once the
// retried job actually starts/finishes, same as any other node.
func rebindWorkflowNodeForRetry(ctx context.Context, st store.Store, oldJob, newJob *models.Job) error {
	ws, ok := st.(workflowControlStore)
	if !ok {
		return ErrWorkflowsUnsupported
	}

	node, err := ws.GetWorkflowNodeByJobID(ctx, oldJob.JobID)
	if err != nil {
		return fmt.Errorf("failed to load workflow node for retried job: %w", err)
	}
	node.JobID = &newJob.JobID
	node.Status = "submitted"
	node.CompletedAt = nil
	node.DecisionReason = "retried"
	if err := ws.UpdateWorkflowNode(ctx, node); err != nil {
		return fmt.Errorf("failed to rebind workflow node to retried job: %w", err)
	}

	if oldJob.WorkflowID == nil || *oldJob.WorkflowID == "" {
		return nil
	}
	wf, err := ws.GetWorkflowInstance(ctx, *oldJob.WorkflowID)
	if err != nil {
		return fmt.Errorf("failed to load workflow instance for retried job: %w", err)
	}
	wf.Status = "running"
	wf.CompletedAt = nil
	if err := ws.UpdateWorkflowInstance(ctx, wf); err != nil {
		return fmt.Errorf("failed to refresh workflow instance status after retry: %w", err)
	}
	recordRetryEvent(ctx, ws, wf.WorkflowID, &node.NodeID, &newJob.JobID, "job retried")
	return nil
}

func recordRetryEvent(ctx context.Context, ws workflowControlStore, workflowID string, nodeID, jobID *string, reason string) {
	if err := ws.CreateWorkflowEvent(ctx, &models.WorkflowEvent{
		WorkflowID: workflowID,
		NodeID:     nodeID,
		JobID:      jobID,
		EventType:  "node_retried",
		Reason:     reason,
		Details:    models.JSONB{},
	}); err != nil {
		logging.Log.WithError(err).WithField("workflow_id", workflowID).Warn("Failed to record workflow retry event")
	}
}

// cloneJobForRetry builds the new job row RetryJob creates: every spec
// field that describes *how to run the job* is copied from original, every
// field that describes *the outcome of a specific execution* is left at its
// zero value (started/completed/exit_code/last_error/cancel_mode/
// logs_object_key/artifacts_object_key/corndogs_task_id/worker_id — simply
// not set below). ParentJobID is set to original's JobID and RetryCount to
// original.RetryCount+1 per the retry feature spec. WorkflowRunID is
// deliberately NOT copied: a retry is a new execution attempt of the same
// workflow node, so it gets its own fresh run id (mirrors
// workflow_runtime.go's submitWorkflowNode, which also mints a new
// WorkflowRunID per submission even though WorkflowNodeID/WorkflowID stay
// fixed to the node's identity).
func cloneJobForRetry(original *models.Job) *models.Job {
	now := time.Now().UTC()
	parentJobID := original.JobID

	newJob := &models.Job{
		CreatedAt: now,
		UpdatedAt: now,
		UserID:    original.UserID,
		ProjectID: original.ProjectID,

		Name:        original.Name,
		Description: original.Description,
		JobFile:     original.JobFile,
		Notes:       original.Notes,

		SourceURL:  cloneStringPtr(original.SourceURL),
		SourceRef:  cloneStringPtr(original.SourceRef),
		SourceType: cloneSourceTypePtr(original.SourceType),
		SourcePath: cloneStringPtr(original.SourcePath),

		CISourceType: cloneSourceTypePtr(original.CISourceType),
		CISourceURL:  cloneStringPtr(original.CISourceURL),
		CISourceRef:  cloneStringPtr(original.CISourceRef),

		ContainerImage: cloneStringPtr(original.ContainerImage),

		CodeDir:     original.CodeDir,
		JobDir:      original.JobDir,
		JobCommand:  original.JobCommand,
		RunnerImage: original.RunnerImage,
		JobEnvVars:  cloneJSONB(original.JobEnvVars),
		JobEnvFile:  original.JobEnvFile,

		TimeoutSeconds: original.TimeoutSeconds,
		Priority:       original.Priority,
		Capabilities:   append(pq.StringArray(nil), original.Capabilities...),
		RunAsUser:      original.RunAsUser,

		QueueName:       original.QueueName,
		AutoTargetState: original.AutoTargetState,

		Status: "submitted",

		EventMetadata: cloneJSONB(original.EventMetadata),
		ParentJobID:   &parentJobID,
		RetryCount:    original.RetryCount + 1,

		WorkflowID:       original.WorkflowID,
		WorkflowNodeID:   original.WorkflowNodeID,
		WorkflowNodeName: original.WorkflowNodeName,

		VCSRepo:   cloneStringPtr(original.VCSRepo),
		PRNumber:  cloneIntPtr(original.PRNumber),
		CommitSHA: cloneStringPtr(original.CommitSHA),
	}

	if original.WorkflowNodeID != nil && *original.WorkflowNodeID != "" {
		runID := uuid.New().String()
		newJob.WorkflowRunID = &runID
	}

	return newJob
}

func cloneJSONB(in models.JSONB) models.JSONB {
	if in == nil {
		return nil
	}
	out := make(models.JSONB, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func cloneStringPtr(in *string) *string {
	if in == nil {
		return nil
	}
	cp := *in
	return &cp
}

func cloneIntPtr(in *int) *int {
	if in == nil {
		return nil
	}
	cp := *in
	return &cp
}

func cloneSourceTypePtr(in *models.SourceType) *models.SourceType {
	if in == nil {
		return nil
	}
	cp := *in
	return &cp
}
