package test

// Postgres-backed integration coverage for the dev/alpha worker enrollment
// bootstrap (WORKERS_PLAN.md Wave-4 P4):
// postgres_store.PostgresDbStore.EnsureDefaultWorkerPool. Verifies it
// creates a global "default" pool + an active enrollment token whose hash
// matches config.DefaultWorkerEnrollmentToken, that the resulting raw token
// validates through the exact same internal/workerauth.Enrollment path a
// real worker Register call drives, and that calling it again (mirroring a
// coordinator restart with the same env value) does not create a duplicate
// pool or token row.

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/config"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/postgres_store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/workerauth"
)

func TestEnsureDefaultWorkerPool_BootstrapsAndIsIdempotent(t *testing.T) {
	RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
		origToken := config.DefaultWorkerEnrollmentToken
		defer func() { config.DefaultWorkerEnrollmentToken = origToken }()

		rawToken := "bootstrap-token-" + uuid.NewString()
		config.DefaultWorkerEnrollmentToken = rawToken

		require.NoError(t, postgres_store.PostgresStore.EnsureDefaultWorkerPool(ctx))

		defaultPool := findDefaultWorkerPool(t, ctx)
		require.NotNil(t, defaultPool, "expected a global pool named %q", postgres_store.DefaultWorkerPoolName)

		tokens, err := postgres_store.PostgresStore.ListPoolEnrollmentTokens(ctx, defaultPool.PoolID)
		require.NoError(t, err)
		require.Len(t, tokens, 1)
		assert.True(t, tokens[0].IsActive)

		// The bootstrapped token must validate exactly like an admin-minted
		// one -- the same internal/workerauth.Enrollment path a real
		// worker's Register call drives.
		enrollment := workerauth.NewEnrollment(postgres_store.PostgresStore)
		validatedPool, err := enrollment.ValidateEnrollmentToken(ctx, rawToken)
		require.NoError(t, err)
		assert.Equal(t, defaultPool.PoolID, validatedPool.PoolID)

		// Idempotent: calling again with the same env value (mirroring a
		// coordinator restart) must not create a second pool or a second
		// token row.
		require.NoError(t, postgres_store.PostgresStore.EnsureDefaultWorkerPool(ctx))

		poolsAgain, err := postgres_store.PostgresStore.ListWorkerPools(ctx, nil)
		require.NoError(t, err)
		count := 0
		for _, p := range poolsAgain {
			if p.Name == postgres_store.DefaultWorkerPoolName && p.OrgID == nil {
				count++
			}
		}
		assert.Equal(t, 1, count, "EnsureDefaultWorkerPool must not create a duplicate default pool")

		tokensAgain, err := postgres_store.PostgresStore.ListPoolEnrollmentTokens(ctx, defaultPool.PoolID)
		require.NoError(t, err)
		assert.Len(t, tokensAgain, 1, "EnsureDefaultWorkerPool must not create a duplicate enrollment token")
	})
}

func TestEnsureDefaultWorkerPool_NoopWhenTokenUnset(t *testing.T) {
	RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
		origToken := config.DefaultWorkerEnrollmentToken
		defer func() { config.DefaultWorkerEnrollmentToken = origToken }()
		config.DefaultWorkerEnrollmentToken = ""

		require.NoError(t, postgres_store.PostgresStore.EnsureDefaultWorkerPool(ctx))

		if pool := findDefaultWorkerPool(t, ctx); pool != nil {
			t.Fatalf("a default pool was created despite no bootstrap token being configured: %+v", pool)
		}
	})
}

func findDefaultWorkerPool(t *testing.T, ctx context.Context) *models.WorkerPool {
	t.Helper()
	pools, err := postgres_store.PostgresStore.ListWorkerPools(ctx, nil)
	require.NoError(t, err)
	for i := range pools {
		if pools[i].Name == postgres_store.DefaultWorkerPoolName && pools[i].OrgID == nil {
			return &pools[i]
		}
	}
	return nil
}
