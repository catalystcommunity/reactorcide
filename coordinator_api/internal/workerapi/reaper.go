package workerapi

import (
	"context"
	"time"

	"github.com/catalystcommunity/app-utils-go/logging"
)

// leaseReapInterval is how often the lease reaper scans for stale open
// worker_leases rows: once immediately on Start, then on this ticker --
// mirroring internal/worker/corndogs_worker.go's runCancellingReaper cadence.
const leaseReapInterval = 60 * time.Second

// leaseStaleAfter bounds how old an open lease's acquired_at must be before
// the reaper treats it as orphaned. Generous relative to HeartbeatInterval: a
// lease this old with no release almost certainly belongs to a worker that
// stopped heartbeating, whose corndogs task has ALREADY timed out and
// requeued on its own. The reaper's only job is bookkeeping: mark the
// display/audit row released so it stops showing as an open lease for a
// worker that is, in fact, gone.
const leaseStaleAfter = 15 * time.Minute

// RunLeaseReaper drives reapStaleLeases on leaseReapInterval until ctx is
// cancelled, running once immediately on entry. Wire this alongside the
// coordinator's other background loops (see handlers/router.go, next to
// wherever internal/worker's own reapers would start for an in-process
// worker) -- it is bookkeeping only, never touches corndogs: the actual
// requeue-on-death is corndogs' own task timeout, which the coordinator
// triggers simply by no longer heartbeating a dead worker's task.
func (s *WorkerService) RunLeaseReaper(ctx context.Context) {
	s.reapStaleLeases(ctx)

	ticker := time.NewTicker(leaseReapInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.reapStaleLeases(ctx)
		}
	}
}

// reapStaleLeases marks every worker_leases row open (released_at IS NULL)
// since before leaseStaleAfter as released with outcome "reaped:stale", and
// drops its cached secret values (see leaseSecretCache). It never touches
// corndogs or the job row -- by the time a lease is this old without a
// worker heartbeat, corndogs has already requeued (or timed out) the task
// on its own, and whatever picks the task up next (a live worker, or this
// same one after reconnecting) drives the job's own status from there.
func (s *WorkerService) reapStaleLeases(ctx context.Context) {
	threshold := time.Now().Add(-leaseStaleAfter)
	stale, err := s.deps.Store.ListStaleActiveLeases(ctx, threshold)
	if err != nil {
		logging.Log.WithError(err).Warn("Failed to list stale active worker leases for reaper")
		return
	}

	for i := range stale {
		lease := &stale[i]
		if err := s.deps.Store.ReleaseWorkerLease(ctx, lease.LeaseID, "reaped:stale"); err != nil {
			logging.Log.WithError(err).WithField("lease_id", lease.LeaseID).Warn("Failed to release stale worker lease")
			continue
		}
		s.secrets.delete(lease.LeaseID)
		logging.Log.WithFields(map[string]interface{}{
			"lease_id":  lease.LeaseID,
			"worker_id": lease.WorkerID,
			"job_id":    lease.JobID,
		}).Warn("Reaped stale worker lease with no active heartbeat")
	}
}
