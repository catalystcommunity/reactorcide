package uiapi

import (
	"context"
	"errors"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/characteristics"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/jobcontrol"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/uiapi/csilapi"
)

// queueBacklogCounts returns the corndogs per-queue task count map (queue
// UUID -> pending task count), or an empty map if no corndogs client is
// configured or the call fails -- backlog is a display convenience for
// list-queues, never worth failing the whole op over.
func (s *UiService) queueBacklogCounts(ctx context.Context) map[string]int64 {
	if s.deps.CorndogsClient == nil {
		return nil
	}
	counts, _, err := s.deps.CorndogsClient.GetQueueTaskCounts(ctx)
	if err != nil {
		return nil
	}
	return counts
}

func queueToSummary(q *models.Queue, backlog map[string]int64) csilapi.QueueSummary {
	return csilapi.QueueSummary{
		QueueId:         q.QueueID,
		QueueUuid:       q.QueueUUID,
		Characteristics: queueCharacteristicsToCsil(q.Characteristics),
		DisplayName:     q.DisplayName,
		IsDefault:       q.IsDefault,
		BacklogCount:    backlog[q.QueueUUID],
	}
}

// ListQueues requires global admin: queues have no creation path that sets
// an owning org today (see requireManageWorkers's doc comment), so this is
// always the global-admin branch.
func (s *UiService) ListQueues(ctx context.Context, req csilapi.ListQueuesRequest) (csilapi.ListQueuesResponse, error) {
	id, _, authErr := s.deps.requireUser(ctx)
	if authErr != nil {
		return csilapi.ListQueuesResponse{}, authErr
	}
	if err := s.requireManageWorkers(ctx, id, nil); err != nil {
		return csilapi.ListQueuesResponse{}, err
	}

	queues, err := s.deps.Store.ListQueues(ctx, 0, 0)
	if err != nil {
		return csilapi.ListQueuesResponse{}, NewServiceError("internal", "an internal error occurred")
	}
	backlog := s.queueBacklogCounts(ctx)
	out := make([]csilapi.QueueSummary, len(queues))
	for i := range queues {
		out[i] = queueToSummary(&queues[i], backlog)
	}
	return csilapi.ListQueuesResponse{Queues: out}, nil
}

// CreateQueue lets an operator pre-create a queue (characteristics +
// display name up front). Characteristics are immutable once created --
// there is no update-characteristics op, only RenameQueue for the display
// name. Requires global admin, same as every queue op.
func (s *UiService) CreateQueue(ctx context.Context, req csilapi.CreateQueueRequest) (csilapi.CreateQueueResponse, error) {
	id, _, authErr := s.deps.requireUser(ctx)
	if authErr != nil {
		return csilapi.CreateQueueResponse{}, authErr
	}
	if err := s.requireManageWorkers(ctx, id, nil); err != nil {
		return csilapi.CreateQueueResponse{}, err
	}

	chars, err := characteristics.ParseJobCharacteristics(csilCharacteristicsToRaw(req.Characteristics))
	if err != nil {
		return csilapi.CreateQueueResponse{}, NewServiceError("invalid_argument", err.Error())
	}

	queue, err := s.deps.Store.CreateQueue(ctx, chars, derefOr(req.DisplayName, ""))
	if err != nil {
		if errors.Is(err, store.ErrAlreadyExists) {
			return csilapi.CreateQueueResponse{}, NewServiceError("conflict", "a queue with these characteristics already exists")
		}
		return csilapi.CreateQueueResponse{}, NewServiceError("internal", "failed to create queue")
	}
	s.recordAudit(ctx, queue.OrgID, "queue.create", "queue", queue.QueueID, models.JSONB{"worker_class": queue.WorkerClass, "queue_uuid": queue.QueueUUID})
	return csilapi.CreateQueueResponse{Queue: queueToSummary(queue, s.queueBacklogCounts(ctx))}, nil
}

// RenameQueue updates a queue's mutable display name only. Requires global
// admin, same as every queue op.
func (s *UiService) RenameQueue(ctx context.Context, req csilapi.RenameQueueRequest) (csilapi.RenameQueueResponse, error) {
	id, _, authErr := s.deps.requireUser(ctx)
	if authErr != nil {
		return csilapi.RenameQueueResponse{}, authErr
	}
	if err := requireNonEmpty("queue_id", req.QueueId, 64); err != nil {
		return csilapi.RenameQueueResponse{}, err
	}
	if err := s.requireManageWorkers(ctx, id, nil); err != nil {
		return csilapi.RenameQueueResponse{}, err
	}

	queue, err := s.deps.Store.GetQueueByID(ctx, req.QueueId)
	if err != nil {
		return csilapi.RenameQueueResponse{}, mapStoreErr(err, "queue not found")
	}
	if err := s.deps.Store.UpdateQueueDisplayName(ctx, req.QueueId, req.DisplayName); err != nil {
		return csilapi.RenameQueueResponse{}, mapStoreErr(err, "queue not found")
	}
	queue.DisplayName = req.DisplayName
	s.recordAudit(ctx, queue.OrgID, "queue.update", "queue", queue.QueueID, models.JSONB{"display_name": queue.DisplayName})
	return csilapi.RenameQueueResponse{Queue: queueToSummary(queue, s.queueBacklogCounts(ctx))}, nil
}

// DeleteQueue cancels every non-terminal job routed to the queue (via the
// existing jobcontrol.CancelJob path — the same graceful-cancel flow the UI
// cancel-job op drives) before removing the row. The seeded default queue
// (is_default) may never be deleted — refused with invalid_argument, since
// untagged/os-less jobs would otherwise have nowhere to resolve to. Requires
// global admin, same as every queue op.
func (s *UiService) DeleteQueue(ctx context.Context, req csilapi.DeleteQueueRequest) (csilapi.DeleteQueueResponse, error) {
	id, _, authErr := s.deps.requireUser(ctx)
	if authErr != nil {
		return csilapi.DeleteQueueResponse{}, authErr
	}
	if err := requireNonEmpty("queue_id", req.QueueId, 64); err != nil {
		return csilapi.DeleteQueueResponse{}, err
	}
	if err := s.requireManageWorkers(ctx, id, nil); err != nil {
		return csilapi.DeleteQueueResponse{}, err
	}

	queue, err := s.deps.Store.GetQueueByID(ctx, req.QueueId)
	if err != nil {
		return csilapi.DeleteQueueResponse{}, mapStoreErr(err, "queue not found")
	}
	if queue.IsDefault {
		return csilapi.DeleteQueueResponse{}, NewServiceError("invalid_argument", "the default queue cannot be deleted")
	}

	jobs, err := s.deps.Store.ListNonTerminalJobsByQueue(ctx, queue.QueueUUID)
	if err != nil {
		return csilapi.DeleteQueueResponse{}, NewServiceError("internal", "failed to list jobs routed to this queue")
	}
	cancelledIDs := make([]string, 0, len(jobs))
	for i := range jobs {
		job := jobs[i]
		if _, cancelErr := jobcontrol.CancelJob(ctx, s.deps.Store, s.deps.CorndogsClient, &job); cancelErr != nil {
			// A job that flipped to terminal between the list and this call
			// (ErrNotCancellable) is not a failure of the delete -- it's
			// already out of the way. Any other error aborts the delete
			// rather than removing a queue jobs are still routed to.
			if !errors.Is(cancelErr, jobcontrol.ErrNotCancellable) {
				return csilapi.DeleteQueueResponse{}, NewServiceError("internal", "failed to cancel an in-flight job routed to this queue")
			}
			continue
		}
		cancelledIDs = append(cancelledIDs, job.JobID)
	}

	if err := s.deps.Store.DeleteQueue(ctx, req.QueueId); err != nil {
		return csilapi.DeleteQueueResponse{}, mapStoreErr(err, "queue not found")
	}
	s.recordAudit(ctx, queue.OrgID, "queue.delete", "queue", queue.QueueID, models.JSONB{"queue_uuid": queue.QueueUUID, "cancelled_jobs": len(cancelledIDs)})
	return csilapi.DeleteQueueResponse{Deleted: true, CancelledJobIds: cancelledIDs}, nil
}
