package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/characteristics"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/corndogs"
	pb "github.com/catalystcommunity/reactorcide/coordinator_api/internal/corndogs/v1alpha1"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/vcs"
)

func TestProcessTriggers_NoTriggersFile(t *testing.T) {
	mockStore := &MockStore{}
	mockCorndogs := corndogs.NewMockClient()
	tp := NewTriggerProcessor(mockStore, mockCorndogs)

	err := tp.ProcessTriggers(context.Background(), "/nonexistent/path", &models.Job{})
	if err != nil {
		t.Errorf("expected no error for missing triggers file, got %v", err)
	}

	if mockCorndogs.GetSubmitTaskCallCount() != 0 || len(mockCorndogs.SubmitTaskToQueueCalls) != 0 {
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

	if mockCorndogs.GetSubmitTaskCallCount() != 0 || len(mockCorndogs.SubmitTaskToQueueCalls) != 0 {
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

func TestValidateWorkflowAuthorityPinsExactOrigin(t *testing.T) {
	baseRepo, baseSHA := "https://example.test/upstream.git", "base-sha"
	headRepo, headSHA := "https://example.test/fork.git", "head-sha"
	parent := &models.Job{JobID: "eval", OrgID: "org", CIRepository: baseRepo, CISHA: baseSHA,
		CISourceURL: &baseRepo, CISourceRef: &baseSHA, SourceURL: &headRepo, SourceRef: &headSHA,
		ExecutionProfile: "standard"}
	if err := (&vcs.JobMetadata{VCSProvider: "github", Repo: "org/repo", CommitSHA: headSHA, IsEval: true}).ApplyToJob(parent); err != nil {
		t.Fatal(err)
	}
	tp := NewTriggerProcessor(&MockStore{}, nil)
	base := &triggerWorkflowSpec{ID: "tests", TriggerType: "runnerlib_eval", CIOrigin: "base",
		CIRepository: baseRepo, CISHA: baseSHA, ExecutionProfile: "standard", WorkerClass: "default"}
	if err := tp.validateWorkflowAuthority(context.Background(), parent, base); err != nil {
		t.Fatalf("base authority rejected: %v", err)
	}
	head := &triggerWorkflowSpec{ID: "tests", TriggerType: "runnerlib_eval", CIOrigin: "head",
		CIRepository: headRepo, CISHA: headSHA, ExecutionProfile: "pr-untrusted", WorkerClass: "default",
		PolicyRevision: "revision", PolicyRuleID: "backend"}
	if err := tp.validateWorkflowAuthority(context.Background(), parent, head); err != nil {
		t.Fatalf("head authority rejected: %v", err)
	}
	head.CISHA = "branch-name"
	if err := tp.validateWorkflowAuthority(context.Background(), parent, head); err == nil {
		t.Fatal("head workflow was not pinned to the exact event SHA")
	}
}

func TestEvalResultViolationOnlyDoesNotRequireWorkflow(t *testing.T) {
	parent := &models.Job{JobID: "eval", OrgID: "org"}
	tp := NewTriggerProcessor(&MockStore{}, nil)
	data := []byte(`{"type":"trigger_job","trigger_type":"runnerlib_eval","policy_violations":[{"path":".reactorcide/jobs/new.yaml"}]}`)
	created, err := tp.ProcessTriggersFromData(context.Background(), data, "", parent)
	if err != nil {
		t.Fatalf("violation-only result failed: %v", err)
	}
	if len(created) != 0 {
		t.Fatalf("expected no jobs, got %d", len(created))
	}
}

func TestRunnerlibEvalCrossLanguageFixture(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "testdata", "runnerlib_eval_trigger.json"))
	if err != nil {
		t.Fatal(err)
	}
	var envelope triggersFile
	if err := json.Unmarshal(data, &envelope); err != nil {
		t.Fatalf("Go could not parse the runnerlib protocol fixture: %v", err)
	}
	if envelope.TriggerType != "runnerlib_eval" || len(envelope.Workflows) != 1 || len(envelope.PolicyViolations) != 1 {
		t.Fatalf("unexpected runnerlib evaluation fixture: %+v", envelope)
	}
	workflow := envelope.Workflows[0]
	if workflow.ID != "backend-tests" || workflow.CIOrigin != "head" || workflow.CISHA != "head-sha" || len(workflow.Jobs) != 1 {
		t.Fatalf("workflow provenance did not survive JSON parsing: %+v", workflow)
	}
}

func TestTriggerPayloadRejectsVersionField(t *testing.T) {
	tp := NewTriggerProcessor(&MockStore{}, nil)
	data := []byte(`{"version":2,"type":"trigger_job","trigger_type":"runnerlib_eval"}`)
	if _, err := tp.ProcessTriggersFromData(context.Background(), data, "", &models.Job{JobID: "eval", OrgID: "org"}); err == nil {
		t.Fatal("versioned trigger payload was accepted")
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
					"REACTORCIDE_BRANCH":     "main",
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

	// Verify Corndogs submission -- queue-routed submit, not the legacy
	// SubmitTask.
	if len(mockCorndogs.SubmitTaskToQueueCalls) != 1 {
		t.Fatalf("expected 1 SubmitTaskToQueue call, got %d", len(mockCorndogs.SubmitTaskToQueueCalls))
	}

	submitCall := mockCorndogs.SubmitTaskToQueueCalls[0]
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
		OrgID:          "org-123",
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
	if len(mockCorndogs.SubmitTaskToQueueCalls) != 2 {
		t.Errorf("expected 2 SubmitTaskToQueue calls, got %d", len(mockCorndogs.SubmitTaskToQueueCalls))
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
					"CUSTOM_VAR":             "custom_value",
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
	mockCorndogs.SubmitTaskToQueueFunc = func(ctx context.Context, queue string, payload *corndogs.TaskPayload, priority int64) (*pb.Task, error) {
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

	if len(mockCorndogs.SubmitTaskToQueueCalls) != 1 {
		t.Fatalf("expected 1 SubmitTaskToQueue call, got %d", len(mockCorndogs.SubmitTaskToQueueCalls))
	}

	call := mockCorndogs.SubmitTaskToQueueCalls[0]
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
	jobIDs, err := tp.ProcessTriggersFromData(context.Background(), data, "", parentJob)
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

	_, err := tp.ProcessTriggersFromData(context.Background(), []byte("not json"), "", &models.Job{})
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

	jobIDs, err := tp.ProcessTriggersFromData(context.Background(), data, "", &models.Job{})
	if err != nil {
		t.Errorf("expected no error for empty jobs, got %v", err)
	}
	if len(jobIDs) != 0 {
		t.Errorf("expected 0 job IDs, got %d", len(jobIDs))
	}
	if mockCorndogs.GetSubmitTaskCallCount() != 0 || len(mockCorndogs.SubmitTaskToQueueCalls) != 0 {
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

	_, err = tp.ProcessTriggersFromData(context.Background(), data, "", &models.Job{})
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

	job, err := tp.buildJobFromTrigger(spec, parentJob)
	if err != nil {
		t.Fatalf("buildJobFromTrigger failed: %v", err)
	}

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
	if job.OrgID != parentJob.OrgID {
		t.Fatalf("child organization = %q, want inherited %q", job.OrgID, parentJob.OrgID)
	}
}

// TestBuildJobFromTrigger_InheritsParentCharacteristics verifies a triggered
// job with no `characteristics` block of its own inherits the parent (eval)
// job's characteristics wholesale.
func TestBuildJobFromTrigger_InheritsParentCharacteristics(t *testing.T) {
	mockStore := &MockStore{}
	tp := NewTriggerProcessor(mockStore, nil)

	parentChars, err := characteristics.ParseJobCharacteristics(map[string]any{"os": "windows", "arch": "amd64"})
	if err != nil {
		t.Fatalf("failed to build parent characteristics: %v", err)
	}
	parentJob := &models.Job{
		JobID:           "parent-id",
		UserID:          "user-123",
		QueueName:       "some-queue-uuid",
		RunnerImage:     "default:runner",
		TimeoutSeconds:  3600,
		Characteristics: parentChars,
	}

	spec := triggerJobSpec{JobName: "child-job"}

	job, err := tp.buildJobFromTrigger(spec, parentJob)
	if err != nil {
		t.Fatalf("buildJobFromTrigger failed: %v", err)
	}

	if characteristics.Hash(job.Characteristics) != characteristics.Hash(parentChars) {
		t.Errorf("expected child to inherit parent characteristics %s, got %s",
			characteristics.CanonicalString(parentChars), characteristics.CanonicalString(job.Characteristics))
	}
}

// TestBuildJobFromTrigger_OverridesParentCharacteristics verifies a
// triggered job whose spec declares its own `characteristics` block uses
// that instead of the parent's.
func TestBuildJobFromTrigger_OverridesParentCharacteristics(t *testing.T) {
	mockStore := &MockStore{}
	tp := NewTriggerProcessor(mockStore, nil)

	parentChars, err := characteristics.ParseJobCharacteristics(map[string]any{"os": "linux"})
	if err != nil {
		t.Fatalf("failed to build parent characteristics: %v", err)
	}
	parentJob := &models.Job{
		JobID:           "parent-id",
		UserID:          "user-123",
		QueueName:       "some-queue-uuid",
		RunnerImage:     "default:runner",
		TimeoutSeconds:  3600,
		Characteristics: parentChars,
	}

	spec := triggerJobSpec{
		JobName:         "child-job",
		Characteristics: map[string]interface{}{"os": "windows"},
	}

	job, err := tp.buildJobFromTrigger(spec, parentJob)
	if err != nil {
		t.Fatalf("buildJobFromTrigger failed: %v", err)
	}

	wantChars, err := characteristics.ParseJobCharacteristics(map[string]any{"os": "windows"})
	if err != nil {
		t.Fatalf("failed to build expected characteristics: %v", err)
	}
	if characteristics.Hash(job.Characteristics) != characteristics.Hash(wantChars) {
		t.Errorf("expected overridden characteristics %s, got %s",
			characteristics.CanonicalString(wantChars), characteristics.CanonicalString(job.Characteristics))
	}
}

// TestBuildJobFromTrigger_InvalidCharacteristicsErrors verifies a spec with
// an invalid `characteristics` block (a list value, which is rejected for
// job/queue characteristics) fails buildJobFromTrigger with an error rather
// than silently falling back.
func TestBuildJobFromTrigger_InvalidCharacteristicsErrors(t *testing.T) {
	mockStore := &MockStore{}
	tp := NewTriggerProcessor(mockStore, nil)

	parentJob := &models.Job{JobID: "parent-id", UserID: "user-123"}
	spec := triggerJobSpec{
		JobName:         "child-job",
		Characteristics: map[string]interface{}{"os": []interface{}{"linux", "windows"}},
	}

	if _, err := tp.buildJobFromTrigger(spec, parentJob); err == nil {
		t.Fatal("expected an error for a list-valued characteristic in a triggered job spec")
	}
}

// TestBuildJobFromTrigger_ResourcesOverride verifies a spec's `resources`
// block is parsed onto the child job when present, and left empty
// (DB-default territory) when absent.
func TestBuildJobFromTrigger_ResourcesOverride(t *testing.T) {
	mockStore := &MockStore{}
	tp := NewTriggerProcessor(mockStore, nil)
	parentJob := &models.Job{JobID: "parent-id", UserID: "user-123"}

	t.Run("no resources block leaves fields empty for DB defaults", func(t *testing.T) {
		spec := triggerJobSpec{JobName: "child-job"}
		job, err := tp.buildJobFromTrigger(spec, parentJob)
		if err != nil {
			t.Fatalf("buildJobFromTrigger failed: %v", err)
		}
		if job.ResourceCPURequest != "" || job.ResourceCPULimit != "" || job.ResourceMemoryLimit != "" {
			t.Errorf("expected empty resource fields, got %q/%q/%q",
				job.ResourceCPURequest, job.ResourceCPULimit, job.ResourceMemoryLimit)
		}
	})

	t.Run("resources block is parsed onto the child job", func(t *testing.T) {
		spec := triggerJobSpec{
			JobName: "child-job",
			Resources: map[string]interface{}{
				"cpu":    map[string]interface{}{"request": "2", "limit": "4"},
				"memory": map[string]interface{}{"limit": "8Gi"},
			},
		}
		job, err := tp.buildJobFromTrigger(spec, parentJob)
		if err != nil {
			t.Fatalf("buildJobFromTrigger failed: %v", err)
		}
		if job.ResourceCPURequest != "2" || job.ResourceCPULimit != "4" || job.ResourceMemoryLimit != "8Gi" {
			t.Errorf("expected resources 2/4/8Gi, got %q/%q/%q",
				job.ResourceCPURequest, job.ResourceCPULimit, job.ResourceMemoryLimit)
		}
	})
}

func TestBuildJobEnv_PassesAPICredentials(t *testing.T) {
	// Set up environment variables that the worker reads
	t.Setenv("REACTORCIDE_JOB_API_URL", "http://coordinator:6080")
	t.Setenv("REACTORCIDE_API_TOKEN", "test-api-token-123")

	job := &models.Job{
		JobID:     "test-job",
		QueueName: "reactorcide-jobs",
	}

	env := BuildJobEnv(job)

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

	job := &models.Job{
		JobID:     "test-job",
		QueueName: "reactorcide-jobs",
	}

	env := BuildJobEnv(job)

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

	job, err := tp.buildJobFromTrigger(spec, parentJob)
	if err != nil {
		t.Fatalf("buildJobFromTrigger failed: %v", err)
	}

	// Notes should be updated with StatusContext set to job name and IsEval cleared
	var metadata vcs.JobMetadata
	if err := json.Unmarshal([]byte(job.Notes), &metadata); err != nil {
		t.Fatalf("failed to parse job notes: %v", err)
	}
	if metadata.VCSProvider != "github" {
		t.Errorf("expected vcs_provider 'github', got %q", metadata.VCSProvider)
	}
	if metadata.Repo != "org/repo" {
		t.Errorf("expected repo 'org/repo', got %q", metadata.Repo)
	}
	if metadata.CommitSHA != "abc123" {
		t.Errorf("expected commit_sha 'abc123', got %q", metadata.CommitSHA)
	}
	if metadata.StatusContext != "child-job" {
		t.Errorf("expected status_context 'child-job', got %q", metadata.StatusContext)
	}
	if metadata.IsEval {
		t.Error("expected IsEval to be false")
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

	job, err := tp.buildJobFromTrigger(spec, parentJob)
	if err != nil {
		t.Fatalf("buildJobFromTrigger failed: %v", err)
	}

	if job.Notes != "" {
		t.Errorf("expected Notes to be empty when parent has no notes, got %q", job.Notes)
	}
}

func TestProcessTriggers_JobFile(t *testing.T) {
	tmpDir := t.TempDir()

	// Create workspace with a job definition file
	jobDir := filepath.Join(tmpDir, "src", ".reactorcide", "jobs")
	if err := os.MkdirAll(jobDir, 0755); err != nil {
		t.Fatal(err)
	}

	timeout := 1800
	priority := 10
	jobYAML := `name: test-go
description: "Run Go tests"
job:
  image: golang:1.25-alpine
  command: |
    set -e
    go test ./... -count=1
  timeout: 1800
  priority: 10
  raw_command: true
`
	if err := os.WriteFile(filepath.Join(jobDir, "test-go.yaml"), []byte(jobYAML), 0644); err != nil {
		t.Fatal(err)
	}

	// Write triggers.json with job_file reference
	triggersData := triggersFile{
		Type: "trigger_job",
		Jobs: []triggerJobSpec{
			{
				JobFile: ".reactorcide/jobs/test-go.yaml",
			},
		},
	}
	writeTriggersFile(t, tmpDir, triggersData)

	var createdJob *models.Job
	mockStore := &MockStore{
		CreateJobFunc: func(ctx context.Context, job *models.Job) error {
			job.JobID = "generated-job-id"
			createdJob = job
			return nil
		},
	}
	mockCorndogs := corndogs.NewMockClient()

	parentJob := &models.Job{
		JobID:          "parent-job-id",
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

	if createdJob == nil {
		t.Fatal("expected job to be created")
	}

	if createdJob.Name != "test-go" {
		t.Errorf("expected job name 'test-go', got %q", createdJob.Name)
	}
	if createdJob.RunnerImage != "golang:1.25-alpine" {
		t.Errorf("expected runner image 'golang:1.25-alpine', got %q", createdJob.RunnerImage)
	}
	if createdJob.TimeoutSeconds != timeout {
		t.Errorf("expected timeout %d, got %d", timeout, createdJob.TimeoutSeconds)
	}
	if createdJob.Priority != priority {
		t.Errorf("expected priority %d, got %d", priority, createdJob.Priority)
	}
	if createdJob.JobCommand == "" {
		t.Error("expected job command to be set from YAML")
	}
}

func TestProcessTriggersFromData_JobFileRequiresWorkspace(t *testing.T) {
	triggersData := triggersFile{
		Type: "trigger_job",
		Jobs: []triggerJobSpec{
			{JobFile: ".reactorcide/jobs/test-go.yaml"},
		},
	}
	data, err := json.Marshal(triggersData)
	if err != nil {
		t.Fatal(err)
	}

	mockStore := &MockStore{}
	mockCorndogs := corndogs.NewMockClient()
	parentJob := &models.Job{
		JobID:          "parent-job-id",
		UserID:         "user-123",
		QueueName:      "reactorcide-jobs",
		RunnerImage:    "default:image",
		TimeoutSeconds: 3600,
	}

	tp := NewTriggerProcessor(mockStore, mockCorndogs)
	_, err = tp.ProcessTriggersFromData(context.Background(), data, "", parentJob)
	if err == nil {
		t.Fatal("expected job_file without workspace to fail")
	}
	if !strings.Contains(err.Error(), "requires workspace-backed trigger processing") {
		t.Fatalf("expected workspace-backed error, got %v", err)
	}
	if mockCorndogs.GetSubmitTaskCallCount() != 0 || len(mockCorndogs.SubmitTaskToQueueCalls) != 0 {
		t.Error("expected no Corndogs calls when job_file cannot be resolved")
	}
}

func TestProcessTriggers_JobFileWithOverrides(t *testing.T) {
	tmpDir := t.TempDir()

	// Create workspace with a job definition file
	jobDir := filepath.Join(tmpDir, "src", ".reactorcide", "jobs")
	if err := os.MkdirAll(jobDir, 0755); err != nil {
		t.Fatal(err)
	}

	jobYAML := `name: test-go
description: "Run Go tests"
job:
  image: golang:1.25-alpine
  command: go test ./...
  timeout: 1800
  priority: 10
environment:
  GO_ENV: test
`
	if err := os.WriteFile(filepath.Join(jobDir, "test-go.yaml"), []byte(jobYAML), 0644); err != nil {
		t.Fatal(err)
	}

	// Triggers with inline overrides
	overrideTimeout := 900
	triggersData := triggersFile{
		Type: "trigger_job",
		Jobs: []triggerJobSpec{
			{
				JobFile: ".reactorcide/jobs/test-go.yaml",
				Timeout: &overrideTimeout,
				Env: map[string]string{
					"EXTRA_VAR": "extra_value",
				},
			},
		},
	}
	writeTriggersFile(t, tmpDir, triggersData)

	var createdJob *models.Job
	mockStore := &MockStore{
		CreateJobFunc: func(ctx context.Context, job *models.Job) error {
			job.JobID = "generated-job-id"
			createdJob = job
			return nil
		},
	}
	mockCorndogs := corndogs.NewMockClient()

	parentJob := &models.Job{
		JobID:          "parent-job-id",
		UserID:         "user-123",
		QueueName:      "reactorcide-jobs",
		RunnerImage:    "default:image",
		TimeoutSeconds: 3600,
		JobEnvVars: models.JSONB{
			"PARENT_VAR": "parent_value",
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

	// Name should come from YAML (no override specified)
	if createdJob.Name != "test-go" {
		t.Errorf("expected job name 'test-go', got %q", createdJob.Name)
	}
	// Image should come from YAML (no override specified)
	if createdJob.RunnerImage != "golang:1.25-alpine" {
		t.Errorf("expected runner image 'golang:1.25-alpine', got %q", createdJob.RunnerImage)
	}
	// Timeout should be overridden by inline spec
	if createdJob.TimeoutSeconds != 900 {
		t.Errorf("expected timeout 900 (overridden), got %d", createdJob.TimeoutSeconds)
	}

	// Env should merge: parent → YAML → inline trigger spec
	if createdJob.JobEnvVars["PARENT_VAR"] != "parent_value" {
		t.Error("expected parent env var 'PARENT_VAR' to be inherited")
	}
	if createdJob.JobEnvVars["GO_ENV"] != "test" {
		t.Error("expected YAML env var 'GO_ENV' to be present")
	}
	if createdJob.JobEnvVars["EXTRA_VAR"] != "extra_value" {
		t.Error("expected inline trigger env var 'EXTRA_VAR' to be present")
	}
}

func TestProcessTriggers_JobFileMissing(t *testing.T) {
	tmpDir := t.TempDir()

	// Create src dir but no job file
	if err := os.MkdirAll(filepath.Join(tmpDir, "src"), 0755); err != nil {
		t.Fatal(err)
	}

	triggersData := triggersFile{
		Type: "trigger_job",
		Jobs: []triggerJobSpec{
			{
				JobFile: ".reactorcide/jobs/nonexistent.yaml",
			},
		},
	}
	writeTriggersFile(t, tmpDir, triggersData)

	mockStore := &MockStore{}
	mockCorndogs := corndogs.NewMockClient()

	parentJob := &models.Job{
		JobID:          "parent-job-id",
		UserID:         "user-123",
		QueueName:      "reactorcide-jobs",
		RunnerImage:    "default:image",
		TimeoutSeconds: 3600,
	}

	tp := NewTriggerProcessor(mockStore, mockCorndogs)
	err := tp.ProcessTriggers(context.Background(), tmpDir, parentJob)
	// Should not return error — missing job file is logged and skipped
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// No jobs should be created
	if mockCorndogs.GetSubmitTaskCallCount() != 0 || len(mockCorndogs.SubmitTaskToQueueCalls) != 0 {
		t.Error("expected no Corndogs calls when job file is missing")
	}
}

func TestProcessTriggers_JobFileInvalidYAML(t *testing.T) {
	tmpDir := t.TempDir()

	// Create workspace with an invalid YAML job definition
	jobDir := filepath.Join(tmpDir, "src", ".reactorcide", "jobs")
	if err := os.MkdirAll(jobDir, 0755); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(jobDir, "bad.yaml"), []byte("not: valid: yaml: [[["), 0644); err != nil {
		t.Fatal(err)
	}

	triggersData := triggersFile{
		Type: "trigger_job",
		Jobs: []triggerJobSpec{
			{
				JobFile: ".reactorcide/jobs/bad.yaml",
			},
		},
	}
	writeTriggersFile(t, tmpDir, triggersData)

	mockStore := &MockStore{}
	mockCorndogs := corndogs.NewMockClient()

	parentJob := &models.Job{
		JobID:          "parent-job-id",
		UserID:         "user-123",
		QueueName:      "reactorcide-jobs",
		RunnerImage:    "default:image",
		TimeoutSeconds: 3600,
	}

	tp := NewTriggerProcessor(mockStore, mockCorndogs)
	err := tp.ProcessTriggers(context.Background(), tmpDir, parentJob)
	// Should not return error — invalid YAML is logged and skipped
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if mockCorndogs.GetSubmitTaskCallCount() != 0 || len(mockCorndogs.SubmitTaskToQueueCalls) != 0 {
		t.Error("expected no Corndogs calls when job file has invalid YAML")
	}
}

func TestProcessTriggers_JobFileEnvMerge(t *testing.T) {
	tmpDir := t.TempDir()

	// Create workspace with a job definition file that has environment vars
	jobDir := filepath.Join(tmpDir, "src", ".reactorcide", "jobs")
	if err := os.MkdirAll(jobDir, 0755); err != nil {
		t.Fatal(err)
	}

	jobYAML := `name: test-env
description: "Test env merging"
job:
  image: alpine:latest
  command: echo test
  timeout: 600
environment:
  YAML_VAR: yaml_value
  SHARED_VAR: from_yaml
`
	if err := os.WriteFile(filepath.Join(jobDir, "test-env.yaml"), []byte(jobYAML), 0644); err != nil {
		t.Fatal(err)
	}

	triggersData := triggersFile{
		Type: "trigger_job",
		Jobs: []triggerJobSpec{
			{
				JobFile: ".reactorcide/jobs/test-env.yaml",
				Env: map[string]string{
					"INLINE_VAR": "inline_value",
					"SHARED_VAR": "from_inline", // Should override YAML value
				},
			},
		},
	}
	writeTriggersFile(t, tmpDir, triggersData)

	var createdJob *models.Job
	mockStore := &MockStore{
		CreateJobFunc: func(ctx context.Context, job *models.Job) error {
			job.JobID = "generated-job-id"
			createdJob = job
			return nil
		},
	}
	mockCorndogs := corndogs.NewMockClient()

	parentJob := &models.Job{
		JobID:          "parent-job-id",
		UserID:         "user-123",
		QueueName:      "reactorcide-jobs",
		RunnerImage:    "default:image",
		TimeoutSeconds: 3600,
		JobEnvVars: models.JSONB{
			"PARENT_VAR": "parent_value",
			"SHARED_VAR": "from_parent", // Should be overridden by YAML then by inline
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

	// Parent env var should be inherited
	if createdJob.JobEnvVars["PARENT_VAR"] != "parent_value" {
		t.Errorf("expected PARENT_VAR 'parent_value', got %v", createdJob.JobEnvVars["PARENT_VAR"])
	}
	// YAML env var should be present
	if createdJob.JobEnvVars["YAML_VAR"] != "yaml_value" {
		t.Errorf("expected YAML_VAR 'yaml_value', got %v", createdJob.JobEnvVars["YAML_VAR"])
	}
	// Inline env var should be present
	if createdJob.JobEnvVars["INLINE_VAR"] != "inline_value" {
		t.Errorf("expected INLINE_VAR 'inline_value', got %v", createdJob.JobEnvVars["INLINE_VAR"])
	}
	// Inline should win over YAML which wins over parent
	if createdJob.JobEnvVars["SHARED_VAR"] != "from_inline" {
		t.Errorf("expected SHARED_VAR 'from_inline' (inline wins), got %v", createdJob.JobEnvVars["SHARED_VAR"])
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
