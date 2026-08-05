package uiapi

import (
	"context"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/authz"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/uiapi/csilapi"
)

// requireManageWorkers requires org admin (of orgID) or global admin -- every
// worker/pool/queue admin op is gated the same way, via
// authz.Caps.ManageWorkers; there is no separate "view" capability for this
// surface. orgID is nil when the target resource has no owning org (a global
// pool, or every queue today -- queues have no creation path that sets one
// yet): only a true global admin may manage it, since there is no org scope
// left to check Capabilities against.
func (s *UiService) requireManageWorkers(ctx context.Context, id authz.Identity, orgID *string) error {
	if orgID == nil {
		if err := s.deps.Resolver.RequireGlobalAdmin(ctx, id); err != nil {
			return mapPermissionErr(err)
		}
		return nil
	}
	caps, err := s.deps.Resolver.Capabilities(ctx, id, authz.Scope{OrgID: orgID})
	if err != nil {
		return NewServiceError("internal", "an internal error occurred")
	}
	if !caps.ManageWorkers {
		return NewServiceError("forbidden", "you do not have permission to manage workers for this org")
	}
	return nil
}

// workerStatusValid reports whether status is one of the three persisted
// worker statuses (models.Worker's own status CHECK constraint).
func workerStatusValid(status string) bool {
	switch status {
	case models.WorkerStatusActive, models.WorkerStatusQuarantined, models.WorkerStatusDisabled:
		return true
	default:
		return false
	}
}

// workerToSummary renders w as the wire WorkerSummary, including its active
// lease count (a live ListActiveLeasesForWorker call per worker -- the
// worker/lease tables are operator/coordinator-scale, not per-job, so this
// is an admin-listing convenience, not a hot path).
func (s *UiService) workerToSummary(ctx context.Context, w *models.Worker) csilapi.WorkerSummary {
	out := csilapi.WorkerSummary{
		WorkerId:        w.WorkerID,
		PoolId:          w.PoolID,
		WorkerKey:       w.WorkerKey,
		Os:              w.OS,
		Arch:            w.Arch,
		Characteristics: workerCharacteristicsToCsil(w.Characteristics),
		Status:          w.Status,
		LastSeenAt:      formatTimePtr(w.LastSeenAt),
	}
	if w.Hostname != "" {
		h := w.Hostname
		out.Hostname = &h
	}
	if w.WorkerVersion != "" {
		v := w.WorkerVersion
		out.WorkerVersion = &v
	}
	if leases, err := s.deps.Store.ListActiveLeasesForWorker(ctx, w.WorkerID); err == nil {
		out.ActiveLeaseCount = int64(len(leases))
	}
	return out
}

// resolveWorkerOrgScope resolves w's owning org via its pool, for
// requireManageWorkers -- mirrors how CancelJob resolves a job's org via
// job.UserID. nil when the pool itself has no org (a global pool).
func (s *UiService) resolveWorkerOrgScope(ctx context.Context, w *models.Worker) (*string, error) {
	pool, err := s.deps.Store.GetWorkerPoolByID(ctx, w.PoolID)
	if err != nil {
		return nil, err
	}
	return pool.OrgID, nil
}

// ListWorkers requires org admin (of the target pool's org) or global admin
// when pool_id is given; global admin only when pool_id is omitted, since
// listing across every pool/org has no single org scope to check.
func (s *UiService) ListWorkers(ctx context.Context, req csilapi.ListWorkersRequest) (csilapi.ListWorkersResponse, error) {
	id, _, authErr := s.deps.requireUser(ctx)
	if authErr != nil {
		return csilapi.ListWorkersResponse{}, authErr
	}

	var poolID *string
	if req.PoolId != nil {
		if err := requireNonEmpty("pool_id", *req.PoolId, 64); err != nil {
			return csilapi.ListWorkersResponse{}, err
		}
		pool, err := s.deps.Store.GetWorkerPoolByID(ctx, *req.PoolId)
		if err != nil {
			return csilapi.ListWorkersResponse{}, mapStoreErr(err, "pool not found")
		}
		if err := s.requireManageWorkers(ctx, id, pool.OrgID); err != nil {
			return csilapi.ListWorkersResponse{}, err
		}
		poolID = &pool.PoolID
	} else if err := s.requireManageWorkers(ctx, id, nil); err != nil {
		return csilapi.ListWorkersResponse{}, err
	}

	workers, err := s.deps.Store.ListWorkers(ctx, poolID)
	if err != nil {
		return csilapi.ListWorkersResponse{}, NewServiceError("internal", "an internal error occurred")
	}
	out := make([]csilapi.WorkerSummary, len(workers))
	for i := range workers {
		out[i] = s.workerToSummary(ctx, &workers[i])
	}
	return csilapi.ListWorkersResponse{Workers: out}, nil
}

// SetWorkerStatus sets a worker's status to active/quarantined/disabled --
// the quarantine/disable/re-activate admin control. A non-active worker is
// refused any further job offer by internal/workerapi.RequestJob's own
// status guard (see that method's doc comment).
func (s *UiService) SetWorkerStatus(ctx context.Context, req csilapi.SetWorkerStatusRequest) (csilapi.SetWorkerStatusResponse, error) {
	id, _, authErr := s.deps.requireUser(ctx)
	if authErr != nil {
		return csilapi.SetWorkerStatusResponse{}, authErr
	}
	if err := requireNonEmpty("worker_id", req.WorkerId, 64); err != nil {
		return csilapi.SetWorkerStatusResponse{}, err
	}
	if !workerStatusValid(req.Status) {
		return csilapi.SetWorkerStatusResponse{}, NewServiceError("invalid_argument", "status must be one of active, quarantined, disabled")
	}

	worker, err := s.deps.Store.GetWorkerByID(ctx, req.WorkerId)
	if err != nil {
		return csilapi.SetWorkerStatusResponse{}, mapStoreErr(err, "worker not found")
	}
	orgID, err := s.resolveWorkerOrgScope(ctx, worker)
	if err != nil {
		return csilapi.SetWorkerStatusResponse{}, mapStoreErr(err, "worker's pool not found")
	}
	if err := s.requireManageWorkers(ctx, id, orgID); err != nil {
		return csilapi.SetWorkerStatusResponse{}, err
	}

	if err := s.deps.Store.UpdateWorkerStatus(ctx, req.WorkerId, req.Status); err != nil {
		return csilapi.SetWorkerStatusResponse{}, mapStoreErr(err, "worker not found")
	}
	worker.Status = req.Status
	return csilapi.SetWorkerStatusResponse{Worker: s.workerToSummary(ctx, worker)}, nil
}

// DrainWorker asks a worker to stop accepting new work. Modeled as the
// "quarantined" status rather than a fourth persisted worker state (see
// reactorcide-ui.csil's DrainWorkerRequest doc comment for the design
// choice): internal/workerapi.RequestJob already refuses to offer a
// non-active worker any further job, and Heartbeat's response now carries a
// `draining` flag (derived from this same status) so a worker still
// finishing existing leases learns to shut itself down cleanly instead of
// polling forever.
func (s *UiService) DrainWorker(ctx context.Context, req csilapi.DrainWorkerRequest) (csilapi.DrainWorkerResponse, error) {
	id, _, authErr := s.deps.requireUser(ctx)
	if authErr != nil {
		return csilapi.DrainWorkerResponse{}, authErr
	}
	if err := requireNonEmpty("worker_id", req.WorkerId, 64); err != nil {
		return csilapi.DrainWorkerResponse{}, err
	}

	worker, err := s.deps.Store.GetWorkerByID(ctx, req.WorkerId)
	if err != nil {
		return csilapi.DrainWorkerResponse{}, mapStoreErr(err, "worker not found")
	}
	orgID, err := s.resolveWorkerOrgScope(ctx, worker)
	if err != nil {
		return csilapi.DrainWorkerResponse{}, mapStoreErr(err, "worker's pool not found")
	}
	if err := s.requireManageWorkers(ctx, id, orgID); err != nil {
		return csilapi.DrainWorkerResponse{}, err
	}

	if err := s.deps.Store.UpdateWorkerStatus(ctx, req.WorkerId, models.WorkerStatusQuarantined); err != nil {
		return csilapi.DrainWorkerResponse{}, mapStoreErr(err, "worker not found")
	}
	worker.Status = models.WorkerStatusQuarantined
	return csilapi.DrainWorkerResponse{Worker: s.workerToSummary(ctx, worker)}, nil
}
