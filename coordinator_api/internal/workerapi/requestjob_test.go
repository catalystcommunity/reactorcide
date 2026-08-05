package workerapi

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/characteristics"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/corndogs"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/workerapi/csilapi"
)

// mustCharacteristics is a small test-only wrapper around
// characteristics.ParseJobCharacteristics that fails the test on error,
// keeping seed calls below to one line each.
func mustCharacteristics(t *testing.T, raw map[string]any) characteristics.Characteristics {
	t.Helper()
	c, err := characteristics.ParseJobCharacteristics(raw)
	if err != nil {
		t.Fatalf("failed to build characteristics: %v", err)
	}
	return c
}

func TestRequestJob_MatchesSatisfyingQueueOnly(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()

	gpuQueueUUID := "11111111-1111-1111-1111-111111111111"
	h.store.seedQueue(models.Queue{
		QueueUUID:       gpuQueueUUID,
		Characteristics: mustCharacteristics(t, map[string]any{"os": "linux", "gpu": true}),
	})
	defaultQueueUUID := "22222222-2222-2222-2222-222222222222"
	h.store.seedQueue(models.Queue{
		QueueUUID:       defaultQueueUUID,
		Characteristics: mustCharacteristics(t, map[string]any{"os": "linux"}),
	})

	job := &models.Job{
		UserID:     "user-1",
		Name:       "build",
		JobCommand: "echo hi",
		Status:     "submitted",
	}
	h.store.seedJob(job)

	if _, err := h.corndogs.SubmitTaskToQueue(ctx, gpuQueueUUID, &corndogs.TaskPayload{JobID: job.JobID}, 0); err != nil {
		t.Fatalf("failed to submit task: %v", err)
	}

	// Worker A satisfies {os:linux, gpu:true} -- should get the task.
	tokenA, _ := h.registerWorker(t, "worker-a", "linux", "amd64", []csilapi.CustomCharacteristic{{Key: "gpu", Value: true}})
	// Worker B only satisfies {os:linux} -- must NOT get the gpu-queue task.
	tokenB, _ := h.registerWorker(t, "worker-b", "linux", "amd64", nil)

	respB, err := h.service.RequestJob(ctxWithAuth(tokenB), csilapi.RequestJobRequest{
		WorkerCharacteristics: csilapi.WorkerCharacteristics{Os: "linux", Arch: "amd64"},
	})
	if err != nil {
		t.Fatalf("RequestJob (worker B) failed: %v", err)
	}
	if respB.HasLease {
		t.Fatalf("worker B does not satisfy the gpu queue and must not receive its task; got lease %+v", respB.Lease)
	}

	respA, err := h.service.RequestJob(ctxWithAuth(tokenA), csilapi.RequestJobRequest{
		WorkerCharacteristics: csilapi.WorkerCharacteristics{
			Os: "linux", Arch: "amd64",
			Custom: []csilapi.CustomCharacteristic{{Key: "gpu", Value: true}},
		},
	})
	if err != nil {
		t.Fatalf("RequestJob (worker A) failed: %v", err)
	}
	if !respA.HasLease || respA.Lease == nil {
		t.Fatalf("worker A satisfies the gpu queue and should have received its task")
	}
	if respA.Lease.JobId != job.JobID {
		t.Fatalf("expected lease for job %s, got %s", job.JobID, respA.Lease.JobId)
	}

	saved, err := h.store.GetJobByID(ctx, job.JobID)
	if err != nil {
		t.Fatalf("failed to reload job: %v", err)
	}
	if saved.Status != "running" {
		t.Fatalf("expected job to be running after claim, got %q", saved.Status)
	}
	if saved.WorkerID == nil {
		t.Fatalf("expected job.WorkerID to be set to the claiming worker")
	}
}

func TestRequestJob_NoMatchingQueue(t *testing.T) {
	h := newTestHarness()

	h.store.seedQueue(models.Queue{
		QueueUUID:       "33333333-3333-3333-3333-333333333333",
		Characteristics: mustCharacteristics(t, map[string]any{"os": "windows"}),
	})

	token, _ := h.registerWorker(t, "worker-c", "linux", "amd64", nil)
	resp, err := h.service.RequestJob(ctxWithAuth(token), csilapi.RequestJobRequest{
		WorkerCharacteristics: csilapi.WorkerCharacteristics{Os: "linux", Arch: "amd64"},
	})
	if err != nil {
		t.Fatalf("RequestJob failed: %v", err)
	}
	if resp.HasLease {
		t.Fatalf("worker satisfies no queue; must not receive a lease")
	}
}

func TestRequestJob_UnauthorizedWithoutSession(t *testing.T) {
	h := newTestHarness()
	_, err := h.service.RequestJob(context.Background(), csilapi.RequestJobRequest{
		WorkerCharacteristics: csilapi.WorkerCharacteristics{Os: "linux", Arch: "amd64"},
	})
	assertServiceErrorCode(t, err, "unauthorized")
}

// --- secret isolation ------------------------------------------------------

func seedQueueAndJobWithSecretRef(t *testing.T, h *testHarness, grantSecret bool) (job *models.Job, sessionToken string) {
	t.Helper()
	ctx := context.Background()

	queueUUID := "44444444-4444-4444-4444-444444444444"
	h.store.seedQueue(models.Queue{
		QueueUUID:       queueUUID,
		Characteristics: mustCharacteristics(t, map[string]any{"os": "linux"}),
	})

	job = &models.Job{
		UserID:     "secret-user",
		Name:       "build-with-secret",
		JobCommand: "echo hi",
		Status:     "submitted",
		JobEnvVars: models.JSONB{
			"API_KEY": "${secret:myorg/creds:api_key}",
			"PLAIN":   "not-a-secret",
		},
	}
	h.store.seedJob(job)

	h.secretsProvider.set("myorg/creds", "api_key", "sooper-seekrit-value")

	if grantSecret {
		h.store.seedSecretGrant(models.SecretGrant{
			UserID:            job.UserID,
			SecretPathMatch:   models.SecretGrantMatchPrefix,
			SecretPathPattern: "myorg/creds",
			JobNameMatch:      models.SecretGrantMatchAny,
		})
	}

	if _, err := h.corndogs.SubmitTaskToQueue(ctx, queueUUID, &corndogs.TaskPayload{JobID: job.JobID}, 0); err != nil {
		t.Fatalf("failed to submit task: %v", err)
	}

	sessionToken, _ = h.registerWorker(t, "secret-worker", "linux", "amd64", nil)
	return job, sessionToken
}

func TestRequestJob_GrantedSecret_ResolvedSeparatelyFromEnv(t *testing.T) {
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

	var secretEnvVal, plainEnvVal string
	var sawAPIKeyInEnv bool
	for _, e := range resp.Lease.Env {
		if e.Key == "API_KEY" {
			sawAPIKeyInEnv = true
		}
		if e.Key == "PLAIN" {
			plainEnvVal = e.Value
		}
	}
	for _, e := range resp.Lease.Secrets {
		if e.Key == "API_KEY" {
			secretEnvVal = e.Value
		}
	}

	if sawAPIKeyInEnv {
		t.Fatalf("secret-bearing env var API_KEY must NOT appear in Lease.Env, only Lease.Secrets")
	}
	if secretEnvVal != "sooper-seekrit-value" {
		t.Fatalf("expected Lease.Secrets to carry the resolved value, got %q", secretEnvVal)
	}
	if plainEnvVal != "not-a-secret" {
		t.Fatalf("expected the non-secret PLAIN env var to pass through Lease.Env unchanged, got %q", plainEnvVal)
	}

	// The corndogs task payload for this job must never have carried the
	// secret value -- it only ever carried a job reference (job_id).
	assertNoSecretInCorndogsPayloads(t, h, "sooper-seekrit-value")

	saved, err := h.store.GetJobByID(context.Background(), job.JobID)
	if err != nil {
		t.Fatalf("failed to reload job: %v", err)
	}
	if saved.Status != "running" {
		t.Fatalf("expected job to be running, got %q", saved.Status)
	}
}

func TestRequestJob_UngrantedSecret_NotLeasedAndNotLeaked(t *testing.T) {
	h := newTestHarness()
	job, token := seedQueueAndJobWithSecretRef(t, h, false)

	resp, err := h.service.RequestJob(ctxWithAuth(token), csilapi.RequestJobRequest{
		WorkerCharacteristics: csilapi.WorkerCharacteristics{Os: "linux", Arch: "amd64"},
	})
	if err != nil {
		t.Fatalf("RequestJob returned a transport error instead of has_lease=false: %v", err)
	}
	if resp.HasLease || resp.Lease != nil {
		t.Fatalf("an ungranted secret reference must never be leased; got %+v", resp)
	}

	saved, err := h.store.GetJobByID(context.Background(), job.JobID)
	if err != nil {
		t.Fatalf("failed to reload job: %v", err)
	}
	if saved.Status != "failed" {
		t.Fatalf("expected the job to be failed cleanly on grant denial, got %q", saved.Status)
	}
	if !strings.Contains(saved.LastError, "secret") {
		t.Fatalf("expected LastError to describe the secret denial, got %q", saved.LastError)
	}
	if strings.Contains(saved.LastError, "sooper-seekrit-value") {
		t.Fatalf("LastError must never contain the actual secret value: %q", saved.LastError)
	}

	assertNoSecretInCorndogsPayloads(t, h, "sooper-seekrit-value")
}

// assertNoSecretInCorndogsPayloads inspects every payload byte slice
// workerapi's own code has sent to corndogs across every call type this
// package makes (SubmitTaskToQueue -- test setup only, UpdateTask --
// RequestJob's processing/failed transitions) and fails the test if
// secretValue appears anywhere in them. This is the "assert on the payload
// bytes" check.
func assertNoSecretInCorndogsPayloads(t *testing.T, h *testHarness, secretValue string) {
	t.Helper()
	for _, call := range h.corndogs.SubmitTaskToQueueCalls {
		payloadBytes, err := json.Marshal(call.Payload)
		if err != nil {
			t.Fatalf("failed to marshal payload for inspection: %v", err)
		}
		if strings.Contains(string(payloadBytes), secretValue) {
			t.Fatalf("corndogs SubmitTaskToQueue payload leaked a secret value: %s", payloadBytes)
		}
	}
	for _, call := range h.corndogs.UpdateTaskCalls {
		if strings.Contains(string(call.Payload), secretValue) {
			t.Fatalf("corndogs UpdateTask payload leaked a secret value: %s", call.Payload)
		}
	}
}
