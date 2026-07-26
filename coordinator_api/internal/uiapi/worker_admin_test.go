package uiapi

import (
	"context"
	"strings"
	"testing"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/characteristics"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/uiapi/csilapi"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/workerauth"
)

// seedOrgMember grants userID a plain org/member role assignment at orgID --
// the "authenticated but not an admin" row of the permission matrix, used
// alongside seedOrgAdmin/seedGlobalAdmin (testhelpers_test.go).
func seedOrgMember(st *fakeStore, userID, orgID string) {
	st.grantRole(models.PrincipalTypeUser, userID, models.ScopeTypeOrg, &orgID, models.RoleMember)
}

// --- authz matrix: create-pool, create-enrollment-token, delete-queue,
// set-worker-status (WORKERS_PLAN.md Wave-4 P4) -- only org-admin (of the
// resource's org) / global-admin may ever perform these ops. -------------

func TestCreatePool_PermissionMatrix(t *testing.T) {
	req := csilapi.CreatePoolRequest{OrgId: strPtr("org-1"), Name: "ci-pool"}

	t.Run("anonymous may not create", func(t *testing.T) {
		deps, st := newTestDeps(t)
		st.putUser(models.User{UserID: "org-1"})
		ui := NewUiService(deps)
		_, err := ui.CreatePool(anonCtx(), req)
		requireCode(t, err, "unauthorized")
	})

	t.Run("plain member may not create", func(t *testing.T) {
		deps, st := newTestDeps(t)
		st.putUser(models.User{UserID: "org-1"})
		member := st.putUser(models.User{UserID: "member-1"})
		seedOrgMember(st, member.UserID, "org-1")
		ui := NewUiService(deps)
		ctx := mintSessionCtx(t, deps, member.UserID)
		_, err := ui.CreatePool(ctx, req)
		requireCode(t, err, "forbidden")
	})

	t.Run("org admin of the target org may create", func(t *testing.T) {
		deps, st := newTestDeps(t)
		st.putUser(models.User{UserID: "org-1"})
		admin := st.putUser(models.User{UserID: "admin-1"})
		seedOrgAdmin(st, admin.UserID, "org-1")
		ui := NewUiService(deps)
		ctx := mintSessionCtx(t, deps, admin.UserID)
		resp, err := ui.CreatePool(ctx, req)
		requireOK(t, err)
		if resp.Pool.Name != "ci-pool" {
			t.Errorf("Name = %q, want ci-pool", resp.Pool.Name)
		}
	})

	t.Run("org admin of a DIFFERENT org may not create in org-1", func(t *testing.T) {
		deps, st := newTestDeps(t)
		st.putUser(models.User{UserID: "org-1"})
		admin := st.putUser(models.User{UserID: "admin-2"})
		seedOrgAdmin(st, admin.UserID, "org-2")
		ui := NewUiService(deps)
		ctx := mintSessionCtx(t, deps, admin.UserID)
		_, err := ui.CreatePool(ctx, req)
		requireCode(t, err, "forbidden")
	})

	t.Run("global admin may create in any org", func(t *testing.T) {
		deps, st := newTestDeps(t)
		st.putUser(models.User{UserID: "org-1"})
		admin := st.putUser(models.User{UserID: "gadmin-1"})
		seedGlobalAdmin(st, admin.UserID)
		ui := NewUiService(deps)
		ctx := mintSessionCtx(t, deps, admin.UserID)
		_, err := ui.CreatePool(ctx, req)
		requireOK(t, err)
	})

	t.Run("global admin may create a global (org-less) pool", func(t *testing.T) {
		deps, st := newTestDeps(t)
		admin := st.putUser(models.User{UserID: "gadmin-2"})
		seedGlobalAdmin(st, admin.UserID)
		ui := NewUiService(deps)
		ctx := mintSessionCtx(t, deps, admin.UserID)
		resp, err := ui.CreatePool(ctx, csilapi.CreatePoolRequest{Name: "global-pool"})
		requireOK(t, err)
		if resp.Pool.OrgId != nil {
			t.Errorf("OrgId = %v, want nil for a global pool", resp.Pool.OrgId)
		}
	})

	t.Run("org admin may not create a global (org-less) pool", func(t *testing.T) {
		deps, st := newTestDeps(t)
		st.putUser(models.User{UserID: "org-1"})
		admin := st.putUser(models.User{UserID: "admin-3"})
		seedOrgAdmin(st, admin.UserID, "org-1")
		ui := NewUiService(deps)
		ctx := mintSessionCtx(t, deps, admin.UserID)
		_, err := ui.CreatePool(ctx, csilapi.CreatePoolRequest{Name: "global-pool"})
		requireCode(t, err, "forbidden")
	})
}

func TestCreateEnrollmentToken_PermissionMatrix(t *testing.T) {
	setup := func(t *testing.T) (*Deps, *fakeStore, models.WorkerPool) {
		t.Helper()
		deps, st := newTestDeps(t)
		st.putUser(models.User{UserID: "org-1"})
		pool := st.putPool(models.WorkerPool{OrgID: strPtr("org-1"), Name: "pool-1"})
		return deps, st, pool
	}

	t.Run("anonymous may not create", func(t *testing.T) {
		deps, _, pool := setup(t)
		ui := NewUiService(deps)
		_, err := ui.CreateEnrollmentToken(anonCtx(), csilapi.CreateEnrollmentTokenRequest{PoolId: pool.PoolID})
		requireCode(t, err, "unauthorized")
	})

	t.Run("plain member may not create", func(t *testing.T) {
		deps, st, pool := setup(t)
		member := st.putUser(models.User{UserID: "member-1"})
		seedOrgMember(st, member.UserID, "org-1")
		ui := NewUiService(deps)
		ctx := mintSessionCtx(t, deps, member.UserID)
		_, err := ui.CreateEnrollmentToken(ctx, csilapi.CreateEnrollmentTokenRequest{PoolId: pool.PoolID})
		requireCode(t, err, "forbidden")
	})

	t.Run("org admin of the pool's org may create", func(t *testing.T) {
		deps, st, pool := setup(t)
		admin := st.putUser(models.User{UserID: "admin-1"})
		seedOrgAdmin(st, admin.UserID, "org-1")
		ui := NewUiService(deps)
		ctx := mintSessionCtx(t, deps, admin.UserID)
		resp, err := ui.CreateEnrollmentToken(ctx, csilapi.CreateEnrollmentTokenRequest{PoolId: pool.PoolID})
		requireOK(t, err)
		if resp.Token == "" {
			t.Errorf("Token = empty, want a raw token")
		}
	})

	t.Run("global admin may create", func(t *testing.T) {
		deps, st, pool := setup(t)
		admin := st.putUser(models.User{UserID: "gadmin-1"})
		seedGlobalAdmin(st, admin.UserID)
		ui := NewUiService(deps)
		ctx := mintSessionCtx(t, deps, admin.UserID)
		_, err := ui.CreateEnrollmentToken(ctx, csilapi.CreateEnrollmentTokenRequest{PoolId: pool.PoolID})
		requireOK(t, err)
	})
}

func TestDeleteQueue_PermissionMatrix(t *testing.T) {
	setup := func(t *testing.T) (*Deps, *fakeStore, models.Queue) {
		t.Helper()
		deps, st := newTestDeps(t)
		st.putUser(models.User{UserID: "org-1"})
		queue := st.putQueue(models.Queue{
			Characteristics:     mustJobCharacteristics(t, map[string]any{"os": "linux", "arch": "amd64"}),
			CharacteristicsHash: "hash-nondefault",
			DisplayName:         "amd64",
		})
		return deps, st, queue
	}

	t.Run("anonymous may not delete", func(t *testing.T) {
		deps, _, queue := setup(t)
		ui := NewUiService(deps)
		_, err := ui.DeleteQueue(anonCtx(), csilapi.DeleteQueueRequest{QueueId: queue.QueueID})
		requireCode(t, err, "unauthorized")
	})

	t.Run("org admin (queues have no org) may not delete", func(t *testing.T) {
		deps, st, queue := setup(t)
		admin := st.putUser(models.User{UserID: "admin-1"})
		seedOrgAdmin(st, admin.UserID, "org-1")
		ui := NewUiService(deps)
		ctx := mintSessionCtx(t, deps, admin.UserID)
		_, err := ui.DeleteQueue(ctx, csilapi.DeleteQueueRequest{QueueId: queue.QueueID})
		requireCode(t, err, "forbidden")
	})

	t.Run("global admin may delete", func(t *testing.T) {
		deps, st, queue := setup(t)
		admin := st.putUser(models.User{UserID: "gadmin-1"})
		seedGlobalAdmin(st, admin.UserID)
		ui := NewUiService(deps)
		ctx := mintSessionCtx(t, deps, admin.UserID)
		resp, err := ui.DeleteQueue(ctx, csilapi.DeleteQueueRequest{QueueId: queue.QueueID})
		requireOK(t, err)
		if !resp.Deleted {
			t.Errorf("Deleted = false, want true")
		}
	})
}

func TestSetWorkerStatus_PermissionMatrix(t *testing.T) {
	setup := func(t *testing.T) (*Deps, *fakeStore, models.Worker) {
		t.Helper()
		deps, st := newTestDeps(t)
		st.putUser(models.User{UserID: "org-1"})
		pool := st.putPool(models.WorkerPool{OrgID: strPtr("org-1"), Name: "pool-1"})
		worker := st.putWorker(models.Worker{PoolID: pool.PoolID, WorkerKey: "wk-1", OS: "linux", Arch: "amd64"})
		return deps, st, worker
	}

	t.Run("anonymous may not set status", func(t *testing.T) {
		deps, _, worker := setup(t)
		ui := NewUiService(deps)
		_, err := ui.SetWorkerStatus(anonCtx(), csilapi.SetWorkerStatusRequest{WorkerId: worker.WorkerID, Status: "quarantined"})
		requireCode(t, err, "unauthorized")
	})

	t.Run("plain member may not set status", func(t *testing.T) {
		deps, st, worker := setup(t)
		member := st.putUser(models.User{UserID: "member-1"})
		seedOrgMember(st, member.UserID, "org-1")
		ui := NewUiService(deps)
		ctx := mintSessionCtx(t, deps, member.UserID)
		_, err := ui.SetWorkerStatus(ctx, csilapi.SetWorkerStatusRequest{WorkerId: worker.WorkerID, Status: "quarantined"})
		requireCode(t, err, "forbidden")
	})

	t.Run("org admin of the worker's pool's org may set status", func(t *testing.T) {
		deps, st, worker := setup(t)
		admin := st.putUser(models.User{UserID: "admin-1"})
		seedOrgAdmin(st, admin.UserID, "org-1")
		ui := NewUiService(deps)
		ctx := mintSessionCtx(t, deps, admin.UserID)
		resp, err := ui.SetWorkerStatus(ctx, csilapi.SetWorkerStatusRequest{WorkerId: worker.WorkerID, Status: "quarantined"})
		requireOK(t, err)
		if resp.Worker.Status != "quarantined" {
			t.Errorf("Status = %q, want quarantined", resp.Worker.Status)
		}
	})

	t.Run("global admin may set status", func(t *testing.T) {
		deps, st, worker := setup(t)
		admin := st.putUser(models.User{UserID: "gadmin-1"})
		seedGlobalAdmin(st, admin.UserID)
		ui := NewUiService(deps)
		ctx := mintSessionCtx(t, deps, admin.UserID)
		_, err := ui.SetWorkerStatus(ctx, csilapi.SetWorkerStatusRequest{WorkerId: worker.WorkerID, Status: "disabled"})
		requireOK(t, err)
	})

	t.Run("invalid status is rejected even for global admin", func(t *testing.T) {
		deps, st, worker := setup(t)
		admin := st.putUser(models.User{UserID: "gadmin-2"})
		seedGlobalAdmin(st, admin.UserID)
		ui := NewUiService(deps)
		ctx := mintSessionCtx(t, deps, admin.UserID)
		_, err := ui.SetWorkerStatus(ctx, csilapi.SetWorkerStatusRequest{WorkerId: worker.WorkerID, Status: "bogus"})
		requireCode(t, err, "invalid_argument")
	})
}

// --- secret handling: raw token once, never re-returned -------------------

// TestCreateEnrollmentToken_RawOnceValidatesAndNeverLeaksAgain drives the
// SECURITY contract from WORKERS_PLAN.md Wave-4 P4: create-enrollment-token
// returns the raw token exactly once; it must hash-validate through
// internal/workerauth.Enrollment (the same path a real worker Register call
// uses) and must never reappear, in any form, from list-enrollment-tokens.
func TestCreateEnrollmentToken_RawOnceValidatesAndNeverLeaksAgain(t *testing.T) {
	deps, st := newTestDeps(t)
	st.putUser(models.User{UserID: "org-1"})
	pool := st.putPool(models.WorkerPool{OrgID: strPtr("org-1"), Name: "pool-1"})
	admin := st.putUser(models.User{UserID: "admin-1"})
	seedOrgAdmin(st, admin.UserID, "org-1")
	ui := NewUiService(deps)
	ctx := mintSessionCtx(t, deps, admin.UserID)

	createResp, err := ui.CreateEnrollmentToken(ctx, csilapi.CreateEnrollmentTokenRequest{
		PoolId: pool.PoolID,
		Name:   strPtr("ci-token"),
	})
	requireOK(t, err)
	rawToken := createResp.Token
	if rawToken == "" {
		t.Fatalf("CreateEnrollmentToken returned an empty raw token")
	}
	if createResp.Summary.IsActive != true {
		t.Fatalf("Summary.IsActive = false, want true for a freshly created token")
	}

	// The raw token must validate through the exact same enrollment path a
	// real worker's Register call drives.
	enrollment := workerauth.NewEnrollment(st)
	validatedPool, err := enrollment.ValidateEnrollmentToken(context.Background(), rawToken)
	if err != nil {
		t.Fatalf("ValidateEnrollmentToken(raw token) failed: %v", err)
	}
	if validatedPool.PoolID != pool.PoolID {
		t.Fatalf("ValidateEnrollmentToken resolved pool %q, want %q", validatedPool.PoolID, pool.PoolID)
	}

	// list-enrollment-tokens must return metadata only: no field anywhere
	// carries the raw token value.
	listResp, err := ui.ListEnrollmentTokens(ctx, csilapi.ListEnrollmentTokensRequest{PoolId: pool.PoolID})
	requireOK(t, err)
	if len(listResp.Tokens) != 1 {
		t.Fatalf("len(Tokens) = %d, want 1", len(listResp.Tokens))
	}
	got := listResp.Tokens[0]
	if got.TokenId != createResp.Summary.TokenId {
		t.Fatalf("TokenId = %q, want %q", got.TokenId, createResp.Summary.TokenId)
	}
	assertNoRawTokenLeak(t, rawToken, got)

	// A wrong/garbage token must still be rejected -- validation isn't a
	// no-op.
	if _, err := enrollment.ValidateEnrollmentToken(context.Background(), "not-the-token"); err == nil {
		t.Fatalf("ValidateEnrollmentToken(garbage) unexpectedly succeeded")
	}
}

// assertNoRawTokenLeak fails the test if raw appears anywhere in summary's
// string-shaped fields (name, timestamps, IDs) -- a structural sanity check
// on top of the type system already having no raw-value field to leak
// through.
func assertNoRawTokenLeak(t *testing.T, raw string, summary csilapi.EnrollmentTokenSummary) {
	t.Helper()
	fields := []string{summary.TokenId, summary.PoolId, summary.CreatedAt}
	if summary.Name != nil {
		fields = append(fields, *summary.Name)
	}
	if summary.LastUsedAt != nil {
		fields = append(fields, *summary.LastUsedAt)
	}
	if summary.DeactivatedAt != nil {
		fields = append(fields, *summary.DeactivatedAt)
	}
	for _, f := range fields {
		if strings.Contains(f, raw) {
			t.Fatalf("EnrollmentTokenSummary field %q contains the raw token value", f)
		}
	}
}

// --- delete-queue: cancels in-flight jobs, refuses the default queue ------

func mustJobCharacteristics(t *testing.T, raw map[string]any) characteristics.Characteristics {
	t.Helper()
	c, err := characteristics.ParseJobCharacteristics(raw)
	if err != nil {
		t.Fatalf("failed to build characteristics: %v", err)
	}
	return c
}

func TestDeleteQueue_CancelsInFlightJobsAndRefusesDefault(t *testing.T) {
	deps, st := newTestDeps(t)
	admin := st.putUser(models.User{UserID: "gadmin-1"})
	seedGlobalAdmin(st, admin.UserID)
	ui := NewUiService(deps)
	ctx := mintSessionCtx(t, deps, admin.UserID)

	queue := st.putQueue(models.Queue{
		Characteristics:     mustJobCharacteristics(t, map[string]any{"os": "linux", "arch": "amd64"}),
		CharacteristicsHash: "hash-amd64",
		DisplayName:         "amd64",
	})
	job := st.putJob(models.Job{
		UserID:    "org-1",
		Name:      "build",
		QueueName: queue.QueueUUID,
		Status:    "running",
	})

	resp, err := ui.DeleteQueue(ctx, csilapi.DeleteQueueRequest{QueueId: queue.QueueID})
	requireOK(t, err)
	if !resp.Deleted {
		t.Fatalf("Deleted = false, want true")
	}
	found := false
	for _, id := range resp.CancelledJobIds {
		if id == job.JobID {
			found = true
		}
	}
	if !found {
		t.Fatalf("CancelledJobIds = %v, want it to include %q", resp.CancelledJobIds, job.JobID)
	}

	updated, err := st.GetJobByID(context.Background(), job.JobID)
	if err != nil {
		t.Fatalf("failed to reload job: %v", err)
	}
	if updated.Status != "cancelling" && updated.Status != "cancelled" {
		t.Fatalf("job status = %q, want cancelling or cancelled", updated.Status)
	}

	if _, err := st.GetQueueByID(context.Background(), queue.QueueID); err == nil {
		t.Fatalf("queue row still exists after delete-queue")
	}

	// The default queue may never be deleted.
	defaultQueue := st.putQueue(models.Queue{
		Characteristics:     mustJobCharacteristics(t, map[string]any{"os": "linux"}),
		CharacteristicsHash: "hash-default",
		DisplayName:         "Default (linux)",
		IsDefault:           true,
	})
	_, err = ui.DeleteQueue(ctx, csilapi.DeleteQueueRequest{QueueId: defaultQueue.QueueID})
	requireCode(t, err, "invalid_argument")

	if _, err := st.GetQueueByID(context.Background(), defaultQueue.QueueID); err != nil {
		t.Fatalf("default queue row was deleted despite the refusal: %v", err)
	}
}
