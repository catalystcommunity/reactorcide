package coordinatorworker

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/catalystcommunity/app-utils-go/logging"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/admission"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/worker"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/workerclient"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/workerclient/csilapi"
	"github.com/sirupsen/logrus"
)

// runnerFactory matches internal/worker.NewJobRunner's signature; tests
// substitute a fake JobRunner without touching Docker/containerd/k8s.
type runnerFactory func(backend string) (worker.JobRunner, error)

// Run is the coordinator-mediated worker run loop's entry point. It blocks
// until ctx is cancelled (a graceful shutdown: stop polling for new work, let
// already-claimed leases finish and report their result, then return) or a
// fatal setup error occurs (e.g. the configured ContainerRuntime backend
// can't be constructed, or the very first Register never succeeds because ctx
// was already cancelled). A resumable connection failure
// (RequestJob/Heartbeat erroring against a coordinator that's down or a
// session that expired) does not return an error -- it backs off and
// re-registers.
func Run(ctx context.Context, cfg Config) error {
	transport := &workerclient.CSILRPCTransport{
		BaseURL:                cfg.CoordinatorURL,
		AllowInsecureTransport: cfg.AllowInsecureTransport,
	}
	c := workerclient.NewWithTransport(transport)
	return runLoop(ctx, cfg, c, worker.NewJobRunner)
}

func runLoop(ctx context.Context, cfg Config, c client, newRunner runnerFactory) error {
	if cfg.CoordinatorURL == "" {
		return errors.New("coordinatorworker: CoordinatorURL is required")
	}
	if cfg.EnrollmentToken == "" {
		return errors.New("coordinatorworker: EnrollmentToken is required")
	}
	if cfg.WorkerKey == "" {
		return errors.New("coordinatorworker: WorkerKey is required")
	}
	if cfg.OS == "" || cfg.Arch == "" {
		return errors.New("coordinatorworker: OS and Arch are required")
	}

	runner, err := newRunner(cfg.ContainerRuntime)
	if err != nil {
		return fmt.Errorf("coordinatorworker: create job runner: %w", err)
	}
	cfg.runtime = worker.RuntimeName(runner)
	// Make the image set manager before the first VM prune: it tells the
	// retention to keep the images of the last set (the owned list).
	images := newImageSetManager(cfg, runner)
	if cache, ok, err := initializeVMImageCache(ctx, runner, cfg, time.Now()); err != nil {
		return err
	} else if ok {
		if cfg.VMImagePruneInterval > 0 && cfg.VMImageMaxUnused > 0 {
			go maintainVMImageCache(ctx, cache, cfg.VMImageMaxUnused, cfg.VMImagePruneInterval)
		}
	}

	concurrency := cfg.Concurrency
	if concurrency < 1 {
		concurrency = 1
	}
	mode, err := admission.ParseMode(cfg.AdmissionMode)
	if err != nil {
		return fmt.Errorf("coordinatorworker: %w", err)
	}
	resourcesMode := mode == admission.ModeResources
	reservations := newLeaseReservations()
	if resourcesMode {
		// Fail at startup when the capacity cannot be measured, for example
		// an unknown host memory size with no budget, or missing RBAC.
		report, err := measureAdmission(ctx, cfg, runner, reservations)
		if err != nil {
			return fmt.Errorf("coordinatorworker: resources admission: %w", err)
		}
		if !report.Capacity.IsPositive() {
			return errors.New("coordinatorworker: resources admission: capacity is zero; set REACTORCIDE_WORKER_ADMISSION_CPU and REACTORCIDE_WORKER_ADMISSION_MEMORY")
		}
		logging.Log.WithFields(map[string]any{
			"capacity_cpu_millicores": report.Capacity.CPUMillicores,
			"capacity_memory_bytes":   report.Capacity.MemoryBytes,
			"max_leases":              concurrency,
		}).Info("coordinatorworker: resources admission mode")
	}
	cfg = applyDetectedTopology(ctx, cfg, runner)
	if cfg, err = applyEgressCharacteristic(cfg); err != nil {
		return err
	}

	reg, err := registerWithBackoff(ctx, cfg, c)
	if err != nil {
		return err
	}
	images.offer(reg.ImageSet)
	heartbeatInterval := time.Duration(reg.HeartbeatInterval) * time.Second
	replayTelemetrySpool(c, cfg.DataDir)

	tracker := newLeaseTracker()
	tracker.images = images
	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup

	hbCtx, hbCancel := context.WithCancel(context.Background())
	defer hbCancel()
	go images.run(hbCtx)
	go heartbeatLoop(hbCtx, c, runner, tracker, heartbeatInterval)
	go telemetryReplayLoop(hbCtx, c, cfg.DataDir, cfg.TelemetrySendInterval)

	// Startup pre-seed gate (owner decision I4): no RequestJob until the
	// first pull of the image set ends. The heartbeat reports "updating"
	// meanwhile.
	if err := images.waitReady(ctx); err != nil {
		return err
	}

	errBackoff := reconnectBackoffMin

pollLoop:
	for {
		select {
		case <-ctx.Done():
			break pollLoop
		case sem <- struct{}{}:
		}
		wc, wcErr := workerCharacteristics(cfg)
		if wcErr != nil {
			<-sem
			return fmt.Errorf("coordinatorworker: %w", wcErr)
		}
		wc.Custom = withImageDigests(wc.Custom, images.digestCharacteristic())

		var report *csilapi.AdmissionReport
		if resourcesMode {
			measured, measureErr := measureAdmission(ctx, cfg, runner, reservations)
			if measureErr != nil || !measured.Headroom.IsPositive() {
				<-sem
				if measureErr != nil {
					logging.Log.WithError(measureErr).Warn("coordinatorworker: could not measure admission headroom; waiting")
				}
				if !sleepOrDone(ctx, noLeaseBackoff) {
					break pollLoop
				}
				continue
			}
			report = admissionReportWire(measured)
		}

		resp, err := requestJob(ctx, c, wc, report)
		if err != nil {
			<-sem
			logging.Log.WithError(err).Warn("coordinatorworker: RequestJob failed; backing off and re-registering")
			if !sleepOrDone(ctx, errBackoff) {
				break pollLoop
			}
			errBackoff = nextBackoff(errBackoff, reconnectBackoffMax)
			// The session may have expired (or the coordinator restarted and
			// lost in-memory state); re-Register to mint a fresh one.
			// registerWithBackoff itself only returns on success or ctx
			// cancellation, so a persistently-down coordinator keeps retrying
			// here rather than propagating an error out of Run.
			rereg, err := registerWithBackoff(ctx, cfg, c)
			if err != nil {
				return err
			}
			images.offer(rereg.ImageSet)
			continue
		}
		errBackoff = reconnectBackoffMin

		if !resp.HasLease || resp.Lease == nil {
			<-sem
			if !sleepOrDone(ctx, noLeaseBackoff) {
				break pollLoop
			}
			continue
		}

		lease := *resp.Lease
		if resourcesMode {
			reservations.add(lease.LeaseId, lease.JobId, leaseNeed(lease))
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			defer reservations.remove(lease.LeaseId)
			runLease(c, runner, lease, tracker, cfg)
		}()
	}

	wg.Wait()
	return ctx.Err()
}

func initializeVMImageCache(ctx context.Context, runner worker.JobRunner, cfg Config, now time.Time) (worker.VMImageCacheManager, bool, error) {
	cache, ok := runner.(worker.VMImageCacheManager)
	if !ok {
		return nil, false, nil
	}
	removed, err := cache.PruneImages(ctx, cfg.VMImageMaxUnused, now)
	if err != nil {
		return nil, true, fmt.Errorf("coordinatorworker: prune VM image cache: %w", err)
	}
	if removed > 0 {
		logging.Log.WithField("images_removed", removed).Info("pruned unused VM images")
	}
	if err := cache.PrefetchImages(ctx, cfg.VMImagePrefetch); err != nil {
		return nil, true, fmt.Errorf("coordinatorworker: prefetch VM images: %w", err)
	}
	return cache, true, nil
}

func maintainVMImageCache(ctx context.Context, cache worker.VMImageCacheManager, maxUnused, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			removed, err := cache.PruneImages(ctx, maxUnused, now)
			if err != nil {
				logging.Log.WithError(err).Warn("failed to prune VM image cache")
				continue
			}
			if removed > 0 {
				logging.Log.WithField("images_removed", removed).Info("pruned unused VM images")
			}
		}
	}
}

// registerWithBackoff calls Register with an escalating backoff until it
// succeeds or ctx is done. It never gives up on its own -- Register failing
// against a reachable-but-rejecting coordinator (e.g. a revoked enrollment
// token) is indistinguishable here from a transient network error, so this
// package logs and retries rather than guessing; an operator watching logs
// (or revoking/rotating the token back) resolves either case the same way.
func registerWithBackoff(ctx context.Context, cfg Config, c client) (rr registerResult, err error) {
	info, err := workerInfo(cfg)
	if err != nil {
		return rr, fmt.Errorf("coordinatorworker: %w", err)
	}

	backoff := reconnectBackoffMin
	for {
		if ctx.Err() != nil {
			return rr, ctx.Err()
		}
		resp, err := c.Register(ctx, cfg.EnrollmentToken, info)
		if err == nil {
			logRegistered(resp)
			return registerResult{HeartbeatInterval: resp.HeartbeatInterval, WorkerID: resp.WorkerId, ImageSet: resp.ImageSet}, nil
		}
		logging.Log.WithError(err).Warn("coordinatorworker: register failed; retrying")
		if !sleepOrDone(ctx, backoff) {
			return rr, ctx.Err()
		}
		backoff = nextBackoff(backoff, reconnectBackoffMax)
	}
}

// logRegistered logs the worker ID and the enrollment token (ID and name,
// never the value) of a successful Register, for audits of a token
// rotation. A coordinator before 0.13 sends no token fields.
func logRegistered(resp csilapi.RegisterResponse) {
	fields := logrus.Fields{"worker_id": resp.WorkerId, "enrollment_token_id": "unknown", "enrollment_token_name": ""}
	if resp.EnrollmentTokenId != nil {
		fields["enrollment_token_id"] = *resp.EnrollmentTokenId
	}
	if resp.EnrollmentTokenName != nil {
		fields["enrollment_token_name"] = *resp.EnrollmentTokenName
	}
	logging.Log.WithFields(fields).Info("coordinatorworker: registered")
}

// registerResult is the small slice of RegisterResponse the run loop needs,
// kept separate from csilapi.RegisterResponse only so registerWithBackoff's
// signature doesn't force every caller to import csilapi.
type registerResult struct {
	HeartbeatInterval int64
	WorkerID          string
	// ImageSet is the image set of the register response, or nil.
	ImageSet *csilapi.ImageSet
}

// sleepOrDone waits for d or ctx cancellation, whichever comes first. Returns
// false if ctx was the reason it returned (caller should stop).
func sleepOrDone(ctx context.Context, d time.Duration) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}
