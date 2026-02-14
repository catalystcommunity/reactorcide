package vcs

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

// MockClient is a mock VCS client for testing
type MockClient struct {
	mock.Mock
}

func (m *MockClient) ParseWebhook(r *http.Request) (*WebhookEvent, error) {
	args := m.Called(r)
	if event, ok := args.Get(0).(*WebhookEvent); ok {
		return event, args.Error(1)
	}
	return nil, args.Error(1)
}

func (m *MockClient) ValidateWebhook(r *http.Request, secret string) error {
	args := m.Called(r, secret)
	return args.Error(0)
}

func (m *MockClient) UpdateCommitStatus(ctx context.Context, repo string, update StatusUpdate) error {
	args := m.Called(ctx, repo, update)
	return args.Error(0)
}

func (m *MockClient) UpdatePRComment(ctx context.Context, repo string, prNumber int, comment string) error {
	args := m.Called(ctx, repo, prNumber, comment)
	return args.Error(0)
}

func (m *MockClient) GetPRInfo(ctx context.Context, repo string, prNumber int) (*PullRequestInfo, error) {
	args := m.Called(ctx, repo, prNumber)
	if info, ok := args.Get(0).(*PullRequestInfo); ok {
		return info, args.Error(1)
	}
	return nil, args.Error(1)
}

func (m *MockClient) GetProvider() Provider {
	args := m.Called()
	return args.Get(0).(Provider)
}

func TestJobStatusUpdater_UpdateJobStatus(t *testing.T) {
	updater := NewJobStatusUpdater()
	mockClient := new(MockClient)

	// Add the mock client
	updater.AddVCSClient(GitHub, mockClient)

	tests := []struct {
		name           string
		job            *models.Job
		expectUpdate   bool
		expectedStatus StatusState
	}{
		{
			name: "job_with_vcs_metadata_success",
			job: &models.Job{
				JobID:  "test-job-1",
				Status: "completed",
				Notes: `{"vcs_provider":"github","repo":"test/repo","commit_sha":"abc123"}`,
				ExitCode: func() *int { i := 0; return &i }(),
			},
			expectUpdate:   true,
			expectedStatus: StatusSuccess,
		},
		{
			name: "job_with_vcs_metadata_failure",
			job: &models.Job{
				JobID:     "test-job-2",
				Status:    "failed",
				Notes:     `{"vcs_provider":"github","repo":"test/repo","commit_sha":"def456"}`,
				LastError: "Test error",
			},
			expectUpdate:   true,
			expectedStatus: StatusFailure,
		},
		{
			name: "job_without_vcs_metadata",
			job: &models.Job{
				JobID:  "test-job-3",
				Status: "completed",
				Notes:  "", // No VCS metadata
			},
			expectUpdate: false,
		},
		{
			name: "job_with_pr_metadata",
			job: &models.Job{
				JobID:  "test-job-4",
				Status: "completed",
				Notes:  `{"vcs_provider":"github","repo":"test/repo","pr_number":123,"commit_sha":"ghi789"}`,
				ExitCode: func() *int { i := 0; return &i }(),
				StartedAt: func() *time.Time { t := time.Now().Add(-5 * time.Minute); return &t }(),
				CompletedAt: func() *time.Time { t := time.Now(); return &t }(),
			},
			expectUpdate:   true,
			expectedStatus: StatusSuccess,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.expectUpdate {
				// Parse metadata to check if PR comment is expected
				var metadata JobMetadata
				json.Unmarshal([]byte(tt.job.Notes), &metadata)

				mockClient.On("UpdateCommitStatus", mock.Anything, metadata.Repo, mock.MatchedBy(func(update StatusUpdate) bool {
					return update.State == tt.expectedStatus && update.SHA == metadata.CommitSHA
				})).Return(nil).Once()

				if metadata.PRNumber > 0 && updater.isJobComplete(tt.job.Status) {
					mockClient.On("UpdatePRComment", mock.Anything, metadata.Repo, metadata.PRNumber, mock.AnythingOfType("string")).Return(nil).Once()
				}
			}

			err := updater.UpdateJobStatus(context.Background(), tt.job)
			assert.NoError(t, err)

			if tt.expectUpdate {
				mockClient.AssertExpectations(t)
			}
		})
	}
}

func TestJobStatusUpdater_MapJobStatusToVCSStatus(t *testing.T) {
	updater := NewJobStatusUpdater()

	tests := []struct {
		jobStatus string
		expected  StatusState
	}{
		{"submitted", StatusPending},
		{"queued", StatusPending},
		{"running", StatusRunning},
		{"completed", StatusSuccess},
		{"failed", StatusFailure},
		{"cancelled", StatusCancelled},
		{"timeout", StatusError},
		{"unknown", StatusError},
	}

	for _, tt := range tests {
		t.Run(tt.jobStatus, func(t *testing.T) {
			result := updater.mapJobStatusToVCSStatus(tt.jobStatus)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestJobStatusUpdater_GetStatusDescription(t *testing.T) {
	updater := NewJobStatusUpdater()

	tests := []struct {
		name     string
		job      *models.Job
		expected string
	}{
		{
			name: "submitted",
			job: &models.Job{
				Status: "submitted",
			},
			expected: "CI build submitted",
		},
		{
			name: "completed_success",
			job: &models.Job{
				Status: "completed",
				ExitCode: func() *int { i := 0; return &i }(),
			},
			expected: "CI build passed",
		},
		{
			name: "completed_with_exit_code",
			job: &models.Job{
				Status: "completed",
				ExitCode: func() *int { i := 1; return &i }(),
			},
			expected: "CI build completed with exit code 1",
		},
		{
			name: "failed_with_error",
			job: &models.Job{
				Status:    "failed",
				LastError: "Container crashed",
			},
			expected: "CI build failed: Container crashed",
		},
		{
			name: "failed_with_long_error",
			job: &models.Job{
				Status:    "failed",
				LastError: "This is a very long error message that should be truncated to prevent the status from being too long",
			},
			expected: "CI build failed: This is a very long error message that shoul...",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := updater.getStatusDescription(tt.job)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestJobStatusUpdater_IsJobComplete(t *testing.T) {
	updater := NewJobStatusUpdater()

	tests := []struct {
		status   string
		expected bool
	}{
		{"completed", true},
		{"failed", true},
		{"cancelled", true},
		{"timeout", true},
		{"running", false},
		{"submitted", false},
		{"queued", false},
	}

	for _, tt := range tests {
		t.Run(tt.status, func(t *testing.T) {
			result := updater.isJobComplete(tt.status)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestJobStatusUpdater_PerProjectToken(t *testing.T) {
	projectID := "project-123"
	perProjectClient := new(MockClient)
	globalClient := new(MockClient)

	updater := NewJobStatusUpdater()
	updater.AddVCSClient(GitHub, globalClient)

	// Wire per-project resolution
	updater.SetProjectLookup(func(ctx context.Context, pid string) (*models.Project, error) {
		assert.Equal(t, projectID, pid)
		return &models.Project{
			ProjectID:      projectID,
			VCSTokenSecret: "vcs/github:token",
		}, nil
	})
	updater.SetTokenResolver(func(ctx context.Context, secretRef string) (string, error) {
		assert.Equal(t, "vcs/github:token", secretRef)
		return "per-project-token-abc", nil
	})
	updater.SetClientFactory(func(provider Provider, token string) (Client, error) {
		assert.Equal(t, GitHub, provider)
		assert.Equal(t, "per-project-token-abc", token)
		return perProjectClient, nil
	})

	job := &models.Job{
		JobID:     "test-job",
		Status:    "completed",
		ProjectID: &projectID,
		Notes:     `{"vcs_provider":"github","repo":"org/repo","commit_sha":"abc123"}`,
		ExitCode:  func() *int { i := 0; return &i }(),
	}

	// Expect per-project client to be called, NOT the global client
	perProjectClient.On("UpdateCommitStatus", mock.Anything, "org/repo", mock.MatchedBy(func(u StatusUpdate) bool {
		return u.State == StatusSuccess && u.SHA == "abc123"
	})).Return(nil).Once()

	err := updater.UpdateJobStatus(context.Background(), job)
	assert.NoError(t, err)

	perProjectClient.AssertExpectations(t)
	globalClient.AssertNotCalled(t, "UpdateCommitStatus")
}

func TestJobStatusUpdater_FallsBackToGlobalWhenNoProjectToken(t *testing.T) {
	projectID := "project-456"
	globalClient := new(MockClient)

	updater := NewJobStatusUpdater()
	updater.AddVCSClient(GitHub, globalClient)

	// Wire per-project resolution that returns empty token
	updater.SetProjectLookup(func(ctx context.Context, pid string) (*models.Project, error) {
		return &models.Project{
			ProjectID:      projectID,
			VCSTokenSecret: "", // no per-project token
		}, nil
	})
	updater.SetTokenResolver(func(ctx context.Context, secretRef string) (string, error) {
		return "", nil
	})
	updater.SetClientFactory(func(provider Provider, token string) (Client, error) {
		t.Fatal("client factory should not be called when no token")
		return nil, nil
	})

	job := &models.Job{
		JobID:     "test-job",
		Status:    "completed",
		ProjectID: &projectID,
		Notes:     `{"vcs_provider":"github","repo":"org/repo","commit_sha":"abc123"}`,
		ExitCode:  func() *int { i := 0; return &i }(),
	}

	// Global client should be used as fallback
	globalClient.On("UpdateCommitStatus", mock.Anything, "org/repo", mock.Anything).Return(nil).Once()

	err := updater.UpdateJobStatus(context.Background(), job)
	assert.NoError(t, err)

	globalClient.AssertExpectations(t)
}

func TestJobStatusUpdater_FallsBackToGlobalOnResolverError(t *testing.T) {
	projectID := "project-789"
	globalClient := new(MockClient)

	updater := NewJobStatusUpdater()
	updater.AddVCSClient(GitHub, globalClient)

	// Wire per-project resolution that fails
	updater.SetProjectLookup(func(ctx context.Context, pid string) (*models.Project, error) {
		return &models.Project{
			ProjectID:      projectID,
			VCSTokenSecret: "vcs/github:token",
		}, nil
	})
	updater.SetTokenResolver(func(ctx context.Context, secretRef string) (string, error) {
		return "", assert.AnError
	})
	updater.SetClientFactory(func(provider Provider, token string) (Client, error) {
		t.Fatal("client factory should not be called on resolver error")
		return nil, nil
	})

	job := &models.Job{
		JobID:     "test-job",
		Status:    "failed",
		ProjectID: &projectID,
		Notes:     `{"vcs_provider":"github","repo":"org/repo","commit_sha":"abc123"}`,
	}

	// Global client should be used as fallback
	globalClient.On("UpdateCommitStatus", mock.Anything, "org/repo", mock.Anything).Return(nil).Once()

	err := updater.UpdateJobStatus(context.Background(), job)
	assert.NoError(t, err)

	globalClient.AssertExpectations(t)
}