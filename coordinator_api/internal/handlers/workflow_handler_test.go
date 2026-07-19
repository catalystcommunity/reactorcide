package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/checkauth"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/corndogs"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

// mockWorkflowStore extends MockStore with the narrow workflow persistence
// methods internal/jobcontrol.CancelWorkflow and WorkflowHandler.CancelWorkflow
// need (workflowControlStore / workflowInstanceGetter), plus the create
// operations and the full worker.TriggerProcessor-required method set
// (GetWorkflowVars/UpsertWorkflowVar/ListWorkflowEvents/
// GetWorkflowNodeByJobID) that the retry handlers additionally need —
// jobcontrol.RetryWorkflow drives initial submission of a freshly created
// instance via worker.TriggerProcessor.EvaluateWorkflow, which type-asserts
// the store against its own (structurally matched) workflowStore interface.
// It's an in-memory fake, not a mock-with-Func-fields like MockStore, since
// CancelWorkflow/RetryWorkflow/RetryUnsuccessfulJobs all read back their own
// writes (list nodes, cancel/retry some, recompute status).
type mockWorkflowStore struct {
	*MockStore

	instances map[string]*models.WorkflowInstance
	nodes     map[string][]models.WorkflowNode
	jobs      map[string]*models.Job
	vars      map[string]models.JSONB
	events    []models.WorkflowEvent

	updatedInstances []models.WorkflowInstance
	updatedNodes     []models.WorkflowNode
	nextWfID         int
	nextNodeID       int
}

func newMockWorkflowStore() *mockWorkflowStore {
	m := &mockWorkflowStore{
		MockStore: &MockStore{},
		instances: map[string]*models.WorkflowInstance{},
		nodes:     map[string][]models.WorkflowNode{},
		jobs:      map[string]*models.Job{},
		vars:      map[string]models.JSONB{},
	}
	m.MockStore.GetJobByIDFunc = func(ctx context.Context, jobID string) (*models.Job, error) {
		job, ok := m.jobs[jobID]
		if !ok {
			return nil, store.ErrNotFound
		}
		cp := *job
		return &cp, nil
	}
	m.MockStore.UpdateJobFunc = func(ctx context.Context, job *models.Job) error {
		cp := *job
		m.jobs[job.JobID] = &cp
		return nil
	}
	m.MockStore.CreateJobFunc = func(ctx context.Context, job *models.Job) error {
		if job.JobID == "" {
			job.JobID = fmt.Sprintf("job-%d", len(m.jobs)+1)
		}
		cp := *job
		m.jobs[job.JobID] = &cp
		return nil
	}
	return m
}

func (m *mockWorkflowStore) CreateWorkflowInstance(ctx context.Context, wf *models.WorkflowInstance) error {
	if wf.WorkflowID == "" {
		m.nextWfID++
		wf.WorkflowID = fmt.Sprintf("wf-%d", m.nextWfID)
	}
	cp := *wf
	m.instances[wf.WorkflowID] = &cp
	return nil
}

func (m *mockWorkflowStore) CreateWorkflowNode(ctx context.Context, node *models.WorkflowNode) error {
	if node.NodeID == "" {
		m.nextNodeID++
		node.NodeID = fmt.Sprintf("node-%d", m.nextNodeID)
	}
	m.nodes[node.WorkflowID] = append(m.nodes[node.WorkflowID], *node)
	return nil
}

func (m *mockWorkflowStore) GetWorkflowNodeByJobID(ctx context.Context, jobID string) (*models.WorkflowNode, error) {
	for _, nodes := range m.nodes {
		for i := range nodes {
			if nodes[i].JobID != nil && *nodes[i].JobID == jobID {
				cp := nodes[i]
				return &cp, nil
			}
		}
	}
	return nil, store.ErrNotFound
}

func (m *mockWorkflowStore) GetWorkflowVars(ctx context.Context, workflowID string) (map[string]models.JSONB, error) {
	return map[string]models.JSONB{}, nil
}

func (m *mockWorkflowStore) UpsertWorkflowVar(ctx context.Context, v *models.WorkflowVar) error {
	return nil
}

func (m *mockWorkflowStore) ListWorkflowEvents(ctx context.Context, workflowID string, limit, offset int) ([]models.WorkflowEvent, error) {
	return m.events, nil
}

func (m *mockWorkflowStore) GetWorkflowInstance(ctx context.Context, workflowID string) (*models.WorkflowInstance, error) {
	wf, ok := m.instances[workflowID]
	if !ok {
		return nil, store.ErrNotFound
	}
	cp := *wf
	return &cp, nil
}

func (m *mockWorkflowStore) UpdateWorkflowInstance(ctx context.Context, wf *models.WorkflowInstance) error {
	cp := *wf
	m.instances[wf.WorkflowID] = &cp
	m.updatedInstances = append(m.updatedInstances, cp)
	return nil
}

func (m *mockWorkflowStore) ListWorkflowNodes(ctx context.Context, workflowID string) ([]models.WorkflowNode, error) {
	out := append([]models.WorkflowNode{}, m.nodes[workflowID]...)
	return out, nil
}

func (m *mockWorkflowStore) UpdateWorkflowNode(ctx context.Context, node *models.WorkflowNode) error {
	nodes := m.nodes[node.WorkflowID]
	for i := range nodes {
		if nodes[i].NodeID == node.NodeID {
			nodes[i] = *node
		}
	}
	m.nodes[node.WorkflowID] = nodes
	m.updatedNodes = append(m.updatedNodes, *node)
	return nil
}

func (m *mockWorkflowStore) CreateWorkflowEvent(ctx context.Context, event *models.WorkflowEvent) error {
	return nil
}

func workflowCancelRequest(workflowID, userID string) *http.Request {
	req := httptest.NewRequest("PUT", "/api/v1/workflows/"+workflowID+"/cancel", nil)
	user := &models.User{UserID: userID}
	ctx := checkauth.SetUserContext(req.Context(), user)
	ctx = context.WithValue(ctx, GetContextKey("workflow_id"), workflowID)
	return req.WithContext(ctx)
}

// TestWorkflowHandler_CancelWorkflow_CascadesToNodes verifies PUT
// /api/v1/workflows/{id}/cancel: a running node's job is moved to
// "cancelling" (graceful, worker-driven), and a pending node with no job is
// marked "cancelled" directly. See internal/jobcontrol.CancelWorkflow.
func TestWorkflowHandler_CancelWorkflow_CascadesToNodes(t *testing.T) {
	ms := newMockWorkflowStore()
	ms.instances["wf-1"] = &models.WorkflowInstance{WorkflowID: "wf-1", UserID: "test-user-id", Status: "running"}
	ms.jobs["job-running"] = &models.Job{JobID: "job-running", Status: "running", UserID: "test-user-id"}
	ms.nodes["wf-1"] = []models.WorkflowNode{
		{NodeID: "node-running", WorkflowID: "wf-1", Name: "build", Status: "running", JobID: strPtr("job-running")},
		{NodeID: "node-pending", WorkflowID: "wf-1", Name: "deploy", Status: "pending"},
		{NodeID: "node-done", WorkflowID: "wf-1", Name: "lint", Status: "completed"},
	}

	handler := NewWorkflowHandlerWithCorndogs(ms, nil)
	req := workflowCancelRequest("wf-1", "test-user-id")
	w := httptest.NewRecorder()
	handler.CancelWorkflow(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", w.Code, w.Body.String())
	}

	job, ok := ms.jobs["job-running"]
	if !ok {
		t.Fatal("expected job-running to still exist")
	}
	if job.Status != "cancelling" {
		t.Errorf("expected running node's job to move to 'cancelling', got %s", job.Status)
	}

	nodes := ms.nodes["wf-1"]
	var pending, done *models.WorkflowNode
	for i := range nodes {
		switch nodes[i].NodeID {
		case "node-pending":
			pending = &nodes[i]
		case "node-done":
			done = &nodes[i]
		}
	}
	if pending == nil || pending.Status != "cancelled" {
		t.Errorf("expected pending node to be cancelled directly, got %+v", pending)
	}
	if done == nil || done.Status != "completed" {
		t.Errorf("expected already-terminal node to be left untouched, got %+v", done)
	}

	// A running node's job is still mid-cancel (worker hasn't confirmed the
	// stop yet), so the instance should stay on the transient "cancelling"
	// status rather than jumping straight to "cancelled".
	if len(ms.updatedInstances) == 0 {
		t.Fatal("expected at least one workflow instance update")
	}
	last := ms.updatedInstances[len(ms.updatedInstances)-1]
	if last.Status != "cancelling" {
		t.Errorf("expected workflow instance status 'cancelling' while a node is still stopping, got %s", last.Status)
	}
}

// TestWorkflowHandler_CancelWorkflow_AllPendingResolvesImmediately verifies
// that when every non-terminal node was pending/waiting (no jobs to stop),
// the workflow instance resolves straight to "cancelled" rather than being
// left on the transient "cancelling" status forever.
func TestWorkflowHandler_CancelWorkflow_AllPendingResolvesImmediately(t *testing.T) {
	ms := newMockWorkflowStore()
	ms.instances["wf-2"] = &models.WorkflowInstance{WorkflowID: "wf-2", UserID: "test-user-id", Status: "evaluating"}
	ms.nodes["wf-2"] = []models.WorkflowNode{
		{NodeID: "node-a", WorkflowID: "wf-2", Name: "a", Status: "pending"},
		{NodeID: "node-b", WorkflowID: "wf-2", Name: "b", Status: "waiting"},
	}

	handler := NewWorkflowHandlerWithCorndogs(ms, nil)
	req := workflowCancelRequest("wf-2", "test-user-id")
	w := httptest.NewRecorder()
	handler.CancelWorkflow(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", w.Code, w.Body.String())
	}
	last := ms.updatedInstances[len(ms.updatedInstances)-1]
	if last.Status != "cancelled" {
		t.Errorf("expected workflow instance status 'cancelled' once every node resolved synchronously, got %s", last.Status)
	}
}

// TestWorkflowHandler_CancelWorkflow_Forbidden verifies a non-owner,
// non-admin caller cannot cancel someone else's workflow.
func TestWorkflowHandler_CancelWorkflow_Forbidden(t *testing.T) {
	ms := newMockWorkflowStore()
	ms.instances["wf-3"] = &models.WorkflowInstance{WorkflowID: "wf-3", UserID: "owner-id", Status: "running"}

	handler := NewWorkflowHandlerWithCorndogs(ms, nil)
	req := workflowCancelRequest("wf-3", "someone-else")
	w := httptest.NewRecorder()
	handler.CancelWorkflow(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("expected status 403, got %d", w.Code)
	}
}

// TestWorkflowHandler_CancelWorkflow_NotFound verifies a missing workflow ID
// returns 404 rather than panicking or cancelling nothing silently.
func TestWorkflowHandler_CancelWorkflow_NotFound(t *testing.T) {
	ms := newMockWorkflowStore()

	handler := NewWorkflowHandlerWithCorndogs(ms, nil)
	req := workflowCancelRequest("does-not-exist", "test-user-id")
	w := httptest.NewRecorder()
	handler.CancelWorkflow(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected status 404, got %d", w.Code)
	}
}

func workflowRetryRequest(path, workflowID, userID string) *http.Request {
	req := httptest.NewRequest("POST", "/api/v1/workflows/"+workflowID+path, nil)
	user := &models.User{UserID: userID}
	ctx := checkauth.SetUserContext(req.Context(), user)
	ctx = context.WithValue(ctx, GetContextKey("workflow_id"), workflowID)
	return req.WithContext(ctx)
}

// TestWorkflowHandler_RetryWorkflow_CreatesFreshInstance verifies POST
// /api/v1/workflows/{id}/retry on a failed workflow creates and returns a
// brand-new workflow instance with a fresh node submitted, leaving the old
// instance untouched. See internal/jobcontrol.RetryWorkflow.
func TestWorkflowHandler_RetryWorkflow_CreatesFreshInstance(t *testing.T) {
	ms := newMockWorkflowStore()
	ms.jobs["eval-job"] = &models.Job{JobID: "eval-job", UserID: "test-user-id", QueueName: "reactorcide-jobs", RunnerImage: "runner:latest", CodeDir: "/job/src", JobDir: "/job/src"}
	// IDs deliberately avoid the "wf-N"/"node-N" shape newMockWorkflowStore's
	// CreateWorkflowInstance/CreateWorkflowNode counters generate, so the
	// brand-new instance/node RetryWorkflow creates can't coincidentally
	// collide with (and overwrite) these pre-seeded old ones in the map.
	ms.instances["wf-orig"] = &models.WorkflowInstance{
		WorkflowID:  "wf-orig",
		UserID:      "test-user-id",
		ParentJobID: strPtr("eval-job"),
		Name:        "CI",
		Status:      "failed",
	}
	ms.nodes["wf-orig"] = []models.WorkflowNode{
		{NodeID: "node-orig", WorkflowID: "wf-orig", Name: "build", DisplayName: "build", Status: "failed", JobID: strPtr("job-old"), JobSpec: models.JSONB{"job_name": "build", "job_command": "make build"}},
	}
	ms.jobs["job-old"] = &models.Job{JobID: "job-old", UserID: "test-user-id", Status: "failed"}

	handler := NewWorkflowHandlerWithCorndogs(ms, corndogs.NewMockClient())
	req := workflowRetryRequest("/retry", "wf-orig", "test-user-id")
	w := httptest.NewRecorder()
	handler.RetryWorkflow(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected status 201, got %d: %s", w.Code, w.Body.String())
	}

	var resp models.WorkflowInstance
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if resp.WorkflowID == "wf-orig" {
		t.Error("expected a distinct new workflow ID")
	}
	if resp.Status != "running" {
		t.Errorf("expected new workflow status 'running', got %q", resp.Status)
	}

	// Old instance/nodes untouched.
	if ms.instances["wf-orig"].Status != "failed" {
		t.Errorf("expected old workflow instance to remain 'failed', got %q", ms.instances["wf-orig"].Status)
	}
	if len(ms.nodes["wf-orig"]) != 1 || ms.nodes["wf-orig"][0].Status != "failed" {
		t.Errorf("expected old node untouched, got %+v", ms.nodes["wf-orig"])
	}
}

// TestWorkflowHandler_RetryWorkflow_NotRetryable verifies a running workflow
// (not failed/cancelled) is refused with 400.
func TestWorkflowHandler_RetryWorkflow_NotRetryable(t *testing.T) {
	ms := newMockWorkflowStore()
	ms.instances["wf-1"] = &models.WorkflowInstance{WorkflowID: "wf-1", UserID: "test-user-id", Status: "running"}

	handler := NewWorkflowHandlerWithCorndogs(ms, nil)
	req := workflowRetryRequest("/retry", "wf-1", "test-user-id")
	w := httptest.NewRecorder()
	handler.RetryWorkflow(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected status 400 for a non-retryable (running) workflow, got %d", w.Code)
	}
}

// TestWorkflowHandler_RetryWorkflow_Forbidden verifies a non-owner,
// non-admin caller cannot retry someone else's workflow.
func TestWorkflowHandler_RetryWorkflow_Forbidden(t *testing.T) {
	ms := newMockWorkflowStore()
	ms.instances["wf-1"] = &models.WorkflowInstance{WorkflowID: "wf-1", UserID: "owner-id", Status: "failed"}

	handler := NewWorkflowHandlerWithCorndogs(ms, nil)
	req := workflowRetryRequest("/retry", "wf-1", "someone-else")
	w := httptest.NewRecorder()
	handler.RetryWorkflow(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("expected status 403, got %d", w.Code)
	}
}

// TestWorkflowHandler_RetryUnsuccessfulJobs_RetriesInPlace verifies POST
// /api/v1/workflows/{id}/retry-unsuccessful job-retries every failed/
// cancelled member job in place — same workflow instance, same nodes (no
// new workflow_id) — while leaving the already-completed node alone.
func TestWorkflowHandler_RetryUnsuccessfulJobs_RetriesInPlace(t *testing.T) {
	ms := newMockWorkflowStore()
	ms.instances["wf-1"] = &models.WorkflowInstance{WorkflowID: "wf-1", UserID: "test-user-id", Status: "failed"}
	ms.jobs["job-ok"] = &models.Job{JobID: "job-ok", UserID: "test-user-id", Status: "completed", Name: "lint"}
	ms.jobs["job-failed"] = &models.Job{JobID: "job-failed", UserID: "test-user-id", Status: "failed", Name: "build"}
	ms.nodes["wf-1"] = []models.WorkflowNode{
		{NodeID: "node-ok", WorkflowID: "wf-1", Name: "lint", Status: "completed", JobID: strPtr("job-ok")},
		{NodeID: "node-failed", WorkflowID: "wf-1", Name: "build", Status: "failed", JobID: strPtr("job-failed")},
	}

	handler := NewWorkflowHandlerWithCorndogs(ms, corndogs.NewMockClient())
	req := workflowRetryRequest("/retry-unsuccessful", "wf-1", "test-user-id")
	w := httptest.NewRecorder()
	handler.RetryUnsuccessfulJobs(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp RetryUnsuccessfulResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if len(resp.Jobs) != 1 {
		t.Fatalf("expected 1 retried job, got %d: %+v", len(resp.Jobs), resp.Jobs)
	}
	if resp.Jobs[0].Name != "build" {
		t.Errorf("expected the retried job to be 'build', got %q", resp.Jobs[0].Name)
	}
	if resp.Error != "" {
		t.Errorf("expected no aggregated error, got %q", resp.Error)
	}

	// Same workflow instance, no new workflow_id created.
	if _, ok := ms.instances["wf-1"]; !ok {
		t.Fatal("expected the original workflow instance to still exist")
	}
	if len(ms.instances) != 1 {
		t.Errorf("expected retry-unsuccessful to stay in place (no new workflow instance), got %d instances", len(ms.instances))
	}
}
