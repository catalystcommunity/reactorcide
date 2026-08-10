package test

// End-to-end proof for the coordinator-mediated worker run loop: the REAL
// internal/coordinatorworker.Run loop, talking real CSIL-RPC/HTTP to the REAL
// router-mounted dispatcher (handlers.GetAppMuxWithClient ->
// uiapi.NewHandlerWithWorker) served over an httptest.Server, against real
// Postgres (this package's shared testcontainers instance) and the REAL
// Docker JobRunner backend -- Register -> RequestJob -> spawn a real "alpine"
// container -> stream its real stdout back via AppendLogs -> ReportResult ->
// job row completed with its logs in object storage. The CSIL-RPC protocol
// has separate coverage with a fake Client.
//
// Skips (not fails) if Docker is unavailable in the environment running the
// test, mirroring internal/worker/docker_runner_integration_test.go's own
// guard.

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/characteristics"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/coordinatorworker"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/corndogs"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/handlers"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/jobtelemetry"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/objects"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/secrets"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/postgres_store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/worker"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/workerauth"
	"github.com/stretchr/testify/require"
)

func TestWorkerRunLoopDockerIntegration(t *testing.T) {
	ctx := context.Background()

	// Guard: skip (not fail) when Docker isn't reachable in this environment,
	// exactly like internal/worker's own Docker integration tests.
	if _, err := worker.NewDockerRunner(); err != nil {
		t.Skipf("Docker not available: %v", err)
	}

	// Master keys must exist before the app mux is built, same reasoning as
	// TestWorkerProtocolIntegration: router.go's own singleton key manager
	// must find already-persisted key material. This job carries no secrets,
	// but resolveJobSecrets still runs over its (empty) env.
	_, err := secrets.LoadOrCreateMasterKeys(testDB)
	require.NoError(t, err)

	handlers.ResetAppMux()
	defer handlers.ResetAppMux()
	memStore := objects.NewMemoryObjectStore()
	handlers.SetObjectStore(memStore)
	defer handlers.SetObjectStore(nil)

	mockCorndogs := newIntegrationCorndogsClient()
	mux := handlers.GetAppMuxWithClient(mockCorndogs)

	server := httptest.NewServer(mux)
	defer server.Close()

	// --- default queue + pool + enrollment token ---------------------------

	require.NoError(t, postgres_store.PostgresStore.EnsureDefaultQueue(ctx))
	defaultChars, err := characteristics.ParseJobCharacteristics(map[string]any{})
	require.NoError(t, err)
	defaultQueue, err := postgres_store.PostgresStore.FindOrCreateQueueByCharacteristics(ctx, defaultChars)
	require.NoError(t, err)

	wpool := &models.WorkerPool{Name: uniqueName("worker-run-loop-pool")}
	require.NoError(t, postgres_store.PostgresStore.CreateWorkerPool(ctx, wpool))
	grantDefaultOrgPool(t, ctx, wpool.PoolID)
	rawToken, tokenHash, err := workerauth.GenerateEnrollmentToken()
	require.NoError(t, err)
	_, err = postgres_store.PostgresStore.CreatePoolEnrollmentToken(ctx, wpool.PoolID, "primary", tokenHash)
	require.NoError(t, err)

	// --- seed a simple job on the default queue -----------------------------

	du := &DataUtils{db: testDB}
	job, err := du.CreateJob(DataSetup{
		"Name":           "worker-run-loop-job",
		"JobCommand":     "sh -c 'echo hello; echo done'",
		"Status":         "submitted",
		"ContainerImage": "alpine:latest",
		"OrgID":          defaultQueue.OrgID,
		"QueueName":      defaultQueue.QueueUUID,
	})
	require.NoError(t, err)

	if _, err := mockCorndogs.SubmitTaskToQueue(ctx, defaultQueue.QueueUUID, &corndogs.TaskPayload{JobID: job.JobID}, 0); err != nil {
		t.Fatalf("failed to submit corndogs task: %v", err)
	}

	// --- run ONE iteration of the real coordinator-mediated worker loop -----

	cfg := coordinatorworker.Config{
		CoordinatorURL:   server.URL,
		EnrollmentToken:  rawToken,
		WorkerKey:        uniqueName("worker-key"),
		OS:               "linux",
		Arch:             "amd64",
		ContainerRuntime: "docker",
		Concurrency:      1,
	}

	runCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- coordinatorworker.Run(runCtx, cfg) }()

	require.Eventually(t, func() bool {
		j, err := postgres_store.PostgresStore.GetJobByID(ctx, job.JobID)
		return err == nil && (j.Status == "completed" || j.Status == "failed")
	}, 60*time.Second, 250*time.Millisecond, "expected the job to reach a terminal status")

	cancel()
	<-done // Run returns ctx.Err() once the loop observes cancellation; the
	// point of this test is the job's/logs' state, asserted below, not Run's
	// own return value.

	// --- assert: job completed, exit code 0, real container stdout landed in
	// object storage exactly like a pull-based worker's logs would ------

	finalJob, err := postgres_store.PostgresStore.GetJobByID(ctx, job.JobID)
	require.NoError(t, err)
	require.Equal(t, "completed", finalJob.Status, "last error: %s", finalJob.LastError)
	require.NotNil(t, finalJob.ExitCode)
	require.Equal(t, 0, *finalJob.ExitCode)

	require.Eventually(t, func() bool {
		entries, err := jobtelemetry.ReadLogEntries(ctx, memStore, job.JobID, "stdout")
		return err == nil && len(entries) > 0 && entries[0].Message == "hello"
	}, 15*time.Second, 200*time.Millisecond, "expected the real container's stdout to be shipped to immutable telemetry storage")
}
