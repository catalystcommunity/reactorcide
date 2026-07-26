package workerapi

import (
	"context"
	"testing"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/corndogs"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/workerapi/csilapi"
)

// claimOneJob is a test helper that seeds a queue + job, submits its
// corndogs task, registers a worker, and claims the job via RequestJob --
// the shared setup Heartbeat/AppendLogs/ReportResult tests all need a live
// lease for. It also stamps job.CorndogsTaskID (normally done by the real
// submit path, job_handler.go, which these tests bypass by driving corndogs
// directly) so Heartbeat/ReportResult's corndogs plumbing has something to
// extend/finalize.
func claimOneJob(t *testing.T, h *testHarness) (job *models.Job, leaseID, sessionToken string) {
	t.Helper()
	ctx := context.Background()

	queueUUID := "55555555-5555-5555-5555-555555555555"
	h.store.seedQueue(models.Queue{
		QueueUUID:       queueUUID,
		Characteristics: mustCharacteristics(t, map[string]any{"os": "linux"}),
	})

	job = &models.Job{UserID: "user-1", Name: "build", JobCommand: "echo hi", Status: "submitted"}
	h.store.seedJob(job)
	submittedTask, err := h.corndogs.SubmitTaskToQueue(ctx, queueUUID, &corndogs.TaskPayload{JobID: job.JobID}, 0)
	if err != nil {
		t.Fatalf("failed to submit task: %v", err)
	}
	job.CorndogsTaskID = &submittedTask.Uuid
	if err := h.store.UpdateJob(ctx, job); err != nil {
		t.Fatalf("failed to stamp corndogs_task_id: %v", err)
	}

	sessionToken, _ = h.registerWorker(t, "claim-worker", "linux", "amd64", nil)
	resp, err := h.service.RequestJob(ctxWithAuth(sessionToken), csilapi.RequestJobRequest{
		WorkerCharacteristics: csilapi.WorkerCharacteristics{Os: "linux", Arch: "amd64"},
	})
	if err != nil {
		t.Fatalf("RequestJob failed: %v", err)
	}
	if !resp.HasLease || resp.Lease == nil {
		t.Fatalf("expected a lease from claimOneJob's setup")
	}

	saved, err := h.store.GetJobByID(ctx, job.JobID)
	if err != nil {
		t.Fatalf("failed to reload claimed job: %v", err)
	}
	return saved, resp.Lease.LeaseId, sessionToken
}

func TestHeartbeat_ExtendsCorndogsAndEmitsCancelDirective(t *testing.T) {
	h := newTestHarness()
	job, leaseID, token := claimOneJob(t, h)

	// No cancel requested yet: heartbeat extends the task, no directives.
	resp, err := h.service.Heartbeat(ctxWithAuth(token), csilapi.HeartbeatRequest{
		Status:        "running",
		RunningLeases: []csilapi.RunningLease{{LeaseId: leaseID}},
	})
	if err != nil {
		t.Fatalf("Heartbeat failed: %v", err)
	}
	if len(resp.Directives) != 0 {
		t.Fatalf("expected no directives before any cancel request, got %+v", resp.Directives)
	}
	if len(h.corndogs.SendHeartbeatCalls) != 1 {
		t.Fatalf("expected exactly one SendHeartbeat call, got %d", len(h.corndogs.SendHeartbeatCalls))
	}
	if h.corndogs.SendHeartbeatCalls[0].TaskID != *job.CorndogsTaskID {
		t.Fatalf("expected heartbeat to extend the job's own corndogs task")
	}

	// Simulate a cancel request landing on the job mid-run (mirrors
	// jobcontrol.CancelJob's write for a running job).
	if _, matched, err := h.store.UpdateJobStatusGuarded(context.Background(), job.JobID, []string{"running"}, func(j *models.Job) {
		j.Status = "cancelling"
		j.CancelMode = "cancel"
	}); err != nil || !matched {
		t.Fatalf("failed to move job to cancelling: matched=%v err=%v", matched, err)
	}

	resp2, err := h.service.Heartbeat(ctxWithAuth(token), csilapi.HeartbeatRequest{
		Status:        "running",
		RunningLeases: []csilapi.RunningLease{{LeaseId: leaseID}},
	})
	if err != nil {
		t.Fatalf("Heartbeat failed: %v", err)
	}
	if len(resp2.Directives) != 1 || resp2.Directives[0].LeaseId != leaseID || resp2.Directives[0].Action != "cancel" {
		t.Fatalf("expected a single cancel directive for the cancelling lease, got %+v", resp2.Directives)
	}
}

func TestHeartbeat_KillDirectiveWhenKillRequested(t *testing.T) {
	h := newTestHarness()
	job, leaseID, token := claimOneJob(t, h)

	if _, matched, err := h.store.UpdateJobStatusGuarded(context.Background(), job.JobID, []string{"running"}, func(j *models.Job) {
		j.Status = "cancelling"
		j.CancelMode = "kill"
	}); err != nil || !matched {
		t.Fatalf("failed to move job to cancelling/kill: matched=%v err=%v", matched, err)
	}

	resp, err := h.service.Heartbeat(ctxWithAuth(token), csilapi.HeartbeatRequest{
		Status:        "running",
		RunningLeases: []csilapi.RunningLease{{LeaseId: leaseID}},
	})
	if err != nil {
		t.Fatalf("Heartbeat failed: %v", err)
	}
	if len(resp.Directives) != 1 || resp.Directives[0].Action != "kill" {
		t.Fatalf("expected a kill directive, got %+v", resp.Directives)
	}
}

func TestHeartbeat_UnauthorizedWithoutSession(t *testing.T) {
	h := newTestHarness()
	_, err := h.service.Heartbeat(context.Background(), csilapi.HeartbeatRequest{Status: "running"})
	assertServiceErrorCode(t, err, "unauthorized")
}
