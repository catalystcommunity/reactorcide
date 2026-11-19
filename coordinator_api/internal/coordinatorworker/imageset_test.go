package coordinatorworker

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/characteristics"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/worker"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/workerclient/csilapi"
	"github.com/stretchr/testify/require"
)

const (
	dgA = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	dgB = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	dgC = "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	dgV = "sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
)

func entry(dgst, kind string) csilapi.ImageSetEntry {
	return csilapi.ImageSetEntry{Ref: "registry.example/img@" + dgst, Digest: dgst, Kind: kind, Aliases: []string{"alias-" + dgst[7:9]}}
}

func imageSet(version int64, entries ...csilapi.ImageSetEntry) *csilapi.ImageSet {
	return &csilapi.ImageSet{Version: version, Images: entries}
}

// fakeSeeder is a JobRunner with the ImageSetSeeder capability. A pull of
// a digest in block waits until the channel is closed; a digest in fail
// returns that error.
type fakeSeeder struct {
	*fakeRunner
	mu       sync.Mutex
	block    map[string]chan struct{}
	fail     map[string]error
	notOwned map[string]bool
	space    worker.ImageSpace
	pulls    []string
	removed  []string
	kept     []string
	attempts int
	attemptT []time.Time
	// present digests are ready without a pull (like an image that the
	// runtime has). A pulled digest becomes present.
	present   map[string]bool
	cancelled []string
	skipped   []string
}

func newFakeSeeder() *fakeSeeder {
	return &fakeSeeder{fakeRunner: &fakeRunner{}, block: map[string]chan struct{}{}, fail: map[string]error{}, notOwned: map[string]bool{}, present: map[string]bool{}}
}

func (f *fakeSeeder) ImageSetKind() string { return worker.ImageKindContainer }

func (f *fakeSeeder) ImageSpace(context.Context, []worker.ImageSetImage) (worker.ImageSpace, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.space, nil
}

func (f *fakeSeeder) SeedImages(ctx context.Context, images []worker.ImageSetImage, hooks worker.ImageSeedHooks) error {
	f.mu.Lock()
	f.attempts++
	f.attemptT = append(f.attemptT, time.Now())
	f.mu.Unlock()
	for _, img := range images {
		if err := ctx.Err(); err != nil {
			return err
		}
		imgCtx := ctx
		if hooks.ImageContext != nil {
			if c := hooks.ImageContext(img); c != nil {
				imgCtx = c
			}
		}
		if imgCtx.Err() != nil {
			f.mu.Lock()
			f.skipped = append(f.skipped, img.Digest)
			f.mu.Unlock()
			continue
		}
		f.mu.Lock()
		block, err, owned, present := f.block[img.Digest], f.fail[img.Digest], !f.notOwned[img.Digest], f.present[img.Digest]
		f.mu.Unlock()
		if present {
			hooks.Ready(img, false)
			continue
		}
		if block != nil {
			select {
			case <-block:
			case <-imgCtx.Done():
				f.mu.Lock()
				f.cancelled = append(f.cancelled, img.Digest)
				f.mu.Unlock()
				continue
			}
		}
		if err != nil {
			hooks.Failed(img, err)
			continue
		}
		f.mu.Lock()
		f.pulls = append(f.pulls, img.Digest)
		f.present[img.Digest] = true
		f.mu.Unlock()
		hooks.Ready(img, owned)
	}
	return ctx.Err()
}

func (f *fakeSeeder) KeepImages(digests []string) {
	f.mu.Lock()
	f.kept = append([]string(nil), digests...)
	f.mu.Unlock()
}

func (f *fakeSeeder) RemoveImages(_ context.Context, images []worker.ImageSetImage) ([]worker.ImageSetImage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, img := range images {
		f.removed = append(f.removed, img.Digest)
	}
	return nil, nil
}

func (f *fakeSeeder) snapshot() (pulls, removed, kept []string, attempts int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.pulls...), append([]string(nil), f.removed...), append([]string(nil), f.kept...), f.attempts
}

func (f *fakeSeeder) setFail(dgst string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err == nil {
		delete(f.fail, dgst)
	} else {
		f.fail[dgst] = err
	}
}

func startManager(t *testing.T, m *imageSetManager) context.CancelFunc {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { m.run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	return cancel
}

func reportIs(m *imageSetManager, version int64, state string) func() bool {
	return func() bool {
		r := m.heartbeatReport()
		return r.Version == version && r.State == state
	}
}

func digestChar(wc csilapi.WorkerCharacteristics) []string {
	for _, c := range wc.Custom {
		if c.Key == "image_digest" {
			v, _ := c.Value.([]string)
			return v
		}
	}
	return nil
}

// TestImageSetStartupGate: no RequestJob before the set is complete, the
// heartbeat reports "updating" while the worker pulls, and RequestJob then
// sends every ready digest as the image_digest list.
func TestImageSetStartupGate(t *testing.T) {
	seeder := newFakeSeeder()
	release := make(chan struct{})
	seeder.block[dgB] = release

	fc := &fakeClient{}
	fc.RegisterFunc = func(context.Context, string, csilapi.WorkerInfo) (csilapi.RegisterResponse, error) {
		return csilapi.RegisterResponse{WorkerSession: "s", WorkerId: "w", HeartbeatInterval: 1,
			ImageSet: imageSet(3, entry(dgA, "container"), entry(dgB, "container"), entry(dgV, "vm"))}, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- runLoop(ctx, testConfig(), fc, runnerFactoryFor(seeder)) }()

	updating := func() bool {
		fc.mu.Lock()
		defer fc.mu.Unlock()
		for _, hb := range fc.HeartbeatCalls {
			if hb.Report != nil && hb.Report.Version == 3 && hb.Report.State == imageStateUpdating {
				return true
			}
		}
		return false
	}
	require.Eventually(t, updating, 5*time.Second, 20*time.Millisecond, "heartbeat must report updating 3")
	require.Zero(t, fc.snapshotRequestJobCalls(), "no RequestJob before the image set is complete")

	close(release)
	require.Eventually(t, func() bool { return fc.snapshotRequestJobCalls() > 0 }, 5*time.Second, 20*time.Millisecond)
	fc.mu.Lock()
	first := fc.RequestJobCalls[0]
	fc.mu.Unlock()
	require.Equal(t, []string{dgA, dgB}, digestChar(first), "the vm entry is not for this backend")

	updated := func() bool {
		fc.mu.Lock()
		defer fc.mu.Unlock()
		for _, hb := range fc.HeartbeatCalls {
			if hb.Report != nil && hb.Report.Version == 3 && hb.Report.State == imageStateUpdated {
				return len(hb.Report.ReadyDigests) == 2
			}
		}
		return false
	}
	require.Eventually(t, updated, 5*time.Second, 20*time.Millisecond, "heartbeat must report updated 3")
	cancel()
	require.ErrorIs(t, <-done, context.Canceled)
}

// TestImageSetNoSetOpensGate: a worker with no image set reports version 0
// updated and requests jobs at once (no image_digest characteristic).
func TestImageSetNoSetOpensGate(t *testing.T) {
	fc := &fakeClient{}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- runLoop(ctx, testConfig(), fc, runnerFactoryFor(&fakeRunner{})) }()
	require.Eventually(t, func() bool { return fc.snapshotRequestJobCalls() > 0 }, 3*time.Second, 10*time.Millisecond)
	fc.mu.Lock()
	require.Nil(t, digestChar(fc.RequestJobCalls[0]))
	fc.mu.Unlock()
	cancel()
	<-done
}

// TestImageSetLiveUpdate: during an update the worker keeps the old
// digests, reports updating N, and then updated N. An owned image that
// left the set is removed.
func TestImageSetLiveUpdate(t *testing.T) {
	seeder := newFakeSeeder()
	m := newImageSetManager(Config{}, seeder)
	startManager(t, m)

	m.offer(imageSet(1, entry(dgA, "container")))
	require.Eventually(t, reportIs(m, 1, imageStateUpdated), 2*time.Second, 5*time.Millisecond)
	require.Equal(t, []string{dgA}, m.digestCharacteristic())

	release := make(chan struct{})
	seeder.mu.Lock()
	seeder.block[dgB] = release
	seeder.mu.Unlock()
	m.offer(imageSet(2, entry(dgB, "container")))
	require.Eventually(t, reportIs(m, 2, imageStateUpdating), 2*time.Second, 5*time.Millisecond)
	require.Equal(t, []string{dgA}, m.digestCharacteristic(), "the worker keeps claiming jobs for the image that it has")
	_, _, kept, _ := seeder.snapshot()
	require.Subset(t, kept, []string{dgA, dgB}, "retention keeps the old and the new set during the update")

	close(release)
	require.Eventually(t, reportIs(m, 2, imageStateUpdated), 2*time.Second, 5*time.Millisecond)
	require.Equal(t, []string{dgB}, m.digestCharacteristic())
	require.Eventually(t, func() bool {
		_, removed, _, _ := seeder.snapshot()
		return len(removed) == 1 && removed[0] == dgA
	}, 2*time.Second, 5*time.Millisecond, "the owned image that left the set is removed")
}

// TestImageSetSupersede: a new set stops the pull of a digest that left the
// set; the older version is never reported as failed.
func TestImageSetSupersede(t *testing.T) {
	seeder := newFakeSeeder()
	seeder.block[dgB] = make(chan struct{}) // never released
	m := newImageSetManager(Config{}, seeder)
	startManager(t, m)

	m.offer(imageSet(2, entry(dgB, "container")))
	require.Eventually(t, reportIs(m, 2, imageStateUpdating), 2*time.Second, 5*time.Millisecond)
	m.offer(imageSet(3, entry(dgC, "container")))
	require.Eventually(t, reportIs(m, 3, imageStateUpdated), 2*time.Second, 5*time.Millisecond)
	require.Equal(t, []string{dgC}, m.digestCharacteristic())
	pulls, _, _, _ := seeder.snapshot()
	require.Equal(t, []string{dgC}, pulls)
	seeder.mu.Lock()
	require.Equal(t, []string{dgB}, seeder.cancelled, "the pull of the digest that left the set stops")
	seeder.mu.Unlock()
	require.Empty(t, m.heartbeatReport().Failed)
}

// TestImageSetSupersedeKeepsInFlightPulls: a new set that still has the
// digest of a pull in progress does not stop that pull. The pull counts
// for the new set.
func TestImageSetSupersedeKeepsInFlightPulls(t *testing.T) {
	seeder := newFakeSeeder()
	release := make(chan struct{})
	seeder.block[dgA] = release
	m := newImageSetManager(Config{}, seeder)
	startManager(t, m)

	m.offer(imageSet(1, entry(dgA, "container")))
	require.Eventually(t, reportIs(m, 1, imageStateUpdating), 2*time.Second, 5*time.Millisecond)
	m.offer(imageSet(2, entry(dgA, "container"), entry(dgC, "container")))
	time.Sleep(20 * time.Millisecond)
	close(release)
	require.Eventually(t, reportIs(m, 2, imageStateUpdated), 2*time.Second, 5*time.Millisecond)
	require.Equal(t, []string{dgA, dgC}, m.digestCharacteristic())
	pulls, _, _, _ := seeder.snapshot()
	require.Equal(t, []string{dgA, dgC}, pulls, "A is pulled one time")
	seeder.mu.Lock()
	require.Empty(t, seeder.cancelled, "the pull of A continued")
	seeder.mu.Unlock()
}

// TestImageSetLowerVersion: after a new enrollment the coordinator version
// can be lower. A set with other content is accepted; a set with the same
// content only takes the new version number (no new pull).
func TestImageSetLowerVersion(t *testing.T) {
	seeder := newFakeSeeder()
	m := newImageSetManager(Config{}, seeder)
	startManager(t, m)

	m.offer(imageSet(5, entry(dgA, "container")))
	require.Eventually(t, reportIs(m, 5, imageStateUpdated), 2*time.Second, 5*time.Millisecond)

	m.offer(imageSet(1, entry(dgB, "container")))
	require.Eventually(t, reportIs(m, 1, imageStateUpdated), 2*time.Second, 5*time.Millisecond)
	require.Equal(t, []string{dgB}, m.digestCharacteristic())

	_, _, _, before := seeder.snapshot()
	m.offer(imageSet(7, entry(dgB, "container")))
	require.Eventually(t, reportIs(m, 7, imageStateUpdated), 2*time.Second, 5*time.Millisecond)
	m.offer(imageSet(7, entry(dgB, "container")))
	time.Sleep(20 * time.Millisecond)
	_, _, _, after := seeder.snapshot()
	require.Equal(t, before, after, "the same content is not pulled again")
}

// TestImageSetOnePermanentFailure: one needed image that fails permanently
// does not stop the others, and the gate opens: the worker never advertises
// the failed digest, so it gets no job for it. The report is failed with
// the failure of that digest.
func TestImageSetOnePermanentFailure(t *testing.T) {
	seeder := newFakeSeeder()
	seeder.setFail(dgB, worker.PermanentPullError(errors.New("manifest unknown")))
	m := newImageSetManager(Config{}, seeder)
	m.retryMin = time.Hour
	startManager(t, m)

	m.offer(imageSet(1, entry(dgA, "container"), entry(dgB, "container"), entry(dgC, "container")))
	require.Eventually(t, reportIs(m, 1, imageStateFailed), 2*time.Second, 5*time.Millisecond)
	require.Eventually(t, gateIsOpen(m), 2*time.Second, 5*time.Millisecond)
	require.Equal(t, []string{dgA, dgC}, m.digestCharacteristic())
	r := m.heartbeatReport()
	require.Equal(t, []csilapi.ImageFailure{{Digest: dgB, Permanent: true, Detail: "manifest unknown"}}, r.Failed)
	require.Contains(t, *r.Detail, "1 with a permanent failure")
	pulls, _, _, _ := seeder.snapshot()
	require.Equal(t, []string{dgA, dgC}, pulls)
}

// TestImageSetTransientFailureKeepsGateClosed: a transient failure of a
// needed image keeps the gate closed, also when the other images are ready.
func TestImageSetTransientFailureKeepsGateClosed(t *testing.T) {
	seeder := newFakeSeeder()
	seeder.setFail(dgB, errors.New("dial tcp: i/o timeout"))
	m := newImageSetManager(Config{}, seeder)
	m.retryMin, m.retryMax = 10*time.Millisecond, 20*time.Millisecond
	startManager(t, m)

	m.offer(imageSet(1, entry(dgA, "container"), entry(dgB, "container"), entry(dgC, "container")))
	require.Eventually(t, reportIs(m, 1, imageStateFailed), 2*time.Second, 5*time.Millisecond)
	require.Eventually(t, func() bool { _, _, _, n := seeder.snapshot(); return n >= 3 }, 2*time.Second, 5*time.Millisecond, "the worker tries again")
	require.False(t, m.gateOpen())
	r := m.heartbeatReport()
	require.Len(t, r.Failed, 1)
	require.False(t, r.Failed[0].Permanent)
	require.Equal(t, []string{dgA, dgC}, r.ReadyDigests)

	seeder.setFail(dgB, nil)
	require.Eventually(t, reportIs(m, 1, imageStateUpdated), 2*time.Second, 5*time.Millisecond)
	require.True(t, m.gateOpen())
	require.Empty(t, m.heartbeatReport().Failed)
}

// TestImageSetFailureKeepsOldAndBacksOff: a failed pull reports failed with
// a detail, keeps the old digests, and tries again with a growing delay.
func TestImageSetFailureKeepsOldAndBacksOff(t *testing.T) {
	seeder := newFakeSeeder()
	m := newImageSetManager(Config{}, seeder)
	m.retryMin, m.retryMax = 20*time.Millisecond, 200*time.Millisecond
	startManager(t, m)

	m.offer(imageSet(1, entry(dgA, "container")))
	require.Eventually(t, reportIs(m, 1, imageStateUpdated), 2*time.Second, 5*time.Millisecond)

	seeder.setFail(dgB, errors.New("registry unavailable"))
	m.offer(imageSet(2, entry(dgA, "container"), entry(dgB, "container")))
	require.Eventually(t, reportIs(m, 2, imageStateFailed), 2*time.Second, 5*time.Millisecond)
	r := m.heartbeatReport()
	require.NotNil(t, r.Detail)
	require.Contains(t, *r.Detail, "registry unavailable")
	require.Equal(t, []string{dgA}, r.ReadyDigests)
	require.Equal(t, []string{dgA}, m.digestCharacteristic(), "the old image stays ready")

	require.Eventually(t, func() bool { _, _, _, n := seeder.snapshot(); return n >= 4 }, 3*time.Second, 5*time.Millisecond)
	seeder.mu.Lock()
	times := append([]time.Time(nil), seeder.attemptT...)
	seeder.mu.Unlock()
	// attempts: v1, v2 #1, v2 #2 (after 20ms), v2 #3 (after 40ms).
	require.GreaterOrEqual(t, times[3].Sub(times[2]), times[2].Sub(times[1]), "the retry delay grows")
	_, removed, _, _ := seeder.snapshot()
	require.Empty(t, removed, "a failed update removes nothing")

	seeder.setFail(dgB, nil)
	require.Eventually(t, reportIs(m, 2, imageStateUpdated), 3*time.Second, 5*time.Millisecond)
	require.Equal(t, []string{dgA, dgB}, m.digestCharacteristic())
}

// TestImageSetDiskSpace: the worker checks the free space before the pull
// and reports both numbers.
func TestImageSetDiskSpace(t *testing.T) {
	seeder := newFakeSeeder()
	seeder.space = worker.ImageSpace{FreeBytes: 100, FreeKnown: true, RequiredBytes: 500, RequiredKnown: true}
	m := newImageSetManager(Config{}, seeder)
	m.retryMin = time.Hour
	startManager(t, m)

	m.offer(imageSet(1, entry(dgA, "container")))
	require.Eventually(t, reportIs(m, 1, imageStateFailed), 2*time.Second, 5*time.Millisecond)
	r := m.heartbeatReport()
	require.Contains(t, *r.Detail, "not enough disk space")
	require.EqualValues(t, 100, *r.FreeBytes)
	require.EqualValues(t, 500, *r.RequiredBytes)
	pulls, _, _, _ := seeder.snapshot()
	require.Empty(t, pulls, "no pull without the space")
}

// TestImageSetRemovesOnlyOwnedAndUnused: an image that the operator pulled
// is never removed; an image that a running lease uses is removed after
// the lease ends.
func TestImageSetRemovesOnlyOwnedAndUnused(t *testing.T) {
	seeder := newFakeSeeder()
	seeder.notOwned[dgA] = true
	dataDir := t.TempDir()
	m := newImageSetManager(Config{DataDir: dataDir}, seeder)
	startManager(t, m)

	m.offer(imageSet(1, entry(dgA, "container"), entry(dgB, "container")))
	require.Eventually(t, reportIs(m, 1, imageStateUpdated), 2*time.Second, 5*time.Millisecond)
	raw, err := os.ReadFile(filepath.Join(dataDir, imageSetOwnedFile))
	require.NoError(t, err)
	require.Contains(t, string(raw), dgB)
	require.NotContains(t, string(raw), dgA)

	release := m.use("registry.example/img@" + dgB)
	m.offer(imageSet(2, entry(dgC, "container")))
	require.Eventually(t, reportIs(m, 2, imageStateUpdated), 2*time.Second, 5*time.Millisecond)
	_, removed, _, _ := seeder.snapshot()
	require.Empty(t, removed, "B is in use and A is not owned")

	release()
	m.removeLeft(context.Background())
	_, removed, _, _ = seeder.snapshot()
	require.Equal(t, []string{dgB}, removed)

	// The owned list survives a restart.
	again := loadOwnedImages(dataDir)
	got := []string{}
	for _, img := range again.list() {
		got = append(got, img.Digest)
	}
	sort.Strings(got)
	require.Equal(t, []string{dgC}, got)
}

// TestImageSetNoSeederFails: a backend that cannot pull reports failed for
// a set with images. No entry is for this backend (it has no kind), so no
// entry holds the gate, and the gate opens.
func TestImageSetNoSeederFails(t *testing.T) {
	m := newImageSetManager(Config{ContainerRuntime: "custom"}, &fakeRunner{})
	startManager(t, m)
	m.offer(imageSet(4, entry(dgA, "container")))
	require.Eventually(t, reportIs(m, 4, imageStateFailed), 2*time.Second, 5*time.Millisecond)
	require.NoError(t, m.waitReady(context.Background()))
	require.Contains(t, *m.heartbeatReport().Detail, "cannot pull image sets")
}

func TestImageDigestCharacteristicUnion(t *testing.T) {
	cfg := Config{Custom: []characteristics.KV{{Key: "image_digest", Value: []string{dgC}}, {Key: "pool", Value: "mac"}}}
	m := newImageSetManager(cfg, newFakeSeeder())
	m.mu.Lock()
	m.ready[dgA] = true
	m.mu.Unlock()

	wc, err := workerCharacteristics(cfg)
	require.NoError(t, err)
	custom := withImageDigests(wc.Custom, m.digestCharacteristic())
	require.Len(t, custom, 2)
	require.Equal(t, "pool", custom[0].Key)
	require.Equal(t, []string{dgA, dgC}, digestChar(csilapi.WorkerCharacteristics{Custom: custom}), "the operator value adds digests")

	require.Len(t, withImageDigests(wc.Custom, nil), 1, "an empty list is not sent")
}

func TestImageSetVMPrefetchIsExtra(t *testing.T) {
	ref := "registry.example/vm/base:1@" + dgV
	m := newImageSetManager(Config{VMImagePrefetch: []string{ref}}, &vmKindSeeder{newFakeSeeder()})
	images, _ := m.imagesFor(imageSet(1, entry(dgA, "vm"), entry(dgB, "container")))
	got := []string{}
	for _, img := range images {
		got = append(got, img.Digest)
	}
	require.Equal(t, []string{dgA, dgV}, got, "the set entries of the backend kind and the operator list")
	require.Equal(t, []string{dgV}, m.digestCharacteristic(), "a prefetched pinned image is ready")
}

type vmKindSeeder struct{ *fakeSeeder }

func (vmKindSeeder) ImageSetKind() string { return worker.ImageKindVM }

func TestWorkerInfoSendsRuntime(t *testing.T) {
	cfg := testConfig()
	info, err := workerInfo(cfg)
	require.NoError(t, err)
	require.Nil(t, info.Runtime, "an unknown runner sends no runtime")
	cfg.runtime = "docker"
	info, err = workerInfo(cfg)
	require.NoError(t, err)
	require.Equal(t, "docker", *info.Runtime)
}

func pendingEntry(dgst, kind string) csilapi.ImageSetEntry {
	e := entry(dgst, kind)
	pending := true
	e.Pending = &pending
	return e
}

func gateIsOpen(m *imageSetManager) func() bool { return m.gateOpen }

// TestImageSetStrictGateRequiredFailure: when an image that is not pending
// fails, the worker stays not ready (failed, retries), and becomes ready
// only when the pull succeeds (owner decision I4).
func TestImageSetStrictGateRequiredFailure(t *testing.T) {
	seeder := newFakeSeeder()
	seeder.setFail(dgB, errors.New("registry unavailable"))
	m := newImageSetManager(Config{}, seeder)
	m.retryMin, m.retryMax = 10*time.Millisecond, 20*time.Millisecond
	startManager(t, m)

	m.offer(imageSet(1, entry(dgA, "container"), entry(dgB, "container")))
	require.Eventually(t, reportIs(m, 1, imageStateFailed), 2*time.Second, 5*time.Millisecond)
	require.Eventually(t, func() bool { _, _, _, n := seeder.snapshot(); return n >= 3 }, 2*time.Second, 5*time.Millisecond, "the worker tries again")
	require.False(t, m.gateOpen(), "a failed needed image keeps the gate closed")
	require.Contains(t, *m.heartbeatReport().Detail, "registry unavailable")

	seeder.setFail(dgB, nil)
	require.Eventually(t, gateIsOpen(m), 2*time.Second, 5*time.Millisecond)
	require.Eventually(t, reportIs(m, 1, imageStateUpdated), 2*time.Second, 5*time.Millisecond)
}

// TestImageSetMissingPendingIsStrict: an entry without the pending value
// (an older coordinator) holds the gate.
func TestImageSetMissingPendingIsStrict(t *testing.T) {
	seeder := newFakeSeeder()
	seeder.setFail(dgB, errors.New("boom"))
	m := newImageSetManager(Config{}, seeder)
	m.retryMin = time.Hour
	startManager(t, m)
	e := entry(dgB, "container")
	e.Pending = nil
	m.offer(imageSet(1, entry(dgA, "container"), e))
	require.Eventually(t, reportIs(m, 1, imageStateFailed), 2*time.Second, 5*time.Millisecond)
	require.False(t, m.gateOpen())
}

// TestImageSetPendingFailureServesJobs: a pending image that fails does not
// hold the gate. The report is failed (the rollout cannot promote) and the
// worker claims jobs for the ready digests.
func TestImageSetPendingFailureServesJobs(t *testing.T) {
	seeder := newFakeSeeder()
	seeder.setFail(dgA, errors.New("pending pull failed"))
	m := newImageSetManager(Config{}, seeder)
	m.retryMin = time.Hour
	startManager(t, m)

	// The pending entry is first in the set; the worker still pulls the
	// needed image first.
	m.offer(imageSet(1, pendingEntry(dgA, "container"), entry(dgB, "container")))
	require.Eventually(t, reportIs(m, 1, imageStateFailed), 2*time.Second, 5*time.Millisecond)
	require.True(t, m.gateOpen(), "a pending failure does not hold the gate")
	require.Equal(t, []string{dgB}, m.digestCharacteristic())
	require.Contains(t, *m.heartbeatReport().Detail, "pending pull failed")
}

// TestImageSetPendingPullDoesNotDelayGate: the gate opens when the needed
// images are ready, while a pending image is still in progress.
func TestImageSetPendingPullDoesNotDelayGate(t *testing.T) {
	seeder := newFakeSeeder()
	release := make(chan struct{})
	seeder.block[dgB] = release
	m := newImageSetManager(Config{}, seeder)
	startManager(t, m)

	m.offer(imageSet(1, entry(dgA, "container"), pendingEntry(dgB, "container")))
	require.Eventually(t, gateIsOpen(m), 2*time.Second, 5*time.Millisecond)
	require.Equal(t, imageStateUpdating, m.heartbeatReport().State)
	require.Equal(t, []string{dgA}, m.digestCharacteristic())
	close(release)
	require.Eventually(t, reportIs(m, 1, imageStateUpdated), 2*time.Second, 5*time.Millisecond)
	require.Equal(t, []string{dgA, dgB}, m.digestCharacteristic())
}

// TestImageSetOnlyPendingOpensGate: a set with only pending images does not
// hold the gate.
func TestImageSetOnlyPendingOpensGate(t *testing.T) {
	seeder := newFakeSeeder()
	seeder.block[dgA] = make(chan struct{}) // never released
	m := newImageSetManager(Config{}, seeder)
	startManager(t, m)
	m.offer(imageSet(1, pendingEntry(dgA, "container")))
	require.Eventually(t, gateIsOpen(m), 2*time.Second, 5*time.Millisecond)
}

// TestImageSetStrictGateNoRequestJob: the run loop sends no RequestJob while
// a needed image fails.
func TestImageSetStrictGateNoRequestJob(t *testing.T) {
	seeder := newFakeSeeder()
	seeder.setFail(dgA, errors.New("registry unavailable"))
	fc := &fakeClient{}
	fc.RegisterFunc = func(context.Context, string, csilapi.WorkerInfo) (csilapi.RegisterResponse, error) {
		return csilapi.RegisterResponse{WorkerSession: "s", WorkerId: "w", HeartbeatInterval: 1,
			ImageSet: imageSet(2, entry(dgA, "container"))}, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- runLoop(ctx, testConfig(), fc, runnerFactoryFor(seeder)) }()
	failed := func() bool {
		fc.mu.Lock()
		defer fc.mu.Unlock()
		for _, hb := range fc.HeartbeatCalls {
			if hb.Report != nil && hb.Report.Version == 2 && hb.Report.State == imageStateFailed {
				return true
			}
		}
		return false
	}
	require.Eventually(t, failed, 5*time.Second, 20*time.Millisecond, "heartbeat must report failed 2")
	require.Zero(t, fc.snapshotRequestJobCalls(), "no RequestJob while a needed image fails")
	cancel()
	require.ErrorIs(t, <-done, context.Canceled)
}
