package test

// Postgres-backed coverage for migration 000059 (workers.admission and
// jobs_waiting_queue_idx) and postgres_store/admission_operations.go.

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/backlog"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/characteristics"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/postgres_store"
)

func TestWaitingQueueHeadsAndGroups(t *testing.T) {
	RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
		du := &DataUtils{db: tx}
		queueA, queueB := uuid.NewString(), uuid.NewString()
		now := time.Now().UTC()
		create := func(queue string, priority int, age time.Duration, cpu, memory, status string) *models.Job {
			job, err := du.CreateJob(DataSetup{
				"QueueName": queue, "Priority": priority, "Status": status,
				"ResourceCPURequest": cpu, "ResourceMemoryLimit": memory,
			})
			require.NoError(t, err)
			require.NoError(t, tx.Exec("UPDATE jobs SET created_at = ? WHERE job_id = ?", now.Add(-age), job.JobID).Error)
			return job
		}
		oldLow := create(queueA, 0, 30*time.Minute, "1", "4Gi", "submitted")
		newHigh := create(queueA, 10, time.Minute, "2", "8Gi", "queued")
		create(queueA, 20, time.Hour, "8", "8Gi", "running") // not waiting
		onlyB := create(queueB, 0, 5*time.Minute, "500m", "1Gi", "submitted")
		create(queueB, 0, 2*time.Minute, "500m", "1Gi", "submitted")

		heads, err := postgres_store.PostgresStore.ListWaitingQueueHeads(ctx, []string{queueA, queueB})
		require.NoError(t, err)
		byQueue := map[string]models.WaitingJob{}
		for _, head := range heads {
			byQueue[head.QueueName] = head
		}
		require.Len(t, byQueue, 2)
		require.Equal(t, newHigh.JobID, byQueue[queueA].JobID, "priority runs before age")
		require.Equal(t, "2", byQueue[queueA].ResourceCPURequest)
		require.Equal(t, onlyB.JobID, byQueue[queueB].JobID, "oldest first in one priority")
		_ = oldLow

		empty, err := postgres_store.PostgresStore.ListWaitingQueueHeads(ctx, nil)
		require.NoError(t, err)
		require.Empty(t, empty)

		groups, err := postgres_store.PostgresStore.ListWaitingJobGroups(ctx)
		require.NoError(t, err)
		var countA, countB int64
		for _, group := range groups {
			switch group.QueueName {
			case queueA:
				countA += group.Count
			case queueB:
				countB += group.Count
				require.Equal(t, "500m", group.ResourceCPURequest)
				require.WithinDuration(t, now.Add(-5*time.Minute), group.OldestCreatedAt, time.Second)
			}
		}
		require.Equal(t, int64(2), countA)
		require.Equal(t, int64(2), countB)

		// The backlog source works against the real store.
		source := backlog.SourceFrom(postgres_store.PostgresStore)
		require.NotNil(t, source)
		snapshot, err := backlog.Load(ctx, source, nil, now)
		require.NoError(t, err)
		var found bool
		for _, q := range snapshot.Queues {
			if q.QueueUUID == queueA {
				found = true
				require.Equal(t, int64(2), q.Count)
				require.Equal(t, int64(3000), q.CPURequestMillicores)
				require.Equal(t, int64(12<<30), q.MemoryLimitBytes)
			}
		}
		require.True(t, found)
	})
}

func TestUpdateWorkerAdmission(t *testing.T) {
	RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
		pool := &models.WorkerPool{Name: uniqueName("pool")}
		require.NoError(t, postgres_store.PostgresStore.CreateWorkerPool(ctx, pool))
		chars, err := characteristics.ParseWorkerCharacteristics("linux", "amd64", []characteristics.KV{{Key: "region", Value: "us-east-1"}})
		require.NoError(t, err)
		worker, err := postgres_store.PostgresStore.UpsertWorkerByKey(ctx, &models.Worker{
			PoolID: pool.PoolID, WorkerKey: uuid.NewString(), OS: "linux", Arch: "amd64", Characteristics: chars,
		})
		require.NoError(t, err)
		require.Nil(t, worker.Admission, "a new worker has no admission report")

		reported := time.Now().UTC().Truncate(time.Second)
		require.NoError(t, postgres_store.PostgresStore.UpdateWorkerAdmission(ctx, worker.WorkerID, &models.WorkerAdmission{
			Mode: "resources", CapacityCPUMillicores: 8000, CapacityMemoryBytes: 32 << 30,
			HeadroomCPUMillicores: 2000, HeadroomMemoryBytes: 4 << 30, ReportedAt: reported,
		}))
		got, err := postgres_store.PostgresStore.GetWorkerByID(ctx, worker.WorkerID)
		require.NoError(t, err)
		require.NotNil(t, got.Admission)
		require.Equal(t, int64(2000), got.Admission.HeadroomCPUMillicores)
		require.True(t, got.Admission.ReportedAt.Equal(reported))

		// Register (an upsert) keeps the report.
		again, err := postgres_store.PostgresStore.UpsertWorkerByKey(ctx, &models.Worker{
			PoolID: pool.PoolID, WorkerKey: worker.WorkerKey, OS: "linux", Arch: "amd64", Characteristics: chars,
		})
		require.NoError(t, err)
		require.NotNil(t, again.Admission)

		require.NoError(t, postgres_store.PostgresStore.UpdateWorkerAdmission(ctx, worker.WorkerID, nil))
		got, err = postgres_store.PostgresStore.GetWorkerByID(ctx, worker.WorkerID)
		require.NoError(t, err)
		require.Nil(t, got.Admission)

		require.ErrorIs(t, postgres_store.PostgresStore.UpdateWorkerAdmission(ctx, uuid.NewString(), nil), store.ErrNotFound)
	})
}

// TestWorkerCapacityMigration runs migration 000059 over an existing worker
// and checks that the row keeps its meaning (no admission report) and that
// the Down migration removes the column and the index.
func TestWorkerCapacityMigration(t *testing.T) {
	db := newScratchDBAt(t, "worker_capacity_migrate", 58)
	var poolID string
	require.NoError(t, db.QueryRow(`INSERT INTO worker_pools (name) VALUES ('wc-pool') RETURNING pool_id`).Scan(&poolID))
	_, err := db.Exec(`INSERT INTO workers (pool_id, worker_key, os, arch, characteristics) VALUES ($1, 'wc-key', 'linux', 'amd64', '{}')`, poolID)
	require.NoError(t, err)

	require.NoError(t, goose.UpTo(db, "migrations", 59))
	var admissionNull bool
	require.NoError(t, db.QueryRow(`SELECT admission IS NULL FROM workers WHERE worker_key = 'wc-key'`).Scan(&admissionNull))
	require.True(t, admissionNull, "an existing worker has no admission report")
	var indexes int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM pg_indexes WHERE indexname = 'jobs_waiting_queue_idx'`).Scan(&indexes))
	require.Equal(t, 1, indexes)

	require.NoError(t, goose.DownTo(db, "migrations", 58))
	var columns int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM information_schema.columns WHERE table_name = 'workers' AND column_name = 'admission'`).Scan(&columns))
	require.Zero(t, columns)
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM pg_indexes WHERE indexname = 'jobs_waiting_queue_idx'`).Scan(&indexes))
	require.Zero(t, indexes)
}
