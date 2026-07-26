package worker

import (
	"context"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

// MockStore implements store.Store for testing. Originally defined
// alongside the (now-removed) legacy direct-corndogs worker tests; kept
// here as a shared test helper for internal/worker tests that still need a
// minimal store.Store (e.g. trigger_processor_test.go, workflow_runtime_test.go).
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
func (m *MockStore) Initialize() (func(), error) { return nil, nil }
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
func (m *MockStore) DeleteJob(ctx context.Context, jobID string) error                 { return nil }
func (m *MockStore) GetJobsByUser(ctx context.Context, userID string, limit, offset int) ([]models.Job, error) {
	return nil, nil
}
func (m *MockStore) CreateUser(ctx context.Context, user *models.User) error { return nil }
func (m *MockStore) GetUserByID(ctx context.Context, userID string) (*models.User, error) {
	return nil, nil
}
func (m *MockStore) EnsureDefaultUser() error                     { return nil }
func (m *MockStore) EnsureDefaultQueue(ctx context.Context) error { return nil }
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
