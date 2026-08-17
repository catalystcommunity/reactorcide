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

// fakeJobStatusReporter records the jobs whose individual VCS check was posted.
type fakeJobStatusReporter struct{ jobIDs []string }

func (f *fakeJobStatusReporter) UpdateJobStatus(_ context.Context, job *models.Job) error {
	f.jobIDs = append(f.jobIDs, job.JobID)
	return nil
}

// TestReportResultResolvesNonNodeJobCheck guards that a completing non-node job
// (e.g. the eval, which posts a pending "reactorcide/eval" check at creation)
// gets its own VCS check resolved on ReportResult -- previously it hung pending
// forever.
func TestReportResultResolvesNonNodeJobCheck(t *testing.T) {
	h := newTestHarness()
	rep := &fakeJobStatusReporter{}
	h.deps.JobStatusReporter = rep

	job, leaseID, token := claimOneJob(t, h) // no WorkflowNodeID
	if _, err := h.service.ReportResult(ctxWithAuth(token), csilapi.ReportResultRequest{
		LeaseId: leaseID, ExitCode: 0, Status: "completed",
	}); err != nil {
		t.Fatalf("ReportResult failed: %v", err)
	}
	if len(rep.jobIDs) != 1 || rep.jobIDs[0] != job.JobID {
		t.Fatalf("expected the non-node job's VCS check reported on completion, got %v", rep.jobIDs)
	}
}

// TestReportResultSkipsWorkflowNodeCheck guards that a workflow node job does
// NOT get an individual VCS check on completion (the aggregate workflow check
// covers it) -- otherwise every node would add a redundant per-node check.
func TestReportResultSkipsWorkflowNodeCheck(t *testing.T) {
	h := newTestHarness()
	rep := &fakeJobStatusReporter{}
	h.deps.JobStatusReporter = rep

	ctx := context.Background()
	queueUUID := "55555555-5555-5555-5555-555555555555"
	h.store.seedQueue(models.Queue{QueueUUID: queueUUID, Characteristics: mustCharacteristics(t, map[string]any{"os": "linux"})})
	nodeID := "node-1"
	job := &models.Job{UserID: "user-1", Name: "test-go", JobCommand: "go test", Status: "submitted", WorkflowNodeID: &nodeID}
	h.store.seedJob(job)
	task, err := h.corndogs.SubmitTaskToQueue(ctx, queueUUID, &corndogs.TaskPayload{JobID: job.JobID}, 0)
	if err != nil {
		t.Fatalf("submit task: %v", err)
	}
	job.CorndogsTaskID = &task.Uuid
	if err := h.store.UpdateJob(ctx, job); err != nil {
		t.Fatalf("update job: %v", err)
	}
	token, _ := h.registerWorker(t, "node-worker", "linux", "amd64", nil)
	resp, err := h.service.RequestJob(ctxWithAuth(token), csilapi.RequestJobRequest{
		WorkerCharacteristics: csilapi.WorkerCharacteristics{Os: "linux", Arch: "amd64"},
	})
	if err != nil || !resp.HasLease {
		t.Fatalf("RequestJob failed: %v (hasLease=%v)", err, resp.HasLease)
	}
	if _, err := h.service.ReportResult(ctxWithAuth(token), csilapi.ReportResultRequest{
		LeaseId: resp.Lease.LeaseId, ExitCode: 0, Status: "completed",
	}); err != nil {
		t.Fatalf("ReportResult failed: %v", err)
	}
	if len(rep.jobIDs) != 0 {
		t.Fatalf("workflow node job must not get an individual VCS check; got %v", rep.jobIDs)
	}
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

// TestSecretDenialAdvancesWorkflow guards the coordinator-only terminal path.
// The worker gets no lease after a secret denial, so it cannot report the
// result that normally advances the workflow.
func TestSecretDenialAdvancesWorkflow(t *testing.T) {
	h := newTestHarness()
	fin := &fakeWorkflowFinalizer{}
	h.deps.WorkflowFinalizer = fin

	job, token := seedQueueAndJobWithSecretRef(t, h, false)
	workflowID := "wf-secret-denial"
	job.WorkflowID = &workflowID
	if err := h.store.UpdateJob(context.Background(), job); err != nil {
		t.Fatalf("failed to bind job to workflow: %v", err)
	}

	resp, err := h.service.RequestJob(ctxWithAuth(token), csilapi.RequestJobRequest{
		WorkerCharacteristics: csilapi.WorkerCharacteristics{Os: "linux", Arch: "amd64"},
	})
	if err != nil {
		t.Fatalf("RequestJob failed: %v", err)
	}
	if resp.HasLease || resp.Lease != nil {
		t.Fatalf("secret denial must not return a lease; got %+v", resp)
	}
	if len(fin.started) != 1 || fin.started[0] != job.JobID {
		t.Fatalf("expected workflow start for %q, got %v", job.JobID, fin.started)
	}
	if len(fin.completed) != 1 || fin.completed[0] != job.JobID {
		t.Fatalf("expected workflow completion for %q after secret denial, got %v", job.JobID, fin.completed)
	}
}

// TestClaimTimeCancellationAdvancesWorkflow guards the other terminal path
// that does not return a lease to a worker.
func TestClaimTimeCancellationAdvancesWorkflow(t *testing.T) {
	h := newTestHarness()
	fin := &fakeWorkflowFinalizer{}
	h.deps.WorkflowFinalizer = fin
	ctx := context.Background()

	queueUUID := "55555555-5555-5555-5555-555555555556"
	h.store.seedQueue(models.Queue{
		QueueUUID:       queueUUID,
		Characteristics: mustCharacteristics(t, map[string]any{"os": "linux"}),
	})
	workflowID := "wf-claim-cancel"
	job := &models.Job{
		UserID:     "user-1",
		Name:       "cancel-before-lease",
		JobCommand: "echo hi",
		Status:     "cancelling",
		WorkflowID: &workflowID,
	}
	h.store.seedJob(job)
	if _, err := h.corndogs.SubmitTaskToQueue(ctx, queueUUID, &corndogs.TaskPayload{JobID: job.JobID}, 0); err != nil {
		t.Fatalf("failed to submit task: %v", err)
	}
	token, _ := h.registerWorker(t, "wf-cancel-worker", "linux", "amd64", nil)

	resp, err := h.service.RequestJob(ctxWithAuth(token), csilapi.RequestJobRequest{
		WorkerCharacteristics: csilapi.WorkerCharacteristics{Os: "linux", Arch: "amd64"},
	})
	if err != nil {
		t.Fatalf("RequestJob failed: %v", err)
	}
	if resp.HasLease || resp.Lease != nil {
		t.Fatalf("claim-time cancellation must not return a lease; got %+v", resp)
	}
	if len(fin.started) != 0 {
		t.Fatalf("cancelled job must not start its workflow node; got %v", fin.started)
	}
	if len(fin.completed) != 1 || fin.completed[0] != job.JobID {
		t.Fatalf("expected workflow completion for %q after cancellation, got %v", job.JobID, fin.completed)
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
