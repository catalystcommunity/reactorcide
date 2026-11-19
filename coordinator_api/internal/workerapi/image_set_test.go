package workerapi

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/characteristics"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/imageset"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/workerapi/csilapi"
)

const imageTestOrg = "00000000-0000-0000-0000-0000000000aa"

// imageFakeStore adds the image set inputs (imageset.Store) to fakeStore.
// Every pool has the default class grant of imageTestOrg.
type imageFakeStore struct {
	*fakeStore
	aliases []models.ImageAlias
}

func (f *imageFakeStore) ListPoolClassGrants(_ context.Context, poolIDs []string) ([]models.PoolClassGrant, error) {
	out := []models.PoolClassGrant{}
	for _, id := range poolIDs {
		out = append(out, models.PoolClassGrant{PoolID: id, OrgID: imageTestOrg, ClassName: "default"})
	}
	return out, nil
}

func (f *imageFakeStore) ListImageAliasesForOrgs(context.Context, []string) ([]models.ImageAlias, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]models.ImageAlias(nil), f.aliases...), nil
}

func (f *imageFakeStore) ListPendingImageAliases(context.Context) ([]models.ImageAlias, error) {
	return nil, nil
}

func (f *imageFakeStore) ListDrainingImages(context.Context, []string) ([]models.DrainingImage, error) {
	return nil, nil
}

func (f *imageFakeStore) LockWorkerImageSet(_ context.Context, workerID string) (*models.Worker, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	w, ok := f.workers[workerID]
	if !ok {
		return nil, store.ErrNotFound
	}
	copy := *w
	return &copy, nil
}

var _ imageset.Store = (*imageFakeStore)(nil)

func imageHarness(t *testing.T) (*testHarness, *imageFakeStore) {
	t.Helper()
	h := newTestHarness()
	st := &imageFakeStore{fakeStore: h.store, aliases: []models.ImageAlias{{
		AliasID: "a1", OrgID: imageTestOrg, Name: "win", Kind: models.ImageAliasKindVM, OS: "windows", Arch: "amd64",
		Characteristics: characteristics.Characteristics{},
		ActiveRef:       "oci://r.example/vm/win:1@" + imgDigestA, ActiveDigest: imgDigestA,
	}}}
	h.deps.Store = st
	return h, st
}

func registerImageWorker(t *testing.T, h *testHarness, key string, runtime *string) (csilapi.RegisterResponse, string) {
	t.Helper()
	pool := &models.WorkerPool{Name: "pool-" + key}
	h.store.seedPool(pool)
	raw := "raw-token-" + key
	h.store.seedEnrollmentToken(&models.PoolEnrollmentToken{PoolID: pool.PoolID, TokenHash: rawTokenHash(raw), IsActive: true})
	resp, err := h.service.Register(context.Background(), csilapi.RegisterRequest{EnrollmentToken: raw,
		WorkerInfo: csilapi.WorkerInfo{WorkerKey: key, Os: "windows", Arch: "amd64", Runtime: runtime}})
	if err != nil {
		t.Fatal(err)
	}
	return resp, resp.WorkerSession
}

func TestRegisterDeliversImageSetToImageAwareWorker(t *testing.T) {
	h, _ := imageHarness(t)
	resp, _ := registerImageWorker(t, h, "vm-worker", strPtrImage("vm"))
	if resp.ImageSet == nil || resp.ImageSet.Version != 1 || len(resp.ImageSet.Images) != 1 {
		t.Fatalf("image set = %+v", resp.ImageSet)
	}
	entry := resp.ImageSet.Images[0]
	if entry.Digest != imgDigestA || entry.Kind != "vm" || entry.Aliases[0] != "win" || entry.Ref != "r.example/vm/win:1@"+imgDigestA {
		t.Fatalf("entry = %+v", entry)
	}
	w, _ := h.store.GetWorkerByID(context.Background(), resp.WorkerId)
	if w.ImageRuntime == nil || *w.ImageRuntime != "vm" || w.ImageSetVersion != 1 {
		t.Fatalf("worker row = %+v", w)
	}

	// Register again with the same set: the version does not change.
	again, _ := registerImageWorker(t, h, "vm-worker", strPtrImage("vm"))
	if again.ImageSet == nil || again.ImageSet.Version != 1 {
		t.Fatalf("second register = %+v", again.ImageSet)
	}
}

func TestRegisterOldWorkerGetsNoImageSet(t *testing.T) {
	h, _ := imageHarness(t)
	resp, token := registerImageWorker(t, h, "old-worker", nil)
	if resp.ImageSet != nil {
		t.Fatal("an old worker must get no image set")
	}
	hb, err := h.service.Heartbeat(ctxWithAuth(token), csilapi.HeartbeatRequest{Status: "idle"})
	if err != nil || hb.ImageSet != nil {
		t.Fatalf("old worker heartbeat: %+v %v", hb.ImageSet, err)
	}
	// An unknown runtime is not an error: the worker works as an old worker.
	unknown, _ := registerImageWorker(t, h, "odd-worker", strPtrImage("firecracker"))
	if unknown.ImageSet != nil {
		t.Fatal("an unknown runtime must get no image set")
	}
}

func TestHeartbeatImageSetDeliveryRules(t *testing.T) {
	h, st := imageHarness(t)
	resp, token := registerImageWorker(t, h, "vm-worker", strPtrImage("vm"))
	version := resp.ImageSet.Version

	beat := func(report *csilapi.ImageSetReport) *csilapi.ImageSet {
		t.Helper()
		out, err := h.service.Heartbeat(ctxWithAuth(token), csilapi.HeartbeatRequest{Status: "idle", ImageSetReport: report})
		if err != nil {
			t.Fatal(err)
		}
		return out.ImageSet
	}
	// No report yet: the set is sent.
	if set := beat(nil); set == nil || set.Version != version {
		t.Fatalf("no report: %+v", set)
	}
	// The current version: nothing is sent.
	if set := beat(&csilapi.ImageSetReport{Version: version, State: "updating", ReadyDigests: []string{}}); set != nil {
		t.Fatalf("current version must not be sent again: %+v", set)
	}
	if set := beat(&csilapi.ImageSetReport{Version: version, State: "failed", Detail: strPtrImage("disk"), ReadyDigests: []string{}}); set != nil {
		t.Fatal("a failed report of the current version must not get the set again")
	}
	// An older version: the set is sent.
	if set := beat(&csilapi.ImageSetReport{Version: version - 1, State: "updated", ReadyDigests: []string{}}); set == nil || set.Version != version {
		t.Fatalf("older version: %+v", set)
	}
	// A newer version (the worker had a higher version before a new
	// enrollment): the set is sent, so that the worker takes the version
	// of the coordinator.
	if set := beat(&csilapi.ImageSetReport{Version: version + 5, State: "updated", ReadyDigests: []string{}}); set == nil || set.Version != version {
		t.Fatalf("newer version: %+v", set)
	}

	// An alias change on this replica: the next heartbeat computes a new
	// version and sends it to a worker that reports the old one.
	st.mu.Lock()
	st.aliases[0].PendingRef = strPtrImage("oci://r.example/vm/win:2@" + imgDigestB)
	st.aliases[0].PendingDigest = strPtrImage(imgDigestB)
	st.mu.Unlock()
	imageset.Invalidate()
	set := beat(&csilapi.ImageSetReport{Version: version, State: "updated", ReadyDigests: []string{imgDigestA}})
	if set == nil || set.Version != version+1 || len(set.Images) != 2 {
		t.Fatalf("new version after alias change: %+v", set)
	}
	if set := beat(&csilapi.ImageSetReport{Version: version + 1, State: "updated", ReadyDigests: []string{imgDigestA, imgDigestB}}); set != nil {
		t.Fatal("the new current version must not be sent again")
	}
}

func TestHeartbeatUsesCachedSetUntilExpiry(t *testing.T) {
	h, st := imageHarness(t)
	resp, token := registerImageWorker(t, h, "vm-worker", strPtrImage("vm"))
	clock := time.Now()
	h.service.imageSets.now = func() time.Time { return clock }
	report := &csilapi.ImageSetReport{Version: resp.ImageSet.Version, State: "updated", ReadyDigests: []string{imgDigestA}}
	beat := func() *csilapi.ImageSet {
		out, err := h.service.Heartbeat(ctxWithAuth(token), csilapi.HeartbeatRequest{Status: "idle", ImageSetReport: report})
		if err != nil {
			t.Fatal(err)
		}
		return out.ImageSet
	}
	// A change on another replica (no Invalidate here) waits for the cache.
	st.mu.Lock()
	st.aliases[0].ActiveRef, st.aliases[0].ActiveDigest = "oci://r.example/vm/win:2@"+imgDigestB, imgDigestB
	st.mu.Unlock()
	if set := beat(); set != nil {
		t.Fatal("a cached set must be used inside the recompute interval")
	}
	clock = clock.Add(imageSetRecomputeEvery)
	if set := beat(); set == nil || set.Images[0].Digest != imgDigestB {
		t.Fatalf("after the interval the set must be computed again: %+v", set)
	}
}

func TestHeartbeatReportingWorkerWithoutRuntimeIsImageAware(t *testing.T) {
	h, _ := imageHarness(t)
	_, token := registerImageWorker(t, h, "reporting-worker", nil)
	out, err := h.service.Heartbeat(ctxWithAuth(token), csilapi.HeartbeatRequest{Status: "idle",
		ImageSetReport: &csilapi.ImageSetReport{Version: 0, State: "updated", ReadyDigests: []string{}}})
	if err != nil {
		t.Fatal(err)
	}
	// A windows worker without a runtime gets the vm aliases.
	if out.ImageSet == nil || out.ImageSet.Version != 1 || len(out.ImageSet.Images) != 1 {
		t.Fatalf("image set = %+v", out.ImageSet)
	}
}

func TestBuildLeaseCarriesImageAlias(t *testing.T) {
	name := "win"
	job := &models.Job{JobID: "j1", RunnerImage: "r.example/vm/win:1@" + imgDigestA, ImageAlias: &name}
	lease := buildLease("l1", job, &resolvedLeaseSecrets{}, nil)
	if lease.ImageAlias == nil || *lease.ImageAlias != "win" || !strings.HasPrefix(lease.Image, "r.example/") {
		t.Fatalf("lease = %+v", lease)
	}
	if lease.ImageRetryOf != nil {
		t.Fatal("a job that is not a changed retry must send no image_retry_of")
	}
	job.ImageAlias = nil
	if lease := buildLease("l1", job, &resolvedLeaseSecrets{}, nil); lease.ImageAlias != nil {
		t.Fatal("a job without an alias must send no image_alias")
	}
}

// TestBuildLeaseCarriesImageRetryOf: the worker writes "retry of:" in the
// job log header, so the lease carries the earlier image.
func TestBuildLeaseCarriesImageRetryOf(t *testing.T) {
	name := "win"
	previous := "win (r.example/vm/win:0@" + imgDigestA + ")"
	job := &models.Job{JobID: "j1", RunnerImage: "r.example/vm/win:1@" + imgDigestA, ImageAlias: &name, ImageRetryOf: &previous}
	lease := buildLease("l1", job, &resolvedLeaseSecrets{}, nil)
	if lease.ImageRetryOf == nil || *lease.ImageRetryOf != previous {
		t.Fatalf("image_retry_of = %v, want %q", lease.ImageRetryOf, previous)
	}
}

func TestWireImageSetPendingOnlyWhenPending(t *testing.T) {
	out := wireImageSet(imageset.Versioned{Version: 2, Set: imageset.Set{Entries: []imageset.Entry{
		{Ref: "r/a@" + imgDigestA, Digest: imgDigestA, Kind: "vm", Aliases: []string{"win"}},
		{Ref: "r/b@" + imgDigestA, Digest: "sha256:" + strings.Repeat("e", 64), Kind: "vm", Aliases: []string{"win"}, Pending: true},
	}}})
	if out.Images[0].Pending != nil {
		t.Fatalf("an entry that is not pending carries no pending value: %+v", out.Images[0])
	}
	if out.Images[1].Pending == nil || !*out.Images[1].Pending {
		t.Fatalf("a pending entry carries pending true: %+v", out.Images[1])
	}
}
