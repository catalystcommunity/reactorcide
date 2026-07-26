package workerapi

import (
	"context"
	"testing"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/workerapi/csilapi"
)

func TestRegister_Happy(t *testing.T) {
	h := newTestHarness()

	token, workerID := h.registerWorker(t, "host-guid-1", "linux", "amd64", []csilapi.CustomCharacteristic{
		{Key: "gpu", Value: "true"},
	})
	if token == "" {
		t.Fatal("expected a non-empty worker session token")
	}
	if workerID == "" {
		t.Fatal("expected a non-empty worker id")
	}

	saved, err := h.store.GetWorkerByID(context.Background(), workerID)
	if err != nil {
		t.Fatalf("worker not persisted: %v", err)
	}
	if saved.WorkerKey != "host-guid-1" || saved.OS != "linux" || saved.Arch != "amd64" {
		t.Fatalf("unexpected worker row: %+v", saved)
	}
	if saved.Status != models.WorkerStatusActive {
		t.Fatalf("expected new worker to default to active status, got %q", saved.Status)
	}

	// Re-registering the same worker_key must not mint a second worker row.
	_, workerID2 := h.registerWorker(t, "host-guid-1", "linux", "amd64", nil)
	if workerID2 != workerID {
		t.Fatalf("re-register minted a new worker id: %s != %s", workerID2, workerID)
	}
}

func TestRegister_PreservesQuarantineOnReRegister(t *testing.T) {
	h := newTestHarness()
	_, workerID := h.registerWorker(t, "host-guid-2", "linux", "amd64", nil)

	if err := h.store.UpdateWorkerStatus(context.Background(), workerID, models.WorkerStatusQuarantined); err != nil {
		t.Fatalf("failed to quarantine worker: %v", err)
	}

	h.registerWorker(t, "host-guid-2", "linux", "amd64", nil)

	saved, err := h.store.GetWorkerByID(context.Background(), workerID)
	if err != nil {
		t.Fatalf("worker not found: %v", err)
	}
	if saved.Status != models.WorkerStatusQuarantined {
		t.Fatalf("Register must not silently clear an admin quarantine; got status %q", saved.Status)
	}
}

func TestRegister_BadToken(t *testing.T) {
	h := newTestHarness()

	_, err := h.service.Register(context.Background(), csilapi.RegisterRequest{
		EnrollmentToken: "not-a-real-token",
		WorkerInfo: csilapi.WorkerInfo{
			WorkerKey: "host-guid-3",
			Os:        "linux",
			Arch:      "amd64",
		},
	})
	assertServiceErrorCode(t, err, "unauthorized")
}

func TestRegister_EmptyToken(t *testing.T) {
	h := newTestHarness()

	_, err := h.service.Register(context.Background(), csilapi.RegisterRequest{
		EnrollmentToken: "",
		WorkerInfo: csilapi.WorkerInfo{
			WorkerKey: "host-guid-4",
			Os:        "linux",
			Arch:      "amd64",
		},
	})
	assertServiceErrorCode(t, err, "unauthorized")
}

func TestRegister_MissingWorkerKey(t *testing.T) {
	h := newTestHarness()
	pool := &models.WorkerPool{Name: "pool"}
	h.store.seedPool(pool)
	rawToken := "raw-token"
	h.store.seedEnrollmentToken(&models.PoolEnrollmentToken{
		PoolID:    pool.PoolID,
		TokenHash: rawTokenHash(rawToken),
		IsActive:  true,
	})

	_, err := h.service.Register(context.Background(), csilapi.RegisterRequest{
		EnrollmentToken: rawToken,
		WorkerInfo:      csilapi.WorkerInfo{Os: "linux", Arch: "amd64"},
	})
	assertServiceErrorCode(t, err, "invalid_argument")
}
