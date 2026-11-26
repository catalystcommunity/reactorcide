package worker

import (
	"context"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

// guardedJobUpdater is the guarded row update of the Postgres store.
type guardedJobUpdater interface {
	UpdateJobStatusGuarded(ctx context.Context, jobID string, fromStatuses []string, apply func(*models.Job)) (*models.Job, bool, error)
}

type jobUpdater interface {
	UpdateJob(ctx context.Context, job *models.Job) error
}

// everyJobStatus is the status set of a guarded update that must apply in
// every status.
var everyJobStatus = []string{"submitted", "queued", "scheduling", "running", "cancelling",
	"completed", "failed", "cancelled", "timeout"}

// RecordQueueSubmission records the Corndogs task of a job that was just
// submitted to the queue.
//
// A worker can claim the task before this write. A save of the whole
// in-memory row then sets the job back to submitted and clears the claim of
// the worker: the worker's result is discarded as superseded, and the job
// (and its CI event) never finishes. So the write locks the current row,
// sets only the task ID, and changes the status only when the row still has
// the status it had before the submit. job gets the stored row.
func RecordQueueSubmission(ctx context.Context, st jobUpdater, job *models.Job, taskID, taskState string) error {
	before := job.Status
	guarded, ok := st.(guardedJobUpdater)
	if !ok {
		job.CorndogsTaskID = &taskID
		job.Status = taskState
		return st.UpdateJob(ctx, job)
	}
	updated, matched, err := guarded.UpdateJobStatusGuarded(ctx, job.JobID, everyJobStatus, func(j *models.Job) {
		j.CorndogsTaskID = &taskID
		if j.Status == before && taskState != "" {
			j.Status = taskState
		}
	})
	if err != nil {
		return err
	}
	if matched && updated != nil {
		*job = *updated
	}
	return nil
}
