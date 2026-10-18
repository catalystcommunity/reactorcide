package workerapi

import (
	"context"
	"testing"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/workerapi/csilapi"
)

func claimSchedulingJob(t *testing.T, h *testHarness) (*models.Job, string, string) {
	return claimOneJobWithTiming(t, h, true)
}

func TestSchedulingClaimAndRuntimeHeartbeat(t *testing.T) {
	h := newTestHarness()
	job, leaseID, token := claimSchedulingJob(t, h)
	lease, err := h.store.GetWorkerLeaseByID(context.Background(), leaseID)
	if err != nil {
		t.Fatal(err)
	}
	if job.SchedulingStartedAt == nil || !job.SchedulingStartedAt.Equal(lease.AcquiredAt) {
		t.Fatal("scheduling time must identify the claimed lease")
	}
	if job.Status != "scheduling" || job.StartedAt != nil || job.SchedulingStartedAt == nil {
		t.Fatalf("unexpected claim state: %s start=%v scheduling=%v", job.Status, job.StartedAt, job.SchedulingStartedAt)
	}
	_, err = h.service.Heartbeat(ctxWithAuth(token), csilapi.HeartbeatRequest{RunningLeases: []csilapi.RunningLease{{LeaseId: leaseID}}})
	if err != nil {
		t.Fatal(err)
	}
	saved, _ := h.store.GetJobByID(context.Background(), job.JobID)
	if saved.Status != "scheduling" || saved.StartedAt != nil {
		t.Fatal("empty heartbeat started runtime")
	}
	start := time.Now().UTC().Add(-time.Second).Format(time.RFC3339Nano)
	_, err = h.service.Heartbeat(ctxWithAuth(token), csilapi.HeartbeatRequest{RunningLeases: []csilapi.RunningLease{{LeaseId: leaseID, StartedAt: &start}}})
	if err != nil {
		t.Fatal(err)
	}
	saved, _ = h.store.GetJobByID(context.Background(), job.JobID)
	if saved.Status != "running" || saved.StartedAt == nil || saved.StartedAt.Format(time.RFC3339Nano) != start {
		t.Fatalf("runtime start not preserved: %s %v", saved.Status, saved.StartedAt)
	}
	// Repeated heartbeats cannot reset the runtime start.
	later := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = h.service.Heartbeat(ctxWithAuth(token), csilapi.HeartbeatRequest{RunningLeases: []csilapi.RunningLease{{LeaseId: leaseID, StartedAt: &later}}})
	if err != nil {
		t.Fatal(err)
	}
	saved, _ = h.store.GetJobByID(context.Background(), job.JobID)
	if saved.StartedAt.Format(time.RFC3339Nano) != start {
		t.Fatal("heartbeat reset runtime start")
	}
}

func TestSchedulingResultCarriesShortJobRuntime(t *testing.T) {
	h := newTestHarness()
	job, leaseID, token := claimSchedulingJob(t, h)
	now := time.Now().UTC()
	start := now.Add(-10 * time.Second).Format(time.RFC3339Nano)
	finish := now.Add(-8 * time.Second).Format(time.RFC3339Nano)
	_, err := h.service.ReportResult(ctxWithAuth(token), csilapi.ReportResultRequest{LeaseId: leaseID, Status: "completed", StartedAt: &start, FinishedAt: &finish})
	if err != nil {
		t.Fatal(err)
	}
	saved, _ := h.store.GetJobByID(context.Background(), job.JobID)
	if saved.StartedAt == nil || saved.CompletedAt == nil || saved.CompletedAt.Sub(*saved.StartedAt) != 2*time.Second {
		t.Fatalf("runtime includes scheduling or result delivery: %v %v", saved.StartedAt, saved.CompletedAt)
	}
}

func TestSchedulingFailureHasNoRuntime(t *testing.T) {
	h := newTestHarness()
	job, leaseID, token := claimSchedulingJob(t, h)
	message := "SchedulingBlocked: node selector cannot match"
	_, err := h.service.ReportResult(ctxWithAuth(token), csilapi.ReportResultRequest{LeaseId: leaseID, Status: "failed", ExitCode: -1, Error: &message})
	if err != nil {
		t.Fatal(err)
	}
	saved, _ := h.store.GetJobByID(context.Background(), job.JobID)
	if saved.Status != "failed" || saved.StartedAt != nil || saved.CompletedAt == nil {
		t.Fatalf("failure has invented runtime: %s %v", saved.Status, saved.StartedAt)
	}
}

func TestSchedulingTimingCannotUndoCancellationOrUseReleasedLease(t *testing.T) {
	for _, released := range []bool{false, true} {
		h := newTestHarness()
		job, leaseID, token := claimSchedulingJob(t, h)
		_, _, err := h.store.UpdateJobStatusGuarded(context.Background(), job.JobID, []string{"scheduling"}, func(j *models.Job) { j.Status = "cancelling"; j.CancelMode = "kill" })
		if err != nil {
			t.Fatal(err)
		}
		if released {
			if err := h.store.ReleaseWorkerLease(context.Background(), leaseID, "superseded"); err != nil {
				t.Fatal(err)
			}
		}
		start := time.Now().UTC().Format(time.RFC3339Nano)
		response, err := h.service.Heartbeat(ctxWithAuth(token), csilapi.HeartbeatRequest{RunningLeases: []csilapi.RunningLease{{LeaseId: leaseID, StartedAt: &start}}})
		if err != nil {
			t.Fatal(err)
		}
		saved, _ := h.store.GetJobByID(context.Background(), job.JobID)
		if saved.Status != "cancelling" {
			t.Fatal("heartbeat undid cancellation")
		}
		if released && saved.StartedAt != nil {
			t.Fatal("released lease changed runtime")
		}
		if !released && (len(response.Directives) != 1 || response.Directives[0].Action != "kill") {
			t.Fatal("scheduling lost kill directive")
		}
	}
}

func TestSchedulingRejectsInvalidRuntimeTimestamps(t *testing.T) {
	for _, stamp := range []string{"bad", time.Now().Add(time.Hour).Format(time.RFC3339Nano), time.Now().Add(-time.Hour).Format(time.RFC3339Nano)} {
		h := newTestHarness()
		job, leaseID, token := claimSchedulingJob(t, h)
		_, err := h.service.Heartbeat(ctxWithAuth(token), csilapi.HeartbeatRequest{RunningLeases: []csilapi.RunningLease{{LeaseId: leaseID, StartedAt: &stamp}}})
		if err == nil {
			t.Fatal("invalid timestamp accepted")
		}
		saved, _ := h.store.GetJobByID(context.Background(), job.JobID)
		if saved.Status != "scheduling" || saved.StartedAt != nil {
			t.Fatal("invalid timestamp changed job")
		}
	}
}

func TestSchedulingWorkflowStartsOnlyAtRuntime(t *testing.T) {
	h := newTestHarness()
	fin := &fakeWorkflowFinalizer{}
	h.deps.WorkflowFinalizer = fin
	job, leaseID, token := claimWorkflowJob(t, h, "workflow-1", true)
	if len(fin.started) != 0 {
		t.Fatal("workflow node started at claim")
	}
	start := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := h.service.Heartbeat(ctxWithAuth(token), csilapi.HeartbeatRequest{RunningLeases: []csilapi.RunningLease{{LeaseId: leaseID, StartedAt: &start}}}); err != nil {
		t.Fatal(err)
	}
	if len(fin.started) != 1 || fin.started[0] != job.JobID {
		t.Fatal("runtime did not start workflow node")
	}
}

func TestSchedulingLostLeaseRecovery(t *testing.T) {
	h, _, _ := newReconcilerHarness()
	job, _ := seedLostJob(t, h, "88888888-8888-8888-8888-888888888885", time.Hour, 0)
	_, _, err := h.store.UpdateJobStatusGuarded(context.Background(), job.JobID, []string{"running"}, func(j *models.Job) { j.Status = "scheduling"; j.SchedulingStartedAt = j.StartedAt; j.StartedAt = nil })
	if err != nil {
		t.Fatal(err)
	}
	h.service.reconcileLostJobs(context.Background())
	saved, _ := h.store.GetJobByID(context.Background(), job.JobID)
	if saved.Status != "submitted" || saved.StartedAt != nil || saved.SchedulingStartedAt != nil || saved.WorkerID != nil {
		t.Fatalf("scheduling lease was not recovered: %s", saved.Status)
	}
}
