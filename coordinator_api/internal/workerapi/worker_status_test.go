package workerapi

import (
	"context"
	"testing"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/corndogs"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/workerapi/csilapi"
)

// TestRequestJob_QuarantinedWorkerGetsNoLease drives) must never be offered a
// job, even when a satisfying queue has work waiting.
func TestRequestJob_QuarantinedWorkerGetsNoLease(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()

	queueUUID := "55555555-5555-5555-5555-555555555555"
	h.store.seedQueue(models.Queue{
		QueueUUID:       queueUUID,
		Characteristics: mustCharacteristics(t, map[string]any{"os": "linux"}),
	})
	job := &models.Job{UserID: "user-1", Name: "build", JobCommand: "echo hi", Status: "submitted"}
	h.store.seedJob(job)
	if _, err := h.corndogs.SubmitTaskToQueue(ctx, queueUUID, &corndogs.TaskPayload{JobID: job.JobID}, 0); err != nil {
		t.Fatalf("failed to submit task: %v", err)
	}

	token, workerID := h.registerWorker(t, "worker-quarantined", "linux", "amd64", nil)
	if err := h.store.UpdateWorkerStatus(ctx, workerID, models.WorkerStatusQuarantined); err != nil {
		t.Fatalf("UpdateWorkerStatus: %v", err)
	}

	resp, err := h.service.RequestJob(ctxWithAuth(token), csilapi.RequestJobRequest{
		WorkerCharacteristics: csilapi.WorkerCharacteristics{Os: "linux", Arch: "amd64"},
	})
	if err != nil {
		t.Fatalf("RequestJob failed: %v", err)
	}
	if resp.HasLease {
		t.Fatalf("quarantined worker must not receive a lease, got %+v", resp.Lease)
	}

	// The task must still be sitting in corndogs untouched (not claimed,
	// not failed) -- a quarantined worker is refused an offer, it doesn't
	// poison the task for the next (active) worker to claim.
	job2, err := h.store.GetJobByID(ctx, job.JobID)
	if err != nil {
		t.Fatalf("failed to reload job: %v", err)
	}
	if job2.Status != "submitted" {
		t.Fatalf("job status changed to %q despite the claim being refused pre-match", job2.Status)
	}
}

// TestRequestJob_DisabledWorkerGetsNoLease is the disabled-status sibling of
// the quarantined case above.
func TestRequestJob_DisabledWorkerGetsNoLease(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()

	queueUUID := "66666666-6666-6666-6666-666666666666"
	h.store.seedQueue(models.Queue{
		QueueUUID:       queueUUID,
		Characteristics: mustCharacteristics(t, map[string]any{"os": "linux"}),
	})
	job := &models.Job{UserID: "user-1", Name: "build", JobCommand: "echo hi", Status: "submitted"}
	h.store.seedJob(job)
	if _, err := h.corndogs.SubmitTaskToQueue(ctx, queueUUID, &corndogs.TaskPayload{JobID: job.JobID}, 0); err != nil {
		t.Fatalf("failed to submit task: %v", err)
	}

	token, workerID := h.registerWorker(t, "worker-disabled", "linux", "amd64", nil)
	if err := h.store.UpdateWorkerStatus(ctx, workerID, models.WorkerStatusDisabled); err != nil {
		t.Fatalf("UpdateWorkerStatus: %v", err)
	}

	resp, err := h.service.RequestJob(ctxWithAuth(token), csilapi.RequestJobRequest{
		WorkerCharacteristics: csilapi.WorkerCharacteristics{Os: "linux", Arch: "amd64"},
	})
	if err != nil {
		t.Fatalf("RequestJob failed: %v", err)
	}
	if resp.HasLease {
		t.Fatalf("disabled worker must not receive a lease, got %+v", resp.Lease)
	}
}

// TestHeartbeat_DrainingFlagMirrorsWorkerStatus drives the drain-worker
// signal: Heartbeat's response carries draining=true for a
// quarantined/disabled worker and draining=false for an active one, derived
// live from the worker's current status on every call.
func TestHeartbeat_DrainingFlagMirrorsWorkerStatus(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()
	token, workerID := h.registerWorker(t, "worker-drain", "linux", "amd64", nil)

	resp, err := h.service.Heartbeat(ctxWithAuth(token), csilapi.HeartbeatRequest{Status: "idle"})
	if err != nil {
		t.Fatalf("Heartbeat failed: %v", err)
	}
	if resp.Draining {
		t.Fatalf("an active worker's heartbeat must report draining=false")
	}

	if err := h.store.UpdateWorkerStatus(ctx, workerID, models.WorkerStatusQuarantined); err != nil {
		t.Fatalf("UpdateWorkerStatus: %v", err)
	}
	resp, err = h.service.Heartbeat(ctxWithAuth(token), csilapi.HeartbeatRequest{Status: "idle"})
	if err != nil {
		t.Fatalf("Heartbeat failed: %v", err)
	}
	if !resp.Draining {
		t.Fatalf("a quarantined worker's heartbeat must report draining=true")
	}
}
