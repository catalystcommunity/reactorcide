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
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/egress"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/principals"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/priority"
	"strings"
	"time"

	"github.com/catalystcommunity/app-utils-go/logging"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/characteristics"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/corndogs"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/imagealias"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/runshifts"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/worker"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/workflowengine"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/workflowevents"
	"github.com/google/uuid"
	"github.com/lib/pq"
)

// ErrNotRetryable is returned when the target job is not in a retryable
// state — see models.Job.IsRetryable. Mirrors ErrNotCancellable's role for
// the cancel/kill flow.
var ErrNotRetryable = errors.New("job cannot be retried in its current state")

// ErrWorkerClassRefused is returned when the worker class of a retried job
// refuses it (worker.CheckWorkerClassAdmission).
var ErrWorkerClassRefused = errors.New("the worker class refuses the retried job")

// ErrImageGone is returned when a retry or a rerun cannot get an image: the
// alias of the earlier run (the alias that the job named, or the alias that
// pinned its direct reference) does not exist any more, is now for another
// kind or platform, or does not fit the job (imagealias.ResolveRetry). The
// job would wait forever, so it is not created.
var ErrImageGone = errors.New("the image of the earlier run is not available any more")

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

// queueResolvingStore is the narrow store capability RetryJob uses to
// re-resolve a retried job's characteristics to a queue UUID before
// resubmitting. Defined here on the consumer side (repo convention: narrow
// interface + type assertion on the store), mirroring
// internal/handlers/job_handler.go and internal/worker/trigger_processor.go's
// identical interface for their own submit paths. The concrete
// PostgresDbStore satisfies it via
// internal/store/postgres_store/queue_operations.go.
type queueResolvingStore interface {
	FindOrCreateQueueByCharacteristics(ctx context.Context, chars characteristics.Characteristics) (*models.Queue, error)
}

type orgQueueResolvingStore interface {
	FindOrCreateQueueForOrg(ctx context.Context, orgID, workerClass string, chars characteristics.Characteristics) (*models.Queue, error)
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
	return retryJob(ctx, st, corndogsClient, job, models.RunShifts{})
}

func retryJob(ctx context.Context, st store.Store, corndogsClient corndogs.ClientInterface, job *models.Job, shifts models.RunShifts) (*models.Job, error) {
	if job == nil || !job.IsRetryable() {
		return nil, JobNotRetryable(job)
	}

	newJob := cloneJobForRetry(job)
	// A retry resolves the image again through the alias of the earlier run
	// and uses its current pin: after an alias update the old digest leaves
	// the worker image sets (owner decision 2026-11-18).
	imageResult, err := imagealias.ResolveRetry(ctx, st, newJob, imagealias.CarriedFromJob(job))
	if err != nil {
		if refused, ok := imagealias.IsRefused(err); ok {
			return nil, fmt.Errorf("%w: %s", ErrImageGone, refused.Message)
		}
		return nil, err
	}
	if imageResult.Changed {
		logging.Log.WithFields(map[string]interface{}{
			"job_id": job.JobID, "image": imagealias.DisplayJob(newJob), "retry_of_image": imageResult.Previous,
		}).Info("Retry uses the current pin of the image alias")
	}
	// The worker class conditions can have changed since the first run, so
	// a retry checks them again. Fail closed.
	if err := worker.CheckWorkerClassAdmission(ctx, st, newJob.OrgID, newJob.WorkerClass, newJob.RefClasses, newJob.ExecutionProfile, nil); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrWorkerClassRefused, err)
	}
	// Shifts change only resources and the timeout (see rerun.go). Every
	// authority field above comes from the original job.
	if err := applyRetryShifts(ctx, st, newJob, shifts); err != nil {
		return nil, err
	}
	// The re-runner is the actor of the retry. With no actor in the context
	// the database inherits the actor of the original job (parent_job_id).
	principals.ApplyToJob(newJob, principals.ActorOf(ctx))
	// A retry keeps the lane and the priority of the original job; the
	// re-runner's groups do not apply. Only a retry lane can change the lane
	// (docs/priority.md).
	priority.AssignRetry(ctx, st, job, newJob)

	// Re-resolve the queue from the cloned characteristics rather than
	// trusting the cloned QueueName verbatim -- this is a no-op in the common
	// case (the original job's characteristics already resolved to a live
	// queue row) but stays correct if that queue row was ever deleted and
	// needs re-creating.
	egress.RouteJob(ctx, st, newJob)
	if qs, ok := st.(orgQueueResolvingStore); ok {
		queue, err := qs.FindOrCreateQueueForOrg(ctx, newJob.OrgID, newJob.WorkerClass, newJob.Characteristics)
		if err != nil {
			return nil, fmt.Errorf("failed to resolve queue for retried job: %w", err)
		}
		newJob.QueueName = queue.QueueUUID
	} else if qs, ok := st.(queueResolvingStore); ok {
		queue, err := qs.FindOrCreateQueueByCharacteristics(ctx, newJob.Characteristics)
		if err != nil {
			return nil, fmt.Errorf("failed to resolve queue for retried job: %w", err)
		}
		newJob.QueueName = queue.QueueUUID
	}

	if err := st.CreateJob(ctx, newJob); err != nil {
		return nil, fmt.Errorf("failed to create retried job: %w", err)
	}
	relinkRetriedEvalEvent(ctx, st, job, newJob)

	if corndogsClient != nil {
		payload := worker.BuildTaskPayload(newJob)
		task, err := corndogsClient.SubmitTaskToQueue(ctx, newJob.QueueName, payload, int64(newJob.Priority))
		if err != nil {
			logging.Log.WithError(err).WithField("job_id", newJob.JobID).
				Error("Failed to submit retried job to Corndogs")
			newJob.Status = "failed"
			newJob.LastError = fmt.Sprintf("failed to submit to Corndogs: %v", err)
			newJob.SetFailure(models.FailureClassPlatform, "queue submission")
			if err := st.UpdateJob(ctx, newJob); err != nil {
				return newJob, fmt.Errorf("failed to record queue submission failure of retried job: %w", err)
			}
		} else if err := worker.RecordQueueSubmission(ctx, st, newJob, task.Uuid, task.CurrentState); err != nil {
			return newJob, fmt.Errorf("failed to update retried job after Corndogs submission: %w", err)
		}
	}

	if job.WorkflowNodeID != nil && *job.WorkflowNodeID != "" {
		if err := rebindWorkflowNodeForRetry(ctx, st, job, newJob); err != nil {
			return newJob, fmt.Errorf("job retried but failed to rebind workflow node: %w", err)
		}
	}
	bumpJobReportRevision(ctx, st, newJob)

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
	return retryWorkflow(ctx, st, corndogsClient, workflowID, models.RunShifts{}, false)
}

// retryWorkflow is RetryWorkflow with shifts. rerun accepts any finished
// workflow (see RerunWorkflow); otherwise the workflow must be retryable.
func retryWorkflow(ctx context.Context, st store.Store, corndogsClient corndogs.ClientInterface, workflowID string, shifts models.RunShifts, rerun bool) (*models.WorkflowInstance, error) {
	ws, ok := st.(workflowRetryStore)
	if !ok {
		return nil, ErrWorkflowsUnsupported
	}

	old, err := ws.GetWorkflowInstance(ctx, workflowID)
	if err != nil {
		return nil, err
	}
	if rerun && !IsWorkflowRerunnable(old) {
		return old, WorkflowNotRerunnable(old)
	}
	if !rerun && !old.IsRetryable() {
		return old, WorkflowNotRetryable(old)
	}
	if err := CheckShifts(ctx, st, old.OwnershipOrgID(), old.ProjectID, shifts); err != nil {
		return old, err
	}
	oldNodes, err := ws.ListWorkflowNodes(ctx, workflowID)
	if err != nil {
		return old, err
	}

	newWf := &models.WorkflowInstance{
		UserID:      old.UserID,
		OrgID:       old.OrgID,
		ProjectID:   old.ProjectID,
		ParentJobID: old.ParentJobID,
		// A retry or a re-run serves the same CI event: its jobs keep the
		// event, and the event checks count it in place of the instance it
		// supersedes (rerun_of_workflow_id, the first instance of the chain).
		// SetCIEventID below links the event.
		RerunOfWorkflowID:  rerunChainRoot(old),
		ParentWorkflowID:   cloneStringPtr(old.ParentWorkflowID),
		RootWorkflowID:     cloneStringPtr(old.RootWorkflowID),
		RefClasses:         append([]string{}, old.RefClasses...),
		Name:               old.Name,
		WorkflowSecurityID: old.WorkflowSecurityID,
		SourceFile:         old.SourceFile,
		Status:             "evaluating",
		QueueName:          old.QueueName,
		VCSProvider:        old.VCSProvider,
		VCSRepo:            old.VCSRepo,
		PRNumber:           cloneIntPtr(old.PRNumber),
		CommitSHA:          old.CommitSHA,
		StatusContext:      old.StatusContext,
		CommentMarker:      old.CommentMarker,
		CIOrigin:           old.CIOrigin, CIRepository: old.CIRepository, CISHA: old.CISHA,
		ExecutionProfile: old.ExecutionProfile, WorkerClass: old.WorkerClass,
		PolicyRevision: old.PolicyRevision, PolicyRuleID: old.PolicyRuleID, ApprovalID: cloneStringPtr(old.ApprovalID),
	}
	// A root re-run keeps the event in rerun_event_id, not event_id: the
	// (event, security ID) index of root workflows stays the identity of
	// the first instance (migration 000051).
	newWf.SetCIEventID(old.CIEventID())
	if old.ActorPrincipalID != nil {
		newWf.ActorPrincipalID, newWf.ActorIdentity = cloneStringPtr(old.ActorPrincipalID), old.ActorIdentity
	}
	// A retry or a re-run keeps the owner snapshot of the original run.
	if old.OwnerGroup != nil {
		newWf.SetOwnership(old.Ownership(), models.OwnerSourceRetry)
	}
	if actor := principals.ActorOf(ctx); !actor.IsZero() {
		newWf.ActorPrincipalID, newWf.ActorIdentity = actor.Ptr(), actor.Identity
	}
	// The shifts of the original instance stay; new shifts replace them key
	// by key. Every job of the new instance gets them (worker
	// applyWorkflowShifts).
	if old.Shifts != nil || !shifts.IsZero() {
		merged := shifts
		if old.Shifts != nil {
			merged = old.Shifts.Merge(shifts)
		}
		newWf.Shifts = merged.Ptr()
	}
	if err := ws.CreateWorkflowInstance(ctx, newWf); err != nil {
		return nil, fmt.Errorf("failed to create retried workflow instance: %w", err)
	}
	workflowevents.WorkflowCreated(ctx, newWf)

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
			// A retry replays the recorded node authority. It must not
			// evaluate a newer policy implicitly.
			CIOrigin: on.CIOrigin, CIRepository: on.CIRepository, CISHA: on.CISHA,
			ExecutionProfile: on.ExecutionProfile, WorkerClass: on.WorkerClass,
			PolicyRevision: on.PolicyRevision, PolicyRuleID: on.PolicyRuleID, ApprovalID: cloneStringPtr(on.ApprovalID),
		}
		// A retry or a re-run keeps the resolved image of the earlier run.
		if err := carryNodeImage(ctx, st, on, node); err != nil {
			return newWf, fmt.Errorf("failed to copy the image of workflow node %q: %w", on.Name, err)
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
	bumpWorkflowReportRevision(ctx, st, newWf)
	return newWf, nil
}

// RetryUnsuccessfulJobs retries a workflow in place (same workflow instance,
// same nodes; compare RetryWorkflow). It does three things:
//
//  1. Each node that was skipped because a dependency was not successful
//     (dependency-skipped, see isDependencySkipped), and each finished node
//     downstream of such a node or of a failed, cancelled or timed-out node,
//     goes back to "pending" (resetNodeForRetry). A node skipped by its own
//     `if:` stays skipped. Its old job stays in the history.
//  2. Each failed, cancelled or timed-out job that is not downstream of
//     another reset or retried node is retried with RetryJob semantics (a
//     clone of the job, with shifts). A retryable node that is downstream
//     goes back to pending instead, so it cannot run before its upstream.
//  3. When it reset a node, it evaluates the workflow once, as for a new
//     workflow: a pending node whose dependencies are finished runs or is
//     skipped by its condition now; the others wait, and the normal
//     completion hooks evaluate them when the retried jobs finish.
//
// The returned jobs are the retried jobs and the jobs that the evaluation
// started. Individual retry failures do not abort the batch: the result has
// both the jobs that succeeded and an aggregated error. A workflow with
// nothing to retry or reset gives a *StateError (ErrWorkflowNotRetryable),
// not an empty success.
func RetryUnsuccessfulJobs(ctx context.Context, st store.Store, corndogsClient corndogs.ClientInterface, workflowID string) ([]*models.Job, error) {
	return retryUnsuccessfulJobs(ctx, st, corndogsClient, workflowID, models.RunShifts{})
}

func retryUnsuccessfulJobs(ctx context.Context, st store.Store, corndogsClient corndogs.ClientInterface, workflowID string, shifts models.RunShifts) ([]*models.Job, error) {
	ws, ok := st.(workflowControlStore)
	if !ok {
		return nil, ErrWorkflowsUnsupported
	}
	wf, err := ws.GetWorkflowInstance(ctx, workflowID)
	if err != nil {
		return nil, err
	}
	if !shifts.IsZero() {
		// Refuse shifts over the limits before any job is retried.
		if err := CheckShifts(ctx, st, wf.OwnershipOrgID(), wf.ProjectID, shifts); err != nil {
			return nil, err
		}
	}

	nodes, err := ws.ListWorkflowNodes(ctx, workflowID)
	if err != nil {
		return nil, err
	}

	var errs []error
	retryable := map[string]*models.Job{}
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
		if job.IsRetryable() {
			retryable[node.NodeID] = job
		}
	}
	plan := planRetryUnsuccessful(nodes, retryable)
	if len(plan.retry) == 0 && len(plan.reset) == 0 && len(errs) == 0 {
		return nil, nothingToRetry(wf)
	}

	// Reset first: a retried job must not finish while its dependents still
	// read as finished, or its completion hook would not run them.
	for _, node := range plan.reset {
		if err := resetNodeForRetry(ctx, ws, wf, node); err != nil {
			errs = append(errs, fmt.Errorf("node %q: %w", node.DisplayName, err))
		}
	}

	var retried []*models.Job
	for _, node := range plan.retry {
		job := retryable[node.NodeID]
		newJob, err := retryJob(ctx, st, corndogsClient, job, shifts)
		if err != nil {
			errs = append(errs, fmt.Errorf("node %q (job %s): %w", node.DisplayName, job.JobID, err))
			continue
		}
		retried = append(retried, newJob)
	}

	if len(plan.reset) > 0 {
		reloaded, err := ws.GetWorkflowInstance(ctx, workflowID)
		if err == nil {
			wf = reloaded
		}
		started, err := worker.NewTriggerProcessor(st, corndogsClient).EvaluateWorkflow(ctx, wf)
		if err != nil {
			errs = append(errs, fmt.Errorf("evaluate workflow after reset: %w", err))
		}
		for _, jobID := range started {
			if job, err := st.GetJobByID(ctx, jobID); err == nil {
				retried = append(retried, job)
			}
		}
		bumpWorkflowReportRevision(ctx, st, wf)
	}

	if len(errs) > 0 {
		return retried, fmt.Errorf("retry-unsuccessful-jobs: %d of %d retries failed: %w",
			len(errs), len(errs)+len(retried), errors.Join(errs...))
	}
	return retried, nil
}

// RetryUnsuccessfulAvailable reports whether RetryUnsuccessfulJobs has work
// for a workflow with these nodes: a node with a failed, cancelled or
// timed-out job, or a dependency-skipped node. It reads the node statuses
// only (a node's status follows its job), so a UI can show the action
// without loading every job.
func RetryUnsuccessfulAvailable(nodes []models.WorkflowNode) bool {
	retryable := map[string]*models.Job{}
	for i := range nodes {
		if nodes[i].JobID != nil && *nodes[i].JobID != "" && workflowengine.Failure(nodes[i].Status) {
			retryable[nodes[i].NodeID] = &models.Job{JobID: *nodes[i].JobID}
		}
	}
	plan := planRetryUnsuccessful(nodes, retryable)
	return len(plan.retry) > 0 || len(plan.reset) > 0
}

// retryUnsuccessfulPlan is what RetryUnsuccessfulJobs does to each node.
type retryUnsuccessfulPlan struct {
	// retry: nodes whose job is retried in place (RetryJob).
	retry []*models.WorkflowNode
	// reset: nodes that go back to pending and are evaluated again.
	reset []*models.WorkflowNode
}

// planRetryUnsuccessful selects the nodes to retry and to reset. retryable
// maps a node ID to its failed, cancelled or timed-out job. The seeds are the
// retryable nodes and the dependency-skipped nodes. Every finished node
// downstream of a seed is reset, except a node skipped by its own `if:`. A
// retryable node that is downstream of a seed is reset, not retried.
func planRetryUnsuccessful(nodes []models.WorkflowNode, retryable map[string]*models.Job) retryUnsuccessfulPlan {
	byName := map[string][]int{}
	for i := range nodes {
		byName[nodes[i].Name] = append(byName[nodes[i].Name], i)
	}
	dependents := map[string][]int{}
	for i := range nodes {
		for _, dep := range nodes[i].DependsOn {
			dependents[dep] = append(dependents[dep], i)
		}
	}

	seed := make([]bool, len(nodes))
	for i := range nodes {
		if retryable[nodes[i].NodeID] != nil || isDependencySkipped(nodes, &nodes[i]) {
			seed[i] = true
		}
	}

	// downstream[i]: node i depends, directly or not, on a seed.
	downstream := make([]bool, len(nodes))
	var queue []string
	queued := map[string]bool{}
	for i := range nodes {
		if seed[i] && !queued[nodes[i].Name] {
			queued[nodes[i].Name] = true
			queue = append(queue, nodes[i].Name)
		}
	}
	for len(queue) > 0 {
		name := queue[0]
		queue = queue[1:]
		for _, i := range dependents[name] {
			if downstream[i] {
				continue
			}
			downstream[i] = true
			if !queued[nodes[i].Name] {
				queued[nodes[i].Name] = true
				queue = append(queue, nodes[i].Name)
			}
		}
	}

	var plan retryUnsuccessfulPlan
	for i := range nodes {
		node := &nodes[i]
		switch {
		case retryable[node.NodeID] != nil && !downstream[i]:
			plan.retry = append(plan.retry, node)
		case isDependencySkipped(nodes, node):
			plan.reset = append(plan.reset, node)
		case downstream[i] && workflowengine.Terminal(node.Status) && !(node.Status == "skipped" && !hasDependencyDecision(node)):
			plan.reset = append(plan.reset, node)
		}
	}
	return plan
}

// isDependencySkipped reports whether the engine skipped node because of a
// dependency that can change: a dependency is failed, cancelled, timed out,
// or itself dependency-skipped, or the skip is stale (the condition holds for
// the dependencies as they are now, for example after an earlier retry made
// them pass). A node skipped by its own `if:` has no dependency decision, and
// as a dependency it is final. A node whose condition is false for
// successful dependencies (for example any_failed) is not dependency-skipped:
// evaluated again, it is skipped again.
func isDependencySkipped(nodes []models.WorkflowNode, node *models.WorkflowNode) bool {
	return dependencySkipped(nodes, node, map[string]bool{})
}

func dependencySkipped(nodes []models.WorkflowNode, node *models.WorkflowNode, visiting map[string]bool) bool {
	if node.Status != "skipped" || !hasDependencyDecision(node) || visiting[node.NodeID] {
		return false
	}
	visiting[node.NodeID] = true
	defer delete(visiting, node.NodeID)
	for _, dep := range node.DependsOn {
		for i := range nodes {
			if nodes[i].Name != dep {
				continue
			}
			if workflowengine.Failure(nodes[i].Status) || dependencySkipped(nodes, &nodes[i], visiting) {
				return true
			}
		}
	}
	engineNodes := make([]workflowengine.Node, len(nodes))
	for i := range nodes {
		engineNodes[i] = workflowengine.Node{ID: nodes[i].NodeID, Name: nodes[i].Name, DisplayName: nodes[i].DisplayName,
			Status: nodes[i].Status, DependsOn: []string(nodes[i].DependsOn), Condition: nodes[i].Condition}
	}
	self := workflowengine.Node{ID: node.NodeID, Name: node.Name, Status: node.Status,
		DependsOn: []string(node.DependsOn), Condition: node.Condition}
	if ready, _ := workflowengine.DependenciesReady(engineNodes, self); !ready {
		return false
	}
	stale, _ := workflowengine.EvaluateCondition(engineNodes, self)
	return stale
}

// hasDependencyDecision reports whether the workflow engine decided node's
// skip from its dependencies (workflowengine.ActionSkip), as opposed to the
// job's `if:` at registration ("skipped by if: ...").
func hasDependencyDecision(node *models.WorkflowNode) bool {
	for _, reason := range node.DecisionReasons {
		if reason.Kind == "dependency" && reason.Matched != nil && !*reason.Matched {
			return true
		}
	}
	// Nodes written before decision reasons were recorded have only the
	// summary text of the engine skip.
	return len(node.DecisionReasons) == 0 && strings.HasPrefix(node.DecisionReason, "skipped: ")
}

// resetNodeForRetry puts node back to pending, with no job, for the engine
// to evaluate again. The old job keeps its workflow_node_id, so the history
// of the node still has it.
func resetNodeForRetry(ctx context.Context, ws workflowControlStore, wf *models.WorkflowInstance, node *models.WorkflowNode) error {
	previous := node.Status
	node.Status = "pending"
	node.JobID = nil
	node.CompletedAt = nil
	node.DecisionReasons = nil
	node.DecisionReason = "reset by retry-unsuccessful (was " + previous + ")"
	if err := ws.UpdateWorkflowNode(ctx, node); err != nil {
		return fmt.Errorf("failed to reset workflow node for retry: %w", err)
	}
	if wf.Status != "running" {
		wf.Status = "running"
		wf.CompletedAt = nil
		if err := ws.UpdateWorkflowInstance(ctx, wf); err != nil {
			return fmt.Errorf("failed to set the workflow to running after reset: %w", err)
		}
		workflowevents.WorkflowUpdated(ctx, wf)
	}
	workflowevents.NodeUpdated(ctx, wf, node)
	recordRetryEvent(ctx, ws, wf.WorkflowID, &node.NodeID, nil, "node reset for evaluation (was "+previous+")")
	return nil
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
	workflowevents.NodeUpdated(ctx, wf, node)
	workflowevents.WorkflowUpdated(ctx, wf)
	reason := "job retried"
	if newJob.Shifts != nil {
		reason += " with shifts " + runshifts.Describe(*newJob.Shifts)
	}
	recordRetryEvent(ctx, ws, wf.WorkflowID, &node.NodeID, &newJob.JobID, reason)
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
		OrgID:     original.OrgID,
		ProjectID: original.ProjectID,

		Name:        original.Name,
		Description: original.Description,
		// A retry serves the same CI event. A retried eval therefore uses the
		// same trigger operation ID and reuses the event's workflows.
		EventID: cloneStringPtr(original.EventID),
		// A retry keeps the ref classes of the original (docs/ref-classes.md).
		RefClasses: append([]string{}, original.RefClasses...),
		JobFile:    original.JobFile,
		Notes:      original.Notes,

		SourceURL:  cloneStringPtr(original.SourceURL),
		SourceRef:  cloneStringPtr(original.SourceRef),
		SourceType: cloneSourceTypePtr(original.SourceType),
		SourcePath: cloneStringPtr(original.SourcePath),

		CISourceType: cloneSourceTypePtr(original.CISourceType),
		CISourceURL:  cloneStringPtr(original.CISourceURL),
		CISourceRef:  cloneStringPtr(original.CISourceRef),

		// The resolved image of the earlier run: the reference, the alias
		// names, and the image_digest characteristic (copied with
		// Characteristics below). retryJob resolves it again through the
		// alias (imagealias.ResolveRetry).
		ContainerImage: cloneStringPtr(original.ContainerImage),
		ImageAlias:     cloneStringPtr(original.ImageAlias),
		ImagePinAlias:  cloneStringPtr(original.ImagePinAlias),

		CodeDir:     original.CodeDir,
		JobDir:      original.JobDir,
		JobCommand:  original.JobCommand,
		RunnerImage: original.RunnerImage,
		JobEnvVars:  cloneJSONB(original.JobEnvVars),
		JobEnvFile:  original.JobEnvFile,

		TimeoutSeconds:     original.TimeoutSeconds,
		Priority:           original.Priority,
		Capabilities:       append(pq.StringArray(nil), original.Capabilities...),
		ImagePullSecrets:   append(pq.StringArray(nil), original.ImagePullSecrets...),
		RunAsUser:          original.RunAsUser,
		RunAsAdministrator: cloneBoolPtr(original.RunAsAdministrator),

		QueueName:       original.QueueName,
		WorkerClass:     original.WorkerClass,
		AutoTargetState: original.AutoTargetState,
		Characteristics: cloneCharacteristics(original.Characteristics),

		ResourceCPURequest:  original.ResourceCPURequest,
		ResourceCPULimit:    original.ResourceCPULimit,
		ResourceMemoryLimit: original.ResourceMemoryLimit,
		// Storage and earlier shifts are resources too: a retry keeps them.
		ResourceStorageRequest: original.ResourceStorageRequest,
		ResourceStorageLimit:   original.ResourceStorageLimit,
		Shifts:                 cloneShifts(original.Shifts),

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
		CIOrigin:  original.CIOrigin, CIRepository: original.CIRepository, CISHA: original.CISHA,
		ExecutionProfile: original.ExecutionProfile, PolicyRevision: original.PolicyRevision,
		PolicyRuleID: original.PolicyRuleID, ApprovalID: cloneStringPtr(original.ApprovalID),
	}

	if original.WorkflowNodeID != nil && *original.WorkflowNodeID != "" {
		runID := uuid.New().String()
		newJob.WorkflowRunID = &runID
	}

	return newJob
}

// carryNodeImage copies the resolved image of the earlier run of a workflow
// node to its new node: from the job of the old node, or from the carried
// values of the old node when it has no job. The node job then resolves
// its image again through the alias of the earlier run and gets the
// current pin of that alias (imagealias.ResolveRetry).
func carryNodeImage(ctx context.Context, st store.Store, old, node *models.WorkflowNode) error {
	if old.JobID != nil && *old.JobID != "" {
		job, err := st.GetJobByID(ctx, *old.JobID)
		switch {
		case err == nil:
			carried := imagealias.CarriedFromJob(job)
			if carried.Ref == "" {
				return nil
			}
			node.CarriedImageRef = &carried.Ref
			node.CarriedImageAlias = cloneStringPtr(carried.Alias)
			node.CarriedImagePinAlias = cloneStringPtr(carried.PinAlias)
			if carried.Digest != "" {
				node.CarriedImageDigest = &carried.Digest
			}
			return nil
		case errors.Is(err, store.ErrNotFound):
			// The job is gone: use the carried values, if any.
		default:
			return err
		}
	}
	node.CarriedImageRef = cloneStringPtr(old.CarriedImageRef)
	node.CarriedImageAlias = cloneStringPtr(old.CarriedImageAlias)
	node.CarriedImageDigest = cloneStringPtr(old.CarriedImageDigest)
	node.CarriedImagePinAlias = cloneStringPtr(old.CarriedImagePinAlias)
	return nil
}

// rerunChainRoot returns the first instance of the retry or re-run chain of
// old: a re-run of a re-run points at the same first instance.
func rerunChainRoot(old *models.WorkflowInstance) *string {
	if old.RerunOfWorkflowID != nil && *old.RerunOfWorkflowID != "" {
		return cloneStringPtr(old.RerunOfWorkflowID)
	}
	id := old.WorkflowID
	return &id
}

func cloneShifts(in *models.RunShifts) *models.RunShifts {
	if in == nil {
		return nil
	}
	cp := *in
	return &cp
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

// cloneCharacteristics shallow-copies a Characteristics map. Values
// (characteristics.Value) are immutable by construction (see
// internal/characteristics), so a shallow copy is sufficient to give the
// retried job its own map instance.
func cloneCharacteristics(in characteristics.Characteristics) characteristics.Characteristics {
	if in == nil {
		return nil
	}
	out := make(characteristics.Characteristics, len(in))
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

func cloneBoolPtr(in *bool) *bool {
	if in == nil {
		return nil
	}
	out := *in
	return &out
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
