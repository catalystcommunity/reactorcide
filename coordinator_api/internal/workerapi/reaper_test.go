package workerapi

// This file covers RunLeaseReaper and reapStaleLeases. These functions had no
// test coverage, despite being wired into the
// coordinator's startup path (handlers/router.go's
// startWorkerLeaseReaperOnce) and despite P5's own scope explicitly calling
// out "lease-expiry-requeue" as integration surface to cover. The reaper
// itself is display/audit bookkeeping only -- it never touches corndogs or
// the job row, since corndogs' own task timeout is what actually requeues a
// dead worker's task -- so this is a unit-level test against the
// fakeStore/fakeCorndogsClient harness the rest of this package already uses,
// not a new integration test.

import (
	"context"
	"testing"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/workerapi/csilapi"
)

// TestReapStaleLeases_ReleasesOrphanedLeaseAndDropsSecretCache claims a job
// with a granted secret (so the lease secret cache -- see cache.go's
// leaseSecretCache -- is actually populated, mirroring
// TestRequestJob_GrantedSecret_ResolvedSeparatelyFromEnv's setup), backdates
// the lease's AcquiredAt past leaseStaleAfter (mirroring a worker that
// stopped heartbeating), and asserts reapStaleLeases releases it with
// outcome "reaped:stale", drops its cached secret values, and leaves the
// job row's own status completely untouched.
func TestReapStaleLeases_ReleasesOrphanedLeaseAndDropsSecretCache(t *testing.T) {
	h := newTestHarness()
	job, token := seedQueueAndJobWithSecretRef(t, h, true)

	resp, err := h.service.RequestJob(ctxWithAuth(token), csilapi.RequestJobRequest{
		WorkerCharacteristics: csilapi.WorkerCharacteristics{Os: "linux", Arch: "amd64"},
	})
	if err != nil {
		t.Fatalf("RequestJob failed: %v", err)
	}
	if !resp.HasLease || resp.Lease == nil {
		t.Fatalf("expected a lease for a job with a granted secret")
	}
	leaseID := resp.Lease.LeaseId

	if got := h.service.secrets.get(leaseID); len(got) == 0 {
		t.Fatalf("expected the lease secret cache to hold the resolved secret value before reaping")
	}

	lease, err := h.store.GetWorkerLeaseByID(context.Background(), leaseID)
	if err != nil {
		t.Fatalf("failed to load lease: %v", err)
	}
	lease.LastHeartbeatAt = time.Now().Add(-leaseStaleAfter - time.Minute)

	h.service.reapStaleLeases(context.Background())

	reloaded, err := h.store.GetWorkerLeaseByID(context.Background(), leaseID)
	if err != nil {
		t.Fatalf("failed to reload lease: %v", err)
	}
	if reloaded.ReleasedAt == nil {
		t.Fatalf("expected the stale lease to be released by the reaper")
	}
	if reloaded.Outcome != "reaped:stale" {
		t.Fatalf("Outcome = %q, want reaped:stale", reloaded.Outcome)
	}
	if got := h.service.secrets.get(leaseID); len(got) != 0 {
		t.Fatalf("expected the lease secret cache to be dropped after reaping, got %v", got)
	}

	reloadedJob, err := h.store.GetJobByID(context.Background(), job.JobID)
	if err != nil {
		t.Fatalf("failed to reload job: %v", err)
	}
	if reloadedJob.Status != "running" {
		t.Fatalf("expected the reaper to leave the job's own status untouched, got %q", reloadedJob.Status)
	}
}

// TestReapStaleLeases_LeavesFreshLeasesAlone verifies a just-acquired lease
// (well within leaseStaleAfter) is left open by a reaper pass.
func TestReapStaleLeases_LeavesFreshLeasesAlone(t *testing.T) {
	h := newTestHarness()
	_, token := seedQueueAndJobWithSecretRef(t, h, true)

	resp, err := h.service.RequestJob(ctxWithAuth(token), csilapi.RequestJobRequest{
		WorkerCharacteristics: csilapi.WorkerCharacteristics{Os: "linux", Arch: "amd64"},
	})
	if err != nil {
		t.Fatalf("RequestJob failed: %v", err)
	}
	if !resp.HasLease || resp.Lease == nil {
		t.Fatalf("expected a lease")
	}

	h.service.reapStaleLeases(context.Background())

	reloaded, err := h.store.GetWorkerLeaseByID(context.Background(), resp.Lease.LeaseId)
	if err != nil {
		t.Fatalf("failed to reload lease: %v", err)
	}
	if reloaded.ReleasedAt != nil {
		t.Fatalf("expected a fresh lease to remain open after a reaper pass, got released=%v outcome=%q", reloaded.ReleasedAt, reloaded.Outcome)
	}
}
