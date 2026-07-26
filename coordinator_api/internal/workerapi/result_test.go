package workerapi

import (
	"context"
	"testing"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/workerapi/csilapi"
)

func TestReportResult_FinalizesJobAndReleasesLease(t *testing.T) {
	h := newTestHarness()
	job, leaseID, token := claimOneJob(t, h)

	resp, err := h.service.ReportResult(ctxWithAuth(token), csilapi.ReportResultRequest{
		LeaseId:  leaseID,
		ExitCode: 0,
		Status:   "completed",
	})
	if err != nil {
		t.Fatalf("ReportResult failed: %v", err)
	}
	if !resp.Ok {
		t.Fatalf("expected ok=true")
	}

	saved, err := h.store.GetJobByID(context.Background(), job.JobID)
	if err != nil {
		t.Fatalf("failed to reload job: %v", err)
	}
	if saved.Status != "completed" {
		t.Fatalf("expected job to be completed, got %q", saved.Status)
	}
	if saved.ExitCode == nil || *saved.ExitCode != 0 {
		t.Fatalf("expected exit code 0, got %+v", saved.ExitCode)
	}

	lease, err := h.store.GetWorkerLeaseByID(context.Background(), leaseID)
	if err != nil {
		t.Fatalf("failed to reload lease: %v", err)
	}
	if lease.IsActive() {
		t.Fatalf("expected lease to be released after ReportResult")
	}
	if lease.Outcome != "completed" {
		t.Fatalf("expected lease outcome to record the final status, got %q", lease.Outcome)
	}

	if len(h.corndogs.CompleteTaskCalls) != 1 {
		t.Fatalf("expected corndogs CompleteTask to be called once, got %d", len(h.corndogs.CompleteTaskCalls))
	}
}

func TestReportResult_FailedExitCode(t *testing.T) {
	h := newTestHarness()
	job, leaseID, token := claimOneJob(t, h)

	errMsg := "build step failed"
	_, err := h.service.ReportResult(ctxWithAuth(token), csilapi.ReportResultRequest{
		LeaseId:  leaseID,
		ExitCode: 1,
		Status:   "failed",
		Error:    &errMsg,
	})
	if err != nil {
		t.Fatalf("ReportResult failed: %v", err)
	}

	saved, err := h.store.GetJobByID(context.Background(), job.JobID)
	if err != nil {
		t.Fatalf("failed to reload job: %v", err)
	}
	if saved.Status != "failed" {
		t.Fatalf("expected job to be failed, got %q", saved.Status)
	}
	if saved.LastError != errMsg {
		t.Fatalf("expected LastError to carry the worker's error, got %q", saved.LastError)
	}
}

func TestReportResult_RejectsLeaseNotOwnedByCaller(t *testing.T) {
	h := newTestHarness()
	_, leaseID, _ := claimOneJob(t, h)

	otherToken, _ := h.registerWorker(t, "other-worker", "linux", "amd64", nil)
	_, err := h.service.ReportResult(ctxWithAuth(otherToken), csilapi.ReportResultRequest{
		LeaseId:  leaseID,
		ExitCode: 0,
		Status:   "completed",
	})
	if err == nil {
		t.Fatalf("expected an error when a worker reports a result for a lease it doesn't own")
	}
}

func TestReportResult_UnauthorizedWithoutSession(t *testing.T) {
	h := newTestHarness()
	_, err := h.service.ReportResult(context.Background(), csilapi.ReportResultRequest{LeaseId: "does-not-matter"})
	assertServiceErrorCode(t, err, "unauthorized")
}
