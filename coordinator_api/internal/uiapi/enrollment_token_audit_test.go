package uiapi

import (
	"testing"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/uiapi/csilapi"
)

func TestDeactivateEnrollmentToken_RequiresConfirmPermanent(t *testing.T) {
	deps, st := newTestDeps(t)
	st.putUser(models.User{UserID: "org-1"})
	pool := st.putPool(models.WorkerPool{OrgID: strPtr("org-1"), Name: "pool-1"})
	admin := st.putUser(models.User{UserID: "admin-1"})
	seedOrgAdmin(st, admin.UserID, "org-1")
	ui := NewUiService(deps)
	ctx := mintSessionCtx(t, deps, admin.UserID)

	created, err := ui.CreateEnrollmentToken(ctx, csilapi.CreateEnrollmentTokenRequest{PoolId: pool.PoolID, Name: strPtr("2026")})
	requireOK(t, err)
	tokenID := created.Summary.TokenId

	for _, confirm := range []*bool{nil, boolPtr(false)} {
		_, err := ui.DeactivateEnrollmentToken(ctx, csilapi.DeactivateEnrollmentTokenRequest{TokenId: tokenID, ConfirmPermanent: confirm})
		requireCode(t, err, "invalid_argument")
	}
	listed, err := ui.ListEnrollmentTokens(ctx, csilapi.ListEnrollmentTokensRequest{PoolId: pool.PoolID})
	requireOK(t, err)
	if !listed.Tokens[0].IsActive {
		t.Fatal("a deactivate without confirm_permanent changed the token")
	}

	resp, err := ui.DeactivateEnrollmentToken(ctx, csilapi.DeactivateEnrollmentTokenRequest{TokenId: tokenID, ConfirmPermanent: boolPtr(true)})
	requireOK(t, err)
	if resp.Summary.IsActive {
		t.Fatal("token is still active after a confirmed deactivate")
	}
	if resp.Summary.ActiveWorkerCount == nil {
		t.Fatal("deactivate response has no active_worker_count")
	}
}

func TestListWorkers_EnrollmentTokenFieldsAndFilters(t *testing.T) {
	deps, st := newTestDeps(t)
	pool := st.putPool(models.WorkerPool{Name: "global"})
	admin := st.putUser(models.User{UserID: "admin-1"})
	seedGlobalAdmin(st, admin.UserID)
	ui := NewUiService(deps)
	ctx := mintSessionCtx(t, deps, admin.UserID)

	oldID, newID, oldName := "tok-old", "tok-new", "2025"
	st.putWorker(models.Worker{WorkerID: "w-old", PoolID: pool.PoolID, WorkerKey: "k1", OS: "linux", Arch: "amd64",
		EnrollmentTokenID: &oldID, EnrollmentTokenName: &oldName, EnrollmentTokenActive: boolPtr(false), HasActiveSession: true})
	st.putWorker(models.Worker{WorkerID: "w-gone", PoolID: pool.PoolID, WorkerKey: "k2", OS: "linux", Arch: "amd64",
		EnrollmentTokenID: &oldID, EnrollmentTokenName: &oldName, EnrollmentTokenActive: boolPtr(false), HasActiveSession: false})
	st.putWorker(models.Worker{WorkerID: "w-new", PoolID: pool.PoolID, WorkerKey: "k3", OS: "linux", Arch: "amd64",
		EnrollmentTokenID: &newID, EnrollmentTokenActive: boolPtr(true), HasActiveSession: true})
	st.putWorker(models.Worker{WorkerID: "w-legacy", PoolID: pool.PoolID, WorkerKey: "k4", OS: "linux", Arch: "amd64"})

	ids := func(resp csilapi.ListWorkersResponse) map[string]csilapi.WorkerSummary {
		out := map[string]csilapi.WorkerSummary{}
		for _, w := range resp.Workers {
			out[w.WorkerId] = w
		}
		return out
	}

	all, err := ui.ListWorkers(ctx, csilapi.ListWorkersRequest{})
	requireOK(t, err)
	got := ids(all)
	if len(got) != 4 {
		t.Fatalf("got %d workers, want 4", len(got))
	}
	old := got["w-old"]
	if old.EnrollmentTokenId == nil || *old.EnrollmentTokenId != oldID || old.EnrollmentTokenName == nil || *old.EnrollmentTokenName != oldName {
		t.Fatalf("w-old token fields = %+v", old)
	}
	if old.OnDeactivatedToken == nil || !*old.OnDeactivatedToken {
		t.Fatal("w-old: on_deactivated_token is not set")
	}
	if *got["w-gone"].OnDeactivatedToken {
		t.Fatal("w-gone has no active session, so it must not warn")
	}
	if got["w-new"].EnrollmentTokenName != nil {
		t.Fatal("an unnamed token must not get a name")
	}
	if got["w-legacy"].EnrollmentTokenId != nil || *got["w-legacy"].OnDeactivatedToken {
		t.Fatal("a worker with no recorded token must have no token fields")
	}

	warn, err := ui.ListWorkers(ctx, csilapi.ListWorkersRequest{OnDeactivatedToken: boolPtr(true)})
	requireOK(t, err)
	if g := ids(warn); len(g) != 1 || g["w-old"].WorkerId == "" {
		t.Fatalf("on_deactivated_token=true gave %v", g)
	}
	byToken, err := ui.ListWorkers(ctx, csilapi.ListWorkersRequest{EnrollmentTokenId: &oldID})
	requireOK(t, err)
	if g := ids(byToken); len(g) != 2 {
		t.Fatalf("enrollment_token_id filter gave %d workers, want 2", len(g))
	}
	notWarn, err := ui.ListWorkers(ctx, csilapi.ListWorkersRequest{OnDeactivatedToken: boolPtr(false)})
	requireOK(t, err)
	if g := ids(notWarn); len(g) != 3 {
		t.Fatalf("on_deactivated_token=false gave %d workers, want 3", len(g))
	}
}
