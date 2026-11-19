package vcs

import (
	"bytes"
	"net/http/httptest"
	"testing"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func githubPullRequestPayload(action string, draft bool) string {
	draftText := "false"
	if draft {
		draftText = "true"
	}
	return `{"action":"` + action + `","number":5,"pull_request":{"number":5,"state":"open","draft":` + draftText + `,
		"head":{"sha":"h1","ref":"feature","repo":{"full_name":"o/r"}},"base":{"sha":"b1","ref":"main","repo":{"full_name":"o/r"}},
		"user":{"login":"dev"}},"repository":{"full_name":"o/r","clone_url":"https://github.com/o/r.git"},"sender":{"login":"dev"}}`
}

func TestGitHubDraftPullRequests(t *testing.T) {
	client, err := NewGitHubClient(Config{Provider: GitHub})
	require.NoError(t, err)
	parse := func(action string, draft bool) *WebhookEvent {
		req := httptest.NewRequest("POST", "/webhook", bytes.NewBufferString(githubPullRequestPayload(action, draft)))
		req.Header.Set("X-GitHub-Event", "pull_request")
		event, err := client.ParseWebhook(req)
		require.NoError(t, err)
		return event
	}

	opened := parse("opened", true)
	assert.True(t, opened.PullRequest.Draft)
	assert.Equal(t, EventPullRequestOpened, opened.GenericEvent)

	ready := parse("ready_for_review", false)
	assert.False(t, ready.PullRequest.Draft)
	assert.Equal(t, EventPullRequestUpdated, ready.GenericEvent, "ready_for_review is an update with draft=false")

	converted := parse("converted_to_draft", true)
	assert.Equal(t, EventUnknown, converted.GenericEvent, "converted_to_draft starts nothing")

	synchronized := parse("synchronize", false)
	assert.False(t, synchronized.PullRequest.Draft)
}

func TestGitLabDraftMergeRequests(t *testing.T) {
	client, err := NewGitLabClient(Config{Provider: GitLab})
	require.NoError(t, err)
	for _, field := range []string{"draft", "work_in_progress"} {
		payload := `{"object_kind":"merge_request","user":{"username":"dev"},"project":{"path_with_namespace":"o/r","git_http_url":"https://gitlab.com/o/r.git"},
			"object_attributes":{"iid":3,"state":"opened","source_branch":"feature","target_branch":"main","last_commit":{"id":"h1"},"` + field + `":true}}`
		req := httptest.NewRequest("POST", "/webhook", bytes.NewBufferString(payload))
		req.Header.Set("X-Gitlab-Event", "Merge Request Hook")
		event, err := client.ParseWebhook(req)
		require.NoError(t, err)
		require.NotNil(t, event.PullRequest)
		assert.True(t, event.PullRequest.Draft, field)
	}
}

func TestEventChecks_SkippedAndRateLimited(t *testing.T) {
	// A skipped draft is pending: a success would pass branch protection
	// for a head that ran nothing. ready_for_review posts a new status.
	skipped := ComputeEventChecks(EventChecksInput{EventStatus: models.CIEventStatusSkipped, SkipKind: models.SkipReasonDraft,
		SkipReason: "draft pull request (draft_prs: skip)"})
	assert.Equal(t, StatusPending, skipped.State)
	assert.Equal(t, "Skipped: draft pull request", skipped.Description)

	shed := ComputeEventChecks(EventChecksInput{EventStatus: models.CIEventStatusSkipped, SkipKind: models.SkipReasonShed,
		SkipReason: "draft pull request while shed level 2 (draft_prs: skip_when_shedding)"})
	assert.Equal(t, StatusPending, shed.State)
	assert.Equal(t, "Skipped: draft pull request while shed level 2 (draft_prs: skip_when_shedding)", shed.Description)

	unknown := ComputeEventChecks(EventChecksInput{EventStatus: models.CIEventStatusSkipped})
	assert.Equal(t, StatusPending, unknown.State)
	assert.Equal(t, "Skipped: not evaluated", unknown.Description)

	limited := ComputeEventChecks(EventChecksInput{EventStatus: models.CIEventStatusRateLimited})
	assert.Equal(t, StatusPending, limited.State)
	assert.Contains(t, limited.Description, "Rate limited")
}
