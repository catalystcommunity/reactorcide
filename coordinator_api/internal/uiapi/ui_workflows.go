package uiapi

import (
	"context"
	"sort"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/uiapi/csilapi"
)

// workflowsVisibleToStore is jobsVisibleToStore's counterpart for workflows:
// the same SQL-side visibility predicate, over the
// "workflow_instances UNION ALL loose jobs" read model. See
// postgres_store/visibility_operations.go's ListWorkflowSummariesVisibleTo.
type workflowsVisibleToStore interface {
	ListWorkflowSummariesVisibleTo(ctx context.Context, viewerID string, isGlobalAdmin bool, filters map[string]interface{}, limit, offset int) ([]models.WorkflowSummary, int64, error)
}

// workflowDetailStore is what GetWorkflow needs beyond the list: the single
// summary for the authorization check, and the node rows the DAG view reads.
// ListWorkflowNodes has existed on the postgres store since workflows landed
// and was never exposed by any API.
type workflowDetailStore interface {
	GetWorkflowSummary(ctx context.Context, workflowID string) (*models.WorkflowSummary, error)
	ListWorkflowNodes(ctx context.Context, workflowID string) ([]models.WorkflowNode, error)
}

func workflowToSummary(wf *models.WorkflowSummary) csilapi.WorkflowSummaryDetail {
	detail := csilapi.WorkflowSummaryDetail{
		WorkflowId:       wf.WorkflowID,
		Kind:             wf.Kind,
		Name:             wf.Name,
		Status:           wf.Status,
		ProjectId:        wf.ProjectID,
		CreatedAt:        formatTime(wf.CreatedAt),
		UpdatedAt:        formatTime(wf.UpdatedAt),
		CompletedAt:      formatTimePtr(wf.CompletedAt),
		QueueName:        wf.QueueName,
		VcsRepo:          wf.VCSRepo,
		CommitSha:        wf.CommitSHA,
		JobCount:         int64(wf.JobCount),
		RunningCount:     int64(wf.RunningCount),
		CompletedCount:   int64(wf.CompletedCount),
		FailedCount:      int64(wf.FailedCount),
		SkippedCount:     int64(wf.SkippedCount),
		LooseJobId:       wf.LooseJobID,
		DecisionSummary:  wf.DecisionSummary,
		ParentJobId:      wf.ParentJobID,
		RootWorkflowId:   wf.RootWorkflowID,
		ParentWorkflowId: wf.ParentWorkflowID,
		OriginJobId:      wf.OriginJobID,
		OriginType:       wf.OriginType,
		TriggerType:      wf.TriggerType,
		CiOrigin:         wf.CIOrigin,
		ExecutionProfile: wf.ExecutionProfile,
		WorkerClass:      wf.WorkerClass,
	}
	if wf.PRNumber != nil {
		n := int64(*wf.PRNumber)
		detail.PrNumber = &n
	}
	if wf.LooseJobExit != nil {
		n := int64(*wf.LooseJobExit)
		detail.LooseJobExit = &n
	}
	return detail
}

func workflowNodeToSummary(node *models.WorkflowNode) csilapi.WorkflowNodeSummary {
	summary := csilapi.WorkflowNodeSummary{
		NodeId:      node.NodeID,
		Name:        node.Name,
		DisplayName: node.DisplayName,
		Status:      node.Status,
		// depends_on is `[* text]`, not optional, so a root node with no
		// dependencies must encode as an empty array rather than a null.
		DependsOn:        append([]string{}, node.DependsOn...),
		Condition:        node.Condition,
		JobId:            node.JobID,
		DecisionReason:   node.DecisionReason,
		CompletedAt:      formatTimePtr(node.CompletedAt),
		CiOrigin:         node.CIOrigin,
		ExecutionProfile: node.ExecutionProfile,
		WorkerClass:      node.WorkerClass,
	}
	if node.ItemIndex != nil {
		n := int64(*node.ItemIndex)
		summary.ItemIndex = &n
	}
	return summary
}

// ListWorkflows returns the page of workflows visible to the caller, with an
// exact total. See ui_jobs.go's jobsVisibleToStore for why filtering happens
// in SQL rather than after pagination.
func (s *UiService) ListWorkflows(ctx context.Context, req csilapi.ListWorkflowsRequest) (csilapi.ListWorkflowsResponse, error) {
	visibleStore, ok := s.deps.Store.(workflowsVisibleToStore)
	if !ok {
		return csilapi.ListWorkflowsResponse{}, NewServiceError("internal", "workflow listing is not available on this server")
	}
	viewerID, isGlobalAdmin, err := s.viewerScope(ctx)
	if err != nil {
		return csilapi.ListWorkflowsResponse{}, err
	}
	limit, offset := listPageBounds(req.Limit, req.Offset)

	filters := map[string]interface{}{}
	if req.Status != nil && *req.Status != "" {
		filters["status"] = *req.Status
	}
	if req.ProjectId != nil && *req.ProjectId != "" {
		filters["project_id"] = *req.ProjectId
	}

	workflows, total, err := visibleStore.ListWorkflowSummariesVisibleTo(ctx, viewerID, isGlobalAdmin, filters, limit, offset)
	if err != nil {
		return csilapi.ListWorkflowsResponse{}, NewServiceError("internal", "failed to list workflows")
	}
	response := csilapi.ListWorkflowsResponse{
		Workflows: make([]csilapi.WorkflowSummaryDetail, 0, len(workflows)),
		Total:     total,
		Limit:     int64(limit),
		Offset:    int64(offset),
	}
	for i := range workflows {
		response.Workflows = append(response.Workflows, workflowToSummary(&workflows[i]))
	}
	return response, nil
}

// GetWorkflow returns one workflow with its nodes and its jobs inline.
//
// The nodes carry depends_on, which is what a DAG view needs and what no API
// exposed before. They ride along rather than living behind a second op: a
// workflow is small next to everything else the UI already transfers, so a
// second round trip to draw its graph buys nothing.
//
// A workflow the caller cannot view reports "not found" for the same reason
// GetJob does: "forbidden" would confirm the id exists.
func (s *UiService) GetWorkflow(ctx context.Context, req csilapi.GetWorkflowRequest) (csilapi.GetWorkflowResponse, error) {
	if err := requireNonEmpty("workflow_id", req.WorkflowId, 64); err != nil {
		return csilapi.GetWorkflowResponse{}, err
	}
	detailStore, ok := s.deps.Store.(workflowDetailStore)
	if !ok {
		return csilapi.GetWorkflowResponse{}, NewServiceError("internal", "workflow detail is not available on this server")
	}
	identity, _ := s.deps.resolveIdentity(ctx)

	workflow, err := detailStore.GetWorkflowSummary(ctx, req.WorkflowId)
	if err != nil {
		return csilapi.GetWorkflowResponse{}, mapStoreErr(err, "workflow not found")
	}
	visible, err := s.deps.Resolver.CanViewWorkflowSummary(ctx, identity, workflow)
	if err != nil {
		return csilapi.GetWorkflowResponse{}, NewServiceError("internal", "an internal error occurred")
	}
	if !visible {
		return csilapi.GetWorkflowResponse{}, NewServiceError("not_found", "workflow not found")
	}

	response := csilapi.GetWorkflowResponse{
		Workflow: workflowToSummary(workflow),
		Nodes:    []csilapi.WorkflowNodeSummary{},
		Jobs:     []csilapi.JobSummary{},
	}

	nodes, err := detailStore.ListWorkflowNodes(ctx, req.WorkflowId)
	if err != nil {
		return csilapi.GetWorkflowResponse{}, NewServiceError("internal", "failed to read workflow nodes")
	}
	// Stable order so a re-render does not reshuffle the graph. The DAG view
	// computes its own layering; this only removes row-order nondeterminism.
	sort.SliceStable(nodes, func(i, j int) bool { return nodes[i].Name < nodes[j].Name })
	for i := range nodes {
		response.Nodes = append(response.Nodes, workflowNodeToSummary(&nodes[i]))
	}

	// The workflow's jobs come through the same visibility-filtered list path
	// the job list uses. They are already known visible (a job inherits its
	// workflow's project/org), but routing them through one code path keeps a
	// future divergence from becoming a leak here.
	if visibleStore, ok := s.deps.Store.(jobsVisibleToStore); ok {
		viewerID, isGlobalAdmin, scopeErr := s.viewerScope(ctx)
		if scopeErr != nil {
			return csilapi.GetWorkflowResponse{}, scopeErr
		}
		jobs, _, jobErr := visibleStore.ListJobsVisibleTo(ctx, viewerID, isGlobalAdmin,
			map[string]interface{}{"workflow_id": req.WorkflowId}, maxListLimit, 0)
		if jobErr != nil {
			return csilapi.GetWorkflowResponse{}, NewServiceError("internal", "failed to read workflow jobs")
		}
		for i := range jobs {
			response.Jobs = append(response.Jobs, jobToSummary(&jobs[i]))
		}
	}

	return response, nil
}
