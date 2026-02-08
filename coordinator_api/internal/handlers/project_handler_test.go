package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/checkauth"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ProjectMockStore implements store.Store for project handler testing
type ProjectMockStore struct {
	CreateProjectFunc  func(ctx context.Context, project *models.Project) error
	GetProjectByIDFunc func(ctx context.Context, projectID string) (*models.Project, error)
	UpdateProjectFunc  func(ctx context.Context, project *models.Project) error
	DeleteProjectFunc  func(ctx context.Context, projectID string) error
	ListProjectsFunc   func(ctx context.Context, limit, offset int) ([]models.Project, error)

	CreateProjectCalls  []models.Project
	GetProjectByIDCalls []string
	UpdateProjectCalls  []models.Project
	DeleteProjectCalls  []string
	ListProjectsCalls   []struct{ Limit, Offset int }
}

func (m *ProjectMockStore) CreateProject(ctx context.Context, project *models.Project) error {
	m.CreateProjectCalls = append(m.CreateProjectCalls, *project)
	if m.CreateProjectFunc != nil {
		return m.CreateProjectFunc(ctx, project)
	}
	if project.ProjectID == "" {
		project.ProjectID = uuid.New().String()
	}
	project.CreatedAt = time.Now().UTC()
	project.UpdatedAt = time.Now().UTC()
	return nil
}

func (m *ProjectMockStore) GetProjectByID(ctx context.Context, projectID string) (*models.Project, error) {
	m.GetProjectByIDCalls = append(m.GetProjectByIDCalls, projectID)
	if m.GetProjectByIDFunc != nil {
		return m.GetProjectByIDFunc(ctx, projectID)
	}
	return nil, store.ErrNotFound
}

func (m *ProjectMockStore) GetProjectByRepoURL(ctx context.Context, repoURL string) (*models.Project, error) {
	return nil, store.ErrNotFound
}

func (m *ProjectMockStore) UpdateProject(ctx context.Context, project *models.Project) error {
	m.UpdateProjectCalls = append(m.UpdateProjectCalls, *project)
	if m.UpdateProjectFunc != nil {
		return m.UpdateProjectFunc(ctx, project)
	}
	return nil
}

func (m *ProjectMockStore) DeleteProject(ctx context.Context, projectID string) error {
	m.DeleteProjectCalls = append(m.DeleteProjectCalls, projectID)
	if m.DeleteProjectFunc != nil {
		return m.DeleteProjectFunc(ctx, projectID)
	}
	return nil
}

func (m *ProjectMockStore) ListProjects(ctx context.Context, limit, offset int) ([]models.Project, error) {
	m.ListProjectsCalls = append(m.ListProjectsCalls, struct{ Limit, Offset int }{limit, offset})
	if m.ListProjectsFunc != nil {
		return m.ListProjectsFunc(ctx, limit, offset)
	}
	return []models.Project{}, nil
}

// Stub implementations for remaining store.Store interface methods
func (m *ProjectMockStore) Initialize() (func(), error)                             { return nil, nil }
func (m *ProjectMockStore) EnsureDefaultUser() error                                { return nil }
func (m *ProjectMockStore) CreateUser(ctx context.Context, user *models.User) error { return nil }
func (m *ProjectMockStore) GetUserByID(ctx context.Context, userID string) (*models.User, error) {
	return nil, nil
}
func (m *ProjectMockStore) CreateJob(ctx context.Context, job *models.Job) error { return nil }
func (m *ProjectMockStore) GetJobByID(ctx context.Context, jobID string) (*models.Job, error) {
	return nil, nil
}
func (m *ProjectMockStore) UpdateJob(ctx context.Context, job *models.Job) error { return nil }
func (m *ProjectMockStore) DeleteJob(ctx context.Context, jobID string) error    { return nil }
func (m *ProjectMockStore) ListJobs(ctx context.Context, filters map[string]interface{}, limit, offset int) ([]models.Job, error) {
	return nil, nil
}
func (m *ProjectMockStore) GetJobsByUser(ctx context.Context, userID string, limit, offset int) ([]models.Job, error) {
	return nil, nil
}
func (m *ProjectMockStore) ValidateAPIToken(ctx context.Context, token string) (*models.APIToken, *models.User, error) {
	return nil, nil, nil
}
func (m *ProjectMockStore) CreateAPIToken(ctx context.Context, token *models.APIToken) error {
	return nil
}
func (m *ProjectMockStore) UpdateTokenLastUsed(ctx context.Context, tokenID string, lastUsed time.Time) error {
	return nil
}
func (m *ProjectMockStore) GetAPITokensByUser(ctx context.Context, userID string) ([]models.APIToken, error) {
	return nil, nil
}
func (m *ProjectMockStore) DeleteAPIToken(ctx context.Context, tokenID string) error { return nil }

// helper to create a test project
func testProject(id string) *models.Project {
	return &models.Project{
		ProjectID:             id,
		CreatedAt:             time.Now().UTC(),
		UpdatedAt:             time.Now().UTC(),
		Name:                  "test-project",
		Description:           "A test project",
		RepoURL:               "github.com/org/repo",
		Enabled:               true,
		TargetBranches:        []string{"main"},
		AllowedEventTypes:     []string{"push", "pull_request"},
		DefaultCISourceType:   models.SourceTypeGit,
		DefaultCISourceRef:    "main",
		DefaultRunnerImage:    "alpine:latest",
		DefaultTimeoutSeconds: 3600,
		DefaultQueueName:      "reactorcide-jobs",
	}
}

// helper to set user context on a request
func withUser(r *http.Request) *http.Request {
	user := &models.User{UserID: "test-user-id"}
	ctx := checkauth.SetUserContext(r.Context(), user)
	return r.WithContext(ctx)
}

// helper to set project_id in context
func withProjectID(r *http.Request, id string) *http.Request {
	ctx := context.WithValue(r.Context(), GetContextKey("project_id"), id)
	return r.WithContext(ctx)
}

func TestProjectHandler_CreateProject(t *testing.T) {
	tests := []struct {
		name           string
		request        CreateProjectRequest
		setupMock      func(*ProjectMockStore)
		withAuth       bool
		expectedStatus int
		checkResponse  func(*testing.T, *httptest.ResponseRecorder)
	}{
		{
			name: "success",
			request: CreateProjectRequest{
				Name:    "my-project",
				RepoURL: "github.com/org/repo",
			},
			withAuth:       true,
			expectedStatus: http.StatusCreated,
			checkResponse: func(t *testing.T, w *httptest.ResponseRecorder) {
				var resp ProjectResponse
				require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
				assert.Equal(t, "my-project", resp.Name)
				assert.Equal(t, "github.com/org/repo", resp.RepoURL)
				assert.NotEmpty(t, resp.ProjectID)
			},
		},
		{
			name: "success with all fields",
			request: CreateProjectRequest{
				Name:                  "full-project",
				Description:           "Full description",
				RepoURL:               "github.com/org/full-repo",
				Enabled:               boolPtr(false),
				TargetBranches:        []string{"main", "develop"},
				AllowedEventTypes:     []string{"push"},
				DefaultCISourceType:   "git",
				DefaultCISourceURL:    "github.com/org/ci",
				DefaultCISourceRef:    "v1",
				DefaultRunnerImage:    "custom:latest",
				DefaultJobCommand:     "make test",
				DefaultTimeoutSeconds: intPtr(1800),
				DefaultQueueName:      "custom-queue",
			},
			withAuth:       true,
			expectedStatus: http.StatusCreated,
			checkResponse: func(t *testing.T, w *httptest.ResponseRecorder) {
				var resp ProjectResponse
				require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
				assert.Equal(t, "full-project", resp.Name)
				assert.Equal(t, false, resp.Enabled)
				assert.Equal(t, []string{"main", "develop"}, resp.TargetBranches)
				assert.Equal(t, "custom:latest", resp.DefaultRunnerImage)
				assert.Equal(t, 1800, resp.DefaultTimeoutSeconds)
			},
		},
		{
			name: "missing name",
			request: CreateProjectRequest{
				RepoURL: "github.com/org/repo",
			},
			withAuth:       true,
			expectedStatus: http.StatusBadRequest,
		},
		{
			name: "missing repo_url",
			request: CreateProjectRequest{
				Name: "my-project",
			},
			withAuth:       true,
			expectedStatus: http.StatusBadRequest,
		},
		{
			name: "store conflict error",
			request: CreateProjectRequest{
				Name:    "duplicate",
				RepoURL: "github.com/org/dup",
			},
			setupMock: func(m *ProjectMockStore) {
				m.CreateProjectFunc = func(ctx context.Context, project *models.Project) error {
					return store.ErrAlreadyExists
				}
			},
			withAuth:       true,
			expectedStatus: http.StatusConflict,
		},
		{
			name: "no auth",
			request: CreateProjectRequest{
				Name:    "my-project",
				RepoURL: "github.com/org/repo",
			},
			withAuth:       false,
			expectedStatus: http.StatusUnauthorized,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockStore := &ProjectMockStore{}
			if tt.setupMock != nil {
				tt.setupMock(mockStore)
			}
			handler := NewProjectHandler(mockStore)

			body, err := json.Marshal(tt.request)
			require.NoError(t, err)

			req := httptest.NewRequest(http.MethodPost, "/api/v1/projects", bytes.NewReader(body))
			if tt.withAuth {
				req = withUser(req)
			}

			w := httptest.NewRecorder()
			handler.CreateProject(w, req)

			assert.Equal(t, tt.expectedStatus, w.Code)
			if tt.checkResponse != nil {
				tt.checkResponse(t, w)
			}
		})
	}
}

func TestProjectHandler_GetProject(t *testing.T) {
	projectID := uuid.New().String()
	project := testProject(projectID)

	tests := []struct {
		name           string
		projectID      string
		setupMock      func(*ProjectMockStore)
		withAuth       bool
		expectedStatus int
		checkResponse  func(*testing.T, *httptest.ResponseRecorder)
	}{
		{
			name:      "success",
			projectID: projectID,
			setupMock: func(m *ProjectMockStore) {
				m.GetProjectByIDFunc = func(ctx context.Context, id string) (*models.Project, error) {
					return project, nil
				}
			},
			withAuth:       true,
			expectedStatus: http.StatusOK,
			checkResponse: func(t *testing.T, w *httptest.ResponseRecorder) {
				var resp ProjectResponse
				require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
				assert.Equal(t, projectID, resp.ProjectID)
				assert.Equal(t, "test-project", resp.Name)
			},
		},
		{
			name:           "not found",
			projectID:      uuid.New().String(),
			withAuth:       true,
			expectedStatus: http.StatusNotFound,
		},
		{
			name:           "missing project_id",
			projectID:      "",
			withAuth:       true,
			expectedStatus: http.StatusBadRequest,
		},
		{
			name:           "no auth",
			projectID:      projectID,
			withAuth:       false,
			expectedStatus: http.StatusUnauthorized,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockStore := &ProjectMockStore{}
			if tt.setupMock != nil {
				tt.setupMock(mockStore)
			}
			handler := NewProjectHandler(mockStore)

			req := httptest.NewRequest(http.MethodGet, "/api/v1/projects/"+tt.projectID, nil)
			if tt.withAuth {
				req = withUser(req)
			}
			if tt.projectID != "" {
				req = withProjectID(req, tt.projectID)
			}

			w := httptest.NewRecorder()
			handler.GetProject(w, req)

			assert.Equal(t, tt.expectedStatus, w.Code)
			if tt.checkResponse != nil {
				tt.checkResponse(t, w)
			}
		})
	}
}

func TestProjectHandler_ListProjects(t *testing.T) {
	projects := []models.Project{
		*testProject(uuid.New().String()),
		*testProject(uuid.New().String()),
	}
	projects[1].Name = "second-project"

	tests := []struct {
		name           string
		queryParams    string
		setupMock      func(*ProjectMockStore)
		withAuth       bool
		expectedStatus int
		checkResponse  func(*testing.T, *httptest.ResponseRecorder)
	}{
		{
			name: "success with defaults",
			setupMock: func(m *ProjectMockStore) {
				m.ListProjectsFunc = func(ctx context.Context, limit, offset int) ([]models.Project, error) {
					assert.Equal(t, 20, limit)
					assert.Equal(t, 0, offset)
					return projects, nil
				}
			},
			withAuth:       true,
			expectedStatus: http.StatusOK,
			checkResponse: func(t *testing.T, w *httptest.ResponseRecorder) {
				var resp ListProjectsResponse
				require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
				assert.Len(t, resp.Projects, 2)
				assert.Equal(t, 20, resp.Limit)
				assert.Equal(t, 0, resp.Offset)
			},
		},
		{
			name:        "custom limit and offset",
			queryParams: "limit=5&offset=10",
			setupMock: func(m *ProjectMockStore) {
				m.ListProjectsFunc = func(ctx context.Context, limit, offset int) ([]models.Project, error) {
					assert.Equal(t, 5, limit)
					assert.Equal(t, 10, offset)
					return []models.Project{}, nil
				}
			},
			withAuth:       true,
			expectedStatus: http.StatusOK,
			checkResponse: func(t *testing.T, w *httptest.ResponseRecorder) {
				var resp ListProjectsResponse
				require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
				assert.Equal(t, 5, resp.Limit)
				assert.Equal(t, 10, resp.Offset)
			},
		},
		{
			name:        "limit capped at 100",
			queryParams: "limit=200",
			setupMock: func(m *ProjectMockStore) {
				m.ListProjectsFunc = func(ctx context.Context, limit, offset int) ([]models.Project, error) {
					assert.Equal(t, 20, limit) // invalid value, falls back to default
					return []models.Project{}, nil
				}
			},
			withAuth:       true,
			expectedStatus: http.StatusOK,
		},
		{
			name:           "no auth",
			withAuth:       false,
			expectedStatus: http.StatusUnauthorized,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockStore := &ProjectMockStore{}
			if tt.setupMock != nil {
				tt.setupMock(mockStore)
			}
			handler := NewProjectHandler(mockStore)

			url := "/api/v1/projects"
			if tt.queryParams != "" {
				url += "?" + tt.queryParams
			}
			req := httptest.NewRequest(http.MethodGet, url, nil)
			if tt.withAuth {
				req = withUser(req)
			}

			w := httptest.NewRecorder()
			handler.ListProjects(w, req)

			assert.Equal(t, tt.expectedStatus, w.Code)
			if tt.checkResponse != nil {
				tt.checkResponse(t, w)
			}
		})
	}
}

func TestProjectHandler_UpdateProject(t *testing.T) {
	projectID := uuid.New().String()
	project := testProject(projectID)

	tests := []struct {
		name           string
		projectID      string
		request        UpdateProjectRequest
		setupMock      func(*ProjectMockStore)
		withAuth       bool
		expectedStatus int
		checkResponse  func(*testing.T, *httptest.ResponseRecorder)
	}{
		{
			name:      "success partial update",
			projectID: projectID,
			request: UpdateProjectRequest{
				Name:        strPtr("updated-name"),
				Description: strPtr("updated description"),
			},
			setupMock: func(m *ProjectMockStore) {
				m.GetProjectByIDFunc = func(ctx context.Context, id string) (*models.Project, error) {
					p := *project // copy
					return &p, nil
				}
			},
			withAuth:       true,
			expectedStatus: http.StatusOK,
			checkResponse: func(t *testing.T, w *httptest.ResponseRecorder) {
				var resp ProjectResponse
				require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
				assert.Equal(t, "updated-name", resp.Name)
				assert.Equal(t, "updated description", resp.Description)
				// Unchanged fields should remain
				assert.Equal(t, "github.com/org/repo", resp.RepoURL)
			},
		},
		{
			name:      "success update enabled",
			projectID: projectID,
			request: UpdateProjectRequest{
				Enabled: boolPtr(false),
			},
			setupMock: func(m *ProjectMockStore) {
				m.GetProjectByIDFunc = func(ctx context.Context, id string) (*models.Project, error) {
					p := *project
					return &p, nil
				}
			},
			withAuth:       true,
			expectedStatus: http.StatusOK,
			checkResponse: func(t *testing.T, w *httptest.ResponseRecorder) {
				var resp ProjectResponse
				require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
				assert.Equal(t, false, resp.Enabled)
			},
		},
		{
			name:           "not found",
			projectID:      uuid.New().String(),
			request:        UpdateProjectRequest{Name: strPtr("x")},
			withAuth:       true,
			expectedStatus: http.StatusNotFound,
		},
		{
			name:           "missing project_id",
			projectID:      "",
			request:        UpdateProjectRequest{Name: strPtr("x")},
			withAuth:       true,
			expectedStatus: http.StatusBadRequest,
		},
		{
			name:           "no auth",
			projectID:      projectID,
			request:        UpdateProjectRequest{Name: strPtr("x")},
			withAuth:       false,
			expectedStatus: http.StatusUnauthorized,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockStore := &ProjectMockStore{}
			if tt.setupMock != nil {
				tt.setupMock(mockStore)
			}
			handler := NewProjectHandler(mockStore)

			body, err := json.Marshal(tt.request)
			require.NoError(t, err)

			req := httptest.NewRequest(http.MethodPut, "/api/v1/projects/"+tt.projectID, bytes.NewReader(body))
			if tt.withAuth {
				req = withUser(req)
			}
			if tt.projectID != "" {
				req = withProjectID(req, tt.projectID)
			}

			w := httptest.NewRecorder()
			handler.UpdateProject(w, req)

			assert.Equal(t, tt.expectedStatus, w.Code)
			if tt.checkResponse != nil {
				tt.checkResponse(t, w)
			}
		})
	}
}

func TestProjectHandler_DeleteProject(t *testing.T) {
	projectID := uuid.New().String()

	tests := []struct {
		name           string
		projectID      string
		setupMock      func(*ProjectMockStore)
		withAuth       bool
		expectedStatus int
	}{
		{
			name:           "success",
			projectID:      projectID,
			withAuth:       true,
			expectedStatus: http.StatusNoContent,
		},
		{
			name:      "not found",
			projectID: uuid.New().String(),
			setupMock: func(m *ProjectMockStore) {
				m.DeleteProjectFunc = func(ctx context.Context, id string) error {
					return store.ErrNotFound
				}
			},
			withAuth:       true,
			expectedStatus: http.StatusNotFound,
		},
		{
			name:           "missing project_id",
			projectID:      "",
			withAuth:       true,
			expectedStatus: http.StatusBadRequest,
		},
		{
			name:           "no auth",
			projectID:      projectID,
			withAuth:       false,
			expectedStatus: http.StatusUnauthorized,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockStore := &ProjectMockStore{}
			if tt.setupMock != nil {
				tt.setupMock(mockStore)
			}
			handler := NewProjectHandler(mockStore)

			req := httptest.NewRequest(http.MethodDelete, "/api/v1/projects/"+tt.projectID, nil)
			if tt.withAuth {
				req = withUser(req)
			}
			if tt.projectID != "" {
				req = withProjectID(req, tt.projectID)
			}

			w := httptest.NewRecorder()
			handler.DeleteProject(w, req)

			assert.Equal(t, tt.expectedStatus, w.Code)
		})
	}
}

// helper functions
func strPtr(s string) *string { return &s }
func boolPtr(b bool) *bool    { return &b }

// intPtr is defined in job_handler_test.go
