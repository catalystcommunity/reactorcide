package corndogs

import (
	"context"
	"fmt"
	"sync"
	"time"

	pb "github.com/catalystcommunity/reactorcide/coordinator_api/internal/corndogs/v1alpha1"
	"github.com/google/uuid"
)

// MockClient is a mock implementation of ClientInterface for testing
type MockClient struct {
	mu sync.Mutex

	// Control behavior
	SubmitTaskFunc      func(ctx context.Context, payload *TaskPayload, priority int64) (*pb.Task, error)
	GetNextTaskFunc     func(ctx context.Context, state string, timeout int64) (*pb.Task, error)
	UpdateTaskFunc      func(ctx context.Context, taskID string, currentState string, newState string, payload []byte) (*pb.Task, error)
	SendHeartbeatFunc   func(ctx context.Context, taskID string, currentState string, timeoutExtensionSeconds int64) (*pb.Task, error)
	CompleteTaskFunc    func(ctx context.Context, taskID string, currentState string) (*pb.Task, error)
	CancelTaskFunc      func(ctx context.Context, taskID string, currentState string) (*pb.Task, error)
	GetTaskByIDFunc     func(ctx context.Context, taskID string) (*pb.Task, error)
	CleanUpTimedOutFunc func(ctx context.Context) (int64, error)
	GetQueuesFunc       func(ctx context.Context) ([]string, int64, error)

	// Track calls for assertions
	SubmitTaskCalls      []SubmitTaskCall
	GetNextTaskCalls     []GetNextTaskCall
	UpdateTaskCalls      []UpdateTaskCall
	SendHeartbeatCalls   []SendHeartbeatCall
	CompleteTaskCalls    []CompleteTaskCall
	CancelTaskCalls      []CancelTaskCall
	GetTaskByIDCalls     []GetTaskByIDCall
	CleanUpTimedOutCalls []CleanUpTimedOutCall
}

// Call tracking structures
type SubmitTaskCall struct {
	Payload  *TaskPayload
	Priority int64
}

type GetNextTaskCall struct {
	State   string
	Timeout int64
}

type UpdateTaskCall struct {
	TaskID       string
	CurrentState string
	NewState     string
	Payload      []byte
}

type SendHeartbeatCall struct {
	TaskID                   string
	CurrentState             string
	TimeoutExtensionSeconds  int64
}

type CompleteTaskCall struct {
	TaskID       string
	CurrentState string
}

type CancelTaskCall struct {
	TaskID       string
	CurrentState string
}

type GetTaskByIDCall struct {
	TaskID string
}

type CleanUpTimedOutCall struct {
	Time time.Time
}

// NewMockClient creates a new mock client with default behaviors
func NewMockClient() *MockClient {
	return &MockClient{}
}

// SubmitTask mock implementation
func (m *MockClient) SubmitTask(ctx context.Context, payload *TaskPayload, priority int64) (*pb.Task, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.SubmitTaskCalls = append(m.SubmitTaskCalls, SubmitTaskCall{
		Payload:  payload,
		Priority: priority,
	})

	if m.SubmitTaskFunc != nil {
		return m.SubmitTaskFunc(ctx, payload, priority)
	}

	// Default behavior
	return &pb.Task{
		Uuid:            uuid.New().String(),
		Queue:           "reactorcide-jobs",
		CurrentState:    "submitted",
		AutoTargetState: "submitted-working",
		SubmitTime:      time.Now().Unix(),
		UpdateTime:      time.Now().Unix(),
		Timeout:         3600,
		Priority:        priority,
	}, nil
}

// GetNextTask mock implementation
func (m *MockClient) GetNextTask(ctx context.Context, state string, timeout int64) (*pb.Task, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.GetNextTaskCalls = append(m.GetNextTaskCalls, GetNextTaskCall{
		State:   state,
		Timeout: timeout,
	})

	if m.GetNextTaskFunc != nil {
		return m.GetNextTaskFunc(ctx, state, timeout)
	}

	// Default behavior - no tasks available
	return nil, fmt.Errorf("failed to get next task: rpc error: code = NotFound")
}

// UpdateTask mock implementation
func (m *MockClient) UpdateTask(ctx context.Context, taskID string, currentState string, newState string, payload []byte) (*pb.Task, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.UpdateTaskCalls = append(m.UpdateTaskCalls, UpdateTaskCall{
		TaskID:       taskID,
		CurrentState: currentState,
		NewState:     newState,
		Payload:      payload,
	})

	if m.UpdateTaskFunc != nil {
		return m.UpdateTaskFunc(ctx, taskID, currentState, newState, payload)
	}

	// Default behavior
	return &pb.Task{
		Uuid:            taskID,
		Queue:           "reactorcide-jobs",
		CurrentState:    newState,
		AutoTargetState: "completed",
		UpdateTime:      time.Now().Unix(),
	}, nil
}

// SendHeartbeat mock implementation
func (m *MockClient) SendHeartbeat(ctx context.Context, taskID string, currentState string, timeoutExtensionSeconds int64) (*pb.Task, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.SendHeartbeatCalls = append(m.SendHeartbeatCalls, SendHeartbeatCall{
		TaskID:                  taskID,
		CurrentState:            currentState,
		TimeoutExtensionSeconds: timeoutExtensionSeconds,
	})

	if m.SendHeartbeatFunc != nil {
		return m.SendHeartbeatFunc(ctx, taskID, currentState, timeoutExtensionSeconds)
	}

	// Default behavior - return updated task with extended timeout
	return &pb.Task{
		Uuid:            taskID,
		Queue:           "reactorcide-jobs",
		CurrentState:    currentState,
		AutoTargetState: "completed",
		UpdateTime:      time.Now().Unix(),
		Timeout:         timeoutExtensionSeconds,
	}, nil
}

// CompleteTask mock implementation
func (m *MockClient) CompleteTask(ctx context.Context, taskID string, currentState string) (*pb.Task, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.CompleteTaskCalls = append(m.CompleteTaskCalls, CompleteTaskCall{
		TaskID:       taskID,
		CurrentState: currentState,
	})

	if m.CompleteTaskFunc != nil {
		return m.CompleteTaskFunc(ctx, taskID, currentState)
	}

	// Default behavior
	return &pb.Task{
		Uuid:            taskID,
		Queue:           "reactorcide-jobs",
		CurrentState:    "completed",
		AutoTargetState: "completed",
		UpdateTime:      time.Now().Unix(),
	}, nil
}

// CancelTask mock implementation
func (m *MockClient) CancelTask(ctx context.Context, taskID string, currentState string) (*pb.Task, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.CancelTaskCalls = append(m.CancelTaskCalls, CancelTaskCall{
		TaskID:       taskID,
		CurrentState: currentState,
	})

	if m.CancelTaskFunc != nil {
		return m.CancelTaskFunc(ctx, taskID, currentState)
	}

	// Default behavior
	return &pb.Task{
		Uuid:            taskID,
		Queue:           "reactorcide-jobs",
		CurrentState:    "cancelled",
		AutoTargetState: "cancelled",
		UpdateTime:      time.Now().Unix(),
	}, nil
}

// GetTaskByID mock implementation
func (m *MockClient) GetTaskByID(ctx context.Context, taskID string) (*pb.Task, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.GetTaskByIDCalls = append(m.GetTaskByIDCalls, GetTaskByIDCall{
		TaskID: taskID,
	})

	if m.GetTaskByIDFunc != nil {
		return m.GetTaskByIDFunc(ctx, taskID)
	}

	// Default behavior
	return &pb.Task{
		Uuid:            taskID,
		Queue:           "reactorcide-jobs",
		CurrentState:    "submitted",
		AutoTargetState: "submitted-working",
		UpdateTime:      time.Now().Unix(),
	}, nil
}

// CleanUpTimedOut mock implementation
func (m *MockClient) CleanUpTimedOut(ctx context.Context) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.CleanUpTimedOutCalls = append(m.CleanUpTimedOutCalls, CleanUpTimedOutCall{
		Time: time.Now(),
	})

	if m.CleanUpTimedOutFunc != nil {
		return m.CleanUpTimedOutFunc(ctx)
	}

	// Default behavior
	return 0, nil
}

// GetQueues mock implementation
func (m *MockClient) GetQueues(ctx context.Context) ([]string, int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.GetQueuesFunc != nil {
		return m.GetQueuesFunc(ctx)
	}

	// Default behavior
	return []string{"reactorcide-jobs"}, 1, nil
}

// GetQueueTaskCounts mock implementation
func (m *MockClient) GetQueueTaskCounts(ctx context.Context) (map[string]int64, int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Default behavior
	return map[string]int64{"reactorcide-jobs": 1}, 1, nil
}

// GetTaskStateCounts mock implementation
func (m *MockClient) GetTaskStateCounts(ctx context.Context) (int64, map[string]int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Default behavior
	return 1, map[string]int64{"submitted": 1}, nil
}

// Close mock implementation
func (m *MockClient) Close() error {
	return nil
}

// Reset clears all recorded calls
func (m *MockClient) Reset() {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.SubmitTaskCalls = nil
	m.GetNextTaskCalls = nil
	m.UpdateTaskCalls = nil
	m.SendHeartbeatCalls = nil
	m.CompleteTaskCalls = nil
	m.CancelTaskCalls = nil
	m.GetTaskByIDCalls = nil
	m.CleanUpTimedOutCalls = nil
}

// GetSubmitTaskCallCount returns the number of SubmitTask calls
func (m *MockClient) GetSubmitTaskCallCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.SubmitTaskCalls)
}

// GetCompleteTaskCallCount returns the number of CompleteTask calls
func (m *MockClient) GetCompleteTaskCallCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.CompleteTaskCalls)
}

// GetCancelTaskCallCount returns the number of CancelTask calls
func (m *MockClient) GetCancelTaskCallCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.CancelTaskCalls)
}
