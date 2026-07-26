package coordinatorworker

import (
	"context"
	"time"

	"github.com/catalystcommunity/app-utils-go/logging"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/worker"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/workerclient/csilapi"
)

// heartbeatLoop sends a Heartbeat on every tick of interval until ctx is
// cancelled, reporting every lease currently tracked as running and acting
// on any directive the coordinator returns. It always heartbeats (even with
// zero running leases) because Heartbeat also renews the worker session
// server-side (WORKERS_PLAN.md "Workers -- registration, auth, protocol").
func heartbeatLoop(ctx context.Context, c client, runner worker.JobRunner, tracker *leaseTracker, interval time.Duration) {
	if interval <= 0 {
		interval = defaultHeartbeatInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			heartbeatOnce(ctx, c, runner, tracker)
		}
	}
}

func heartbeatOnce(ctx context.Context, c client, runner worker.JobRunner, tracker *leaseTracker) {
	ids := tracker.runningLeaseIDs()
	status := "idle"
	if len(ids) > 0 {
		status = "running"
	}
	running := make([]csilapi.RunningLease, len(ids))
	for i, id := range ids {
		running[i] = csilapi.RunningLease{LeaseId: id}
	}

	resp, err := c.Heartbeat(ctx, status, running)
	if err != nil {
		logging.Log.WithError(err).Warn("coordinatorworker: heartbeat failed")
		return
	}

	for _, d := range resp.Directives {
		applyDirective(runner, tracker, d)
	}
}

// applyDirective resolves a directive's lease_id to the runner handle this
// worker is actually executing and acts on it: "cancel" calls Stop with the
// lease's cancel-grace period (graceful SIGTERM, forced kill after grace,
// per JobRunner.Stop's own contract); "kill" calls Cleanup immediately
// (WORKERS_PLAN.md Directive: "action is cancel ... or kill ... immediate").
// A lease_id the tracker no longer holds (the lease already finished and
// was removed) is silently ignored -- a race between the job's own
// completion and a stale directive is expected, not an error. Each action
// only ever applies once per lease (trackedLease.setOutcome), so a
// heartbeat tick that repeats an already-actioned directive (the
// coordinator may keep returning it until it observes the lease released)
// does not double-Stop/double-Cleanup.
func applyDirective(runner worker.JobRunner, tracker *leaseTracker, d csilapi.Directive) {
	tl, ok := tracker.get(d.LeaseId)
	if !ok {
		return
	}
	if prior := tl.setOutcome(d.Action); prior != "" {
		return
	}

	tl.mu.Lock()
	runnerID := tl.runnerID
	grace := tl.graceSeconds
	tl.mu.Unlock()
	if runnerID == "" {
		// SpawnJob hasn't returned yet; nothing to Stop/Cleanup. The next
		// heartbeat tick will retry via the outcome-already-set short
		// circuit... except setOutcome above already claimed it. Reset so a
		// later tick (once runnerID is populated) can still act.
		tl.mu.Lock()
		tl.outcome = ""
		tl.mu.Unlock()
		return
	}

	logger := logging.Log.WithFields(map[string]interface{}{"lease_id": d.LeaseId, "job_id": tl.jobID, "action": d.Action})
	switch d.Action {
	case "cancel":
		go func() {
			if err := runner.Stop(context.Background(), runnerID, time.Duration(grace)*time.Second); err != nil {
				logger.WithError(err).Warn("failed to stop job for cancel directive")
			}
		}()
	case "kill":
		go func() {
			if err := runner.Cleanup(context.Background(), runnerID); err != nil {
				logger.WithError(err).Warn("failed to clean up job for kill directive")
			}
		}()
	default:
		logger.Warn("unknown directive action")
	}
}
