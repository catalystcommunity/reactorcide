package worker

import (
	"context"
	"sync"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/workflowevents"
)

// publishingWorkflowStore emits a lifecycle event after every workflow or node
// write that succeeds.
//
// This is a decorator rather than a publish call at each site on purpose.
// workflow_runtime.go writes workflow and node rows from roughly a dozen
// places, and the failure mode of the per-site approach is silent: a new write
// path added later simply does not emit an event, and the symptom is "the UI
// sometimes does not update", which is miserable to trace. Wrapping the one
// place every caller obtains its store from (TriggerProcessor.workflowStore)
// means a new write path is covered the moment it is written.
//
// Events are published only AFTER the underlying write returns nil. A failed
// write must not announce a state that was never persisted.
type publishingWorkflowStore struct {
	workflowStore

	// mu guards workflows, which caches the parent workflow of nodes this
	// decorator has already looked up. A node row carries no ownership of its
	// own, and an event has to carry ownership so a stream can authorize it
	// without a query, so a node update needs its workflow. The cache holds
	// only for this decorator's lifetime (one operation), and it is used only
	// for OWNERSHIP -- which cannot change -- so a stale status in a cached
	// row cannot produce a wrong event.
	mu        sync.Mutex
	workflows map[string]*models.WorkflowInstance
}

func newPublishingWorkflowStore(inner workflowStore) *publishingWorkflowStore {
	return &publishingWorkflowStore{
		workflowStore: inner,
		workflows:     make(map[string]*models.WorkflowInstance),
	}
}

func (p *publishingWorkflowStore) remember(wf *models.WorkflowInstance) {
	if wf == nil || wf.WorkflowID == "" {
		return
	}
	p.mu.Lock()
	p.workflows[wf.WorkflowID] = wf
	p.mu.Unlock()
}

// workflowFor resolves a node's parent workflow, preferring the cache.
func (p *publishingWorkflowStore) workflowFor(ctx context.Context, workflowID string) *models.WorkflowInstance {
	if workflowID == "" {
		return nil
	}
	p.mu.Lock()
	cached, ok := p.workflows[workflowID]
	p.mu.Unlock()
	if ok {
		return cached
	}
	wf, err := p.workflowStore.GetWorkflowInstance(ctx, workflowID)
	if err != nil || wf == nil {
		// No workflow means no ownership to attach, so no event. The UI falls
		// back to its repair poll rather than receiving a frame the stream
		// would have to drop anyway.
		return nil
	}
	p.remember(wf)
	return wf
}

func (p *publishingWorkflowStore) CreateWorkflowInstance(ctx context.Context, wf *models.WorkflowInstance) error {
	if err := p.workflowStore.CreateWorkflowInstance(ctx, wf); err != nil {
		return err
	}
	p.remember(wf)
	workflowevents.WorkflowCreated(ctx, wf)
	return nil
}

func (p *publishingWorkflowStore) UpdateWorkflowInstance(ctx context.Context, wf *models.WorkflowInstance) error {
	if err := p.workflowStore.UpdateWorkflowInstance(ctx, wf); err != nil {
		return err
	}
	p.remember(wf)
	workflowevents.WorkflowUpdated(ctx, wf)
	return nil
}

func (p *publishingWorkflowStore) CreateWorkflowNode(ctx context.Context, node *models.WorkflowNode) error {
	if err := p.workflowStore.CreateWorkflowNode(ctx, node); err != nil {
		return err
	}
	// A created node is a node update from the DAG view's point of view: the
	// graph gained a vertex and needs to redraw.
	workflowevents.NodeUpdated(ctx, p.workflowFor(ctx, node.WorkflowID), node)
	return nil
}

func (p *publishingWorkflowStore) UpdateWorkflowNode(ctx context.Context, node *models.WorkflowNode) error {
	if err := p.workflowStore.UpdateWorkflowNode(ctx, node); err != nil {
		return err
	}
	workflowevents.NodeUpdated(ctx, p.workflowFor(ctx, node.WorkflowID), node)
	return nil
}
