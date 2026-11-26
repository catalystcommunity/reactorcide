package test

// A worker can claim a job between the queue submit and the write of the
// task ID. The write must keep the claim (it used to save the whole stale
// row: the job went back to submitted, the worker's result was discarded,
// and the CI event stayed evaluating).

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/postgres_store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/worker"
)

func TestRecordQueueSubmissionKeepsWorkerClaim(t *testing.T) {
	RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
		du := &DataUtils{db: tx}
		owner, err := du.CreateUser(DataSetup{})
		require.NoError(t, err)
		job := &models.Job{UserID: owner.UserID, Name: "eval", JobCommand: "true", Status: "submitted"}
		require.NoError(t, store.AppStore.CreateJob(ctx, job))
		stale := *job // the submitter's in-memory copy

		// The worker claims the task first.
		workerID := "worker-1"
		_, matched, err := postgres_store.PostgresStore.UpdateJobStatusGuarded(ctx, job.JobID, []string{"submitted"}, func(j *models.Job) {
			j.Status = "running"
			j.WorkerID = &workerID
		})
		require.NoError(t, err)
		require.True(t, matched)

		taskID := uuid.New().String()
		require.NoError(t, worker.RecordQueueSubmission(ctx, store.AppStore, &stale, taskID, "submitted"))

		loaded, err := store.AppStore.GetJobByID(ctx, job.JobID)
		require.NoError(t, err)
		require.Equal(t, "running", loaded.Status, "the claim of the worker is kept")
		require.NotNil(t, loaded.WorkerID)
		require.Equal(t, workerID, *loaded.WorkerID)
		require.NotNil(t, loaded.CorndogsTaskID)
		require.Equal(t, taskID, *loaded.CorndogsTaskID)
		require.Equal(t, "running", stale.Status, "the caller gets the stored row")
	})
}

func TestRecordQueueSubmissionSetsQueueState(t *testing.T) {
	RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
		du := &DataUtils{db: tx}
		owner, err := du.CreateUser(DataSetup{})
		require.NoError(t, err)
		job := &models.Job{UserID: owner.UserID, Name: "build", JobCommand: "true", Status: "submitted"}
		require.NoError(t, store.AppStore.CreateJob(ctx, job))

		taskID := uuid.New().String()
		require.NoError(t, worker.RecordQueueSubmission(ctx, store.AppStore, job, taskID, "queued"))

		loaded, err := store.AppStore.GetJobByID(ctx, job.JobID)
		require.NoError(t, err)
		require.Equal(t, "queued", loaded.Status)
		require.Equal(t, taskID, *loaded.CorndogsTaskID)
	})
}
