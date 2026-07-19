package jobcontrol

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/corndogs"
	pb "github.com/catalystcommunity/reactorcide/coordinator_api/internal/corndogs/v1alpha1"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

// retryMockStore is an in-memory store.Store implementing every method
// retry.go's RetryJob/RetryWorkflow/RetryUnsuccessfulJobs need — including
// the full set worker.TriggerProcessor.EvaluateWorkflow requires (its
// internal type assertion against the unexported workflowStore interface is
// structural, so this fake just needs the matching method set, not an
// explicit "implements" declaration). Deliberately a real in-memory fake
// rather than a Func-per-call mock, same rationale as jobControlMockStore in
// jobcontrol_test.go: RetryWorkflow reads back its own writes (list nodes,
// evaluate readiness, submit) so it needs actual state to operate against.
type retryMockStore struct {
	jobs       map[string]*models.Job
	workflows  map[string]*models.WorkflowInstance
	nodes      map[string]*models.WorkflowNode
	nodeOrder  map[string][]string
	events     []models.WorkflowEvent
	nextWfID   int
	nextNodeID int
	nextJobID  int
}

func newRetryMockStore() *retryMockStore {
	return &retryMockStore{
		jobs:      map[string]*models.Job{},
		workflows: map[string]*models.WorkflowInstance{},
		nodes:     map[string]*models.WorkflowNode{},
		nodeOrder: map[string][]string{},
	}
}

func (m *retryMockStore) addJob(job *models.Job) *models.Job {
	cp := *job
	m.jobs[job.JobID] = &cp
	return &cp
}

func (m *retryMockStore) addWorkflow(wf *models.WorkflowInstance) *models.WorkflowInstance {
	cp := *wf
	m.workflows[wf.WorkflowID] = &cp
	return &cp
}

func (m *retryMockStore) addNode(node *models.WorkflowNode) *models.WorkflowNode {
	cp := *node
	m.nodes[node.NodeID] = &cp
	m.nodeOrder[node.WorkflowID] = append(m.nodeOrder[node.WorkflowID], node.NodeID)
	return &cp
}

// --- Job operations ---

func (m *retryMockStore) GetJobByID(ctx context.Context, jobID string) (*models.Job, error) {
	j, ok := m.jobs[jobID]
	if !ok {
		return nil, store.ErrNotFound
	}
	cp := *j
	return &cp, nil
}

func (m *retryMockStore) CreateJob(ctx context.Context, job *models.Job) error {
	if job.JobID == "" {
		m.nextJobID++
		job.JobID = fmt.Sprintf("job-%d", m.nextJobID)
	}
	cp := *job
	m.jobs[job.JobID] = &cp
	return nil
}

func (m *retryMockStore) UpdateJob(ctx context.Context, job *models.Job) error {
	if _, ok := m.jobs[job.JobID]; !ok {
		return store.ErrNotFound
	}
	cp := *job
	m.jobs[job.JobID] = &cp
	return nil
}

func (m *retryMockStore) DeleteJob(ctx context.Context, jobID string) error {
	delete(m.jobs, jobID)
	return nil
}

func (m *retryMockStore) ListJobs(ctx context.Context, filters map[string]interface{}, limit, offset int) ([]models.Job, error) {
	return nil, nil
}

// --- Workflow operations ---

func (m *retryMockStore) CreateWorkflowInstance(ctx context.Context, wf *models.WorkflowInstance) error {
	if wf.WorkflowID == "" {
		m.nextWfID++
		wf.WorkflowID = fmt.Sprintf("wf-%d", m.nextWfID)
	}
	cp := *wf
	m.workflows[wf.WorkflowID] = &cp
	return nil
}

func (m *retryMockStore) GetWorkflowInstance(ctx context.Context, workflowID string) (*models.WorkflowInstance, error) {
	wf, ok := m.workflows[workflowID]
	if !ok {
		return nil, store.ErrNotFound
	}
	cp := *wf
	return &cp, nil
}

func (m *retryMockStore) UpdateWorkflowInstance(ctx context.Context, wf *models.WorkflowInstance) error {
	if _, ok := m.workflows[wf.WorkflowID]; !ok {
		return store.ErrNotFound
	}
	cp := *wf
	m.workflows[wf.WorkflowID] = &cp
	return nil
}

func (m *retryMockStore) CreateWorkflowNode(ctx context.Context, node *models.WorkflowNode) error {
	if node.NodeID == "" {
		m.nextNodeID++
		node.NodeID = fmt.Sprintf("node-%d", m.nextNodeID)
	}
	cp := *node
	m.nodes[node.NodeID] = &cp
	m.nodeOrder[node.WorkflowID] = append(m.nodeOrder[node.WorkflowID], node.NodeID)
	return nil
}

func (m *retryMockStore) UpdateWorkflowNode(ctx context.Context, node *models.WorkflowNode) error {
	if _, ok := m.nodes[node.NodeID]; !ok {
		return store.ErrNotFound
	}
	cp := *node
	m.nodes[node.NodeID] = &cp
	return nil
}

func (m *retryMockStore) ListWorkflowNodes(ctx context.Context, workflowID string) ([]models.WorkflowNode, error) {
	var out []models.WorkflowNode
	for _, id := range m.nodeOrder[workflowID] {
		out = append(out, *m.nodes[id])
	}
	return out, nil
}

func (m *retryMockStore) GetWorkflowNodeByJobID(ctx context.Context, jobID string) (*models.WorkflowNode, error) {
	for _, n := range m.nodes {
		if n.JobID != nil && *n.JobID == jobID {
			cp := *n
			return &cp, nil
		}
	}
	return nil, store.ErrNotFound
}

func (m *retryMockStore) GetWorkflowVars(ctx context.Context, workflowID string) (map[string]models.JSONB, error) {
	return map[string]models.JSONB{}, nil
}

func (m *retryMockStore) UpsertWorkflowVar(ctx context.Context, v *models.WorkflowVar) error {
	return nil
}

func (m *retryMockStore) CreateWorkflowEvent(ctx context.Context, event *models.WorkflowEvent) error {
	m.events = append(m.events, *event)
	return nil
}

func (m *retryMockStore) ListWorkflowEvents(ctx context.Context, workflowID string, limit, offset int) ([]models.WorkflowEvent, error) {
	return m.events, nil
}

// --- Remaining store.Store methods: stubs, unused by retry.go ---

func (m *retryMockStore) Initialize() (func(), error) { return nil, nil }
func (m *retryMockStore) CreateProject(ctx context.Context, project *models.Project) error {
	return nil
}
func (m *retryMockStore) GetProjectByID(ctx context.Context, projectID string) (*models.Project, error) {
	return nil, nil
}
func (m *retryMockStore) GetProjectByRepoURL(ctx context.Context, repoURL string) (*models.Project, error) {
	return nil, nil
}
func (m *retryMockStore) UpdateProject(ctx context.Context, project *models.Project) error {
	return nil
}
func (m *retryMockStore) DeleteProject(ctx context.Context, projectID string) error { return nil }
func (m *retryMockStore) ListProjects(ctx context.Context, limit, offset int) ([]models.Project, error) {
	return nil, nil
}
func (m *retryMockStore) GetJobsByUser(ctx context.Context, userID string, limit, offset int) ([]models.Job, error) {
	return nil, nil
}
func (m *retryMockStore) ListJobsForPRCommit(ctx context.Context, repo string, prNumber int, commitSHA string) ([]models.Job, error) {
	return nil, nil
}
func (m *retryMockStore) ListJobsForPR(ctx context.Context, repo string, prNumber int) ([]models.Job, error) {
	return nil, nil
}
func (m *retryMockStore) ForPRCommit(ctx context.Context, repo string, prNumber int, commitSHA string, fn func(ctx context.Context) error) error {
	return fn(ctx)
}
func (m *retryMockStore) IsPRMerged(ctx context.Context, repo string, prNumber int) (bool, error) {
	return false, nil
}
func (m *retryMockStore) MarkPRMerged(ctx context.Context, repo string, prNumber int) error {
	return nil
}
func (m *retryMockStore) ValidateAPIToken(ctx context.Context, token string) (*models.APIToken, *models.User, error) {
	return nil, nil, nil
}
func (m *retryMockStore) CreateAPIToken(ctx context.Context, apiToken *models.APIToken) error {
	return nil
}
func (m *retryMockStore) UpdateTokenLastUsed(ctx context.Context, tokenID string, lastUsed time.Time) error {
	return nil
}
func (m *retryMockStore) GetAPITokensByUser(ctx context.Context, userID string) ([]models.APIToken, error) {
	return nil, nil
}
func (m *retryMockStore) DeleteAPIToken(ctx context.Context, tokenID string) error { return nil }
func (m *retryMockStore) GetUserByID(ctx context.Context, userID string) (*models.User, error) {
	return nil, nil
}
func (m *retryMockStore) CreateUser(ctx context.Context, user *models.User) error { return nil }
func (m *retryMockStore) EnsureDefaultUser() error                                { return nil }

var _ store.Store = (*retryMockStore)(nil)
var _ workflowRetryStore = (*retryMockStore)(nil)

func strPtr(s string) *string { return &s }
func intPtr(i int) *int       { return &i }

// ===== RetryJob =====

// TestRetryJob_StatusMatrix verifies the retryable-status rule end to end
// through RetryJob (not just models.Job.IsRetryable in isolation): every
// non-terminal or successfully-terminal status is refused, and every
// terminal-but-unsuccessful status ("failed"/"cancelled"/"timeout")
// succeeds.
func TestRetryJob_StatusMatrix(t *testing.T) {
	statuses := []struct {
		status      string
		wantAllowed bool
	}{
		{"submitted", false},
		{"queued", false},
		{"running", false},
		{"cancelling", false},
		{"completed", false},
		{"timeout", true},
		{"failed", true},
		{"cancelled", true},
	}

	for _, tt := range statuses {
		t.Run(tt.status, func(t *testing.T) {
			st := newRetryMockStore()
			job := st.addJob(&models.Job{JobID: "orig-job", Status: tt.status, JobCommand: "echo hi"})
			mockCorndogs := corndogs.NewMockClient()

			newJob, err := RetryJob(context.Background(), st, mockCorndogs, job)
			if tt.wantAllowed {
				if err != nil {
					t.Fatalf("expected retry to succeed from status %q, got error: %v", tt.status, err)
				}
				if newJob == nil || newJob.JobID == job.JobID {
					t.Fatalf("expected a distinct new job, got %+v", newJob)
				}
			} else {
				if !errors.Is(err, ErrNotRetryable) {
					t.Fatalf("expected ErrNotRetryable from status %q, got %v", tt.status, err)
				}
			}
		})
	}
}

// TestRetryJob_ClonesSpecFields verifies the new job carries forward every
// spec field the feature spec calls out, while execution fields are zeroed
// and lineage fields (ParentJobID, RetryCount, Status) are set correctly.
func TestRetryJob_ClonesSpecFields(t *testing.T) {
	st := newRetryMockStore()
	exitCode := 1
	startedAt := time.Now().Add(-time.Hour)
	completedAt := time.Now()
	original := &models.Job{
		JobID:              "job-orig",
		UserID:             "user-1",
		ProjectID:          strPtr("proj-1"),
		Name:               "build",
		Description:        "builds the thing",
		JobFile:            "jobs/build.yaml",
		Notes:              `{"vcs_provider":"github"}`,
		SourceURL:          strPtr("https://example.com/repo.git"),
		SourceRef:          strPtr("refs/heads/main"),
		SourceType:         sourceTypePtr("git"),
		SourcePath:         strPtr("subdir"),
		CISourceType:       sourceTypePtr("git"),
		CISourceURL:        strPtr("https://example.com/ci.git"),
		CISourceRef:        strPtr("main"),
		ContainerImage:     strPtr("quay.io/example/image:latest"),
		CodeDir:            "/job/src",
		JobDir:             "/job/src/subdir",
		JobCommand:         "make test",
		RunnerImage:        "quay.io/catalystcommunity/reactorcide_runner",
		JobEnvVars:         models.JSONB{"FOO": "bar"},
		JobEnvFile:         ".env",
		TimeoutSeconds:     1800,
		Priority:           5,
		Capabilities:       []string{"docker"},
		RunAsUser:          "runner",
		QueueName:          "reactorcide-jobs",
		AutoTargetState:    "running",
		Status:             "failed",
		CorndogsTaskID:     strPtr("old-task-id"),
		StartedAt:          &startedAt,
		CompletedAt:        &completedAt,
		ExitCode:           &exitCode,
		WorkerID:           strPtr("worker-1"),
		LastError:          "exit code 1",
		CancelMode:         "",
		LogsObjectKey:      "logs/job-orig",
		ArtifactsObjectKey: "artifacts/job-orig",
		EventMetadata:      models.JSONB{"event": "push"},
		RetryCount:         2,
		VCSRepo:            strPtr("owner/repo"),
		PRNumber:           intPtr(42),
		CommitSHA:          strPtr("abc123"),
	}
	st.addJob(original)
	mockCorndogs := corndogs.NewMockClient()

	newJob, err := RetryJob(context.Background(), st, mockCorndogs, original)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Spec fields carried forward.
	checks := []struct {
		name string
		got  interface{}
		want interface{}
	}{
		{"UserID", newJob.UserID, original.UserID},
		{"ProjectID", derefStr(newJob.ProjectID), derefStr(original.ProjectID)},
		{"Name", newJob.Name, original.Name},
		{"Description", newJob.Description, original.Description},
		{"JobFile", newJob.JobFile, original.JobFile},
		{"SourceURL", derefStr(newJob.SourceURL), derefStr(original.SourceURL)},
		{"SourceRef", derefStr(newJob.SourceRef), derefStr(original.SourceRef)},
		{"SourcePath", derefStr(newJob.SourcePath), derefStr(original.SourcePath)},
		{"CISourceURL", derefStr(newJob.CISourceURL), derefStr(original.CISourceURL)},
		{"CISourceRef", derefStr(newJob.CISourceRef), derefStr(original.CISourceRef)},
		{"ContainerImage", derefStr(newJob.ContainerImage), derefStr(original.ContainerImage)},
		{"CodeDir", newJob.CodeDir, original.CodeDir},
		{"JobDir", newJob.JobDir, original.JobDir},
		{"JobCommand", newJob.JobCommand, original.JobCommand},
		{"RunnerImage", newJob.RunnerImage, original.RunnerImage},
		{"JobEnvFile", newJob.JobEnvFile, original.JobEnvFile},
		{"TimeoutSeconds", newJob.TimeoutSeconds, original.TimeoutSeconds},
		{"Priority", newJob.Priority, original.Priority},
		{"RunAsUser", newJob.RunAsUser, original.RunAsUser},
		{"QueueName", newJob.QueueName, original.QueueName},
		{"AutoTargetState", newJob.AutoTargetState, original.AutoTargetState},
		{"VCSRepo", derefStr(newJob.VCSRepo), derefStr(original.VCSRepo)},
		{"CommitSHA", derefStr(newJob.CommitSHA), derefStr(original.CommitSHA)},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %v, want %v", c.name, c.got, c.want)
		}
	}
	if newJob.JobEnvVars["FOO"] != "bar" {
		t.Errorf("expected JobEnvVars to carry FOO=bar, got %+v", newJob.JobEnvVars)
	}
	if len(newJob.Capabilities) != 1 || newJob.Capabilities[0] != "docker" {
		t.Errorf("expected Capabilities [docker], got %+v", newJob.Capabilities)
	}
	if newJob.PRNumber == nil || *newJob.PRNumber != 42 {
		t.Errorf("expected PRNumber 42, got %v", newJob.PRNumber)
	}

	// Lineage fields.
	if newJob.JobID == original.JobID {
		t.Error("expected a distinct new JobID")
	}
	if newJob.ParentJobID == nil || *newJob.ParentJobID != original.JobID {
		t.Errorf("expected ParentJobID to point at original job %s, got %v", original.JobID, newJob.ParentJobID)
	}
	if newJob.RetryCount != original.RetryCount+1 {
		t.Errorf("expected RetryCount %d, got %d", original.RetryCount+1, newJob.RetryCount)
	}
	if newJob.Status == "failed" {
		t.Error("expected new job's status not to still be 'failed' after successful submission")
	}

	// Execution fields zeroed.
	if newJob.StartedAt != nil {
		t.Errorf("expected StartedAt zeroed, got %v", newJob.StartedAt)
	}
	if newJob.CompletedAt != nil {
		t.Errorf("expected CompletedAt zeroed, got %v", newJob.CompletedAt)
	}
	if newJob.ExitCode != nil {
		t.Errorf("expected ExitCode zeroed, got %v", *newJob.ExitCode)
	}
	if newJob.WorkerID != nil {
		t.Errorf("expected WorkerID zeroed, got %v", *newJob.WorkerID)
	}
	if newJob.LastError != "" {
		t.Errorf("expected LastError zeroed, got %q", newJob.LastError)
	}
	if newJob.CancelMode != "" {
		t.Errorf("expected CancelMode zeroed, got %q", newJob.CancelMode)
	}
	if newJob.LogsObjectKey != "" {
		t.Errorf("expected LogsObjectKey zeroed, got %q", newJob.LogsObjectKey)
	}
	if newJob.ArtifactsObjectKey != "" {
		t.Errorf("expected ArtifactsObjectKey zeroed, got %q", newJob.ArtifactsObjectKey)
	}
	if newJob.CorndogsTaskID == nil || *newJob.CorndogsTaskID == "old-task-id" {
		t.Errorf("expected a fresh CorndogsTaskID from the new Corndogs submission, got %v", newJob.CorndogsTaskID)
	}

	// Corndogs was actually invoked with the new job's payload.
	if mockCorndogs.GetSubmitTaskCallCount() != 1 {
		t.Errorf("expected 1 SubmitTask call, got %d", mockCorndogs.GetSubmitTaskCallCount())
	}
}

// TestRetryJob_NodeRebindAndWorkflowRunning verifies that retrying a job
// that belongs to a workflow node rebinds the node to the new job and
// forces the workflow instance status back to "running", per the feature
// spec's "same workflow, same node" requirement.
func TestRetryJob_NodeRebindAndWorkflowRunning(t *testing.T) {
	st := newRetryMockStore()
	completedAt := time.Now()
	st.addWorkflow(&models.WorkflowInstance{WorkflowID: "wf-1", UserID: "user-1", Status: "failed", CompletedAt: &completedAt})
	node := st.addNode(&models.WorkflowNode{
		NodeID:      "node-1",
		WorkflowID:  "wf-1",
		Name:        "build",
		DisplayName: "build",
		Status:      "failed",
		JobID:       strPtr("orig-job"),
		CompletedAt: &completedAt,
	})
	job := st.addJob(&models.Job{
		JobID:          "orig-job",
		UserID:         "user-1",
		Status:         "failed",
		WorkflowID:     &node.WorkflowID,
		WorkflowNodeID: &node.NodeID,
		JobCommand:     "make test",
	})
	mockCorndogs := corndogs.NewMockClient()

	newJob, err := RetryJob(context.Background(), st, mockCorndogs, job)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	updatedNode, err := st.GetWorkflowNodeByJobID(context.Background(), newJob.JobID)
	if err != nil {
		t.Fatalf("expected the workflow node to be rebound to the new job: %v", err)
	}
	if updatedNode.NodeID != "node-1" {
		t.Errorf("expected the same node (node-1) to be rebound, got %s", updatedNode.NodeID)
	}
	if updatedNode.Status != "submitted" {
		t.Errorf("expected node status 'submitted' after rebind, got %q", updatedNode.Status)
	}
	if updatedNode.CompletedAt != nil {
		t.Errorf("expected node CompletedAt cleared after rebind, got %v", updatedNode.CompletedAt)
	}

	// The old node's original JobID should no longer resolve to it (only the
	// new job does) — GetWorkflowNodeByJobID(oldJobID) should now fail.
	if _, err := st.GetWorkflowNodeByJobID(context.Background(), "orig-job"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("expected the old job id to no longer resolve to any node, got err=%v", err)
	}

	wf, err := st.GetWorkflowInstance(context.Background(), "wf-1")
	if err != nil {
		t.Fatalf("unexpected error reloading workflow: %v", err)
	}
	if wf.Status != "running" {
		t.Errorf("expected workflow instance status 'running' after node retry, got %q", wf.Status)
	}
	if wf.CompletedAt != nil {
		t.Errorf("expected workflow instance CompletedAt cleared, got %v", wf.CompletedAt)
	}
}

// TestRetryJob_NoWorkflow verifies a loose (non-workflow) job retries
// cleanly without touching any workflow bookkeeping.
func TestRetryJob_NoWorkflow(t *testing.T) {
	st := newRetryMockStore()
	job := st.addJob(&models.Job{JobID: "orig-job", UserID: "user-1", Status: "cancelled", JobCommand: "echo hi"})
	mockCorndogs := corndogs.NewMockClient()

	newJob, err := RetryJob(context.Background(), st, mockCorndogs, job)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if newJob.WorkflowID != nil || newJob.WorkflowNodeID != nil {
		t.Errorf("expected no workflow linkage on a loose job retry, got %+v", newJob)
	}
}

// TestRetryJob_NilJob verifies a nil job is refused rather than panicking.
func TestRetryJob_NilJob(t *testing.T) {
	st := newRetryMockStore()
	mockCorndogs := corndogs.NewMockClient()
	if _, err := RetryJob(context.Background(), st, mockCorndogs, nil); !errors.Is(err, ErrNotRetryable) {
		t.Errorf("expected ErrNotRetryable for nil job, got %v", err)
	}
}

// TestRetryJob_CorndogsFailure_MarksNewJobFailed verifies a Corndogs
// submission failure on the retried job is recorded on the NEW job (not
// silently dropped), same as trigger_processor.go's createAndSubmitJob.
func TestRetryJob_CorndogsFailure_MarksNewJobFailed(t *testing.T) {
	st := newRetryMockStore()
	job := st.addJob(&models.Job{JobID: "orig-job", UserID: "user-1", Status: "failed", JobCommand: "echo hi"})
	mockCorndogs := corndogs.NewMockClient()
	mockCorndogs.SubmitTaskFunc = func(ctx context.Context, payload *corndogs.TaskPayload, priority int64) (*pb.Task, error) {
		return nil, fmt.Errorf("queue unavailable")
	}

	newJob, err := RetryJob(context.Background(), st, mockCorndogs, job)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if newJob.Status != "failed" {
		t.Errorf("expected new job status 'failed' after Corndogs submission error, got %q", newJob.Status)
	}
	if !strings.Contains(newJob.LastError, "queue unavailable") {
		t.Errorf("expected LastError to mention the Corndogs failure, got %q", newJob.LastError)
	}
}

// ===== RetryWorkflow =====

// newRetryWorkflowFixture builds an old, failed workflow instance with a
// single ready (no-dependency) node whose JobSpec is a real
// trigger-job-spec payload, plus the eval job the workflow's ParentJobID
// points at (submitWorkflowNode/buildJobFromTrigger need to load it to
// inherit queue/runner-image/etc defaults) — the same shape
// ProcessTriggersFromData would have produced.
func newRetryWorkflowFixture(t *testing.T) (*retryMockStore, *models.WorkflowInstance) {
	t.Helper()
	st := newRetryMockStore()
	st.addJob(&models.Job{
		JobID:       "eval-job",
		UserID:      "user-1",
		QueueName:   "reactorcide-jobs",
		RunnerImage: "quay.io/catalystcommunity/reactorcide_runner",
		CodeDir:     "/job/src",
		JobDir:      "/job/src",
	})
	pr := 7
	old := st.addWorkflow(&models.WorkflowInstance{
		WorkflowID:    "wf-old",
		UserID:        "user-1",
		ProjectID:     strPtr("proj-1"),
		ParentJobID:   strPtr("eval-job"),
		Name:          "CI",
		Status:        "failed",
		QueueName:     "reactorcide-jobs",
		VCSProvider:   "github",
		VCSRepo:       "owner/repo",
		PRNumber:      &pr,
		CommitSHA:     "abc123",
		StatusContext: "CI",
		CommentMarker: "<!-- reactorcide:workflows:abc123:pull_request -->",
	})
	st.addNode(&models.WorkflowNode{
		NodeID:      "node-old-1",
		WorkflowID:  old.WorkflowID,
		Name:        "build",
		DisplayName: "build",
		Status:      "failed",
		JobID:       strPtr("job-old-1"),
		JobSpec:     models.JSONB{"job_name": "build", "job_command": "make build"},
	})
	st.addJob(&models.Job{JobID: "job-old-1", UserID: "user-1", Status: "failed", WorkflowID: strPtr(old.WorkflowID), WorkflowNodeID: strPtr("node-old-1")})
	return st, old
}

// TestRetryWorkflow_CreatesFreshInstanceAndSubmits verifies RetryWorkflow
// creates an entirely new WorkflowInstance (distinct WorkflowID) with fresh
// nodes copied from the old instance's definition, drives initial
// submission (the ready node lands on "submitted" with a new JobID), and
// leaves the old instance/nodes/jobs completely untouched.
func TestRetryWorkflow_CreatesFreshInstanceAndSubmits(t *testing.T) {
	st, old := newRetryWorkflowFixture(t)
	mockCorndogs := corndogs.NewMockClient()

	newWf, err := RetryWorkflow(context.Background(), st, mockCorndogs, old.WorkflowID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if newWf.WorkflowID == old.WorkflowID {
		t.Fatal("expected a distinct new workflow ID")
	}
	if newWf.Name != old.Name || newWf.VCSRepo != old.VCSRepo || newWf.CommitSHA != old.CommitSHA {
		t.Errorf("expected definition fields copied, got %+v", newWf)
	}
	if newWf.CommentMarker != old.CommentMarker {
		t.Errorf("expected comment marker copied verbatim, got %q want %q", newWf.CommentMarker, old.CommentMarker)
	}
	if newWf.Status != "running" {
		t.Errorf("expected new workflow status 'running' after initial submission, got %q", newWf.Status)
	}

	newNodes, err := st.ListWorkflowNodes(context.Background(), newWf.WorkflowID)
	if err != nil {
		t.Fatalf("unexpected error listing new nodes: %v", err)
	}
	if len(newNodes) != 1 {
		t.Fatalf("expected 1 new node, got %d", len(newNodes))
	}
	if newNodes[0].NodeID == "node-old-1" {
		t.Error("expected a distinct new node ID")
	}
	if newNodes[0].Status != "submitted" {
		t.Errorf("expected new node status 'submitted' (ready, no deps), got %q", newNodes[0].Status)
	}
	if newNodes[0].JobID == nil || *newNodes[0].JobID == "job-old-1" {
		t.Errorf("expected a fresh job bound to the new node, got %v", newNodes[0].JobID)
	}

	if mockCorndogs.GetSubmitTaskCallCount() != 1 {
		t.Errorf("expected 1 SubmitTask call, got %d", mockCorndogs.GetSubmitTaskCallCount())
	}

	// Old instance/nodes/jobs untouched.
	reloadedOld, err := st.GetWorkflowInstance(context.Background(), old.WorkflowID)
	if err != nil {
		t.Fatalf("unexpected error reloading old workflow: %v", err)
	}
	if reloadedOld.Status != "failed" {
		t.Errorf("expected old workflow instance to remain 'failed', got %q", reloadedOld.Status)
	}
	oldNodes, err := st.ListWorkflowNodes(context.Background(), old.WorkflowID)
	if err != nil {
		t.Fatalf("unexpected error listing old nodes: %v", err)
	}
	if len(oldNodes) != 1 || oldNodes[0].Status != "failed" || derefStr(oldNodes[0].JobID) != "job-old-1" {
		t.Errorf("expected old node untouched, got %+v", oldNodes)
	}
}

// TestRetryWorkflow_NotRetryable_Refused verifies a workflow instance not
// in "failed"/"cancelled" cannot be retried.
func TestRetryWorkflow_NotRetryable_Refused(t *testing.T) {
	st, old := newRetryWorkflowFixture(t)
	old.Status = "running"
	st.addWorkflow(old)

	mockCorndogs := corndogs.NewMockClient()
	if _, err := RetryWorkflow(context.Background(), st, mockCorndogs, old.WorkflowID); !errors.Is(err, ErrWorkflowNotRetryable) {
		t.Errorf("expected ErrWorkflowNotRetryable, got %v", err)
	}
}

// TestRetryWorkflow_NotFound verifies a missing workflow ID surfaces
// store.ErrNotFound rather than panicking.
func TestRetryWorkflow_NotFound(t *testing.T) {
	st := newRetryMockStore()
	mockCorndogs := corndogs.NewMockClient()
	if _, err := RetryWorkflow(context.Background(), st, mockCorndogs, "does-not-exist"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("expected store.ErrNotFound, got %v", err)
	}
}

// ===== RetryUnsuccessfulJobs =====

// retryFailureStore wraps retryMockStore to simulate a hard failure
// (distinct from "not retryable", which RetryJob refuses cleanly) creating
// one specific job by name, so RetryUnsuccessfulJobs' continue-past-
// individual-failures behavior has something real to exercise.
type retryFailureStore struct {
	*retryMockStore
	failCreateForName string
}

func (m *retryFailureStore) CreateJob(ctx context.Context, job *models.Job) error {
	if job.Name == m.failCreateForName {
		return fmt.Errorf("simulated create failure for %s", job.Name)
	}
	return m.retryMockStore.CreateJob(ctx, job)
}

var _ store.Store = (*retryFailureStore)(nil)

// TestRetryUnsuccessfulJobs_SkipsSuccessfulAndContinuesPastFailures verifies
// the workflow-scoped bulk retry: a completed node's job is left alone, a
// node with no job is skipped, a failing individual retry doesn't abort the
// batch, and every other failed/cancelled node still gets retried.
func TestRetryUnsuccessfulJobs_SkipsSuccessfulAndContinuesPastFailures(t *testing.T) {
	base := newRetryMockStore()
	st := &retryFailureStore{retryMockStore: base, failCreateForName: "flaky"}

	base.addWorkflow(&models.WorkflowInstance{WorkflowID: "wf-1", UserID: "user-1", Status: "failed"})
	base.addJob(&models.Job{JobID: "job-ok", UserID: "user-1", Status: "completed", Name: "lint"})
	base.addJob(&models.Job{JobID: "job-failed", UserID: "user-1", Status: "failed", Name: "build"})
	base.addJob(&models.Job{JobID: "job-cancelled", UserID: "user-1", Status: "cancelled", Name: "test"})
	base.addJob(&models.Job{JobID: "job-flaky", UserID: "user-1", Status: "failed", Name: "flaky"})
	base.addNode(&models.WorkflowNode{NodeID: "node-ok", WorkflowID: "wf-1", Name: "lint", Status: "completed", JobID: strPtr("job-ok")})
	base.addNode(&models.WorkflowNode{NodeID: "node-failed", WorkflowID: "wf-1", Name: "build", Status: "failed", JobID: strPtr("job-failed")})
	base.addNode(&models.WorkflowNode{NodeID: "node-cancelled", WorkflowID: "wf-1", Name: "test", Status: "cancelled", JobID: strPtr("job-cancelled")})
	base.addNode(&models.WorkflowNode{NodeID: "node-flaky", WorkflowID: "wf-1", Name: "flaky", Status: "failed", JobID: strPtr("job-flaky")})
	base.addNode(&models.WorkflowNode{NodeID: "node-no-job", WorkflowID: "wf-1", Name: "never-ran", Status: "pending"})

	mockCorndogs := corndogs.NewMockClient()
	retried, err := RetryUnsuccessfulJobs(context.Background(), st, mockCorndogs, "wf-1")
	if err == nil {
		t.Fatal("expected an aggregated error describing the one simulated failure")
	}
	if !strings.Contains(err.Error(), "1 of") {
		t.Errorf("expected aggregated error to mention 1 failure, got %q", err.Error())
	}
	if len(retried) != 2 {
		t.Fatalf("expected 2 successful retries (build, test), got %d: %+v", len(retried), retried)
	}
	names := map[string]bool{}
	for _, j := range retried {
		names[j.Name] = true
	}
	if !names["build"] || !names["test"] {
		t.Errorf("expected retries for build and test, got %+v", names)
	}
	if names["lint"] {
		t.Error("expected the completed 'lint' job to be skipped, not retried")
	}
	if names["flaky"] {
		t.Error("expected the failing 'flaky' create to not appear among successes")
	}
}

// TestRetryUnsuccessfulJobs_AllSuccessful_NoError verifies the happy path
// returns a nil error when every retryable node retries cleanly.
func TestRetryUnsuccessfulJobs_AllSuccessful_NoError(t *testing.T) {
	st := newRetryMockStore()
	st.addWorkflow(&models.WorkflowInstance{WorkflowID: "wf-1", UserID: "user-1", Status: "cancelled"})
	st.addJob(&models.Job{JobID: "job-failed", UserID: "user-1", Status: "failed", Name: "build"})
	st.addNode(&models.WorkflowNode{NodeID: "node-failed", WorkflowID: "wf-1", Name: "build", Status: "failed", JobID: strPtr("job-failed")})

	mockCorndogs := corndogs.NewMockClient()
	retried, err := RetryUnsuccessfulJobs(context.Background(), st, mockCorndogs, "wf-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(retried) != 1 {
		t.Fatalf("expected 1 retried job, got %d", len(retried))
	}
}

func derefStr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func sourceTypePtr(s string) *models.SourceType {
	st := models.SourceType(s)
	return &st
}
