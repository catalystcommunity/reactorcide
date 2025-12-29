package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/checkauth"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/config"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/corndogs"
	pb "github.com/catalystcommunity/reactorcide/coordinator_api/internal/corndogs/v1alpha1"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/google/uuid"
)

// MockStore implements store.Store for testing
type MockStore struct {
	CreateJobFunc  func(ctx context.Context, job *models.Job) error
	GetJobByIDFunc func(ctx context.Context, jobID string) (*models.Job, error)
	UpdateJobFunc  func(ctx context.Context, job *models.Job) error
	ListJobsFunc   func(ctx context.Context, filters map[string]interface{}, limit, offset int) ([]models.Job, error)
	DeleteJobFunc  func(ctx context.Context, jobID string) error

	// Track calls
	CreateJobCalls  []models.Job
	UpdateJobCalls  []models.Job
	GetJobByIDCalls []string
}

func (m *MockStore) CreateJob(ctx context.Context, job *models.Job) error {
	m.CreateJobCalls = append(m.CreateJobCalls, *job)
	if m.CreateJobFunc != nil {
		return m.CreateJobFunc(ctx, job)
	}
	// Default behavior - generate a job ID
	if job.JobID == "" {
		job.JobID = uuid.New().String()
	}
	return nil
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

func (m *MockStore) ListJobs(ctx context.Context, filters map[string]interface{}, limit, offset int) ([]models.Job, error) {
	if m.ListJobsFunc != nil {
		return m.ListJobsFunc(ctx, filters, limit, offset)
	}
	return []models.Job{}, nil
}

func (m *MockStore) DeleteJob(ctx context.Context, jobID string) error {
	if m.DeleteJobFunc != nil {
		return m.DeleteJobFunc(ctx, jobID)
	}
	return nil
}

// Implement other required store.Store methods with minimal functionality
func (m *MockStore) Initialize() (func(), error)                             { return nil, nil }
func (m *MockStore) HealthCheck() error                                      { return nil }
func (m *MockStore) Begin(ctx context.Context) context.Context               { return ctx }
func (m *MockStore) Commit(ctx context.Context) error                        { return nil }
func (m *MockStore) Rollback(ctx context.Context) error                      { return nil }
func (m *MockStore) GetContext(ctx context.Context) interface{}              { return nil }
func (m *MockStore) EnsureDefaultUser() error                                { return nil }
func (m *MockStore) CreateUser(ctx context.Context, user *models.User) error { return nil }
func (m *MockStore) GetUserByID(ctx context.Context, userID string) (*models.User, error) {
	return nil, nil
}
func (m *MockStore) GetUserByAPIToken(ctx context.Context, token string) (*models.User, error) {
	return nil, nil
}
func (m *MockStore) UpdateUser(ctx context.Context, user *models.User) error          { return nil }
func (m *MockStore) DeleteUser(ctx context.Context, userID string) error              { return nil }
func (m *MockStore) CreateAPIToken(ctx context.Context, token *models.APIToken) error { return nil }
func (m *MockStore) GetAPITokenByID(ctx context.Context, tokenID string) (*models.APIToken, error) {
	return nil, nil
}
func (m *MockStore) GetAPITokenByToken(ctx context.Context, token string) (*models.APIToken, error) {
	return nil, nil
}
func (m *MockStore) ListAPITokens(ctx context.Context, userID string) ([]models.APIToken, error) {
	return nil, nil
}
func (m *MockStore) DeleteAPIToken(ctx context.Context, tokenID string) error { return nil }
func (m *MockStore) ValidateAPIToken(ctx context.Context, token string) (*models.APIToken, *models.User, error) {
	return nil, nil, nil
}
func (m *MockStore) UpdateTokenLastUsed(ctx context.Context, tokenID string, lastUsed time.Time) error {
	return nil
}
func (m *MockStore) GetAPITokensByUser(ctx context.Context, userID string) ([]models.APIToken, error) {
	return nil, nil
}
func (m *MockStore) GetJobsByUser(ctx context.Context, userID string, limit, offset int) ([]models.Job, error) {
	return nil, nil
}

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

func TestJobHandler_CreateJob_WithCorndogs(t *testing.T) {
	tests := []struct {
		name                  string
		request               CreateJobRequest
		setupMockStore        func(*MockStore)
		setupMockCorndogs     func(*corndogs.MockClient)
		expectedStatus        int
		expectedCorndogsCalls int
		checkResponse         func(*testing.T, JobResponse)
	}{
		{
			name: "successful job creation with Corndogs submission",
			request: CreateJobRequest{
				Name:       "Test Job",
				JobCommand: "echo hello",
				SourceType: "git",
				SourceURL:     "https://github.com/test/repo.git",
			},
			setupMockStore: func(m *MockStore) {
				m.CreateJobFunc = func(ctx context.Context, job *models.Job) error {
					job.JobID = "test-job-id"
					return nil
				}
				m.UpdateJobFunc = func(ctx context.Context, job *models.Job) error {
					return nil
				}
			},
			setupMockCorndogs: func(m *corndogs.MockClient) {
				m.SubmitTaskFunc = func(ctx context.Context, payload *corndogs.TaskPayload, priority int64) (*pb.Task, error) {
					return &pb.Task{
						Uuid:         "corndogs-task-id",
						CurrentState: "submitted",
					}, nil
				}
			},
			expectedStatus:        http.StatusCreated,
			expectedCorndogsCalls: 1,
			checkResponse: func(t *testing.T, resp JobResponse) {
				if resp.JobID != "test-job-id" {
					t.Errorf("expected job ID 'test-job-id', got %s", resp.JobID)
				}
				if resp.Status != "submitted" {
					t.Errorf("expected status 'submitted', got %s", resp.Status)
				}
			},
		},
		{
			name: "job creation succeeds even if Corndogs submission fails",
			request: CreateJobRequest{
				Name:       "Test Job",
				JobCommand: "echo hello",
				SourceType: "git",
				SourceURL:     "https://github.com/test/repo.git",
			},
			setupMockStore: func(m *MockStore) {
				m.CreateJobFunc = func(ctx context.Context, job *models.Job) error {
					job.JobID = "test-job-id"
					return nil
				}
				m.UpdateJobFunc = func(ctx context.Context, job *models.Job) error {
					return nil
				}
			},
			setupMockCorndogs: func(m *corndogs.MockClient) {
				m.SubmitTaskFunc = func(ctx context.Context, payload *corndogs.TaskPayload, priority int64) (*pb.Task, error) {
					return nil, fmt.Errorf("corndogs error")
				}
			},
			expectedStatus:        http.StatusCreated,
			expectedCorndogsCalls: 1,
			checkResponse: func(t *testing.T, resp JobResponse) {
				if resp.Status != "failed" {
					t.Errorf("expected status 'failed', got %s", resp.Status)
				}
			},
		},
		{
			name: "job creation without Corndogs client",
			request: CreateJobRequest{
				Name:       "Test Job",
				JobCommand: "echo hello",
				SourceType: "git",
				SourceURL:     "https://github.com/test/repo.git",
			},
			setupMockStore: func(m *MockStore) {
				m.CreateJobFunc = func(ctx context.Context, job *models.Job) error {
					job.JobID = "test-job-id"
					return nil
				}
			},
			setupMockCorndogs:     nil, // No Corndogs client
			expectedStatus:        http.StatusCreated,
			expectedCorndogsCalls: 0,
			checkResponse: func(t *testing.T, resp JobResponse) {
				if resp.Status != "submitted" {
					t.Errorf("expected status 'submitted', got %s", resp.Status)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Setup mocks
			mockStore := &MockStore{}
			if tt.setupMockStore != nil {
				tt.setupMockStore(mockStore)
			}

			var corndogsClient corndogs.ClientInterface
			var mockCorndogs *corndogs.MockClient
			if tt.setupMockCorndogs != nil {
				mockCorndogs = corndogs.NewMockClient()
				tt.setupMockCorndogs(mockCorndogs)
				corndogsClient = mockCorndogs
			}

			// Create handler
			handler := NewJobHandler(mockStore, corndogsClient)

			// Create request
			body, _ := json.Marshal(tt.request)
			req := httptest.NewRequest("POST", "/api/v1/jobs", bytes.NewReader(body))

			// Add user to context
			user := &models.User{UserID: "test-user-id"}
			ctx := checkauth.SetUserContext(req.Context(), user)
			req = req.WithContext(ctx)

			// Execute request
			w := httptest.NewRecorder()
			handler.CreateJob(w, req)

			// Check status code
			if w.Code != tt.expectedStatus {
				t.Errorf("expected status %d, got %d", tt.expectedStatus, w.Code)
			}

			// Check Corndogs calls
			if mockCorndogs != nil && mockCorndogs.GetSubmitTaskCallCount() != tt.expectedCorndogsCalls {
				t.Errorf("expected %d Corndogs calls, got %d", tt.expectedCorndogsCalls, mockCorndogs.GetSubmitTaskCallCount())
			}

			// Check response
			if tt.checkResponse != nil && w.Code == http.StatusCreated {
				var resp JobResponse
				if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
					t.Fatalf("failed to decode response: %v", err)
				}
				tt.checkResponse(t, resp)
			}
		})
	}
}

func TestJobHandler_CancelJob_WithCorndogs(t *testing.T) {
	tests := []struct {
		name                  string
		jobID                 string
		setupMockStore        func(*MockStore)
		setupMockCorndogs     func(*corndogs.MockClient)
		expectedStatus        int
		expectedCorndogsCalls int
	}{
		{
			name:  "successful job cancellation with Corndogs",
			jobID: "test-job-id",
			setupMockStore: func(m *MockStore) {
				taskID := "corndogs-task-id"
				m.GetJobByIDFunc = func(ctx context.Context, jobID string) (*models.Job, error) {
					return &models.Job{
						JobID:          jobID,
						Status:         "running",
						CorndogsTaskID: &taskID,
						UserID:         "test-user-id",
					}, nil
				}
				m.UpdateJobFunc = func(ctx context.Context, job *models.Job) error {
					return nil
				}
			},
			setupMockCorndogs: func(m *corndogs.MockClient) {
				m.CancelTaskFunc = func(ctx context.Context, taskID string, currentState string) (*pb.Task, error) {
					return &pb.Task{
						Uuid:         taskID,
						CurrentState: "cancelled",
					}, nil
				}
			},
			expectedStatus:        http.StatusOK,
			expectedCorndogsCalls: 1,
		},
		{
			name:  "job cancellation continues even if Corndogs fails",
			jobID: "test-job-id",
			setupMockStore: func(m *MockStore) {
				taskID := "corndogs-task-id"
				m.GetJobByIDFunc = func(ctx context.Context, jobID string) (*models.Job, error) {
					return &models.Job{
						JobID:          jobID,
						Status:         "running",
						CorndogsTaskID: &taskID,
						UserID:         "test-user-id",
					}, nil
				}
				m.UpdateJobFunc = func(ctx context.Context, job *models.Job) error {
					return nil
				}
			},
			setupMockCorndogs: func(m *corndogs.MockClient) {
				m.CancelTaskFunc = func(ctx context.Context, taskID string, currentState string) (*pb.Task, error) {
					return nil, fmt.Errorf("corndogs error")
				}
			},
			expectedStatus:        http.StatusOK,
			expectedCorndogsCalls: 1,
		},
		{
			name:  "job cancellation without Corndogs task ID",
			jobID: "test-job-id",
			setupMockStore: func(m *MockStore) {
				m.GetJobByIDFunc = func(ctx context.Context, jobID string) (*models.Job, error) {
					return &models.Job{
						JobID:          jobID,
						Status:         "running",
						CorndogsTaskID: nil, // No Corndogs task
						UserID:         "test-user-id",
					}, nil
				}
				m.UpdateJobFunc = func(ctx context.Context, job *models.Job) error {
					return nil
				}
			},
			setupMockCorndogs: func(m *corndogs.MockClient) {
				// Should not be called
			},
			expectedStatus:        http.StatusOK,
			expectedCorndogsCalls: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Setup mocks
			mockStore := &MockStore{}
			if tt.setupMockStore != nil {
				tt.setupMockStore(mockStore)
			}

			var corndogsClient corndogs.ClientInterface
			var mockCorndogs *corndogs.MockClient
			if tt.setupMockCorndogs != nil {
				mockCorndogs = corndogs.NewMockClient()
				tt.setupMockCorndogs(mockCorndogs)
				corndogsClient = mockCorndogs
			}

			// Create handler
			handler := NewJobHandler(mockStore, corndogsClient)

			// Create request
			req := httptest.NewRequest("PUT", fmt.Sprintf("/api/v1/jobs/%s/cancel", tt.jobID), nil)

			// Add user to context
			user := &models.User{UserID: "test-user-id"}
			ctx := checkauth.SetUserContext(req.Context(), user)
			ctx = context.WithValue(ctx, GetContextKey("job_id"), tt.jobID)
			req = req.WithContext(ctx)

			// Execute request
			w := httptest.NewRecorder()
			handler.CancelJob(w, req)

			// Check status code
			if w.Code != tt.expectedStatus {
				t.Errorf("expected status %d, got %d", tt.expectedStatus, w.Code)
			}

			// Check Corndogs calls
			if mockCorndogs != nil && mockCorndogs.GetCancelTaskCallCount() != tt.expectedCorndogsCalls {
				t.Errorf("expected %d Corndogs cancel calls, got %d", tt.expectedCorndogsCalls, mockCorndogs.GetCancelTaskCallCount())
			}

			// Verify job was updated to cancelled
			if len(mockStore.UpdateJobCalls) > 0 {
				lastUpdate := mockStore.UpdateJobCalls[len(mockStore.UpdateJobCalls)-1]
				if lastUpdate.Status != "cancelled" {
					t.Errorf("expected job status to be 'cancelled', got %s", lastUpdate.Status)
				}
			}
		})
	}
}

func TestJobHandler_CorndogsPayloadGeneration(t *testing.T) {
	// This test verifies that the payload sent to Corndogs is correct
	mockStore := &MockStore{}
	mockStore.CreateJobFunc = func(ctx context.Context, job *models.Job) error {
		job.JobID = "test-job-id"
		return nil
	}

	mockCorndogs := corndogs.NewMockClient()
	var capturedPayload *corndogs.TaskPayload
	mockCorndogs.SubmitTaskFunc = func(ctx context.Context, payload *corndogs.TaskPayload, priority int64) (*pb.Task, error) {
		capturedPayload = payload
		return &pb.Task{
			Uuid:         "task-id",
			CurrentState: "submitted",
		}, nil
	}

	handler := NewJobHandler(mockStore, corndogs.ClientInterface(mockCorndogs))

	request := CreateJobRequest{
		Name:        "Test Job",
		Description: "Test Description",
		JobCommand:  "echo hello",
		SourceType:  "git",
		SourceURL:      "https://github.com/test/repo.git",
		SourceRef:      "main",
		JobEnvVars: map[string]string{
			"KEY1": "value1",
			"KEY2": "value2",
		},
		TimeoutSeconds: intPtr(1800),
		Priority:       intPtr(5),
	}

	body, _ := json.Marshal(request)
	req := httptest.NewRequest("POST", "/api/v1/jobs", bytes.NewReader(body))

	user := &models.User{UserID: "test-user-id"}
	ctx := checkauth.SetUserContext(req.Context(), user)
	req = req.WithContext(ctx)

	w := httptest.NewRecorder()
	handler.CreateJob(w, req)

	// Verify the payload
	if capturedPayload == nil {
		t.Fatal("no payload was captured")
	}

	if capturedPayload.JobID != "test-job-id" {
		t.Errorf("expected job ID 'test-job-id', got %s", capturedPayload.JobID)
	}

	if capturedPayload.JobType != "run" {
		t.Errorf("expected job type 'run', got %s", capturedPayload.JobType)
	}

	// Check config
	if capturedPayload.Config["command"] != "echo hello" {
		t.Errorf("expected command 'echo hello', got %v", capturedPayload.Config["command"])
	}

	if capturedPayload.Config["timeout"] != 1800 {
		t.Errorf("expected timeout 1800, got %v", capturedPayload.Config["timeout"])
	}

	// Check environment variables
	t.Logf("Config: %+v", capturedPayload.Config)
	if env := capturedPayload.Config["environment"]; env != nil {
		// The environment is a JSONB (map[string]interface{})
		if envMap, ok := env.(models.JSONB); ok {
			if envMap["KEY1"] != "value1" {
				t.Errorf("expected KEY1='value1', got %v", envMap["KEY1"])
			}
		} else if envMap, ok := env.(map[string]interface{}); ok {
			if envMap["KEY1"] != "value1" {
				t.Errorf("expected KEY1='value1', got %v", envMap["KEY1"])
			}
		} else {
			t.Errorf("environment has unexpected type: %T", env)
		}
	} else {
		t.Error("environment variables not set in payload")
	}

	// Check source
	if capturedPayload.Source["type"] != "git" {
		t.Errorf("expected source type 'git', got %v", capturedPayload.Source["type"])
	}

	if capturedPayload.Source["url"] != "https://github.com/test/repo.git" {
		t.Errorf("expected git URL, got %v", capturedPayload.Source["url"])
	}

	// Check metadata
	if capturedPayload.Metadata["user_id"] != "test-user-id" {
		t.Errorf("expected user_id 'test-user-id', got %v", capturedPayload.Metadata["user_id"])
	}
}

// Helper function to create int pointer
func intPtr(i int) *int {
	return &i
}

// TestJobHandler_CICodeAllowlist tests the CI code allowlist enforcement
func TestJobHandler_CICodeAllowlist(t *testing.T) {
	// Import config to set/unset allowlist
	originalAllowlist := config.CiCodeAllowlist
	defer func() {
		config.CiCodeAllowlist = originalAllowlist
	}()

	tests := []struct {
		name           string
		allowlist      string // Set in config before test
		request        CreateJobRequest
		setupMockStore func(*MockStore)
		expectedStatus int
		errorContains  string // If non-empty, check error response contains this string
	}{
		{
			name:      "job without CI source - allowlist not enforced",
			allowlist: "github.com/trusted/ci-repo",
			request: CreateJobRequest{
				Name:       "Test Job",
				JobCommand: "echo hello",
				SourceType: "git",
				SourceURL:  "https://github.com/untrusted/source.git",
				// No CI source fields
			},
			setupMockStore: func(m *MockStore) {
				m.CreateJobFunc = func(ctx context.Context, job *models.Job) error {
					job.JobID = "test-job-id"
					return nil
				}
			},
			expectedStatus: http.StatusCreated,
		},
		{
			name:      "job with CI source in allowlist - succeeds",
			allowlist: "github.com/trusted/ci-repo",
			request: CreateJobRequest{
				Name:         "Test Job",
				JobCommand:   "echo hello",
				SourceType:   "git",
				SourceURL:    "https://github.com/untrusted/source.git",
				CISourceType: "git",
				CISourceURL:  "https://github.com/trusted/ci-repo.git",
				CISourceRef:  "main",
			},
			setupMockStore: func(m *MockStore) {
				m.CreateJobFunc = func(ctx context.Context, job *models.Job) error {
					job.JobID = "test-job-id"
					return nil
				}
			},
			expectedStatus: http.StatusCreated,
		},
		{
			name:      "job with CI source NOT in allowlist - returns 403",
			allowlist: "github.com/trusted/ci-repo",
			request: CreateJobRequest{
				Name:         "Test Job",
				JobCommand:   "echo hello",
				SourceType:   "git",
				SourceURL:    "https://github.com/untrusted/source.git",
				CISourceType: "git",
				CISourceURL:  "https://github.com/malicious/ci-repo.git",
				CISourceRef:  "main",
			},
			setupMockStore: func(m *MockStore) {
				// Should never be called since validation fails
			},
			expectedStatus: http.StatusForbidden,
		},
		{
			name:      "URL normalization - different formats match",
			allowlist: "github.com/trusted/ci-repo",
			request: CreateJobRequest{
				Name:         "Test Job",
				JobCommand:   "echo hello",
				SourceType:   "git",
				SourceURL:    "https://github.com/untrusted/source.git",
				CISourceType: "git",
				// Different format but same repo
				CISourceURL: "git@github.com:trusted/ci-repo.git",
				CISourceRef: "main",
			},
			setupMockStore: func(m *MockStore) {
				m.CreateJobFunc = func(ctx context.Context, job *models.Job) error {
					job.JobID = "test-job-id"
					return nil
				}
			},
			expectedStatus: http.StatusCreated,
		},
		{
			name:      "multiple repos in allowlist - matches second entry",
			allowlist: "github.com/org1/repo1,github.com/org2/repo2,github.com/org3/repo3",
			request: CreateJobRequest{
				Name:         "Test Job",
				JobCommand:   "echo hello",
				SourceType:   "git",
				SourceURL:    "https://github.com/untrusted/source.git",
				CISourceType: "git",
				CISourceURL:  "https://github.com/org2/repo2",
				CISourceRef:  "main",
			},
			setupMockStore: func(m *MockStore) {
				m.CreateJobFunc = func(ctx context.Context, job *models.Job) error {
					job.JobID = "test-job-id"
					return nil
				}
			},
			expectedStatus: http.StatusCreated,
		},
		{
			name:      "empty allowlist - all CI sources allowed with warning",
			allowlist: "",
			request: CreateJobRequest{
				Name:         "Test Job",
				JobCommand:   "echo hello",
				SourceType:   "git",
				SourceURL:    "https://github.com/untrusted/source.git",
				CISourceType: "git",
				CISourceURL:  "https://github.com/any/repo.git",
				CISourceRef:  "main",
			},
			setupMockStore: func(m *MockStore) {
				m.CreateJobFunc = func(ctx context.Context, job *models.Job) error {
					job.JobID = "test-job-id"
					return nil
				}
			},
			expectedStatus: http.StatusCreated,
		},
		{
			name:      "ci_source_type copy - rejected for security",
			allowlist: "github.com/trusted/ci-repo",
			request: CreateJobRequest{
				Name:         "Test Job",
				JobCommand:   "echo hello",
				SourceType:   "git",
				SourceURL:    "https://github.com/untrusted/source.git",
				CISourceType: "copy",
				CISourceURL:  "/local/path",
			},
			setupMockStore: func(m *MockStore) {
				// Should never be called
			},
			expectedStatus: http.StatusBadRequest,
			errorContains:  "invalid_input",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Set allowlist for this test
			config.CiCodeAllowlist = tt.allowlist

			// Setup mock store
			mockStore := &MockStore{}
			if tt.setupMockStore != nil {
				tt.setupMockStore(mockStore)
			}

			// Create handler (no corndogs client needed for these tests)
			handler := NewJobHandler(mockStore, nil)

			// Create request
			body, _ := json.Marshal(tt.request)
			req := httptest.NewRequest("POST", "/api/v1/jobs", bytes.NewReader(body))

			// Add user to context
			user := &models.User{UserID: "test-user-id"}
			ctx := checkauth.SetUserContext(req.Context(), user)
			req = req.WithContext(ctx)

			// Execute request
			w := httptest.NewRecorder()
			handler.CreateJob(w, req)

			// Check status code
			if w.Code != tt.expectedStatus {
				t.Errorf("expected status %d, got %d. Response: %s", tt.expectedStatus, w.Code, w.Body.String())
			}

			// Check error message if expected
			if tt.errorContains != "" && !bytes.Contains(w.Body.Bytes(), []byte(tt.errorContains)) {
				t.Errorf("expected error to contain '%s', got: %s", tt.errorContains, w.Body.String())
			}

			// If created successfully, verify CI source fields were stored
			if tt.expectedStatus == http.StatusCreated && tt.request.CISourceType != "" {
				if len(mockStore.CreateJobCalls) != 1 {
					t.Fatalf("expected 1 CreateJob call, got %d", len(mockStore.CreateJobCalls))
				}
				createdJob := mockStore.CreateJobCalls[0]

				// Check CI source fields
				if createdJob.CISourceType == nil {
					t.Error("expected CISourceType to be set")
				} else if string(*createdJob.CISourceType) != tt.request.CISourceType {
					t.Errorf("expected CISourceType '%s', got '%s'", tt.request.CISourceType, string(*createdJob.CISourceType))
				}

				if createdJob.CISourceURL == nil {
					t.Error("expected CISourceURL to be set")
				}

				if createdJob.CISourceRef == nil {
					t.Error("expected CISourceRef to be set")
				}
			}
		})
	}
}
