package corndogs

import (
	"context"

	pb "github.com/catalystcommunity/reactorcide/coordinator_api/internal/corndogs/v1alpha1"
)

// ClientInterface defines the interface for Corndogs operations
// This allows for easy mocking in tests
type ClientInterface interface {
	// SubmitTask submits a new task to Corndogs
	SubmitTask(ctx context.Context, payload *TaskPayload, priority int64) (*pb.Task, error)

	// GetNextTask gets the next available task from the queue
	GetNextTask(ctx context.Context, state string, timeout int64) (*pb.Task, error)

	// UpdateTask updates the state of a task
	UpdateTask(ctx context.Context, taskID string, currentState string, newState string, payload []byte) (*pb.Task, error)

	// CompleteTask marks a task as completed
	CompleteTask(ctx context.Context, taskID string, currentState string) (*pb.Task, error)

	// CancelTask cancels a task
	CancelTask(ctx context.Context, taskID string, currentState string) (*pb.Task, error)

	// GetTaskByID gets a task by its ID
	GetTaskByID(ctx context.Context, taskID string) (*pb.Task, error)

	// CleanUpTimedOut cleans up timed out tasks
	CleanUpTimedOut(ctx context.Context) (int64, error)

	// GetQueues gets all queues
	GetQueues(ctx context.Context) ([]string, int64, error)

	// GetQueueTaskCounts gets task counts per queue
	GetQueueTaskCounts(ctx context.Context) (map[string]int64, int64, error)

	// GetTaskStateCounts gets task counts per state for a queue
	GetTaskStateCounts(ctx context.Context) (int64, map[string]int64, error)

	// Close closes the connection
	Close() error
}

// Ensure Client implements ClientInterface
var _ ClientInterface = (*Client)(nil)
