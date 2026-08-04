package postgres_store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/checkauth"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/config"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"gorm.io/gorm"
)

// --- worker_pools -------------------------------------------------------

// CreateWorkerPool creates a new worker pool row.
func (ps PostgresDbStore) CreateWorkerPool(ctx context.Context, pool *models.WorkerPool) error {
	if err := ps.getDB(ctx).Create(pool).Error; err != nil {
		if isUniqueViolation(err) {
			return fmt.Errorf("a worker pool with this name already exists for this org: %w", store.ErrAlreadyExists)
		}
		return fmt.Errorf("failed to create worker pool: %w", err)
	}
	return nil
}

// GetWorkerPoolByID retrieves a worker pool by its primary key.
func (ps PostgresDbStore) GetWorkerPoolByID(ctx context.Context, poolID string) (*models.WorkerPool, error) {
	if !isValidUUID(poolID) {
		return nil, store.ErrNotFound
	}

	var p models.WorkerPool
	if err := ps.getDB(ctx).Where("pool_id = ?", poolID).First(&p).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, store.ErrNotFound
		}
		return nil, fmt.Errorf("failed to get worker pool %s: %w", poolID, err)
	}
	return &p, nil
}

// ListWorkerPools lists worker pools, newest first, optionally scoped to a
// single org (nil orgID lists across all orgs, including global pools).
func (ps PostgresDbStore) ListWorkerPools(ctx context.Context, orgID *string) ([]models.WorkerPool, error) {
	query := ps.getDB(ctx).Order("created_at DESC")
	if orgID != nil {
		query = query.Where("org_id = ?", *orgID)
	}

	var pools []models.WorkerPool
	if err := query.Find(&pools).Error; err != nil {
		return nil, fmt.Errorf("failed to list worker pools: %w", err)
	}
	return pools, nil
}

// UpdateWorkerPool updates a pool's mutable fields (name, description).
// OrgID is immutable once set -- a pool doesn't change ownership through
// this path.
func (ps PostgresDbStore) UpdateWorkerPool(ctx context.Context, poolID, name, description string) error {
	if !isValidUUID(poolID) {
		return store.ErrNotFound
	}

	result := ps.getDB(ctx).Model(&models.WorkerPool{}).
		Where("pool_id = ?", poolID).
		Updates(map[string]interface{}{
			"name":        name,
			"description": description,
			"updated_at":  gorm.Expr("timezone('utc', now())"),
		})
	if result.Error != nil {
		return fmt.Errorf("failed to update worker pool %s: %w", poolID, result.Error)
	}
	if result.RowsAffected == 0 {
		return store.ErrNotFound
	}
	return nil
}

// DeleteWorkerPool deletes a worker pool row. Enrollment tokens for the pool
// cascade-delete (ON DELETE CASCADE); workers referencing the pool do NOT
// (no ON DELETE clause on workers.pool_id), so a pool with existing workers
// fails this delete with a foreign key violation -- callers that need
// delete-orphaned-workers orchestration must do it themselves before/around
// calling this, mirroring DeleteQueue's row-level-only contract.
func (ps PostgresDbStore) DeleteWorkerPool(ctx context.Context, poolID string) error {
	if !isValidUUID(poolID) {
		return store.ErrNotFound
	}

	result := ps.getDB(ctx).Where("pool_id = ?", poolID).Delete(&models.WorkerPool{})
	if result.Error != nil {
		return fmt.Errorf("failed to delete worker pool %s: %w", poolID, result.Error)
	}
	if result.RowsAffected == 0 {
		return store.ErrNotFound
	}
	return nil
}

// --- pool_enrollment_tokens -----------------------------------------------

// CreatePoolEnrollmentToken creates a new enrollment token row for a pool.
// tokenHash must already be the SHA-256 hash of the raw token (see
// internal/workerauth.GenerateEnrollmentToken) -- this layer never sees or
// stores the raw value.
func (ps PostgresDbStore) CreatePoolEnrollmentToken(ctx context.Context, poolID, name string, tokenHash []byte) (*models.PoolEnrollmentToken, error) {
	if !isValidUUID(poolID) {
		return nil, store.ErrNotFound
	}

	t := &models.PoolEnrollmentToken{
		PoolID:    poolID,
		Name:      name,
		TokenHash: tokenHash,
		IsActive:  true,
	}
	if err := ps.getDB(ctx).Create(t).Error; err != nil {
		if isUniqueViolation(err) {
			return nil, fmt.Errorf("an enrollment token with this hash already exists: %w", store.ErrAlreadyExists)
		}
		return nil, fmt.Errorf("failed to create pool enrollment token: %w", err)
	}
	return t, nil
}

// GetPoolEnrollmentTokenByID retrieves an enrollment token by its primary
// key (active or not). Added alongside internal/uiapi (WORKERS_PLAN.md
// Wave-4 P4): deactivate-enrollment-token targets a token by ID alone (no
// pool_id in the request), so it must load the row first to discover its
// owning pool (and, through that, its owning org) before it can run an
// authorization check against that scope -- the same "additive bare-ID
// lookup" pattern documented on uiapi.DataStore for its other mutating ops.
func (ps PostgresDbStore) GetPoolEnrollmentTokenByID(ctx context.Context, tokenID string) (*models.PoolEnrollmentToken, error) {
	if !isValidUUID(tokenID) {
		return nil, store.ErrNotFound
	}

	var t models.PoolEnrollmentToken
	if err := ps.getDB(ctx).Where("token_id = ?", tokenID).First(&t).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, store.ErrNotFound
		}
		return nil, fmt.Errorf("failed to get pool enrollment token %s: %w", tokenID, err)
	}
	return &t, nil
}

// ListPoolEnrollmentTokens lists every enrollment token (active and
// deactivated) for a pool, newest first -- for admin display, where a
// rotated-out token's history still matters.
func (ps PostgresDbStore) ListPoolEnrollmentTokens(ctx context.Context, poolID string) ([]models.PoolEnrollmentToken, error) {
	var tokens []models.PoolEnrollmentToken
	if err := ps.getDB(ctx).
		Where("pool_id = ?", poolID).
		Order("created_at DESC").
		Find(&tokens).Error; err != nil {
		return nil, fmt.Errorf("failed to list pool enrollment tokens: %w", err)
	}
	return tokens, nil
}

// GetActiveEnrollmentTokenByHash looks up an active (is_active) enrollment
// token by its SHA-256 hash. A deactivated token's hash never matches here
// even if otherwise correct -- mirrors GetActiveUISessionByTokenHash's
// active-only semantics. Returns store.ErrNotFound on no match; callers in
// internal/workerauth translate that into the deliberately
// non-distinguishing ErrEnrollmentRejected.
func (ps PostgresDbStore) GetActiveEnrollmentTokenByHash(ctx context.Context, tokenHash []byte) (*models.PoolEnrollmentToken, error) {
	var t models.PoolEnrollmentToken
	if err := ps.getDB(ctx).
		Where("token_hash = ? AND is_active", tokenHash).
		First(&t).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, store.ErrNotFound
		}
		return nil, fmt.Errorf("failed to get active enrollment token: %w", err)
	}
	return &t, nil
}

// TouchEnrollmentTokenLastUsed stamps last_used_at=now() for an enrollment
// token by ID.
func (ps PostgresDbStore) TouchEnrollmentTokenLastUsed(ctx context.Context, tokenID string) error {
	if !isValidUUID(tokenID) {
		return store.ErrNotFound
	}

	result := ps.getDB(ctx).Model(&models.PoolEnrollmentToken{}).
		Where("token_id = ?", tokenID).
		Update("last_used_at", time.Now().UTC())
	if result.Error != nil {
		return fmt.Errorf("failed to touch enrollment token %s last used: %w", tokenID, result.Error)
	}
	if result.RowsAffected == 0 {
		return store.ErrNotFound
	}
	return nil
}

// DeactivatePoolEnrollmentToken marks an enrollment token inactive and
// stamps deactivated_at. Idempotent, mirroring
// DeactivateProjectWebhookSecret/DeactivateProjectVCSCredential: an
// already-inactive-but-existing row succeeds as a no-op (original
// deactivated_at preserved) rather than reporting store.ErrNotFound; only a
// wholly missing row does that.
func (ps PostgresDbStore) DeactivatePoolEnrollmentToken(ctx context.Context, tokenID string) error {
	if !isValidUUID(tokenID) {
		return store.ErrNotFound
	}

	now := time.Now().UTC()
	result := ps.getDB(ctx).Model(&models.PoolEnrollmentToken{}).
		Where("token_id = ? AND is_active", tokenID).
		Updates(map[string]interface{}{
			"is_active":      false,
			"deactivated_at": now,
		})
	if result.Error != nil {
		return fmt.Errorf("failed to deactivate enrollment token %s: %w", tokenID, result.Error)
	}
	if result.RowsAffected == 0 {
		var count int64
		if err := ps.getDB(ctx).Model(&models.PoolEnrollmentToken{}).
			Where("token_id = ?", tokenID).
			Count(&count).Error; err != nil {
			return fmt.Errorf("failed to check enrollment token existence: %w", err)
		}
		if count == 0 {
			return store.ErrNotFound
		}
		// Row exists and is already inactive: no-op success, original
		// deactivated_at is left untouched.
	}
	return nil
}

// --- workers --------------------------------------------------------------

// UpsertWorkerByKey inserts a new worker row, or -- if a row with the same
// WorkerKey already exists -- refreshes its pool, hostname, os, arch,
// characteristics, worker_version, status (when worker.Status is set), and
// last_seen_at, leaving WorkerID/EnrolledAt/CreatedAt untouched. This is the
// Register upsert primitive (WORKERS_PLAN.md "Workers" -- "Upserts a
// workers row keyed by a stable worker-provided worker_key").
//
// Concurrent-insert race: two Registers racing on the SAME never-before-seen
// worker_key will have one INSERT succeed and the other fail on the unique
// worker_key index; the loser re-selects and updates the winner's row
// instead of erroring, mirroring FindOrCreateQueueByCharacteristics'
// race-handling.
func (ps PostgresDbStore) UpsertWorkerByKey(ctx context.Context, worker *models.Worker) (*models.Worker, error) {
	if worker.WorkerKey == "" {
		return nil, fmt.Errorf("worker_operations: worker_key is required: %w", store.ErrInvalidInput)
	}

	existing, err := ps.GetWorkerByKey(ctx, worker.WorkerKey)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}

	now := time.Now().UTC()

	if existing == nil {
		toCreate := *worker
		if toCreate.EnrolledAt == nil {
			toCreate.EnrolledAt = &now
		}
		toCreate.LastSeenAt = &now
		if toCreate.Status == "" {
			toCreate.Status = models.WorkerStatusActive
		}

		if createErr := ps.getDB(ctx).Create(&toCreate).Error; createErr != nil {
			if !isUniqueViolation(createErr) {
				return nil, fmt.Errorf("failed to create worker: %w", createErr)
			}
			// Lost the race to a concurrent Register with the same
			// worker_key -- fall through to the update path below against
			// the winner's row.
			existing, err = ps.GetWorkerByKey(ctx, worker.WorkerKey)
			if err != nil {
				return nil, fmt.Errorf("worker upsert create raced but re-select failed: %w", err)
			}
		} else {
			return &toCreate, nil
		}
	}

	existing.PoolID = worker.PoolID
	existing.Hostname = worker.Hostname
	existing.OS = worker.OS
	existing.Arch = worker.Arch
	existing.Characteristics = worker.Characteristics
	existing.WorkerVersion = worker.WorkerVersion
	if worker.Status != "" {
		existing.Status = worker.Status
	}
	existing.LastSeenAt = &now

	if saveErr := ps.getDB(ctx).Save(existing).Error; saveErr != nil {
		return nil, fmt.Errorf("failed to update worker: %w", saveErr)
	}
	return existing, nil
}

// GetWorkerByID retrieves a worker by its primary key.
func (ps PostgresDbStore) GetWorkerByID(ctx context.Context, workerID string) (*models.Worker, error) {
	if !isValidUUID(workerID) {
		return nil, store.ErrNotFound
	}

	var w models.Worker
	if err := ps.getDB(ctx).Where("worker_id = ?", workerID).First(&w).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, store.ErrNotFound
		}
		return nil, fmt.Errorf("failed to get worker %s: %w", workerID, err)
	}
	return &w, nil
}

// GetWorkerByKey retrieves a worker by its worker-provided WorkerKey.
func (ps PostgresDbStore) GetWorkerByKey(ctx context.Context, workerKey string) (*models.Worker, error) {
	if workerKey == "" {
		return nil, store.ErrNotFound
	}

	var w models.Worker
	if err := ps.getDB(ctx).Where("worker_key = ?", workerKey).First(&w).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, store.ErrNotFound
		}
		return nil, fmt.Errorf("failed to get worker by key: %w", err)
	}
	return &w, nil
}

// ListWorkersByPool lists every worker in a pool, newest first.
func (ps PostgresDbStore) ListWorkersByPool(ctx context.Context, poolID string) ([]models.Worker, error) {
	var workers []models.Worker
	if err := ps.getDB(ctx).
		Where("pool_id = ?", poolID).
		Order("created_at DESC").
		Find(&workers).Error; err != nil {
		return nil, fmt.Errorf("failed to list workers for pool %s: %w", poolID, err)
	}
	return workers, nil
}

// ListWorkers lists workers, newest first, optionally scoped to a single
// pool (nil poolID lists across every pool) -- the admin list-workers op's
// "optional pool filter" (WORKERS_PLAN.md Wave-4 P4). Distinct from
// ListWorkersByPool (which always requires a pool): kept as its own method
// rather than changing ListWorkersByPool's signature, since RequestJob's
// matching path and any other existing caller of ListWorkersByPool always
// has a concrete pool in hand.
func (ps PostgresDbStore) ListWorkers(ctx context.Context, poolID *string) ([]models.Worker, error) {
	query := ps.getDB(ctx).Order("created_at DESC")
	if poolID != nil {
		query = query.Where("pool_id = ?", *poolID)
	}

	var workers []models.Worker
	if err := query.Find(&workers).Error; err != nil {
		return nil, fmt.Errorf("failed to list workers: %w", err)
	}
	return workers, nil
}

// UpdateWorkerStatus sets a worker's status (active/quarantined/disabled) --
// the admin quarantine/disable/enable surface (WORKERS_PLAN.md P4).
func (ps PostgresDbStore) UpdateWorkerStatus(ctx context.Context, workerID, status string) error {
	if !isValidUUID(workerID) {
		return store.ErrNotFound
	}

	result := ps.getDB(ctx).Model(&models.Worker{}).
		Where("worker_id = ?", workerID).
		Update("status", status)
	if result.Error != nil {
		return fmt.Errorf("failed to update worker %s status: %w", workerID, result.Error)
	}
	if result.RowsAffected == 0 {
		return store.ErrNotFound
	}
	return nil
}

// TouchWorkerLastSeen stamps last_seen_at=now() for a worker by ID.
func (ps PostgresDbStore) TouchWorkerLastSeen(ctx context.Context, workerID string) error {
	if !isValidUUID(workerID) {
		return store.ErrNotFound
	}

	result := ps.getDB(ctx).Model(&models.Worker{}).
		Where("worker_id = ?", workerID).
		Update("last_seen_at", time.Now().UTC())
	if result.Error != nil {
		return fmt.Errorf("failed to touch worker %s last seen: %w", workerID, result.Error)
	}
	if result.RowsAffected == 0 {
		return store.ErrNotFound
	}
	return nil
}

// --- worker_sessions --------------------------------------------------------

// CreateWorkerSession creates a new worker session row. Session.TokenHash
// must already be a SHA-256 hash (see internal/workerauth.WorkerSessions.Mint)
// -- this layer never sees or stores the raw token.
func (ps PostgresDbStore) CreateWorkerSession(ctx context.Context, session *models.WorkerSession) error {
	if err := ps.getDB(ctx).Create(session).Error; err != nil {
		return fmt.Errorf("failed to create worker session: %w", err)
	}
	return nil
}

// GetActiveWorkerSessionByTokenHash looks up a non-revoked, non-expired
// worker session by its token hash, and returns it together with its
// owning worker row (a "join" in the sense the caller gets both without a
// second explicit lookup -- see WORKERS_PLAN.md P2-A's ask for this
// signature). Returns store.ErrNotFound if no active session matches the
// hash, or if the session's worker row is somehow missing.
func (ps PostgresDbStore) GetActiveWorkerSessionByTokenHash(ctx context.Context, tokenHash []byte) (*models.WorkerSession, *models.Worker, error) {
	var session models.WorkerSession
	if err := ps.getDB(ctx).
		Where("token_hash = ? AND revoked_at IS NULL AND expires_at > ?", tokenHash, time.Now().UTC()).
		First(&session).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil, store.ErrNotFound
		}
		return nil, nil, fmt.Errorf("failed to get active worker session: %w", err)
	}

	worker, err := ps.GetWorkerByID(ctx, session.WorkerID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, nil, store.ErrNotFound
		}
		return nil, nil, fmt.Errorf("failed to resolve worker session's worker: %w", err)
	}
	return &session, worker, nil
}

// TouchWorkerSessionLastSeen stamps last_seen_at=now() for a worker session
// by ID.
func (ps PostgresDbStore) TouchWorkerSessionLastSeen(ctx context.Context, sessionID string) error {
	if !isValidUUID(sessionID) {
		return store.ErrNotFound
	}

	result := ps.getDB(ctx).Model(&models.WorkerSession{}).
		Where("session_id = ?", sessionID).
		Update("last_seen_at", time.Now().UTC())
	if result.Error != nil {
		return fmt.Errorf("failed to touch worker session %s last seen: %w", sessionID, result.Error)
	}
	if result.RowsAffected == 0 {
		return store.ErrNotFound
	}
	return nil
}

// RevokeWorkerSession revokes a worker session by ID. Idempotent: revoking
// an already-revoked (but existing) session succeeds as a no-op, mirroring
// internal/auth.Sessions.RevokeSession's caller-facing idempotency. Only a
// wholly missing session row returns store.ErrNotFound.
func (ps PostgresDbStore) RevokeWorkerSession(ctx context.Context, sessionID string) error {
	if !isValidUUID(sessionID) {
		return store.ErrNotFound
	}

	now := time.Now().UTC()
	result := ps.getDB(ctx).Model(&models.WorkerSession{}).
		Where("session_id = ? AND revoked_at IS NULL", sessionID).
		Update("revoked_at", now)
	if result.Error != nil {
		return fmt.Errorf("failed to revoke worker session %s: %w", sessionID, result.Error)
	}
	if result.RowsAffected == 0 {
		var count int64
		if err := ps.getDB(ctx).Model(&models.WorkerSession{}).
			Where("session_id = ?", sessionID).
			Count(&count).Error; err != nil {
			return fmt.Errorf("failed to check worker session existence: %w", err)
		}
		if count == 0 {
			return store.ErrNotFound
		}
		// Already revoked: no-op success.
	}
	return nil
}

// DeleteExpiredWorkerSessions deletes worker sessions whose expires_at has
// passed, returning the number of rows removed. Housekeeping primitive for
// a future scheduled reaper -- P2-A does not wire a caller for this yet.
func (ps PostgresDbStore) DeleteExpiredWorkerSessions(ctx context.Context) (int64, error) {
	result := ps.getDB(ctx).Where("expires_at < ?", time.Now().UTC()).Delete(&models.WorkerSession{})
	if result.Error != nil {
		return 0, fmt.Errorf("failed to delete expired worker sessions: %w", result.Error)
	}
	return result.RowsAffected, nil
}

// --- worker_leases ----------------------------------------------------------

// CreateWorkerLease records a new worker<->job claim. queueUUID is optional
// (display/audit convenience -- the routing identity of record remains the
// job row's own QueueName).
func (ps PostgresDbStore) CreateWorkerLease(ctx context.Context, workerID, jobID string, queueUUID *string) (*models.WorkerLease, error) {
	lease := &models.WorkerLease{
		WorkerID:  workerID,
		JobID:     jobID,
		QueueUUID: queueUUID,
	}
	if err := ps.getDB(ctx).Create(lease).Error; err != nil {
		return nil, fmt.Errorf("failed to create worker lease: %w", err)
	}
	return lease, nil
}

// GetWorkerLeaseByID retrieves a worker lease by its primary key. Added
// alongside internal/workerapi (P2-A2): AppendLogs/ReportResult need to
// resolve a lease_id from the CSIL-RPC request into its owning worker/job
// before acting on it, which none of the existing list-scoped lease queries
// (ListActiveLeasesForWorker/ListStaleActiveLeases) provide.
func (ps PostgresDbStore) GetWorkerLeaseByID(ctx context.Context, leaseID string) (*models.WorkerLease, error) {
	if !isValidUUID(leaseID) {
		return nil, store.ErrNotFound
	}

	var l models.WorkerLease
	if err := ps.getDB(ctx).Where("lease_id = ?", leaseID).First(&l).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, store.ErrNotFound
		}
		return nil, fmt.Errorf("failed to get worker lease %s: %w", leaseID, err)
	}
	return &l, nil
}

// TouchWorkerLeaseHeartbeat records liveness for one reported active lease.
func (ps PostgresDbStore) TouchWorkerLeaseHeartbeat(ctx context.Context, leaseID string) error {
	result := ps.getDB(ctx).Model(&models.WorkerLease{}).
		Where("lease_id = ? AND released_at IS NULL", leaseID).
		Update("last_heartbeat_at", time.Now().UTC())
	if result.Error != nil {
		return fmt.Errorf("touch worker lease heartbeat: %w", result.Error)
	}
	return nil
}

// ReleaseWorkerLease marks a lease released with the given outcome.
// Idempotent: releasing an already-released lease is a no-op success (the
// first release's outcome wins), mirroring DeactivatePoolEnrollmentToken's
// semantics; only a wholly missing lease returns store.ErrNotFound.
func (ps PostgresDbStore) ReleaseWorkerLease(ctx context.Context, leaseID, outcome string) error {
	if !isValidUUID(leaseID) {
		return store.ErrNotFound
	}

	now := time.Now().UTC()
	result := ps.getDB(ctx).Model(&models.WorkerLease{}).
		Where("lease_id = ? AND released_at IS NULL", leaseID).
		Updates(map[string]interface{}{
			"released_at": now,
			"outcome":     outcome,
		})
	if result.Error != nil {
		return fmt.Errorf("failed to release worker lease %s: %w", leaseID, result.Error)
	}
	if result.RowsAffected == 0 {
		var count int64
		if err := ps.getDB(ctx).Model(&models.WorkerLease{}).
			Where("lease_id = ?", leaseID).
			Count(&count).Error; err != nil {
			return fmt.Errorf("failed to check worker lease existence: %w", err)
		}
		if count == 0 {
			return store.ErrNotFound
		}
		// Already released: no-op success.
	}
	return nil
}

// ListActiveLeasesForWorker lists a worker's still-open (released_at IS
// NULL) leases, most recently acquired first. Used by Heartbeat to know
// which corndogs tasks to extend.
func (ps PostgresDbStore) ListActiveLeasesForWorker(ctx context.Context, workerID string) ([]models.WorkerLease, error) {
	var leases []models.WorkerLease
	if err := ps.getDB(ctx).
		Where("worker_id = ? AND released_at IS NULL", workerID).
		Order("acquired_at DESC").
		Find(&leases).Error; err != nil {
		return nil, fmt.Errorf("failed to list active leases for worker %s: %w", workerID, err)
	}
	return leases, nil
}

// ListStaleActiveLeases returns every still-open lease (released_at IS
// NULL) acquired before olderThan -- for the future reaper that mirrors
// runCancellingReaper (WORKERS_PLAN.md "Workers"): a lease this old with no
// release almost certainly belongs to a worker that stopped heartbeating,
// whose corndogs task has already timed out and requeued on its own; the
// reaper's job here is just to mark the display/audit row released, not to
// touch corndogs.
func (ps PostgresDbStore) ListStaleActiveLeases(ctx context.Context, olderThan time.Time) ([]models.WorkerLease, error) {
	var leases []models.WorkerLease
	// Reap by the LEASE's liveness, not the worker row. A replacement process
	// can use the same worker key without keeping an unreported old lease open.
	// legitimately long-running job (e.g. a multi-arch release build) holds an
	// open lease for a long time while its worker keeps heartbeating, and must
	// NOT be reaped -- reaping it releases the lease and AppendLogs then rejects
	// the job's remaining output. Only leases whose worker has actually gone
	// silent (last_seen_at older than the threshold) are stale.
	if err := ps.getDB(ctx).
		Where("worker_leases.released_at IS NULL AND worker_leases.last_heartbeat_at < ?", olderThan).
		Order("worker_leases.acquired_at ASC").
		Find(&leases).Error; err != nil {
		return nil, fmt.Errorf("failed to list stale active leases: %w", err)
	}
	return leases, nil
}

// --- dev/alpha enrollment bootstrap ---------------------------------------

// DefaultWorkerPoolName is the pool name EnsureDefaultWorkerPool
// finds-or-creates: a global (org_id NULL) pool reserved for the dev/alpha
// enrollment bootstrap (WORKERS_PLAN.md Wave-4 P4).
const DefaultWorkerPoolName = "default"

// defaultWorkerEnrollmentTokenName is the pool_enrollment_tokens.name stamped
// on the token EnsureDefaultWorkerPool registers, so it's recognizable (vs.
// an admin-created token) in the admin list-enrollment-tokens view.
const defaultWorkerEnrollmentTokenName = "dev-bootstrap"

// EnsureDefaultWorkerPool is the dev/alpha worker enrollment bootstrap
// (WORKERS_PLAN.md Wave-4 P4): if config.DefaultWorkerEnrollmentToken (env
// REACTORCIDE_DEFAULT_WORKER_ENROLLMENT_TOKEN) is set, ensure a global pool
// named "default" exists and that this token's SHA-256 hash is registered as
// an active pool_enrollment_tokens row for it -- so a dev/compose worker can
// enroll with a token known in advance rather than one minted through the
// admin UI. A no-op if the env var is unset.
//
// Idempotent by comparing the token's HASH (never a stored raw value -- the
// raw value is never persisted anywhere, mirroring every other enrollment
// token) against existing pool_enrollment_tokens rows: a restart with the
// same env value finds its hash already registered and does nothing further,
// so it never creates a duplicate token row. Safe under concurrent
// coordinator pods starting simultaneously for the same reason
// EnsureDefaultQueue/CreatePoolEnrollmentToken's own unique constraints are:
// a lost race here just means the other pod's insert already achieved the
// desired end state.
//
// Call this once at startup, alongside EnsureDefaultUser/EnsureDefaultQueue
// (see cmd/api.go's initStores) -- deliberately NOT part of the store.Store
// interface those two are (this repo's narrow-interface convention: only
// cmd/api.go's startup path needs it, so it's reached there via a type
// assertion rather than growing store.Store for every store fake in the
// codebase).
func (ps PostgresDbStore) EnsureDefaultWorkerPool(ctx context.Context) error {
	raw := config.DefaultWorkerEnrollmentToken
	if raw == "" {
		return nil // no dev/alpha bootstrap configured
	}
	hash := checkauth.HashAPIToken(raw)

	var existingCount int64
	if err := ps.getDB(ctx).Model(&models.PoolEnrollmentToken{}).
		Where("token_hash = ?", hash).
		Count(&existingCount).Error; err != nil {
		return fmt.Errorf("failed to check for existing default worker enrollment token: %w", err)
	}
	if existingCount > 0 {
		// Already registered (by this process on a previous startup, or by
		// another coordinator pod) -- nothing to do. Deliberately does not
		// re-read or compare the raw value: the hash match alone is the
		// idempotency key.
		return nil
	}

	pool, err := ps.ensureDefaultWorkerPoolRow(ctx)
	if err != nil {
		return err
	}

	if _, err := ps.CreatePoolEnrollmentToken(ctx, pool.PoolID, defaultWorkerEnrollmentTokenName, hash); err != nil {
		if errors.Is(err, store.ErrAlreadyExists) {
			// Lost a race with a concurrent coordinator pod registering the
			// same token hash -- desired end state holds either way.
			return nil
		}
		return fmt.Errorf("failed to register default worker enrollment token: %w", err)
	}
	return nil
}

// ensureDefaultWorkerPoolRow finds or creates the global "default" worker
// pool EnsureDefaultWorkerPool's token attaches to.
func (ps PostgresDbStore) ensureDefaultWorkerPoolRow(ctx context.Context) (*models.WorkerPool, error) {
	var pool models.WorkerPool
	err := ps.getDB(ctx).Where("org_id IS NULL AND name = ?", DefaultWorkerPoolName).First(&pool).Error
	if err == nil {
		return &pool, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, fmt.Errorf("failed to check for default worker pool: %w", err)
	}

	newPool := &models.WorkerPool{Name: DefaultWorkerPoolName}
	if err := ps.CreateWorkerPool(ctx, newPool); err != nil {
		if errors.Is(err, store.ErrAlreadyExists) {
			// Lost a race with a concurrent coordinator pod creating the
			// same pool between our SELECT and our INSERT -- re-select
			// rather than surfacing a spurious error.
			var winner models.WorkerPool
			if selErr := ps.getDB(ctx).Where("org_id IS NULL AND name = ?", DefaultWorkerPoolName).First(&winner).Error; selErr != nil {
				return nil, fmt.Errorf("default worker pool create lost race but re-select failed: %w", selErr)
			}
			return &winner, nil
		}
		return nil, fmt.Errorf("failed to create default worker pool: %w", err)
	}
	return newPool, nil
}
