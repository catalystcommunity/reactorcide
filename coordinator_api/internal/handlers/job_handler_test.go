package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/characteristics"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/checkauth"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/config"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/corndogs"
	pb "github.com/catalystcommunity/reactorcide/coordinator_api/internal/corndogs/v1alpha1"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/objects"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
func (m *MockStore) EnsureDefaultQueue(ctx context.Context) error            { return nil }
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
func (m *MockStore) ListJobsForPRCommit(ctx context.Context, repo string, prNumber int, commitSHA string) ([]models.Job, error) {
	return nil, nil
}
func (m *MockStore) ListJobsForPR(ctx context.Context, repo string, prNumber int) ([]models.Job, error) {
	return nil, nil
}
func (m *MockStore) ForPRCommit(ctx context.Context, repo string, prNumber int, commitSHA string, fn func(ctx context.Context) error) error {
	return fn(ctx)
}
func (m *MockStore) IsPRMerged(ctx context.Context, repo string, prNumber int) (bool, error) {
	return false, nil
}
func (m *MockStore) MarkPRMerged(ctx context.Context, repo string, prNumber int) error { return nil }

func TestValidateCreateJobRequestAllowsNoSource(t *testing.T) {
	handler := &JobHandler{}
	err := handler.validateCreateJobRequest(&CreateJobRequest{
		Name:       "native VM smoke test",
		JobCommand: "sw_vers",
		SourceType: "none",
	})
	if err != nil {
		t.Fatalf("validate source type none: %v", err)
	}
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
				SourceURL:  "https://github.com/test/repo.git",
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
				m.SubmitTaskToQueueFunc = func(ctx context.Context, queue string, payload *corndogs.TaskPayload, priority int64) (*pb.Task, error) {
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
				SourceURL:  "https://github.com/test/repo.git",
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
				m.SubmitTaskToQueueFunc = func(ctx context.Context, queue string, payload *corndogs.TaskPayload, priority int64) (*pb.Task, error) {
					return nil, fmt.Errorf("corndogs error")
				}
			},
			expectedStatus:        http.StatusCreated,
			expectedCorndogsCalls: 1,
			checkResponse: func(t *testing.T, resp JobResponse) {
				if resp.Status != "failed" {
					t.Errorf("expected status 'failed', got %s", resp.Status)
				}
				if resp.LastError == "" {
					t.Error("expected last_error to be returned for failed job creation")
				}
			},
		},
		{
			name: "job creation without Corndogs client",
			request: CreateJobRequest{
				Name:       "Test Job",
				JobCommand: "echo hello",
				SourceType: "git",
				SourceURL:  "https://github.com/test/repo.git",
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

			// Check Corndogs calls -- submission goes through SubmitTaskToQueue
			// (queue-routed submit path), not the legacy SubmitTask.
			if mockCorndogs != nil && len(mockCorndogs.SubmitTaskToQueueCalls) != tt.expectedCorndogsCalls {
				t.Errorf("expected %d Corndogs calls, got %d", tt.expectedCorndogsCalls, len(mockCorndogs.SubmitTaskToQueueCalls))
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
		expectedJobStatus     string
	}{
		{
			// Running jobs have an active container, so CancelJob hands off
			// to the worker rather than cancelling the Corndogs task itself:
			// the job moves to "cancelling" and the worker cancels the
			// Corndogs task once it has actually stopped the container (see
			// internal/coordinatorworker's cancel-directive handling and
			// internal/workerapi's ReportResult finalize path).
			name:  "running job cancellation moves to cancelling, defers Corndogs cancel to the worker",
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
				// Should not be called for a running job's cancel — the
				// worker cancels the Corndogs task once it has stopped.
			},
			expectedStatus:        http.StatusOK,
			expectedCorndogsCalls: 0,
			expectedJobStatus:     "cancelling",
		},
		{
			// Submitted/queued jobs never started a container, so CancelJob
			// cancels the Corndogs task immediately and lands directly on
			// the terminal "cancelled" status — no worker involvement.
			name:  "queued job cancellation cancels Corndogs task immediately",
			jobID: "test-job-id",
			setupMockStore: func(m *MockStore) {
				taskID := "corndogs-task-id"
				m.GetJobByIDFunc = func(ctx context.Context, jobID string) (*models.Job, error) {
					return &models.Job{
						JobID:          jobID,
						Status:         "queued",
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
			expectedJobStatus:     "cancelled",
		},
		{
			name:  "submitted job cancellation continues even if Corndogs fails",
			jobID: "test-job-id",
			setupMockStore: func(m *MockStore) {
				taskID := "corndogs-task-id"
				m.GetJobByIDFunc = func(ctx context.Context, jobID string) (*models.Job, error) {
					return &models.Job{
						JobID:          jobID,
						Status:         "submitted",
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
			expectedJobStatus:     "cancelled",
		},
		{
			name:  "job cancellation without Corndogs task ID",
			jobID: "test-job-id",
			setupMockStore: func(m *MockStore) {
				m.GetJobByIDFunc = func(ctx context.Context, jobID string) (*models.Job, error) {
					return &models.Job{
						JobID:          jobID,
						Status:         "submitted",
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
			expectedJobStatus:     "cancelled",
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

			// Verify job was updated to the expected status
			if len(mockStore.UpdateJobCalls) > 0 {
				lastUpdate := mockStore.UpdateJobCalls[len(mockStore.UpdateJobCalls)-1]
				if lastUpdate.Status != tt.expectedJobStatus {
					t.Errorf("expected job status to be %q, got %s", tt.expectedJobStatus, lastUpdate.Status)
				}
			}
		})
	}
}

// TestJobHandler_KillJob_RunningJob verifies POST /api/v1/jobs/{id}/kill on
// a running job sets status "cancelling" with CancelMode == "kill" — the
// marker job_processor.go's cancel-poll uses to route into an immediate
// forced Cleanup instead of a graceful Stop.
//
// The caller here is a plain (non-RoleStore) MockStore, so h.visibility is
// nil and canUserKillJob's fail-closed fallback applies (see
// TestJobHandler_KillJob_NilResolver_FailClosed_DeniesNonAdminOwner): the
// caller must carry the legacy admin role for the kill itself to be
// authorized, even though it also happens to own the job.
func TestJobHandler_KillJob_RunningJob(t *testing.T) {
	mockStore := &MockStore{
		GetJobByIDFunc: func(ctx context.Context, jobID string) (*models.Job, error) {
			return &models.Job{JobID: jobID, Status: "running", UserID: "test-user-id"}, nil
		},
		UpdateJobFunc: func(ctx context.Context, job *models.Job) error { return nil },
	}
	handler := NewJobHandler(mockStore, nil)

	req := httptest.NewRequest("POST", "/api/v1/jobs/test-job-id/kill", nil)
	user := &models.User{UserID: "test-user-id", Roles: []string{"admin"}}
	ctx := checkauth.SetUserContext(req.Context(), user)
	ctx = context.WithValue(ctx, GetContextKey("job_id"), "test-job-id")
	req = req.WithContext(ctx)

	w := httptest.NewRecorder()
	handler.KillJob(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", w.Code, w.Body.String())
	}
	if len(mockStore.UpdateJobCalls) != 1 {
		t.Fatalf("expected exactly 1 UpdateJob call, got %d", len(mockStore.UpdateJobCalls))
	}
	updated := mockStore.UpdateJobCalls[0]
	if updated.Status != "cancelling" {
		t.Errorf("expected status 'cancelling', got %s", updated.Status)
	}
	if updated.CancelMode != "kill" {
		t.Errorf("expected CancelMode %q, got %q", "kill", updated.CancelMode)
	}
}

// TestJobHandler_KillJob_NilResolver_FailClosed_DeniesNonAdminOwner is
// Finding 1's fail-closed regression test: when the wired store doesn't
// satisfy authz.RoleStore (h.visibility is nil), the pre-existing
// owner-or-admin canUserAccessJob check must NOT be used for kill — only
// the legacy isAdmin(user) check is honored, so a plain owner (no admin
// role) cannot kill their own job in this fallback, even though they could
// cancel it (see TestJobHandler_KillJob_NilResolver_OwnerCanStillCancel).
func TestJobHandler_KillJob_NilResolver_FailClosed_DeniesNonAdminOwner(t *testing.T) {
	mockStore := &MockStore{
		GetJobByIDFunc: func(ctx context.Context, jobID string) (*models.Job, error) {
			return &models.Job{JobID: jobID, Status: "running", UserID: "test-user-id"}, nil
		},
	}
	handler := NewJobHandler(mockStore, nil)
	if handler.visibility != nil {
		t.Fatal("expected a plain MockStore to not satisfy authz.RoleStore")
	}

	req := httptest.NewRequest("POST", "/api/v1/jobs/test-job-id/kill", nil)
	owner := &models.User{UserID: "test-user-id"} // no admin role
	ctx := checkauth.SetUserContext(req.Context(), owner)
	ctx = context.WithValue(ctx, GetContextKey("job_id"), "test-job-id")
	req = req.WithContext(ctx)

	w := httptest.NewRecorder()
	handler.KillJob(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403 (fail-closed: owner without admin role can't kill without a visibility resolver), got %d: %s", w.Code, w.Body.String())
	}
	if len(mockStore.UpdateJobCalls) != 0 {
		t.Fatalf("expected no UpdateJob call for a denied kill, got %d", len(mockStore.UpdateJobCalls))
	}
}

// TestJobHandler_KillJob_NilResolver_OwnerCanStillCancel pairs with the
// fail-closed test above: cancel keeps its original owner-or-admin
// semantics regardless of h.visibility, so the same plain owner CAN cancel
// (just not kill) their own job.
func TestJobHandler_KillJob_NilResolver_OwnerCanStillCancel(t *testing.T) {
	mockStore := &MockStore{
		GetJobByIDFunc: func(ctx context.Context, jobID string) (*models.Job, error) {
			return &models.Job{JobID: jobID, Status: "running", UserID: "test-user-id"}, nil
		},
		UpdateJobFunc: func(ctx context.Context, job *models.Job) error { return nil },
	}
	handler := NewJobHandler(mockStore, nil)

	req := httptest.NewRequest("PUT", "/api/v1/jobs/test-job-id/cancel", nil)
	owner := &models.User{UserID: "test-user-id"}
	ctx := checkauth.SetUserContext(req.Context(), owner)
	ctx = context.WithValue(ctx, GetContextKey("job_id"), "test-job-id")
	req = req.WithContext(ctx)

	w := httptest.NewRecorder()
	handler.CancelJob(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 (owner can still cancel without a visibility resolver), got %d: %s", w.Code, w.Body.String())
	}
}

// TestJobHandler_KillJob_AlreadyTerminal verifies kill is rejected for a job
// that's already in a terminal state, same as cancel. The caller carries
// the legacy admin role so it clears canUserKillJob's fail-closed gate (see
// TestJobHandler_KillJob_NilResolver_FailClosed_DeniesNonAdminOwner) and
// this test exercises the CanBeKilled state check specifically.
func TestJobHandler_KillJob_AlreadyTerminal(t *testing.T) {
	mockStore := &MockStore{
		GetJobByIDFunc: func(ctx context.Context, jobID string) (*models.Job, error) {
			return &models.Job{JobID: jobID, Status: "completed", UserID: "test-user-id"}, nil
		},
	}
	handler := NewJobHandler(mockStore, nil)

	req := httptest.NewRequest("POST", "/api/v1/jobs/test-job-id/kill", nil)
	user := &models.User{UserID: "test-user-id", Roles: []string{"admin"}}
	ctx := checkauth.SetUserContext(req.Context(), user)
	ctx = context.WithValue(ctx, GetContextKey("job_id"), "test-job-id")
	req = req.WithContext(ctx)

	w := httptest.NewRecorder()
	handler.KillJob(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected status 400 for an already-terminal job, got %d", w.Code)
	}
}

// TestJobHandler_RetryJob_HappyPath verifies POST /api/v1/jobs/{id}/retry on
// a failed job creates and returns a brand-new job row (distinct JobID,
// status not "failed"), submitted to Corndogs.
func TestJobHandler_RetryJob_HappyPath(t *testing.T) {
	mockStore := &MockStore{
		GetJobByIDFunc: func(ctx context.Context, jobID string) (*models.Job, error) {
			return &models.Job{JobID: jobID, Status: "failed", UserID: "test-user-id", JobCommand: "make test"}, nil
		},
	}
	mockCorndogs := corndogs.NewMockClient()
	handler := NewJobHandler(mockStore, mockCorndogs)

	req := httptest.NewRequest("POST", "/api/v1/jobs/test-job-id/retry", nil)
	user := &models.User{UserID: "test-user-id"}
	ctx := checkauth.SetUserContext(req.Context(), user)
	ctx = context.WithValue(ctx, GetContextKey("job_id"), "test-job-id")
	req = req.WithContext(ctx)

	w := httptest.NewRecorder()
	handler.RetryJob(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected status 201, got %d: %s", w.Code, w.Body.String())
	}

	var resp JobResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if resp.JobID == "test-job-id" {
		t.Error("expected a distinct new job ID from the retried job")
	}
	if resp.Status == "failed" {
		t.Errorf("expected the new job to not still be 'failed', got %q", resp.Status)
	}
	if len(mockCorndogs.SubmitTaskToQueueCalls) != 1 {
		t.Errorf("expected 1 SubmitTaskToQueue call, got %d", len(mockCorndogs.SubmitTaskToQueueCalls))
	}
	if len(mockStore.CreateJobCalls) != 1 {
		t.Errorf("expected 1 CreateJob call, got %d", len(mockStore.CreateJobCalls))
	}
}

// TestJobHandler_RetryJob_NotRetryable verifies a running job (not
// failed/cancelled) is refused with 400.
func TestJobHandler_RetryJob_NotRetryable(t *testing.T) {
	mockStore := &MockStore{
		GetJobByIDFunc: func(ctx context.Context, jobID string) (*models.Job, error) {
			return &models.Job{JobID: jobID, Status: "running", UserID: "test-user-id"}, nil
		},
	}
	handler := NewJobHandler(mockStore, nil)

	req := httptest.NewRequest("POST", "/api/v1/jobs/test-job-id/retry", nil)
	user := &models.User{UserID: "test-user-id"}
	ctx := checkauth.SetUserContext(req.Context(), user)
	ctx = context.WithValue(ctx, GetContextKey("job_id"), "test-job-id")
	req = req.WithContext(ctx)

	w := httptest.NewRecorder()
	handler.RetryJob(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected status 400 for a non-retryable (running) job, got %d", w.Code)
	}
	if len(mockStore.CreateJobCalls) != 0 {
		t.Errorf("expected no CreateJob call for a refused retry, got %d", len(mockStore.CreateJobCalls))
	}
}

// TestJobHandler_RetryJob_Forbidden verifies a non-owner, non-admin caller
// cannot retry someone else's job — same authz tier as CancelJob
// (canUserAccessJob).
func TestJobHandler_RetryJob_Forbidden(t *testing.T) {
	mockStore := &MockStore{
		GetJobByIDFunc: func(ctx context.Context, jobID string) (*models.Job, error) {
			return &models.Job{JobID: jobID, Status: "failed", UserID: "owner-id"}, nil
		},
	}
	handler := NewJobHandler(mockStore, nil)

	req := httptest.NewRequest("POST", "/api/v1/jobs/test-job-id/retry", nil)
	user := &models.User{UserID: "someone-else"}
	ctx := checkauth.SetUserContext(req.Context(), user)
	ctx = context.WithValue(ctx, GetContextKey("job_id"), "test-job-id")
	req = req.WithContext(ctx)

	w := httptest.NewRecorder()
	handler.RetryJob(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("expected status 403, got %d", w.Code)
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
	mockCorndogs.SubmitTaskToQueueFunc = func(ctx context.Context, queue string, payload *corndogs.TaskPayload, priority int64) (*pb.Task, error) {
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
		SourceURL:   "https://github.com/test/repo.git",
		SourceRef:   "main",
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

// TestGetJobLogsWithMemoryStore tests GetJobLogs with an in-memory object store
func TestGetJobLogsWithMemoryStore(t *testing.T) {
	testJobID := "test-job-123"
	testUserID := "test-user-456"

	testJob := &models.Job{
		JobID:  testJobID,
		UserID: testUserID,
		Name:   "Test Job",
		Status: "completed",
	}

	testUser := &models.User{
		UserID:   testUserID,
		Username: "testuser",
		Email:    "test@example.com",
	}

	mockStoreInstance := &MockStore{
		GetJobByIDFunc: func(ctx context.Context, jobID string) (*models.Job, error) {
			if jobID == testJobID {
				return testJob, nil
			}
			return nil, store.ErrNotFound
		},
	}

	t.Run("returns stdout logs from memory store", func(t *testing.T) {
		memStore := objects.NewMemoryObjectStore()
		handler := NewJobHandlerWithObjectStore(mockStoreInstance, nil, memStore)

		// Store test logs as JSON array
		stdoutEntries := []LogEntry{
			{Timestamp: "2024-01-01T10:00:00Z", Stream: "stdout", Level: "info", Message: "Hello from stdout!"},
			{Timestamp: "2024-01-01T10:00:01Z", Stream: "stdout", Level: "info", Message: "Line 2"},
		}
		stdoutContent, _ := json.Marshal(stdoutEntries)
		stdoutKey := "logs/" + testJobID + "/stdout.json"
		err := memStore.Put(context.Background(), stdoutKey, bytes.NewReader(stdoutContent), "application/json")
		require.NoError(t, err)

		// Create request with user context
		req := httptest.NewRequest("GET", "/api/v1/jobs/"+testJobID+"/logs?stream=stdout", nil)
		ctx := checkauth.SetUserContext(req.Context(), testUser)
		ctx = context.WithValue(ctx, GetContextKey("job_id"), testJobID)
		req = req.WithContext(ctx)

		rr := httptest.NewRecorder()
		handler.GetJobLogs(rr, req)

		assert.Equal(t, http.StatusOK, rr.Code)
		assert.Equal(t, "application/json", rr.Header().Get("Content-Type"))

		// Parse and verify JSON array
		var entries []LogEntry
		err = json.Unmarshal(rr.Body.Bytes(), &entries)
		require.NoError(t, err)
		assert.Len(t, entries, 2)
		assert.Equal(t, "Hello from stdout!", entries[0].Message)
	})

	t.Run("returns stderr logs from memory store", func(t *testing.T) {
		memStore := objects.NewMemoryObjectStore()
		handler := NewJobHandlerWithObjectStore(mockStoreInstance, nil, memStore)

		// Store test logs as JSON array
		stderrEntries := []LogEntry{
			{Timestamp: "2024-01-01T10:00:00Z", Stream: "stderr", Level: "error", Message: "Error output"},
			{Timestamp: "2024-01-01T10:00:01Z", Stream: "stderr", Level: "error", Message: "Stack trace"},
		}
		stderrContent, _ := json.Marshal(stderrEntries)
		stderrKey := "logs/" + testJobID + "/stderr.json"
		err := memStore.Put(context.Background(), stderrKey, bytes.NewReader(stderrContent), "application/json")
		require.NoError(t, err)

		// Create request with user context
		req := httptest.NewRequest("GET", "/api/v1/jobs/"+testJobID+"/logs?stream=stderr", nil)
		ctx := checkauth.SetUserContext(req.Context(), testUser)
		ctx = context.WithValue(ctx, GetContextKey("job_id"), testJobID)
		req = req.WithContext(ctx)

		rr := httptest.NewRecorder()
		handler.GetJobLogs(rr, req)

		assert.Equal(t, http.StatusOK, rr.Code)

		// Parse and verify JSON array
		var entries []LogEntry
		err = json.Unmarshal(rr.Body.Bytes(), &entries)
		require.NoError(t, err)
		assert.Len(t, entries, 2)
		assert.Equal(t, "Error output", entries[0].Message)
	})

	t.Run("returns combined logs by default", func(t *testing.T) {
		memStore := objects.NewMemoryObjectStore()
		handler := NewJobHandlerWithObjectStore(mockStoreInstance, nil, memStore)

		// Store both stdout and stderr as JSON arrays
		stdoutEntries := []LogEntry{
			{Timestamp: "2024-01-01T10:00:00Z", Stream: "stdout", Level: "info", Message: "stdout first"},
			{Timestamp: "2024-01-01T10:00:02Z", Stream: "stdout", Level: "info", Message: "stdout second"},
		}
		stderrEntries := []LogEntry{
			{Timestamp: "2024-01-01T10:00:01Z", Stream: "stderr", Level: "error", Message: "stderr middle"},
		}
		stdoutContent, _ := json.Marshal(stdoutEntries)
		stderrContent, _ := json.Marshal(stderrEntries)
		err := memStore.Put(context.Background(), "logs/"+testJobID+"/stdout.json", bytes.NewReader(stdoutContent), "application/json")
		require.NoError(t, err)
		err = memStore.Put(context.Background(), "logs/"+testJobID+"/stderr.json", bytes.NewReader(stderrContent), "application/json")
		require.NoError(t, err)

		// Create request with user context
		req := httptest.NewRequest("GET", "/api/v1/jobs/"+testJobID+"/logs", nil)
		ctx := checkauth.SetUserContext(req.Context(), testUser)
		ctx = context.WithValue(ctx, GetContextKey("job_id"), testJobID)
		req = req.WithContext(ctx)

		rr := httptest.NewRecorder()
		handler.GetJobLogs(rr, req)

		assert.Equal(t, http.StatusOK, rr.Code)

		// Parse and verify combined JSON array is sorted by timestamp
		var entries []LogEntry
		err = json.Unmarshal(rr.Body.Bytes(), &entries)
		require.NoError(t, err)
		assert.Len(t, entries, 3)
		// Should be sorted by timestamp
		assert.Equal(t, "stdout first", entries[0].Message)
		assert.Equal(t, "stderr middle", entries[1].Message)
		assert.Equal(t, "stdout second", entries[2].Message)
	})

	t.Run("returns 404 when no logs exist", func(t *testing.T) {
		memStore := objects.NewMemoryObjectStore()
		handler := NewJobHandlerWithObjectStore(mockStoreInstance, nil, memStore)

		// No logs stored
		req := httptest.NewRequest("GET", "/api/v1/jobs/"+testJobID+"/logs", nil)
		ctx := checkauth.SetUserContext(req.Context(), testUser)
		ctx = context.WithValue(ctx, GetContextKey("job_id"), testJobID)
		req = req.WithContext(ctx)

		rr := httptest.NewRecorder()
		handler.GetJobLogs(rr, req)

		assert.Equal(t, http.StatusNotFound, rr.Code)
	})

	t.Run("returns 400 for invalid stream parameter", func(t *testing.T) {
		memStore := objects.NewMemoryObjectStore()
		handler := NewJobHandlerWithObjectStore(mockStoreInstance, nil, memStore)

		req := httptest.NewRequest("GET", "/api/v1/jobs/"+testJobID+"/logs?stream=invalid", nil)
		ctx := checkauth.SetUserContext(req.Context(), testUser)
		ctx = context.WithValue(ctx, GetContextKey("job_id"), testJobID)
		req = req.WithContext(ctx)

		rr := httptest.NewRecorder()
		handler.GetJobLogs(rr, req)

		assert.Equal(t, http.StatusBadRequest, rr.Code)
	})

	t.Run("returns 401 when user not in context", func(t *testing.T) {
		memStore := objects.NewMemoryObjectStore()
		handler := NewJobHandlerWithObjectStore(mockStoreInstance, nil, memStore)

		req := httptest.NewRequest("GET", "/api/v1/jobs/"+testJobID+"/logs", nil)
		ctx := context.WithValue(req.Context(), GetContextKey("job_id"), testJobID)
		req = req.WithContext(ctx)

		rr := httptest.NewRecorder()
		handler.GetJobLogs(rr, req)

		assert.Equal(t, http.StatusUnauthorized, rr.Code)
	})

	t.Run("returns 403 when user doesn't own the job", func(t *testing.T) {
		memStore := objects.NewMemoryObjectStore()
		handler := NewJobHandlerWithObjectStore(mockStoreInstance, nil, memStore)

		otherUser := &models.User{
			UserID:   "other-user-789",
			Username: "otheruser",
			Email:    "other@example.com",
		}

		req := httptest.NewRequest("GET", "/api/v1/jobs/"+testJobID+"/logs", nil)
		ctx := checkauth.SetUserContext(req.Context(), otherUser)
		ctx = context.WithValue(ctx, GetContextKey("job_id"), testJobID)
		req = req.WithContext(ctx)

		rr := httptest.NewRecorder()
		handler.GetJobLogs(rr, req)

		assert.Equal(t, http.StatusForbidden, rr.Code)
	})
}

// TestGetJobLogsWithFilesystemStore tests GetJobLogs with a filesystem object store using tmp directories
func TestGetJobLogsWithFilesystemStore(t *testing.T) {
	testJobID := "test-job-fs-123"
	testUserID := "test-user-fs-456"

	testJob := &models.Job{
		JobID:  testJobID,
		UserID: testUserID,
		Name:   "Test Job FS",
		Status: "completed",
	}

	testUser := &models.User{
		UserID:   testUserID,
		Username: "testuser",
		Email:    "test@example.com",
	}

	mockStoreInstance := &MockStore{
		GetJobByIDFunc: func(ctx context.Context, jobID string) (*models.Job, error) {
			if jobID == testJobID {
				return testJob, nil
			}
			return nil, store.ErrNotFound
		},
	}

	t.Run("returns logs from filesystem store with tmp directory", func(t *testing.T) {
		// Create a temporary directory for the test
		tmpDir, err := os.MkdirTemp("", "reactorcide-logs-test-*")
		require.NoError(t, err)
		defer os.RemoveAll(tmpDir)

		// Create filesystem object store
		fsStore := objects.NewFilesystemObjectStore(tmpDir)
		handler := NewJobHandlerWithObjectStore(mockStoreInstance, nil, fsStore)

		// Store test logs as JSON array using the filesystem store
		stdoutEntries := []LogEntry{
			{Timestamp: "2024-01-01T10:00:00Z", Stream: "stdout", Level: "info", Message: "Hello from filesystem!"},
			{Timestamp: "2024-01-01T10:00:01Z", Stream: "stdout", Level: "info", Message: "Line 2"},
			{Timestamp: "2024-01-01T10:00:02Z", Stream: "stdout", Level: "info", Message: "Line 3"},
		}
		stdoutContent, _ := json.Marshal(stdoutEntries)
		stdoutKey := "logs/" + testJobID + "/stdout.json"
		err = fsStore.Put(context.Background(), stdoutKey, bytes.NewReader(stdoutContent), "application/json")
		require.NoError(t, err)

		// Verify the file was created
		expectedPath := filepath.Join(tmpDir, "logs", testJobID, "stdout.json")
		_, err = os.Stat(expectedPath)
		require.NoError(t, err, "Log file should exist on filesystem")

		// Create request with user context
		req := httptest.NewRequest("GET", "/api/v1/jobs/"+testJobID+"/logs?stream=stdout", nil)
		ctx := checkauth.SetUserContext(req.Context(), testUser)
		ctx = context.WithValue(ctx, GetContextKey("job_id"), testJobID)
		req = req.WithContext(ctx)

		rr := httptest.NewRecorder()
		handler.GetJobLogs(rr, req)

		assert.Equal(t, http.StatusOK, rr.Code)
		assert.Equal(t, "application/json", rr.Header().Get("Content-Type"))

		// Parse and verify JSON array
		var entries []LogEntry
		err = json.Unmarshal(rr.Body.Bytes(), &entries)
		require.NoError(t, err)
		assert.Len(t, entries, 3)
		assert.Equal(t, "Hello from filesystem!", entries[0].Message)
	})

	t.Run("returns combined logs from filesystem", func(t *testing.T) {
		// Create a temporary directory for the test
		tmpDir, err := os.MkdirTemp("", "reactorcide-logs-combined-test-*")
		require.NoError(t, err)
		defer os.RemoveAll(tmpDir)

		// Create filesystem object store
		fsStore := objects.NewFilesystemObjectStore(tmpDir)
		handler := NewJobHandlerWithObjectStore(mockStoreInstance, nil, fsStore)

		// Store both stdout and stderr as JSON arrays
		stdoutEntries := []LogEntry{
			{Timestamp: "2024-01-01T10:00:00Z", Stream: "stdout", Level: "info", Message: "Standard output"},
		}
		stderrEntries := []LogEntry{
			{Timestamp: "2024-01-01T10:00:01Z", Stream: "stderr", Level: "error", Message: "Standard error"},
		}
		stdoutContent, _ := json.Marshal(stdoutEntries)
		stderrContent, _ := json.Marshal(stderrEntries)
		err = fsStore.Put(context.Background(), "logs/"+testJobID+"/stdout.json", bytes.NewReader(stdoutContent), "application/json")
		require.NoError(t, err)
		err = fsStore.Put(context.Background(), "logs/"+testJobID+"/stderr.json", bytes.NewReader(stderrContent), "application/json")
		require.NoError(t, err)

		// Create request with user context
		req := httptest.NewRequest("GET", "/api/v1/jobs/"+testJobID+"/logs", nil)
		ctx := checkauth.SetUserContext(req.Context(), testUser)
		ctx = context.WithValue(ctx, GetContextKey("job_id"), testJobID)
		req = req.WithContext(ctx)

		rr := httptest.NewRecorder()
		handler.GetJobLogs(rr, req)

		assert.Equal(t, http.StatusOK, rr.Code)

		// Parse and verify combined JSON array is sorted by timestamp
		var entries []LogEntry
		err = json.Unmarshal(rr.Body.Bytes(), &entries)
		require.NoError(t, err)
		assert.Len(t, entries, 2)
		assert.Equal(t, "Standard output", entries[0].Message)
		assert.Equal(t, "Standard error", entries[1].Message)
	})

	t.Run("handles non-existent logs directory gracefully", func(t *testing.T) {
		// Create a temporary directory for the test
		tmpDir, err := os.MkdirTemp("", "reactorcide-logs-empty-test-*")
		require.NoError(t, err)
		defer os.RemoveAll(tmpDir)

		// Create filesystem object store
		fsStore := objects.NewFilesystemObjectStore(tmpDir)
		handler := NewJobHandlerWithObjectStore(mockStoreInstance, nil, fsStore)

		// Don't store any logs - request should return 404
		req := httptest.NewRequest("GET", "/api/v1/jobs/"+testJobID+"/logs", nil)
		ctx := checkauth.SetUserContext(req.Context(), testUser)
		ctx = context.WithValue(ctx, GetContextKey("job_id"), testJobID)
		req = req.WithContext(ctx)

		rr := httptest.NewRecorder()
		handler.GetJobLogs(rr, req)

		assert.Equal(t, http.StatusNotFound, rr.Code)
	})
}

// TestJobHandler_SubmitTriggers tests the POST /api/v1/jobs/{job_id}/triggers endpoint
func TestJobHandler_SubmitTriggers(t *testing.T) {
	parentJobID := "parent-job-123"
	testUserID := "test-user-id"

	parentJob := &models.Job{
		JobID:          parentJobID,
		UserID:         testUserID,
		QueueName:      "reactorcide-jobs",
		RunnerImage:    "default:image",
		TimeoutSeconds: 3600,
	}

	tests := []struct {
		name           string
		jobID          string
		body           string
		userID         string
		setupMockStore func(*MockStore)
		expectedStatus int
		checkResponse  func(*testing.T, *httptest.ResponseRecorder)
	}{
		{
			name:   "successful trigger submission",
			jobID:  parentJobID,
			body:   `{"type":"trigger_job","jobs":[{"job_name":"test","job_command":"make test","container_image":"alpine:latest"}]}`,
			userID: testUserID,
			setupMockStore: func(m *MockStore) {
				m.GetJobByIDFunc = func(ctx context.Context, jobID string) (*models.Job, error) {
					if jobID == parentJobID {
						return parentJob, nil
					}
					return nil, store.ErrNotFound
				}
				m.CreateJobFunc = func(ctx context.Context, job *models.Job) error {
					job.JobID = "created-job-id"
					return nil
				}
			},
			expectedStatus: http.StatusCreated,
			checkResponse: func(t *testing.T, rr *httptest.ResponseRecorder) {
				var resp SubmitTriggersResponse
				err := json.NewDecoder(rr.Body).Decode(&resp)
				require.NoError(t, err)
				assert.Equal(t, 1, resp.Count)
				assert.Len(t, resp.CreatedJobIDs, 1)
				assert.Equal(t, "created-job-id", resp.CreatedJobIDs[0])
			},
		},
		{
			name:   "job not found",
			jobID:  "nonexistent",
			body:   `{"type":"trigger_job","jobs":[]}`,
			userID: testUserID,
			setupMockStore: func(m *MockStore) {
				m.GetJobByIDFunc = func(ctx context.Context, jobID string) (*models.Job, error) {
					return nil, store.ErrNotFound
				}
			},
			expectedStatus: http.StatusNotFound,
		},
		{
			name:   "forbidden - different user",
			jobID:  parentJobID,
			body:   `{"type":"trigger_job","jobs":[]}`,
			userID: "other-user-id",
			setupMockStore: func(m *MockStore) {
				m.GetJobByIDFunc = func(ctx context.Context, jobID string) (*models.Job, error) {
					return parentJob, nil
				}
			},
			expectedStatus: http.StatusForbidden,
		},
		{
			name:   "invalid JSON body",
			jobID:  parentJobID,
			body:   `not json`,
			userID: testUserID,
			setupMockStore: func(m *MockStore) {
				m.GetJobByIDFunc = func(ctx context.Context, jobID string) (*models.Job, error) {
					return parentJob, nil
				}
			},
			expectedStatus: http.StatusBadRequest,
		},
		{
			name:   "wrong trigger type",
			jobID:  parentJobID,
			body:   `{"type":"unknown_type","jobs":[{"job_name":"test"}]}`,
			userID: testUserID,
			setupMockStore: func(m *MockStore) {
				m.GetJobByIDFunc = func(ctx context.Context, jobID string) (*models.Job, error) {
					return parentJob, nil
				}
			},
			expectedStatus: http.StatusBadRequest,
		},
		{
			name:   "empty jobs returns 201 with count 0",
			jobID:  parentJobID,
			body:   `{"type":"trigger_job","jobs":[]}`,
			userID: testUserID,
			setupMockStore: func(m *MockStore) {
				m.GetJobByIDFunc = func(ctx context.Context, jobID string) (*models.Job, error) {
					return parentJob, nil
				}
			},
			expectedStatus: http.StatusCreated,
			checkResponse: func(t *testing.T, rr *httptest.ResponseRecorder) {
				var resp SubmitTriggersResponse
				err := json.NewDecoder(rr.Body).Decode(&resp)
				require.NoError(t, err)
				assert.Equal(t, 0, resp.Count)
				assert.Len(t, resp.CreatedJobIDs, 0)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockStore := &MockStore{}
			if tt.setupMockStore != nil {
				tt.setupMockStore(mockStore)
			}

			handler := NewJobHandler(mockStore, nil)

			req := httptest.NewRequest("POST", fmt.Sprintf("/api/v1/jobs/%s/triggers", tt.jobID), bytes.NewReader([]byte(tt.body)))
			user := &models.User{UserID: tt.userID}
			ctx := checkauth.SetUserContext(req.Context(), user)
			ctx = context.WithValue(ctx, GetContextKey("job_id"), tt.jobID)
			req = req.WithContext(ctx)

			rr := httptest.NewRecorder()
			handler.SubmitTriggers(rr, req)

			assert.Equal(t, tt.expectedStatus, rr.Code, "Response body: %s", rr.Body.String())

			if tt.checkResponse != nil {
				tt.checkResponse(t, rr)
			}
		})
	}
}

// TestGetJobLogsObjectStoreNotConfigured tests behavior when object store is nil
func TestGetJobLogsObjectStoreNotConfigured(t *testing.T) {
	testJobID := "test-job-no-store"
	testUserID := "test-user-no-store"

	testJob := &models.Job{
		JobID:  testJobID,
		UserID: testUserID,
		Name:   "Test Job No Store",
		Status: "completed",
	}

	testUser := &models.User{
		UserID:   testUserID,
		Username: "testuser",
		Email:    "test@example.com",
	}

	mockStoreInstance := &MockStore{
		GetJobByIDFunc: func(ctx context.Context, jobID string) (*models.Job, error) {
			if jobID == testJobID {
				return testJob, nil
			}
			return nil, store.ErrNotFound
		},
	}

	t.Run("returns 503 when object store is not configured", func(t *testing.T) {
		// Handler with nil object store
		handler := NewJobHandlerWithObjectStore(mockStoreInstance, nil, nil)

		req := httptest.NewRequest("GET", "/api/v1/jobs/"+testJobID+"/logs", nil)
		ctx := checkauth.SetUserContext(req.Context(), testUser)
		ctx = context.WithValue(ctx, GetContextKey("job_id"), testJobID)
		req = req.WithContext(ctx)

		rr := httptest.NewRecorder()
		handler.GetJobLogs(rr, req)

		assert.Equal(t, http.StatusServiceUnavailable, rr.Code)
	})
}

// ===== Characteristics / Resources submit-path wiring =====

// queueResolvingMockStore layers a fake FindOrCreateQueueByCharacteristics
// onto MockStore so CreateJob's queueResolvingStore type assertion succeeds,
// letting these tests exercise the queue-resolution branch of the submit
// path (job_handler.go's queueResolvingStore) without a real database.
type queueResolvingMockStore struct {
	*MockStore
	// Queues maps a characteristics hash to the queue that should be
	// "found" for it; a miss synthesizes a new queue (like the real
	// find-or-create), recording it in Calls.
	Queues map[string]*models.Queue
	Calls  []characteristics.Characteristics
}

func newQueueResolvingMockStore() *queueResolvingMockStore {
	return &queueResolvingMockStore{
		MockStore: &MockStore{},
		Queues:    map[string]*models.Queue{},
	}
}

func (m *queueResolvingMockStore) FindOrCreateQueueByCharacteristics(ctx context.Context, chars characteristics.Characteristics) (*models.Queue, error) {
	m.Calls = append(m.Calls, chars)
	hash := characteristics.Hash(chars)
	if q, ok := m.Queues[hash]; ok {
		return q, nil
	}
	q := &models.Queue{
		QueueID:             uuid.New().String(),
		QueueUUID:           uuid.New().String(),
		Characteristics:     chars,
		CharacteristicsHash: hash,
	}
	m.Queues[hash] = q
	return q, nil
}

// TestJobHandler_CreateJob_CharacteristicsDefaultToLinux verifies a bare job
// request (no `characteristics` field) resolves to {"os":"linux"} -- the one
// default assumption in the characteristic model.
func TestJobHandler_CreateJob_CharacteristicsDefaultToLinux(t *testing.T) {
	mockStore := &MockStore{
		CreateJobFunc: func(ctx context.Context, job *models.Job) error {
			job.JobID = "test-job-id"
			return nil
		},
	}
	handler := NewJobHandler(mockStore, nil)

	request := CreateJobRequest{
		Name:       "Test Job",
		JobCommand: "echo hello",
		SourceType: "copy",
		SourcePath: "./",
	}
	body, _ := json.Marshal(request)
	req := httptest.NewRequest("POST", "/api/v1/jobs", bytes.NewReader(body))
	user := &models.User{UserID: "test-user-id"}
	req = req.WithContext(checkauth.SetUserContext(req.Context(), user))

	w := httptest.NewRecorder()
	handler.CreateJob(w, req)

	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	require.Len(t, mockStore.CreateJobCalls, 1)

	got := mockStore.CreateJobCalls[0].Characteristics
	want, err := characteristics.ParseJobCharacteristics(nil)
	require.NoError(t, err)
	assert.Equal(t, characteristics.Hash(want), characteristics.Hash(got))
}

// TestJobHandler_CreateJob_CharacteristicsParsed verifies an explicit
// `characteristics` block on the request is parsed onto the job.
func TestJobHandler_CreateJob_CharacteristicsParsed(t *testing.T) {
	mockStore := &MockStore{
		CreateJobFunc: func(ctx context.Context, job *models.Job) error {
			job.JobID = "test-job-id"
			return nil
		},
	}
	handler := NewJobHandler(mockStore, nil)

	request := CreateJobRequest{
		Name:            "Test Job",
		JobCommand:      "echo hello",
		SourceType:      "copy",
		SourcePath:      "./",
		Characteristics: map[string]interface{}{"os": "windows", "gpu": true},
	}
	body, _ := json.Marshal(request)
	req := httptest.NewRequest("POST", "/api/v1/jobs", bytes.NewReader(body))
	user := &models.User{UserID: "test-user-id"}
	req = req.WithContext(checkauth.SetUserContext(req.Context(), user))

	w := httptest.NewRecorder()
	handler.CreateJob(w, req)

	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	require.Len(t, mockStore.CreateJobCalls, 1)

	got := mockStore.CreateJobCalls[0].Characteristics
	want, err := characteristics.ParseJobCharacteristics(map[string]any{"os": "windows", "gpu": true})
	require.NoError(t, err)
	assert.Equal(t, characteristics.Hash(want), characteristics.Hash(got))
}

// TestJobHandler_CreateJob_InvalidCharacteristics_Returns400 verifies a
// list-valued characteristic (rejected for job/queue characteristics --
// list values are worker-only) is a 400, not a 500 or a silently-ignored
// value.
func TestJobHandler_CreateJob_InvalidCharacteristics_Returns400(t *testing.T) {
	mockStore := &MockStore{}
	handler := NewJobHandler(mockStore, nil)

	request := CreateJobRequest{
		Name:            "Test Job",
		JobCommand:      "echo hello",
		SourceType:      "copy",
		SourcePath:      "./",
		Characteristics: map[string]interface{}{"os": []interface{}{"linux", "windows"}},
	}
	body, _ := json.Marshal(request)
	req := httptest.NewRequest("POST", "/api/v1/jobs", bytes.NewReader(body))
	user := &models.User{UserID: "test-user-id"}
	req = req.WithContext(checkauth.SetUserContext(req.Context(), user))

	w := httptest.NewRecorder()
	handler.CreateJob(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Empty(t, mockStore.CreateJobCalls, "job must not be persisted when characteristics fail validation")
}

// TestJobHandler_CreateJob_ResourcesParsed verifies a `resources` block is
// parsed onto the job's resource fields, and left empty (DB-default
// territory) when the request has none.
func TestJobHandler_CreateJob_ResourcesParsed(t *testing.T) {
	mockStore := &MockStore{
		CreateJobFunc: func(ctx context.Context, job *models.Job) error {
			job.JobID = "test-job-id"
			return nil
		},
	}
	handler := NewJobHandler(mockStore, nil)

	request := CreateJobRequest{
		Name:       "Test Job",
		JobCommand: "echo hello",
		SourceType: "copy",
		SourcePath: "./",
		Resources: map[string]interface{}{
			"cpu":    map[string]interface{}{"request": "500m", "limit": "1"},
			"memory": map[string]interface{}{"limit": "1Gi"},
		},
	}
	body, _ := json.Marshal(request)
	req := httptest.NewRequest("POST", "/api/v1/jobs", bytes.NewReader(body))
	user := &models.User{UserID: "test-user-id"}
	req = req.WithContext(checkauth.SetUserContext(req.Context(), user))

	w := httptest.NewRecorder()
	handler.CreateJob(w, req)

	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	require.Len(t, mockStore.CreateJobCalls, 1)

	got := mockStore.CreateJobCalls[0]
	assert.Equal(t, "500m", got.ResourceCPURequest)
	assert.Equal(t, "1", got.ResourceCPULimit)
	assert.Equal(t, "1Gi", got.ResourceMemoryLimit)
}

// TestJobHandler_CreateJob_ResourcesAbsent_LeavesFieldsEmpty verifies that
// omitting `resources` entirely leaves the resource fields empty so the DB
// column defaults (cpu.request=1, cpu.limit=2, memory.limit=4Gi) apply.
func TestJobHandler_CreateJob_ResourcesAbsent_LeavesFieldsEmpty(t *testing.T) {
	mockStore := &MockStore{
		CreateJobFunc: func(ctx context.Context, job *models.Job) error {
			job.JobID = "test-job-id"
			return nil
		},
	}
	handler := NewJobHandler(mockStore, nil)

	request := CreateJobRequest{
		Name:       "Test Job",
		JobCommand: "echo hello",
		SourceType: "copy",
		SourcePath: "./",
	}
	body, _ := json.Marshal(request)
	req := httptest.NewRequest("POST", "/api/v1/jobs", bytes.NewReader(body))
	user := &models.User{UserID: "test-user-id"}
	req = req.WithContext(checkauth.SetUserContext(req.Context(), user))

	w := httptest.NewRecorder()
	handler.CreateJob(w, req)

	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	require.Len(t, mockStore.CreateJobCalls, 1)

	got := mockStore.CreateJobCalls[0]
	assert.Empty(t, got.ResourceCPURequest)
	assert.Empty(t, got.ResourceCPULimit)
	assert.Empty(t, got.ResourceMemoryLimit)
}

// TestJobHandler_CreateJob_ResolvesQueueUUID_SubmitsToIt verifies the submit
// path resolves the job's characteristics to a queue (find-or-create) and
// submits the Corndogs task to that queue's UUID. Uses
// queueResolvingMockStore since the plain MockStore doesn't implement
// queueResolvingStore (that's the point being tested: production's
// PostgresDbStore does, via
// internal/store/postgres_store/queue_operations.go).
func TestJobHandler_CreateJob_ResolvesQueueUUID_SubmitsToIt(t *testing.T) {
	qStore := newQueueResolvingMockStore()
	qStore.MockStore.CreateJobFunc = func(ctx context.Context, job *models.Job) error {
		job.JobID = "test-job-id"
		return nil
	}

	mockCorndogs := corndogs.NewMockClient()
	handler := NewJobHandler(qStore, mockCorndogs)

	request := CreateJobRequest{
		Name:            "Test Job",
		JobCommand:      "echo hello",
		SourceType:      "copy",
		SourcePath:      "./",
		Characteristics: map[string]interface{}{"os": "windows"},
	}
	body, _ := json.Marshal(request)
	req := httptest.NewRequest("POST", "/api/v1/jobs", bytes.NewReader(body))
	user := &models.User{UserID: "test-user-id"}
	req = req.WithContext(checkauth.SetUserContext(req.Context(), user))

	w := httptest.NewRecorder()
	handler.CreateJob(w, req)

	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	require.Len(t, qStore.Calls, 1, "expected exactly one queue-resolution call")

	wantChars, err := characteristics.ParseJobCharacteristics(map[string]any{"os": "windows"})
	require.NoError(t, err)
	resolvedQueue := qStore.Queues[characteristics.Hash(wantChars)]
	require.NotNil(t, resolvedQueue, "expected the {os:windows} queue to have been created")

	require.Len(t, qStore.MockStore.CreateJobCalls, 1)
	assert.Equal(t, resolvedQueue.QueueUUID, qStore.MockStore.CreateJobCalls[0].QueueName,
		"expected job.QueueName to be set to the resolved queue's UUID before persisting")

	require.Len(t, mockCorndogs.SubmitTaskToQueueCalls, 1)
	assert.Equal(t, resolvedQueue.QueueUUID, mockCorndogs.SubmitTaskToQueueCalls[0].Queue,
		"expected the Corndogs task to be submitted to the resolved queue's UUID")
}
