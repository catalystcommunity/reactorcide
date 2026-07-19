package jobcontrol

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/corndogs"
	pb "github.com/catalystcommunity/reactorcide/coordinator_api/internal/corndogs/v1alpha1"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

// jobControlMockStore is a minimal in-memory store.Store that also
// implements guardedJobStore, so these tests exercise the same race-safe
// transition path production (postgres_store) uses rather than only the
// best-effort fallback. Deliberately real (not a Func-per-call mock) so
// UpdateJobStatusGuarded's fromStatuses check has actual state to race
// against.
type jobControlMockStore struct {
	jobs map[string]*models.Job
}

func newJobControlMockStore(jobs ...*models.Job) *jobControlMockStore {
	m := &jobControlMockStore{jobs: map[string]*models.Job{}}
	for _, j := range jobs {
		cp := *j
		m.jobs[j.JobID] = &cp
	}
	return m
}

func (m *jobControlMockStore) GetJobByID(ctx context.Context, jobID string) (*models.Job, error) {
	j, ok := m.jobs[jobID]
	if !ok {
		return nil, store.ErrNotFound
	}
	cp := *j
	return &cp, nil
}

func (m *jobControlMockStore) UpdateJob(ctx context.Context, job *models.Job) error {
	if _, ok := m.jobs[job.JobID]; !ok {
		return store.ErrNotFound
	}
	cp := *job
	m.jobs[job.JobID] = &cp
	return nil
}

func (m *jobControlMockStore) UpdateJobStatusGuarded(ctx context.Context, jobID string, fromStatuses []string, apply func(*models.Job)) (*models.Job, bool, error) {
	j, ok := m.jobs[jobID]
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

// Remaining store.Store methods: stubs, unused by jobcontrol.
func (m *jobControlMockStore) Initialize() (func(), error) { return nil, nil }
func (m *jobControlMockStore) CreateProject(ctx context.Context, project *models.Project) error {
	return nil
}
func (m *jobControlMockStore) GetProjectByID(ctx context.Context, projectID string) (*models.Project, error) {
	return nil, nil
}
func (m *jobControlMockStore) GetProjectByRepoURL(ctx context.Context, repoURL string) (*models.Project, error) {
	return nil, nil
}
func (m *jobControlMockStore) UpdateProject(ctx context.Context, project *models.Project) error {
	return nil
}
func (m *jobControlMockStore) DeleteProject(ctx context.Context, projectID string) error { return nil }
func (m *jobControlMockStore) ListProjects(ctx context.Context, limit, offset int) ([]models.Project, error) {
	return nil, nil
}
func (m *jobControlMockStore) GetJobsByUser(ctx context.Context, userID string, limit, offset int) ([]models.Job, error) {
	return nil, nil
}
func (m *jobControlMockStore) CreateJob(ctx context.Context, job *models.Job) error { return nil }
func (m *jobControlMockStore) DeleteJob(ctx context.Context, jobID string) error    { return nil }
func (m *jobControlMockStore) ListJobs(ctx context.Context, filters map[string]interface{}, limit, offset int) ([]models.Job, error) {
	return nil, nil
}
func (m *jobControlMockStore) ListJobsForPRCommit(ctx context.Context, repo string, prNumber int, commitSHA string) ([]models.Job, error) {
	return nil, nil
}
func (m *jobControlMockStore) ListJobsForPR(ctx context.Context, repo string, prNumber int) ([]models.Job, error) {
	return nil, nil
}
func (m *jobControlMockStore) ForPRCommit(ctx context.Context, repo string, prNumber int, commitSHA string, fn func(ctx context.Context) error) error {
	return fn(ctx)
}
func (m *jobControlMockStore) IsPRMerged(ctx context.Context, repo string, prNumber int) (bool, error) {
	return false, nil
}
func (m *jobControlMockStore) MarkPRMerged(ctx context.Context, repo string, prNumber int) error {
	return nil
}
func (m *jobControlMockStore) ValidateAPIToken(ctx context.Context, token string) (*models.APIToken, *models.User, error) {
	return nil, nil, nil
}
func (m *jobControlMockStore) CreateAPIToken(ctx context.Context, apiToken *models.APIToken) error {
	return nil
}
func (m *jobControlMockStore) UpdateTokenLastUsed(ctx context.Context, tokenID string, lastUsed time.Time) error {
	return nil
}
func (m *jobControlMockStore) GetAPITokensByUser(ctx context.Context, userID string) ([]models.APIToken, error) {
	return nil, nil
}
func (m *jobControlMockStore) DeleteAPIToken(ctx context.Context, tokenID string) error { return nil }
func (m *jobControlMockStore) GetUserByID(ctx context.Context, userID string) (*models.User, error) {
	return nil, nil
}
func (m *jobControlMockStore) CreateUser(ctx context.Context, user *models.User) error { return nil }
func (m *jobControlMockStore) EnsureDefaultUser() error                                { return nil }

var _ store.Store = (*jobControlMockStore)(nil)
var _ guardedJobStore = (*jobControlMockStore)(nil)

// TestCancelJob_SubmittedSuccess covers the "won the race" branch of
// Finding 1b: a submitted job with no worker anywhere near it cancels the
// Corndogs task and lands directly on "cancelled".
func TestCancelJob_SubmittedSuccess(t *testing.T) {
	taskID := "task-1"
	job := &models.Job{JobID: "job-1", Status: "submitted", CorndogsTaskID: &taskID}
	st := newJobControlMockStore(job)
	mockCorndogs := corndogs.NewMockClient()

	updated, err := CancelJob(context.Background(), st, mockCorndogs, job)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if updated.Status != "cancelled" {
		t.Errorf("expected status 'cancelled', got %q", updated.Status)
	}
	if updated.LastError != "cancelled" {
		t.Errorf("expected last_error 'cancelled', got %q", updated.LastError)
	}
	if mockCorndogs.GetCancelTaskCallCount() != 1 {
		t.Errorf("expected 1 CancelTask call, got %d", mockCorndogs.GetCancelTaskCallCount())
	}
	stored, err := st.GetJobByID(context.Background(), job.JobID)
	if err != nil {
		t.Fatalf("unexpected error reloading job: %v", err)
	}
	if stored.Status != "cancelled" {
		t.Errorf("expected stored status 'cancelled', got %q", stored.Status)
	}
}

// TestCancelJob_SubmittedClaimedRace_LeavesCancelling covers the "lost the
// race" branch of Finding 1b: the Corndogs task was already claimed by a
// worker (simulated by CancelTask failing), so the job must be left
// "cancelling" for the claiming worker to finalize rather than forced to
// "cancelled" out from under it (the TOCTOU/ghost-execution bug).
func TestCancelJob_SubmittedClaimedRace_LeavesCancelling(t *testing.T) {
	taskID := "task-1"
	job := &models.Job{JobID: "job-1", Status: "submitted", CorndogsTaskID: &taskID}
	st := newJobControlMockStore(job)
	mockCorndogs := corndogs.NewMockClient()
	mockCorndogs.CancelTaskFunc = func(ctx context.Context, taskID string, currentState string) (*pb.Task, error) {
		return nil, fmt.Errorf("task already claimed")
	}

	updated, err := CancelJob(context.Background(), st, mockCorndogs, job)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if updated.Status != "cancelling" {
		t.Errorf("expected status left 'cancelling' for the worker to finalize, got %q", updated.Status)
	}
	if updated.CancelMode != "cancel" {
		t.Errorf("expected cancel_mode 'cancel', got %q", updated.CancelMode)
	}

	stored, err := st.GetJobByID(context.Background(), job.JobID)
	if err != nil {
		t.Fatalf("unexpected error reloading job: %v", err)
	}
	if stored.Status != "cancelling" {
		t.Errorf("expected stored status 'cancelling', got %q", stored.Status)
	}
}

// TestCancelJob_RunningJob_LeavesCancelling verifies the ordinary running-job
// path still hands off to the worker rather than finalizing anything itself.
func TestCancelJob_RunningJob_LeavesCancelling(t *testing.T) {
	job := &models.Job{JobID: "job-1", Status: "running"}
	st := newJobControlMockStore(job)
	mockCorndogs := corndogs.NewMockClient()

	updated, err := CancelJob(context.Background(), st, mockCorndogs, job)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if updated.Status != "cancelling" {
		t.Errorf("expected status 'cancelling', got %q", updated.Status)
	}
	if updated.CancelMode != "cancel" {
		t.Errorf("expected cancel_mode 'cancel', got %q", updated.CancelMode)
	}
	if mockCorndogs.GetCancelTaskCallCount() != 0 {
		t.Errorf("expected no CancelTask call for a running job, got %d", mockCorndogs.GetCancelTaskCallCount())
	}
}

// TestKillJob_OnCancelling_Allowed covers Finding 2a: kill can escalate a
// job that's already "cancelling" (a stuck graceful cancel), setting
// cancel_mode to "kill" without disturbing the "cancelling" status itself —
// the executing worker's pollForCancel will pick up the escalation on its
// next tick.
func TestKillJob_OnCancelling_Allowed(t *testing.T) {
	job := &models.Job{JobID: "job-1", Status: "cancelling", CancelMode: "cancel"}
	st := newJobControlMockStore(job)
	mockCorndogs := corndogs.NewMockClient()

	updated, err := KillJob(context.Background(), st, mockCorndogs, job)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if updated.Status != "cancelling" {
		t.Errorf("expected status to remain 'cancelling', got %q", updated.Status)
	}
	if updated.CancelMode != "kill" {
		t.Errorf("expected cancel_mode escalated to 'kill', got %q", updated.CancelMode)
	}

	stored, err := st.GetJobByID(context.Background(), job.JobID)
	if err != nil {
		t.Fatalf("unexpected error reloading job: %v", err)
	}
	if stored.CancelMode != "kill" {
		t.Errorf("expected stored cancel_mode 'kill', got %q", stored.CancelMode)
	}
}

// TestCancelJob_OnCancelling_Refused covers Finding 2a's other half: a
// graceful cancel (not kill) against an already-"cancelling" job has
// nothing new to do and must be refused.
func TestCancelJob_OnCancelling_Refused(t *testing.T) {
	job := &models.Job{JobID: "job-1", Status: "cancelling", CancelMode: "cancel"}
	st := newJobControlMockStore(job)
	mockCorndogs := corndogs.NewMockClient()

	_, err := CancelJob(context.Background(), st, mockCorndogs, job)
	if !errors.Is(err, ErrNotCancellable) {
		t.Errorf("expected ErrNotCancellable, got %v", err)
	}
}

// TestCancelJob_AlreadyTerminal_Refused is a basic regression check: a
// terminal job can't be cancelled or killed.
func TestCancelJob_AlreadyTerminal_Refused(t *testing.T) {
	job := &models.Job{JobID: "job-1", Status: "completed"}
	st := newJobControlMockStore(job)
	mockCorndogs := corndogs.NewMockClient()

	if _, err := CancelJob(context.Background(), st, mockCorndogs, job); !errors.Is(err, ErrNotCancellable) {
		t.Errorf("expected ErrNotCancellable from CancelJob, got %v", err)
	}
	if _, err := KillJob(context.Background(), st, mockCorndogs, job); !errors.Is(err, ErrNotCancellable) {
		t.Errorf("expected ErrNotCancellable from KillJob, got %v", err)
	}
}

// TestKillJob_SubmittedJob_SetsKilledLastError verifies the immediate-finalize
// path (no worker involved yet) records "killed by admin" as the terminal
// last_error for a kill, distinct from a graceful cancel's "cancelled".
func TestKillJob_SubmittedJob_SetsKilledLastError(t *testing.T) {
	job := &models.Job{JobID: "job-1", Status: "queued"}
	st := newJobControlMockStore(job)
	mockCorndogs := corndogs.NewMockClient()

	updated, err := KillJob(context.Background(), st, mockCorndogs, job)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if updated.Status != "cancelled" {
		t.Errorf("expected status 'cancelled', got %q", updated.Status)
	}
	if updated.LastError != "killed by admin" {
		t.Errorf("expected last_error 'killed by admin', got %q", updated.LastError)
	}
}
