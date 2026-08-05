package workerapi

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/corndogs"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/workerapi/csilapi"
)

// seedQueueAndVCSJob seeds a satisfiable default queue plus a submitted job
// with the given source URL/project, and submits its corndogs task, so a
// RequestJob call in these tests only needs to register a worker and call
// RequestJob.
func seedQueueAndVCSJob(t *testing.T, h *testHarness, job *models.Job, queueUUID string) (sessionToken string) {
	t.Helper()
	ctx := context.Background()

	h.store.seedQueue(models.Queue{
		QueueUUID:       queueUUID,
		Characteristics: mustCharacteristics(t, map[string]any{"os": "linux"}),
	})
	h.store.seedJob(job)

	if _, err := h.corndogs.SubmitTaskToQueue(ctx, queueUUID, &corndogs.TaskPayload{JobID: job.JobID}, 0); err != nil {
		t.Fatalf("failed to submit task: %v", err)
	}

	sessionToken, _ = h.registerWorker(t, "vcs-worker-"+queueUUID, "linux", "amd64", nil)
	return sessionToken
}

// TestRequestJob_GrantedVCSCredential_ResolvedIntoLeaseVcsAuth asserts that a
// job whose project has a configured GitHub VCS credential gets that
// credential resolved into the lease's dedicated vcs_auth field -- and that
// the resolved token value never appears anywhere else: not in Lease.Env, not
// in Lease.Secrets, and not in any corndogs task payload byte slice.
func TestRequestJob_GrantedVCSCredential_ResolvedIntoLeaseVcsAuth(t *testing.T) {
	h := newTestHarness()
	const vcsToken = "sooper-seekrit-ghp-token"

	projectUserID := "project-owner-1"
	h.store.seedProject(&models.Project{
		ProjectID:      "proj-1",
		UserID:         &projectUserID,
		RepoURL:        "github.com/example/repo",
		VCSTokenSecret: "vcs/example/repo:github_pat",
	})
	h.secretsProvider.set("vcs/example/repo", "github_pat", vcsToken)

	sourceURL := "https://github.com/example/repo.git"
	projectID := "proj-1"
	job := &models.Job{
		UserID:     "job-submitter",
		ProjectID:  &projectID,
		Name:       "build",
		JobCommand: "echo hi",
		Status:     "submitted",
		SourceURL:  &sourceURL,
	}

	token := seedQueueAndVCSJob(t, h, job, "55555555-5555-5555-5555-555555555555")

	resp, err := h.service.RequestJob(ctxWithAuth(token), csilapi.RequestJobRequest{
		WorkerCharacteristics: csilapi.WorkerCharacteristics{Os: "linux", Arch: "amd64"},
	})
	if err != nil {
		t.Fatalf("RequestJob failed: %v", err)
	}
	if !resp.HasLease || resp.Lease == nil {
		t.Fatalf("expected a lease for a job with a configured VCS credential")
	}

	vcsAuth := resp.Lease.VcsAuth
	if vcsAuth == nil {
		t.Fatalf("expected Lease.vcs_auth to be populated")
	}
	if vcsAuth.Provider != "github" {
		t.Fatalf("expected provider github, got %q", vcsAuth.Provider)
	}
	if vcsAuth.Token != vcsToken {
		t.Fatalf("expected the resolved VCS token, got %q", vcsAuth.Token)
	}
	if vcsAuth.Username != "x-access-token" {
		t.Fatalf("expected GitHub credential username x-access-token, got %q", vcsAuth.Username)
	}
	if vcsAuth.Url != sourceURL {
		t.Fatalf("expected vcs_auth.url to be the job's source URL, got %q", vcsAuth.Url)
	}

	for _, e := range resp.Lease.Env {
		if strings.Contains(e.Value, vcsToken) {
			t.Fatalf("VCS token must never appear in Lease.Env, found in %s=%s", e.Key, e.Value)
		}
	}
	for _, e := range resp.Lease.Secrets {
		if strings.Contains(e.Value, vcsToken) {
			t.Fatalf("VCS token must never appear in Lease.Secrets, found in %s=%s", e.Key, e.Value)
		}
	}

	assertNoSecretInCorndogsPayloads(t, h, vcsToken)
	assertNoSecretInLeaseJSON(t, resp.Lease, vcsToken)
}

// TestRequestJob_NoVCSCredentialConfigured_VcsAuthAbsent asserts a job whose
// source repo has a recognizable provider but no configured credential (a
// public repo) still gets a lease, just without vcs_auth -- credential
// absence is not an error.
func TestRequestJob_NoVCSCredentialConfigured_VcsAuthAbsent(t *testing.T) {
	h := newTestHarness()

	sourceURL := "https://github.com/example/public-repo.git"
	job := &models.Job{
		UserID:     "job-submitter-2",
		Name:       "build-public",
		JobCommand: "echo hi",
		Status:     "submitted",
		SourceURL:  &sourceURL,
	}

	token := seedQueueAndVCSJob(t, h, job, "66666666-6666-6666-6666-666666666666")

	resp, err := h.service.RequestJob(ctxWithAuth(token), csilapi.RequestJobRequest{
		WorkerCharacteristics: csilapi.WorkerCharacteristics{Os: "linux", Arch: "amd64"},
	})
	if err != nil {
		t.Fatalf("RequestJob failed: %v", err)
	}
	if !resp.HasLease || resp.Lease == nil {
		t.Fatalf("expected a lease even though no VCS credential is configured")
	}
	if resp.Lease.VcsAuth != nil {
		t.Fatalf("expected vcs_auth to be absent when no credential is configured, got %+v", resp.Lease.VcsAuth)
	}
}

// TestRequestJob_NoSourceURL_VcsAuthAbsent asserts a job with no source repo
// at all (e.g. a raw-command job) never gets a vcs_auth block.
func TestRequestJob_NoSourceURL_VcsAuthAbsent(t *testing.T) {
	h := newTestHarness()

	job := &models.Job{
		UserID:     "job-submitter-3",
		Name:       "raw-command",
		JobCommand: "echo hi",
		Status:     "submitted",
	}

	token := seedQueueAndVCSJob(t, h, job, "77777777-7777-7777-7777-777777777777")

	resp, err := h.service.RequestJob(ctxWithAuth(token), csilapi.RequestJobRequest{
		WorkerCharacteristics: csilapi.WorkerCharacteristics{Os: "linux", Arch: "amd64"},
	})
	if err != nil {
		t.Fatalf("RequestJob failed: %v", err)
	}
	if !resp.HasLease || resp.Lease == nil {
		t.Fatalf("expected a lease for a job with no source URL")
	}
	if resp.Lease.VcsAuth != nil {
		t.Fatalf("expected vcs_auth to be absent for a job with no source URL, got %+v", resp.Lease.VcsAuth)
	}
}

// TestRequestJob_RotationVCSCredential_TakesPrecedenceOverLegacyRef asserts
// RequestJob reuses internal/vcs's rotation-aware resolution (an active
// project_vcs_credentials row wins over the project's legacy static
// VCSTokenSecret ref), mirroring the deleted
// (*JobProcessor).resolveVCSCheckoutToken's own precedence exactly.
func TestRequestJob_RotationVCSCredential_TakesPrecedenceOverLegacyRef(t *testing.T) {
	h := newTestHarness()
	const legacyToken = "legacy-token-value"
	const rotatedToken = "rotated-token-value"

	h.store.seedProject(&models.Project{
		ProjectID:      "proj-rot",
		RepoURL:        "github.com/example/rotated-repo",
		VCSTokenSecret: "vcs/legacy:token",
	})
	h.secretsProvider.set("vcs/legacy", "token", legacyToken)
	h.secretsProvider.set("vcs/rotated", "token", rotatedToken)
	h.store.seedVCSCredential(models.ProjectVCSCredential{
		ProjectID: "proj-rot",
		Provider:  "github",
		Name:      "rotated-in",
		SecretRef: "vcs/rotated:token",
		IsActive:  true,
	})

	sourceURL := "https://github.com/example/rotated-repo.git"
	projectID := "proj-rot"
	job := &models.Job{
		UserID:     "job-submitter-4",
		ProjectID:  &projectID,
		Name:       "build-rotated",
		JobCommand: "echo hi",
		Status:     "submitted",
		SourceURL:  &sourceURL,
	}

	token := seedQueueAndVCSJob(t, h, job, "88888888-8888-8888-8888-888888888888")

	resp, err := h.service.RequestJob(ctxWithAuth(token), csilapi.RequestJobRequest{
		WorkerCharacteristics: csilapi.WorkerCharacteristics{Os: "linux", Arch: "amd64"},
	})
	if err != nil {
		t.Fatalf("RequestJob failed: %v", err)
	}
	if !resp.HasLease || resp.Lease == nil || resp.Lease.VcsAuth == nil {
		t.Fatalf("expected a lease with vcs_auth populated")
	}
	if resp.Lease.VcsAuth.Token != rotatedToken {
		t.Fatalf("expected the rotated (higher-precedence) credential to win, got %q", resp.Lease.VcsAuth.Token)
	}

	assertNoSecretInCorndogsPayloads(t, h, legacyToken)
	assertNoSecretInCorndogsPayloads(t, h, rotatedToken)
}

// TestRequestJob_VCSAuthResolutionError_FailsClaimCleanly asserts a genuine
// VCS credential resolution failure (a malformed secret reference) fails the
// job claim cleanly -- same "denied secret" finalize path a job-secret grant
// denial uses -- rather than silently leasing the job without checkout auth
// or leaking the malformed-ref error detail as a secret value.
func TestRequestJob_VCSAuthResolutionError_FailsClaimCleanly(t *testing.T) {
	h := newTestHarness()

	h.store.seedProject(&models.Project{
		ProjectID:      "proj-bad-ref",
		RepoURL:        "github.com/example/bad-ref-repo",
		VCSTokenSecret: "not-a-valid-secret-ref", // missing "path:key" colon
	})

	sourceURL := "https://github.com/example/bad-ref-repo.git"
	projectID := "proj-bad-ref"
	job := &models.Job{
		UserID:     "job-submitter-5",
		ProjectID:  &projectID,
		Name:       "build-bad-ref",
		JobCommand: "echo hi",
		Status:     "submitted",
		SourceURL:  &sourceURL,
	}

	token := seedQueueAndVCSJob(t, h, job, "99999999-9999-9999-9999-999999999999")

	resp, err := h.service.RequestJob(ctxWithAuth(token), csilapi.RequestJobRequest{
		WorkerCharacteristics: csilapi.WorkerCharacteristics{Os: "linux", Arch: "amd64"},
	})
	if err != nil {
		t.Fatalf("RequestJob returned a transport error instead of has_lease=false: %v", err)
	}
	if resp.HasLease || resp.Lease != nil {
		t.Fatalf("a VCS credential resolution failure must never be leased; got %+v", resp)
	}

	saved, err := h.store.GetJobByID(context.Background(), job.JobID)
	if err != nil {
		t.Fatalf("failed to reload job: %v", err)
	}
	if saved.Status != "failed" {
		t.Fatalf("expected the job to be failed cleanly on VCS auth resolution error, got %q", saved.Status)
	}
}

// assertNoSecretInLeaseJSON is a defense-in-depth check: marshal the whole
// Lease to JSON (as if it were being serialized) and assert the secret value
// only appears where expected (vcs_auth.token), never leaking into any other
// field via a copy/paste mistake in buildLease.
func assertNoSecretInLeaseJSON(t *testing.T, lease *csilapi.Lease, secretValue string) {
	t.Helper()
	b, err := json.Marshal(lease)
	if err != nil {
		t.Fatalf("failed to marshal lease: %v", err)
	}
	count := strings.Count(string(b), secretValue)
	if count > 1 {
		t.Fatalf("expected the VCS token to appear at most once (vcs_auth.token) in the serialized lease, found %d occurrences: %s", count, b)
	}
}
