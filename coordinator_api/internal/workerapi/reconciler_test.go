package workerapi

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/corndogs"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

// lostJobFake adds the reconciler's store queries to fakeStore. openNodeJobs
// holds the job IDs whose workflow node is still submitted/running.
type lostJobFake struct {
	*fakeStore
	openNodeJobs map[string]bool
}

func (f *lostJobFake) ListLostRunningJobs(ctx context.Context, cutoff time.Time) ([]models.Job, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []models.Job
	for _, j := range f.jobs {
		if j.Status != "running" || j.StartedAt == nil || !j.StartedAt.Before(cutoff) {
			continue
		}
		held := false
		for _, l := range f.leases {
			if l.JobID == j.JobID && (l.ReleasedAt == nil || !l.ReleasedAt.Before(cutoff)) {
				held = true
			}
		}
		if !held {
			out = append(out, *j)
		}
	}
	return out, nil
}

func (f *lostJobFake) ListFinalJobsWithOpenWorkflowNode(ctx context.Context, cutoff time.Time) ([]models.Job, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []models.Job
	for id := range f.openNodeJobs {
		j, ok := f.jobs[id]
		if ok && terminalStatuses[j.Status] && j.CompletedAt != nil && j.CompletedAt.Before(cutoff) {
			out = append(out, *j)
		}
	}
	return out, nil
}

func newReconcilerHarness() (*testHarness, *lostJobFake, *fakeWorkflowFinalizer) {
	h := newTestHarness()
	lf := &lostJobFake{fakeStore: h.store, openNodeJobs: map[string]bool{}}
	h.deps.Store = lf
	fin := &fakeWorkflowFinalizer{}
	h.deps.WorkflowFinalizer = fin
	return h, lf, fin
}

// seedLostJob reproduces the incident's stuck row: running with a worker_id,
// no worker_leases row, and a corndogs task that is already failed.
func seedLostJob(t *testing.T, h *testHarness, queueUUID string, startedAgo time.Duration, retryCount int) (*models.Job, string) {
	t.Helper()
	ctx := context.Background()
	h.store.seedQueue(models.Queue{QueueUUID: queueUUID, Characteristics: mustCharacteristics(t, map[string]any{"os": "linux"})})
	started := time.Now().UTC().Add(-startedAgo)
	workerID := "gone-worker"
	workflowID := "wf-lost"
	job := &models.Job{UserID: "user-1", Name: "corndogs-ci-tests", JobCommand: "echo hi", Status: "running",
		StartedAt: &started, WorkerID: &workerID, RetryCount: retryCount, WorkflowID: &workflowID, QueueName: queueUUID}
	h.store.seedJob(job)
	task, err := h.corndogs.SubmitTaskToQueue(ctx, queueUUID, &corndogs.TaskPayload{JobID: job.JobID}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.corndogs.UpdateTask(ctx, task.Uuid, "submitted", "failed", nil); err != nil {
		t.Fatal(err)
	}
	job.CorndogsTaskID = &task.Uuid
	if err := h.store.UpdateJob(ctx, job); err != nil {
		t.Fatal(err)
	}
	return job, task.Uuid
}

func TestReconcileLostJobRequeuesWithNewTask(t *testing.T) {
	h, _, _ := newReconcilerHarness()
	queueUUID := "88888888-8888-8888-8888-888888888881"
	job, oldTask := seedLostJob(t, h, queueUUID, 3*time.Hour, 0)

	h.service.reconcileLostJobs(context.Background())

	saved, _ := h.store.GetJobByID(context.Background(), job.JobID)
	if saved.Status != "submitted" || saved.WorkerID != nil || saved.StartedAt != nil {
		t.Fatalf("lost job after reconcile = %q worker %v started %v; want submitted", saved.Status, saved.WorkerID, saved.StartedAt)
	}
	if saved.RetryCount != 1 {
		t.Fatalf("retry_count = %d, want 1", saved.RetryCount)
	}
	if saved.CorndogsTaskID == nil || *saved.CorndogsTaskID == oldTask {
		t.Fatalf("lost job must carry a replacement corndogs task, got %v", saved.CorndogsTaskID)
	}

	token, _ := h.registerWorker(t, "recovery-worker", "linux", "amd64", nil)
	resp := requestJob(t, h, token)
	if !resp.HasLease || resp.Lease.JobId != job.JobID {
		t.Fatalf("requeued lost job must be claimable; got %+v", resp)
	}
}

func TestReconcileLostJobFailsAfterRetries(t *testing.T) {
	h, _, fin := newReconcilerHarness()
	job, _ := seedLostJob(t, h, "88888888-8888-8888-8888-888888888882", 3*time.Hour, maxLostJobRetries)

	h.service.reconcileLostJobs(context.Background())

	saved, _ := h.store.GetJobByID(context.Background(), job.JobID)
	if saved.Status != "failed" || saved.CompletedAt == nil || !strings.Contains(saved.LastError, "lost") {
		t.Fatalf("exhausted lost job = %q (%q); want failed with a lost error", saved.Status, saved.LastError)
	}
	if len(fin.completed) != 1 || fin.completed[0] != job.JobID {
		t.Fatalf("failed lost job must advance its workflow; completed = %v", fin.completed)
	}
}

func TestReconcileLeavesRecentAndLeasedJobsAlone(t *testing.T) {
	h, _, _ := newReconcilerHarness()
	recent, _ := seedLostJob(t, h, "88888888-8888-8888-8888-888888888883", 30*time.Second, 0)

	// A job that a worker still holds, however long it has run.
	leased, _, _ := claimOneJob(t, h)
	old := time.Now().UTC().Add(-5 * time.Hour)
	h.store.mu.Lock()
	h.store.jobs[leased.JobID].StartedAt = &old
	h.store.mu.Unlock()

	h.service.reconcileLostJobs(context.Background())

	for _, id := range []string{recent.JobID, leased.JobID} {
		saved, _ := h.store.GetJobByID(context.Background(), id)
		if saved.Status != "running" {
			t.Fatalf("job %s = %q, want running (not lost)", id, saved.Status)
		}
	}
}

// TestReconcileAfterStaleLeaseReap covers the eval job of the incident: its
// result was dropped, the reaper released its lease, and the job stayed at
// running.
func TestReconcileAfterStaleLeaseReap(t *testing.T) {
	h, _, _ := newReconcilerHarness()
	job, leaseID, _ := claimOneJob(t, h)
	longAgo := time.Now().UTC().Add(-time.Hour)
	h.store.mu.Lock()
	h.store.jobs[job.JobID].StartedAt = &longAgo
	h.store.leases[leaseID].ReleasedAt = &longAgo
	h.store.leases[leaseID].Outcome = "reaped:stale"
	h.store.mu.Unlock()

	h.service.reconcileLostJobs(context.Background())

	saved, _ := h.store.GetJobByID(context.Background(), job.JobID)
	if saved.Status != "submitted" {
		t.Fatalf("job with a reaped lease = %q, want submitted", saved.Status)
	}
}

func TestReconcileCompletesOpenWorkflowNode(t *testing.T) {
	h, lf, fin := newReconcilerHarness()
	done := time.Now().UTC().Add(-10 * time.Minute)
	workflowID := "wf-open-node"
	job := &models.Job{UserID: "user-1", Name: "helm-validate", Status: "completed", CompletedAt: &done, WorkflowID: &workflowID}
	h.store.seedJob(job)
	lf.openNodeJobs[job.JobID] = true

	h.service.reconcileLostJobs(context.Background())

	if len(fin.completed) != 1 || fin.completed[0] != job.JobID {
		t.Fatalf("final job with an open node must complete the node; completed = %v", fin.completed)
	}
}

// TestSupersededTaskIsDiscarded: after a requeue, the old task can come back
// from its claim timeout. It must not drive the job.
func TestSupersededTaskIsDiscarded(t *testing.T) {
	h, _, _ := newReconcilerHarness()
	ctx := context.Background()
	queueUUID := "88888888-8888-8888-8888-888888888884"
	job, oldTask := seedLostJob(t, h, queueUUID, 3*time.Hour, 0)
	h.service.reconcileLostJobs(ctx)

	// Old task back at submitted, at a higher priority so it is claimed first.
	if _, err := h.corndogs.UpdateTask(ctx, oldTask, "cancelled", "submitted", nil); err != nil {
		t.Fatal(err)
	}
	token, _ := h.registerWorker(t, "stale-task-worker", "linux", "amd64", nil)
	var leased int
	for i := 0; i < 3; i++ {
		if resp := requestJob(t, h, token); resp.HasLease {
			leased++
		}
	}
	if leased != 1 {
		t.Fatalf("job leased %d times, want exactly once", leased)
	}
	saved, _ := h.store.GetJobByID(ctx, job.JobID)
	if saved.Status != "running" {
		t.Fatalf("job = %q, want running under the replacement task", saved.Status)
	}
}
