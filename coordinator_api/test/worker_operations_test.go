package test

// Postgres-backed integration coverage for the Wave-2 worker foundation:
// postgres_store/worker_operations.go and internal/workerauth against a real
// database, covering the full chain pool create -> enrollment token
// create+validate-by-hash -> worker upsert (insert then update the same
// worker_key) -> session create+resolve+ revoke -> lease
// create+release+stale-list.
//
// Like queue_operations_test.go, these run inside RunTransactionalTest so
// every row created here rolls back and never leaks into other tests
// sharing the long-lived test container.

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/characteristics"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/postgres_store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/workerauth"
)

// TestWorkerFoundation_FullChain exercises the end-to-end store-op chain a
// real Register/RequestJob/Heartbeat/ReportResult protocol implementation
// (P2-A2) will drive: pool -> enrollment token -> worker upsert -> worker
// session -> worker lease.
func TestWorkerFoundation_FullChain(t *testing.T) {
	RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
		du := &DataUtils{db: tx}

		// --- pool create --------------------------------------------------
		pool := &models.WorkerPool{Name: uniqueName("pool")}
		require.NoError(t, postgres_store.PostgresStore.CreateWorkerPool(ctx, pool))
		require.NotEmpty(t, pool.PoolID)

		got, err := postgres_store.PostgresStore.GetWorkerPoolByID(ctx, pool.PoolID)
		require.NoError(t, err)
		assert.Equal(t, pool.Name, got.Name)

		pools, err := postgres_store.PostgresStore.ListWorkerPools(ctx, nil)
		require.NoError(t, err)
		assert.NotEmpty(t, pools)

		require.NoError(t, postgres_store.PostgresStore.UpdateWorkerPool(ctx, pool.PoolID, pool.Name, "updated description"))
		got, err = postgres_store.PostgresStore.GetWorkerPoolByID(ctx, pool.PoolID)
		require.NoError(t, err)
		assert.Equal(t, "updated description", got.Description)

		// --- enrollment token create + validate-by-hash -------------------
		rawToken, tokenHash, err := workerauth.GenerateEnrollmentToken()
		require.NoError(t, err)
		assert.Len(t, tokenHash, 32, "expected a 32-byte SHA-256 hash")

		enrollmentToken, err := postgres_store.PostgresStore.CreatePoolEnrollmentToken(ctx, pool.PoolID, "ci-token", tokenHash)
		require.NoError(t, err)
		require.NotEmpty(t, enrollmentToken.TokenID)

		tokens, err := postgres_store.PostgresStore.ListPoolEnrollmentTokens(ctx, pool.PoolID)
		require.NoError(t, err)
		require.Len(t, tokens, 1)

		enrollment := workerauth.NewEnrollment(&storeAdapter{ctx: ctx})
		resolvedPool, err := enrollment.ValidateEnrollmentToken(ctx, rawToken)
		require.NoError(t, err)
		assert.Equal(t, pool.PoolID, resolvedPool.PoolID)

		// A wrong/forged token must be rejected.
		_, err = enrollment.ValidateEnrollmentToken(ctx, "not-the-right-token")
		assert.ErrorIs(t, err, workerauth.ErrEnrollmentRejected)

		// TouchEnrollmentTokenLastUsed ran as part of validation (best-effort).
		refreshedToken, err := postgres_store.PostgresStore.GetActiveEnrollmentTokenByHash(ctx, tokenHash)
		require.NoError(t, err)
		assert.NotNil(t, refreshedToken.LastUsedAt)

		// Deactivate is idempotent: repeat calls succeed as no-ops.
		require.NoError(t, postgres_store.PostgresStore.DeactivatePoolEnrollmentToken(ctx, enrollmentToken.TokenID))
		require.NoError(t, postgres_store.PostgresStore.DeactivatePoolEnrollmentToken(ctx, enrollmentToken.TokenID))
		_, err = postgres_store.PostgresStore.GetActiveEnrollmentTokenByHash(ctx, tokenHash)
		assert.ErrorIs(t, err, store.ErrNotFound, "expected a deactivated token to no longer resolve as active")

		// A deactivated token must also fail enrollment validation end-to-end.
		_, err = enrollment.ValidateEnrollmentToken(ctx, rawToken)
		assert.ErrorIs(t, err, workerauth.ErrEnrollmentRejected)

		// --- worker upsert (insert then update same worker_key) -----------
		workerKey := uniqueName("worker-key")
		chars, err := characteristics.ParseWorkerCharacteristics("linux", "amd64", nil)
		require.NoError(t, err)

		w := &models.Worker{
			PoolID:          pool.PoolID,
			WorkerKey:       workerKey,
			Hostname:        "ci-runner-1",
			OS:              "linux",
			Arch:            "amd64",
			Characteristics: chars,
			WorkerVersion:   "0.1.0",
		}
		created, err := postgres_store.PostgresStore.UpsertWorkerByKey(ctx, w)
		require.NoError(t, err)
		require.NotEmpty(t, created.WorkerID)
		assert.Equal(t, models.WorkerStatusActive, created.Status)
		require.NotNil(t, created.EnrolledAt)
		require.NotNil(t, created.LastSeenAt)

		byID, err := postgres_store.PostgresStore.GetWorkerByID(ctx, created.WorkerID)
		require.NoError(t, err)
		assert.Equal(t, workerKey, byID.WorkerKey)

		byKey, err := postgres_store.PostgresStore.GetWorkerByKey(ctx, workerKey)
		require.NoError(t, err)
		assert.Equal(t, created.WorkerID, byKey.WorkerID)

		// Re-register with the same worker_key: must update the existing
		// row, not create a second one.
		w2 := &models.Worker{
			PoolID:          pool.PoolID,
			WorkerKey:       workerKey,
			Hostname:        "ci-runner-1-renamed",
			OS:              "linux",
			Arch:            "arm64",
			Characteristics: chars,
			WorkerVersion:   "0.2.0",
		}
		updated, err := postgres_store.PostgresStore.UpsertWorkerByKey(ctx, w2)
		require.NoError(t, err)
		assert.Equal(t, created.WorkerID, updated.WorkerID, "expected upsert-by-key to update the existing row, not create a new one")
		assert.Equal(t, "arm64", updated.Arch)
		assert.Equal(t, "ci-runner-1-renamed", updated.Hostname)
		assert.Equal(t, "0.2.0", updated.WorkerVersion)
		assert.Equal(t, created.EnrolledAt.Unix(), updated.EnrolledAt.Unix(), "expected EnrolledAt to be preserved across an update")

		var workerCount int64
		require.NoError(t, tx.Model(&models.Worker{}).Where("worker_key = ?", workerKey).Count(&workerCount).Error)
		assert.Equal(t, int64(1), workerCount, "expected exactly one worker row for this worker_key")

		listed, err := postgres_store.PostgresStore.ListWorkersByPool(ctx, pool.PoolID)
		require.NoError(t, err)
		assert.Len(t, listed, 1)

		require.NoError(t, postgres_store.PostgresStore.UpdateWorkerStatus(ctx, created.WorkerID, models.WorkerStatusQuarantined))
		afterStatus, err := postgres_store.PostgresStore.GetWorkerByID(ctx, created.WorkerID)
		require.NoError(t, err)
		assert.Equal(t, models.WorkerStatusQuarantined, afterStatus.Status)

		require.NoError(t, postgres_store.PostgresStore.TouchWorkerLastSeen(ctx, created.WorkerID))

		// --- session create + resolve + revoke -----------------------------
		sessions := workerauth.NewWorkerSessions(&storeAdapter{ctx: ctx})
		rawSession, err := sessions.Mint(ctx, created.WorkerID)
		require.NoError(t, err)
		require.NotEmpty(t, rawSession)

		resolvedWorker, resolvedSession, err := sessions.Resolve(ctx, rawSession)
		require.NoError(t, err)
		assert.Equal(t, created.WorkerID, resolvedWorker.WorkerID)
		assert.Equal(t, created.WorkerID, resolvedSession.WorkerID)
		assert.WithinDuration(t, time.Now(), resolvedSession.ExpiresAt, workerauth.WorkerSessionTTL+time.Minute)

		require.NoError(t, sessions.Revoke(ctx, rawSession))
		_, _, err = sessions.Resolve(ctx, rawSession)
		assert.ErrorIs(t, err, store.ErrNotFound, "expected a revoked session to no longer resolve")

		// Revoke is idempotent.
		require.NoError(t, sessions.Revoke(ctx, rawSession))

		// --- lease create + release + stale-list ---------------------------
		job, err := du.CreateJob(DataSetup{})
		require.NoError(t, err)

		queueUUID := uuid.NewString()
		lease, err := postgres_store.PostgresStore.CreateWorkerLease(ctx, created.WorkerID, job.JobID, &queueUUID)
		require.NoError(t, err)
		require.NotEmpty(t, lease.LeaseID)
		assert.True(t, lease.IsActive())

		active, err := postgres_store.PostgresStore.ListActiveLeasesForWorker(ctx, created.WorkerID)
		require.NoError(t, err)
		require.Len(t, active, 1)
		assert.Equal(t, lease.LeaseID, active[0].LeaseID)

		require.NoError(t, postgres_store.PostgresStore.ReleaseWorkerLease(ctx, lease.LeaseID, "completed"))
		active, err = postgres_store.PostgresStore.ListActiveLeasesForWorker(ctx, created.WorkerID)
		require.NoError(t, err)
		assert.Empty(t, active, "expected no active leases after release")

		// Releasing again is idempotent.
		require.NoError(t, postgres_store.PostgresStore.ReleaseWorkerLease(ctx, lease.LeaseID, "ignored-second-outcome"))

		// A second, still-open lease acquired "in the past" (backdated
		// directly via tx, since AcquiredAt has a DB-side default) shows up
		// in the stale scan.
		staleLease, err := postgres_store.PostgresStore.CreateWorkerLease(ctx, created.WorkerID, job.JobID, &queueUUID)
		require.NoError(t, err)
		backdated := time.Now().UTC().Add(-2 * time.Hour)
		require.NoError(t, tx.Model(&models.WorkerLease{}).
			Where("lease_id = ?", staleLease.LeaseID).
			Update("last_heartbeat_at", backdated).Error)

		stale, err := postgres_store.PostgresStore.ListStaleActiveLeases(ctx, time.Now().UTC().Add(-1*time.Hour))
		require.NoError(t, err)
		var foundStale bool
		for _, l := range stale {
			if l.LeaseID == staleLease.LeaseID {
				foundStale = true
			}
			// The released lease from earlier must never appear in an
			// active-lease scan.
			assert.NotEqual(t, lease.LeaseID, l.LeaseID)
		}
		assert.True(t, foundStale, "expected the backdated lease to appear in ListStaleActiveLeases")
	})
}

// storeAdapter narrows postgres_store.PostgresStore down to
// workerauth.EnrollmentTokenStore/WorkerSessionStore for these tests --
// mirroring queue_operations_test.go's queueStore narrowing -- and pins the
// RunTransactionalTest ctx (which carries the tx) so calls through it see
// the same uncommitted rows the test set up directly via
// postgres_store.PostgresStore.
type storeAdapter struct {
	ctx context.Context
}

func (a *storeAdapter) GetActiveEnrollmentTokenByHash(ctx context.Context, tokenHash []byte) (*models.PoolEnrollmentToken, error) {
	return postgres_store.PostgresStore.GetActiveEnrollmentTokenByHash(ctx, tokenHash)
}

func (a *storeAdapter) TouchEnrollmentTokenLastUsed(ctx context.Context, tokenID string) error {
	return postgres_store.PostgresStore.TouchEnrollmentTokenLastUsed(ctx, tokenID)
}

func (a *storeAdapter) GetWorkerPoolByID(ctx context.Context, poolID string) (*models.WorkerPool, error) {
	return postgres_store.PostgresStore.GetWorkerPoolByID(ctx, poolID)
}

func (a *storeAdapter) CreateWorkerSession(ctx context.Context, session *models.WorkerSession) error {
	return postgres_store.PostgresStore.CreateWorkerSession(ctx, session)
}

func (a *storeAdapter) GetActiveWorkerSessionByTokenHash(ctx context.Context, tokenHash []byte) (*models.WorkerSession, *models.Worker, error) {
	return postgres_store.PostgresStore.GetActiveWorkerSessionByTokenHash(ctx, tokenHash)
}

func (a *storeAdapter) TouchWorkerSessionLastSeen(ctx context.Context, sessionID string) error {
	return postgres_store.PostgresStore.TouchWorkerSessionLastSeen(ctx, sessionID)
}

func (a *storeAdapter) RevokeWorkerSession(ctx context.Context, sessionID string) error {
	return postgres_store.PostgresStore.RevokeWorkerSession(ctx, sessionID)
}
