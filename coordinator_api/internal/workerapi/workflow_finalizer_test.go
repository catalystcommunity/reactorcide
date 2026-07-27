package workerapi

import (
	"context"
	"testing"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/corndogs"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/workerapi/csilapi"
)

// fakeWorkflowFinalizer records the job IDs it was asked to advance so tests
// can assert the coordinator-mediated lifecycle drives workflow progression.
type fakeWorkflowFinalizer struct {
	started   []string
	completed []string
}

func (f *fakeWorkflowFinalizer) ProcessWorkflowJobStarted(_ context.Context, job *models.Job) error {
	f.started = append(f.started, job.JobID)
	return nil
}

func (f *fakeWorkflowFinalizer) ProcessWorkflowCompletion(_ context.Context, _ string, job *models.Job) error {
	f.completed = append(f.completed, job.JobID)
	return nil
}

// claimWorkflowJob mirrors claimOneJob but binds the job to a workflow instance
// so the started/completion hooks fire.
func claimWorkflowJob(t *testing.T, h *testHarness, workflowID string) (job *models.Job, leaseID, sessionToken string) {
	t.Helper()
	ctx := context.Background()

	queueUUID := "55555555-5555-5555-5555-555555555555"
	h.store.seedQueue(models.Queue{
		QueueUUID:       queueUUID,
		Characteristics: mustCharacteristics(t, map[string]any{"os": "linux"}),
	})

	wf := workflowID
	job = &models.Job{UserID: "user-1", Name: "ichoi-release", JobCommand: "echo hi", Status: "submitted", WorkflowID: &wf}
	h.store.seedJob(job)
	task, err := h.corndogs.SubmitTaskToQueue(ctx, queueUUID, &corndogs.TaskPayload{JobID: job.JobID}, 0)
	if err != nil {
		t.Fatalf("failed to submit task: %v", err)
	}
	job.CorndogsTaskID = &task.Uuid
	if err := h.store.UpdateJob(ctx, job); err != nil {
		t.Fatalf("failed to stamp corndogs_task_id: %v", err)
	}

	sessionToken, _ = h.registerWorker(t, "wf-worker", "linux", "amd64", nil)
	resp, err := h.service.RequestJob(ctxWithAuth(sessionToken), csilapi.RequestJobRequest{
		WorkerCharacteristics: csilapi.WorkerCharacteristics{Os: "linux", Arch: "amd64"},
	})
	if err != nil {
		t.Fatalf("RequestJob failed: %v", err)
	}
	if !resp.HasLease || resp.Lease == nil {
		t.Fatalf("expected a lease from claimWorkflowJob's setup")
	}
	return job, resp.Lease.LeaseId, sessionToken
}

// TestWorkflowAdvancesAcrossJobLifecycle guards the coordinator-mediated
// workflow progression: claiming a workflow-bound job marks its node running,
// and reporting the result advances the workflow (which rolls up the instance
// status). Without these hooks the workflow instance stays "running" forever
// even after all its jobs complete -- the regression this test protects.
func TestWorkflowAdvancesAcrossJobLifecycle(t *testing.T) {
	h := newTestHarness()
	fin := &fakeWorkflowFinalizer{}
	h.deps.WorkflowFinalizer = fin

	job, leaseID, token := claimWorkflowJob(t, h, "wf-123")

	if len(fin.started) != 1 || fin.started[0] != job.JobID {
		t.Fatalf("expected ProcessWorkflowJobStarted for %q on claim, got %v", job.JobID, fin.started)
	}

	if _, err := h.service.ReportResult(ctxWithAuth(token), csilapi.ReportResultRequest{
		LeaseId:  leaseID,
		ExitCode: 0,
		Status:   "completed",
	}); err != nil {
		t.Fatalf("ReportResult failed: %v", err)
	}

	if len(fin.completed) != 1 || fin.completed[0] != job.JobID {
		t.Fatalf("expected ProcessWorkflowCompletion for %q on report, got %v", job.JobID, fin.completed)
	}
}

// TestWorkflowFinalizerSkippedForNonWorkflowJob guards that a plain job (no
// workflow) never touches the workflow finalizer.
func TestWorkflowFinalizerSkippedForNonWorkflowJob(t *testing.T) {
	h := newTestHarness()
	fin := &fakeWorkflowFinalizer{}
	h.deps.WorkflowFinalizer = fin

	_, leaseID, token := claimOneJob(t, h)

	if _, err := h.service.ReportResult(ctxWithAuth(token), csilapi.ReportResultRequest{
		LeaseId:  leaseID,
		ExitCode: 0,
		Status:   "completed",
	}); err != nil {
		t.Fatalf("ReportResult failed: %v", err)
	}

	if len(fin.started) != 0 || len(fin.completed) != 0 {
		t.Fatalf("non-workflow job must not invoke the workflow finalizer; started=%v completed=%v", fin.started, fin.completed)
	}
}
