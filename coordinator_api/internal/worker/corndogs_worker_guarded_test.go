package worker

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/corndogs"
	pb "github.com/catalystcommunity/reactorcide/coordinator_api/internal/corndogs/v1alpha1"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

// guardedMockStore is an in-memory store.Store that also implements
// guardedJobStore and staleCancellingLister, so these tests exercise the
// race-safe paths corndogs_worker.go prefers in production (postgres_store)
// rather than only the best-effort fallback used by the rest of this
// package's MockStore-based tests.
type guardedMockStore struct {
	mu   sync.Mutex
	jobs map[string]*models.Job
}

func newGuardedMockStore(jobs ...*models.Job) *guardedMockStore {
	g := &guardedMockStore{jobs: map[string]*models.Job{}}
	for _, j := range jobs {
		cp := *j
		g.jobs[j.JobID] = &cp
	}
	return g
}

func (g *guardedMockStore) GetJobByID(ctx context.Context, jobID string) (*models.Job, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	j, ok := g.jobs[jobID]
	if !ok {
		return nil, store.ErrNotFound
	}
	cp := *j
	return &cp, nil
}

func (g *guardedMockStore) UpdateJob(ctx context.Context, job *models.Job) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if _, ok := g.jobs[job.JobID]; !ok {
		return store.ErrNotFound
	}
	cp := *job
	g.jobs[job.JobID] = &cp
	return nil
}

func (g *guardedMockStore) UpdateJobStatusGuarded(ctx context.Context, jobID string, fromStatuses []string, apply func(*models.Job)) (*models.Job, bool, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	j, ok := g.jobs[jobID]
	if !ok {
		return nil, false, store.ErrNotFound
	}
	matched := false
	for _, s := range fromStatuses {
		if j.Status == s {
			matched = true
			break
		}
	}
	if !matched {
		return nil, false, nil
	}
	apply(j)
	j.UpdatedAt = time.Now()
	cp := *j
	return &cp, true, nil
}

func (g *guardedMockStore) ListStaleCancellingJobs(ctx context.Context, olderThan time.Time) ([]models.Job, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	var out []models.Job
	for _, j := range g.jobs {
		if j.Status == "cancelling" && j.UpdatedAt.Before(olderThan) {
			out = append(out, *j)
		}
	}
	return out, nil
}

func (g *guardedMockStore) setUpdatedAt(jobID string, t time.Time) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if j, ok := g.jobs[jobID]; ok {
		j.UpdatedAt = t
	}
}

// Remaining store.Store methods: stubs, unused by these tests.
func (g *guardedMockStore) Initialize() (func(), error) { return nil, nil }
func (g *guardedMockStore) CreateProject(ctx context.Context, project *models.Project) error {
	return nil
}
func (g *guardedMockStore) GetProjectByID(ctx context.Context, projectID string) (*models.Project, error) {
	return nil, nil
}
func (g *guardedMockStore) GetProjectByRepoURL(ctx context.Context, repoURL string) (*models.Project, error) {
	return nil, nil
}
func (g *guardedMockStore) UpdateProject(ctx context.Context, project *models.Project) error {
	return nil
}
func (g *guardedMockStore) DeleteProject(ctx context.Context, projectID string) error { return nil }
func (g *guardedMockStore) ListProjects(ctx context.Context, limit, offset int) ([]models.Project, error) {
	return nil, nil
}
func (g *guardedMockStore) GetJobsByUser(ctx context.Context, userID string, limit, offset int) ([]models.Job, error) {
	return nil, nil
}
func (g *guardedMockStore) CreateJob(ctx context.Context, job *models.Job) error { return nil }
func (g *guardedMockStore) DeleteJob(ctx context.Context, jobID string) error    { return nil }
func (g *guardedMockStore) ListJobs(ctx context.Context, filters map[string]interface{}, limit, offset int) ([]models.Job, error) {
	return nil, nil
}
func (g *guardedMockStore) ListJobsForPRCommit(ctx context.Context, repo string, prNumber int, commitSHA string) ([]models.Job, error) {
	return nil, nil
}
func (g *guardedMockStore) ListJobsForPR(ctx context.Context, repo string, prNumber int) ([]models.Job, error) {
	return nil, nil
}
func (g *guardedMockStore) ForPRCommit(ctx context.Context, repo string, prNumber int, commitSHA string, fn func(ctx context.Context) error) error {
	return fn(ctx)
}
func (g *guardedMockStore) IsPRMerged(ctx context.Context, repo string, prNumber int) (bool, error) {
	return false, nil
}
func (g *guardedMockStore) MarkPRMerged(ctx context.Context, repo string, prNumber int) error {
	return nil
}
func (g *guardedMockStore) ValidateAPIToken(ctx context.Context, token string) (*models.APIToken, *models.User, error) {
	return nil, nil, nil
}
func (g *guardedMockStore) CreateAPIToken(ctx context.Context, apiToken *models.APIToken) error {
	return nil
}
func (g *guardedMockStore) UpdateTokenLastUsed(ctx context.Context, tokenID string, lastUsed time.Time) error {
	return nil
}
func (g *guardedMockStore) GetAPITokensByUser(ctx context.Context, userID string) ([]models.APIToken, error) {
	return nil, nil
}
func (g *guardedMockStore) DeleteAPIToken(ctx context.Context, tokenID string) error { return nil }
func (g *guardedMockStore) GetUserByID(ctx context.Context, userID string) (*models.User, error) {
	return nil, nil
}
func (g *guardedMockStore) CreateUser(ctx context.Context, user *models.User) error { return nil }
func (g *guardedMockStore) EnsureDefaultUser() error                                { return nil }

var _ store.Store = (*guardedMockStore)(nil)
var _ guardedJobStore = (*guardedMockStore)(nil)
var _ staleCancellingLister = (*guardedMockStore)(nil)

// TestCornDogsWorker_ClaimPath_CancellingJob_FinalizesWithoutExecuting
// covers Finding 1c: a job that's already "cancelling" by the time this
// worker claims its Corndogs task (jobcontrol.transitionJob lost its
// pre-claim CancelTask race) must be finalized to "cancelled" directly,
// never handed to the processor.
func TestCornDogsWorker_ClaimPath_CancellingJob_FinalizesWithoutExecuting(t *testing.T) {
	taskID := "corndogs-task-1"
	job := &models.Job{JobID: "claim-cancel-job", Status: "cancelling", CancelMode: "cancel", CorndogsTaskID: &taskID}
	st := newGuardedMockStore(job)
	mockCorndogs := corndogs.NewMockClient()
	mockProcessor := &MockJobProcessor{}

	taskPayload := &corndogs.TaskPayload{JobID: job.JobID, JobType: "run"}
	payloadBytes, _ := json.Marshal(taskPayload)
	mockCorndogs.GetNextTaskFunc = func(ctx context.Context, state string, timeout int64) (*pb.Task, error) {
		return &pb.Task{Uuid: "task-id", CurrentState: "submitted-working", Payload: payloadBytes}, nil
	}

	config := &Config{QueueName: "test-queue", PollInterval: 100 * time.Millisecond, Concurrency: 1, Store: st}
	w := NewCornDogsWorkerWithProcessor(config, mockCorndogs, mockProcessor, nil, nil)

	w.processNextTask(context.Background(), 0)

	if len(mockProcessor.ProcessJobCalls) != 0 {
		t.Errorf("expected ProcessJobWithContext to be skipped, got %d calls", len(mockProcessor.ProcessJobCalls))
	}
	stored, err := st.GetJobByID(context.Background(), job.JobID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if stored.Status != "cancelled" {
		t.Errorf("expected status 'cancelled', got %q", stored.Status)
	}
	if stored.LastError != "cancelled" {
		t.Errorf("expected last_error 'cancelled', got %q", stored.LastError)
	}
	if mockCorndogs.GetCancelTaskCallCount() != 1 {
		t.Errorf("expected 1 CancelTask call, got %d", mockCorndogs.GetCancelTaskCallCount())
	}
}

// TestCornDogsWorker_ClaimPath_KillRequested_SetsKilledLastError verifies
// the claim-path finalize honors cancel_mode="kill" the same way the
// cancel-poll does.
func TestCornDogsWorker_ClaimPath_KillRequested_SetsKilledLastError(t *testing.T) {
	job := &models.Job{JobID: "claim-kill-job", Status: "cancelling", CancelMode: "kill"}
	st := newGuardedMockStore(job)
	mockCorndogs := corndogs.NewMockClient()
	mockProcessor := &MockJobProcessor{}

	taskPayload := &corndogs.TaskPayload{JobID: job.JobID, JobType: "run"}
	payloadBytes, _ := json.Marshal(taskPayload)
	mockCorndogs.GetNextTaskFunc = func(ctx context.Context, state string, timeout int64) (*pb.Task, error) {
		return &pb.Task{Uuid: "task-id", CurrentState: "submitted-working", Payload: payloadBytes}, nil
	}

	config := &Config{QueueName: "test-queue", PollInterval: 100 * time.Millisecond, Concurrency: 1, Store: st}
	w := NewCornDogsWorkerWithProcessor(config, mockCorndogs, mockProcessor, nil, nil)

	w.processNextTask(context.Background(), 0)

	stored, err := st.GetJobByID(context.Background(), job.JobID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if stored.Status != "cancelled" {
		t.Errorf("expected status 'cancelled', got %q", stored.Status)
	}
	if stored.LastError != "killed by admin" {
		t.Errorf("expected last_error 'killed by admin', got %q", stored.LastError)
	}
}

// TestCornDogsWorker_TerminalWrite_Guarded_DoesNotClobberFinalizedJob
// covers Finding 1d: if the job row is no longer "running"/"cancelling" by
// the time the worker tries to land its terminal status (e.g. the
// cancelling-job reaper finalized it out from under a worker that hung),
// the guarded terminal write must not overwrite that status.
func TestCornDogsWorker_TerminalWrite_Guarded_DoesNotClobberFinalizedJob(t *testing.T) {
	job := &models.Job{JobID: "terminal-race-job", Status: "submitted", JobCommand: "echo hi"}
	st := newGuardedMockStore(job)
	mockCorndogs := corndogs.NewMockClient()
	mockProcessor := &MockJobProcessor{}

	taskPayload := &corndogs.TaskPayload{JobID: job.JobID, JobType: "run"}
	payloadBytes, _ := json.Marshal(taskPayload)
	mockCorndogs.GetNextTaskFunc = func(ctx context.Context, state string, timeout int64) (*pb.Task, error) {
		return &pb.Task{Uuid: "task-id", CurrentState: "submitted-working", Payload: payloadBytes}, nil
	}

	mockProcessor.ProcessJobFunc = func(ctx context.Context, j *models.Job) *JobResult {
		// Simulate the reaper (or another worker after a crash/restart)
		// finalizing this job out from under the in-flight execution.
		if _, _, err := st.UpdateJobStatusGuarded(ctx, job.JobID, []string{"running"}, func(row *models.Job) {
			row.Status = "cancelled"
			row.LastError = "cancelled: no active worker (reaped)"
		}); err != nil {
			t.Fatalf("unexpected error simulating concurrent finalize: %v", err)
		}
		return &JobResult{ExitCode: 0}
	}

	config := &Config{QueueName: "test-queue", PollInterval: 100 * time.Millisecond, Concurrency: 1, Store: st}
	w := NewCornDogsWorkerWithProcessor(config, mockCorndogs, mockProcessor, nil, nil)

	w.processNextTask(context.Background(), 0)

	stored, err := st.GetJobByID(context.Background(), job.JobID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if stored.Status != "cancelled" {
		t.Errorf("expected the terminal write to leave the reaper's status alone, got %q", stored.Status)
	}
	if stored.LastError != "cancelled: no active worker (reaped)" {
		t.Errorf("expected the reaper's last_error to survive, got %q", stored.LastError)
	}
}

// TestCornDogsWorker_Reaper_FinalizesStaleCancellingJob covers Finding 2b: a
// "cancelling" job whose updated_at is older than the reap threshold (no
// live worker could still legitimately be mid-cancel on it) gets finalized.
func TestCornDogsWorker_Reaper_FinalizesStaleCancellingJob(t *testing.T) {
	taskID := "corndogs-task-1"
	job := &models.Job{JobID: "stale-job", Status: "cancelling", CancelMode: "kill", CorndogsTaskID: &taskID}
	st := newGuardedMockStore(job)
	st.setUpdatedAt(job.JobID, time.Now().Add(-10*time.Minute))

	mockCorndogs := corndogs.NewMockClient()
	config := &Config{QueueName: "test-queue", Store: st, CancelGrace: time.Second, HeartbeatInterval: time.Second}
	w := NewCornDogsWorkerWithProcessor(config, mockCorndogs, &MockJobProcessor{}, nil, nil)

	w.reapStaleCancellingJobs(context.Background())

	stored, err := st.GetJobByID(context.Background(), job.JobID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if stored.Status != "cancelled" {
		t.Errorf("expected reaped job status 'cancelled', got %q", stored.Status)
	}
	if stored.LastError != "cancelled: no active worker (reaped)" {
		t.Errorf("unexpected last_error: %q", stored.LastError)
	}
	if mockCorndogs.GetCancelTaskCallCount() != 1 {
		t.Errorf("expected 1 best-effort CancelTask call, got %d", mockCorndogs.GetCancelTaskCallCount())
	}
}

// TestCornDogsWorker_Reaper_LeavesFreshCancellingJobAlone verifies the
// reaper doesn't touch a "cancelling" job that's still within a live
// worker's legitimate cancel window.
func TestCornDogsWorker_Reaper_LeavesFreshCancellingJobAlone(t *testing.T) {
	job := &models.Job{JobID: "fresh-job", Status: "cancelling", CancelMode: "cancel"}
	st := newGuardedMockStore(job)
	st.setUpdatedAt(job.JobID, time.Now())

	mockCorndogs := corndogs.NewMockClient()
	config := &Config{QueueName: "test-queue", Store: st, CancelGrace: time.Second, HeartbeatInterval: time.Second}
	w := NewCornDogsWorkerWithProcessor(config, mockCorndogs, &MockJobProcessor{}, nil, nil)

	w.reapStaleCancellingJobs(context.Background())

	stored, err := st.GetJobByID(context.Background(), job.JobID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if stored.Status != "cancelling" {
		t.Errorf("expected fresh cancelling job to be left alone, got %q", stored.Status)
	}
	if mockCorndogs.GetCancelTaskCallCount() != 0 {
		t.Errorf("expected no CancelTask call for a job the reaper left alone, got %d", mockCorndogs.GetCancelTaskCallCount())
	}
}
