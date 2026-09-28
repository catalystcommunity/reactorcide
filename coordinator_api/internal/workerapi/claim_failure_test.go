package workerapi

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"syscall"
	"testing"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/corndogs"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/secrets"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/workerapi/csilapi"
)

// errConnRefused is the shape of the 2026-09-27 incident error: Postgres was
// restarting after an OOM kill.
var errConnRefused = fmt.Errorf("failed to create api token: %w", &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED})

// faultyStore wraps fakeStore with a job token store and injectable failures
// for the writes RequestJob makes while it commits a claim.
type faultyStore struct {
	*fakeStore
	mu           sync.Mutex
	mintErr      error
	leaseErr     error
	denialErr    error // fails the submitted->failed write of a denial
	revokedJobs  []string
	mintedTokens int
}

func (f *faultyStore) MintJobToken(ctx context.Context, job *models.Job) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.mintErr != nil {
		return "", f.mintErr
	}
	f.mintedTokens++
	return fmt.Sprintf("job-token-%d", f.mintedTokens), nil
}

func (f *faultyStore) RevokeJobTokens(ctx context.Context, jobID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.revokedJobs = append(f.revokedJobs, jobID)
	return nil
}

func (f *faultyStore) CreateWorkerLease(ctx context.Context, workerID, jobID string, queueUUID *string) (*models.WorkerLease, error) {
	f.mu.Lock()
	err := f.leaseErr
	f.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return f.fakeStore.CreateWorkerLease(ctx, workerID, jobID, queueUUID)
}

func (f *faultyStore) UpdateJobStatusGuarded(ctx context.Context, jobID string, fromStatuses []string, apply func(*models.Job)) (*models.Job, bool, error) {
	f.mu.Lock()
	denialErr := f.denialErr
	f.mu.Unlock()
	if denialErr != nil {
		probe := &models.Job{}
		apply(probe)
		if probe.Status == "failed" {
			return nil, false, denialErr
		}
	}
	return f.fakeStore.UpdateJobStatusGuarded(ctx, jobID, fromStatuses, apply)
}

func (f *faultyStore) clearFaults() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.mintErr, f.leaseErr, f.denialErr = nil, nil, nil
}

func newFaultyHarness() (*testHarness, *faultyStore) {
	h := newTestHarness()
	fs := &faultyStore{fakeStore: h.store}
	h.deps.Store = fs
	return h, fs
}

// seedClaimableJob seeds a queue, a job, and its corndogs task, and registers
// a worker that satisfies the queue.
func seedClaimableJob(t *testing.T, h *testHarness, queueUUID string) (job *models.Job, taskID, token string) {
	t.Helper()
	ctx := context.Background()
	h.store.seedQueue(models.Queue{QueueUUID: queueUUID, Characteristics: mustCharacteristics(t, map[string]any{"os": "linux"})})
	job = &models.Job{UserID: "user-1", Name: "ci-tests", JobCommand: "echo hi", Status: "submitted"}
	h.store.seedJob(job)
	task, err := h.corndogs.SubmitTaskToQueue(ctx, queueUUID, &corndogs.TaskPayload{JobID: job.JobID}, 5)
	if err != nil {
		t.Fatalf("failed to submit task: %v", err)
	}
	job.CorndogsTaskID = &task.Uuid
	if err := h.store.UpdateJob(ctx, job); err != nil {
		t.Fatalf("failed to stamp corndogs_task_id: %v", err)
	}
	token, _ = h.registerWorker(t, "claim-worker-"+queueUUID[:4], "linux", "amd64", nil)
	return job, task.Uuid, token
}

func requestJob(t *testing.T, h *testHarness, token string) csilapi.RequestJobResponse {
	t.Helper()
	resp, err := h.service.RequestJob(ctxWithAuth(token), csilapi.RequestJobRequest{
		WorkerCharacteristics: csilapi.WorkerCharacteristics{Os: "linux", Arch: "amd64"},
	})
	if err != nil {
		t.Fatalf("RequestJob returned an error instead of has_lease=false: %v", err)
	}
	return resp
}

func taskState(t *testing.T, h *testHarness, taskID string) string {
	t.Helper()
	task, err := h.corndogs.GetTaskByID(context.Background(), taskID)
	if err != nil {
		t.Fatalf("GetTaskByID(%s): %v", taskID, err)
	}
	return task.CurrentState
}

func assertJobRequeued(t *testing.T, h *testHarness, fs *faultyStore, job *models.Job, taskID string) {
	t.Helper()
	saved, err := h.store.GetJobByID(context.Background(), job.JobID)
	if err != nil {
		t.Fatalf("failed to reload job: %v", err)
	}
	if saved.Status != "submitted" || saved.WorkerID != nil || saved.StartedAt != nil {
		t.Fatalf("job after failed claim = status %q worker %v started %v; want submitted with no worker", saved.Status, saved.WorkerID, saved.StartedAt)
	}
	if got := taskState(t, h, taskID); got != "submitted" {
		t.Fatalf("corndogs task state after failed claim = %q, want submitted", got)
	}
	h.store.mu.Lock()
	defer h.store.mu.Unlock()
	for _, l := range h.store.leases {
		if l.JobID == job.JobID && l.ReleasedAt == nil {
			t.Fatalf("failed claim left open lease %s", l.LeaseID)
		}
	}
}

// TestRequestJob_JobTokenStoreErrorRequeues reproduces the incident: the job
// token write failed with a connection error. The job must go back to
// submitted, the task must stay claimable, and the next claim must lease it.
func TestRequestJob_JobTokenStoreErrorRequeues(t *testing.T) {
	h, fs := newFaultyHarness()
	job, taskID, token := seedClaimableJob(t, h, "77777777-7777-7777-7777-777777777771")

	fs.mintErr = errConnRefused
	if resp := requestJob(t, h, token); resp.HasLease {
		t.Fatalf("claim with a failed job token must not return a lease")
	}
	assertJobRequeued(t, h, fs, job, taskID)
	if len(fs.revokedJobs) != 1 {
		t.Fatalf("expected the abandoned claim's job tokens to be revoked, got %v", fs.revokedJobs)
	}

	fs.clearFaults()
	resp := requestJob(t, h, token)
	if !resp.HasLease || resp.Lease == nil || resp.Lease.JobId != job.JobID {
		t.Fatalf("next RequestJob must lease the requeued job; got %+v", resp)
	}
}

func TestRequestJob_LeaseStoreErrorRequeues(t *testing.T) {
	h, fs := newFaultyHarness()
	job, taskID, token := seedClaimableJob(t, h, "77777777-7777-7777-7777-777777777772")

	fs.leaseErr = errConnRefused
	if resp := requestJob(t, h, token); resp.HasLease {
		t.Fatalf("claim with a failed lease write must not return a lease")
	}
	assertJobRequeued(t, h, fs, job, taskID)

	fs.clearFaults()
	resp := requestJob(t, h, token)
	if !resp.HasLease || resp.Lease == nil || resp.Lease.JobId != job.JobID {
		t.Fatalf("next RequestJob must lease the requeued job; got %+v", resp)
	}
	var sawToken bool
	for _, s := range resp.Lease.Secrets {
		if s.Key == "REACTORCIDE_API_TOKEN" && s.Value != "" {
			sawToken = true
		}
	}
	if !sawToken {
		t.Fatalf("lease must carry the minted job token in Secrets")
	}
}

// TestRequestJob_SecretStoreErrorIsNotADenial: a secret lookup that fails
// because the store is down must not fail the job.
func TestRequestJob_SecretStoreErrorIsNotADenial(t *testing.T) {
	h, fs := newFaultyHarness()
	job, token := seedQueueAndJobWithSecretRef(t, h, true)
	h.deps.SecretsProvider = func(ctx context.Context, orgID string) (secrets.Provider, error) {
		return nil, errConnRefused
	}

	if resp := requestJob(t, h, token); resp.HasLease {
		t.Fatalf("claim must not lease while the secret store is down")
	}
	saved, _ := h.store.GetJobByID(context.Background(), job.JobID)
	if saved.Status != "submitted" {
		t.Fatalf("store outage during secret resolution must leave the job submitted, got %q (%s)", saved.Status, saved.LastError)
	}
	if saved.CorndogsTaskID == nil {
		// seedQueueAndJobWithSecretRef does not stamp the task id; find the
		// task by its requeue call instead.
		var requeued bool
		for _, call := range h.corndogs.UpdateTaskCalls {
			if call.NewState == "submitted" {
				requeued = true
			}
			if call.NewState == "failed" {
				t.Fatalf("task must not be failed on a store outage")
			}
		}
		if !requeued {
			t.Fatalf("task must be requeued on a store outage")
		}
	}
	_ = fs
}

// TestRequestJob_DenialNotRecordedLeavesTaskClaimable: a real denial whose
// job-row write fails must not finish the corndogs task.
func TestRequestJob_DenialNotRecordedLeavesTaskClaimable(t *testing.T) {
	h, fs := newFaultyHarness()
	job, token := seedQueueAndJobWithSecretRef(t, h, false)
	fs.denialErr = errors.New("write failed")

	if resp := requestJob(t, h, token); resp.HasLease {
		t.Fatalf("ungranted secret must never be leased")
	}
	for _, call := range h.corndogs.UpdateTaskCalls {
		if call.NewState == "failed" {
			t.Fatalf("task must not be failed while the job row is not failed")
		}
	}
	saved, _ := h.store.GetJobByID(context.Background(), job.JobID)
	if saved.Status != "submitted" {
		t.Fatalf("job = %q, want submitted", saved.Status)
	}
}
