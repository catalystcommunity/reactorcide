package uiapi

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/characteristics"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/uiapi/csilapi"
)

// aliasFakeStore adds an in-memory image alias store and the image set
// inputs to fakeStore.
type aliasFakeStore struct {
	*fakeStore
	amu     sync.Mutex
	aliases map[string]*models.ImageAlias
	grants  []models.PoolClassGrant
	audits  []*models.AuditEvent
	nextID  int
	// inUse is the count that CountActiveImageAliasJobs returns, by alias
	// name.
	inUse map[string]int64
}

func (f *aliasFakeStore) CountActiveImageAliasJobs(_ context.Context, alias *models.ImageAlias) (int64, error) {
	f.amu.Lock()
	defer f.amu.Unlock()
	return f.inUse[alias.Name], nil
}

func newAliasFake(st *fakeStore) *aliasFakeStore {
	return &aliasFakeStore{fakeStore: st, aliases: map[string]*models.ImageAlias{}}
}

func sameScope(a *models.ImageAlias, orgID string, projectID *string) bool {
	if a.OrgID != orgID {
		return false
	}
	if projectID == nil || *projectID == "" {
		return a.ProjectID == nil
	}
	return a.ProjectID != nil && *a.ProjectID == *projectID
}

func (f *aliasFakeStore) CreateImageAlias(_ context.Context, alias *models.ImageAlias) error {
	if err := alias.Validate(); err != nil {
		return store.ErrInvalidInput
	}
	f.amu.Lock()
	defer f.amu.Unlock()
	for _, a := range f.aliases {
		if sameScope(a, alias.OrgID, alias.ProjectID) && a.Name == alias.Name {
			return store.ErrAlreadyExists
		}
	}
	f.nextID++
	alias.AliasID = "alias-" + string(rune('0'+f.nextID))
	alias.CreatedAt, alias.UpdatedAt = time.Now(), time.Now()
	copy := *alias
	f.aliases[alias.AliasID] = &copy
	return nil
}

func (f *aliasFakeStore) UpdateImageAlias(_ context.Context, alias *models.ImageAlias) error {
	if err := alias.Validate(); err != nil {
		return store.ErrInvalidInput
	}
	f.amu.Lock()
	defer f.amu.Unlock()
	if _, ok := f.aliases[alias.AliasID]; !ok {
		return store.ErrNotFound
	}
	copy := *alias
	f.aliases[alias.AliasID] = &copy
	return nil
}

func (f *aliasFakeStore) GetImageAlias(_ context.Context, orgID string, projectID *string, name string) (*models.ImageAlias, error) {
	f.amu.Lock()
	defer f.amu.Unlock()
	for _, a := range f.aliases {
		if sameScope(a, orgID, projectID) && a.Name == name {
			copy := *a
			return &copy, nil
		}
	}
	return nil, store.ErrNotFound
}

func (f *aliasFakeStore) GetImageAliasByID(_ context.Context, id string) (*models.ImageAlias, error) {
	f.amu.Lock()
	defer f.amu.Unlock()
	if a, ok := f.aliases[id]; ok {
		copy := *a
		return &copy, nil
	}
	return nil, store.ErrNotFound
}

func (f *aliasFakeStore) ListImageAliases(_ context.Context, orgID string, projectID *string) ([]models.ImageAlias, error) {
	f.amu.Lock()
	defer f.amu.Unlock()
	out := []models.ImageAlias{}
	for _, a := range f.aliases {
		if sameScope(a, orgID, projectID) {
			out = append(out, *a)
		}
	}
	return out, nil
}

func (f *aliasFakeStore) ListOrgImageAliases(_ context.Context, orgID string) ([]models.ImageAlias, error) {
	return f.ListImageAliasesForOrgs(context.Background(), []string{orgID})
}

func (f *aliasFakeStore) DeleteImageAlias(_ context.Context, orgID string, projectID *string, name string) error {
	f.amu.Lock()
	defer f.amu.Unlock()
	for id, a := range f.aliases {
		if sameScope(a, orgID, projectID) && a.Name == name {
			delete(f.aliases, id)
			return nil
		}
	}
	return store.ErrNotFound
}

func (f *aliasFakeStore) ResolveImageAlias(ctx context.Context, orgID string, projectID *string, name string) (*models.ImageAlias, error) {
	if a, err := f.GetImageAlias(ctx, orgID, projectID, name); err == nil {
		return a, nil
	}
	return f.GetImageAlias(ctx, orgID, nil, name)
}

func (f *aliasFakeStore) ListImageAliasesWithDigest(context.Context, string, *string, string, models.ImagePlatform) ([]models.ImageAlias, error) {
	return nil, nil
}

func (f *aliasFakeStore) SetImageAliasPending(context.Context, string, string, string) (*models.ImageAlias, error) {
	return nil, store.ErrNotFound
}

func (f *aliasFakeStore) ClearImageAliasPending(context.Context, string, string) (*models.ImageAlias, error) {
	return nil, store.ErrNotFound
}

func (f *aliasFakeStore) PromoteImageAliasPending(context.Context, string, string) (*models.ImageAlias, bool, error) {
	return nil, false, store.ErrNotFound
}

func (f *aliasFakeStore) ListPoolClassGrants(context.Context, []string) ([]models.PoolClassGrant, error) {
	return append([]models.PoolClassGrant(nil), f.grants...), nil
}

func (f *aliasFakeStore) ListImageAliasesForOrgs(_ context.Context, orgIDs []string) ([]models.ImageAlias, error) {
	f.amu.Lock()
	defer f.amu.Unlock()
	out := []models.ImageAlias{}
	for _, a := range f.aliases {
		for _, id := range orgIDs {
			if a.OrgID == id {
				out = append(out, *a)
			}
		}
	}
	return out, nil
}

func (f *aliasFakeStore) ListPendingImageAliases(context.Context) ([]models.ImageAlias, error) {
	return nil, nil
}

func (f *aliasFakeStore) ListDrainingImages(context.Context, []string) ([]models.DrainingImage, error) {
	return nil, nil
}

func (f *aliasFakeStore) LockWorkerImageSet(ctx context.Context, workerID string) (*models.Worker, error) {
	return f.GetWorkerByID(ctx, workerID)
}

func (f *aliasFakeStore) SetWorkerImageSet(context.Context, string, int64, string) error { return nil }

func (f *aliasFakeStore) UpdateWorkerImageState(context.Context, string, models.WorkerImageState) error {
	return nil
}

func (f *aliasFakeStore) GetWorkerImageState(context.Context, string) (*models.WorkerImageState, error) {
	return nil, nil
}

func (f *aliasFakeStore) AppendAuditEvent(_ context.Context, event *models.AuditEvent) error {
	f.amu.Lock()
	defer f.amu.Unlock()
	f.audits = append(f.audits, event)
	return nil
}

func (f *aliasFakeStore) LockImageAlias(ctx context.Context, aliasID string) (*models.ImageAlias, error) {
	return f.GetImageAliasByID(ctx, aliasID)
}

var _ imageAliasUIStore = (*aliasFakeStore)(nil)

const aliasTestOrg = "org-images"

func aliasDigest(c string) string { return "sha256:" + strings.Repeat(c, 64) }

func aliasRef(c string) string { return "oci://r.example/vm/win:" + c + "@" + aliasDigest(c) }

func aliasDeps(t *testing.T) (*Deps, *aliasFakeStore) {
	t.Helper()
	deps, st := newTestDeps(t)
	requireOK(t, st.CreateOrganization(context.Background(), &models.Organization{OrgID: aliasTestOrg, Name: "images"}))
	fake := newAliasFake(st)
	deps.Store = fake
	return deps, fake
}

func setAlias(t *testing.T, s *UiService, ctx context.Context, req csilapi.SetImageAliasRequest) (csilapi.SetImageAliasResponse, error) {
	t.Helper()
	if req.Organization == "" {
		req.Organization = "images"
	}
	return s.SetImageAlias(ctx, req)
}

func TestImageAliasPermissions(t *testing.T) {
	deps, st := aliasDeps(t)
	admin := st.putUser(models.User{UserID: "admin-1"})
	owner := st.putUser(models.User{UserID: "owner-1"})
	member := st.putUser(models.User{UserID: "member-1"})
	project := st.putProject(models.Project{OrgID: aliasTestOrg})
	other := st.putProject(models.Project{OrgID: "org-other"})
	seedOrgAdmin(st.fakeStore, admin.UserID, aliasTestOrg)
	seedProjectOwner(st.fakeStore, owner.UserID, project.ProjectID)
	seedProjectMember(st.fakeStore, member.UserID, project.ProjectID)
	svc := NewUiService(deps)
	osName, arch := "windows", "amd64"
	orgReq := csilapi.SetImageAliasRequest{Name: "win", Ref: aliasRef("1"), Os: &osName, Arch: &arch}
	projReq := orgReq
	projReq.ProjectId = &project.ProjectID

	// Organization scope: organization admin only.
	if _, err := setAlias(t, svc, mintSessionCtx(t, deps, owner.UserID), orgReq); serviceErrCode(t, err) != "forbidden" {
		t.Fatalf("project owner on org alias: %v", err)
	}
	if _, err := setAlias(t, svc, anonCtx(), orgReq); serviceErrCode(t, err) != "unauthorized" {
		t.Fatalf("anonymous: %v", err)
	}
	resp, err := setAlias(t, svc, mintSessionCtx(t, deps, admin.UserID), orgReq)
	requireOK(t, err)
	if resp.Outcome != aliasOutcomeCreated || resp.Alias.Kind != "vm" || resp.Alias.Scope != "organization" {
		t.Fatalf("create = %+v", resp)
	}
	// Project scope: project owner or organization admin; a member is refused.
	if _, err := setAlias(t, svc, mintSessionCtx(t, deps, member.UserID), projReq); serviceErrCode(t, err) != "forbidden" {
		t.Fatalf("member: %v", err)
	}
	requireOK(t, func() error { _, err := setAlias(t, svc, mintSessionCtx(t, deps, owner.UserID), projReq); return err }())
	// A project of another organization is not found.
	wrong := orgReq
	wrong.ProjectId = &other.ProjectID
	if _, err := setAlias(t, svc, mintSessionCtx(t, deps, admin.UserID), wrong); serviceErrCode(t, err) != "not_found" {
		t.Fatalf("other organization project: %v", err)
	}
	// Reads have the same rule.
	if _, err := svc.ListImageAliases(mintSessionCtx(t, deps, member.UserID), csilapi.ListImageAliasesRequest{Organization: "images"}); serviceErrCode(t, err) != "forbidden" {
		t.Fatalf("member list: %v", err)
	}
	list, err := svc.ListImageAliases(mintSessionCtx(t, deps, owner.UserID), csilapi.ListImageAliasesRequest{Organization: "images", ProjectId: &project.ProjectID})
	requireOK(t, err)
	// The project alias replaces the organization alias of the same name.
	if len(list.Aliases) != 1 || list.Aliases[0].Scope != "project" {
		t.Fatalf("project list = %+v", list.Aliases)
	}
	if _, err := svc.DeleteImageAlias(mintSessionCtx(t, deps, owner.UserID), csilapi.DeleteImageAliasRequest{Organization: "images", Name: "win"}); serviceErrCode(t, err) != "forbidden" {
		t.Fatalf("project owner delete org alias: %v", err)
	}
	if _, err := svc.GetImageRolloutStatus(mintSessionCtx(t, deps, member.UserID), csilapi.GetImageRolloutStatusRequest{Organization: "images", ProjectId: &project.ProjectID}); serviceErrCode(t, err) != "forbidden" {
		t.Fatalf("member rollout: %v", err)
	}
}

func TestSetImageAliasRollout(t *testing.T) {
	deps, st := aliasDeps(t)
	admin := st.putUser(models.User{UserID: "admin-1"})
	seedOrgAdmin(st.fakeStore, admin.UserID, aliasTestOrg)
	svc := NewUiService(deps)
	ctx := mintSessionCtx(t, deps, admin.UserID)
	osName, arch := "windows", "amd64"

	if _, err := setAlias(t, svc, ctx, csilapi.SetImageAliasRequest{Name: "win", Ref: "oci://r.example/vm/win:1"}); serviceErrCode(t, err) != "invalid_argument" {
		t.Fatalf("an unpinned reference must be refused: %v", err)
	}
	if _, err := setAlias(t, svc, ctx, csilapi.SetImageAliasRequest{Name: "win", Ref: aliasRef("1")}); serviceErrCode(t, err) != "invalid_argument" {
		t.Fatalf("a new alias needs os and arch: %v", err)
	}
	bad := []csilapi.CharacteristicEntry{{Key: "image_digest", Value: "x"}}
	if _, err := setAlias(t, svc, ctx, csilapi.SetImageAliasRequest{Name: "win", Ref: aliasRef("1"), Os: &osName, Arch: &arch, Characteristics: bad}); serviceErrCode(t, err) != "invalid_argument" {
		t.Fatalf("a reserved characteristic must be refused: %v", err)
	}
	created, err := setAlias(t, svc, ctx, csilapi.SetImageAliasRequest{Name: "win", Ref: aliasRef("1"), Os: &osName, Arch: &arch,
		Characteristics: []csilapi.CharacteristicEntry{{Key: "worker_class", Value: "mac-xcode"}}})
	requireOK(t, err)
	if created.Outcome != aliasOutcomeCreated || created.Alias.ActiveDigest != aliasDigest("1") || created.Alias.PendingRef != nil {
		t.Fatalf("created = %+v", created)
	}

	// The same reference again: unchanged.
	same, err := setAlias(t, svc, ctx, csilapi.SetImageAliasRequest{Name: "win", Ref: aliasRef("1")})
	requireOK(t, err)
	if same.Outcome != aliasOutcomeUnchanged {
		t.Fatalf("same = %+v", same)
	}
	// A new digest: pending, the active pin does not change.
	pending, err := setAlias(t, svc, ctx, csilapi.SetImageAliasRequest{Name: "win", Ref: aliasRef("2")})
	requireOK(t, err)
	if pending.Outcome != aliasOutcomePending || pending.Alias.ActiveDigest != aliasDigest("1") ||
		pending.Alias.PendingDigest == nil || *pending.Alias.PendingDigest != aliasDigest("2") {
		t.Fatalf("pending = %+v", pending)
	}
	// The characteristics stay when the request does not give them.
	if len(pending.Alias.Characteristics) != 1 {
		t.Fatalf("characteristics changed: %+v", pending.Alias.Characteristics)
	}
	// kind, os, arch, and characteristics cannot change (finding: a change
	// would skip the rollout). The same value is not a change.
	otherOS, otherKind := "macos", "container"
	sameOS := "windows"
	for name, req := range map[string]csilapi.SetImageAliasRequest{
		"os":              {Name: "win", Ref: aliasRef("2"), Os: &otherOS},
		"kind":            {Name: "win", Ref: aliasRef("2"), Kind: &otherKind},
		"characteristics": {Name: "win", Ref: aliasRef("2"), Characteristics: []csilapi.CharacteristicEntry{}},
	} {
		_, err := setAlias(t, svc, ctx, req)
		if serviceErrCode(t, err) != "failed_precondition" || !strings.Contains(err.Error(), "cannot change") {
			t.Fatalf("a %s change must be refused: %v", name, err)
		}
	}
	if _, err := setAlias(t, svc, ctx, csilapi.SetImageAliasRequest{Name: "win", Ref: aliasRef("2"), Os: &sameOS,
		Characteristics: []csilapi.CharacteristicEntry{{Key: "worker_class", Value: "mac-xcode"}}}); err != nil {
		t.Fatalf("the same metadata is not a change: %v", err)
	}
	// The active reference again: the pending pin is cancelled.
	back, err := setAlias(t, svc, ctx, csilapi.SetImageAliasRequest{Name: "win", Ref: aliasRef("1")})
	requireOK(t, err)
	if back.Outcome != aliasOutcomeUpdated || back.Alias.PendingRef != nil {
		t.Fatalf("back = %+v", back)
	}
	// --force: promoted at once.
	force := true
	forced, err := setAlias(t, svc, ctx, csilapi.SetImageAliasRequest{Name: "win", Ref: aliasRef("3"), Force: &force})
	requireOK(t, err)
	if forced.Outcome != aliasOutcomePromoted || forced.Alias.ActiveDigest != aliasDigest("3") || forced.Alias.PendingRef != nil {
		t.Fatalf("forced = %+v", forced)
	}

	actions := []string{}
	for _, event := range st.audits {
		actions = append(actions, event.Action)
	}
	want := "image_alias.create,image_alias.set,image_alias.set,image_alias.set,image_alias.promote"
	if strings.Join(actions, ",") != want {
		t.Fatalf("audit actions = %v", actions)
	}
	last := st.audits[len(st.audits)-1]
	if last.Details["old_digest"] != aliasDigest("1") || last.Details["new_digest"] != aliasDigest("3") || last.Details["forced"] != true {
		t.Fatalf("promote audit = %+v", last.Details)
	}
	if st.audits[1].Details["old_ref"] != aliasRef("1") || st.audits[1].Details["pending_ref"] != aliasRef("2") {
		t.Fatalf("set audit = %+v", st.audits[1].Details)
	}

	// A job that is not terminal uses the alias: the delete is refused.
	st.amu.Lock()
	st.inUse = map[string]int64{"win": 2}
	st.amu.Unlock()
	_, err = svc.DeleteImageAlias(ctx, csilapi.DeleteImageAliasRequest{Organization: "images", Name: "win"})
	if serviceErrCode(t, err) != "failed_precondition" || !strings.Contains(err.Error(), "2 job(s)") {
		t.Fatalf("delete of an alias in use: %v", err)
	}
	if _, err := svc.GetImageAlias(ctx, csilapi.GetImageAliasRequest{Organization: "images", Name: "win"}); err != nil {
		t.Fatalf("a refused delete keeps the alias: %v", err)
	}
	st.amu.Lock()
	st.inUse = nil
	st.amu.Unlock()

	_, err = svc.DeleteImageAlias(ctx, csilapi.DeleteImageAliasRequest{Organization: "images", Name: "win"})
	requireOK(t, err)
	if _, err := svc.GetImageAlias(ctx, csilapi.GetImageAliasRequest{Organization: "images", Name: "win"}); serviceErrCode(t, err) != "not_found" {
		t.Fatalf("deleted alias: %v", err)
	}
}

func TestImageRolloutStatusAndWorkerImageState(t *testing.T) {
	deps, st := aliasDeps(t)
	admin := st.putUser(models.User{UserID: "admin-1"})
	seedOrgAdmin(st.fakeStore, admin.UserID, aliasTestOrg)
	svc := NewUiService(deps)
	ctx := mintSessionCtx(t, deps, admin.UserID)

	pool := models.WorkerPool{OrgID: strPtr(aliasTestOrg), Name: "vm"}
	requireOK(t, st.CreateWorkerPool(context.Background(), &pool))
	st.grants = []models.PoolClassGrant{{PoolID: pool.PoolID, OrgID: aliasTestOrg, ClassName: "default"}}
	pending := aliasRef("2")
	pendingDigest := aliasDigest("2")
	st.aliases["a1"] = &models.ImageAlias{AliasID: "a1", OrgID: aliasTestOrg, Name: "win", Kind: "vm", OS: "windows", Arch: "amd64",
		Characteristics: characteristics.Characteristics{}, ActiveRef: aliasRef("1"), ActiveDigest: aliasDigest("1"),
		PendingRef: &pending, PendingDigest: &pendingDigest}

	seen := time.Now().UTC()
	runtime, state, version := "vm", models.WorkerImageStateUpdated, int64(4)
	worker := st.putWorker(models.Worker{PoolID: pool.PoolID, OS: "windows", Arch: "amd64", Status: models.WorkerStatusActive,
		Characteristics: characteristics.Characteristics{}, LastSeenAt: &seen, ImageRuntime: &runtime, ImageSetVersion: 4,
		ImageState: &state, ImageStateVersion: &version, ImageReadyDigests: []string{aliasDigest("1")}})

	resp, err := svc.GetImageRolloutStatus(ctx, csilapi.GetImageRolloutStatusRequest{Organization: "images"})
	requireOK(t, err)
	if len(resp.Rollouts) != 1 || len(resp.Rollouts[0].Workers) != 1 {
		t.Fatalf("rollouts = %+v", resp.Rollouts)
	}
	rw := resp.Rollouts[0].Workers[0]
	if rw.WorkerId != worker.WorkerID || !rw.HasActive || rw.HasPending || rw.Ready || rw.ImageStatus != "healthy" {
		t.Fatalf("rollout worker = %+v", rw)
	}

	stateResp, err := svc.GetWorkerImageState(ctx, csilapi.GetWorkerImageStateRequest{WorkerId: worker.WorkerID})
	requireOK(t, err)
	if !stateResp.ImageAware || stateResp.ImageStatus == nil || *stateResp.ImageStatus != "healthy" || len(stateResp.Images) != 2 {
		t.Fatalf("worker image state = %+v", stateResp)
	}
	if !stateResp.Images[0].Ready || stateResp.Images[1].Ready {
		t.Fatalf("ready flags = %+v", stateResp.Images)
	}
	// The active pin has no pending value; the pending pin has pending true.
	if stateResp.Images[0].Pending != nil || stateResp.Images[1].Pending == nil || !*stateResp.Images[1].Pending {
		t.Fatalf("pending flags = %+v", stateResp.Images)
	}

	// A hosted pool (no organization) that serves the organization: the
	// organization admin cannot manage its workers, so the rollout gives
	// counts and the pool name only, no worker IDs or host names.
	hosted := models.WorkerPool{Name: "hosted-win"}
	requireOK(t, st.CreateWorkerPool(context.Background(), &hosted))
	st.grants = append(st.grants, models.PoolClassGrant{PoolID: hosted.PoolID, OrgID: aliasTestOrg, ClassName: "default"})
	failedState := models.WorkerImageStateFailed
	hostedWorker := st.putWorker(models.Worker{PoolID: hosted.PoolID, OS: "windows", Arch: "amd64", Status: models.WorkerStatusActive,
		Hostname: "secret-host", Characteristics: characteristics.Characteristics{}, LastSeenAt: &seen, ImageRuntime: &runtime,
		ImageSetVersion: 4, ImageState: &failedState, ImageStateVersion: &version, ImageReadyDigests: []string{},
		ImageFailed: []models.ImageFailure{{Digest: aliasDigest("1"), Permanent: true, Detail: "denied"}}})
	resp, err = svc.GetImageRolloutStatus(ctx, csilapi.GetImageRolloutStatusRequest{Organization: "images"})
	requireOK(t, err)
	r0 := resp.Rollouts[0]
	if len(r0.Workers) != 1 || r0.Workers[0].WorkerId != worker.WorkerID {
		t.Fatalf("only the managed worker has a row: %+v", r0.Workers)
	}
	if len(r0.OtherPools) != 1 || r0.OtherPools[0].PoolName != "hosted-win" || r0.OtherPools[0].Workers != 1 || r0.OtherPools[0].FailedWorkers != 1 {
		t.Fatalf("other pools = %+v", r0.OtherPools)
	}
	if len(r0.Problems) != 1 || !strings.Contains(r0.Problems[0], "cannot pull the active image") || strings.Contains(r0.Problems[0], "secret-host") {
		t.Fatalf("problems = %+v", r0.Problems)
	}
	if raw := fmt.Sprintf("%+v", resp); strings.Contains(raw, hostedWorker.WorkerID) || strings.Contains(raw, "secret-host") {
		t.Fatalf("the rollout status shows a worker of a pool that the caller cannot manage: %s", raw)
	}
	hostedState := hostedWorker
	stateOut := svc.workerImageFailures(&hostedState)
	if len(stateOut) != 1 || !stateOut[0].Permanent {
		t.Fatalf("failures = %+v", stateOut)
	}

	summary := svc.workerToSummary(context.Background(), &worker)
	if summary.ImageStatus == nil || *summary.ImageStatus != "healthy" || summary.ImageSetVersion == nil || *summary.ImageSetVersion != 4 {
		t.Fatalf("worker summary = %+v", summary)
	}
	failed := worker
	detail := "no space left on device"
	failed.ImageState, failed.ImageStateDetail = &failedState, &detail
	summary = svc.workerToSummary(context.Background(), &failed)
	if *summary.ImageStatus != "image update failed" || *summary.ImageStatusDetail != detail {
		t.Fatalf("failed summary = %+v", summary)
	}
	old := worker
	old.ImageRuntime, old.ImageState = nil, nil
	if summary := svc.workerToSummary(context.Background(), &old); summary.ImageStatus != nil {
		t.Fatal("an old worker must have no image status")
	}
}
