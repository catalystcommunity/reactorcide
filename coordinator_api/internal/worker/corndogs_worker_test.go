package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/corndogs"
	pb "github.com/catalystcommunity/reactorcide/coordinator_api/internal/corndogs/v1alpha1"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

// MockJobProcessor mocks the JobProcessor for testing
type MockJobProcessor struct {
	ProcessJobFunc  func(ctx context.Context, job *models.Job) *JobResult
	ProcessJobCalls []models.Job
}

func (m *MockJobProcessor) ProcessJob(ctx context.Context, job *models.Job) *JobResult {
	return m.ProcessJobWithContext(ctx, job, nil)
}

func (m *MockJobProcessor) ProcessJobWithContext(ctx context.Context, job *models.Job, execCtx *JobExecutionContext) *JobResult {
	m.ProcessJobCalls = append(m.ProcessJobCalls, *job)
	if m.ProcessJobFunc != nil {
		return m.ProcessJobFunc(ctx, job)
	}
	return &JobResult{
		ExitCode:      0,
		LogsObjectKey: "logs/test.log",
	}
}

// MockTriggerProcessor is a test helper that wraps TriggerProcessor to track calls
type MockTriggerProcessor struct {
	ProcessTriggersFunc  func(ctx context.Context, workspaceDir string, parentJob *models.Job) error
	ProcessTriggersCalls []struct {
		WorkspaceDir string
		ParentJob    *models.Job
	}
}

// MockStore implements store.Store for testing
type MockStore struct {
	GetJobByIDFunc  func(ctx context.Context, jobID string) (*models.Job, error)
	UpdateJobFunc   func(ctx context.Context, job *models.Job) error
	CreateJobFunc   func(ctx context.Context, job *models.Job) error
	GetJobByIDCalls []string
	UpdateJobCalls  []models.Job
	CreateJobCalls  []models.Job
}

func (m *MockStore) GetJobByID(ctx context.Context, jobID string) (*models.Job, error) {
	m.GetJobByIDCalls = append(m.GetJobByIDCalls, jobID)
	if m.GetJobByIDFunc != nil {
		return m.GetJobByIDFunc(ctx, jobID)
	}
	return nil, store.ErrNotFound
}

func (m *MockStore) UpdateJob(ctx context.Context, job *models.Job) error {
	m.UpdateJobCalls = append(m.UpdateJobCalls, *job)
	if m.UpdateJobFunc != nil {
		return m.UpdateJobFunc(ctx, job)
	}
	return nil
}

// Implement other required store.Store methods with minimal functionality
func (m *MockStore) Initialize() (func(), error)                          { return nil, nil }
func (m *MockStore) CreateJob(ctx context.Context, job *models.Job) error {
	m.CreateJobCalls = append(m.CreateJobCalls, *job)
	if m.CreateJobFunc != nil {
		return m.CreateJobFunc(ctx, job)
	}
	return nil
}
func (m *MockStore) ListJobs(ctx context.Context, filters map[string]interface{}, limit, offset int) ([]models.Job, error) {
	return nil, nil
}
func (m *MockStore) DeleteJob(ctx context.Context, jobID string) error { return nil }
func (m *MockStore) GetJobsByUser(ctx context.Context, userID string, limit, offset int) ([]models.Job, error) {
	return nil, nil
}
func (m *MockStore) CreateUser(ctx context.Context, user *models.User) error { return nil }
func (m *MockStore) GetUserByID(ctx context.Context, userID string) (*models.User, error) {
	return nil, nil
}
func (m *MockStore) EnsureDefaultUser() error { return nil }
func (m *MockStore) ValidateAPIToken(ctx context.Context, token string) (*models.APIToken, *models.User, error) {
	return nil, nil, nil
}
func (m *MockStore) CreateAPIToken(ctx context.Context, apiToken *models.APIToken) error { return nil }
func (m *MockStore) UpdateTokenLastUsed(ctx context.Context, tokenID string, lastUsed time.Time) error {
	return nil
}
func (m *MockStore) GetAPITokensByUser(ctx context.Context, userID string) ([]models.APIToken, error) {
	return nil, nil
}
func (m *MockStore) DeleteAPIToken(ctx context.Context, tokenID string) error { return nil }

// Project operations (stubs for interface compliance)
func (m *MockStore) CreateProject(ctx context.Context, project *models.Project) error { return nil }
func (m *MockStore) GetProjectByID(ctx context.Context, projectID string) (*models.Project, error) {
	return nil, nil
}
func (m *MockStore) GetProjectByRepoURL(ctx context.Context, repoURL string) (*models.Project, error) {
	return nil, nil
}
func (m *MockStore) UpdateProject(ctx context.Context, project *models.Project) error { return nil }
func (m *MockStore) DeleteProject(ctx context.Context, projectID string) error        { return nil }
func (m *MockStore) ListProjects(ctx context.Context, limit, offset int) ([]models.Project, error) {
	return nil, nil
}

func TestCornDogsWorker_ProcessNextTask_Success(t *testing.T) {
	// Setup mocks
	mockStore := &MockStore{}
	mockCorndogs := corndogs.NewMockClient()
	mockProcessor := &MockJobProcessor{}

	// Create task payload
	taskPayload := &corndogs.TaskPayload{
		JobID:   "test-job-id",
		JobType: "run",
		Config: map[string]interface{}{
			"command": "echo hello",
		},
	}
	payloadBytes, _ := json.Marshal(taskPayload)

	// Setup Corndogs mock to return a task
	mockCorndogs.GetNextTaskFunc = func(ctx context.Context, state string, timeout int64) (*pb.Task, error) {
		return &pb.Task{
			Uuid:            "task-id",
			CurrentState:    "submitted-working",
			AutoTargetState: "completed",
			Payload:         payloadBytes,
		}, nil
	}

	// Setup store mock to return a job
	mockStore.GetJobByIDFunc = func(ctx context.Context, jobID string) (*models.Job, error) {
		return &models.Job{
			JobID:      jobID,
			Status:     "submitted",
			JobCommand: "echo hello",
		}, nil
	}

	mockStore.UpdateJobFunc = func(ctx context.Context, job *models.Job) error {
		return nil
	}

	// Setup processor mock
	mockProcessor.ProcessJobFunc = func(ctx context.Context, job *models.Job) *JobResult {
		return &JobResult{
			ExitCode:      0,
			LogsObjectKey: "logs/success.log",
		}
	}

	// Create worker config
	config := &Config{
		QueueName:    "test-queue",
		PollInterval: 100 * time.Millisecond,
		Concurrency:  1,
		DryRun:       false,
		Store:        mockStore,
	}

	// Create worker
	worker := NewCornDogsWorkerWithProcessor(config, mockCorndogs, mockProcessor, nil)

	// Process one task
	ctx := context.Background()
	worker.processNextTask(ctx, 0)

	// Verify Corndogs interactions
	if mockCorndogs.GetSubmitTaskCallCount() != 0 {
		t.Errorf("expected no submit calls, got %d", mockCorndogs.GetSubmitTaskCallCount())
	}

	// Should have called GetNextTask
	if len(mockCorndogs.GetNextTaskCalls) != 1 {
		t.Errorf("expected 1 GetNextTask call, got %d", len(mockCorndogs.GetNextTaskCalls))
	}

	// Should have updated task state to processing
	if len(mockCorndogs.UpdateTaskCalls) != 1 {
		t.Errorf("expected 1 UpdateTask call, got %d", len(mockCorndogs.UpdateTaskCalls))
	} else {
		call := mockCorndogs.UpdateTaskCalls[0]
		if call.NewState != "processing" {
			t.Errorf("expected state update to 'processing', got %s", call.NewState)
		}
	}

	// Should have completed the task
	if mockCorndogs.GetCompleteTaskCallCount() != 1 {
		t.Errorf("expected 1 CompleteTask call, got %d", mockCorndogs.GetCompleteTaskCallCount())
	}

	// Verify store interactions
	if len(mockStore.GetJobByIDCalls) != 1 {
		t.Errorf("expected 1 GetJobByID call, got %d", len(mockStore.GetJobByIDCalls))
	}

	if len(mockStore.UpdateJobCalls) != 2 { // Once for running, once for completed
		t.Errorf("expected 2 UpdateJob calls, got %d", len(mockStore.UpdateJobCalls))
	}

	// Verify job status updates
	if mockStore.UpdateJobCalls[0].Status != "running" {
		t.Errorf("expected first update to set status to 'running', got %s", mockStore.UpdateJobCalls[0].Status)
	}

	if mockStore.UpdateJobCalls[1].Status != "completed" {
		t.Errorf("expected second update to set status to 'completed', got %s", mockStore.UpdateJobCalls[1].Status)
	}

	// Verify processor was called
	if len(mockProcessor.ProcessJobCalls) != 1 {
		t.Errorf("expected 1 ProcessJob call, got %d", len(mockProcessor.ProcessJobCalls))
	}
}

func TestCornDogsWorker_ProcessNextTask_JobExecutionFailure(t *testing.T) {
	// Setup mocks
	mockStore := &MockStore{}
	mockCorndogs := corndogs.NewMockClient()
	mockProcessor := &MockJobProcessor{}

	// Create task payload
	taskPayload := &corndogs.TaskPayload{
		JobID:   "test-job-id",
		JobType: "run",
	}
	payloadBytes, _ := json.Marshal(taskPayload)

	// Setup Corndogs mock to return a task
	mockCorndogs.GetNextTaskFunc = func(ctx context.Context, state string, timeout int64) (*pb.Task, error) {
		return &pb.Task{
			Uuid:            "task-id",
			CurrentState:    "submitted-working",
			AutoTargetState: "completed",
			Payload:         payloadBytes,
		}, nil
	}

	// Setup store mock
	mockStore.GetJobByIDFunc = func(ctx context.Context, jobID string) (*models.Job, error) {
		return &models.Job{
			JobID:  jobID,
			Status: "submitted",
		}, nil
	}

	mockStore.UpdateJobFunc = func(ctx context.Context, job *models.Job) error {
		return nil
	}

	// Setup processor to return failure
	mockProcessor.ProcessJobFunc = func(ctx context.Context, job *models.Job) *JobResult {
		return &JobResult{
			ExitCode:      1, // Non-zero exit code
			LogsObjectKey: "logs/failure.log",
		}
	}

	// Create worker config
	config := &Config{
		QueueName:    "test-queue",
		PollInterval: 100 * time.Millisecond,
		Concurrency:  1,
		DryRun:       false,
		Store:        mockStore,
	}

	// Create worker
	worker := NewCornDogsWorkerWithProcessor(config, mockCorndogs, mockProcessor, nil)

	// Process one task
	ctx := context.Background()
	worker.processNextTask(ctx, 0)

	// Should NOT have completed the task
	if mockCorndogs.GetCompleteTaskCallCount() != 0 {
		t.Errorf("expected no CompleteTask calls for failed job, got %d", mockCorndogs.GetCompleteTaskCallCount())
	}

	// Should have updated task to failed state
	failedUpdateFound := false
	for _, call := range mockCorndogs.UpdateTaskCalls {
		if call.NewState == "failed" {
			failedUpdateFound = true
			// Check that error payload was included
			var payload map[string]interface{}
			if err := json.Unmarshal(call.Payload, &payload); err == nil {
				if _, ok := payload["error"]; !ok {
					t.Error("expected error in failed task payload")
				}
			}
			break
		}
	}

	if !failedUpdateFound {
		t.Error("expected task to be updated to failed state")
	}

	// Verify final job status
	lastUpdate := mockStore.UpdateJobCalls[len(mockStore.UpdateJobCalls)-1]
	if lastUpdate.Status != "failed" {
		t.Errorf("expected job status to be 'failed', got %s", lastUpdate.Status)
	}
	if lastUpdate.ExitCode == nil || *lastUpdate.ExitCode != 1 {
		t.Error("expected exit code to be 1")
	}
}

func TestCornDogsWorker_ProcessNextTask_NoTasksAvailable(t *testing.T) {
	// Setup mocks
	mockStore := &MockStore{}
	mockCorndogs := corndogs.NewMockClient()

	// Setup Corndogs mock to return no tasks
	mockCorndogs.GetNextTaskFunc = func(ctx context.Context, state string, timeout int64) (*pb.Task, error) {
		return nil, fmt.Errorf("failed to get next task: rpc error: code = NotFound")
	}

	// Create worker config
	config := &Config{
		QueueName:    "test-queue",
		PollInterval: 100 * time.Millisecond,
		Concurrency:  1,
		DryRun:       false,
		Store:        mockStore,
	}

	// Create worker
	worker := &CornDogsWorker{
		config:         config,
		corndogsClient: mockCorndogs,
		processor:      &MockJobProcessor{},
		workerPool:     make(chan struct{}, 1),
	}

	// Process (should handle gracefully when no tasks)
	ctx := context.Background()
	worker.processNextTask(ctx, 0)

	// Should have called GetNextTask
	if len(mockCorndogs.GetNextTaskCalls) != 1 {
		t.Errorf("expected 1 GetNextTask call, got %d", len(mockCorndogs.GetNextTaskCalls))
	}

	// Should not have made any other calls
	if mockCorndogs.GetCompleteTaskCallCount() != 0 {
		t.Errorf("expected no CompleteTask calls, got %d", mockCorndogs.GetCompleteTaskCallCount())
	}

	if len(mockCorndogs.UpdateTaskCalls) != 0 {
		t.Errorf("expected no UpdateTask calls, got %d", len(mockCorndogs.UpdateTaskCalls))
	}

	// Should not have accessed the store
	if len(mockStore.GetJobByIDCalls) != 0 {
		t.Errorf("expected no GetJobByID calls, got %d", len(mockStore.GetJobByIDCalls))
	}
}

func TestCornDogsWorker_ProcessNextTask_InvalidPayload(t *testing.T) {
	// Setup mocks
	mockStore := &MockStore{}
	mockCorndogs := corndogs.NewMockClient()

	// Setup Corndogs mock to return a task with invalid payload
	mockCorndogs.GetNextTaskFunc = func(ctx context.Context, state string, timeout int64) (*pb.Task, error) {
		return &pb.Task{
			Uuid:            "task-id",
			CurrentState:    "submitted-working",
			AutoTargetState: "completed",
			Payload:         []byte("invalid json"),
		}, nil
	}

	// Create worker config
	config := &Config{
		QueueName:    "test-queue",
		PollInterval: 100 * time.Millisecond,
		Concurrency:  1,
		DryRun:       false,
		Store:        mockStore,
	}

	// Create worker
	worker := &CornDogsWorker{
		config:         config,
		corndogsClient: mockCorndogs,
		processor:      &MockJobProcessor{},
		workerPool:     make(chan struct{}, 1),
	}

	// Process one task
	ctx := context.Background()
	worker.processNextTask(ctx, 0)

	// Should have updated task to failed state
	failedUpdateFound := false
	for _, call := range mockCorndogs.UpdateTaskCalls {
		if call.NewState == "failed" {
			failedUpdateFound = true
			// Check that error payload mentions parse failure
			var payload map[string]interface{}
			if err := json.Unmarshal(call.Payload, &payload); err == nil {
				if errorMsg, ok := payload["error"].(string); ok {
					if errorMsg != "Failed to parse payload" {
						t.Errorf("expected error message about parse failure, got %s", errorMsg)
					}
				}
			}
			break
		}
	}

	if !failedUpdateFound {
		t.Error("expected task to be updated to failed state for invalid payload")
	}

	// Should not have accessed the store (job ID couldn't be parsed)
	if len(mockStore.GetJobByIDCalls) != 0 {
		t.Errorf("expected no GetJobByID calls, got %d", len(mockStore.GetJobByIDCalls))
	}
}

func TestCornDogsWorker_ProcessNextTask_JobNotFoundInDatabase(t *testing.T) {
	// Setup mocks
	mockStore := &MockStore{}
	mockCorndogs := corndogs.NewMockClient()

	// Create task payload
	taskPayload := &corndogs.TaskPayload{
		JobID:   "non-existent-job",
		JobType: "run",
	}
	payloadBytes, _ := json.Marshal(taskPayload)

	// Setup Corndogs mock to return a task
	mockCorndogs.GetNextTaskFunc = func(ctx context.Context, state string, timeout int64) (*pb.Task, error) {
		return &pb.Task{
			Uuid:            "task-id",
			CurrentState:    "submitted-working",
			AutoTargetState: "completed",
			Payload:         payloadBytes,
		}, nil
	}

	// Setup store mock to return not found
	mockStore.GetJobByIDFunc = func(ctx context.Context, jobID string) (*models.Job, error) {
		return nil, fmt.Errorf("job not found")
	}

	// Create worker config
	config := &Config{
		QueueName:    "test-queue",
		PollInterval: 100 * time.Millisecond,
		Concurrency:  1,
		DryRun:       false,
		Store:        mockStore,
	}

	// Create worker
	worker := &CornDogsWorker{
		config:         config,
		corndogsClient: mockCorndogs,
		processor:      &MockJobProcessor{},
		workerPool:     make(chan struct{}, 1),
	}

	// Process one task
	ctx := context.Background()
	worker.processNextTask(ctx, 0)

	// Should have updated task to failed state
	failedUpdateFound := false
	for _, call := range mockCorndogs.UpdateTaskCalls {
		if call.NewState == "failed" {
			failedUpdateFound = true
			// Check that error payload mentions job not found
			var payload map[string]interface{}
			if err := json.Unmarshal(call.Payload, &payload); err == nil {
				if errorMsg, ok := payload["error"].(string); ok {
					if errorMsg != "Job not found in database" {
						t.Errorf("expected error message about job not found, got %s", errorMsg)
					}
				}
			}
			break
		}
	}

	if !failedUpdateFound {
		t.Error("expected task to be updated to failed state when job not found")
	}
}

func TestCornDogsWorker_Start_GracefulShutdown(t *testing.T) {
	// Setup mocks
	mockStore := &MockStore{}
	mockCorndogs := corndogs.NewMockClient()

	// Setup Corndogs mock to always return no tasks
	mockCorndogs.GetNextTaskFunc = func(ctx context.Context, state string, timeout int64) (*pb.Task, error) {
		return nil, fmt.Errorf("failed to get next task: rpc error: code = NotFound")
	}

	// Create worker config with short poll interval
	config := &Config{
		QueueName:    "test-queue",
		PollInterval: 10 * time.Millisecond,
		Concurrency:  2,
		DryRun:       false,
		Store:        mockStore,
	}

	// Create worker
	worker := NewCornDogsWorker(config, mockCorndogs)

	// Start worker in goroutine
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)

	go func() {
		done <- worker.Start(ctx)
	}()

	// Let it run for a short time
	time.Sleep(50 * time.Millisecond)

	// Cancel context to trigger shutdown
	cancel()

	// Wait for worker to finish
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("expected nil error from Start, got %v", err)
		}
	case <-time.After(1 * time.Second):
		t.Error("worker did not shut down within timeout")
	}

	// Verify some GetNextTask calls were made
	if len(mockCorndogs.GetNextTaskCalls) < 2 {
		t.Errorf("expected at least 2 GetNextTask calls from 2 workers, got %d", len(mockCorndogs.GetNextTaskCalls))
	}
}

func TestCornDogsWorker_ProcessNextTask_TriggersProcessedOnSuccess(t *testing.T) {
	// Setup mocks
	mockStore := &MockStore{}
	mockCorndogs := corndogs.NewMockClient()
	mockProcessor := &MockJobProcessor{}

	// Create a temporary workspace with a triggers file
	workspaceDir := t.TempDir()
	triggersData := triggersFile{
		Type: "trigger_job",
		Jobs: []triggerJobSpec{
			{
				JobName:        "triggered-test",
				ContainerImage: "alpine:latest",
				JobCommand:     "echo hello",
			},
		},
	}
	writeTriggersFile(t, workspaceDir, triggersData)

	// Create task payload
	taskPayload := &corndogs.TaskPayload{
		JobID:   "eval-job-id",
		JobType: "run",
	}
	payloadBytes, _ := json.Marshal(taskPayload)

	// Setup Corndogs mock to return a task
	mockCorndogs.GetNextTaskFunc = func(ctx context.Context, state string, timeout int64) (*pb.Task, error) {
		return &pb.Task{
			Uuid:            "task-id",
			CurrentState:    "submitted-working",
			AutoTargetState: "completed",
			Payload:         payloadBytes,
		}, nil
	}

	// Setup store mock to return a job
	mockStore.GetJobByIDFunc = func(ctx context.Context, jobID string) (*models.Job, error) {
		return &models.Job{
			JobID:          jobID,
			UserID:         "user-123",
			Status:         "submitted",
			JobCommand:     "runnerlib eval",
			QueueName:      "reactorcide-jobs",
			RunnerImage:    "runnerbase:dev",
			TimeoutSeconds: 3600,
		}, nil
	}

	mockStore.UpdateJobFunc = func(ctx context.Context, job *models.Job) error {
		return nil
	}
	mockStore.CreateJobFunc = func(ctx context.Context, job *models.Job) error {
		job.JobID = "triggered-job-id"
		return nil
	}

	// Setup processor mock to return success with workspace dir
	mockProcessor.ProcessJobFunc = func(ctx context.Context, job *models.Job) *JobResult {
		return &JobResult{
			ExitCode:      0,
			LogsObjectKey: "logs/eval.log",
			WorkspaceDir:  workspaceDir,
		}
	}

	// Create worker config
	config := &Config{
		QueueName:    "test-queue",
		PollInterval: 100 * time.Millisecond,
		Concurrency:  1,
		DryRun:       false,
		Store:        mockStore,
	}

	// Create trigger processor with the mock store and corndogs client
	triggerProc := NewTriggerProcessor(mockStore, mockCorndogs)

	// Create worker with trigger processor
	worker := NewCornDogsWorkerWithProcessor(config, mockCorndogs, mockProcessor, triggerProc)

	// Process one task
	ctx := context.Background()
	worker.processNextTask(ctx, 0)

	// Verify the triggered job was created in the store
	if len(mockStore.CreateJobCalls) != 1 {
		t.Fatalf("expected 1 CreateJob call from trigger processing, got %d", len(mockStore.CreateJobCalls))
	}

	createdJob := mockStore.CreateJobCalls[0]
	if createdJob.Name != "triggered-test" {
		t.Errorf("expected triggered job name 'triggered-test', got %q", createdJob.Name)
	}
	if createdJob.RunnerImage != "alpine:latest" {
		t.Errorf("expected triggered job image 'alpine:latest', got %q", createdJob.RunnerImage)
	}

	// Verify the triggered job was submitted to Corndogs
	// GetSubmitTaskCallCount includes the trigger's submission
	if mockCorndogs.GetSubmitTaskCallCount() != 1 {
		t.Errorf("expected 1 SubmitTask call from trigger processing, got %d", mockCorndogs.GetSubmitTaskCallCount())
	}

	// Verify the eval job itself was completed
	if mockCorndogs.GetCompleteTaskCallCount() != 1 {
		t.Errorf("expected 1 CompleteTask call, got %d", mockCorndogs.GetCompleteTaskCallCount())
	}
}

func TestCornDogsWorker_ProcessNextTask_NoTriggersOnFailure(t *testing.T) {
	// Setup mocks
	mockStore := &MockStore{}
	mockCorndogs := corndogs.NewMockClient()
	mockProcessor := &MockJobProcessor{}

	// Create a temporary workspace with triggers that should NOT be processed
	workspaceDir := t.TempDir()
	triggersData := triggersFile{
		Type: "trigger_job",
		Jobs: []triggerJobSpec{
			{
				JobName:    "should-not-run",
				JobCommand: "echo nope",
			},
		},
	}
	writeTriggersFile(t, workspaceDir, triggersData)

	// Create task payload
	taskPayload := &corndogs.TaskPayload{
		JobID:   "eval-job-id",
		JobType: "run",
	}
	payloadBytes, _ := json.Marshal(taskPayload)

	// Setup Corndogs mock to return a task
	mockCorndogs.GetNextTaskFunc = func(ctx context.Context, state string, timeout int64) (*pb.Task, error) {
		return &pb.Task{
			Uuid:            "task-id",
			CurrentState:    "submitted-working",
			AutoTargetState: "completed",
			Payload:         payloadBytes,
		}, nil
	}

	// Setup store mock
	mockStore.GetJobByIDFunc = func(ctx context.Context, jobID string) (*models.Job, error) {
		return &models.Job{
			JobID:       jobID,
			UserID:      "user-123",
			Status:      "submitted",
			JobCommand:  "runnerlib eval",
			QueueName:   "reactorcide-jobs",
			RunnerImage: "runnerbase:dev",
		}, nil
	}
	mockStore.UpdateJobFunc = func(ctx context.Context, job *models.Job) error {
		return nil
	}

	// Setup processor to return FAILURE with workspace dir
	mockProcessor.ProcessJobFunc = func(ctx context.Context, job *models.Job) *JobResult {
		return &JobResult{
			ExitCode:     1,
			WorkspaceDir: workspaceDir,
		}
	}

	// Create trigger processor
	triggerProc := NewTriggerProcessor(mockStore, mockCorndogs)

	// Create worker config
	config := &Config{
		QueueName:    "test-queue",
		PollInterval: 100 * time.Millisecond,
		Concurrency:  1,
		DryRun:       false,
		Store:        mockStore,
	}

	// Create worker with trigger processor
	worker := NewCornDogsWorkerWithProcessor(config, mockCorndogs, mockProcessor, triggerProc)

	// Process one task
	ctx := context.Background()
	worker.processNextTask(ctx, 0)

	// Verify NO triggered jobs were created (trigger processing skipped on failure)
	if len(mockStore.CreateJobCalls) != 0 {
		t.Errorf("expected 0 CreateJob calls on failed job, got %d", len(mockStore.CreateJobCalls))
	}

	// Verify NO triggered jobs were submitted to Corndogs
	if mockCorndogs.GetSubmitTaskCallCount() != 0 {
		t.Errorf("expected 0 SubmitTask calls on failed job, got %d", mockCorndogs.GetSubmitTaskCallCount())
	}
}

func TestCornDogsWorker_ProcessNextTask_NoTriggersFile(t *testing.T) {
	// Setup mocks
	mockStore := &MockStore{}
	mockCorndogs := corndogs.NewMockClient()
	mockProcessor := &MockJobProcessor{}

	// Create a workspace with NO triggers file (normal job, not eval)
	workspaceDir := t.TempDir()

	// Create task payload
	taskPayload := &corndogs.TaskPayload{
		JobID:   "regular-job-id",
		JobType: "run",
	}
	payloadBytes, _ := json.Marshal(taskPayload)

	// Setup Corndogs mock
	mockCorndogs.GetNextTaskFunc = func(ctx context.Context, state string, timeout int64) (*pb.Task, error) {
		return &pb.Task{
			Uuid:            "task-id",
			CurrentState:    "submitted-working",
			AutoTargetState: "completed",
			Payload:         payloadBytes,
		}, nil
	}

	// Setup store mock
	mockStore.GetJobByIDFunc = func(ctx context.Context, jobID string) (*models.Job, error) {
		return &models.Job{
			JobID:       jobID,
			UserID:      "user-123",
			Status:      "submitted",
			JobCommand:  "echo hello",
			QueueName:   "reactorcide-jobs",
			RunnerImage: "alpine:latest",
		}, nil
	}
	mockStore.UpdateJobFunc = func(ctx context.Context, job *models.Job) error {
		return nil
	}

	// Processor returns success with workspace but no triggers.json exists
	mockProcessor.ProcessJobFunc = func(ctx context.Context, job *models.Job) *JobResult {
		return &JobResult{
			ExitCode:     0,
			WorkspaceDir: workspaceDir,
		}
	}

	// Create trigger processor
	triggerProc := NewTriggerProcessor(mockStore, mockCorndogs)

	config := &Config{
		QueueName:    "test-queue",
		PollInterval: 100 * time.Millisecond,
		Concurrency:  1,
		DryRun:       false,
		Store:        mockStore,
	}

	worker := NewCornDogsWorkerWithProcessor(config, mockCorndogs, mockProcessor, triggerProc)

	ctx := context.Background()
	worker.processNextTask(ctx, 0)

	// No triggers file means no jobs created - should complete without error
	if len(mockStore.CreateJobCalls) != 0 {
		t.Errorf("expected 0 CreateJob calls when no triggers.json, got %d", len(mockStore.CreateJobCalls))
	}

	// The task should still be completed
	if mockCorndogs.GetCompleteTaskCallCount() != 1 {
		t.Errorf("expected 1 CompleteTask call, got %d", mockCorndogs.GetCompleteTaskCallCount())
	}
}

func TestCornDogsWorker_ProcessNextTask_NilTriggerProcessor(t *testing.T) {
	// Verify worker works correctly when triggerProcessor is nil (backwards compatible)
	mockStore := &MockStore{}
	mockCorndogs := corndogs.NewMockClient()
	mockProcessor := &MockJobProcessor{}

	taskPayload := &corndogs.TaskPayload{
		JobID:   "test-job-id",
		JobType: "run",
	}
	payloadBytes, _ := json.Marshal(taskPayload)

	mockCorndogs.GetNextTaskFunc = func(ctx context.Context, state string, timeout int64) (*pb.Task, error) {
		return &pb.Task{
			Uuid:            "task-id",
			CurrentState:    "submitted-working",
			AutoTargetState: "completed",
			Payload:         payloadBytes,
		}, nil
	}

	mockStore.GetJobByIDFunc = func(ctx context.Context, jobID string) (*models.Job, error) {
		return &models.Job{
			JobID:      jobID,
			Status:     "submitted",
			JobCommand: "echo hello",
		}, nil
	}
	mockStore.UpdateJobFunc = func(ctx context.Context, job *models.Job) error {
		return nil
	}

	mockProcessor.ProcessJobFunc = func(ctx context.Context, job *models.Job) *JobResult {
		return &JobResult{
			ExitCode:     0,
			WorkspaceDir: "/some/workspace",
		}
	}

	config := &Config{
		QueueName:    "test-queue",
		PollInterval: 100 * time.Millisecond,
		Concurrency:  1,
		DryRun:       false,
		Store:        mockStore,
	}

	// Create worker with nil trigger processor
	worker := NewCornDogsWorkerWithProcessor(config, mockCorndogs, mockProcessor, nil)

	ctx := context.Background()
	worker.processNextTask(ctx, 0)

	// Should complete normally without trigger processing
	if mockCorndogs.GetCompleteTaskCallCount() != 1 {
		t.Errorf("expected 1 CompleteTask call, got %d", mockCorndogs.GetCompleteTaskCallCount())
	}

	// No CreateJob calls since trigger processor is nil
	if len(mockStore.CreateJobCalls) != 0 {
		t.Errorf("expected 0 CreateJob calls with nil trigger processor, got %d", len(mockStore.CreateJobCalls))
	}
}
