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

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/corndogs"
	pb "github.com/catalystcommunity/reactorcide/coordinator_api/internal/corndogs/v1alpha1"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/vcs"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// WebhookMockStore implements store.Store for webhook handler testing
type WebhookMockStore struct {
	CreateJobFunc          func(ctx context.Context, job *models.Job) error
	UpdateJobFunc          func(ctx context.Context, job *models.Job) error
	GetProjectByRepoURLFunc func(ctx context.Context, repoURL string) (*models.Project, error)

	CreateJobCalls          []*models.Job
	UpdateJobCalls          []*models.Job
	GetProjectByRepoURLCalls []string
}

func (m *WebhookMockStore) CreateJob(ctx context.Context, job *models.Job) error {
	m.CreateJobCalls = append(m.CreateJobCalls, job)
	if m.CreateJobFunc != nil {
		return m.CreateJobFunc(ctx, job)
	}
	if job.JobID == "" {
		job.JobID = uuid.New().String()
	}
	job.CreatedAt = time.Now().UTC()
	job.UpdatedAt = time.Now().UTC()
	return nil
}

func (m *WebhookMockStore) UpdateJob(ctx context.Context, job *models.Job) error {
	m.UpdateJobCalls = append(m.UpdateJobCalls, job)
	if m.UpdateJobFunc != nil {
		return m.UpdateJobFunc(ctx, job)
	}
	return nil
}

func (m *WebhookMockStore) GetProjectByRepoURL(ctx context.Context, repoURL string) (*models.Project, error) {
	m.GetProjectByRepoURLCalls = append(m.GetProjectByRepoURLCalls, repoURL)
	if m.GetProjectByRepoURLFunc != nil {
		return m.GetProjectByRepoURLFunc(ctx, repoURL)
	}
	return nil, store.ErrNotFound
}

// Stub implementations for remaining store.Store interface methods
func (m *WebhookMockStore) Initialize() (func(), error)                             { return nil, nil }
func (m *WebhookMockStore) EnsureDefaultUser() error                                { return nil }
func (m *WebhookMockStore) CreateUser(ctx context.Context, user *models.User) error { return nil }
func (m *WebhookMockStore) GetUserByID(ctx context.Context, userID string) (*models.User, error) {
	return nil, nil
}
func (m *WebhookMockStore) CreateProject(ctx context.Context, project *models.Project) error {
	return nil
}
func (m *WebhookMockStore) GetProjectByID(ctx context.Context, projectID string) (*models.Project, error) {
	return nil, store.ErrNotFound
}
func (m *WebhookMockStore) UpdateProject(ctx context.Context, project *models.Project) error {
	return nil
}
func (m *WebhookMockStore) DeleteProject(ctx context.Context, projectID string) error { return nil }
func (m *WebhookMockStore) ListProjects(ctx context.Context, limit, offset int) ([]models.Project, error) {
	return nil, nil
}
func (m *WebhookMockStore) GetJobByID(ctx context.Context, jobID string) (*models.Job, error) {
	return nil, nil
}
func (m *WebhookMockStore) DeleteJob(ctx context.Context, jobID string) error { return nil }
func (m *WebhookMockStore) ListJobs(ctx context.Context, filters map[string]interface{}, limit, offset int) ([]models.Job, error) {
	return nil, nil
}
func (m *WebhookMockStore) GetJobsByUser(ctx context.Context, userID string, limit, offset int) ([]models.Job, error) {
	return nil, nil
}
func (m *WebhookMockStore) ValidateAPIToken(ctx context.Context, token string) (*models.APIToken, *models.User, error) {
	return nil, nil, nil
}
func (m *WebhookMockStore) CreateAPIToken(ctx context.Context, token *models.APIToken) error {
	return nil
}
func (m *WebhookMockStore) UpdateTokenLastUsed(ctx context.Context, tokenID string, lastUsed time.Time) error {
	return nil
}
func (m *WebhookMockStore) GetAPITokensByUser(ctx context.Context, userID string) ([]models.APIToken, error) {
	return nil, nil
}
func (m *WebhookMockStore) DeleteAPIToken(ctx context.Context, tokenID string) error { return nil }

// MockVCSClient implements vcs.Client for testing
type MockVCSClient struct {
	ParseWebhookFunc        func(r *http.Request) (*vcs.WebhookEvent, error)
	ValidateWebhookFunc     func(r *http.Request, secret string) error
	UpdateCommitStatusFunc  func(ctx context.Context, repo string, update vcs.StatusUpdate) error
	UpdatePRCommentFunc     func(ctx context.Context, repo string, prNumber int, comment string) error
	GetPRInfoFunc           func(ctx context.Context, repo string, prNumber int) (*vcs.PullRequestInfo, error)
}

func (m *MockVCSClient) GetProvider() vcs.Provider { return vcs.GitHub }

func (m *MockVCSClient) ParseWebhook(r *http.Request) (*vcs.WebhookEvent, error) {
	if m.ParseWebhookFunc != nil {
		return m.ParseWebhookFunc(r)
	}
	return nil, fmt.Errorf("no mock configured")
}

func (m *MockVCSClient) ValidateWebhook(r *http.Request, secret string) error {
	if m.ValidateWebhookFunc != nil {
		return m.ValidateWebhookFunc(r, secret)
	}
	return nil
}

func (m *MockVCSClient) UpdateCommitStatus(ctx context.Context, repo string, update vcs.StatusUpdate) error {
	if m.UpdateCommitStatusFunc != nil {
		return m.UpdateCommitStatusFunc(ctx, repo, update)
	}
	return nil
}

func (m *MockVCSClient) UpdatePRComment(ctx context.Context, repo string, prNumber int, comment string) error {
	if m.UpdatePRCommentFunc != nil {
		return m.UpdatePRCommentFunc(ctx, repo, prNumber, comment)
	}
	return nil
}

func (m *MockVCSClient) GetPRInfo(ctx context.Context, repo string, prNumber int) (*vcs.PullRequestInfo, error) {
	if m.GetPRInfoFunc != nil {
		return m.GetPRInfoFunc(ctx, repo, prNumber)
	}
	return nil, nil
}

// helper to build a webhook project for tests
func webhookTestProject() *models.Project {
	return &models.Project{
		ProjectID:          uuid.New().String(),
		Name:               "test-project",
		RepoURL:            "github.com/test-org/test-repo",
		Enabled:            true,
		TargetBranches:     []string{"main"},
		AllowedEventTypes:  []string{"push", "pull_request_opened", "pull_request_updated", "tag_created"},
		DefaultRunnerImage: "alpine:latest",
		DefaultQueueName:   "reactorcide-jobs",
	}
}

// helper to create a GitHub PR webhook payload
func makePRWebhookBody(repoFullName, cloneURL, headSHA, headRef, baseRef string, prNumber int) []byte {
	payload := map[string]interface{}{
		"action": "opened",
		"number": prNumber,
		"pull_request": map[string]interface{}{
			"title":    "Test PR",
			"body":     "Test PR body",
			"state":    "open",
			"merged":   false,
			"html_url": fmt.Sprintf("https://github.com/%s/pull/%d", repoFullName, prNumber),
			"head": map[string]interface{}{
				"ref": headRef,
				"sha": headSHA,
			},
			"base": map[string]interface{}{
				"ref": baseRef,
				"sha": "base-sha-1234",
			},
			"user": map[string]interface{}{
				"login": "testuser",
			},
		},
		"repository": map[string]interface{}{
			"full_name":      repoFullName,
			"clone_url":      cloneURL,
			"ssh_url":        "",
			"html_url":       fmt.Sprintf("https://github.com/%s", repoFullName),
			"default_branch": "main",
		},
	}
	body, _ := json.Marshal(payload)
	return body
}

// helper to create a GitHub push webhook payload
func makePushWebhookBody(repoFullName, cloneURL, afterSHA, ref string) []byte {
	payload := map[string]interface{}{
		"ref":     ref,
		"before":  "before-sha-1234",
		"after":   afterSHA,
		"created": false,
		"deleted": false,
		"forced":  false,
		"compare": fmt.Sprintf("https://github.com/%s/compare/before...after", repoFullName),
		"commits": []map[string]interface{}{
			{
				"id":      afterSHA,
				"message": "test commit",
				"author":  map[string]interface{}{"name": "test", "email": "test@test.com"},
			},
		},
		"repository": map[string]interface{}{
			"full_name":      repoFullName,
			"clone_url":      cloneURL,
			"ssh_url":        "",
			"html_url":       fmt.Sprintf("https://github.com/%s", repoFullName),
			"default_branch": "main",
		},
		"pusher": map[string]interface{}{
			"name":  "testuser",
			"email": "test@test.com",
		},
	}
	body, _ := json.Marshal(payload)
	return body
}

func TestWebhookHandler_PREvent_SubmitsToCorndogs(t *testing.T) {
	project := webhookTestProject()
	mockStore := &WebhookMockStore{
		GetProjectByRepoURLFunc: func(ctx context.Context, repoURL string) (*models.Project, error) {
			return project, nil
		},
	}
	mockCorndogs := corndogs.NewMockClient()

	handler := NewWebhookHandler(mockStore, mockCorndogs)

	// Create a mock VCS client that returns a PR event
	prEvent := &vcs.WebhookEvent{
		Provider:     vcs.GitHub,
		EventType:    "pull_request",
		GenericEvent: vcs.EventPullRequestOpened,
		Repository: vcs.RepositoryInfo{
			FullName: "test-org/test-repo",
			CloneURL: "https://github.com/test-org/test-repo.git",
		},
		PullRequest: &vcs.PullRequestInfo{
			Number:  42,
			Title:   "Test PR",
			Action:  "opened",
			HeadSHA: "abc123",
			HeadRef: "feature-branch",
			BaseRef: "main",
		},
	}

	mockVCS := &MockVCSClient{
		ParseWebhookFunc: func(r *http.Request) (*vcs.WebhookEvent, error) {
			return prEvent, nil
		},
	}
	handler.AddVCSClient(vcs.GitHub, mockVCS)

	body := makePRWebhookBody("test-org/test-repo", "https://github.com/test-org/test-repo.git", "abc123", "feature-branch", "main", 42)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/webhooks/github", bytes.NewReader(body))
	req.Header.Set("X-GitHub-Event", "pull_request")
	w := httptest.NewRecorder()

	handler.HandleGitHubWebhook(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	// Verify eval job was created
	require.Len(t, mockStore.CreateJobCalls, 1)
	createdJob := mockStore.CreateJobCalls[0]
	assert.Equal(t, 10, createdJob.Priority) // PR priority
	assert.Equal(t, "pull_request_opened", createdJob.JobEnvVars["REACTORCIDE_EVENT_TYPE"])

	// Verify eval job characteristics
	assert.Contains(t, createdJob.Name, "eval:")
	assert.Contains(t, createdJob.Name, "PR #42")
	assert.Contains(t, createdJob.Description, "Eval job")
	assert.Equal(t, "runnerlib eval --event-type $REACTORCIDE_EVENT_TYPE --branch $REACTORCIDE_BRANCH", createdJob.JobCommand)
	assert.Equal(t, "abc123", createdJob.JobEnvVars["REACTORCIDE_SHA"])
	assert.Equal(t, "feature-branch", createdJob.JobEnvVars["REACTORCIDE_PR_REF"])
	assert.Equal(t, "main", createdJob.JobEnvVars["REACTORCIDE_PR_BASE_REF"])
	// CI source should be set (same-repo mode since project has no DefaultCISourceURL)
	require.NotNil(t, createdJob.CISourceURL)
	assert.Equal(t, "https://github.com/test-org/test-repo.git", *createdJob.CISourceURL)

	// Verify Corndogs submission
	require.Equal(t, 1, mockCorndogs.GetSubmitTaskCallCount())
	submitCall := mockCorndogs.SubmitTaskCalls[0]
	assert.Equal(t, createdJob.JobID, submitCall.Payload.JobID)
	assert.Equal(t, int64(10), submitCall.Priority)
	assert.Equal(t, "run", submitCall.Payload.JobType)

	// Verify source fields in payload
	assert.Equal(t, "git", submitCall.Payload.Source["type"])
	assert.Equal(t, "https://github.com/test-org/test-repo.git", submitCall.Payload.Source["url"])
	assert.Equal(t, "abc123", submitCall.Payload.Source["ref"])

	// Verify job was updated with corndogs task ID
	require.Len(t, mockStore.UpdateJobCalls, 1)
	updatedJob := mockStore.UpdateJobCalls[0]
	assert.NotNil(t, updatedJob.CorndogsTaskID)
	assert.Equal(t, "submitted", updatedJob.Status)
}

func TestWebhookHandler_PushEvent_SubmitsToCorndogs(t *testing.T) {
	project := webhookTestProject()
	mockStore := &WebhookMockStore{
		GetProjectByRepoURLFunc: func(ctx context.Context, repoURL string) (*models.Project, error) {
			return project, nil
		},
	}
	mockCorndogs := corndogs.NewMockClient()

	handler := NewWebhookHandler(mockStore, mockCorndogs)

	pushEvent := &vcs.WebhookEvent{
		Provider:     vcs.GitHub,
		EventType:    "push",
		GenericEvent: vcs.EventPush,
		Repository: vcs.RepositoryInfo{
			FullName: "test-org/test-repo",
			CloneURL: "https://github.com/test-org/test-repo.git",
		},
		Push: &vcs.PushInfo{
			Ref:    "refs/heads/main",
			Before: "before-sha",
			After:  "after-sha-1234",
			Commits: []vcs.Commit{
				{ID: "after-sha-1234", Message: "test commit"},
			},
		},
	}

	mockVCS := &MockVCSClient{
		ParseWebhookFunc: func(r *http.Request) (*vcs.WebhookEvent, error) {
			return pushEvent, nil
		},
	}
	handler.AddVCSClient(vcs.GitHub, mockVCS)

	body := makePushWebhookBody("test-org/test-repo", "https://github.com/test-org/test-repo.git", "after-sha-1234", "refs/heads/main")
	req := httptest.NewRequest(http.MethodPost, "/api/v1/webhooks/github", bytes.NewReader(body))
	req.Header.Set("X-GitHub-Event", "push")
	w := httptest.NewRecorder()

	handler.HandleGitHubWebhook(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	// Verify eval job was created
	require.Len(t, mockStore.CreateJobCalls, 1)
	createdJob := mockStore.CreateJobCalls[0]
	assert.Equal(t, 5, createdJob.Priority) // Push priority
	assert.Equal(t, "push", createdJob.JobEnvVars["REACTORCIDE_EVENT_TYPE"])

	// Verify eval job characteristics
	assert.Contains(t, createdJob.Name, "eval:")
	assert.Contains(t, createdJob.Name, "push to main")
	assert.Contains(t, createdJob.Description, "Eval job")
	assert.Equal(t, "runnerlib eval --event-type $REACTORCIDE_EVENT_TYPE --branch $REACTORCIDE_BRANCH", createdJob.JobCommand)
	assert.Equal(t, "after-sha-1234", createdJob.JobEnvVars["REACTORCIDE_SHA"])
	assert.Equal(t, "main", createdJob.JobEnvVars["REACTORCIDE_BRANCH"])
	// CI source should be set (same-repo mode)
	require.NotNil(t, createdJob.CISourceURL)
	assert.Equal(t, "https://github.com/test-org/test-repo.git", *createdJob.CISourceURL)

	// Verify Corndogs submission
	require.Equal(t, 1, mockCorndogs.GetSubmitTaskCallCount())
	submitCall := mockCorndogs.SubmitTaskCalls[0]
	assert.Equal(t, createdJob.JobID, submitCall.Payload.JobID)
	assert.Equal(t, int64(5), submitCall.Priority)
	assert.Equal(t, "run", submitCall.Payload.JobType)

	// Verify source fields
	assert.Equal(t, "git", submitCall.Payload.Source["type"])
	assert.Equal(t, "https://github.com/test-org/test-repo.git", submitCall.Payload.Source["url"])
	assert.Equal(t, "after-sha-1234", submitCall.Payload.Source["ref"])

	// Verify job was updated with corndogs task ID
	require.Len(t, mockStore.UpdateJobCalls, 1)
	updatedJob := mockStore.UpdateJobCalls[0]
	assert.NotNil(t, updatedJob.CorndogsTaskID)
	assert.Equal(t, "submitted", updatedJob.Status)
}

func TestWebhookHandler_CorndogsSubmissionFailure_JobStillCreated(t *testing.T) {
	project := webhookTestProject()
	mockStore := &WebhookMockStore{
		GetProjectByRepoURLFunc: func(ctx context.Context, repoURL string) (*models.Project, error) {
			return project, nil
		},
	}
	mockCorndogs := corndogs.NewMockClient()
	mockCorndogs.SubmitTaskFunc = func(ctx context.Context, payload *corndogs.TaskPayload, priority int64) (*pb.Task, error) {
		return nil, fmt.Errorf("corndogs connection refused")
	}

	handler := NewWebhookHandler(mockStore, mockCorndogs)

	prEvent := &vcs.WebhookEvent{
		Provider:     vcs.GitHub,
		EventType:    "pull_request",
		GenericEvent: vcs.EventPullRequestOpened,
		Repository: vcs.RepositoryInfo{
			FullName: "test-org/test-repo",
			CloneURL: "https://github.com/test-org/test-repo.git",
		},
		PullRequest: &vcs.PullRequestInfo{
			Number:  1,
			Title:   "Test PR",
			Action:  "opened",
			HeadSHA: "sha123",
			HeadRef: "feature",
			BaseRef: "main",
		},
	}

	mockVCS := &MockVCSClient{
		ParseWebhookFunc: func(r *http.Request) (*vcs.WebhookEvent, error) {
			return prEvent, nil
		},
	}
	handler.AddVCSClient(vcs.GitHub, mockVCS)

	body := makePRWebhookBody("test-org/test-repo", "https://github.com/test-org/test-repo.git", "sha123", "feature", "main", 1)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/webhooks/github", bytes.NewReader(body))
	req.Header.Set("X-GitHub-Event", "pull_request")
	w := httptest.NewRecorder()

	handler.HandleGitHubWebhook(w, req)

	// Webhook should still succeed even if Corndogs fails
	assert.Equal(t, http.StatusOK, w.Code)

	// Job was created in DB
	require.Len(t, mockStore.CreateJobCalls, 1)

	// Corndogs was called but failed
	require.Equal(t, 1, mockCorndogs.GetSubmitTaskCallCount())

	// Job was updated with failed status
	require.Len(t, mockStore.UpdateJobCalls, 1)
	updatedJob := mockStore.UpdateJobCalls[0]
	assert.Equal(t, "failed", updatedJob.Status)
	assert.Nil(t, updatedJob.CorndogsTaskID)
}

func TestWebhookHandler_NilCorndogsClient_JobCreatedNoSubmission(t *testing.T) {
	project := webhookTestProject()
	mockStore := &WebhookMockStore{
		GetProjectByRepoURLFunc: func(ctx context.Context, repoURL string) (*models.Project, error) {
			return project, nil
		},
	}

	// Pass nil corndogs client
	handler := NewWebhookHandler(mockStore, nil)

	pushEvent := &vcs.WebhookEvent{
		Provider:     vcs.GitHub,
		EventType:    "push",
		GenericEvent: vcs.EventPush,
		Repository: vcs.RepositoryInfo{
			FullName: "test-org/test-repo",
			CloneURL: "https://github.com/test-org/test-repo.git",
		},
		Push: &vcs.PushInfo{
			Ref:    "refs/heads/main",
			Before: "before-sha",
			After:  "after-sha",
			Commits: []vcs.Commit{
				{ID: "after-sha", Message: "test commit"},
			},
		},
	}

	mockVCS := &MockVCSClient{
		ParseWebhookFunc: func(r *http.Request) (*vcs.WebhookEvent, error) {
			return pushEvent, nil
		},
	}
	handler.AddVCSClient(vcs.GitHub, mockVCS)

	body := makePushWebhookBody("test-org/test-repo", "https://github.com/test-org/test-repo.git", "after-sha", "refs/heads/main")
	req := httptest.NewRequest(http.MethodPost, "/api/v1/webhooks/github", bytes.NewReader(body))
	req.Header.Set("X-GitHub-Event", "push")
	w := httptest.NewRecorder()

	handler.HandleGitHubWebhook(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	// Job was created
	require.Len(t, mockStore.CreateJobCalls, 1)

	// No UpdateJob calls since corndogs client is nil (no submission happened)
	assert.Len(t, mockStore.UpdateJobCalls, 0)
}

func TestWebhookHandler_UnknownGenericEvent_Ignored(t *testing.T) {
	mockStore := &WebhookMockStore{}
	handler := NewWebhookHandler(mockStore, nil)

	// A "labeled" PR action maps to EventUnknown
	prEvent := &vcs.WebhookEvent{
		Provider:     vcs.GitHub,
		EventType:    "pull_request",
		GenericEvent: vcs.EventUnknown,
		Repository: vcs.RepositoryInfo{
			FullName: "test-org/test-repo",
			CloneURL: "https://github.com/test-org/test-repo.git",
		},
		PullRequest: &vcs.PullRequestInfo{
			Number:  10,
			Title:   "Test PR",
			Action:  "labeled",
			HeadSHA: "sha456",
			HeadRef: "feature",
			BaseRef: "main",
		},
	}

	mockVCS := &MockVCSClient{
		ParseWebhookFunc: func(r *http.Request) (*vcs.WebhookEvent, error) {
			return prEvent, nil
		},
	}
	handler.AddVCSClient(vcs.GitHub, mockVCS)

	body := makePRWebhookBody("test-org/test-repo", "https://github.com/test-org/test-repo.git", "sha456", "feature", "main", 10)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/webhooks/github", bytes.NewReader(body))
	req.Header.Set("X-GitHub-Event", "pull_request")
	w := httptest.NewRecorder()

	handler.HandleGitHubWebhook(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	// No job should be created for an unknown generic event
	assert.Len(t, mockStore.CreateJobCalls, 0)
}

func TestWebhookHandler_PRClosed_FilteredByDefaultProject(t *testing.T) {
	// Default project allows: push, pull_request_opened, pull_request_updated, tag_created
	// A "pull_request_closed" event should be filtered out
	project := webhookTestProject()
	mockStore := &WebhookMockStore{
		GetProjectByRepoURLFunc: func(ctx context.Context, repoURL string) (*models.Project, error) {
			return project, nil
		},
	}
	handler := NewWebhookHandler(mockStore, nil)

	prEvent := &vcs.WebhookEvent{
		Provider:     vcs.GitHub,
		EventType:    "pull_request",
		GenericEvent: vcs.EventPullRequestClosed,
		Repository: vcs.RepositoryInfo{
			FullName: "test-org/test-repo",
			CloneURL: "https://github.com/test-org/test-repo.git",
		},
		PullRequest: &vcs.PullRequestInfo{
			Number:  5,
			Title:   "Closed PR",
			Action:  "closed",
			Merged:  false,
			HeadSHA: "sha789",
			HeadRef: "feature",
			BaseRef: "main",
		},
	}

	mockVCS := &MockVCSClient{
		ParseWebhookFunc: func(r *http.Request) (*vcs.WebhookEvent, error) {
			return prEvent, nil
		},
	}
	handler.AddVCSClient(vcs.GitHub, mockVCS)

	body := makePRWebhookBody("test-org/test-repo", "https://github.com/test-org/test-repo.git", "sha789", "feature", "main", 5)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/webhooks/github", bytes.NewReader(body))
	req.Header.Set("X-GitHub-Event", "pull_request")
	w := httptest.NewRecorder()

	handler.HandleGitHubWebhook(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	// No job created because pull_request_closed is not in AllowedEventTypes
	assert.Len(t, mockStore.CreateJobCalls, 0)
}

func TestWebhookHandler_PRMerged_AllowedWhenConfigured(t *testing.T) {
	// Project explicitly allows pull_request_merged
	project := webhookTestProject()
	project.AllowedEventTypes = append(project.AllowedEventTypes, "pull_request_merged")
	mockStore := &WebhookMockStore{
		GetProjectByRepoURLFunc: func(ctx context.Context, repoURL string) (*models.Project, error) {
			return project, nil
		},
	}
	handler := NewWebhookHandler(mockStore, nil)

	prEvent := &vcs.WebhookEvent{
		Provider:     vcs.GitHub,
		EventType:    "pull_request",
		GenericEvent: vcs.EventPullRequestMerged,
		Repository: vcs.RepositoryInfo{
			FullName: "test-org/test-repo",
			CloneURL: "https://github.com/test-org/test-repo.git",
		},
		PullRequest: &vcs.PullRequestInfo{
			Number:  7,
			Title:   "Merged PR",
			Action:  "closed",
			Merged:  true,
			HeadSHA: "sha-merged",
			HeadRef: "feature",
			BaseRef: "main",
		},
	}

	mockVCS := &MockVCSClient{
		ParseWebhookFunc: func(r *http.Request) (*vcs.WebhookEvent, error) {
			return prEvent, nil
		},
	}
	handler.AddVCSClient(vcs.GitHub, mockVCS)

	body := makePRWebhookBody("test-org/test-repo", "https://github.com/test-org/test-repo.git", "sha-merged", "feature", "main", 7)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/webhooks/github", bytes.NewReader(body))
	req.Header.Set("X-GitHub-Event", "pull_request")
	w := httptest.NewRecorder()

	handler.HandleGitHubWebhook(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	// Job should be created because pull_request_merged is allowed
	require.Len(t, mockStore.CreateJobCalls, 1)
	assert.Equal(t, "pull_request_merged", mockStore.CreateJobCalls[0].JobEnvVars["REACTORCIDE_EVENT_TYPE"])
}

func TestWebhookHandler_TagCreated_AllowedByDefault(t *testing.T) {
	project := webhookTestProject()
	mockStore := &WebhookMockStore{
		GetProjectByRepoURLFunc: func(ctx context.Context, repoURL string) (*models.Project, error) {
			return project, nil
		},
	}
	handler := NewWebhookHandler(mockStore, nil)

	// Tag pushes are processed through processPushEvent but with GenericEvent = tag_created
	pushEvent := &vcs.WebhookEvent{
		Provider:     vcs.GitHub,
		EventType:    "push",
		GenericEvent: vcs.EventTagCreated,
		Repository: vcs.RepositoryInfo{
			FullName: "test-org/test-repo",
			CloneURL: "https://github.com/test-org/test-repo.git",
		},
		Push: &vcs.PushInfo{
			Ref:    "refs/tags/v1.0.0",
			Before: "0000000000000000000000000000000000000000",
			After:  "tag-sha-1234",
			Commits: []vcs.Commit{
				{ID: "tag-sha-1234", Message: "Release v1.0.0"},
			},
		},
	}

	mockVCS := &MockVCSClient{
		ParseWebhookFunc: func(r *http.Request) (*vcs.WebhookEvent, error) {
			return pushEvent, nil
		},
	}
	handler.AddVCSClient(vcs.GitHub, mockVCS)

	body := makePushWebhookBody("test-org/test-repo", "https://github.com/test-org/test-repo.git", "tag-sha-1234", "refs/tags/v1.0.0")
	req := httptest.NewRequest(http.MethodPost, "/api/v1/webhooks/github", bytes.NewReader(body))
	req.Header.Set("X-GitHub-Event", "push")
	w := httptest.NewRecorder()

	handler.HandleGitHubWebhook(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	// tag_created is in the default AllowedEventTypes, but target branch filtering
	// uses the tag name stripped of refs/tags/ prefix - project has TargetBranches=["main"]
	// so the tag "v1.0.0" won't match. Let's verify no job was created.
	// To test that tag_created events ARE processed, we need empty TargetBranches.
	assert.Len(t, mockStore.CreateJobCalls, 0)
}

func TestWebhookHandler_TagCreated_WithEmptyTargetBranches(t *testing.T) {
	project := webhookTestProject()
	project.TargetBranches = []string{} // allow all branches/tags
	mockStore := &WebhookMockStore{
		GetProjectByRepoURLFunc: func(ctx context.Context, repoURL string) (*models.Project, error) {
			return project, nil
		},
	}
	handler := NewWebhookHandler(mockStore, nil)

	pushEvent := &vcs.WebhookEvent{
		Provider:     vcs.GitHub,
		EventType:    "push",
		GenericEvent: vcs.EventTagCreated,
		Repository: vcs.RepositoryInfo{
			FullName: "test-org/test-repo",
			CloneURL: "https://github.com/test-org/test-repo.git",
		},
		Push: &vcs.PushInfo{
			Ref:    "refs/tags/v1.0.0",
			Before: "0000000000000000000000000000000000000000",
			After:  "tag-sha-1234",
			Commits: []vcs.Commit{
				{ID: "tag-sha-1234", Message: "Release v1.0.0"},
			},
		},
	}

	mockVCS := &MockVCSClient{
		ParseWebhookFunc: func(r *http.Request) (*vcs.WebhookEvent, error) {
			return pushEvent, nil
		},
	}
	handler.AddVCSClient(vcs.GitHub, mockVCS)

	body := makePushWebhookBody("test-org/test-repo", "https://github.com/test-org/test-repo.git", "tag-sha-1234", "refs/tags/v1.0.0")
	req := httptest.NewRequest(http.MethodPost, "/api/v1/webhooks/github", bytes.NewReader(body))
	req.Header.Set("X-GitHub-Event", "push")
	w := httptest.NewRecorder()

	handler.HandleGitHubWebhook(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	require.Len(t, mockStore.CreateJobCalls, 1)
	assert.Equal(t, "tag_created", mockStore.CreateJobCalls[0].JobEnvVars["REACTORCIDE_EVENT_TYPE"])
}

func TestWebhookHandler_PRSynchronize_CreatesJob(t *testing.T) {
	project := webhookTestProject()
	mockStore := &WebhookMockStore{
		GetProjectByRepoURLFunc: func(ctx context.Context, repoURL string) (*models.Project, error) {
			return project, nil
		},
	}
	handler := NewWebhookHandler(mockStore, nil)

	prEvent := &vcs.WebhookEvent{
		Provider:     vcs.GitHub,
		EventType:    "pull_request",
		GenericEvent: vcs.EventPullRequestUpdated,
		Repository: vcs.RepositoryInfo{
			FullName: "test-org/test-repo",
			CloneURL: "https://github.com/test-org/test-repo.git",
		},
		PullRequest: &vcs.PullRequestInfo{
			Number:  42,
			Title:   "Updated PR",
			Action:  "synchronize",
			HeadSHA: "new-sha",
			HeadRef: "feature",
			BaseRef: "main",
		},
	}

	mockVCS := &MockVCSClient{
		ParseWebhookFunc: func(r *http.Request) (*vcs.WebhookEvent, error) {
			return prEvent, nil
		},
	}
	handler.AddVCSClient(vcs.GitHub, mockVCS)

	body := makePRWebhookBody("test-org/test-repo", "https://github.com/test-org/test-repo.git", "new-sha", "feature", "main", 42)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/webhooks/github", bytes.NewReader(body))
	req.Header.Set("X-GitHub-Event", "pull_request")
	w := httptest.NewRecorder()

	handler.HandleGitHubWebhook(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	require.Len(t, mockStore.CreateJobCalls, 1)
	assert.Equal(t, "pull_request_updated", mockStore.CreateJobCalls[0].JobEnvVars["REACTORCIDE_EVENT_TYPE"])
}

func TestWebhookHandler_EvalJob_WithDedicatedCISourceRepo(t *testing.T) {
	project := webhookTestProject()
	project.DefaultCISourceType = models.SourceTypeGit
	project.DefaultCISourceURL = "https://github.com/test-org/ci-pipelines.git"
	project.DefaultCISourceRef = "main"

	mockStore := &WebhookMockStore{
		GetProjectByRepoURLFunc: func(ctx context.Context, repoURL string) (*models.Project, error) {
			return project, nil
		},
	}
	handler := NewWebhookHandler(mockStore, nil)

	prEvent := &vcs.WebhookEvent{
		Provider:     vcs.GitHub,
		EventType:    "pull_request",
		GenericEvent: vcs.EventPullRequestOpened,
		Repository: vcs.RepositoryInfo{
			FullName: "test-org/test-repo",
			CloneURL: "https://github.com/test-org/test-repo.git",
		},
		PullRequest: &vcs.PullRequestInfo{
			Number:  99,
			Title:   "Feature PR",
			Action:  "opened",
			HeadSHA: "pr-sha-999",
			HeadRef: "feature-x",
			BaseRef: "main",
		},
	}

	mockVCS := &MockVCSClient{
		ParseWebhookFunc: func(r *http.Request) (*vcs.WebhookEvent, error) {
			return prEvent, nil
		},
	}
	handler.AddVCSClient(vcs.GitHub, mockVCS)

	body := makePRWebhookBody("test-org/test-repo", "https://github.com/test-org/test-repo.git", "pr-sha-999", "feature-x", "main", 99)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/webhooks/github", bytes.NewReader(body))
	req.Header.Set("X-GitHub-Event", "pull_request")
	w := httptest.NewRecorder()

	handler.HandleGitHubWebhook(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	require.Len(t, mockStore.CreateJobCalls, 1)
	createdJob := mockStore.CreateJobCalls[0]

	// Source should be the code repo (untrusted)
	require.NotNil(t, createdJob.SourceURL)
	assert.Equal(t, "https://github.com/test-org/test-repo.git", *createdJob.SourceURL)
	require.NotNil(t, createdJob.SourceRef)
	assert.Equal(t, "pr-sha-999", *createdJob.SourceRef)

	// CI source should be the dedicated CI repo (trusted)
	require.NotNil(t, createdJob.CISourceURL)
	assert.Equal(t, "https://github.com/test-org/ci-pipelines.git", *createdJob.CISourceURL)
	require.NotNil(t, createdJob.CISourceRef)
	assert.Equal(t, "main", *createdJob.CISourceRef)

	// Env vars should include CI source info
	assert.Equal(t, "https://github.com/test-org/ci-pipelines.git", createdJob.JobEnvVars["REACTORCIDE_CI_SOURCE_URL"])
	assert.Equal(t, "main", createdJob.JobEnvVars["REACTORCIDE_CI_SOURCE_REF"])
}
