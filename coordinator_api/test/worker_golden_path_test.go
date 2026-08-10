package test

// TestWorkerGoldenPathE2E is the worker golden-path test
// for.GetAppMuxWithClient -> uiapi.NewHandlerWithWorker) against real
// Postgres (this package's shared testcontainers instance) and a shared
// in-memory Corndogs backend (see newIntegrationCorndogsClient in
// worker_protocol_integration_test.go) -- admin setup, job submission/queue
// routing, worker registration/matching/claim/run/report, and the two admin
// negative paths, all in one flow:
//
//  1. Admin path: a global-admin session (granted directly via the store,
//     not the container-wide-one-shot bootstrap-admin flow -- see the doc
//     comment above that setup below) creates a worker pool + enrollment
//     token, and precreates a non-default queue ({os:linux, gpu:true}) via
//     the create-queue admin op.
//  2. Submit path: three jobs are submitted through the real
//     handlers.JobHandler.CreateJob queue-resolution path -- a bare job
//     (resolves to the seeded default {os:linux} queue), a job with
//     characteristics {gpu:true} (resolves to the admin-precreated gpu
//     queue), and a job with an explicit resources block plus a
//     never-before-seen characteristic (finds-or-creates its own third
//     queue). Each job's queue_name is asserted against the right queue
//     UUID, and every queue row is confirmed to exist via list-queues.
//  3. Worker path: a plain linux/amd64 worker registers and calls
//     RequestJob -- it claims the default-queue job (the gpu job is not
//     offered, since the worker lacks the gpu characteristic). A second,
//     gpu-characteristic worker then claims the gpu-queue job. Both report
//     results and are asserted to reach a terminal job status with their
//     lease released.
//  4. Negative paths: a third worker is registered and immediately
//     quarantined via set-worker-status -- RequestJob returns no lease even
//     though a matching submitted job exists. A fresh, still-unclaimed job
//     on the gpu queue is left in place, then delete-queue on the gpu queue
//     is asserted to cancel it and remove the queue row.
//
// This test does not re-assert what the piece-tests already cover in depth
// (worker_protocol_integration_test.go's secret/VCS-auth isolation,
// worker_run_loop_integration_test.go's real-Docker run,
// worker_admin_bootstrap_test.go's dev bootstrap pool,
// queue_operations_test.go's find-or-create idempotency/race handling,
// worker_operations_test.go's store-op chain, and internal/uiapi's own
// worker_admin_test.go authz-permission matrix) -- its only job is proving
// the pieces compose correctly as one flow.

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/characteristics"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/handlers"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/objects"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/secrets"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/postgres_store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/uiapi/csilapi"
	workercsilapi "github.com/catalystcommunity/reactorcide/coordinator_api/internal/workerapi/csilapi"
)

func TestWorkerGoldenPathE2E(t *testing.T) {
	ctx := context.Background()

	// Master keys must exist before the app mux is built, same reasoning as
	// TestWorkerProtocolIntegration: router.go's own singleton key manager
	// must find already-persisted key material so every job's (here empty)
	// secret resolution runs against real, consistent org key material.
	_, err := secrets.LoadOrCreateMasterKeys(testDB)
	require.NoError(t, err)

	handlers.ResetAppMux()
	defer handlers.ResetAppMux()
	memStore := objects.NewMemoryObjectStore()
	handlers.SetObjectStore(memStore)
	defer handlers.SetObjectStore(nil)

	mockCorndogs := newIntegrationCorndogsClient()
	mux := handlers.GetAppMuxWithClient(mockCorndogs)

	require.NoError(t, postgres_store.PostgresStore.EnsureDefaultQueue(ctx))

	// === 1. Admin path ======================================================

	// A global-admin session. Deliberately NOT the bootstrap-admin CSIL flow
	// (TestUIAuthBootstrapAndPermissionMatrix's own coverage): bootstrap-admin
	// is a real one-shot, container-wide gate ("unauthorized" once ANY global
	// admin already exists -- see AuthService.BootstrapAdmin's doc comment),
	// so it is not safe for this test to depend on against the shared,
	// non-transactional test container other files in this package also
	// commit real global-admin users to. Granting the role directly via the
	// store and minting a session the same way mintSessionForUser does
	// (ui_auth_integration_test.go) exercises the exact same
	// Sessions/Resolver/Capabilities machinery every admin op below checks,
	// without that ordering hazard.
	du := &DataUtils{db: testDB}
	adminUser, err := du.CreateUser(DataSetup{})
	require.NoError(t, err)
	require.NoError(t, postgres_store.PostgresStore.CreateRoleAssignment(ctx, &models.RoleAssignment{
		PrincipalType: models.PrincipalTypeUser,
		PrincipalID:   adminUser.UserID,
		ScopeType:     models.ScopeTypeGlobal,
		Role:          models.RoleAdmin,
	}))
	adminToken := mintSessionForUser(t, adminUser.UserID)

	poolResp, svcErr := csilCall(t, mux, "ReactorcideUi", "create-pool",
		csilapi.EncodeCreatePoolRequest, csilapi.DecodeCreatePoolResponse,
		csilapi.CreatePoolRequest{Name: uniqueName("golden-path-pool")}, adminToken)
	require.Nil(t, svcErr)
	poolID := poolResp.Pool.PoolId
	grantDefaultOrgPool(t, ctx, poolID)

	tokenResp, svcErr := csilCall(t, mux, "ReactorcideUi", "create-enrollment-token",
		csilapi.EncodeCreateEnrollmentTokenRequest, csilapi.DecodeCreateEnrollmentTokenResponse,
		csilapi.CreateEnrollmentTokenRequest{PoolId: poolID}, adminToken)
	require.Nil(t, svcErr)
	require.NotEmpty(t, tokenResp.Token, "create-enrollment-token must return a raw token")

	gpuQueueResp, svcErr := csilCall(t, mux, "ReactorcideUi", "create-queue",
		csilapi.EncodeCreateQueueRequest, csilapi.DecodeCreateQueueResponse,
		csilapi.CreateQueueRequest{
			Characteristics: []csilapi.CharacteristicEntry{
				{Key: "os", Value: "linux"},
				{Key: "gpu", Value: true},
			},
			DisplayName: strPtr("golden-path-gpu"),
		}, adminToken)
	require.Nil(t, svcErr)
	gpuQueueID := gpuQueueResp.Queue.QueueId
	gpuQueueUUID := gpuQueueResp.Queue.QueueUuid
	assert.False(t, gpuQueueResp.Queue.IsDefault)

	// === 2. Submit path ======================================================

	submitter, err := du.CreateUser(DataSetup{})
	require.NoError(t, err)

	h := handlers.NewJobHandler(store.AppStore, mockCorndogs)

	// bare job -> the seeded default {os:linux} queue.
	_, bareResp := submitTestJob(t, ctx, h, submitter.UserID, map[string]interface{}{
		"name":        uniqueName("golden-bare-job"),
		"job_command": "true",
		"source_type": "copy",
		"source_path": "./",
	})
	require.Equal(t, "submitted", bareResp["status"], "bareResp: %+v", bareResp)
	bareJobID, _ := bareResp["job_id"].(string)
	require.NotEmpty(t, bareJobID)

	defaultChars, err := characteristics.ParseJobCharacteristics(nil)
	require.NoError(t, err)
	defaultQueue, err := postgres_store.PostgresStore.FindOrCreateQueueByCharacteristics(ctx, defaultChars)
	require.NoError(t, err)
	assert.Equal(t, defaultQueue.QueueUUID, bareResp["queue_name"],
		"bare job must route to the default queue")

	// gpu-characteristic job -> the admin-precreated gpu queue.
	_, gpuResp := submitTestJob(t, ctx, h, submitter.UserID, map[string]interface{}{
		"name":            uniqueName("golden-gpu-job"),
		"job_command":     "true",
		"source_type":     "copy",
		"source_path":     "./",
		"characteristics": map[string]interface{}{"gpu": true},
	})
	require.Equal(t, "submitted", gpuResp["status"], "gpuResp: %+v", gpuResp)
	gpuJobID, _ := gpuResp["job_id"].(string)
	require.NotEmpty(t, gpuJobID)
	assert.Equal(t, gpuQueueUUID, gpuResp["queue_name"],
		"a job with characteristics {gpu:true} must route to the admin-precreated gpu queue")

	// resources job -> its own brand-new queue (a never-before-seen
	// characteristic keeps it from colliding with the default/gpu queues
	// above, so it never competes with either for a worker's claim below;
	// this job is deliberately left unclaimed -- its only purpose is
	// proving resources persist and queue resolution still works when a
	// resources block is present).
	resourceMarker := uniqueName("golden-resources-marker")
	_, resourcesResp := submitTestJob(t, ctx, h, submitter.UserID, map[string]interface{}{
		"name":            uniqueName("golden-resources-job"),
		"job_command":     "true",
		"source_type":     "copy",
		"source_path":     "./",
		"characteristics": map[string]interface{}{resourceMarker: true},
		"resources": map[string]interface{}{
			"cpu":    map[string]interface{}{"request": "2", "limit": "4"},
			"memory": map[string]interface{}{"limit": "8Gi"},
		},
	})
	require.Equal(t, "submitted", resourcesResp["status"], "resourcesResp: %+v", resourcesResp)
	resourcesJobID, _ := resourcesResp["job_id"].(string)
	require.NotEmpty(t, resourcesJobID)

	resourcesChars, err := characteristics.ParseJobCharacteristics(map[string]any{resourceMarker: true})
	require.NoError(t, err)
	resourcesQueue, err := postgres_store.PostgresStore.FindOrCreateQueueByCharacteristics(ctx, resourcesChars)
	require.NoError(t, err)
	assert.Equal(t, resourcesQueue.QueueUUID, resourcesResp["queue_name"],
		"the resources job must route to its own (freshly created) queue")

	resourcesJob, err := postgres_store.PostgresStore.GetJobByID(ctx, resourcesJobID)
	require.NoError(t, err)
	assert.Equal(t, "2", resourcesJob.ResourceCPURequest)
	assert.Equal(t, "4", resourcesJob.ResourceCPULimit)
	assert.Equal(t, "8Gi", resourcesJob.ResourceMemoryLimit)

	// Every queue row this submit path touched actually exists, both
	// directly and via the admin list-queues surface.
	_, err = postgres_store.PostgresStore.GetQueueByID(ctx, defaultQueue.QueueID)
	require.NoError(t, err)
	_, err = postgres_store.PostgresStore.GetQueueByID(ctx, gpuQueueID)
	require.NoError(t, err)
	_, err = postgres_store.PostgresStore.GetQueueByID(ctx, resourcesQueue.QueueID)
	require.NoError(t, err)

	listResp, svcErr := csilCall(t, mux, "ReactorcideUi", "list-queues",
		csilapi.EncodeListQueuesRequest, csilapi.DecodeListQueuesResponse,
		csilapi.ListQueuesRequest{}, adminToken)
	require.Nil(t, svcErr)
	var sawDefault, sawGPU, sawResources bool
	for _, q := range listResp.Queues {
		switch q.QueueId {
		case defaultQueue.QueueID:
			sawDefault = true
		case gpuQueueID:
			sawGPU = true
		case resourcesQueue.QueueID:
			sawResources = true
		}
	}
	assert.True(t, sawDefault, "list-queues must include the default queue")
	assert.True(t, sawGPU, "list-queues must include the gpu queue")
	assert.True(t, sawResources, "list-queues must include the resources job's queue")

	// === 3. Worker path ======================================================

	rawEnrollmentToken := tokenResp.Token

	// A plain linux/amd64 worker: satisfies only the default queue (it has
	// no "gpu" characteristic), so RequestJob must hand it the bare job,
	// never the gpu job.
	plainReg, svcErr := csilCall(t, mux, "ReactorcideWorker", "register",
		workercsilapi.EncodeRegisterRequest, workercsilapi.DecodeRegisterResponse,
		workercsilapi.RegisterRequest{
			EnrollmentToken: rawEnrollmentToken,
			WorkerInfo:      workercsilapi.WorkerInfo{WorkerKey: uniqueName("golden-plain-worker"), Os: "linux", Arch: "amd64"},
		}, "")
	require.Nil(t, svcErr)
	plainSession := plainReg.WorkerSession

	plainReqResp, svcErr := csilCall(t, mux, "ReactorcideWorker", "request-job",
		workercsilapi.EncodeRequestJobRequest, workercsilapi.DecodeRequestJobResponse,
		workercsilapi.RequestJobRequest{WorkerCharacteristics: workercsilapi.WorkerCharacteristics{Os: "linux", Arch: "amd64"}},
		plainSession)
	require.Nil(t, svcErr)
	require.True(t, plainReqResp.HasLease, "the plain linux worker must claim the default-queue job")
	require.NotNil(t, plainReqResp.Lease)
	assert.Equal(t, bareJobID, plainReqResp.Lease.JobId,
		"the plain linux worker must claim the bare (default-queue) job, not the gpu job")

	// A gpu-characteristic worker: satisfies both the default queue (already
	// drained by the plain worker above, so nothing is left there) and the
	// gpu queue, and must claim the gpu job.
	gpuReg, svcErr := csilCall(t, mux, "ReactorcideWorker", "register",
		workercsilapi.EncodeRegisterRequest, workercsilapi.DecodeRegisterResponse,
		workercsilapi.RegisterRequest{
			EnrollmentToken: rawEnrollmentToken,
			WorkerInfo: workercsilapi.WorkerInfo{
				WorkerKey: uniqueName("golden-gpu-worker"), Os: "linux", Arch: "amd64",
				Custom: []workercsilapi.CustomCharacteristic{{Key: "gpu", Value: true}},
			},
		}, "")
	require.Nil(t, svcErr)
	gpuSession := gpuReg.WorkerSession

	gpuReqResp, svcErr := csilCall(t, mux, "ReactorcideWorker", "request-job",
		workercsilapi.EncodeRequestJobRequest, workercsilapi.DecodeRequestJobResponse,
		workercsilapi.RequestJobRequest{WorkerCharacteristics: workercsilapi.WorkerCharacteristics{
			Os: "linux", Arch: "amd64",
			Custom: []workercsilapi.CustomCharacteristic{{Key: "gpu", Value: true}},
		}},
		gpuSession)
	require.Nil(t, svcErr)
	require.True(t, gpuReqResp.HasLease, "the gpu worker must claim the gpu-queue job")
	require.NotNil(t, gpuReqResp.Lease)
	assert.Equal(t, gpuJobID, gpuReqResp.Lease.JobId)

	// Report both results and confirm terminal state + released leases.
	_, svcErr = csilCall(t, mux, "ReactorcideWorker", "report-result",
		workercsilapi.EncodeReportResultRequest, workercsilapi.DecodeReportResultResponse,
		workercsilapi.ReportResultRequest{LeaseId: plainReqResp.Lease.LeaseId, ExitCode: 0, Status: "completed"},
		plainSession)
	require.Nil(t, svcErr)

	_, svcErr = csilCall(t, mux, "ReactorcideWorker", "report-result",
		workercsilapi.EncodeReportResultRequest, workercsilapi.DecodeReportResultResponse,
		workercsilapi.ReportResultRequest{LeaseId: gpuReqResp.Lease.LeaseId, ExitCode: 0, Status: "completed"},
		gpuSession)
	require.Nil(t, svcErr)

	finalBareJob, err := postgres_store.PostgresStore.GetJobByID(ctx, bareJobID)
	require.NoError(t, err)
	assert.Equal(t, "completed", finalBareJob.Status)
	finalGPUJob, err := postgres_store.PostgresStore.GetJobByID(ctx, gpuJobID)
	require.NoError(t, err)
	assert.Equal(t, "completed", finalGPUJob.Status)

	plainLease, err := postgres_store.PostgresStore.GetWorkerLeaseByID(ctx, plainReqResp.Lease.LeaseId)
	require.NoError(t, err)
	assert.False(t, plainLease.IsActive(), "the plain worker's lease must be released after report-result")
	gpuLease, err := postgres_store.PostgresStore.GetWorkerLeaseByID(ctx, gpuReqResp.Lease.LeaseId)
	require.NoError(t, err)
	assert.False(t, gpuLease.IsActive(), "the gpu worker's lease must be released after report-result")

	// === 4. Negative paths ===================================================

	// Quarantine: a freshly registered worker, quarantined before it ever
	// requests work, must be refused a lease even though a matching
	// submitted job exists (a second bare job on the now-empty default
	// queue).
	_, quarantineTargetResp := submitTestJob(t, ctx, h, submitter.UserID, map[string]interface{}{
		"name":        uniqueName("golden-quarantine-bait-job"),
		"job_command": "true",
		"source_type": "copy",
		"source_path": "./",
	})
	require.Equal(t, "submitted", quarantineTargetResp["status"])

	quarantineReg, svcErr := csilCall(t, mux, "ReactorcideWorker", "register",
		workercsilapi.EncodeRegisterRequest, workercsilapi.DecodeRegisterResponse,
		workercsilapi.RegisterRequest{
			EnrollmentToken: rawEnrollmentToken,
			WorkerInfo:      workercsilapi.WorkerInfo{WorkerKey: uniqueName("golden-quarantined-worker"), Os: "linux", Arch: "amd64"},
		}, "")
	require.Nil(t, svcErr)

	setStatusResp, svcErr := csilCall(t, mux, "ReactorcideUi", "set-worker-status",
		csilapi.EncodeSetWorkerStatusRequest, csilapi.DecodeSetWorkerStatusResponse,
		csilapi.SetWorkerStatusRequest{WorkerId: quarantineReg.WorkerId, Status: "quarantined"}, adminToken)
	require.Nil(t, svcErr)
	assert.Equal(t, "quarantined", setStatusResp.Worker.Status)

	quarantineReqResp, svcErr := csilCall(t, mux, "ReactorcideWorker", "request-job",
		workercsilapi.EncodeRequestJobRequest, workercsilapi.DecodeRequestJobResponse,
		workercsilapi.RequestJobRequest{WorkerCharacteristics: workercsilapi.WorkerCharacteristics{Os: "linux", Arch: "amd64"}},
		quarantineReg.WorkerSession)
	require.Nil(t, svcErr)
	assert.False(t, quarantineReqResp.HasLease,
		"a quarantined worker must be refused a lease even though a matching submitted job exists")

	// Delete-cancels: a fresh, still-unclaimed job on the gpu queue must be
	// cancelled, and the queue row removed, when the gpu queue is deleted.
	_, deleteTargetResp := submitTestJob(t, ctx, h, submitter.UserID, map[string]interface{}{
		"name":            uniqueName("golden-gpu-delete-bait-job"),
		"job_command":     "true",
		"source_type":     "copy",
		"source_path":     "./",
		"characteristics": map[string]interface{}{"gpu": true},
	})
	require.Equal(t, "submitted", deleteTargetResp["status"])
	deleteTargetJobID, _ := deleteTargetResp["job_id"].(string)
	require.NotEmpty(t, deleteTargetJobID)
	require.Equal(t, gpuQueueUUID, deleteTargetResp["queue_name"])

	deleteResp, svcErr := csilCall(t, mux, "ReactorcideUi", "delete-queue",
		csilapi.EncodeDeleteQueueRequest, csilapi.DecodeDeleteQueueResponse,
		csilapi.DeleteQueueRequest{QueueId: gpuQueueID}, adminToken)
	require.Nil(t, svcErr)
	assert.True(t, deleteResp.Deleted)
	assert.Contains(t, deleteResp.CancelledJobIds, deleteTargetJobID,
		"deleting the gpu queue must cancel its still-unclaimed in-flight job")

	finalDeleteTargetJob, err := postgres_store.PostgresStore.GetJobByID(ctx, deleteTargetJobID)
	require.NoError(t, err)
	assert.Equal(t, "cancelled", finalDeleteTargetJob.Status)

	_, err = postgres_store.PostgresStore.GetQueueByID(ctx, gpuQueueID)
	assert.ErrorIs(t, err, store.ErrNotFound, "the gpu queue row must be gone after delete-queue")
}

// strPtr is a tiny local helper for the golden-path test's optional string
// fields (CreateQueueRequest.DisplayName) -- kept file-local rather than
// reused from elsewhere since none of this package's other test files
// happen to export one under this exact name.
func strPtr(s string) *string { return &s }
