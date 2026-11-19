package test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/postgres_store"
)

func TestListJobsFailureClassFilter(t *testing.T) {
	RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
		ps := postgres_store.PostgresStore
		user := createTestUserIn(t, tx)
		oom := createTestJobIn(t, tx, user.UserID, "failed")
		other := createTestJobIn(t, tx, user.UserID, "failed")
		require.NoError(t, tx.Model(&models.Job{}).Where("job_id = ?", oom.JobID).Update("failure_class", models.FailureClassOOM).Error)
		require.NoError(t, tx.Model(&models.Job{}).Where("job_id = ?", other.JobID).Update("failure_class", models.FailureClassJob).Error)

		filters := map[string]interface{}{"user_id": user.UserID, "failure_class": models.FailureClassOOM}
		jobs, err := ps.ListJobs(ctx, filters, 10, 0)
		require.NoError(t, err)
		require.Len(t, jobs, 1)
		assert.Equal(t, oom.JobID, jobs[0].JobID)

		visible, total, err := ps.ListJobsVisibleTo(ctx, user.UserID, true, filters, 10, 0)
		require.NoError(t, err)
		assert.Equal(t, int64(1), total)
		require.Len(t, visible, 1)
		assert.Equal(t, oom.JobID, visible[0].JobID)
	})
}
