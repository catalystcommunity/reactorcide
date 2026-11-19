package test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/characteristics"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/postgres_store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/workerauth"
)

// Migration 000066: a worker records its enrollment token, ListWorkers joins
// the token name and state and the session state, ListPoolEnrollmentTokens
// counts the active workers of each token, and a deactivated token cannot be
// activated again.
func TestWorkerEnrollmentTokenAudit(t *testing.T) {
	RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
		ps := postgres_store.PostgresStore
		pool := &models.WorkerPool{Name: uniqueName("pool")}
		require.NoError(t, ps.CreateWorkerPool(ctx, pool))

		token := func(name string) *models.PoolEnrollmentToken {
			_, hash, err := workerauth.GenerateEnrollmentToken()
			require.NoError(t, err)
			tok, err := ps.CreatePoolEnrollmentToken(ctx, pool.PoolID, name, hash)
			require.NoError(t, err)
			return tok
		}
		oldTok, newTok := token("2025"), token("")

		chars, err := characteristics.ParseWorkerCharacteristics("linux", "amd64", nil)
		require.NoError(t, err)
		worker := func(key string, tok *models.PoolEnrollmentToken, session bool) *models.Worker {
			w, err := ps.UpsertWorkerByKey(ctx, &models.Worker{PoolID: pool.PoolID, WorkerKey: uniqueName(key), OS: "linux", Arch: "amd64",
				Characteristics: chars, EnrollmentTokenID: &tok.TokenID})
			require.NoError(t, err)
			if session {
				_, hash, err := workerauth.GenerateEnrollmentToken()
				require.NoError(t, err)
				require.NoError(t, ps.CreateWorkerSession(ctx, &models.WorkerSession{WorkerID: w.WorkerID, TokenHash: hash,
					ExpiresAt: time.Now().UTC().Add(time.Hour)}))
			}
			return w
		}
		running := worker("running", oldTok, true)
		stopped := worker("stopped", oldTok, false)
		current := worker("current", newTok, true)

		// An upsert with no token keeps the recorded one (an old binary
		// writes no token).
		again, err := ps.UpsertWorkerByKey(ctx, &models.Worker{PoolID: pool.PoolID, WorkerKey: running.WorkerKey, OS: "linux", Arch: "amd64", Characteristics: chars})
		require.NoError(t, err)
		require.NotNil(t, again.EnrollmentTokenID)
		assert.Equal(t, oldTok.TokenID, *again.EnrollmentTokenID)

		tokens, err := ps.ListPoolEnrollmentTokens(ctx, pool.PoolID)
		require.NoError(t, err)
		counts := map[string]int64{}
		for _, tok := range tokens {
			counts[tok.TokenID] = tok.ActiveWorkerCount
		}
		assert.Equal(t, int64(1), counts[oldTok.TokenID], "stopped workers do not count")
		assert.Equal(t, int64(1), counts[newTok.TokenID])

		require.NoError(t, ps.DeactivatePoolEnrollmentToken(ctx, oldTok.TokenID))

		workers, err := ps.ListWorkers(ctx, &pool.PoolID)
		require.NoError(t, err)
		byID := map[string]models.Worker{}
		for _, w := range workers {
			byID[w.WorkerID] = w
		}
		require.Len(t, byID, 3)
		r := byID[running.WorkerID]
		require.NotNil(t, r.EnrollmentTokenName)
		assert.Equal(t, "2025", *r.EnrollmentTokenName)
		require.NotNil(t, r.EnrollmentTokenActive)
		assert.False(t, *r.EnrollmentTokenActive)
		assert.True(t, r.HasActiveSession)
		assert.True(t, r.OnDeactivatedEnrollmentToken())
		assert.False(t, func() bool { w := byID[stopped.WorkerID]; return w.OnDeactivatedEnrollmentToken() }())
		assert.False(t, func() bool { w := byID[current.WorkerID]; return w.OnDeactivatedEnrollmentToken() }())

		// Other reads still work with the read-only columns absent.
		plain, err := ps.GetWorkerByID(ctx, running.WorkerID)
		require.NoError(t, err)
		assert.Nil(t, plain.EnrollmentTokenName)

		// Deactivation is permanent. The trigger error aborts the statement,
		// so run it in a savepoint.
		require.NoError(t, tx.Exec("SAVEPOINT reactivate").Error)
		err = tx.Exec("UPDATE pool_enrollment_tokens SET is_active = true WHERE token_id = ?", oldTok.TokenID).Error
		require.Error(t, err)
		assert.Contains(t, err.Error(), "cannot be activated again")
		require.NoError(t, tx.Exec("ROLLBACK TO SAVEPOINT reactivate").Error)

		// Deleting a token keeps the worker and clears its token.
		require.NoError(t, tx.Exec("DELETE FROM pool_enrollment_tokens WHERE token_id = ?", newTok.TokenID).Error)
		after, err := ps.GetWorkerByID(ctx, current.WorkerID)
		require.NoError(t, err)
		assert.Nil(t, after.EnrollmentTokenID)
	})
}
