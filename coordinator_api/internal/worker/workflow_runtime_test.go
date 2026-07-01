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
}

func (u *workflowRuntimeStatusUpdater) UpdateWorkflowStatus(ctx context.Context, wf *models.WorkflowInstance, nodes []models.WorkflowNode) error {
	u.workflowCalls = append(u.workflowCalls, *wf)
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
