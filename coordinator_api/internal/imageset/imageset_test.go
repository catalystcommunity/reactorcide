package imageset

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/characteristics"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

const (
	orgA  = "00000000-0000-0000-0000-00000000000a"
	orgB  = "00000000-0000-0000-0000-00000000000b"
	projA = "00000000-0000-0000-0000-0000000000a1"
)

func digest(c string) string { return "sha256:" + strings.Repeat(c, 64) }

func ref(name, c string) string { return "oci://registry.example/vm/" + name + ":1@" + digest(c) }

func strp(s string) *string { return &s }

func vmAlias(id, org string, project *string, name, os, arch, c string) models.ImageAlias {
	return models.ImageAlias{AliasID: id, OrgID: org, ProjectID: project, Name: name, Kind: models.ImageAliasKindVM, OS: os, Arch: arch,
		Characteristics: characteristics.Characteristics{}, ActiveRef: ref(name, c), ActiveDigest: digest(c)}
}

func vmWorker(id, pool string) models.Worker {
	return models.Worker{WorkerID: id, PoolID: pool, OS: "windows", Arch: "amd64", Status: models.WorkerStatusActive,
		Characteristics: characteristics.Characteristics{"os": characteristics.StringValue("windows"), "arch": characteristics.StringValue("amd64")},
		ImageRuntime:    strp(models.WorkerRuntimeVM)}
}

func orgPool(id, org string) *models.WorkerPool {
	return &models.WorkerPool{PoolID: id, OrgID: strp(org)}
}

func grant(pool, org, class string) models.PoolClassGrant {
	return models.PoolClassGrant{PoolID: pool, OrgID: org, ClassName: class}
}

func TestServesPoolBoundary(t *testing.T) {
	w := vmWorker("w1", "p1")
	alias := vmAlias("a1", orgA, nil, "win", "windows", "amd64", "1")
	otherOrg := vmAlias("a2", orgB, nil, "win", "windows", "amd64", "2")

	// An organization pool serves only its own organization, even with a
	// grant row in another organization.
	target := TargetFor(&w, orgPool("p1", orgA), []models.PoolClassGrant{grant("p1", orgA, "default"), grant("p1", orgB, "default")})
	if !target.Serves(&alias) || target.Serves(&otherOrg) {
		t.Fatalf("organization pool: serves own=%v other=%v", target.Serves(&alias), target.Serves(&otherOrg))
	}
	// A pool with no grant serves nothing.
	if TargetFor(&w, orgPool("p1", orgA), nil).Serves(&alias) {
		t.Fatal("a pool with no class grant must serve no alias")
	}
	// A hosted pool serves each organization that granted it a class.
	hosted := &models.WorkerPool{PoolID: "p1"}
	target = TargetFor(&w, hosted, []models.PoolClassGrant{grant("p1", orgB, "default")})
	if target.Serves(&alias) || !target.Serves(&otherOrg) {
		t.Fatal("hosted pool must serve only granted organizations")
	}
}

func TestServesKindPlatformAndCharacteristics(t *testing.T) {
	pool := orgPool("p1", orgA)
	grants := []models.PoolClassGrant{grant("p1", orgA, "default")}
	w := vmWorker("w1", "p1")
	w.Characteristics["xcode"] = characteristics.StringListValue{"15", "16"}
	target := TargetFor(&w, pool, grants)

	alias := vmAlias("a1", orgA, nil, "win", "windows", "amd64", "1")
	if !target.Serves(&alias) {
		t.Fatal("matching alias must be served")
	}
	container := alias
	container.Kind = models.ImageAliasKindContainer
	if target.Serves(&container) {
		t.Fatal("a vm worker must not serve a container alias")
	}
	docker := vmWorker("w2", "p1")
	docker.ImageRuntime = strp(models.WorkerRuntimeDocker)
	if !TargetFor(&docker, pool, grants).Serves(&container) || TargetFor(&docker, pool, grants).Serves(&alias) {
		t.Fatal("a docker worker serves container aliases only")
	}
	arm := alias
	arm.Arch = "arm64"
	mac := alias
	mac.OS = "macos"
	if target.Serves(&arm) || target.Serves(&mac) {
		t.Fatal("platform must match")
	}
	withChar := alias
	withChar.Characteristics = characteristics.Characteristics{"xcode": characteristics.StringValue("16")}
	if !target.Serves(&withChar) {
		t.Fatal("a list worker characteristic satisfies a member value")
	}
	withChar.Characteristics = characteristics.Characteristics{"xcode": characteristics.StringValue("14")}
	if target.Serves(&withChar) {
		t.Fatal("a missing value must not match")
	}
	withChar.Characteristics = characteristics.Characteristics{"gpu": characteristics.BoolValue(true)}
	if target.Serves(&withChar) {
		t.Fatal("a missing key must not match")
	}
	// worker_class matches the class grants of the pool.
	withChar.Characteristics = characteristics.Characteristics{WorkerClassKey: characteristics.StringValue("mac-xcode")}
	if target.Serves(&withChar) {
		t.Fatal("worker_class without a grant must not match")
	}
	classTarget := TargetFor(&w, pool, append(grants, grant("p1", orgA, "mac-xcode")))
	if !classTarget.Serves(&withChar) {
		t.Fatal("worker_class with a grant must match")
	}
	// A worker without a runtime that sent a report: vm by its os.
	legacy := vmWorker("w3", "p1")
	legacy.ImageRuntime = nil
	legacy.ImageState = strp(models.WorkerImageStateUpdated)
	if !ImageAware(&legacy) || !TargetFor(&legacy, pool, grants).Serves(&alias) {
		t.Fatal("a reporting worker with a windows os serves vm aliases")
	}
	old := vmWorker("w4", "p1")
	old.ImageRuntime = nil
	if ImageAware(&old) {
		t.Fatal("an old worker is not image aware")
	}
}

func TestComputeScopesPendingAndDrain(t *testing.T) {
	pool := orgPool("p1", orgA)
	w := vmWorker("w1", "p1")
	target := TargetFor(&w, pool, []models.PoolClassGrant{grant("p1", orgA, "default")})

	orgWin := vmAlias("a1", orgA, nil, "win", "windows", "amd64", "1")
	orgWin.PendingRef, orgWin.PendingDigest = strp(ref("win", "2")), strp(digest("2"))
	projWin := vmAlias("a2", orgA, strp(projA), "win", "windows", "amd64", "3")
	// The project alias pins the same digest as another org alias.
	other := vmAlias("a3", orgA, nil, "other", "windows", "amd64", "3")
	mac := vmAlias("a4", orgA, nil, "mac", "macos", "arm64", "4")
	aliases := []models.ImageAlias{orgWin, projWin, other, mac}
	drains := []models.DrainingImage{
		// An older digest of the organization alias: kept.
		{OrgID: orgA, AliasName: "win", Ref: "registry.example/vm/win:0@" + digest("0"), Digest: digest("0")},
		// An older digest of the project alias: kept, named "win".
		{OrgID: orgA, ProjectID: strp(projA), AliasName: "win", Ref: "registry.example/vm/win:9@" + digest("9"), Digest: digest("9")},
		// The active digest: no new entry.
		{OrgID: orgA, AliasName: "win", Ref: ref("win", "1"), Digest: digest("1")},
		// A deleted alias: not kept.
		{OrgID: orgA, AliasName: "gone", Ref: "registry.example/vm/gone@" + digest("8"), Digest: digest("8")},
		// The mac alias is not served: not kept.
		{OrgID: orgA, AliasName: "mac", Ref: "registry.example/vm/mac@" + digest("7"), Digest: digest("7")},
	}
	set := Compute(target, aliases, drains)
	got := map[string]Entry{}
	for _, entry := range set.Entries {
		got[entry.Digest] = entry
	}
	want := []string{digest("0"), digest("1"), digest("2"), digest("3"), digest("9")}
	if len(set.Entries) != len(want) {
		t.Fatalf("entries = %+v", set.Entries)
	}
	for _, d := range want {
		if _, ok := got[d]; !ok {
			t.Fatalf("missing digest %s in %+v", d, set.Entries)
		}
	}
	if names := got[digest("3")].Aliases; len(names) != 2 || names[0] != "other" || names[1] != "win" {
		t.Fatalf("aliases of shared digest = %v", names)
	}
	if e := got[digest("1")]; e.Ref != "registry.example/vm/win:1@"+digest("1") || e.Kind != "vm" {
		t.Fatalf("entry ref must be the job reference form: %+v", e)
	}

	// The hash does not depend on input order.
	reversed := []models.ImageAlias{mac, other, projWin, orgWin}
	if Compute(target, reversed, drains).Hash != set.Hash {
		t.Fatal("hash depends on input order")
	}
	// A change in the set changes the hash.
	if Compute(target, []models.ImageAlias{orgWin}, nil).Hash == set.Hash {
		t.Fatal("hash must change with the set")
	}
	if Compute(target, nil, nil).Hash == "" {
		t.Fatal("an empty set has a hash")
	}
}

// TestComputeDrainKindAndPlatform: a draining job maps to the alias of its
// name only when the kind and the platform of the alias are the kind and
// the platform of the job. A job whose alias name now has another platform
// is not drained (imagealias.CheckCarried refuses a retry of it). An entry
// uses an alias reference for a digest when an alias pins it.
func TestComputeDrainKindAndPlatform(t *testing.T) {
	pool := orgPool("p1", orgA)
	w := vmWorker("w1", "p1")
	target := TargetFor(&w, pool, []models.PoolClassGrant{grant("p1", orgA, "default")})
	win := vmAlias("a1", orgA, nil, "win", "windows", "amd64", "1")
	winChars := characteristics.Characteristics{"os": characteristics.StringValue("windows"), "arch": characteristics.StringValue("amd64")}
	armChars := characteristics.Characteristics{"os": characteristics.StringValue("windows"), "arch": characteristics.StringValue("arm64")}
	drains := []models.DrainingImage{
		{OrgID: orgA, AliasName: "win", Ref: "registry.example/vm/win:0@" + digest("0"), Digest: digest("0"), Characteristics: winChars},
		// The job was for arm64; "win" is now an amd64 alias: not drained.
		{OrgID: orgA, AliasName: "win", Ref: "registry.example/vm/win:arm@" + digest("8"), Digest: digest("8"), Characteristics: armChars},
		// A job that uses the active digest at another location: the entry
		// keeps the alias reference.
		{OrgID: orgA, AliasName: "win", Ref: "mirror.example/vm/win:1@" + digest("1"), Digest: digest("1"), Characteristics: winChars},
	}
	set := Compute(target, []models.ImageAlias{win}, drains)
	got := map[string]Entry{}
	for _, entry := range set.Entries {
		got[entry.Digest] = entry
	}
	if len(got) != 2 || got[digest("0")].Digest == "" || got[digest("1")].Digest == "" {
		t.Fatalf("entries = %+v", set.Entries)
	}
	if got[digest("1")].Ref != "registry.example/vm/win:1@"+digest("1") {
		t.Fatalf("the alias reference must win over a job reference: %+v", got[digest("1")])
	}
}

func TestComputePendingMarker(t *testing.T) {
	pool := orgPool("p1", orgA)
	w := vmWorker("w1", "p1")
	target := TargetFor(&w, pool, []models.PoolClassGrant{grant("p1", orgA, "default")})

	win := vmAlias("a1", orgA, nil, "win", "windows", "amd64", "1")
	win.PendingRef, win.PendingDigest = strp(ref("win", "2")), strp(digest("2"))
	// Another alias has digest 3 active; it is pending for win2.
	win2 := vmAlias("a2", orgA, nil, "win2", "windows", "amd64", "3")
	other := vmAlias("a3", orgA, nil, "other", "windows", "amd64", "4")
	other.PendingRef, other.PendingDigest = strp(ref("other", "3")), strp(digest("3"))
	// A pending digest that a job still uses (an older pin of the alias,
	// set again) is not pending.
	third := vmAlias("a4", orgA, nil, "third", "windows", "amd64", "5")
	third.PendingRef, third.PendingDigest = strp(ref("third", "6")), strp(digest("6"))
	drains := []models.DrainingImage{{OrgID: orgA, AliasName: "third", Ref: ref("third", "6"), Digest: digest("6")}}

	set := Compute(target, []models.ImageAlias{win, win2, other, third}, drains)
	pending := map[string]bool{}
	for _, entry := range set.Entries {
		pending[entry.Digest] = entry.Pending
	}
	want := map[string]bool{digest("1"): false, digest("2"): true, digest("3"): false, digest("4"): false, digest("5"): false, digest("6"): false}
	if len(pending) != len(want) {
		t.Fatalf("entries = %+v", set.Entries)
	}
	for d, p := range want {
		if pending[d] != p {
			t.Fatalf("digest %s pending = %v, want %v", d, pending[d], p)
		}
	}

	// A set without pending entries hashes the same as before the marker,
	// and a promotion (pending to active, same digests) changes the hash.
	plain := []Entry{{Ref: "r", Digest: digest("1"), Kind: "vm", Aliases: []string{"win"}}}
	marked := []Entry{{Ref: "r", Digest: digest("1"), Kind: "vm", Aliases: []string{"win"}, Pending: true}}
	if Hash(plain) == Hash(marked) {
		t.Fatal("the pending marker must change the hash")
	}
}

func TestComputeDrainsDirectPinnedReference(t *testing.T) {
	pool := orgPool("p1", orgA)
	w := vmWorker("w1", "p1")
	w.Characteristics["xcode"] = characteristics.StringListValue{"15", "16"}
	target := TargetFor(&w, pool, []models.PoolClassGrant{grant("p1", orgA, "default")})

	jobChars := func(extra map[string]characteristics.Value) characteristics.Characteristics {
		c := characteristics.Characteristics{
			"os": characteristics.StringValue("windows"), "arch": characteristics.StringValue("amd64"),
			"image_digest": characteristics.StringValue(digest("7")),
		}
		for k, v := range extra {
			c[k] = v
		}
		return c
	}
	direct := func(class string, chars characteristics.Characteristics) models.DrainingImage {
		return models.DrainingImage{OrgID: orgA, WorkerClass: class, Characteristics: chars,
			Ref: "registry.example/vm/win:7@" + digest("7"), Digest: digest("7")}
	}
	cases := []struct {
		name  string
		image models.DrainingImage
		want  bool
	}{
		{"matching job", direct("default", jobChars(nil)), true},
		{"empty class is default", direct("", jobChars(nil)), true},
		{"member of a list characteristic", direct("default", jobChars(map[string]characteristics.Value{"xcode": characteristics.StringValue("16")})), true},
		{"class without a grant", direct("mac-xcode", jobChars(nil)), false},
		{"other platform", direct("default", jobChars(map[string]characteristics.Value{"os": characteristics.StringValue("macos")})), false},
		{"missing characteristic", direct("default", jobChars(map[string]characteristics.Value{"gpu": characteristics.BoolValue(true)})), false},
	}
	for _, tc := range cases {
		set := Compute(target, nil, []models.DrainingImage{tc.image})
		if got := len(set.Entries) == 1; got != tc.want {
			t.Fatalf("%s: entries = %+v, want kept = %v", tc.name, set.Entries, tc.want)
		}
		if tc.want {
			e := set.Entries[0]
			if e.Pending || e.Kind != models.ImageAliasKindVM || len(e.Aliases) != 0 || e.Ref != tc.image.Ref {
				t.Fatalf("%s: entry = %+v", tc.name, e)
			}
		}
	}
	// Another organization's job is not kept.
	other := direct("default", jobChars(nil))
	other.OrgID = orgB
	if len(Compute(target, nil, []models.DrainingImage{other}).Entries) != 0 {
		t.Fatal("a job of an organization that the pool does not serve must not be kept")
	}
	// A container worker does not keep a VM job digest.
	docker := vmWorker("w2", "p1")
	docker.ImageRuntime = strp(models.WorkerRuntimeDocker)
	dt := TargetFor(&docker, pool, []models.PoolClassGrant{grant("p1", orgA, "default")})
	if len(Compute(dt, nil, []models.DrainingImage{direct("default", jobChars(nil))}).Entries) != 0 {
		t.Fatal("a container worker must not keep a VM job digest")
	}
}

// --- fake store --------------------------------------------------------

type fakeStore struct {
	mu       sync.Mutex
	pools    map[string]*models.WorkerPool
	grants   []models.PoolClassGrant
	aliases  map[string]*models.ImageAlias
	drains   []models.DrainingImage
	workers  map[string]*models.Worker
	audits   []*models.AuditEvent
	setCalls int
	// beforePromote runs before a promotion (concurrency tests).
	beforePromote func()
}

func newFake() *fakeStore {
	return &fakeStore{pools: map[string]*models.WorkerPool{}, aliases: map[string]*models.ImageAlias{}, workers: map[string]*models.Worker{}}
}

func (f *fakeStore) ListPoolClassGrants(_ context.Context, poolIDs []string) ([]models.PoolClassGrant, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if poolIDs == nil {
		return append([]models.PoolClassGrant(nil), f.grants...), nil
	}
	var out []models.PoolClassGrant
	for _, g := range f.grants {
		for _, id := range poolIDs {
			if g.PoolID == id {
				out = append(out, g)
			}
		}
	}
	return out, nil
}

func (f *fakeStore) ListImageAliasesForOrgs(_ context.Context, orgIDs []string) ([]models.ImageAlias, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []models.ImageAlias
	for _, a := range f.aliases {
		for _, id := range orgIDs {
			if a.OrgID == id {
				out = append(out, *a)
			}
		}
	}
	return out, nil
}

func (f *fakeStore) ListPendingImageAliases(context.Context) ([]models.ImageAlias, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []models.ImageAlias
	for _, a := range f.aliases {
		if a.PendingDigest != nil {
			out = append(out, *a)
		}
	}
	return out, nil
}

func (f *fakeStore) ListDrainingImages(context.Context, []string) ([]models.DrainingImage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]models.DrainingImage(nil), f.drains...), nil
}

func (f *fakeStore) LockWorkerImageSet(_ context.Context, workerID string) (*models.Worker, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	w, ok := f.workers[workerID]
	if !ok {
		return nil, store.ErrNotFound
	}
	copy := *w
	return &copy, nil
}

func (f *fakeStore) SetWorkerImageSet(_ context.Context, workerID string, version int64, hash string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.setCalls++
	w := f.workers[workerID]
	w.ImageSetVersion, w.ImageSetHash = version, &hash
	return nil
}

func (f *fakeStore) UpdateWorkerImageState(context.Context, string, models.WorkerImageState) error {
	return nil
}

func (f *fakeStore) GetWorkerImageState(context.Context, string) (*models.WorkerImageState, error) {
	return nil, nil
}

func (f *fakeStore) GetWorkerPoolByID(_ context.Context, poolID string) (*models.WorkerPool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if p, ok := f.pools[poolID]; ok {
		return p, nil
	}
	return nil, store.ErrNotFound
}

func (f *fakeStore) ListWorkers(context.Context, *string) ([]models.Worker, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []models.Worker
	for _, w := range f.workers {
		out = append(out, *w)
	}
	return out, nil
}

func (f *fakeStore) ListWorkerPools(context.Context, *string) ([]models.WorkerPool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []models.WorkerPool
	for _, p := range f.pools {
		out = append(out, *p)
	}
	return out, nil
}

func (f *fakeStore) PromoteImageAliasPending(_ context.Context, aliasID, expected string) (*models.ImageAlias, bool, error) {
	if f.beforePromote != nil {
		f.beforePromote()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	a, ok := f.aliases[aliasID]
	if !ok {
		return nil, false, store.ErrNotFound
	}
	if a.PendingDigest == nil || *a.PendingDigest != expected {
		copy := *a
		return &copy, false, nil
	}
	a.ActiveRef, a.ActiveDigest = *a.PendingRef, *a.PendingDigest
	a.PendingRef, a.PendingDigest, a.PendingSince = nil, nil, nil
	copy := *a
	return &copy, true, nil
}

func (f *fakeStore) AppendAuditEvent(_ context.Context, event *models.AuditEvent) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.audits = append(f.audits, event)
	return nil
}

func TestApplyIncreasesVersionOnlyOnChange(t *testing.T) {
	f := newFake()
	f.pools["p1"] = orgPool("p1", orgA)
	f.grants = []models.PoolClassGrant{grant("p1", orgA, "default")}
	w := vmWorker("w1", "p1")
	f.workers["w1"] = &w
	alias := vmAlias("a1", orgA, nil, "win", "windows", "amd64", "1")
	f.aliases["a1"] = &alias
	ctx := context.Background()

	first, err := Apply(ctx, f, "w1")
	if err != nil || first.Version != 1 || !first.Changed || len(first.Set.Entries) != 1 {
		t.Fatalf("first apply = %+v %v", first, err)
	}
	again, err := Apply(ctx, f, "w1")
	if err != nil || again.Version != 1 || again.Changed || f.setCalls != 1 {
		t.Fatalf("same set must keep the version: %+v calls=%d", again, f.setCalls)
	}
	f.aliases["a1"].PendingRef, f.aliases["a1"].PendingDigest = strp(ref("win", "2")), strp(digest("2"))
	changed, err := Apply(ctx, f, "w1")
	if err != nil || changed.Version != 2 || !changed.Changed || len(changed.Set.Entries) != 2 {
		t.Fatalf("changed set = %+v %v", changed, err)
	}
	// An empty set for a worker that serves nothing is version 1 too.
	empty := vmWorker("w2", "p1")
	empty.OS = "linux"
	empty.Characteristics = characteristics.Characteristics{}
	f.workers["w2"] = &empty
	if got, _ := Apply(ctx, f, "w2"); got.Version != 1 || len(got.Set.Entries) != 0 {
		t.Fatalf("empty set = %+v", got)
	}
}

func TestReadiness(t *testing.T) {
	w := vmWorker("w1", "p1")
	w.ImageSetVersion = 3
	if status, _, ok := Readiness(&w); !ok || status != ReadinessUpdating {
		t.Fatalf("no report: %q", status)
	}
	v := int64(2)
	w.ImageState, w.ImageStateVersion = strp(models.WorkerImageStateUpdated), &v
	if status, _, _ := Readiness(&w); status != ReadinessUpdating {
		t.Fatalf("older version: %q", status)
	}
	v = 3
	if status, _, _ := Readiness(&w); status != ReadinessHealthy {
		t.Fatalf("current version: %q", status)
	}
	w.ImageState, w.ImageStateDetail = strp(models.WorkerImageStateFailed), strp("no space left")
	if status, detail, _ := Readiness(&w); status != ReadinessFailed || detail != "no space left" {
		t.Fatalf("failed: %q %q", status, detail)
	}
	old := vmWorker("w2", "p1")
	old.ImageRuntime = nil
	if _, _, ok := Readiness(&old); ok {
		t.Fatal("an old worker has no readiness")
	}
}

// --- rollout -----------------------------------------------------------

var now = time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)

func onlineWorker(id string, ready ...string) *models.Worker {
	w := vmWorker(id, "p1")
	seen := now.Add(-10 * time.Second)
	w.LastSeenAt, w.HasActiveSession = &seen, true
	w.ImageState = strp(models.WorkerImageStateUpdated)
	w.ImageReadyDigests = ready
	return &w
}

func rolloutFake() *fakeStore {
	f := newFake()
	f.pools["p1"] = orgPool("p1", orgA)
	f.grants = []models.PoolClassGrant{grant("p1", orgA, "default")}
	alias := vmAlias("a1", orgA, nil, "win", "windows", "amd64", "1")
	alias.PendingRef, alias.PendingDigest = strp(ref("win", "2")), strp(digest("2"))
	f.aliases["a1"] = &alias
	return f
}

func promoter(f *fakeStore) *Promoter {
	return &Promoter{Store: f, Now: func() time.Time { return now }}
}

func TestPromoteWhenAllOnlineWorkersReady(t *testing.T) {
	f := rolloutFake()
	f.workers["w1"] = onlineWorker("w1", digest("1"), digest("2"))
	f.workers["w2"] = onlineWorker("w2", digest("2"))
	// An offline worker does not block the promotion.
	offline := onlineWorker("w3")
	old := now.Add(-5 * time.Minute)
	offline.LastSeenAt = &old
	f.workers["w3"] = offline
	// An old worker (not image aware) does not block it.
	legacy := onlineWorker("w4")
	legacy.ImageRuntime, legacy.ImageState = nil, nil
	f.workers["w4"] = legacy

	generation := Generation()
	promoted, err := promoter(f).Tick(context.Background())
	if err != nil || len(promoted) != 1 {
		t.Fatalf("promoted = %+v err = %v", promoted, err)
	}
	a := f.aliases["a1"]
	if a.ActiveDigest != digest("2") || a.PendingDigest != nil {
		t.Fatalf("alias after promotion = %+v", a)
	}
	if Generation() == generation {
		t.Fatal("a promotion must invalidate cached image sets")
	}
	if len(f.audits) != 1 || f.audits[0].Action != "image_alias.promote" || f.audits[0].Details["old_digest"] != digest("1") {
		t.Fatalf("audit = %+v", f.audits)
	}
}

func TestNoPromotionWhenOneWorkerNotReady(t *testing.T) {
	f := rolloutFake()
	f.workers["w1"] = onlineWorker("w1", digest("2"))
	// Does not have the pending digest yet.
	updating := onlineWorker("w2", digest("1"))
	updating.ImageState = strp(models.WorkerImageStateUpdating)
	f.workers["w2"] = updating
	promoted, err := promoter(f).Tick(context.Background())
	if err != nil || len(promoted) != 0 || f.aliases["a1"].PendingDigest == nil {
		t.Fatalf("must not promote: %+v %v", promoted, err)
	}
	fleet, _ := LoadFleet(context.Background(), f)
	rollout := EvaluateRollout(f.aliases["a1"], fleet, now)
	if rollout.Blocked != BlockedNotReady || rollout.Online != 2 || rollout.Ready != 1 {
		t.Fatalf("rollout = %+v", rollout)
	}
	// A permanent failure of the pending digest is a problem for the
	// alias owner, and the reason that the rollout is blocked.
	f.workers["w2"].ImageState = strp(models.WorkerImageStateFailed)
	f.workers["w2"].ImageFailed = []models.ImageFailure{{Digest: digest("2"), Permanent: true, Detail: "manifest unknown"}}
	if promoted, _ := promoter(f).Tick(context.Background()); len(promoted) != 0 {
		t.Fatal("a worker without the pending image must block the promotion")
	}
	fleet, _ = LoadFleet(context.Background(), f)
	rollout = EvaluateRollout(f.aliases["a1"], fleet, now)
	if rollout.Blocked != BlockedPendingPermanent || rollout.PendingFailedPermanent != 1 || len(rollout.Problems()) != 1 {
		t.Fatalf("rollout = %+v problems = %v", rollout, rollout.Problems())
	}
}

// TestPromotionLooksOnlyAtPendingDigest: on a hosted pool that serves two
// organizations, a failed image of an alias of organization B does not stop
// the promotion of an alias of organization A. Readiness is the pending
// digest in the ready digests, not the overall state.
func TestPromotionLooksOnlyAtPendingDigest(t *testing.T) {
	f := newFake()
	f.pools["hosted"] = &models.WorkerPool{PoolID: "hosted", Name: "hosted-win"}
	f.grants = []models.PoolClassGrant{grant("hosted", orgA, "default"), grant("hosted", orgB, "default")}
	aliasA := vmAlias("a1", orgA, nil, "win", "windows", "amd64", "1")
	aliasA.PendingRef, aliasA.PendingDigest = strp(ref("win", "2")), strp(digest("2"))
	f.aliases["a1"] = &aliasA
	aliasB := vmAlias("b1", orgB, nil, "bad", "windows", "amd64", "9")
	f.aliases["b1"] = &aliasB
	for _, id := range []string{"w1", "w2"} {
		w := onlineWorker(id, digest("1"), digest("2"))
		w.PoolID = "hosted"
		// The image of organization B fails permanently on each worker.
		w.ImageState = strp(models.WorkerImageStateFailed)
		w.ImageFailed = []models.ImageFailure{{Digest: digest("9"), Permanent: true, Detail: "denied"}}
		f.workers[id] = w
	}
	promoted, err := promoter(f).Tick(context.Background())
	if err != nil || len(promoted) != 1 || promoted[0].AliasID != "a1" {
		t.Fatalf("alias of organization A must be promoted: %+v %v", promoted, err)
	}
	fleet, _ := LoadFleet(context.Background(), f)
	rollout := EvaluateRollout(f.aliases["b1"], fleet, now)
	if rollout.ActiveFailedPermanent != 2 || len(rollout.Problems()) != 1 || !strings.Contains(rollout.Problems()[0], "cannot pull the active image") {
		t.Fatalf("the failed active pin of B must be a problem: %+v %v", rollout, rollout.Problems())
	}
}

func TestNoPromotionWithoutOnlineWorkers(t *testing.T) {
	f := rolloutFake()
	w := onlineWorker("w1", digest("2"))
	w.HasActiveSession = false
	f.workers["w1"] = w
	// A disabled worker is not online.
	disabled := onlineWorker("w2", digest("2"))
	disabled.Status = models.WorkerStatusDisabled
	f.workers["w2"] = disabled
	promoted, err := promoter(f).Tick(context.Background())
	if err != nil || len(promoted) != 0 {
		t.Fatalf("must not promote: %+v %v", promoted, err)
	}
	fleet, _ := LoadFleet(context.Background(), f)
	if rollout := EvaluateRollout(f.aliases["a1"], fleet, now); rollout.Blocked != BlockedNoWorkers {
		t.Fatalf("rollout = %+v", rollout)
	}
}

func TestPromotionGuardedAgainstConcurrentChange(t *testing.T) {
	f := rolloutFake()
	f.workers["w1"] = onlineWorker("w1", digest("2"))
	// Another replica or an alias set gives a new pending pin after the
	// evaluation and before the promotion.
	f.beforePromote = func() {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.aliases["a1"].PendingRef, f.aliases["a1"].PendingDigest = strp(ref("win", "3")), strp(digest("3"))
	}
	promoted, err := promoter(f).Tick(context.Background())
	if err != nil || len(promoted) != 0 {
		t.Fatalf("must not promote a changed pending pin: %+v %v", promoted, err)
	}
	if a := f.aliases["a1"]; a.ActiveDigest != digest("1") || *a.PendingDigest != digest("3") {
		t.Fatalf("alias = %+v", a)
	}
	if len(f.audits) != 0 {
		t.Fatal("no audit for a promotion that did not happen")
	}
}
