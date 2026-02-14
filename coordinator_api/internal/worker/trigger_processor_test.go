package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/corndogs"
	pb "github.com/catalystcommunity/reactorcide/coordinator_api/internal/corndogs/v1alpha1"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

func TestProcessTriggers_NoTriggersFile(t *testing.T) {
	mockStore := &MockStore{}
	mockCorndogs := corndogs.NewMockClient()
	tp := NewTriggerProcessor(mockStore, mockCorndogs)

	err := tp.ProcessTriggers(context.Background(), "/nonexistent/path", &models.Job{})
	if err != nil {
		t.Errorf("expected no error for missing triggers file, got %v", err)
	}

	if mockCorndogs.GetSubmitTaskCallCount() != 0 {
		t.Error("expected no Corndogs calls when triggers file is missing")
	}
}

func TestProcessTriggers_EmptyJobs(t *testing.T) {
	tmpDir := t.TempDir()
	triggersData := triggersFile{
		Type: "trigger_job",
		Jobs: []triggerJobSpec{},
	}
	writeTriggersFile(t, tmpDir, triggersData)

	mockStore := &MockStore{}
	mockCorndogs := corndogs.NewMockClient()
	tp := NewTriggerProcessor(mockStore, mockCorndogs)

	err := tp.ProcessTriggers(context.Background(), tmpDir, &models.Job{})
	if err != nil {
		t.Errorf("expected no error for empty jobs list, got %v", err)
	}

	if mockCorndogs.GetSubmitTaskCallCount() != 0 {
		t.Error("expected no Corndogs calls for empty jobs list")
	}
}

func TestProcessTriggers_InvalidJSON(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "triggers.json"), []byte("not json"), 0644); err != nil {
		t.Fatal(err)
	}

	mockStore := &MockStore{}
	mockCorndogs := corndogs.NewMockClient()
	tp := NewTriggerProcessor(mockStore, mockCorndogs)

	err := tp.ProcessTriggers(context.Background(), tmpDir, &models.Job{})
	if err == nil {
		t.Error("expected error for invalid JSON, got nil")
	}
}

func TestProcessTriggers_WrongType(t *testing.T) {
	tmpDir := t.TempDir()
	triggersData := triggersFile{
		Type: "unknown_type",
		Jobs: []triggerJobSpec{{JobName: "test"}},
	}
	writeTriggersFile(t, tmpDir, triggersData)

	mockStore := &MockStore{}
	mockCorndogs := corndogs.NewMockClient()
	tp := NewTriggerProcessor(mockStore, mockCorndogs)

	err := tp.ProcessTriggers(context.Background(), tmpDir, &models.Job{})
	if err == nil {
		t.Error("expected error for wrong type, got nil")
	}
}

func TestProcessTriggers_SingleJob(t *testing.T) {
	tmpDir := t.TempDir()
	priority := 10
	timeout := 1800
	triggersData := triggersFile{
		Type: "trigger_job",
		Jobs: []triggerJobSpec{
			{
				JobName:        "test-job",
				ContainerImage: "alpine:latest",
				JobCommand:     "make test",
				SourceType:     "git",
				SourceURL:      "https://github.com/org/repo.git",
				SourceRef:      "abc123",
				CISourceType:   "git",
				CISourceURL:    "https://github.com/org/ci.git",
				CISourceRef:    "main",
				Priority:       &priority,
				Timeout:        &timeout,
				Env: map[string]string{
					"REACTORCIDE_EVENT_TYPE": "push",
					"REACTORCIDE_BRANCH":    "main",
				},
			},
		},
	}
	writeTriggersFile(t, tmpDir, triggersData)

	var createdJobs []models.Job
	mockStore := &MockStore{
		CreateJobFunc: func(ctx context.Context, job *models.Job) error {
			job.JobID = "generated-job-id"
			createdJobs = append(createdJobs, *job)
			return nil
		},
	}
	mockCorndogs := corndogs.NewMockClient()

	parentJob := &models.Job{
		JobID:     "parent-job-id",
		UserID:    "user-123",
		QueueName: "reactorcide-jobs",
		JobEnvVars: models.JSONB{
			"REACTORCIDE_CI":       "true",
			"REACTORCIDE_PROVIDER": "github",
		},
	}

	tp := NewTriggerProcessor(mockStore, mockCorndogs)
	err := tp.ProcessTriggers(context.Background(), tmpDir, parentJob)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify job was created
	if len(createdJobs) != 1 {
		t.Fatalf("expected 1 job created, got %d", len(createdJobs))
	}

	job := createdJobs[0]
	if job.Name != "test-job" {
		t.Errorf("expected job name 'test-job', got %q", job.Name)
	}
	if job.UserID != "user-123" {
		t.Errorf("expected user ID 'user-123', got %q", job.UserID)
	}
	if job.ParentJobID == nil || *job.ParentJobID != "parent-job-id" {
		t.Error("expected parent job ID to be set")
	}
	if job.RunnerImage != "alpine:latest" {
		t.Errorf("expected runner image 'alpine:latest', got %q", job.RunnerImage)
	}
	if job.JobCommand != "make test" {
		t.Errorf("expected job command 'make test', got %q", job.JobCommand)
	}
	if job.Priority != 10 {
		t.Errorf("expected priority 10, got %d", job.Priority)
	}
	if job.TimeoutSeconds != 1800 {
		t.Errorf("expected timeout 1800, got %d", job.TimeoutSeconds)
	}
	if job.SourceType == nil || string(*job.SourceType) != "git" {
		t.Error("expected source type 'git'")
	}
	if job.SourceURL == nil || *job.SourceURL != "https://github.com/org/repo.git" {
		t.Error("expected source URL to be set")
	}
	if job.SourceRef == nil || *job.SourceRef != "abc123" {
		t.Error("expected source ref to be set")
	}
	if job.CISourceType == nil || string(*job.CISourceType) != "git" {
		t.Error("expected CI source type 'git'")
	}
	if job.CISourceURL == nil || *job.CISourceURL != "https://github.com/org/ci.git" {
		t.Error("expected CI source URL to be set")
	}

	// Verify env vars are merged (parent + trigger)
	if job.JobEnvVars["REACTORCIDE_CI"] != "true" {
		t.Error("expected parent env var 'CI' to be inherited")
	}
	if job.JobEnvVars["REACTORCIDE_PROVIDER"] != "github" {
		t.Error("expected parent env var 'REACTORCIDE_PROVIDER' to be inherited")
	}
	if job.JobEnvVars["REACTORCIDE_EVENT_TYPE"] != "push" {
		t.Error("expected trigger env var 'REACTORCIDE_EVENT_TYPE' to be set")
	}
	if job.JobEnvVars["REACTORCIDE_BRANCH"] != "main" {
		t.Error("expected trigger env var 'REACTORCIDE_BRANCH' to be set")
	}

	// Verify Corndogs submission
	if mockCorndogs.GetSubmitTaskCallCount() != 1 {
		t.Fatalf("expected 1 SubmitTask call, got %d", mockCorndogs.GetSubmitTaskCallCount())
	}

	submitCall := mockCorndogs.SubmitTaskCalls[0]
	if submitCall.Payload.JobID != "generated-job-id" {
		t.Errorf("expected task payload job ID 'generated-job-id', got %q", submitCall.Payload.JobID)
	}
	if submitCall.Payload.JobType != "run" {
		t.Errorf("expected task type 'run', got %q", submitCall.Payload.JobType)
	}
	if submitCall.Priority != 10 {
		t.Errorf("expected task priority 10, got %d", submitCall.Priority)
	}
}

func TestProcessTriggers_MultipleJobs(t *testing.T) {
	tmpDir := t.TempDir()
	priority1 := 5
	priority2 := 20
	triggersData := triggersFile{
		Type: "trigger_job",
		Jobs: []triggerJobSpec{
			{
				JobName:        "test",
				ContainerImage: "alpine:latest",
				JobCommand:     "make test",
				Priority:       &priority1,
			},
			{
				JobName:        "build",
				ContainerImage: "golang:1.21",
				JobCommand:     "make build",
				Priority:       &priority2,
			},
		},
	}
	writeTriggersFile(t, tmpDir, triggersData)

	createCount := 0
	mockStore := &MockStore{
		CreateJobFunc: func(ctx context.Context, job *models.Job) error {
			createCount++
			job.JobID = fmt.Sprintf("job-%s", job.Name)
			return nil
		},
	}
	mockCorndogs := corndogs.NewMockClient()

	parentJob := &models.Job{
		JobID:          "parent-id",
		UserID:         "user-123",
		QueueName:      "reactorcide-jobs",
		RunnerImage:    "default:image",
		TimeoutSeconds: 3600,
	}

	tp := NewTriggerProcessor(mockStore, mockCorndogs)
	err := tp.ProcessTriggers(context.Background(), tmpDir, parentJob)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if createCount != 2 {
		t.Errorf("expected 2 jobs created, got %d", createCount)
	}
	if mockCorndogs.GetSubmitTaskCallCount() != 2 {
		t.Errorf("expected 2 SubmitTask calls, got %d", mockCorndogs.GetSubmitTaskCallCount())
	}
}

func TestProcessTriggers_EnvVarOverride(t *testing.T) {
	tmpDir := t.TempDir()
	triggersData := triggersFile{
		Type: "trigger_job",
		Jobs: []triggerJobSpec{
			{
				JobName:    "test",
				JobCommand: "echo test",
				Env: map[string]string{
					"REACTORCIDE_EVENT_TYPE": "pull_request_opened",
					"CUSTOM_VAR":            "custom_value",
				},
			},
		},
	}
	writeTriggersFile(t, tmpDir, triggersData)

	var createdJob *models.Job
	mockStore := &MockStore{
		CreateJobFunc: func(ctx context.Context, job *models.Job) error {
			job.JobID = "test-id"
			createdJob = job
			return nil
		},
	}
	mockCorndogs := corndogs.NewMockClient()

	parentJob := &models.Job{
		JobID:          "parent-id",
		UserID:         "user-123",
		QueueName:      "reactorcide-jobs",
		RunnerImage:    "default:image",
		TimeoutSeconds: 3600,
		JobEnvVars: models.JSONB{
			"REACTORCIDE_CI":         "true",
			"REACTORCIDE_EVENT_TYPE": "push", // This should be overridden by trigger
		},
	}

	tp := NewTriggerProcessor(mockStore, mockCorndogs)
	err := tp.ProcessTriggers(context.Background(), tmpDir, parentJob)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if createdJob == nil {
		t.Fatal("expected job to be created")
	}

	// Trigger env var should override parent
	if createdJob.JobEnvVars["REACTORCIDE_EVENT_TYPE"] != "pull_request_opened" {
		t.Errorf("expected trigger env var to override parent, got %v", createdJob.JobEnvVars["REACTORCIDE_EVENT_TYPE"])
	}
	// Parent env var should be inherited
	if createdJob.JobEnvVars["REACTORCIDE_CI"] != "true" {
		t.Error("expected parent env var 'REACTORCIDE_CI' to be inherited")
	}
	// Trigger-specific env var should be present
	if createdJob.JobEnvVars["CUSTOM_VAR"] != "custom_value" {
		t.Error("expected trigger env var 'CUSTOM_VAR' to be set")
	}
}

func TestProcessTriggers_InheritsParentDefaults(t *testing.T) {
	tmpDir := t.TempDir()
	triggersData := triggersFile{
		Type: "trigger_job",
		Jobs: []triggerJobSpec{
			{
				JobName:    "test",
				JobCommand: "echo test",
				// No container_image, timeout, or priority specified
			},
		},
	}
	writeTriggersFile(t, tmpDir, triggersData)

	var createdJob *models.Job
	projectID := "project-123"
	mockStore := &MockStore{
		CreateJobFunc: func(ctx context.Context, job *models.Job) error {
			job.JobID = "test-id"
			createdJob = job
			return nil
		},
	}
	mockCorndogs := corndogs.NewMockClient()

	parentJob := &models.Job{
		JobID:          "parent-id",
		UserID:         "user-123",
		ProjectID:      &projectID,
		QueueName:      "custom-queue",
		RunnerImage:    "custom:runner",
		TimeoutSeconds: 7200,
		EventMetadata: models.JSONB{
			"event_type": "push",
			"repository": "org/repo",
		},
	}

	tp := NewTriggerProcessor(mockStore, mockCorndogs)
	err := tp.ProcessTriggers(context.Background(), tmpDir, parentJob)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if createdJob == nil {
		t.Fatal("expected job to be created")
	}

	if createdJob.RunnerImage != "custom:runner" {
		t.Errorf("expected runner image inherited from parent, got %q", createdJob.RunnerImage)
	}
	if createdJob.TimeoutSeconds != 7200 {
		t.Errorf("expected timeout inherited from parent, got %d", createdJob.TimeoutSeconds)
	}
	if createdJob.QueueName != "custom-queue" {
		t.Errorf("expected queue name inherited from parent, got %q", createdJob.QueueName)
	}
	if createdJob.ProjectID == nil || *createdJob.ProjectID != "project-123" {
		t.Error("expected project ID inherited from parent")
	}
	if createdJob.EventMetadata == nil {
		t.Error("expected event metadata copied from parent")
	} else if createdJob.EventMetadata["event_type"] != "push" {
		t.Error("expected event metadata to match parent")
	}
}

func TestProcessTriggers_NilCorndogsClient(t *testing.T) {
	tmpDir := t.TempDir()
	triggersData := triggersFile{
		Type: "trigger_job",
		Jobs: []triggerJobSpec{
			{
				JobName:    "test",
				JobCommand: "echo test",
			},
		},
	}
	writeTriggersFile(t, tmpDir, triggersData)

	createCount := 0
	mockStore := &MockStore{
		CreateJobFunc: func(ctx context.Context, job *models.Job) error {
			createCount++
			job.JobID = "test-id"
			return nil
		},
	}

	parentJob := &models.Job{
		JobID:          "parent-id",
		UserID:         "user-123",
		QueueName:      "reactorcide-jobs",
		RunnerImage:    "default:image",
		TimeoutSeconds: 3600,
	}

	tp := NewTriggerProcessor(mockStore, nil)
	err := tp.ProcessTriggers(context.Background(), tmpDir, parentJob)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if createCount != 1 {
		t.Errorf("expected 1 job created, got %d", createCount)
	}
}

func TestProcessTriggers_CornDogsSubmitError(t *testing.T) {
	tmpDir := t.TempDir()
	triggersData := triggersFile{
		Type: "trigger_job",
		Jobs: []triggerJobSpec{
			{
				JobName:    "test",
				JobCommand: "echo test",
			},
		},
	}
	writeTriggersFile(t, tmpDir, triggersData)

	var updatedJobs []models.Job
	mockStore := &MockStore{
		CreateJobFunc: func(ctx context.Context, job *models.Job) error {
			job.JobID = "test-id"
			return nil
		},
		UpdateJobFunc: func(ctx context.Context, job *models.Job) error {
			updatedJobs = append(updatedJobs, *job)
			return nil
		},
	}
	mockCorndogs := corndogs.NewMockClient()
	mockCorndogs.SubmitTaskFunc = func(ctx context.Context, payload *corndogs.TaskPayload, priority int64) (*pb.Task, error) {
		return nil, fmt.Errorf("corndogs unavailable")
	}

	parentJob := &models.Job{
		JobID:          "parent-id",
		UserID:         "user-123",
		QueueName:      "reactorcide-jobs",
		RunnerImage:    "default:image",
		TimeoutSeconds: 3600,
	}

	tp := NewTriggerProcessor(mockStore, mockCorndogs)
	err := tp.ProcessTriggers(context.Background(), tmpDir, parentJob)
	// ProcessTriggers should not return error for individual job failures
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Job should be updated with failed status
	if len(updatedJobs) != 1 {
		t.Fatalf("expected 1 job update, got %d", len(updatedJobs))
	}
	if updatedJobs[0].Status != "failed" {
		t.Errorf("expected job status 'failed', got %q", updatedJobs[0].Status)
	}
}

func TestProcessTriggers_TaskPayloadStructure(t *testing.T) {
	tmpDir := t.TempDir()
	priority := 15
	timeout := 900
	triggersData := triggersFile{
		Type: "trigger_job",
		Jobs: []triggerJobSpec{
			{
				JobName:        "deploy",
				ContainerImage: "deploy:v1",
				JobCommand:     "deploy.sh",
				SourceType:     "git",
				SourceURL:      "https://github.com/org/repo.git",
				SourceRef:      "v1.0.0",
				Priority:       &priority,
				Timeout:        &timeout,
				Env: map[string]string{
					"DEPLOY_ENV": "production",
				},
			},
		},
	}
	writeTriggersFile(t, tmpDir, triggersData)

	mockStore := &MockStore{
		CreateJobFunc: func(ctx context.Context, job *models.Job) error {
			job.JobID = "deploy-job-id"
			return nil
		},
	}
	mockCorndogs := corndogs.NewMockClient()

	parentJob := &models.Job{
		JobID:          "parent-id",
		UserID:         "user-456",
		QueueName:      "reactorcide-jobs",
		RunnerImage:    "default:image",
		TimeoutSeconds: 3600,
	}

	tp := NewTriggerProcessor(mockStore, mockCorndogs)
	err := tp.ProcessTriggers(context.Background(), tmpDir, parentJob)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if mockCorndogs.GetSubmitTaskCallCount() != 1 {
		t.Fatalf("expected 1 SubmitTask call, got %d", mockCorndogs.GetSubmitTaskCallCount())
	}

	call := mockCorndogs.SubmitTaskCalls[0]
	payload := call.Payload

	if payload.JobID != "deploy-job-id" {
		t.Errorf("expected job ID 'deploy-job-id', got %q", payload.JobID)
	}
	if payload.JobType != "run" {
		t.Errorf("expected job type 'run', got %q", payload.JobType)
	}

	// Verify config
	if payload.Config["image"] != "deploy:v1" {
		t.Errorf("expected image 'deploy:v1', got %v", payload.Config["image"])
	}
	if payload.Config["command"] != "deploy.sh" {
		t.Errorf("expected command 'deploy.sh', got %v", payload.Config["command"])
	}
	if payload.Config["timeout"] != 900 {
		t.Errorf("expected timeout 900, got %v", payload.Config["timeout"])
	}

	// Verify source
	if payload.Source["type"] != "git" {
		t.Errorf("expected source type 'git', got %v", payload.Source["type"])
	}
	if payload.Source["url"] != "https://github.com/org/repo.git" {
		t.Errorf("expected source URL, got %v", payload.Source["url"])
	}
	if payload.Source["ref"] != "v1.0.0" {
		t.Errorf("expected source ref 'v1.0.0', got %v", payload.Source["ref"])
	}

	// Verify metadata
	if payload.Metadata["user_id"] != "user-456" {
		t.Errorf("expected user_id 'user-456', got %v", payload.Metadata["user_id"])
	}
	if payload.Metadata["name"] != "deploy" {
		t.Errorf("expected name 'deploy', got %v", payload.Metadata["name"])
	}

	// Verify environment in config
	envVars, ok := payload.Config["environment"].(models.JSONB)
	if !ok {
		t.Fatal("expected environment in config to be JSONB")
	}
	if envVars["DEPLOY_ENV"] != "production" {
		t.Errorf("expected DEPLOY_ENV 'production', got %v", envVars["DEPLOY_ENV"])
	}
}

func TestProcessTriggersFromData_ReturnsJobIDs(t *testing.T) {
	priority := 5
	triggersData := triggersFile{
		Type: "trigger_job",
		Jobs: []triggerJobSpec{
			{
				JobName:        "test",
				ContainerImage: "alpine:latest",
				JobCommand:     "make test",
				Priority:       &priority,
			},
			{
				JobName:        "build",
				ContainerImage: "golang:1.21",
				JobCommand:     "make build",
				Priority:       &priority,
			},
		},
	}
	data, err := json.Marshal(triggersData)
	if err != nil {
		t.Fatal(err)
	}

	mockStore := &MockStore{
		CreateJobFunc: func(ctx context.Context, job *models.Job) error {
			job.JobID = fmt.Sprintf("job-%s", job.Name)
			return nil
		},
	}
	mockCorndogs := corndogs.NewMockClient()

	parentJob := &models.Job{
		JobID:          "parent-id",
		UserID:         "user-123",
		QueueName:      "reactorcide-jobs",
		RunnerImage:    "default:image",
		TimeoutSeconds: 3600,
	}

	tp := NewTriggerProcessor(mockStore, mockCorndogs)
	jobIDs, err := tp.ProcessTriggersFromData(context.Background(), data, parentJob)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(jobIDs) != 2 {
		t.Fatalf("expected 2 job IDs, got %d", len(jobIDs))
	}
	if jobIDs[0] != "job-test" {
		t.Errorf("expected first job ID 'job-test', got %q", jobIDs[0])
	}
	if jobIDs[1] != "job-build" {
		t.Errorf("expected second job ID 'job-build', got %q", jobIDs[1])
	}
}

func TestProcessTriggersFromData_InvalidJSON(t *testing.T) {
	mockStore := &MockStore{}
	mockCorndogs := corndogs.NewMockClient()
	tp := NewTriggerProcessor(mockStore, mockCorndogs)

	_, err := tp.ProcessTriggersFromData(context.Background(), []byte("not json"), &models.Job{})
	if err == nil {
		t.Error("expected error for invalid JSON, got nil")
	}
}

func TestProcessTriggersFromData_EmptyJobs(t *testing.T) {
	triggersData := triggersFile{
		Type: "trigger_job",
		Jobs: []triggerJobSpec{},
	}
	data, err := json.Marshal(triggersData)
	if err != nil {
		t.Fatal(err)
	}

	mockStore := &MockStore{}
	mockCorndogs := corndogs.NewMockClient()
	tp := NewTriggerProcessor(mockStore, mockCorndogs)

	jobIDs, err := tp.ProcessTriggersFromData(context.Background(), data, &models.Job{})
	if err != nil {
		t.Errorf("expected no error for empty jobs, got %v", err)
	}
	if len(jobIDs) != 0 {
		t.Errorf("expected 0 job IDs, got %d", len(jobIDs))
	}
	if mockCorndogs.GetSubmitTaskCallCount() != 0 {
		t.Error("expected no Corndogs calls for empty jobs")
	}
}

func TestProcessTriggersFromData_WrongType(t *testing.T) {
	triggersData := triggersFile{
		Type: "unknown_type",
		Jobs: []triggerJobSpec{{JobName: "test"}},
	}
	data, err := json.Marshal(triggersData)
	if err != nil {
		t.Fatal(err)
	}

	mockStore := &MockStore{}
	mockCorndogs := corndogs.NewMockClient()
	tp := NewTriggerProcessor(mockStore, mockCorndogs)

	_, err = tp.ProcessTriggersFromData(context.Background(), data, &models.Job{})
	if err == nil {
		t.Error("expected error for wrong type, got nil")
	}
}

func TestBuildJobFromTrigger_MinimalSpec(t *testing.T) {
	mockStore := &MockStore{}
	tp := NewTriggerProcessor(mockStore, nil)

	spec := triggerJobSpec{
		JobName: "minimal-job",
	}
	parentJob := &models.Job{
		JobID:          "parent-id",
		UserID:         "user-123",
		QueueName:      "reactorcide-jobs",
		RunnerImage:    "default:runner",
		TimeoutSeconds: 3600,
	}

	job := tp.buildJobFromTrigger(spec, parentJob)

	if job.Name != "minimal-job" {
		t.Errorf("expected name 'minimal-job', got %q", job.Name)
	}
	if job.RunnerImage != "default:runner" {
		t.Errorf("expected runner image from parent, got %q", job.RunnerImage)
	}
	if job.TimeoutSeconds != 3600 {
		t.Errorf("expected timeout from parent, got %d", job.TimeoutSeconds)
	}
	if job.Status != "submitted" {
		t.Errorf("expected status 'submitted', got %q", job.Status)
	}
	if job.ParentJobID == nil || *job.ParentJobID != "parent-id" {
		t.Error("expected parent job ID to be set")
	}
}

func TestBuildJobEnv_PassesAPICredentials(t *testing.T) {
	// Set up environment variables that the worker reads
	t.Setenv("REACTORCIDE_JOB_API_URL", "http://coordinator:6080")
	t.Setenv("REACTORCIDE_API_TOKEN", "test-api-token-123")

	jp := NewJobProcessor(&MockStore{}, nil, false)

	job := &models.Job{
		JobID:     "test-job",
		QueueName: "reactorcide-jobs",
	}

	env := jp.buildJobEnv(job)

	if env["REACTORCIDE_COORDINATOR_URL"] != "http://coordinator:6080" {
		t.Errorf("expected REACTORCIDE_COORDINATOR_URL to be set, got %q", env["REACTORCIDE_COORDINATOR_URL"])
	}
	if env["REACTORCIDE_API_TOKEN"] != "test-api-token-123" {
		t.Errorf("expected REACTORCIDE_API_TOKEN to be set, got %q", env["REACTORCIDE_API_TOKEN"])
	}
}

func TestBuildJobEnv_NoAPICredentials(t *testing.T) {
	// Ensure env vars are not set
	t.Setenv("REACTORCIDE_JOB_API_URL", "")
	t.Setenv("REACTORCIDE_API_TOKEN", "")

	jp := NewJobProcessor(&MockStore{}, nil, false)

	job := &models.Job{
		JobID:     "test-job",
		QueueName: "reactorcide-jobs",
	}

	env := jp.buildJobEnv(job)

	if _, ok := env["REACTORCIDE_COORDINATOR_URL"]; ok && env["REACTORCIDE_COORDINATOR_URL"] != "" {
		t.Errorf("expected REACTORCIDE_COORDINATOR_URL to not be set, got %q", env["REACTORCIDE_COORDINATOR_URL"])
	}
	if _, ok := env["REACTORCIDE_API_TOKEN"]; ok && env["REACTORCIDE_API_TOKEN"] != "" {
		t.Errorf("expected REACTORCIDE_API_TOKEN to not be set, got %q", env["REACTORCIDE_API_TOKEN"])
	}
}

func TestBuildJobFromTrigger_CopiesNotesFromParent(t *testing.T) {
	mockStore := &MockStore{}
	tp := NewTriggerProcessor(mockStore, nil)

	vcsNotes := `{"vcs_provider":"github","repo":"org/repo","commit_sha":"abc123","pr_number":42}`

	spec := triggerJobSpec{
		JobName:    "child-job",
		JobCommand: "make test",
	}
	parentJob := &models.Job{
		JobID:          "parent-id",
		UserID:         "user-123",
		QueueName:      "reactorcide-jobs",
		RunnerImage:    "default:runner",
		TimeoutSeconds: 3600,
		Notes:          vcsNotes,
	}

	job := tp.buildJobFromTrigger(spec, parentJob)

	if job.Notes != vcsNotes {
		t.Errorf("expected Notes to be copied from parent, got %q", job.Notes)
	}
}

func TestBuildJobFromTrigger_EmptyNotesNotCopied(t *testing.T) {
	mockStore := &MockStore{}
	tp := NewTriggerProcessor(mockStore, nil)

	spec := triggerJobSpec{
		JobName:    "child-job",
		JobCommand: "make test",
	}
	parentJob := &models.Job{
		JobID:          "parent-id",
		UserID:         "user-123",
		QueueName:      "reactorcide-jobs",
		RunnerImage:    "default:runner",
		TimeoutSeconds: 3600,
		Notes:          "",
	}

	job := tp.buildJobFromTrigger(spec, parentJob)

	if job.Notes != "" {
		t.Errorf("expected Notes to be empty when parent has no notes, got %q", job.Notes)
	}
}

func writeTriggersFile(t *testing.T, dir string, tf triggersFile) {
	t.Helper()
	data, err := json.Marshal(tf)
	if err != nil {
		t.Fatalf("failed to marshal triggers: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "triggers.json"), data, 0644); err != nil {
		t.Fatalf("failed to write triggers file: %v", err)
	}
}
