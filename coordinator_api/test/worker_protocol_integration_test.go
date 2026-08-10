package test

// End-to-end coverage for the coordinator-mediated worker protocol: Register
// -> RequestJob -> Heartbeat -> AppendLogs -> ReportResult driven over the
// REAL, router-mounted CSIL-RPC dispatcher (handlers.GetAppMuxWithClient ->
// uiapi.NewHandlerWithWorker) via net/http/httptest, against real Postgres
// (this package's shared testcontainers instance, see setup_test.go) and a
// real internal/pubsub NOTIFY round trip, using an in-memory object store and
// an in-memory corndogs backend (this package has no live corndogs server to
// test against, so this fake -- built on corndogs.MockClient's Func hooks,
// mirroring internal/workerapi's own equivalent test fake -- exercises the
// real corndogs.Client.GetNextTaskGroup/UpdateTask/etc. wrapper methods
// against real claim/state-transition semantics rather than a canned
// response).
//
// SECURITY: this file asserts (not just documents) that a resolved secret
// value never appears in the corndogs task payload and never appears in the
// RequestJob response's `env` field, only in its dedicated `secrets` field.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	pb "github.com/catalystcommunity/reactorcide/coordinator_api/internal/corndogs/v1alpha1"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/characteristics"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/corndogs"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/handlers"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/jobtelemetry"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/objects"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/pubsub"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/secrets"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/postgres_store"
	workercsilapi "github.com/catalystcommunity/reactorcide/coordinator_api/internal/workerapi/csilapi"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/workerauth"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
)

// --- in-memory corndogs backend (real claim semantics) ---------------------

type integrationCorndogsBackend struct {
	mu    sync.Mutex
	tasks map[string]*pb.Task
}

func newIntegrationCorndogsClient() *corndogs.MockClient {
	backend := &integrationCorndogsBackend{tasks: map[string]*pb.Task{}}
	mc := corndogs.NewMockClient()

	mc.SubmitTaskToQueueFunc = func(ctx context.Context, queue string, payload *corndogs.TaskPayload, priority int64) (*pb.Task, error) {
		backend.mu.Lock()
		defer backend.mu.Unlock()
		payloadBytes, _ := json.Marshal(payload)
		t := &pb.Task{Uuid: uuid.NewString(), Queue: queue, CurrentState: "submitted", Payload: payloadBytes, Priority: priority}
		backend.tasks[t.Uuid] = t
		return t, nil
	}
	mc.GetNextTaskGroupFunc = func(ctx context.Context, queues []string, currentState string, timeout int64) (*pb.Task, error) {
		backend.mu.Lock()
		defer backend.mu.Unlock()
		inGroup := make(map[string]bool, len(queues))
		for _, q := range queues {
			inGroup[q] = true
		}
		var best *pb.Task
		for _, t := range backend.tasks {
			if t.CurrentState == currentState && inGroup[t.Queue] {
				best = t
				break
			}
		}
		if best == nil {
			return nil, nil
		}
		best.CurrentState = "claimed"
		return best, nil
	}
	mc.UpdateTaskFunc = func(ctx context.Context, taskID, currentState, newState string, payload []byte) (*pb.Task, error) {
		backend.mu.Lock()
		defer backend.mu.Unlock()
		t, ok := backend.tasks[taskID]
		if !ok {
			return nil, fmt.Errorf("task not found")
		}
		t.CurrentState = newState
		return t, nil
	}
	mc.CompleteTaskFunc = func(ctx context.Context, taskID, currentState string) (*pb.Task, error) {
		backend.mu.Lock()
		defer backend.mu.Unlock()
		t, ok := backend.tasks[taskID]
		if !ok {
			return nil, fmt.Errorf("task not found")
		}
		t.CurrentState = "completed"
		return t, nil
	}
	mc.CancelTaskFunc = func(ctx context.Context, taskID, currentState string) (*pb.Task, error) {
		backend.mu.Lock()
		defer backend.mu.Unlock()
		t, ok := backend.tasks[taskID]
		if !ok {
			return nil, fmt.Errorf("task not found")
		}
		t.CurrentState = "cancelled"
		return t, nil
	}
	mc.SendHeartbeatFunc = func(ctx context.Context, taskID, currentState string, ext int64) (*pb.Task, error) {
		backend.mu.Lock()
		defer backend.mu.Unlock()
		t, ok := backend.tasks[taskID]
		if !ok {
			return nil, fmt.Errorf("task not found")
		}
		return t, nil
	}
	return mc
}

func grantDefaultOrgPool(t *testing.T, ctx context.Context, poolID string) {
	t.Helper()
	organization, err := postgres_store.PostgresStore.GetDefaultOrganization(ctx)
	require.NoError(t, err)
	class, err := postgres_store.PostgresStore.GetWorkerClass(ctx, organization.OrgID, "default")
	require.NoError(t, err)
	require.NoError(t, postgres_store.PostgresStore.GrantWorkerClassPool(ctx, class.ClassID, poolID))
}

func ensureTestOrgKey(t *testing.T, keyMgr *secrets.MasterKeyManager, orgID string) []byte {
	t.Helper()
	orgKey, err := keyMgr.GetOrgEncryptionKey(testDB, orgID)
	if errors.Is(err, secrets.ErrNotInitialized) {
		require.NoError(t, keyMgr.InitializeOrgSecrets(testDB, orgID))
		orgKey, err = keyMgr.GetOrgEncryptionKey(testDB, orgID)
	}
	require.NoError(t, err)
	return orgKey
}

func TestWorkerProtocolIntegration(t *testing.T) {
	ctx := context.Background()

	// --- master keys + org secrets, set up BEFORE the app mux is built so
	// router.go's own singletonKeyManager (LoadOrCreateMasterKeys) finds the
	// same already-persisted key material rather than generating a second,
	// different key. ---
	keyMgr, err := secrets.LoadOrCreateMasterKeys(testDB)
	require.NoError(t, err)

	handlers.ResetAppMux()
	defer handlers.ResetAppMux()
	memStore := objects.NewMemoryObjectStore()
	handlers.SetObjectStore(memStore)
	defer handlers.SetObjectStore(nil)

	mockCorndogs := newIntegrationCorndogsClient()
	mux := handlers.GetAppMuxWithClient(mockCorndogs)

	// Real NOTIFY round trip: subscribe on a local bus fed by a
	// NotifyListener on the same pgx pool the coordinator's Publisher writes
	// through (handlers.buildWorkerAPIDeps wires
	// pubsub.NewPublisher(postgres_store.PgxPool())).
	pool := postgres_store.PgxPool()
	require.NotNil(t, pool, "worker protocol integration test requires a live pgx pool")
	bus := pubsub.NewBus(logrus.StandardLogger(), 64)
	listener := pubsub.NewNotifyListener(pool, bus, logrus.StandardLogger())
	bus.SetJobTopicController(listener)
	listenCtx, cancelListen := context.WithCancel(ctx)
	defer cancelListen()
	listener.Start(listenCtx)
	time.Sleep(200 * time.Millisecond) // let LISTEN subscribe before we publish

	// --- seed pool + enrollment token + queue + job ------------------------

	wpool := &models.WorkerPool{Name: uniqueName("worker-pool")}
	require.NoError(t, postgres_store.PostgresStore.CreateWorkerPool(ctx, wpool))
	grantDefaultOrgPool(t, ctx, wpool.PoolID)
	rawToken, tokenHash, err := workerauth.GenerateEnrollmentToken()
	require.NoError(t, err)
	_, err = postgres_store.PostgresStore.CreatePoolEnrollmentToken(ctx, wpool.PoolID, "primary", tokenHash)
	require.NoError(t, err)

	// A unique marker characteristic keeps this queue's hash from colliding
	// with the shared {"os":"linux"} default queue (or another test's own
	// linux queue) in this long-lived test container -- queue characteristics
	// are globally unique by hash (characteristics_hash), not
	// per-test-scoped.
	marker := uniqueName("marker")
	chars, err := characteristics.ParseJobCharacteristics(map[string]any{"os": "linux", "integration_marker": marker})
	require.NoError(t, err)
	queue, err := postgres_store.PostgresStore.CreateQueue(ctx, chars, uniqueName("queue"))
	require.NoError(t, err)

	du := &DataUtils{db: testDB}
	job, err := du.CreateJob(DataSetup{
		"Name":       "worker-protocol-job",
		"JobCommand": "true",
		"Status":     "submitted",
		"OrgID":      queue.OrgID,
		"QueueName":  queue.QueueUUID,
		"JobEnvVars": models.JSONB{
			"API_KEY": "${secret:integration/creds:api_key}",
			"PLAIN":   "not-a-secret",
		},
	})
	require.NoError(t, err)

	require.NoError(t, testDB.Create(&models.SecretGrant{
		UserID:            job.OrgID,
		Name:              "integration-grant",
		SecretPathMatch:   models.SecretGrantMatchPrefix,
		SecretPathPattern: "integration/creds",
		JobNameMatch:      models.SecretGrantMatchAny,
	}).Error)
	orgKey := ensureTestOrgKey(t, keyMgr, job.OrgID)
	provider, err := secrets.NewDatabaseProvider(testDB, job.OrgID, orgKey)
	require.NoError(t, err)
	require.NoError(t, provider.Set(ctx, "integration/creds", "api_key", "sooper-seekrit-value"))

	if _, err := mockCorndogs.SubmitTaskToQueue(ctx, queue.QueueUUID, &corndogs.TaskPayload{JobID: job.JobID}, 0); err != nil {
		t.Fatalf("failed to submit corndogs task: %v", err)
	}

	// --- Register: bad token is rejected -----------------------------------

	_, svcErr := csilCall(t, mux, "ReactorcideWorker", "register",
		workercsilapi.EncodeRegisterRequest, workercsilapi.DecodeRegisterResponse,
		workercsilapi.RegisterRequest{
			EnrollmentToken: "not-a-real-token",
			WorkerInfo:      workercsilapi.WorkerInfo{WorkerKey: uniqueName("worker"), Os: "linux", Arch: "amd64"},
		}, "")
	require.NotNil(t, svcErr, "a bad enrollment token must be rejected")
	require.Equal(t, "unauthorized", svcErr.Code)

	// --- Register: happy path -----------------------------------------------

	regResp, svcErr := csilCall(t, mux, "ReactorcideWorker", "register",
		workercsilapi.EncodeRegisterRequest, workercsilapi.DecodeRegisterResponse,
		workercsilapi.RegisterRequest{
			EnrollmentToken: rawToken,
			WorkerInfo:      workercsilapi.WorkerInfo{WorkerKey: uniqueName("worker"), Os: "linux", Arch: "amd64"},
		}, "")
	require.Nil(t, svcErr)
	require.NotEmpty(t, regResp.WorkerSession)
	require.NotEmpty(t, regResp.WorkerId)

	sessionToken := regResp.WorkerSession

	// --- RequestJob: claims the seeded task, secrets isolated --------------

	jobSub := bus.SubscribeJob(job.JobID)
	defer bus.Unsubscribe(jobSub)
	time.Sleep(1200 * time.Millisecond) // let the listener add the per-job channel

	reqResp, svcErr := csilCall(t, mux, "ReactorcideWorker", "request-job",
		workercsilapi.EncodeRequestJobRequest, workercsilapi.DecodeRequestJobResponse,
		workercsilapi.RequestJobRequest{WorkerCharacteristics: workercsilapi.WorkerCharacteristics{
			Os: "linux", Arch: "amd64",
			Custom: []workercsilapi.CustomCharacteristic{{Key: "integration_marker", Value: marker}},
		}},
		sessionToken)
	require.Nil(t, svcErr)
	require.True(t, reqResp.HasLease, "worker satisfies the seeded queue and must claim its task")
	require.NotNil(t, reqResp.Lease)
	require.Equal(t, job.JobID, reqResp.Lease.JobId)

	var sawAPIKeyInEnv bool
	var secretValue string
	for _, e := range reqResp.Lease.Env {
		if e.Key == "API_KEY" {
			sawAPIKeyInEnv = true
		}
	}
	for _, e := range reqResp.Lease.Secrets {
		if e.Key == "API_KEY" {
			secretValue = e.Value
		}
	}
	require.False(t, sawAPIKeyInEnv, "secret-bearing env var must not appear in Lease.Env")
	require.Equal(t, "sooper-seekrit-value", secretValue, "Lease.Secrets must carry the resolved value")

	// Assert the secret never touched the corndogs task payload.
	for _, call := range mockCorndogs.SubmitTaskToQueueCalls {
		payloadBytes, _ := json.Marshal(call.Payload)
		require.False(t, strings.Contains(string(payloadBytes), "sooper-seekrit-value"), "corndogs payload leaked a secret")
	}
	for _, call := range mockCorndogs.UpdateTaskCalls {
		require.False(t, strings.Contains(string(call.Payload), "sooper-seekrit-value"), "corndogs UpdateTask payload leaked a secret")
	}

	waitForEvent(t, jobSub, func(e pubsub.Event) bool { return e.Type == pubsub.EventJobUpdate && e.Status == "running" })

	// --- Heartbeat: extends the task, no directives yet --------------------

	hbResp, svcErr := csilCall(t, mux, "ReactorcideWorker", "heartbeat",
		workercsilapi.EncodeHeartbeatRequest, workercsilapi.DecodeHeartbeatResponse,
		workercsilapi.HeartbeatRequest{Status: "running", RunningLeases: []workercsilapi.RunningLease{{LeaseId: reqResp.Lease.LeaseId}}},
		sessionToken)
	require.Nil(t, svcErr)
	require.Empty(t, hbResp.Directives)

	// --- AppendLogs: writes the expected object key + fires NOTIFY ---------

	_, svcErr = csilCall(t, mux, "ReactorcideWorker", "append-logs",
		workercsilapi.EncodeAppendLogsRequest, workercsilapi.DecodeAppendLogsResponse,
		workercsilapi.AppendLogsRequest{LeaseId: reqResp.Lease.LeaseId, Stream: "stdout", Chunk: "hello from worker\n"},
		sessionToken)
	require.Nil(t, svcErr)

	waitForEvent(t, jobSub, func(e pubsub.Event) bool { return e.Type == pubsub.EventLogAvailable && e.Stream == "stdout" })

	entries, err := jobtelemetry.ReadLogEntries(ctx, memStore, job.JobID, "stdout")
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Equal(t, "hello from worker", entries[0].Message)

	// --- ReportResult: finalizes the job + releases the lease + NOTIFY -----

	rrResp, svcErr := csilCall(t, mux, "ReactorcideWorker", "report-result",
		workercsilapi.EncodeReportResultRequest, workercsilapi.DecodeReportResultResponse,
		workercsilapi.ReportResultRequest{LeaseId: reqResp.Lease.LeaseId, ExitCode: 0, Status: "completed"},
		sessionToken)
	require.Nil(t, svcErr)
	require.True(t, rrResp.Ok)

	waitForEvent(t, jobSub, func(e pubsub.Event) bool { return e.Type == pubsub.EventJobUpdate && e.Status == "completed" })

	finalJob, err := postgres_store.PostgresStore.GetJobByID(ctx, job.JobID)
	require.NoError(t, err)
	require.Equal(t, "completed", finalJob.Status)

	lease, err := postgres_store.PostgresStore.GetWorkerLeaseByID(ctx, reqResp.Lease.LeaseId)
	require.NoError(t, err)
	require.False(t, lease.IsActive())
}

// TestWorkerProtocolIntegration_VCSAuth verifies that a job whose project has
// a configured GitHub VCS credential gets that
// credential resolved into RequestJobResponse's Lease.vcs_auth over the REAL,
// router-mounted CSIL-RPC dispatcher against real Postgres -- no actual git
// clone needed, this only asserts the lease field is populated correctly and
// that the resolved token stays isolated from every other surface a worker or
// corndogs task ever sees (Env, Secrets, corndogs task payloads), exactly
// like the sibling job-secret isolation assertions above.
func TestWorkerProtocolIntegration_VCSAuth(t *testing.T) {
	ctx := context.Background()

	keyMgr, err := secrets.LoadOrCreateMasterKeys(testDB)
	require.NoError(t, err)

	handlers.ResetAppMux()
	defer handlers.ResetAppMux()
	memStore := objects.NewMemoryObjectStore()
	handlers.SetObjectStore(memStore)
	defer handlers.SetObjectStore(nil)

	mockCorndogs := newIntegrationCorndogsClient()
	mux := handlers.GetAppMuxWithClient(mockCorndogs)

	// --- seed pool + enrollment token + queue + project + job --------------

	wpool := &models.WorkerPool{Name: uniqueName("worker-pool-vcs")}
	require.NoError(t, postgres_store.PostgresStore.CreateWorkerPool(ctx, wpool))
	grantDefaultOrgPool(t, ctx, wpool.PoolID)
	rawToken, tokenHash, err := workerauth.GenerateEnrollmentToken()
	require.NoError(t, err)
	_, err = postgres_store.PostgresStore.CreatePoolEnrollmentToken(ctx, wpool.PoolID, "primary", tokenHash)
	require.NoError(t, err)

	marker := uniqueName("marker-vcs")
	chars, err := characteristics.ParseJobCharacteristics(map[string]any{"os": "linux", "integration_marker": marker})
	require.NoError(t, err)
	queue, err := postgres_store.PostgresStore.CreateQueue(ctx, chars, uniqueName("queue-vcs"))
	require.NoError(t, err)

	du := &DataUtils{db: testDB}
	job, err := du.CreateJob(DataSetup{
		"Name":       "worker-protocol-vcs-job",
		"JobCommand": "true",
		"Status":     "submitted",
		"SourceURL":  "https://github.com/example/private-repo.git",
		"OrgID":      queue.OrgID,
		"QueueName":  queue.QueueUUID,
	})
	require.NoError(t, err)

	orgKey := ensureTestOrgKey(t, keyMgr, job.OrgID)
	secretsProvider, err := secrets.NewDatabaseProvider(testDB, job.OrgID, orgKey)
	require.NoError(t, err)
	const vcsToken = "sooper-seekrit-vcs-integration-token"
	require.NoError(t, secretsProvider.Set(ctx, "vcs/integration-repo", "token", vcsToken))

	project := &models.Project{
		UserID:         &job.UserID,
		OrgID:          job.OrgID,
		Name:           uniqueName("project-vcs"),
		RepoURL:        uniqueName("github.com/example/private-repo"),
		VCSTokenSecret: "vcs/integration-repo:token",
	}
	require.NoError(t, testDB.Create(project).Error)
	job.ProjectID = &project.ProjectID
	require.NoError(t, testDB.Save(job).Error)

	if _, err := mockCorndogs.SubmitTaskToQueue(ctx, queue.QueueUUID, &corndogs.TaskPayload{JobID: job.JobID}, 0); err != nil {
		t.Fatalf("failed to submit corndogs task: %v", err)
	}

	// --- Register + RequestJob ----------------------------------------------

	regResp, svcErr := csilCall(t, mux, "ReactorcideWorker", "register",
		workercsilapi.EncodeRegisterRequest, workercsilapi.DecodeRegisterResponse,
		workercsilapi.RegisterRequest{
			EnrollmentToken: rawToken,
			WorkerInfo:      workercsilapi.WorkerInfo{WorkerKey: uniqueName("worker-vcs"), Os: "linux", Arch: "amd64"},
		}, "")
	require.Nil(t, svcErr)
	sessionToken := regResp.WorkerSession

	reqResp, svcErr := csilCall(t, mux, "ReactorcideWorker", "request-job",
		workercsilapi.EncodeRequestJobRequest, workercsilapi.DecodeRequestJobResponse,
		workercsilapi.RequestJobRequest{WorkerCharacteristics: workercsilapi.WorkerCharacteristics{
			Os: "linux", Arch: "amd64",
			Custom: []workercsilapi.CustomCharacteristic{{Key: "integration_marker", Value: marker}},
		}},
		sessionToken)
	require.Nil(t, svcErr)
	require.True(t, reqResp.HasLease, "worker satisfies the seeded queue and must claim its task")
	require.NotNil(t, reqResp.Lease)

	// --- vcs_auth is populated with the project's configured credential ----

	require.NotNil(t, reqResp.Lease.VcsAuth, "expected the lease to carry a resolved VCS checkout credential")
	require.Equal(t, "github", reqResp.Lease.VcsAuth.Provider)
	require.Equal(t, vcsToken, reqResp.Lease.VcsAuth.Token)
	require.Equal(t, "x-access-token", reqResp.Lease.VcsAuth.Username)
	require.Equal(t, "https://github.com/example/private-repo.git", reqResp.Lease.VcsAuth.Url)

	// --- isolation: the token never appears anywhere else -------------------

	for _, e := range reqResp.Lease.Env {
		require.NotContains(t, e.Value, vcsToken, "VCS token must never appear in Lease.Env")
	}
	for _, e := range reqResp.Lease.Secrets {
		require.NotContains(t, e.Value, vcsToken, "VCS token must never appear in Lease.Secrets")
	}
	for _, call := range mockCorndogs.SubmitTaskToQueueCalls {
		payloadBytes, marshalErr := json.Marshal(call.Payload)
		require.NoError(t, marshalErr)
		require.False(t, strings.Contains(string(payloadBytes), vcsToken), "corndogs SubmitTaskToQueue payload leaked the VCS token")
	}
	for _, call := range mockCorndogs.UpdateTaskCalls {
		require.False(t, strings.Contains(string(call.Payload), vcsToken), "corndogs UpdateTask payload leaked the VCS token")
	}
}

func waitForEvent(t *testing.T, sub *pubsub.Subscription, match func(pubsub.Event) bool) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case evt := <-sub.Ch:
			if match(evt) {
				return
			}
		case <-deadline:
			t.Fatalf("timed out waiting for expected NOTIFY event")
		}
	}
}
