package test

import (
	"context"
	"testing"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/jobcontrol"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/principals"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/postgres_store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// A retry records the re-runner as its actor. With no actor in the context,
// the retried job keeps the actor of the original job.
func TestRetryRecordsTheReRunner(t *testing.T) {
	RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
		du := &DataUtils{db: tx}
		owner, err := du.CreateUser(DataSetup{"Username": "retry-owner", "Email": "o@example.com"})
		require.NoError(t, err)
		rerunner, err := du.CreateUser(DataSetup{"Username": "retry-rerunner", "Email": "r@example.com"})
		require.NoError(t, err)
		original, err := du.CreateJob(DataSetup{"Status": "failed"})
		require.NoError(t, err)
		require.NoError(t, tx.Model(&models.Job{}).Where("job_id = ?", original.JobID).
			Updates(map[string]any{"actor_principal_id": owner.UserID, "actor_identity": "user:" + owner.UserID}).Error)
		original, err = postgres_store.PostgresStore.GetJobByID(ctx, original.JobID)
		require.NoError(t, err)

		actor := principals.Actor{PrincipalID: rerunner.UserID, Identity: "user:" + rerunner.UserID}
		retried, err := jobcontrol.RetryJob(principals.WithActor(ctx, actor), postgres_store.PostgresStore, nil, original)
		require.NoError(t, err)
		stored, err := postgres_store.PostgresStore.GetJobByID(ctx, retried.JobID)
		require.NoError(t, err)
		assert.Equal(t, rerunner.UserID, *stored.ActorPrincipalID)
		assert.Equal(t, "user:"+rerunner.UserID, stored.ActorIdentity)

		require.NoError(t, tx.Model(&models.Job{}).Where("job_id = ?", stored.JobID).Update("status", "failed").Error)
		stored.Status = "failed"
		again, err := jobcontrol.RetryJob(ctx, postgres_store.PostgresStore, nil, stored)
		require.NoError(t, err)
		inherited, err := postgres_store.PostgresStore.GetJobByID(ctx, again.JobID)
		require.NoError(t, err)
		assert.Equal(t, rerunner.UserID, *inherited.ActorPrincipalID, "the parent job's actor is the default")
	})
}
