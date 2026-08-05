package coordinatorworker

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/catalystcommunity/app-utils-go/logging"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/worker"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/workerclient"
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
	transport := &workerclient.CSILRPCTransport{BaseURL: cfg.CoordinatorURL}
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

	reg, err := registerWithBackoff(ctx, cfg, c)
	if err != nil {
		return err
	}
	heartbeatInterval := time.Duration(reg.HeartbeatInterval) * time.Second
	replayTelemetrySpool(c, cfg.DataDir)

	tracker := newLeaseTracker()
	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup

	hbCtx, hbCancel := context.WithCancel(context.Background())
	defer hbCancel()
	go heartbeatLoop(hbCtx, c, runner, tracker, heartbeatInterval)
	go telemetryReplayLoop(hbCtx, c, cfg.DataDir, cfg.TelemetrySendInterval)

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

		resp, err := c.RequestJob(ctx, wc)
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
			if _, err := registerWithBackoff(ctx, cfg, c); err != nil {
				return err
			}
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
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
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
			return registerResult{HeartbeatInterval: resp.HeartbeatInterval, WorkerID: resp.WorkerId}, nil
		}
		logging.Log.WithError(err).Warn("coordinatorworker: register failed; retrying")
		if !sleepOrDone(ctx, backoff) {
			return rr, ctx.Err()
		}
		backoff = nextBackoff(backoff, reconnectBackoffMax)
	}
}

// registerResult is the small slice of RegisterResponse the run loop needs,
// kept separate from csilapi.RegisterResponse only so registerWithBackoff's
// signature doesn't force every caller to import csilapi.
type registerResult struct {
	HeartbeatInterval int64
	WorkerID          string
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
