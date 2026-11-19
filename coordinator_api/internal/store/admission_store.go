package store

import (
	"context"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

// AdmissionStore holds the reads and writes of resource-aware admission and
// the backlog signal (docs/workers.md).
//
// It is separate from Store, so the many full Store fakes do not each need
// these methods. The Postgres store implements it; a caller type-asserts and
// treats a store without it as "no admission data".
type AdmissionStore interface {
	// ListWaitingQueueHeads returns the head waiting job of each listed queue
	// that has one, in the Corndogs claim order.
	ListWaitingQueueHeads(ctx context.Context, queueUUIDs []string) ([]models.WaitingJob, error)
	// ListWaitingJobGroups returns every waiting job grouped by queue and
	// resource shape.
	ListWaitingJobGroups(ctx context.Context) ([]models.WaitingJobGroup, error)
	// UpdateWorkerAdmission stores the newest admission report of a worker.
	UpdateWorkerAdmission(ctx context.Context, workerID string, report *models.WorkerAdmission) error
}
