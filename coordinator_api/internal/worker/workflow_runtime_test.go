package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

type workflowRuntimeStore struct {
	*MockStore
	workflows       map[string]*models.WorkflowInstance
	nodes           map[string]*models.WorkflowNode
	nodeByJobID     map[string]string
	vars            map[string]models.JSONB
	events          []models.WorkflowEvent
	historyDuration *int64
}

func newWorkflowRuntimeStore() *workflowRuntimeStore {
	return &workflowRuntimeStore{
		MockStore:   &MockStore{},
		workflows:   map[string]*models.WorkflowInstance{},
		nodes:       map[string]*models.WorkflowNode{},
		nodeByJobID: map[string]string{},
		vars:        map[string]models.JSONB{},
	}
}

func (s *workflowRuntimeStore) CreateWorkflowInstance(ctx context.Context, wf *models.WorkflowInstance) error {
	if wf.WorkflowID == "" {
		wf.WorkflowID = fmt.Sprintf("wf-%d", len(s.workflows)+1)
	}
	copy := *wf
	s.workflows[wf.WorkflowID] = &copy
	return nil
}

func (s *workflowRuntimeStore) GetWorkflowInstance(ctx context.Context, workflowID string) (*models.WorkflowInstance, error) {
	wf, ok := s.workflows[workflowID]
	if !ok {
		return nil, store.ErrNotFound
	}
	copy := *wf
	return &copy, nil
}

func (s *workflowRuntimeStore) UpdateWorkflowInstance(ctx context.Context, wf *models.WorkflowInstance) error {
	copy := *wf
	s.workflows[wf.WorkflowID] = &copy
	return nil
}

func (s *workflowRuntimeStore) CreateWorkflowNode(ctx context.Context, node *models.WorkflowNode) error {
	if node.NodeID == "" {
		node.NodeID = fmt.Sprintf("node-%d", len(s.nodes)+1)
	}
	copy := *node
	s.nodes[node.NodeID] = &copy
	if node.JobID != nil {
		s.nodeByJobID[*node.JobID] = node.NodeID
	}
	return nil
}

func (s *workflowRuntimeStore) UpdateWorkflowNode(ctx context.Context, node *models.WorkflowNode) error {
	copy := *node
	s.nodes[node.NodeID] = &copy
	if node.JobID != nil {
		s.nodeByJobID[*node.JobID] = node.NodeID
	}
	return nil
}

func (s *workflowRuntimeStore) ListWorkflowNodes(ctx context.Context, workflowID string) ([]models.WorkflowNode, error) {
	var out []models.WorkflowNode
	for _, node := range s.nodes {
		if node.WorkflowID == workflowID {
			out = append(out, *node)
		}
	}
	return out, nil
}

func (s *workflowRuntimeStore) GetWorkflowNodeByJobID(ctx context.Context, jobID string) (*models.WorkflowNode, error) {
	nodeID, ok := s.nodeByJobID[jobID]
	if !ok {
		return nil, store.ErrNotFound
	}
	copy := *s.nodes[nodeID]
	return &copy, nil
}

func (s *workflowRuntimeStore) GetWorkflowVars(ctx context.Context, workflowID string) (map[string]models.JSONB, error) {
	out := map[string]models.JSONB{}
	for key, value := range s.vars {
		out[key] = value
	}
	return out, nil
}

func (s *workflowRuntimeStore) UpsertWorkflowVar(ctx context.Context, v *models.WorkflowVar) error {
	s.vars[v.Key] = v.Value
	return nil
}

func (s *workflowRuntimeStore) CreateWorkflowEvent(ctx context.Context, event *models.WorkflowEvent) error {
	s.events = append(s.events, *event)
	return nil
}

func (s *workflowRuntimeStore) ListWorkflowEvents(ctx context.Context, workflowID string, limit, offset int) ([]models.WorkflowEvent, error) {
	return s.events, nil
}

func (s *workflowRuntimeStore) GetLastSuccessfulWorkflowNodeDuration(ctx context.Context, wf *models.WorkflowInstance, nodeName string) (*int64, error) {
	return s.historyDuration, nil
}

type workflowRuntimeStatusUpdater struct {
	MockJobStatusUpdater
	workflowCalls []models.WorkflowInstance
	nodesCalls    [][]models.WorkflowNode
}

func (u *workflowRuntimeStatusUpdater) UpdateWorkflowStatus(ctx context.Context, wf *models.WorkflowInstance, nodes []models.WorkflowNode) error {
	u.workflowCalls = append(u.workflowCalls, *wf)
	nodesCopy := append([]models.WorkflowNode(nil), nodes...)
	u.nodesCalls = append(u.nodesCalls, nodesCopy)
	return nil
}

func TestProcessWorkflowCompletion_OutputConflictFailsNodeAndWorkflow(t *testing.T) {
	tmpDir := t.TempDir()
	output := map[string]interface{}{
		"vars": map[string]interface{}{
			"target": "new-value",
		},
	}
	data, err := json.Marshal(output)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "workflow-output.json"), data, 0644); err != nil {
		t.Fatal(err)
	}

	store := newWorkflowRuntimeStore()
	wf := &models.WorkflowInstance{
		WorkflowID: "wf-1",
		UserID:     "user-1",
		Name:       "Reactorcide Jobs",
		Status:     "running",
		QueueName:  "reactorcide-jobs",
	}
	store.workflows[wf.WorkflowID] = wf
	jobID := "job-1"
	node := &models.WorkflowNode{
		NodeID:     "node-1",
		WorkflowID: wf.WorkflowID,
		Name:       "build",
		Status:     "running",
		JobID:      &jobID,
	}
	store.nodes[node.NodeID] = node
	store.nodeByJobID[jobID] = node.NodeID
	store.vars["target"] = models.JSONB{"value": "old-value"}

	statusUpdater := &workflowRuntimeStatusUpdater{}
	tp := NewTriggerProcessor(store, nil)
	tp.SetStatusUpdater(statusUpdater)
	job := &models.Job{
		JobID:      jobID,
		WorkflowID: &wf.WorkflowID,
		Status:     "completed",
	}

	err = tp.ProcessWorkflowCompletion(context.Background(), tmpDir, job)
	if err == nil {
		t.Fatal("expected workflow var conflict error")
	}
	if got := store.nodes[node.NodeID].Status; got != "failed" {
		t.Fatalf("expected node to fail, got %q", got)
	}
	if got := store.workflows[wf.WorkflowID].Status; got != "failed" {
		t.Fatalf("expected workflow to fail, got %q", got)
	}
	if store.workflows[wf.WorkflowID].LastError == "" {
		t.Fatal("expected workflow last error to be set")
	}
	if len(statusUpdater.workflowCalls) != 1 {
		t.Fatalf("expected one workflow status update, got %d", len(statusUpdater.workflowCalls))
	}
	if statusUpdater.workflowCalls[0].Status != "failed" {
		t.Fatalf("expected failed workflow status update, got %q", statusUpdater.workflowCalls[0].Status)
	}
}

// TestProcessWorkflowJobStarted_ReflectsRetryRebindWithoutDuplicateRow
// verifies the seam jobcontrol.RetryJob relies on instead of pushing its own
// VCS/comment update (see retry.go's design doc comment): once a retried
// job is rebound onto the SAME workflow node (as
// jobcontrol.rebindWorkflowNodeForRetry does — same NodeID, JobID swapped to
// the new job, status reset to "submitted") and that job actually starts
// running, the worker's normal ProcessWorkflowJobStarted hook must find the
// SAME node via GetWorkflowNodeByJobID(newJobID), update it in place, and
// push a workflow status/comment update whose node list still has exactly
// one row for that node — reflecting the CURRENT (retried) job, not a
// second stale row for the original job. This is what makes the workflow PR
// comment table "last run wins" with no duplicate lines even though
// jobcontrol itself never talks to the VCS status updater.
func TestProcessWorkflowJobStarted_ReflectsRetryRebindWithoutDuplicateRow(t *testing.T) {
	store := newWorkflowRuntimeStore()
	wf := &models.WorkflowInstance{
		WorkflowID: "wf-1",
		UserID:     "user-1",
		Name:       "Reactorcide Jobs",
		Status:     "running", // jobcontrol.rebindWorkflowNodeForRetry forces this before the retried job starts.
		QueueName:  "reactorcide-jobs",
	}
	store.workflows[wf.WorkflowID] = wf

	// node-1 originally belonged to job-original (failed); simulate exactly
	// what jobcontrol.rebindWorkflowNodeForRetry does to it on retry: JobID
	// swapped to the new job, status back to "submitted", CompletedAt
	// cleared. Same NodeID throughout — retry never creates a second node.
	newJobID := "job-retried"
	node := &models.WorkflowNode{
		NodeID:      "node-1",
		WorkflowID:  wf.WorkflowID,
		Name:        "build",
		DisplayName: "build",
		Status:      "submitted",
		JobID:       &newJobID,
	}
	store.nodes[node.NodeID] = node
	store.nodeByJobID[newJobID] = node.NodeID

	statusUpdater := &workflowRuntimeStatusUpdater{}
	tp := NewTriggerProcessor(store, nil)
	tp.SetStatusUpdater(statusUpdater)

	retriedJob := &models.Job{
		JobID:          newJobID,
		WorkflowID:     &wf.WorkflowID,
		WorkflowNodeID: &node.NodeID,
		Status:         "running",
	}

	if err := tp.ProcessWorkflowJobStarted(context.Background(), retriedJob); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// The node the worker updated must be the SAME node (by ID), now
	// reflecting the retried job's "running" state.
	updatedNode := store.nodes[node.NodeID]
	if updatedNode.Status != "running" {
		t.Fatalf("expected node-1 status 'running' after the retried job started, got %q", updatedNode.Status)
	}
	if updatedNode.JobID == nil || *updatedNode.JobID != newJobID {
		t.Fatalf("expected node-1 to still be bound to the retried job %q, got %v", newJobID, updatedNode.JobID)
	}

	// Exactly one node exists for the workflow — retry rebinding never
	// spawned a duplicate row.
	nodes, err := store.ListWorkflowNodes(context.Background(), wf.WorkflowID)
	if err != nil {
		t.Fatalf("unexpected error listing nodes: %v", err)
	}
	if len(nodes) != 1 {
		t.Fatalf("expected exactly 1 node for the workflow after retry, got %d: %+v", len(nodes), nodes)
	}

	// The VCS/comment status updater was driven with that single,
	// up-to-date node — this is what renderWorkflowCommentBody (see
	// internal/vcs/workflow_status.go) iterates to build the PR comment
	// table, so the retried node's row reflects its current job/status
	// with no duplicate line for the old job.
	if len(statusUpdater.nodesCalls) != 1 {
		t.Fatalf("expected 1 workflow status update, got %d", len(statusUpdater.nodesCalls))
	}
	pushedNodes := statusUpdater.nodesCalls[0]
	if len(pushedNodes) != 1 {
		t.Fatalf("expected the pushed node list to have exactly 1 node, got %d: %+v", len(pushedNodes), pushedNodes)
	}
	if pushedNodes[0].NodeID != "node-1" || pushedNodes[0].Status != "running" {
		t.Fatalf("expected the pushed node to be node-1 in 'running' status, got %+v", pushedNodes[0])
	}
}

func TestEnsureWorkflow_CommentMarkerIncludesEventType(t *testing.T) {
	notes := `{"vcs_provider":"github","repo":"org/repo","commit_sha":"abc123","pr_number":42}`
	cases := []struct {
		name       string
		eventType  interface{}
		wantMarker string
	}{
		{
			name:       "pr checks event",
			eventType:  "pull_request_updated",
			wantMarker: "<!-- reactorcide:workflows:abc123:pull_request_updated -->",
		},
		{
			name:       "post-merge event on same commit gets a distinct marker",
			eventType:  "pull_request_merged",
			wantMarker: "<!-- reactorcide:workflows:abc123:pull_request_merged -->",
		},
		{
			name:       "missing event type falls back to directly_submitted",
			eventType:  nil,
			wantMarker: "<!-- reactorcide:workflows:abc123:directly_submitted -->",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := newWorkflowRuntimeStore()
			tp := NewTriggerProcessor(store, nil)
			envVars := models.JSONB{}
			if tc.eventType != nil {
				envVars["REACTORCIDE_EVENT_TYPE"] = tc.eventType
			}
			parentJob := &models.Job{
				JobID:      "parent-1",
				UserID:     "user-1",
				Notes:      notes,
				JobEnvVars: envVars,
			}

			wf, err := tp.ensureWorkflow(context.Background(), parentJob, nil)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if wf.CommentMarker != tc.wantMarker {
				t.Fatalf("expected marker %q, got %q", tc.wantMarker, wf.CommentMarker)
			}
		})
	}
}

func TestCreateWorkflowNode_LoadsPreviousSuccessfulDuration(t *testing.T) {
	store := newWorkflowRuntimeStore()
	duration := int64(42000)
	store.historyDuration = &duration
	tp := NewTriggerProcessor(store, nil)
	wf := &models.WorkflowInstance{
		WorkflowID: "wf-1",
		UserID:     "user-1",
		Name:       "Reactorcide Jobs",
		Status:     "evaluating",
		QueueName:  "reactorcide-jobs",
	}

	err := tp.createWorkflowNode(context.Background(), wf, triggerJobSpec{
		JobName:        "test",
		ContainerImage: "alpine:latest",
		JobCommand:     "echo test",
	}, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(store.nodes) != 1 {
		t.Fatalf("expected one node, got %d", len(store.nodes))
	}
	for _, node := range store.nodes {
		if node.LastSuccessfulDurationMs == nil {
			t.Fatal("expected previous duration to be set")
		}
		if *node.LastSuccessfulDurationMs != duration {
			t.Fatalf("expected duration %d, got %d", duration, *node.LastSuccessfulDurationMs)
		}
	}
}
