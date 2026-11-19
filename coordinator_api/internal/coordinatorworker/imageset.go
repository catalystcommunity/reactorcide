package coordinatorworker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/catalystcommunity/app-utils-go/logging"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/characteristics"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/imagealias"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/worker"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/workerclient/csilapi"
)

// Worker image sets (VM_IMAGES_PLAN.md "Worker image sets", owner
// decisions I4, I5, I8, I12, I13).
//
//   - The register response and a heartbeat response can carry an image
//     set with a version. The worker pulls the entries of its backend kind
//     (vm or container) plus the operator list (--vm-image-prefetch), one
//     set at a time. A set with other content (also with a lower version,
//     after a new enrollment) replaces the newest set. A set with the same
//     content and another version only takes the new version number.
//   - Each image is pulled independently. A failure of one image does not
//     stop the others. Each failure is classified (worker.IsPermanentPullError):
//     permanent (not found, digest mismatch, unauthorized or denied,
//     reference not valid) or transient (network, timeout, 5xx, disk
//     space). The report gives each failure (ImageSetReport.failed).
//   - Startup gate (I4): the run loop does not call RequestJob until each
//     entry of the newest set that is not pending (and the operator list)
//     is ready or failed permanently. A permanent failure cannot be fixed
//     by a retry, and the worker never advertises a digest that it does not
//     have, so no job for that digest comes to the worker. A transient
//     failure of such an entry keeps the gate closed; the worker tries
//     again with backoff. A pending entry (ImageSetEntry.pending: only the
//     pending pin of a rollout) never holds the gate. A missing pending
//     value is false (an older coordinator): the gate then waits for every
//     entry.
//   - State: updated when every image is ready; failed when one or more
//     images failed (the worker tries again with backoff). The rollout of
//     an alias looks only at the ready digests, so a failed image of one
//     alias does not stop the rollout of another alias.
//   - After the gate opens it stays open. A live update continues to
//     claim jobs for the ready digests (I12); the coordinator routes by
//     image_digest.
//   - A new set stops only the pulls of digests that are not in the new
//     set. A pull of a digest that is still in the new set continues, and
//     its result counts for the new set.
//   - Each heartbeat sends an ImageSetReport: updating, updated, or failed,
//     with the ready digests, the failures, and the disk space numbers.
//   - Each RequestJob sends the custom characteristic image_digest: the
//     ready digests, plus an operator value (union).
//   - An image that this worker pulled for a set ("owned", recorded in
//     <data-dir>/image-set-owned.json) and that left the set is removed when
//     no running job uses it. Images that the operator or other tools
//     pulled are never removed.

// Image set states (ImageSetReport.state).
const (
	imageStateUpdating = "updating"
	imageStateUpdated  = "updated"
	imageStateFailed   = "failed"
)

const (
	imageSetRetryMin       = 30 * time.Second
	imageSetRetryMax       = 15 * time.Minute
	imageSetRemoveInterval = time.Hour
	imageSetDetailMax      = 512
	imageSetOwnedFile      = "image-set-owned.json"
)

type imageSetManager struct {
	seeder worker.ImageSetSeeder
	kind   string
	// backend names the runner for the failure text when it cannot pull.
	backend string
	// extras is the operator list (vm prefetch). It is pulled with every
	// set, kept, and never removed.
	extras []worker.ImageSetImage
	// operatorDigests is an operator image_digest characteristic value.
	operatorDigests []string
	owned           *ownedImages

	retryMin, retryMax time.Duration
	removeInterval     time.Duration

	mu sync.Mutex
	// target is the newest set. targetSeq increases each time that a set
	// with other content arrives; doneSeq is the targetSeq of the newest
	// set that is complete (every image ready).
	target     *csilapi.ImageSet
	targetHash string
	targetSeq  int64
	doneSeq    int64
	doneSet    []worker.ImageSetImage
	// running is the targetSeq of the attempt in progress (0: none).
	// imageCancels stop the pulls of the attempt, one for each digest.
	running      int64
	attemptCtx   context.Context
	imageCancels map[string]context.CancelFunc
	imageCtxs    map[string]context.Context
	report       csilapi.ImageSetReport
	ready        map[string]bool
	failed       map[string]csilapi.ImageFailure
	inUse        map[string]int

	wake     chan struct{}
	gate     chan struct{}
	gateOnce sync.Once
}

func newImageSetManager(cfg Config, runner worker.JobRunner) *imageSetManager {
	m := &imageSetManager{
		backend:        cfg.ContainerRuntime,
		retryMin:       imageSetRetryMin,
		retryMax:       imageSetRetryMax,
		removeInterval: imageSetRemoveInterval,
		ready:          map[string]bool{},
		failed:         map[string]csilapi.ImageFailure{},
		inUse:          map[string]int{},
		imageCancels:   map[string]context.CancelFunc{},
		imageCtxs:      map[string]context.Context{},
		wake:           make(chan struct{}, 1),
		gate:           make(chan struct{}),
		report:         csilapi.ImageSetReport{State: imageStateUpdated, ReadyDigests: []string{}},
	}
	if name := worker.RuntimeName(runner); name != "" {
		m.backend = name
	}
	if seeder, ok := runner.(worker.ImageSetSeeder); ok {
		m.seeder = seeder
		m.kind = seeder.ImageSetKind()
	}
	if m.kind == worker.ImageKindVM {
		for _, ref := range cfg.VMImagePrefetch {
			ref = strings.TrimSpace(ref)
			if ref != "" {
				m.extras = append(m.extras, worker.ImageSetImage{Ref: ref, Digest: worker.ImageDigest(ref)})
			}
		}
	}
	m.operatorDigests = operatorImageDigests(cfg.Custom)
	if cfg.VMImagePruneInterval > 0 {
		m.removeInterval = cfg.VMImagePruneInterval
	}
	m.owned = loadOwnedImages(cfg.DataDir)
	// Images of the operator list are ready after the startup prefetch
	// (initializeVMImageCache).
	for _, img := range m.extras {
		if img.Digest != "" {
			m.ready[img.Digest] = true
		}
	}
	m.report.ReadyDigests = m.readyListLocked()
	m.keepLocked()
	return m
}

// operatorImageDigests reads an operator image_digest characteristic
// (REACTORCIDE_WORKER_CUSTOM_IMAGE_DIGEST or --custom image_digest=...).
func operatorImageDigests(custom []characteristics.KV) []string {
	var out []string
	for _, kv := range custom {
		if kv.Key != imagealias.DigestCharacteristic {
			continue
		}
		switch v := kv.Value.(type) {
		case string:
			out = append(out, v)
		case []string:
			out = append(out, v...)
		}
	}
	return out
}

// offer takes an image set from a register or heartbeat response.
//
//   - The same version and content as the newest set: ignored.
//   - The same content with another version (the coordinator version can
//     go back after a new enrollment): only the version changes, so that
//     the next report has the version that the coordinator wants.
//   - Other content, with any version: the new newest set. The pulls of
//     digests that are not in the new set stop; the other pulls continue.
func (m *imageSetManager) offer(set *csilapi.ImageSet) {
	if m == nil || set == nil {
		return
	}
	hash := imageSetContentHash(set)
	m.mu.Lock()
	if m.target != nil && hash == m.targetHash {
		if set.Version != m.target.Version {
			logging.Log.WithFields(map[string]any{"image_set_version": set.Version, "previous_version": m.target.Version}).Info("image set has the same content; using the new version number")
			if m.report.Version == m.target.Version {
				m.report.Version = set.Version
			}
			m.target.Version = set.Version
		}
		m.mu.Unlock()
		return
	}
	copied := *set
	copied.Images = append([]csilapi.ImageSetEntry(nil), set.Images...)
	m.target = &copied
	m.targetHash = hash
	m.targetSeq++
	if m.running != 0 {
		wanted := m.wantedTargetLocked()
		var stopped []string
		for dgst, cancel := range m.imageCancels {
			if !wanted[dgst] {
				cancel()
				stopped = append(stopped, dgst)
			}
		}
		sort.Strings(stopped)
		logging.Log.WithFields(map[string]any{"image_set_version": set.Version, "stopped_pulls": stopped}).Info("new image set; stopping only the pulls of images that left the set")
	}
	m.mu.Unlock()
	select {
	case m.wake <- struct{}{}:
	default:
	}
}

// imageSetContentHash identifies the content of a set (not its version).
func imageSetContentHash(set *csilapi.ImageSet) string {
	lines := make([]string, 0, len(set.Images))
	for _, e := range set.Images {
		aliases := append([]string(nil), e.Aliases...)
		sort.Strings(aliases)
		pending := e.Pending != nil && *e.Pending
		lines = append(lines, fmt.Sprintf("%s\x00%s\x00%s\x00%s\x00%t", e.Digest, e.Ref, e.Kind, strings.Join(aliases, ","), pending))
	}
	sort.Strings(lines)
	h := sha256.Sum256([]byte(strings.Join(lines, "\n")))
	return hex.EncodeToString(h[:])
}

// imageContext returns the context for the pull of one image of the
// attempt in progress. It is done at once when the digest is not in the
// newest set.
func (m *imageSetManager) imageContext(img worker.ImageSetImage) context.Context {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.attemptCtx == nil {
		return nil
	}
	if img.Digest == "" {
		return m.attemptCtx
	}
	if !m.wantedTargetLocked()[img.Digest] {
		done, cancel := context.WithCancel(m.attemptCtx)
		cancel()
		return done
	}
	if _, ok := m.imageCancels[img.Digest]; !ok {
		ctx, cancel := context.WithCancel(m.attemptCtx)
		m.imageCancels[img.Digest] = cancel
		m.imageCtxs[img.Digest] = ctx
	}
	return m.imageCtxs[img.Digest]
}

// waitReady blocks until the startup gate opens: every entry of the set
// that is not pending is ready, or the worker has no set.
func (m *imageSetManager) waitReady(ctx context.Context) error {
	if m == nil {
		return nil
	}
	select {
	case <-m.gate:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (m *imageSetManager) openGate() { m.gateOnce.Do(func() { close(m.gate) }) }

// gateOpen reports whether the startup gate is open.
func (m *imageSetManager) gateOpen() bool {
	select {
	case <-m.gate:
		return true
	default:
		return false
	}
}

// openGateIfSettled opens the startup gate when each image of the newest
// set that is not pending (and the operator list) is ready or failed
// permanently. Without a set, the gate opens.
func (m *imageSetManager) openGateIfSettled() {
	m.mu.Lock()
	settled := true
	if m.target != nil {
		_, required, _ := m.splitImages(m.target)
		for _, img := range required {
			if img.Digest == "" || m.ready[img.Digest] {
				continue
			}
			if f, ok := m.failed[img.Digest]; ok && f.Permanent {
				continue
			}
			settled = false
			break
		}
	}
	m.mu.Unlock()
	if settled {
		m.openGate()
	}
}

// heartbeatReport returns the report for the next heartbeat.
func (m *imageSetManager) heartbeatReport() *csilapi.ImageSetReport {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	r := m.report
	r.ReadyDigests = append([]string{}, m.report.ReadyDigests...)
	r.Failed = m.failedListLocked()
	return &r
}

// digestCharacteristic returns the image_digest list for RequestJob: the
// ready digests and the operator value.
func (m *imageSetManager) digestCharacteristic() []string {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	set := map[string]bool{}
	for d := range m.ready {
		set[d] = true
	}
	for _, d := range m.operatorDigests {
		set[d] = true
	}
	return sortedKeys(set)
}

// use marks the image of a running lease as in use, so that the removal
// of images that left the set skips it. It returns the release function.
func (m *imageSetManager) use(image string) func() {
	dgst := worker.ImageDigest(imagealias.JobReference(image))
	if m == nil || dgst == "" {
		return func() {}
	}
	m.mu.Lock()
	m.inUse[dgst]++
	m.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			m.mu.Lock()
			if m.inUse[dgst]--; m.inUse[dgst] <= 0 {
				delete(m.inUse, dgst)
			}
			m.mu.Unlock()
		})
	}
}

// run pulls image sets until ctx is done. Only this goroutine calls the
// seeder, so only one update runs at a time.
func (m *imageSetManager) run(ctx context.Context) {
	backoff := m.retryMin
	removeTicker := time.NewTicker(m.removeInterval)
	defer removeTicker.Stop()
	if toucher, ok := m.seeder.(worker.ImageSetToucher); ok {
		// The touch runs on its own goroutine: a long pull must not delay
		// it, or another worker can remove a pre-seed object in use.
		go func() {
			ticker := time.NewTicker(worker.ImageSetTouchInterval)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					toucher.TouchImageSet(ctx)
				}
			}
		}()
	}
	for {
		m.mu.Lock()
		target, seq := m.target, m.targetSeq
		pending := target != nil && seq > m.doneSeq
		m.mu.Unlock()

		if !pending {
			m.openGate()
			select {
			case <-ctx.Done():
				return
			case <-m.wake:
			case <-removeTicker.C:
				m.removeLeft(ctx)
			}
			continue
		}

		// A wake signal from before this attempt is stale: this attempt
		// already uses the newest set.
		select {
		case <-m.wake:
		default:
		}
		attemptCtx, cancel := context.WithCancel(ctx)
		m.mu.Lock()
		target, seq = m.target, m.targetSeq // the newest set, read after the drain
		m.running, m.attemptCtx = seq, attemptCtx
		m.mu.Unlock()
		images, err := m.attempt(attemptCtx, target)
		cancel()
		m.mu.Lock()
		m.running, m.attemptCtx = 0, nil
		for dgst, c := range m.imageCancels {
			c()
			delete(m.imageCancels, dgst)
			delete(m.imageCtxs, dgst)
		}
		superseded := m.targetSeq > seq
		m.mu.Unlock()
		if ctx.Err() != nil {
			return
		}
		if superseded {
			// A newer set arrived. It starts at once; the old attempt is
			// not a failure. Its ready images count for the new set.
			backoff = m.retryMin
			continue
		}
		failures := m.failuresFor(images)
		if err == nil && failures == 0 {
			m.complete(seq, images)
			backoff = m.retryMin
			m.openGate()
			m.removeLeft(ctx)
			continue
		}

		// The gate stays closed while an image that is not pending is not
		// ready and did not fail permanently.
		m.fail(seq, images, err, backoff)
		m.openGateIfSettled()
		if m.gateOpen() {
			m.removeLeft(ctx)
		}
		select {
		case <-ctx.Done():
			return
		case <-m.wake:
			// A newer set (or the same one again): try at once.
		case <-time.After(backoff):
			backoff = nextBackoff(backoff, m.retryMax)
		}
	}
}

// failuresFor returns the number of images that are not ready.
func (m *imageSetManager) failuresFor(images []worker.ImageSetImage) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, img := range images {
		if img.Digest != "" && !m.ready[img.Digest] {
			n++
		}
	}
	return n
}

// imagesFor returns the images of set that this backend pulls, plus the
// operator list, without duplicates. Images that are not pending come
// first, so that a pending image never stops the pull of a needed one.
func (m *imageSetManager) imagesFor(set *csilapi.ImageSet) (mine []worker.ImageSetImage, otherKinds int) {
	all, _, otherKinds := m.splitImages(set)
	return all, otherKinds
}

// splitImages returns the images of set that this backend pulls: all of
// them (not pending first, then pending), the ones that are not pending
// (with the operator list), and the count of entries of other kinds.
func (m *imageSetManager) splitImages(set *csilapi.ImageSet) (all, required []worker.ImageSetImage, otherKinds int) {
	var pending []worker.ImageSetImage
	seen := map[string]bool{}
	for _, entry := range set.Images {
		if m.kind == "" || entry.Kind != m.kind {
			otherKinds++
			continue
		}
		key := entry.Digest + "|" + entry.Ref
		if seen[key] {
			continue
		}
		seen[key] = true
		img := worker.ImageSetImage{Ref: entry.Ref, Digest: entry.Digest, Aliases: append([]string(nil), entry.Aliases...)}
		if entry.Pending != nil && *entry.Pending {
			pending = append(pending, img)
			continue
		}
		required = append(required, img)
	}
	for _, img := range m.extras {
		key := img.Digest + "|" + img.Ref
		if !seen[key] {
			seen[key] = true
			required = append(required, img)
		}
	}
	// A digest that is required under one reference is required.
	requiredDigest := map[string]bool{}
	for _, img := range required {
		requiredDigest[img.Digest] = true
	}
	all = append([]worker.ImageSetImage(nil), required...)
	for _, img := range pending {
		if img.Digest == "" || !requiredDigest[img.Digest] {
			all = append(all, img)
		}
	}
	return all, required, otherKinds
}

func (m *imageSetManager) attempt(ctx context.Context, set *csilapi.ImageSet) ([]worker.ImageSetImage, error) {
	images, required, otherKinds := m.splitImages(set)
	m.mu.Lock()
	m.report = csilapi.ImageSetReport{Version: set.Version, State: imageStateUpdating, ReadyDigests: m.readyListLocked(),
		FreeBytes: m.report.FreeBytes, RequiredBytes: m.report.RequiredBytes}
	m.keepLocked()
	m.mu.Unlock()
	logger := logging.Log.WithFields(map[string]any{"image_set_version": set.Version, "images": len(images), "pending_images": len(images) - len(required)})

	// A set with only pending images (or no images for this backend) does
	// not hold the gate: the worker cannot be asked to run the others.
	m.openGateIfSettled()
	if m.seeder == nil {
		if otherKinds > 0 && len(images) == 0 {
			return nil, fmt.Errorf("the %s backend cannot pull image sets", m.backendName())
		}
		return images, nil
	}
	if len(images) == 0 && otherKinds > 0 {
		logger.WithField("other_kind_images", otherKinds).Info("image set has no images for this backend")
	}

	// Before the gate opens, pull the images that are not pending in a
	// pass of their own: a pending image then cannot delay them, and a disk
	// space shortage of the pending images does not keep the worker not
	// ready.
	if !m.gateOpen() && len(required) < len(images) {
		logger.Info("pulling the images that the worker needs before it is ready")
		if err := m.seed(ctx, required); err != nil {
			return images, err
		}
		m.openGateIfSettled()
	}
	logger.Info("pulling image set")
	return images, m.seed(ctx, images)
}

// seed checks the disk space and pulls images. Each image is pulled
// independently. Each ready image or permanent failure can open the
// startup gate. The error is for the whole pass (disk space, or the
// backend could not try).
func (m *imageSetManager) seed(ctx context.Context, images []worker.ImageSetImage) error {
	space, err := m.seeder.ImageSpace(ctx, images)
	if err != nil {
		return fmt.Errorf("disk space check: %w", err)
	}
	m.mu.Lock()
	if space.FreeKnown {
		free := space.FreeBytes
		m.report.FreeBytes = &free
	}
	if space.RequiredKnown {
		need := space.RequiredBytes
		m.report.RequiredBytes = &need
	}
	m.mu.Unlock()
	if space.FreeKnown && space.RequiredKnown && space.RequiredBytes > space.FreeBytes {
		return fmt.Errorf("not enough disk space: the images need about %d bytes, and %d bytes are free", space.RequiredBytes, space.FreeBytes)
	}
	return m.seeder.SeedImages(ctx, images, worker.ImageSeedHooks{
		ImageContext: m.imageContext,
		Ready: func(img worker.ImageSetImage, owned bool) {
			m.mu.Lock()
			if img.Digest != "" {
				m.ready[img.Digest] = true
				delete(m.failed, img.Digest)
				m.report.ReadyDigests = m.readyListLocked()
			}
			m.mu.Unlock()
			if owned && !m.isExtra(img) {
				m.owned.add(img)
			}
			m.openGateIfSettled()
		},
		Failed: func(img worker.ImageSetImage, err error) {
			if img.Digest == "" || err == nil {
				return
			}
			failure := csilapi.ImageFailure{Digest: img.Digest, Permanent: worker.IsPermanentPullError(err), Detail: cutDetail(err.Error())}
			m.mu.Lock()
			m.failed[img.Digest] = failure
			m.mu.Unlock()
			logging.Log.WithError(err).WithFields(map[string]any{"image": img.Label(), "permanent": failure.Permanent}).Warn("image set image could not be pulled")
			m.openGateIfSettled()
		},
	})
}

func cutDetail(detail string) string {
	if len(detail) > imageSetDetailMax {
		detail = detail[:imageSetDetailMax]
		for !utf8.ValidString(detail) {
			detail = detail[:len(detail)-1]
		}
	}
	return detail
}

func (m *imageSetManager) backendName() string {
	if m.backend == "" {
		return "configured"
	}
	return m.backend
}

func (m *imageSetManager) isExtra(img worker.ImageSetImage) bool {
	for _, extra := range m.extras {
		if extra.Ref == img.Ref || (img.Digest != "" && extra.Digest == img.Digest) {
			return true
		}
	}
	return false
}

func (m *imageSetManager) complete(seq int64, images []worker.ImageSetImage) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if seq != m.targetSeq {
		return
	}
	m.doneSeq = seq
	m.doneSet = images
	m.pruneLocked()
	m.report = csilapi.ImageSetReport{
		Version:       m.target.Version,
		State:         imageStateUpdated,
		ReadyDigests:  m.readyListLocked(),
		FreeBytes:     m.report.FreeBytes,
		RequiredBytes: m.report.RequiredBytes,
	}
	m.keepLocked()
	logging.Log.WithFields(map[string]any{"image_set_version": m.target.Version, "ready_digests": len(m.ready)}).Info("image set updated")
}

// pruneLocked keeps only the ready digests and the failures of the newest
// set (and the operator list).
func (m *imageSetManager) pruneLocked() {
	wanted := m.wantedLocked()
	for d := range m.ready {
		if !wanted[d] {
			delete(m.ready, d)
		}
	}
	for d := range m.failed {
		if !wanted[d] {
			delete(m.failed, d)
		}
	}
}

// fail reports the attempt as failed: err is a failure of the whole pass,
// and the failures of single images are in m.failed.
func (m *imageSetManager) fail(seq int64, images []worker.ImageSetImage, err error, retryIn time.Duration) {
	m.mu.Lock()
	if seq != m.targetSeq {
		m.mu.Unlock()
		return
	}
	// The images that are not in the newest set are not ready any more.
	m.doneSet = nil
	m.pruneLocked()
	notReady := 0
	permanent := 0
	for _, img := range images {
		if img.Digest == "" || m.ready[img.Digest] {
			continue
		}
		notReady++
		if f, ok := m.failed[img.Digest]; ok && f.Permanent {
			permanent++
		}
	}
	var detail string
	switch {
	case err != nil:
		detail = err.Error()
	case notReady > 0:
		detail = fmt.Sprintf("%d of %d images are not ready (%d with a permanent failure)", notReady, len(images), permanent)
		if first := m.firstFailureLocked(); first != "" {
			detail += ": " + first
		}
	}
	detail = cutDetail(detail)
	m.report.Version = m.target.Version
	m.report.State = imageStateFailed
	m.report.Detail = &detail
	m.report.ReadyDigests = m.readyListLocked()
	m.keepLocked()
	m.mu.Unlock()
	fields := map[string]any{"image_set_version": m.target.Version, "retry_in": retryIn.String(), "detail": detail}
	if !m.gateOpen() {
		logging.Log.WithFields(fields).Warn("image set pull failed; the worker is not ready and claims no job until the images that it needs are ready or failed permanently")
		return
	}
	logging.Log.WithFields(fields).Warn("image set pull failed; the worker serves the images that are ready")
}

// firstFailureLocked returns the detail of the first failure in digest
// order, or "".
func (m *imageSetManager) firstFailureLocked() string {
	list := m.failedListLocked()
	if len(list) == 0 {
		return ""
	}
	return list[0].Detail
}

// failedListLocked returns the failures of the newest set, by digest.
func (m *imageSetManager) failedListLocked() []csilapi.ImageFailure {
	if len(m.failed) == 0 {
		return nil
	}
	wanted := m.wantedTargetLocked()
	out := make([]csilapi.ImageFailure, 0, len(m.failed))
	for d, f := range m.failed {
		if wanted[d] {
			out = append(out, f)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Digest < out[j].Digest })
	if len(out) == 0 {
		return nil
	}
	return out
}

// removeLeft removes owned images that are not in the newest set, the
// completed set, or the operator list, and that no running lease uses.
func (m *imageSetManager) removeLeft(ctx context.Context) {
	if m.seeder == nil {
		return
	}
	m.mu.Lock()
	wanted := m.wantedLocked()
	var candidates []worker.ImageSetImage
	for _, img := range m.owned.list() {
		if wanted[img.Digest] || m.inUse[img.Digest] > 0 {
			continue
		}
		candidates = append(candidates, img)
		// Stop the advertisement before the removal.
		delete(m.ready, img.Digest)
	}
	m.report.ReadyDigests = m.readyListLocked()
	m.mu.Unlock()
	if len(candidates) == 0 {
		return
	}
	kept, err := m.seeder.RemoveImages(ctx, candidates)
	if err != nil {
		logging.Log.WithError(err).Warn("could not remove images that left the image set; trying again later")
		return
	}
	keptSet := map[string]bool{}
	for _, img := range kept {
		keptSet[img.Digest] = true
	}
	for _, img := range candidates {
		if !keptSet[img.Digest] {
			m.owned.remove(img.Digest)
		}
	}
	m.mu.Lock()
	m.keepLocked()
	m.mu.Unlock()
}

// wantedTargetLocked returns the digests of the newest set (this backend
// kind) and the operator list.
func (m *imageSetManager) wantedTargetLocked() map[string]bool {
	wanted := map[string]bool{}
	if m.target != nil {
		images, _ := m.imagesFor(m.target)
		for _, img := range images {
			wanted[img.Digest] = true
		}
	}
	for _, img := range m.extras {
		wanted[img.Digest] = true
	}
	delete(wanted, "")
	return wanted
}

// wantedLocked returns the digests of the newest set (this backend kind),
// the completed set, and the operator list.
func (m *imageSetManager) wantedLocked() map[string]bool {
	wanted := map[string]bool{}
	if m.target != nil {
		images, _ := m.imagesFor(m.target)
		for _, img := range images {
			wanted[img.Digest] = true
		}
	}
	for _, img := range m.doneSet {
		wanted[img.Digest] = true
	}
	for _, img := range m.extras {
		wanted[img.Digest] = true
	}
	delete(wanted, "")
	return wanted
}

// keepLocked tells the backend retention what to keep: the wanted digests,
// the ready digests, and the owned images that are not removed yet.
func (m *imageSetManager) keepLocked() {
	if m.seeder == nil {
		return
	}
	keep := m.wantedLocked()
	for d := range m.ready {
		keep[d] = true
	}
	for _, img := range m.owned.list() {
		keep[img.Digest] = true
	}
	m.seeder.KeepImages(sortedKeys(keep))
}

func (m *imageSetManager) readyListLocked() []string {
	return sortedKeys(m.ready)
}

func sortedKeys(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		if k != "" {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// withImageDigests sets the image_digest custom characteristic to digests
// (it replaces an operator entry, whose values digests already has). An
// empty list removes the key: an empty list characteristic is not valid.
func withImageDigests(custom []csilapi.CustomCharacteristic, digests []string) []csilapi.CustomCharacteristic {
	out := make([]csilapi.CustomCharacteristic, 0, len(custom)+1)
	for _, c := range custom {
		if c.Key != imagealias.DigestCharacteristic {
			out = append(out, c)
		}
	}
	if len(digests) > 0 {
		out = append(out, csilapi.CustomCharacteristic{Key: imagealias.DigestCharacteristic, Value: append([]string{}, digests...)})
	}
	return out
}

// ownedImages is the durable list of images that this worker pulled for
// an image set. Without a data directory the list is in memory only.
type ownedImages struct {
	mu     sync.Mutex
	path   string
	images map[string]worker.ImageSetImage
}

type ownedImagesFile struct {
	Images []ownedImageRecord `json:"images"`
}

type ownedImageRecord struct {
	Ref    string `json:"ref"`
	Digest string `json:"digest"`
}

func loadOwnedImages(dataDir string) *ownedImages {
	o := &ownedImages{images: map[string]worker.ImageSetImage{}}
	if dataDir == "" {
		return o
	}
	o.path = filepath.Join(dataDir, imageSetOwnedFile)
	raw, err := os.ReadFile(o.path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			logging.Log.WithError(err).Warn("could not read the list of image set images; starting with an empty list")
		}
		return o
	}
	var file ownedImagesFile
	if err := json.Unmarshal(raw, &file); err != nil {
		logging.Log.WithError(err).Warn("the list of image set images is not valid; starting with an empty list")
		return o
	}
	for _, rec := range file.Images {
		if rec.Digest != "" {
			o.images[rec.Digest] = worker.ImageSetImage{Ref: rec.Ref, Digest: rec.Digest}
		}
	}
	return o
}

func (o *ownedImages) add(img worker.ImageSetImage) {
	if img.Digest == "" {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if _, ok := o.images[img.Digest]; ok {
		return
	}
	o.images[img.Digest] = worker.ImageSetImage{Ref: img.Ref, Digest: img.Digest}
	o.saveLocked()
}

func (o *ownedImages) remove(dgst string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if _, ok := o.images[dgst]; !ok {
		return
	}
	delete(o.images, dgst)
	o.saveLocked()
}

func (o *ownedImages) list() []worker.ImageSetImage {
	o.mu.Lock()
	defer o.mu.Unlock()
	out := make([]worker.ImageSetImage, 0, len(o.images))
	for _, img := range o.images {
		out = append(out, img)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Digest < out[j].Digest })
	return out
}

func (o *ownedImages) saveLocked() {
	if o.path == "" {
		return
	}
	file := ownedImagesFile{Images: []ownedImageRecord{}}
	for _, img := range o.images {
		file.Images = append(file.Images, ownedImageRecord{Ref: img.Ref, Digest: img.Digest})
	}
	sort.Slice(file.Images, func(i, j int) bool { return file.Images[i].Digest < file.Images[j].Digest })
	raw, err := json.MarshalIndent(file, "", "  ")
	if err == nil {
		err = os.MkdirAll(filepath.Dir(o.path), 0o700)
	}
	if err == nil {
		tmp := o.path + ".tmp"
		if err = os.WriteFile(tmp, raw, 0o600); err == nil {
			err = os.Rename(tmp, o.path)
		}
	}
	if err != nil {
		logging.Log.WithError(err).Warn("could not save the list of image set images")
	}
}
