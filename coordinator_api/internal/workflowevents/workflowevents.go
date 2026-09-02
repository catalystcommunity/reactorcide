// Package workflowevents publishes workflow and job lifecycle events.
//
// It exists so the workflow lifecycle can emit events without internal/pubsub
// having to know about store models. pubsub stays a pure transport (envelopes,
// NOTIFY, a fan-out bus); this package is the thin adapter that turns a model
// row into the event fields a stream authorizes from.
//
// Every function here is a no-op when no publisher is wired, so a process that
// never called pubsub.SetDefaultPublisher (a test, a CLI, a single-replica
// deployment with no pgx pool) simply publishes nothing.
package workflowevents

import (
	"context"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/pubsub"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

func workflowRef(wf *models.WorkflowInstance) pubsub.WorkflowRef {
	ref := pubsub.WorkflowRef{
		WorkflowID:  wf.WorkflowID,
		Status:      wf.Status,
		UpdatedAt:   wf.UpdatedAt.UTC().Format(timeLayout),
		OwnerUserID: wf.OwnershipOrgID(),
	}
	if wf.ProjectID != nil {
		ref.ProjectID = *wf.ProjectID
	}
	return ref
}

// timeLayout matches the RFC3339Nano format every other event field uses.
const timeLayout = "2006-01-02T15:04:05.999999999Z07:00"

// WorkflowCreated announces a new workflow instance so a list view can insert
// it without polling.
func WorkflowCreated(ctx context.Context, wf *models.WorkflowInstance) {
	if wf == nil {
		return
	}
	pubsub.Default().PublishWorkflowCreated(ctx, workflowRef(wf))
}

// WorkflowUpdated announces a workflow status transition.
func WorkflowUpdated(ctx context.Context, wf *models.WorkflowInstance) {
	if wf == nil {
		return
	}
	pubsub.Default().PublishWorkflowUpdate(ctx, workflowRef(wf))
}

// NodeUpdated announces a node transition. The node event carries the parent
// workflow's ownership, because a node row has none of its own and a stream
// must be able to authorize the frame without a lookup.
func NodeUpdated(ctx context.Context, wf *models.WorkflowInstance, node *models.WorkflowNode) {
	if wf == nil || node == nil {
		return
	}
	pubsub.Default().PublishWorkflowNodeUpdate(ctx, workflowRef(wf), node.NodeID, node.Name, node.Status)
}

// JobCreated announces a newly submitted job.
func JobCreated(ctx context.Context, job *models.Job) {
	if job == nil {
		return
	}
	ref := pubsub.JobRef{
		JobID:       job.JobID,
		Status:      job.Status,
		UpdatedAt:   job.UpdatedAt.UTC().Format(timeLayout),
		OwnerUserID: job.OwnershipOrgID(),
	}
	if job.ProjectID != nil {
		ref.ProjectID = *job.ProjectID
	}
	if job.WorkflowID != nil {
		ref.WorkflowID = *job.WorkflowID
	}
	pubsub.Default().PublishJobCreated(ctx, ref)
}

// ProjectUpdated announces a project settings change, visibility included.
// A stream caching "this caller may see project X" drops that answer when this
// arrives.
func ProjectUpdated(ctx context.Context, project *models.Project) {
	if project == nil {
		return
	}
	pubsub.Default().PublishProjectUpdate(ctx, project.ProjectID, project.OwnershipOrgID())
}
