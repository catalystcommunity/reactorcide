package workerapi

import (
	"context"
	"sort"
	"testing"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/corndogs"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/secrets"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/workerapi/csilapi"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/workerauth"
)

// admissionFakeStore adds store.AdmissionStore to the in-memory fakeStore.
type admissionFakeStore struct {
	*fakeStore
	admissions map[string]*models.WorkerAdmission
	writes     int
}

func (a *admissionFakeStore) ListWaitingQueueHeads(_ context.Context, queues []string) ([]models.WaitingJob, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	wanted := map[string]bool{}
	for _, q := range queues {
		wanted[q] = true
	}
	var waiting []models.WaitingJob
	for _, job := range a.jobs {
		if (job.Status == "submitted" || job.Status == "queued") && wanted[job.QueueName] {
			waiting = append(waiting, models.WaitingJob{
				JobID: job.JobID, QueueName: job.QueueName, Priority: job.Priority, CreatedAt: job.CreatedAt,
				ResourceCPURequest: job.ResourceCPURequest, ResourceMemoryLimit: job.ResourceMemoryLimit,
			})
		}
	}
	sort.Slice(waiting, func(i, j int) bool {
		if waiting[i].Priority != waiting[j].Priority {
			return waiting[i].Priority > waiting[j].Priority
		}
		return waiting[i].CreatedAt.Before(waiting[j].CreatedAt)
	})
	seen := map[string]bool{}
	var heads []models.WaitingJob
	for _, w := range waiting {
		if !seen[w.QueueName] {
			seen[w.QueueName] = true
			heads = append(heads, w)
		}
	}
	return heads, nil
}

func (a *admissionFakeStore) ListWaitingJobGroups(context.Context) ([]models.WaitingJobGroup, error) {
	return nil, nil
}

func (a *admissionFakeStore) UpdateWorkerAdmission(_ context.Context, workerID string, report *models.WorkerAdmission) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.writes++
	a.admissions[workerID] = report
	if w, ok := a.workers[workerID]; ok {
		w.Admission = report
	}
	return nil
}

func newAdmissionHarness() (*testHarness, *admissionFakeStore) {
	h := newTestHarness()
	st := &admissionFakeStore{fakeStore: h.store, admissions: map[string]*models.WorkerAdmission{}}
	deps := NewDeps(st, h.corndogs, workerauth.NewEnrollment(st), workerauth.NewWorkerSessions(st), nil, h.objectStore, nil)
	deps.SecretsProvider = func(ctx context.Context, orgID string) (secrets.Provider, error) {
		return h.secretsProvider, nil
	}
	h.deps = deps
	h.service = NewWorkerService(deps)
	return h, st
}

func resourcesReport(cpu, memory int64) *csilapi.AdmissionReport {
	return &csilapi.AdmissionReport{
		Mode:                  "resources",
		CapacityCpuMillicores: 8000, CapacityMemoryBytes: 32 << 30,
		HeadroomCpuMillicores: cpu, HeadroomMemoryBytes: memory,
	}
}

func TestRequestJobResourcesModeSkipsHeadThatDoesNotFit(t *testing.T) {
	h, st := newAdmissionHarness()
	ctx := context.Background()
	chars := mustCharacteristics(t, map[string]any{"os": "linux"})
	const bigQueue, smallQueue = "33333333-3333-3333-3333-333333333331", "33333333-3333-3333-3333-333333333332"
	h.store.seedQueue(models.Queue{QueueUUID: bigQueue, Characteristics: chars})
	h.store.seedQueue(models.Queue{QueueUUID: smallQueue, Characteristics: mustCharacteristics(t, map[string]any{"os": "linux", "arch": "amd64"})})

	now := time.Now().UTC()
	big := &models.Job{Name: "big", JobCommand: "true", QueueName: bigQueue, Priority: 10, CreatedAt: now.Add(-time.Minute),
		ResourceCPURequest: "4", ResourceMemoryLimit: "8Gi"}
	small := &models.Job{Name: "small", JobCommand: "true", QueueName: smallQueue, CreatedAt: now,
		ResourceCPURequest: "1", ResourceMemoryLimit: "2Gi"}
	h.store.seedJob(big)
	h.store.seedJob(small)
	if _, err := h.corndogs.SubmitTaskToQueue(ctx, bigQueue, &corndogs.TaskPayload{JobID: big.JobID}, 10); err != nil {
		t.Fatal(err)
	}
	if _, err := h.corndogs.SubmitTaskToQueue(ctx, smallQueue, &corndogs.TaskPayload{JobID: small.JobID}, 0); err != nil {
		t.Fatal(err)
	}

	token, workerID := h.registerWorker(t, "res-worker", "linux", "amd64", nil)
	resp, err := h.service.RequestJob(ctxWithAuth(token), csilapi.RequestJobRequest{
		WorkerCharacteristics: csilapi.WorkerCharacteristics{Os: "linux", Arch: "amd64"},
		Admission:             resourcesReport(2000, 4<<30),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !resp.HasLease || resp.Lease.JobId != small.JobID {
		t.Fatalf("expected the small job that fits, got %+v", resp.Lease)
	}
	stored := st.admissions[workerID]
	if stored == nil || stored.HeadroomCPUMillicores != 2000 || stored.CapacityMemoryBytes != 32<<30 {
		t.Fatalf("stored admission = %+v", stored)
	}

	// A second call in the report interval with the same capacity does not
	// write again.
	writes := st.writes
	resp, err = h.service.RequestJob(ctxWithAuth(token), csilapi.RequestJobRequest{
		WorkerCharacteristics: csilapi.WorkerCharacteristics{Os: "linux", Arch: "amd64"},
		Admission:             resourcesReport(1000, 2<<30),
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.HasLease {
		t.Fatalf("the big job does not fit and must not be offered: %+v", resp.Lease)
	}
	if st.writes != writes {
		t.Fatal("admission report stored again inside the report interval")
	}

	// A worker with enough headroom gets the big job.
	token2, _ := h.registerWorker(t, "big-worker", "linux", "amd64", nil)
	resp, err = h.service.RequestJob(ctxWithAuth(token2), csilapi.RequestJobRequest{
		WorkerCharacteristics: csilapi.WorkerCharacteristics{Os: "linux", Arch: "amd64"},
		Admission:             resourcesReport(6000, 16<<30),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !resp.HasLease || resp.Lease.JobId != big.JobID {
		t.Fatalf("expected the big job, got %+v", resp.Lease)
	}
}

func TestRequestJobResourcesModeClaimTimeCheckRequeues(t *testing.T) {
	// The plain harness store has no AdmissionStore, so only the claim-time
	// check applies.
	h := newTestHarness()
	ctx := context.Background()
	const queue = "44444444-4444-4444-4444-444444444441"
	h.store.seedQueue(models.Queue{QueueUUID: queue, Characteristics: mustCharacteristics(t, map[string]any{"os": "linux"})})
	job := &models.Job{Name: "big", JobCommand: "true", ResourceCPURequest: "4", ResourceMemoryLimit: "8Gi"}
	h.store.seedJob(job)
	task, err := h.corndogs.SubmitTaskToQueue(ctx, queue, &corndogs.TaskPayload{JobID: job.JobID}, 0)
	if err != nil {
		t.Fatal(err)
	}
	token, _ := h.registerWorker(t, "small-worker", "linux", "amd64", nil)
	resp, err := h.service.RequestJob(ctxWithAuth(token), csilapi.RequestJobRequest{
		WorkerCharacteristics: csilapi.WorkerCharacteristics{Os: "linux", Arch: "amd64"},
		Admission:             resourcesReport(1000, 2<<30),
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.HasLease {
		t.Fatal("a job that does not fit must not be leased")
	}
	if task.CurrentState != "submitted" {
		t.Fatalf("task must go back to submitted, is %q", task.CurrentState)
	}
	saved, _ := h.store.GetJobByID(ctx, job.JobID)
	if saved.Status != "submitted" {
		t.Fatalf("job must stay submitted, is %q", saved.Status)
	}

	// A concurrency-mode worker (no report) gets it, as before.
	resp, err = h.service.RequestJob(ctxWithAuth(token), csilapi.RequestJobRequest{
		WorkerCharacteristics: csilapi.WorkerCharacteristics{Os: "linux", Arch: "amd64"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !resp.HasLease {
		t.Fatal("a concurrency-mode worker must get the job")
	}
}

func TestRequestJobResourcesModeReservesForStarvingHead(t *testing.T) {
	h, _ := newAdmissionHarness()
	previous := admissionReserveAfter
	admissionReserveAfter = 5 * time.Minute
	t.Cleanup(func() { admissionReserveAfter = previous })
	ctx := context.Background()
	const bigQueue, smallQueue = "55555555-5555-5555-5555-555555555551", "55555555-5555-5555-5555-555555555552"
	h.store.seedQueue(models.Queue{QueueUUID: bigQueue, Characteristics: mustCharacteristics(t, map[string]any{"os": "linux"})})
	h.store.seedQueue(models.Queue{QueueUUID: smallQueue, Characteristics: mustCharacteristics(t, map[string]any{"os": "linux", "arch": "amd64"})})
	now := time.Now().UTC()
	big := &models.Job{Name: "big", JobCommand: "true", QueueName: bigQueue, CreatedAt: now.Add(-time.Hour),
		ResourceCPURequest: "6", ResourceMemoryLimit: "8Gi"}
	small := &models.Job{Name: "small", JobCommand: "true", QueueName: smallQueue, CreatedAt: now,
		ResourceCPURequest: "1", ResourceMemoryLimit: "1Gi"}
	h.store.seedJob(big)
	h.store.seedJob(small)
	for _, j := range []*models.Job{big, small} {
		if _, err := h.corndogs.SubmitTaskToQueue(ctx, j.QueueName, &corndogs.TaskPayload{JobID: j.JobID}, 0); err != nil {
			t.Fatal(err)
		}
	}
	token, _ := h.registerWorker(t, "drain-worker", "linux", "amd64", nil)
	resp, err := h.service.RequestJob(ctxWithAuth(token), csilapi.RequestJobRequest{
		WorkerCharacteristics: csilapi.WorkerCharacteristics{Os: "linux", Arch: "amd64"},
		Admission:             resourcesReport(2000, 4<<30),
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.HasLease {
		t.Fatalf("the worker must reserve room for the starving job, not take %s", resp.Lease.JobId)
	}
}
