package workerapi

import (
	"context"
	"sync"
	"testing"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/workerapi/csilapi"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/workerauth"
)

// outageStore fails session and lease lookups with a connection error while
// down is set, like Postgres during its restart after an OOM kill.
type outageStore struct {
	*fakeStore
	mu   sync.Mutex
	down bool
}

func (o *outageStore) setDown(down bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.down = down
}

func (o *outageStore) isDown() bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.down
}

func (o *outageStore) GetActiveWorkerSessionByTokenHash(ctx context.Context, tokenHash []byte) (*models.WorkerSession, *models.Worker, error) {
	if o.isDown() {
		return nil, nil, errConnRefused
	}
	return o.fakeStore.GetActiveWorkerSessionByTokenHash(ctx, tokenHash)
}

func (o *outageStore) GetWorkerLeaseByID(ctx context.Context, leaseID string) (*models.WorkerLease, error) {
	if o.isDown() {
		return nil, errConnRefused
	}
	return o.fakeStore.GetWorkerLeaseByID(ctx, leaseID)
}

// TestReportResultDuringStoreOutage: a session lookup that fails because the
// store is down is "unavailable" (retryable), never "unauthorized". After the
// outage, the retried report completes the job.
func TestReportResultDuringStoreOutage(t *testing.T) {
	h := newTestHarness()
	job, leaseID, token := claimOneJob(t, h)

	outage := &outageStore{fakeStore: h.store}
	h.deps.Store = outage
	h.deps.Sessions = workerauth.NewWorkerSessions(outage)
	outage.setDown(true)

	req := csilapi.ReportResultRequest{LeaseId: leaseID, ExitCode: 0, Status: "completed"}
	_, err := h.service.ReportResult(ctxWithAuth(token), req)
	assertServiceErrorCode(t, err, "unavailable")

	_, err = h.service.RequestJob(ctxWithAuth(token), csilapi.RequestJobRequest{
		WorkerCharacteristics: csilapi.WorkerCharacteristics{Os: "linux", Arch: "amd64"},
	})
	assertServiceErrorCode(t, err, "unavailable")

	outage.setDown(false)
	if _, err := h.service.ReportResult(ctxWithAuth(token), req); err != nil {
		t.Fatalf("ReportResult after the outage failed: %v", err)
	}
	saved, _ := h.store.GetJobByID(context.Background(), job.JobID)
	if saved.Status != "completed" {
		t.Fatalf("job after retried report = %q, want completed", saved.Status)
	}
}

func TestReportResultLeaseLookupOutageIsUnavailable(t *testing.T) {
	h := newTestHarness()
	_, leaseID, token := claimOneJob(t, h)

	outage := &outageStore{fakeStore: h.store}
	h.deps.Store = outage
	// Sessions still resolve; only the lease lookup fails.
	outage.setDown(true)
	h.deps.Sessions = workerauth.NewWorkerSessions(h.store)

	_, err := h.service.ReportResult(ctxWithAuth(token), csilapi.ReportResultRequest{LeaseId: leaseID, Status: "completed"})
	assertServiceErrorCode(t, err, "unavailable")
}

func TestUnknownSessionStillUnauthorized(t *testing.T) {
	h := newTestHarness()
	_, err := h.service.ReportResult(ctxWithAuth("never-issued"), csilapi.ReportResultRequest{LeaseId: "x"})
	assertServiceErrorCode(t, err, "unauthorized")
}

// TestReportResultForSupersededLease: the reconciler requeued the job after
// its lease went stale. A late report from the old worker must not finish
// the job's new corndogs task.
func TestReportResultForSupersededLease(t *testing.T) {
	h := newTestHarness()
	job, leaseID, token := claimOneJob(t, h)

	// Reconciler outcome: job back at submitted with a replacement task.
	newTask := "99999999-9999-9999-9999-999999999999"
	if _, _, err := h.store.UpdateJobStatusGuarded(context.Background(), job.JobID, []string{"running"}, func(j *models.Job) {
		j.Status = "submitted"
		j.WorkerID = nil
		j.CorndogsTaskID = &newTask
	}); err != nil {
		t.Fatal(err)
	}
	callsBefore := len(h.corndogs.CompleteTaskCalls)

	resp, err := h.service.ReportResult(ctxWithAuth(token), csilapi.ReportResultRequest{LeaseId: leaseID, ExitCode: 0, Status: "completed"})
	if err != nil || !resp.Ok {
		t.Fatalf("late ReportResult = %+v, %v; want ok", resp, err)
	}
	if len(h.corndogs.CompleteTaskCalls) != callsBefore {
		t.Fatalf("late report must not complete the replacement task")
	}
	saved, _ := h.store.GetJobByID(context.Background(), job.JobID)
	if saved.Status != "submitted" {
		t.Fatalf("job = %q, want submitted", saved.Status)
	}
	lease, _ := h.store.GetWorkerLeaseByID(context.Background(), leaseID)
	if lease.ReleasedAt == nil || lease.Outcome != "superseded" {
		t.Fatalf("stale lease must be released as superseded; got %+v", lease)
	}
}
