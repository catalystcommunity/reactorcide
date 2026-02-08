package handlers

import (
	"testing"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/vcs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func evalTestProject() *models.Project {
	return &models.Project{
		ProjectID:              "proj-123",
		Name:                   "test-project",
		RepoURL:                "github.com/org/repo",
		Enabled:                true,
		TargetBranches:         []string{"main"},
		AllowedEventTypes:      []string{"push", "pull_request_opened", "pull_request_updated", "tag_created"},
		DefaultCISourceType:    models.SourceTypeGit,
		DefaultCISourceURL:     "https://github.com/org/ci-repo.git",
		DefaultCISourceRef:     "main",
		DefaultRunnerImage:     "quay.io/catalystcommunity/reactorcide_runner",
		DefaultTimeoutSeconds:  1800,
		DefaultQueueName:       "reactorcide-jobs",
	}
}

func TestBuildEvalJob_PROpened(t *testing.T) {
	project := evalTestProject()
	event := &vcs.WebhookEvent{
		Provider:     vcs.GitHub,
		EventType:    "pull_request",
		GenericEvent: vcs.EventPullRequestOpened,
		Repository: vcs.RepositoryInfo{
			FullName: "org/repo",
			CloneURL: "https://github.com/org/repo.git",
		},
		PullRequest: &vcs.PullRequestInfo{
			Number:  42,
			Title:   "Add feature",
			Action:  "opened",
			HeadSHA: "abc1234567890",
			HeadRef: "feature-branch",
			BaseRef: "main",
		},
	}

	job := BuildEvalJob(project, event)

	// Job metadata
	assert.Equal(t, "proj-123", *job.ProjectID)
	assert.Equal(t, "eval: PR #42 opened on org/repo", job.Name)
	assert.Contains(t, job.Description, "pull_request_opened")

	// Source (untrusted code)
	require.NotNil(t, job.SourceURL)
	assert.Equal(t, "https://github.com/org/repo.git", *job.SourceURL)
	require.NotNil(t, job.SourceRef)
	assert.Equal(t, "abc1234567890", *job.SourceRef)
	require.NotNil(t, job.SourceType)
	assert.Equal(t, models.SourceTypeGit, *job.SourceType)

	// CI source (trusted CI code)
	require.NotNil(t, job.CISourceURL)
	assert.Equal(t, "https://github.com/org/ci-repo.git", *job.CISourceURL)
	require.NotNil(t, job.CISourceRef)
	assert.Equal(t, "main", *job.CISourceRef)
	require.NotNil(t, job.CISourceType)
	assert.Equal(t, models.SourceTypeGit, *job.CISourceType)

	// Job configuration
	assert.Equal(t, "runnerlib eval --event-type $REACTORCIDE_EVENT_TYPE --branch $REACTORCIDE_BRANCH", job.JobCommand)
	assert.Equal(t, "quay.io/catalystcommunity/reactorcide_runner", job.RunnerImage)
	assert.Equal(t, 10, job.Priority) // PR priority
	assert.Equal(t, "reactorcide-jobs", job.QueueName)
	assert.Equal(t, 1800, job.TimeoutSeconds)

	// Env vars
	assert.Equal(t, "true", job.JobEnvVars["REACTORCIDE_CI"])
	assert.Equal(t, "github", job.JobEnvVars["REACTORCIDE_PROVIDER"])
	assert.Equal(t, "pull_request_opened", job.JobEnvVars["REACTORCIDE_EVENT_TYPE"])
	assert.Equal(t, "org/repo", job.JobEnvVars["REACTORCIDE_REPO"])
	assert.Equal(t, "https://github.com/org/repo.git", job.JobEnvVars["REACTORCIDE_SOURCE_URL"])
	assert.Equal(t, "abc1234567890", job.JobEnvVars["REACTORCIDE_SHA"])
	assert.Equal(t, "main", job.JobEnvVars["REACTORCIDE_BRANCH"])
	assert.Equal(t, "42", job.JobEnvVars["REACTORCIDE_PR_NUMBER"])
	assert.Equal(t, "feature-branch", job.JobEnvVars["REACTORCIDE_PR_REF"])
	assert.Equal(t, "main", job.JobEnvVars["REACTORCIDE_PR_BASE_REF"])
	assert.Equal(t, "https://github.com/org/ci-repo.git", job.JobEnvVars["REACTORCIDE_CI_SOURCE_URL"])
	assert.Equal(t, "main", job.JobEnvVars["REACTORCIDE_CI_SOURCE_REF"])
}

func TestBuildEvalJob_PRUpdated(t *testing.T) {
	project := evalTestProject()
	event := &vcs.WebhookEvent{
		Provider:     vcs.GitHub,
		EventType:    "pull_request",
		GenericEvent: vcs.EventPullRequestUpdated,
		Repository: vcs.RepositoryInfo{
			FullName: "org/repo",
			CloneURL: "https://github.com/org/repo.git",
		},
		PullRequest: &vcs.PullRequestInfo{
			Number:  42,
			Title:   "Add feature",
			Action:  "synchronize",
			HeadSHA: "newsha999",
			HeadRef: "feature-branch",
			BaseRef: "main",
		},
	}

	job := BuildEvalJob(project, event)

	assert.Equal(t, "eval: PR #42 updated on org/repo", job.Name)
	assert.Equal(t, "pull_request_updated", job.JobEnvVars["REACTORCIDE_EVENT_TYPE"])
	assert.Equal(t, "newsha999", *job.SourceRef)
	assert.Equal(t, 10, job.Priority)
}

func TestBuildEvalJob_PRMerged(t *testing.T) {
	project := evalTestProject()
	event := &vcs.WebhookEvent{
		Provider:     vcs.GitHub,
		EventType:    "pull_request",
		GenericEvent: vcs.EventPullRequestMerged,
		Repository: vcs.RepositoryInfo{
			FullName: "org/repo",
			CloneURL: "https://github.com/org/repo.git",
		},
		PullRequest: &vcs.PullRequestInfo{
			Number:  10,
			Title:   "Merged PR",
			Action:  "closed",
			Merged:  true,
			HeadSHA: "merge-sha",
			HeadRef: "feature",
			BaseRef: "main",
		},
	}

	job := BuildEvalJob(project, event)

	assert.Equal(t, "eval: PR #10 merged on org/repo", job.Name)
	assert.Equal(t, "pull_request_merged", job.JobEnvVars["REACTORCIDE_EVENT_TYPE"])
}

func TestBuildEvalJob_PRClosed(t *testing.T) {
	project := evalTestProject()
	event := &vcs.WebhookEvent{
		Provider:     vcs.GitHub,
		EventType:    "pull_request",
		GenericEvent: vcs.EventPullRequestClosed,
		Repository: vcs.RepositoryInfo{
			FullName: "org/repo",
			CloneURL: "https://github.com/org/repo.git",
		},
		PullRequest: &vcs.PullRequestInfo{
			Number:  10,
			Title:   "Closed PR",
			Action:  "closed",
			Merged:  false,
			HeadSHA: "close-sha",
			HeadRef: "feature",
			BaseRef: "main",
		},
	}

	job := BuildEvalJob(project, event)

	assert.Equal(t, "eval: PR #10 closed on org/repo", job.Name)
	assert.Equal(t, "pull_request_closed", job.JobEnvVars["REACTORCIDE_EVENT_TYPE"])
}

func TestBuildEvalJob_PushEvent(t *testing.T) {
	project := evalTestProject()
	event := &vcs.WebhookEvent{
		Provider:     vcs.GitHub,
		EventType:    "push",
		GenericEvent: vcs.EventPush,
		Repository: vcs.RepositoryInfo{
			FullName: "org/repo",
			CloneURL: "https://github.com/org/repo.git",
		},
		Push: &vcs.PushInfo{
			Ref:    "refs/heads/main",
			Before: "before-sha",
			After:  "after1234567890",
			Commits: []vcs.Commit{
				{ID: "after1234567890", Message: "update things"},
			},
		},
	}

	job := BuildEvalJob(project, event)

	// Job metadata
	assert.Equal(t, "eval: push to main (after12) on org/repo", job.Name)
	assert.Contains(t, job.Description, "push")

	// Source
	require.NotNil(t, job.SourceURL)
	assert.Equal(t, "https://github.com/org/repo.git", *job.SourceURL)
	require.NotNil(t, job.SourceRef)
	assert.Equal(t, "after1234567890", *job.SourceRef)

	// CI source
	require.NotNil(t, job.CISourceURL)
	assert.Equal(t, "https://github.com/org/ci-repo.git", *job.CISourceURL)

	// Configuration
	assert.Equal(t, 5, job.Priority) // Push priority
	assert.Equal(t, "runnerlib eval --event-type $REACTORCIDE_EVENT_TYPE --branch $REACTORCIDE_BRANCH", job.JobCommand)

	// Env vars
	assert.Equal(t, "push", job.JobEnvVars["REACTORCIDE_EVENT_TYPE"])
	assert.Equal(t, "main", job.JobEnvVars["REACTORCIDE_BRANCH"])
	assert.Equal(t, "after1234567890", job.JobEnvVars["REACTORCIDE_SHA"])
	// Push events should not have PR-specific vars
	assert.Nil(t, job.JobEnvVars["REACTORCIDE_PR_NUMBER"])
	assert.Nil(t, job.JobEnvVars["REACTORCIDE_PR_REF"])
	assert.Nil(t, job.JobEnvVars["REACTORCIDE_PR_BASE_REF"])
}

func TestBuildEvalJob_TagCreated(t *testing.T) {
	project := evalTestProject()
	event := &vcs.WebhookEvent{
		Provider:     vcs.GitHub,
		EventType:    "push",
		GenericEvent: vcs.EventTagCreated,
		Repository: vcs.RepositoryInfo{
			FullName: "org/repo",
			CloneURL: "https://github.com/org/repo.git",
		},
		Push: &vcs.PushInfo{
			Ref:   "refs/tags/v1.0.0",
			After: "tagsha1234567890",
		},
	}

	job := BuildEvalJob(project, event)

	assert.Equal(t, "eval: push to v1.0.0 (tagsha1) on org/repo", job.Name)
	assert.Equal(t, "tag_created", job.JobEnvVars["REACTORCIDE_EVENT_TYPE"])
	assert.Equal(t, "v1.0.0", job.JobEnvVars["REACTORCIDE_BRANCH"])
	assert.Equal(t, "tagsha1234567890", job.JobEnvVars["REACTORCIDE_SHA"])
}

func TestBuildEvalJob_SameRepoMode(t *testing.T) {
	// When project has no DefaultCISourceURL, fall back to source repo
	project := evalTestProject()
	project.DefaultCISourceURL = ""
	project.DefaultCISourceRef = ""

	event := &vcs.WebhookEvent{
		Provider:     vcs.GitHub,
		EventType:    "push",
		GenericEvent: vcs.EventPush,
		Repository: vcs.RepositoryInfo{
			FullName: "org/repo",
			CloneURL: "https://github.com/org/repo.git",
		},
		Push: &vcs.PushInfo{
			Ref:   "refs/heads/main",
			After: "sha1234567890abc",
		},
	}

	job := BuildEvalJob(project, event)

	// CI source should match the source repo in same-repo mode
	require.NotNil(t, job.CISourceURL)
	assert.Equal(t, "https://github.com/org/repo.git", *job.CISourceURL)
	require.NotNil(t, job.CISourceRef)
	assert.Equal(t, "sha1234567890abc", *job.CISourceRef)
	require.NotNil(t, job.CISourceType)
	assert.Equal(t, models.SourceTypeGit, *job.CISourceType)

	// Env vars reflect same-repo mode
	assert.Equal(t, "https://github.com/org/repo.git", job.JobEnvVars["REACTORCIDE_CI_SOURCE_URL"])
	assert.Equal(t, "sha1234567890abc", job.JobEnvVars["REACTORCIDE_CI_SOURCE_REF"])
}

func TestBuildEvalJob_CustomJobCommand(t *testing.T) {
	project := evalTestProject()
	project.DefaultJobCommand = "make ci-eval"

	event := &vcs.WebhookEvent{
		Provider:     vcs.GitHub,
		EventType:    "push",
		GenericEvent: vcs.EventPush,
		Repository: vcs.RepositoryInfo{
			FullName: "org/repo",
			CloneURL: "https://github.com/org/repo.git",
		},
		Push: &vcs.PushInfo{
			Ref:   "refs/heads/main",
			After: "sha123",
		},
	}

	job := BuildEvalJob(project, event)

	assert.Equal(t, "make ci-eval", job.JobCommand)
}

func TestBuildEvalJob_DefaultTimeout(t *testing.T) {
	project := evalTestProject()
	project.DefaultTimeoutSeconds = 0

	event := &vcs.WebhookEvent{
		Provider:     vcs.GitHub,
		EventType:    "push",
		GenericEvent: vcs.EventPush,
		Repository: vcs.RepositoryInfo{
			FullName: "org/repo",
			CloneURL: "https://github.com/org/repo.git",
		},
		Push: &vcs.PushInfo{
			Ref:   "refs/heads/main",
			After: "sha123",
		},
	}

	job := BuildEvalJob(project, event)

	// When project timeout is 0, the job should use Go's zero value (0)
	// and let the database default handle it
	assert.Equal(t, 0, job.TimeoutSeconds)
}

func TestBuildEvalJob_CISourceTypeDefaultsToGit(t *testing.T) {
	project := evalTestProject()
	project.DefaultCISourceType = "" // empty
	project.DefaultCISourceURL = "https://github.com/org/ci-repo.git"

	event := &vcs.WebhookEvent{
		Provider:     vcs.GitHub,
		EventType:    "push",
		GenericEvent: vcs.EventPush,
		Repository: vcs.RepositoryInfo{
			FullName: "org/repo",
			CloneURL: "https://github.com/org/repo.git",
		},
		Push: &vcs.PushInfo{
			Ref:   "refs/heads/main",
			After: "sha123",
		},
	}

	job := BuildEvalJob(project, event)

	require.NotNil(t, job.CISourceType)
	assert.Equal(t, models.SourceTypeGit, *job.CISourceType)
}

func TestActionLabel(t *testing.T) {
	tests := []struct {
		eventType vcs.EventType
		expected  string
	}{
		{vcs.EventPullRequestOpened, "opened"},
		{vcs.EventPullRequestUpdated, "updated"},
		{vcs.EventPullRequestMerged, "merged"},
		{vcs.EventPullRequestClosed, "closed"},
		{vcs.EventPush, "push"},
		{vcs.EventTagCreated, "tag_created"},
	}

	for _, tt := range tests {
		t.Run(string(tt.eventType), func(t *testing.T) {
			assert.Equal(t, tt.expected, actionLabel(tt.eventType))
		})
	}
}

func TestExtractBranchOrTag(t *testing.T) {
	tests := []struct {
		ref      string
		expected string
	}{
		{"refs/heads/main", "main"},
		{"refs/heads/feature/my-branch", "feature/my-branch"},
		{"refs/tags/v1.0.0", "v1.0.0"},
		{"refs/tags/release/2.0", "release/2.0"},
		{"something-else", "something-else"},
	}

	for _, tt := range tests {
		t.Run(tt.ref, func(t *testing.T) {
			assert.Equal(t, tt.expected, extractBranchOrTag(tt.ref))
		})
	}
}
