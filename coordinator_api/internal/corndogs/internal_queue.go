package corndogs

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"

	csil "github.com/CatalystCommunity/corndogs/clients/corndogs"
	"github.com/catalystcommunity/app-utils-go/logging"
)

// Internal queues carry coordinator background tasks (for example outbound
// event deliveries). Workers never claim from them: a worker queue is a UUID
// (queues.queue_uuid), and RequestJob drops any name with this prefix.
const (
	InternalQueuePrefix = "reactorcide.internal."
	// OutboundEventsQueue is the queue of outbound event deliveries
	// (docs/outbound-events.md).
	OutboundEventsQueue = InternalQueuePrefix + "outbound-events"
)

// IsInternalQueue reports whether name is a coordinator-only queue.
func IsInternalQueue(name string) bool {
	return strings.HasPrefix(name, InternalQueuePrefix)
}

// ClaimedTask is a task that a coordinator loop claimed from an internal
// queue.
type ClaimedTask struct {
	ID       string
	Queue    string
	State    string
	Revision int64
	Guarded  bool
	Payload  []byte
}

// Modes of an InternalQueue. A Corndogs server before 0.8.0 does not have
// keyed submissions and guarded tasks; the queue then uses the legacy
// operations. The coordinator keeps its own durable task key in its
// database, so a duplicate legacy task is detected and completed with no
// effect.
const (
	modeUnknown int32 = iota
	modeGuarded
	modeLegacy
)

// InternalQueue submits and claims coordinator background tasks with keyed
// submissions and guarded claims (Corndogs 0.8.0 resilience contract).
type InternalQueue struct {
	client *csil.CorndogsClient
	mode   atomic.Int32
}

// InternalQueue returns the internal-queue operations of the client.
func (c *Client) InternalQueue() *InternalQueue {
	return &InternalQueue{client: c.client}
}

func operationID(prefix string) string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return prefix + "-" + hex.EncodeToString(b)
}

func (q *InternalQueue) legacy(err error) bool {
	if errors.Is(err, csil.ErrUnsupported) {
		if q.mode.Swap(modeLegacy) != modeLegacy {
			logging.Log.Warn("Corndogs server has no keyed submissions or guarded tasks (before 0.8.0); internal queues use legacy operations. Upgrade Corndogs.")
		}
		return true
	}
	return false
}

// SubmitKeyedTask submits payload with a submission key. A repeat of the same
// key and payload within the receipt retention returns the first task.
func (q *InternalQueue) SubmitKeyedTask(ctx context.Context, queue, key string, payload []byte) (string, error) {
	if !IsInternalQueue(queue) {
		return "", fmt.Errorf("queue %q is not an internal queue", queue)
	}
	if q.mode.Load() != modeLegacy {
		resp, err := q.client.SubmitKeyedTask(ctx, csil.SubmitKeyedTaskRequest{
			SubmissionKey: key, Guarded: true, Queue: queue,
			CurrentState: "submitted", AutoTargetState: "submitted-working",
			// No Corndogs timeout: the coordinator keeps the delivery lease in
			// its own database and resets stale deliveries itself.
			Timeout: -1, Payload: payload,
		})
		if err == nil {
			q.mode.Store(modeGuarded)
			return resp.Receipt.TaskUuid, nil
		}
		if !q.legacy(err) {
			return "", fmt.Errorf("failed to submit keyed task: %w", err)
		}
	}
	resp, err := q.client.SubmitTask(ctx, csil.SubmitTaskRequest{
		Queue: queue, CurrentState: "submitted", AutoTargetState: "submitted-working",
		Timeout: -1, Payload: payload,
	})
	if err != nil {
		return "", fmt.Errorf("failed to submit task: %w", err)
	}
	return resp.Task.Uuid, nil
}

// Claim claims the next task of queue. It returns nil when the queue is
// empty.
func (q *InternalQueue) Claim(ctx context.Context, queue string) (*ClaimedTask, error) {
	if !IsInternalQueue(queue) {
		return nil, fmt.Errorf("queue %q is not an internal queue", queue)
	}
	if q.mode.Load() != modeLegacy {
		resp, err := q.client.ClaimGuardedTask(ctx, csil.ClaimGuardedTaskRequest{
			OperationId: operationID("claim"), Queue: queue, CurrentState: "submitted",
		})
		if err == nil {
			q.mode.Store(modeGuarded)
			if resp.Delivery == nil {
				return nil, nil
			}
			task := resp.Delivery.Task
			return &ClaimedTask{ID: task.Task.Uuid, Queue: task.Task.Queue, State: task.Task.CurrentState,
				Revision: task.Revision, Guarded: true, Payload: resp.Delivery.Payload}, nil
		}
		if !q.legacy(err) {
			return nil, fmt.Errorf("failed to claim task: %w", err)
		}
	}
	resp, err := q.client.GetNextTaskGroup(ctx, csil.GetNextTaskGroupRequest{Queues: []string{queue}, CurrentState: "submitted"})
	if err != nil {
		return nil, fmt.Errorf("failed to claim task: %w", err)
	}
	if resp.Delivery == nil {
		return nil, nil
	}
	return &ClaimedTask{ID: resp.Delivery.Task.Uuid, Queue: resp.Delivery.Task.Queue,
		State: resp.Delivery.Task.CurrentState, Payload: resp.Delivery.Payload}, nil
}

// claimEnded reports a guarded error that means the task is no longer ours
// or is already final: a stale revision, a missing task, or a superseded
// claim.
func claimEnded(err error) bool {
	code, ok := csil.ServiceErrorCode(err)
	return ok && (code == csil.CodeRevisionConflict || code == csil.CodeTaskNotFound || code == csil.CodeClaimSuperseded)
}

// Complete completes a claimed task.
func (q *InternalQueue) Complete(ctx context.Context, task *ClaimedTask) error {
	if task == nil {
		return nil
	}
	if task.Guarded {
		state := task.State
		_, err := q.client.CompleteGuardedTask(ctx, csil.CompleteGuardedTaskRequest{
			OperationId: operationID("complete"), Uuid: task.ID, Queue: task.Queue,
			ExpectedRevision: task.Revision, ExpectedState: &state,
		})
		if err != nil && !claimEnded(err) {
			return fmt.Errorf("failed to complete task: %w", err)
		}
		return nil
	}
	if _, err := q.client.CompleteTask(ctx, csil.CompleteTaskRequest{Uuid: task.ID, Queue: task.Queue, CurrentState: task.State}); err != nil {
		return fmt.Errorf("failed to complete task: %w", err)
	}
	return nil
}

// Discard cancels a task that is no longer needed. A task that is already
// final, or missing, is not an error.
func (q *InternalQueue) Discard(ctx context.Context, queue, taskID string) error {
	if taskID == "" {
		return nil
	}
	if q.mode.Load() != modeLegacy {
		resp, err := q.client.GetGuardedTask(ctx, csil.GetGuardedTaskRequest{Uuid: taskID, Queue: queue})
		if err == nil {
			if resp.Task == nil || resp.Task.Terminal {
				return nil
			}
			state := resp.Task.Task.CurrentState
			_, err = q.client.CancelGuardedTask(ctx, csil.CancelGuardedTaskRequest{
				OperationId: operationID("discard"), Uuid: taskID, Queue: queue,
				ExpectedRevision: resp.Task.Revision, ExpectedState: &state,
			})
			if err != nil && !claimEnded(err) {
				return fmt.Errorf("failed to discard task: %w", err)
			}
			return nil
		}
		if claimEnded(err) {
			return nil
		}
		if !q.legacy(err) {
			return fmt.Errorf("failed to read task: %w", err)
		}
	}
	resp, err := q.client.GetTaskStateByID(ctx, csil.GetTaskStateByIDRequest{Uuid: taskID, Queue: queue})
	if err != nil || resp.Task == nil {
		return nil
	}
	if _, err := q.client.CancelTask(ctx, csil.CancelTaskRequest{Uuid: taskID, Queue: queue, CurrentState: resp.Task.CurrentState}); err != nil {
		return fmt.Errorf("failed to discard task: %w", err)
	}
	return nil
}
