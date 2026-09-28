package test

// Postgres-backed coverage for the lost-job reconciler's queries
// (postgres_store/job_guarded_operations.go) and for the claim transaction
// that RequestJob commits through InTransaction.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/characteristics"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/postgres_store"
)

func createLeaseWorker(t *testing.T, ctx context.Context) *models.Worker {
	t.Helper()
	pool := &models.WorkerPool{Name: uniqueName("pool")}
	require.NoError(t, postgres_store.PostgresStore.CreateWorkerPool(ctx, pool))
	chars, err := characteristics.ParseWorkerCharacteristics("linux", "amd64", nil)
	require.NoError(t, err)
	w, err := postgres_store.PostgresStore.UpsertWorkerByKey(ctx, &models.Worker{
		PoolID: pool.PoolID, WorkerKey: uniqueName("worker-key"), OS: "linux", Arch: "amd64", Characteristics: chars,
	})
	require.NoError(t, err)
	return w
}

func setJobState(t *testing.T, tx *gorm.DB, jobID string, fields map[string]any) {
	t.Helper()
	require.NoError(t, tx.Model(&models.Job{}).Where("job_id = ?", jobID).Updates(fields).Error)
}

func jobIDs(jobs []models.Job) map[string]bool {
	out := map[string]bool{}
	for _, j := range jobs {
		out[j.JobID] = true
	}
	return out
}

func TestListLostRunningJobs(t *testing.T) {
	RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
		du := &DataUtils{db: tx}
		w := createLeaseWorker(t, ctx)
		longAgo := time.Now().UTC().Add(-3 * time.Hour)
		cutoff := time.Now().UTC().Add(-2 * time.Minute)
		queue := uuid.NewString()

		newRunning := func(started time.Time) *models.Job {
			job, err := du.CreateJob(DataSetup{})
			require.NoError(t, err)
			setJobState(t, tx, job.JobID, map[string]any{"status": "running", "started_at": started})
			return job
		}

		// Lost: running, no lease at all (the incident's claim failure).
		noLease := newRunning(longAgo)
		// Lost: its only lease was released long ago (reaped:stale).
		reaped := newRunning(longAgo)
		reapedLease, err := postgres_store.PostgresStore.CreateWorkerLease(ctx, w.WorkerID, reaped.JobID, &queue)
		require.NoError(t, err)
		require.NoError(t, tx.Model(&models.WorkerLease{}).Where("lease_id = ?", reapedLease.LeaseID).
			Updates(map[string]any{"released_at": longAgo, "outcome": "reaped:stale"}).Error)
		// Held: an open lease, however old the job.
		held := newRunning(longAgo)
		_, err = postgres_store.PostgresStore.CreateWorkerLease(ctx, w.WorkerID, held.JobID, &queue)
		require.NoError(t, err)
		// Grace: lease released only just now.
		justReleased := newRunning(longAgo)
		jrLease, err := postgres_store.PostgresStore.CreateWorkerLease(ctx, w.WorkerID, justReleased.JobID, &queue)
		require.NoError(t, err)
		require.NoError(t, postgres_store.PostgresStore.ReleaseWorkerLease(ctx, jrLease.LeaseID, "reaped:stale"))
		// Grace: started just now.
		recent := newRunning(time.Now().UTC())
		// Not running.
		done, err := du.CreateJob(DataSetup{})
		require.NoError(t, err)
		setJobState(t, tx, done.JobID, map[string]any{"status": "completed", "started_at": longAgo})

		lost, err := postgres_store.PostgresStore.ListLostRunningJobs(ctx, cutoff)
		require.NoError(t, err)
		got := jobIDs(lost)
		assert.True(t, got[noLease.JobID], "running job with no lease is lost")
		assert.True(t, got[reaped.JobID], "running job whose lease was reaped long ago is lost")
		assert.False(t, got[held.JobID], "job with an open lease is not lost")
		assert.False(t, got[justReleased.JobID], "job whose lease was just released is inside the grace period")
		assert.False(t, got[recent.JobID], "job that just started is inside the grace period")
		assert.False(t, got[done.JobID], "completed job is not lost")
	})
}

func TestListFinalJobsWithOpenWorkflowNode(t *testing.T) {
	RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
		du := &DataUtils{db: tx}
		user, err := du.CreateUser(DataSetup{})
		require.NoError(t, err)
		wf := &models.WorkflowInstance{UserID: user.UserID, Name: "pr", Status: "running", QueueName: "reactorcide-jobs"}
		require.NoError(t, postgres_store.PostgresStore.CreateWorkflowInstance(ctx, wf))
		longAgo := time.Now().UTC().Add(-time.Hour)

		newNodeJob := func(jobStatus, nodeStatus string) *models.Job {
			job, err := du.CreateJob(DataSetup{"UserID": user.UserID})
			require.NoError(t, err)
			setJobState(t, tx, job.JobID, map[string]any{"status": jobStatus, "completed_at": longAgo, "workflow_id": wf.WorkflowID})
			node := &models.WorkflowNode{WorkflowID: wf.WorkflowID, Name: uniqueName("node"), DisplayName: "node", Status: nodeStatus, JobID: &job.JobID}
			require.NoError(t, postgres_store.PostgresStore.CreateWorkflowNode(ctx, node))
			return job
		}

		stuck := newNodeJob("completed", "running")
		stuckSubmitted := newNodeJob("failed", "submitted")
		finished := newNodeJob("completed", "success")
		stillRunning := newNodeJob("running", "running")

		final, err := postgres_store.PostgresStore.ListFinalJobsWithOpenWorkflowNode(ctx, time.Now().UTC().Add(-2*time.Minute))
		require.NoError(t, err)
		got := jobIDs(final)
		assert.True(t, got[stuck.JobID])
		assert.True(t, got[stuckSubmitted.JobID])
		assert.False(t, got[finished.JobID])
		assert.False(t, got[stillRunning.JobID])
	})
}

// TestClaimTransactionRollsBack: RequestJob commits the running transition,
// the job token, and the lease through InTransaction. A failure inside it
// must leave the job at submitted with no token and no lease.
func TestClaimTransactionRollsBack(t *testing.T) {
	RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
		du := &DataUtils{db: tx}
		w := createLeaseWorker(t, ctx)
		job, err := du.CreateJob(DataSetup{})
		require.NoError(t, err)
		setJobState(t, tx, job.JobID, map[string]any{"status": "submitted"})
		queue := uuid.NewString()
		failure := errors.New("store unavailable")

		err = postgres_store.PostgresStore.InTransaction(ctx, func(txCtx context.Context) error {
			running, matched, err := postgres_store.PostgresStore.UpdateJobStatusGuarded(txCtx, job.JobID, []string{"submitted"}, func(j *models.Job) {
				j.Status = "running"
				j.WorkerID = &w.WorkerID
			})
			require.NoError(t, err)
			require.True(t, matched)
			_, err = postgres_store.PostgresStore.MintJobToken(txCtx, running)
			require.NoError(t, err)
			_, err = postgres_store.PostgresStore.CreateWorkerLease(txCtx, w.WorkerID, job.JobID, &queue)
			require.NoError(t, err)
			return failure
		})
		require.ErrorIs(t, err, failure)

		saved, err := postgres_store.PostgresStore.GetJobByID(ctx, job.JobID)
		require.NoError(t, err)
		assert.Equal(t, "submitted", saved.Status)
		assert.Nil(t, saved.WorkerID)
		var leases, tokens int64
		require.NoError(t, tx.Model(&models.WorkerLease{}).Where("job_id = ?", job.JobID).Count(&leases).Error)
		require.NoError(t, tx.Model(&models.APIToken{}).Where("bound_job_id = ?", job.JobID).Count(&tokens).Error)
		assert.Zero(t, leases, "rolled-back claim must leave no lease")
		assert.Zero(t, tokens, "rolled-back claim must leave no job token")
	})
}
