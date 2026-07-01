package corndogs

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	csil "github.com/catalystcommunity/reactorcide/coordinator_api/internal/corndogs/csilapi"
	pb "github.com/catalystcommunity/reactorcide/coordinator_api/internal/corndogs/v1alpha1"
)

// Client wraps the Corndogs CSIL-RPC client.
type Client struct {
	client *csil.CorndogsClient
	config Config
}

// Config holds the configuration for the Corndogs client
type Config struct {
	BaseURL      string
	QueueName    string
	Timeout      time.Duration
	MaxRetries   int
	RetryBackoff time.Duration
}

// NewClient creates a new Corndogs client
func NewClient(config Config) (*Client, error) {
	// Set defaults if not provided
	if config.QueueName == "" {
		config.QueueName = "reactorcide-jobs"
	}
	if config.Timeout == 0 {
		config.Timeout = 30 * time.Second
	}
	if config.MaxRetries == 0 {
		config.MaxRetries = 3
	}
	if config.RetryBackoff == 0 {
		config.RetryBackoff = time.Second
	}

	return &Client{
		client: csil.New(normalizeBaseURL(config.BaseURL)),
		config: config,
	}, nil
}

func normalizeBaseURL(baseURL string) string {
	if strings.Contains(baseURL, "://") {
		return baseURL
	}
	return "http://" + baseURL
}

// Close is retained for the client interface; CSIL-RPC uses per-request HTTP.
func (c *Client) Close() error {
	return nil
}

// TaskPayload represents the JSON payload for a Reactorcide job
type TaskPayload struct {
	JobID    string                 `json:"job_id"`
	JobType  string                 `json:"job_type"`
	Config   map[string]interface{} `json:"config"`
	Source   map[string]interface{} `json:"source"`
	Metadata map[string]interface{} `json:"metadata"`
}

// SubmitTask submits a new task to Corndogs
func (c *Client) SubmitTask(ctx context.Context, payload *TaskPayload, priority int64) (*pb.Task, error) {
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal payload: %w", err)
	}

	req := csil.SubmitTaskRequest{
		Queue:           c.config.QueueName,
		CurrentState:    "submitted",
		AutoTargetState: "submitted-working",
		Timeout:         int64(c.config.Timeout.Seconds()),
		Payload:         payloadBytes,
		Priority:        priority,
	}

	resp, err := c.client.SubmitTask(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("failed to submit task: %w", err)
	}

	return toPBTask(resp.Task), nil
}

// GetNextTask gets the next available task from the queue
func (c *Client) GetNextTask(ctx context.Context, state string, timeout int64) (*pb.Task, error) {
	if state == "" {
		state = "submitted"
	}

	req := csil.GetNextTaskRequest{
		Queue:           c.config.QueueName,
		CurrentState:    state,
		OverrideTimeout: timeout,
	}

	resp, err := c.client.GetNextTask(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("failed to get next task: %w", err)
	}

	return toPBTask(resp.Task), nil
}

// UpdateTask updates the state of a task
func (c *Client) UpdateTask(ctx context.Context, taskID string, currentState string, newState string, payload []byte) (*pb.Task, error) {
	req := csil.UpdateTaskRequest{
		Uuid:         taskID,
		Queue:        c.config.QueueName,
		CurrentState: currentState,
		NewState:     newState,
		Payload:      payload,
	}

	resp, err := c.client.UpdateTask(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("failed to update task: %w", err)
	}

	return toPBTask(resp.Task), nil
}

// CompleteTask marks a task as completed
func (c *Client) CompleteTask(ctx context.Context, taskID string, currentState string) (*pb.Task, error) {
	req := csil.CompleteTaskRequest{
		Uuid:         taskID,
		Queue:        c.config.QueueName,
		CurrentState: currentState,
	}

	resp, err := c.client.CompleteTask(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("failed to complete task: %w", err)
	}

	return toPBTask(resp.Task), nil
}

// CancelTask cancels a task
func (c *Client) CancelTask(ctx context.Context, taskID string, currentState string) (*pb.Task, error) {
	req := csil.CancelTaskRequest{
		Uuid:         taskID,
		Queue:        c.config.QueueName,
		CurrentState: currentState,
	}

	resp, err := c.client.CancelTask(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("failed to cancel task: %w", err)
	}

	return toPBTask(resp.Task), nil
}

// GetTaskByID gets a task by its ID
func (c *Client) GetTaskByID(ctx context.Context, taskID string) (*pb.Task, error) {
	req := csil.GetTaskStateByIDRequest{
		Uuid:  taskID,
		Queue: c.config.QueueName,
	}

	resp, err := c.client.GetTaskStateByID(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("failed to get task by ID: %w", err)
	}

	return toPBTask(resp.Task), nil
}

// CleanUpTimedOut cleans up timed out tasks
func (c *Client) CleanUpTimedOut(ctx context.Context) (int64, error) {
	req := csil.CleanUpTimedOutRequest{
		AtTime: time.Now().Unix(),
		Queue:  c.config.QueueName,
	}

	resp, err := c.client.CleanUpTimedOut(ctx, req)
	if err != nil {
		return 0, fmt.Errorf("failed to clean up timed out tasks: %w", err)
	}

	return resp.TimedOut, nil
}

// GetQueues gets all queues
func (c *Client) GetQueues(ctx context.Context) ([]string, int64, error) {
	req := csil.GetQueuesRequest{}

	resp, err := c.client.GetQueues(ctx, req)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to get queues: %w", err)
	}

	return resp.Queues, resp.TotalTaskCount, nil
}

// GetQueueTaskCounts gets task counts per queue
func (c *Client) GetQueueTaskCounts(ctx context.Context) (map[string]int64, int64, error) {
	req := csil.GetQueueTaskCountsRequest{}

	resp, err := c.client.GetQueueTaskCounts(ctx, req)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to get queue task counts: %w", err)
	}

	return resp.QueueCounts, resp.TotalTaskCount, nil
}

// GetTaskStateCounts gets task counts per state for a queue
func (c *Client) GetTaskStateCounts(ctx context.Context) (int64, map[string]int64, error) {
	req := csil.GetTaskStateCountsRequest{
		Queue: c.config.QueueName,
	}

	resp, err := c.client.GetTaskStateCounts(ctx, req)
	if err != nil {
		return 0, nil, fmt.Errorf("failed to get task state counts: %w", err)
	}

	return resp.Count, resp.StateCounts, nil
}

// SendHeartbeat sends a heartbeat for a task by extending its timeout
// This prevents the task from timing out during long-running operations
func (c *Client) SendHeartbeat(ctx context.Context, taskID string, currentState string, timeoutExtensionSeconds int64) (*pb.Task, error) {
	// Use UpdateTask to extend the timeout
	// We keep the same state and just update the timeout
	req := csil.UpdateTaskRequest{
		Uuid:         taskID,
		Queue:        c.config.QueueName,
		CurrentState: currentState,
		NewState:     currentState, // Keep same state
		Timeout:      timeoutExtensionSeconds,
		Payload:      nil, // Keep existing payload
	}

	resp, err := c.client.UpdateTask(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("failed to send heartbeat: %w", err)
	}

	return toPBTask(resp.Task), nil
}

func toPBTask(task *csil.Task) *pb.Task {
	if task == nil {
		return nil
	}
	return &pb.Task{
		Uuid:            task.Uuid,
		Queue:           task.Queue,
		CurrentState:    task.CurrentState,
		AutoTargetState: task.AutoTargetState,
		SubmitTime:      task.SubmitTime,
		UpdateTime:      task.UpdateTime,
		Timeout:         task.Timeout,
		Payload:         task.Payload,
		Priority:        task.Priority,
	}
}

// ParseTaskPayload parses a task payload into a TaskPayload struct
func ParseTaskPayload(task *pb.Task) (*TaskPayload, error) {
	var payload TaskPayload
	if err := json.Unmarshal(task.Payload, &payload); err != nil {
		return nil, fmt.Errorf("failed to unmarshal task payload: %w", err)
	}
	return &payload, nil
}
