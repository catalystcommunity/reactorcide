package workerapi

import (
	"context"
	"testing"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/corndogs"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/objects"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/secrets"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/uiapi"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/workerapi/csilapi"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/workerauth"
)

// testHarness bundles everything a workerapi test needs: an in-memory
// DataStore, an in-memory corndogs backend with real GetNextTaskGroup
// matching semantics (see fakeCorndogsBackend in fakes_test.go), an
// in-memory object store, and a swappable secrets provider -- no live
// Postgres/corndogs/object storage required.
type testHarness struct {
	store           *fakeStore
	corndogs        *corndogs.MockClient
	objectStore     objects.ObjectStore
	secretsProvider *fakeSecretsProvider
	deps            *Deps
	service         *WorkerService
}

func newTestHarness() *testHarness {
	st := newFakeStore()
	cd := newFakeCorndogsClient()
	objStore := objects.NewMemoryObjectStore()
	secretsProv := newFakeSecretsProvider()

	deps := NewDeps(st, cd, workerauth.NewEnrollment(st), workerauth.NewWorkerSessions(st), nil, objStore, nil)
	deps.SecretsProvider = func(ctx context.Context, orgID string) (secrets.Provider, error) {
		return secretsProv, nil
	}

	return &testHarness{
		store:           st,
		corndogs:        cd,
		objectStore:     objStore,
		secretsProvider: secretsProv,
		deps:            deps,
		service:         NewWorkerService(deps),
	}
}

// registerWorker enrolls a fresh worker via a seeded pool/token and returns
// its session token + worker ID, failing the test on any error.
func (h *testHarness) registerWorker(t *testing.T, workerKey, os, arch string, custom []csilapi.CustomCharacteristic) (sessionToken, workerID string) {
	t.Helper()
	pool := &models.WorkerPool{Name: "pool-" + workerKey}
	h.store.seedPool(pool)
	rawToken := "raw-token-" + workerKey
	h.store.seedEnrollmentToken(&models.PoolEnrollmentToken{
		PoolID:    pool.PoolID,
		TokenHash: rawTokenHash(rawToken),
		IsActive:  true,
	})

	resp, err := h.service.Register(context.Background(), csilapi.RegisterRequest{
		EnrollmentToken: rawToken,
		WorkerInfo: csilapi.WorkerInfo{
			WorkerKey: workerKey,
			Os:        os,
			Arch:      arch,
			Custom:    custom,
		},
	})
	if err != nil {
		t.Fatalf("Register failed: %v", err)
	}
	return resp.WorkerSession, resp.WorkerId
}

// ctxWithAuth builds a context carrying the CSIL-RPC envelope's auth field,
// exactly like uiapi.Handler.ServeHTTP does for a real request.
func ctxWithAuth(token string) context.Context {
	return uiapi.WithAuthToken(context.Background(), token)
}
