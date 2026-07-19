// Package jobcontrol holds the execution-layer cancel/kill primitives for
// jobs and workflows, shared between the REST handlers (internal/handlers)
// and — in a later wave — the CSIL-RPC UI service. See UI_AUTH_PLAN.md's
// "Cancel vs Kill" architecture section for the end-to-end design; this
// package is the one place that decides *what a cancel/kill request does to
// the DB rows*, so REST and CSIL callers can't drift from each other.
//
// The actual container-level termination (SIGTERM/SIGKILL, or force-remove)
// happens in the worker, not here: this package only ever flips job/workflow
// status in the store, and does so via a guarded (race-safe) transition when
// the store supports it — see guardedJobStore. For a running job that means
// setting status "cancelling" (+ cancel_mode recording cancel vs kill) and
// trusting internal/worker/job_processor.go's cancel-poll (see
// JobProcessor.pollForCancel) to observe it and act. For a job that hasn't
// started yet (submitted/queued), this package races the worker to dequeue
// the Corndogs task before any worker claims it: if that race is won, the
// job lands directly on "cancelled"; if lost (a worker claimed the task
// first), the job is left "cancelling" for the claiming worker to finalize
// (see internal/worker/corndogs_worker.go's claim-path check).
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
)

// ErrNotCancellable is returned when the target job is already in a
// terminal state and cannot be cancelled/killed, or the requested
// transition doesn't apply (e.g. a graceful cancel against a job that's
// already cancelling — see models.Job.CanBeCancelled vs CanBeKilled).
var ErrNotCancellable = errors.New("job cannot be cancelled in its current state")

// ErrWorkflowsUnsupported is returned when the configured store does not
// implement the narrow workflow-control interface this package needs (e.g.
// a minimal test store). Matches the "consumer-defined narrow interface"
// pattern used elsewhere (see internal/worker/workflow_runtime.go's
// workflowStore, internal/handlers/workflow_handler.go's workflowSummaryStore).
var ErrWorkflowsUnsupported = errors.New("store does not support workflows")

// workflowControlStore is the narrow slice of store.Store's workflow
// operations CancelWorkflow needs. store.Store itself only declares job/
// project/token/secret operations (see internal/store/store_interface.go);
// workflow persistence is a postgres_store-only capability reached via type
// assertion, same as internal/worker/workflow_runtime.go's workflowStore.
type workflowControlStore interface {
	GetWorkflowInstance(ctx context.Context, workflowID string) (*models.WorkflowInstance, error)
	UpdateWorkflowInstance(ctx context.Context, wf *models.WorkflowInstance) error
	ListWorkflowNodes(ctx context.Context, workflowID string) ([]models.WorkflowNode, error)
	UpdateWorkflowNode(ctx context.Context, node *models.WorkflowNode) error
	CreateWorkflowEvent(ctx context.Context, event *models.WorkflowEvent) error
	// GetWorkflowNodeByJobID is not needed by CancelWorkflow (it already has
	// every node from ListWorkflowNodes), but RetryJob needs it to find the
	// single node bound to the job being retried without listing every node
	// in the workflow — see retry.go's rebindWorkflowNodeForRetry.
	GetWorkflowNodeByJobID(ctx context.Context, jobID string) (*models.WorkflowNode, error)
}

// guardedJobStore is the narrow store capability CancelJob/KillJob need for
// a race-safe status transition (Finding 1: CancelJob TOCTOU — a stale
// in-memory job.Status could otherwise let a cancel request and a worker
// claim both "win" against the same row). Reached via type assertion since
// it's not part of store.Store, same narrow-interface pattern as
// workflowControlStore above. See
// internal/store/postgres_store.PostgresDbStore.UpdateJobStatusGuarded for
// the implementation; internal/worker/corndogs_worker.go defines its own
// identically-shaped interface rather than sharing this one, to avoid a
// worker<->jobcontrol import cycle (jobcontrol already imports worker for
// ComputeWorkflowStatus).
type guardedJobStore interface {
	UpdateJobStatusGuarded(ctx context.Context, jobID string, fromStatuses []string, apply func(*models.Job)) (*models.Job, bool, error)
}

// CancelJob transitions job into the graceful-cancel flow and persists the
// change. Submitted/queued jobs (no container exists yet) race the worker
// to dequeue the Corndogs task before it's claimed; if that race is won the
// job lands directly on "cancelled", otherwise it's left "cancelling" for
// the claiming worker to finalize. Running jobs are marked "cancelling" so
// the worker's cancel-poll drives JobRunner.Stop and eventually lands the
// job on "cancelled". Returns store.ErrNotFound-wrapping errors as-is;
// returns ErrNotCancellable if job is already terminal or already
// cancelling (a second graceful cancel has nothing new to do — see
// KillJob, which can escalate a stuck "cancelling" job).
func CancelJob(ctx context.Context, st store.Store, corndogsClient corndogs.ClientInterface, job *models.Job) (*models.Job, error) {
	return transitionJob(ctx, st, corndogsClient, job, false)
}

// KillJob is CancelJob's immediate-force sibling: submitted/queued jobs are
// cancelled the same way (there's no container to kill yet), but running
// (or already-"cancelling") jobs are marked "cancelling" with
// cancel_mode="kill", which routes the worker's cancel-poll into an
// immediate forced Cleanup instead of a graceful JobRunner.Stop — no
// SIGTERM, no grace period, no guarantee runnerlib's cleanup hooks run.
// Unlike CancelJob, KillJob is also valid against a job that's already
// "cancelling" (models.Job.CanBeKilled): it escalates a stuck graceful
// cancel to an immediate kill rather than being refused. See
// UI_AUTH_PLAN.md's Cancel vs Kill section.
func KillJob(ctx context.Context, st store.Store, corndogsClient corndogs.ClientInterface, job *models.Job) (*models.Job, error) {
	return transitionJob(ctx, st, corndogsClient, job, true)
}

// cancellableFromStatuses returns the set of job statuses the guarded
// transition to "cancelling" is allowed to start from, mirroring
// models.Job.CanBeCancelled/CanBeKilled: kill additionally admits
// "cancelling" itself, to allow escalating a stuck graceful cancel.
func cancellableFromStatuses(kill bool) []string {
	if kill {
		return []string{"submitted", "queued", "running", "cancelling"}
	}
	return []string{"submitted", "queued", "running"}
}

// transitionJob drives a job into (or through) the cancel/kill flow. It
// prefers a guarded (race-safe) store transition — see guardedJobStore —
// and falls back to a best-effort blind Save (logging a warning) if the
// configured store doesn't support it, e.g. a minimal test store.
func transitionJob(ctx context.Context, st store.Store, corndogsClient corndogs.ClientInterface, job *models.Job, kill bool) (*models.Job, error) {
	if job == nil {
		return nil, ErrNotCancellable
	}

	// Fast local pre-check against the caller's (possibly stale) copy of
	// the job, purely to short-circuit an obviously-terminal job without a
	// DB round trip. The authoritative decision is always the guarded
	// transition below (or, in the fallback path, the switch inside
	// transitionJobBestEffort) — this is not where the TOCTOU race is
	// closed.
	allowed := job.CanBeCancelled()
	if kill {
		allowed = job.CanBeKilled()
	}
	if !allowed {
		return job, ErrNotCancellable
	}

	gs, ok := st.(guardedJobStore)
	if !ok {
		logging.Log.WithField("job_id", job.JobID).
			Warn("Store does not support guarded job status transitions; falling back to best-effort cancel (racy under concurrent worker claim)")
		return transitionJobBestEffort(ctx, st, corndogsClient, job, kill)
	}

	cancelMode := "cancel"
	if kill {
		cancelMode = "kill"
	}

	var priorStatus string
	updated, matched, err := gs.UpdateJobStatusGuarded(ctx, job.JobID, cancellableFromStatuses(kill), func(j *models.Job) {
		priorStatus = j.Status
		j.Status = "cancelling"
		j.CancelMode = cancelMode
	})
	if err != nil {
		return job, fmt.Errorf("failed to transition job to cancelling: %w", err)
	}
	if !matched {
		// The row's status had already moved past what we expected by the
		// time the lock was acquired (e.g. a concurrent cancel/kill beat us
		// to it, or the job reached a terminal state) — nothing left for
		// this request to do.
		return job, ErrNotCancellable
	}

	if priorStatus != "submitted" && priorStatus != "queued" {
		// Running (or already-"cancelling", for a kill escalation): hand
		// off to the worker. job_processor.go's cancel-poll (or, for a
		// worker that hasn't claimed the task yet, corndogs_worker.go's
		// claim-path check) does the rest.
		return updated, nil
	}

	// No container exists yet — try to dequeue the Corndogs task before any
	// worker claims it. The current_state passed here is always "submitted":
	// that's Corndogs' own pre-claim task state, independent of whether the
	// job's DB status was "submitted" or "queued".
	if corndogsClient != nil && updated.CorndogsTaskID != nil && *updated.CorndogsTaskID != "" {
		if _, err := corndogsClient.CancelTask(ctx, *updated.CorndogsTaskID, "submitted"); err != nil {
			// A worker already claimed the task (or some other state change
			// beat us to it) — leave the job "cancelling"; the claiming
			// worker's claim-path check finalizes it instead (see
			// internal/worker/corndogs_worker.go).
			logging.Log.WithError(err).WithField("job_id", job.JobID).
				Debug("Corndogs task already claimed or not cancellable pre-claim; leaving job cancelling for the worker to finalize")
			return updated, nil
		}
	}

	lastError := "cancelled"
	if kill {
		lastError = "killed by admin"
	}
	finalized, matched, err := gs.UpdateJobStatusGuarded(ctx, job.JobID, []string{"cancelling"}, func(j *models.Job) {
		j.Status = "cancelled"
		j.LastError = lastError
	})
	if err != nil {
		return updated, fmt.Errorf("failed to finalize cancelled job: %w", err)
	}
	if !matched {
		// Someone else (most likely the worker's own claim-path finalize)
		// already moved the row on; they own the terminal status now.
		return updated, nil
	}
	return finalized, nil
}

// transitionJobBestEffort is the pre-guarded-store fallback: a blind
// load-mutate-Save with no protection against a concurrent worker claim.
// Kept for stores that don't implement guardedJobStore (e.g. minimal test
// mocks); production always runs against postgres_store, which does.
func transitionJobBestEffort(ctx context.Context, st store.Store, corndogsClient corndogs.ClientInterface, job *models.Job, kill bool) (*models.Job, error) {
	switch job.Status {
	case "submitted", "queued":
		// Never started a container — nothing for the worker to do. Cancel
		// the Corndogs task (if any was ever submitted) and land directly on
		// the terminal "cancelled" status.
		if corndogsClient != nil && job.CorndogsTaskID != nil && *job.CorndogsTaskID != "" {
			if _, err := corndogsClient.CancelTask(ctx, *job.CorndogsTaskID, "submitted"); err != nil {
				logging.Log.WithError(err).WithField("job_id", job.JobID).
					Warn("Failed to cancel corndogs task for a not-yet-started job")
			}
		}
		job.Status = "cancelled"
		if kill {
			job.LastError = "killed by admin"
		} else {
			job.LastError = "cancelled"
		}
	case "running", "cancelling":
		// Hand off to the worker: flip to "cancelling" (+ cancel_mode) and
		// let job_processor.go's cancel-poll do the rest on its next
		// heartbeat tick.
		job.Status = "cancelling"
		if kill {
			job.CancelMode = "kill"
		} else {
			job.CancelMode = "cancel"
		}
	default:
		return job, ErrNotCancellable
	}

	job.UpdatedAt = time.Now()
	if err := st.UpdateJob(ctx, job); err != nil {
		return job, fmt.Errorf("failed to update job status: %w", err)
	}
	return job, nil
}

// CancelWorkflow cancels (or kills) a workflow instance: every non-terminal
// node's underlying job is transitioned via CancelJob/KillJob, and
// pending/waiting nodes that never had a job submitted are marked
// "cancelled" outright. The workflow instance itself is marked "cancelling"
// while any node's job is still winding down (graceful stop in progress),
// or directly to its final computed status ("cancelled", or occasionally
// "success"/"skipped"/"failed" if the cascade turns out to be a no-op)
// when every node is already terminal by the time the cascade finishes.
func CancelWorkflow(ctx context.Context, st store.Store, corndogsClient corndogs.ClientInterface, workflowID string, kill bool) (*models.WorkflowInstance, error) {
	ws, ok := st.(workflowControlStore)
	if !ok {
		return nil, ErrWorkflowsUnsupported
	}

	wf, err := ws.GetWorkflowInstance(ctx, workflowID)
	if err != nil {
		return nil, err
	}
	nodes, err := ws.ListWorkflowNodes(ctx, workflowID)
	if err != nil {
		return wf, err
	}

	wf.Status = "cancelling"

	for i := range nodes {
		node := &nodes[i]
		if isNodeTerminal(node.Status) {
			continue
		}

		if node.JobID == nil || *node.JobID == "" {
			// Pending/waiting node: no job was ever submitted for it, so
			// there's nothing for the worker to stop — cancel the node
			// directly.
			now := time.Now().UTC()
			node.Status = "cancelled"
			node.CompletedAt = &now
			node.DecisionReason = cancelDecisionReason(kill)
			if err := ws.UpdateWorkflowNode(ctx, node); err != nil {
				return wf, err
			}
			recordCancelEvent(ctx, ws, wf.WorkflowID, &node.NodeID, nil, kill)
			continue
		}

		job, err := st.GetJobByID(ctx, *node.JobID)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				continue
			}
			return wf, err
		}
		if job.IsCompleted() {
			continue
		}
		if job.IsCancelling() && !kill {
			// Already being gracefully cancelled by a prior request, and
			// this is another graceful cancel — nothing new to do. A kill
			// request, though, can still escalate it (see
			// models.Job.CanBeKilled), so fall through to transitionJob in
			// that case instead of skipping the node.
			continue
		}
		if _, err := transitionJob(ctx, st, corndogsClient, job, kill); err != nil && !errors.Is(err, ErrNotCancellable) {
			return wf, err
		}
		recordCancelEvent(ctx, ws, wf.WorkflowID, &node.NodeID, node.JobID, kill)
	}

	// If every node is already terminal (either it was before we started,
	// or the pending/waiting ones above resolved synchronously and no node
	// was actually mid-run), resolve the workflow's final status now instead
	// of leaving it on the transient "cancelling" value — nothing else will
	// trigger a refresh if no job is still in flight. Otherwise leave
	// "cancelling": the still-running node(s) will drive their own
	// completion through the normal ProcessWorkflowCompletion path (worker
	// package), whose refreshWorkflowStatus call will land on "cancelled"
	// via worker.ComputeWorkflowStatus once they finish stopping.
	if final := worker.ComputeWorkflowStatus(nodes); final != "running" {
		wf.Status = final
		now := time.Now().UTC()
		wf.CompletedAt = &now
	}

	if err := ws.UpdateWorkflowInstance(ctx, wf); err != nil {
		return wf, fmt.Errorf("failed to update workflow instance status: %w", err)
	}
	return wf, nil
}

func isNodeTerminal(status string) bool {
	switch status {
	case "completed", "failed", "cancelled", "timeout", "skipped":
		return true
	default:
		return false
	}
}

func cancelDecisionReason(kill bool) string {
	if kill {
		return "workflow killed by admin"
	}
	return "workflow cancelled"
}

func recordCancelEvent(ctx context.Context, ws workflowControlStore, workflowID string, nodeID, jobID *string, kill bool) {
	eventType := "node_cancelled"
	reason := cancelDecisionReason(kill)
	if err := ws.CreateWorkflowEvent(ctx, &models.WorkflowEvent{
		WorkflowID: workflowID,
		NodeID:     nodeID,
		JobID:      jobID,
		EventType:  eventType,
		Reason:     reason,
		Details:    models.JSONB{},
	}); err != nil {
		logging.Log.WithError(err).WithField("workflow_id", workflowID).Warn("Failed to record workflow cancel event")
	}
}
