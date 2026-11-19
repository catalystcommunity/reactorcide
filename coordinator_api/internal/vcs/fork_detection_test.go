package vcs

import (
	"bytes"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func parseGitHubPR(t *testing.T, head, base string) *PullRequestInfo {
	t.Helper()
	client, err := NewGitHubClient(Config{Provider: GitHub})
	require.NoError(t, err)
	payload := `{"action":"opened","number":5,"pull_request":{"number":5,"state":"open",
		"head":{"sha":"h1","ref":"main","repo":` + head + `},"base":{"sha":"b1","ref":"main","repo":` + base + `},
		"user":{"login":"dev"}},"repository":{"full_name":"o/r","clone_url":"https://github.com/o/r.git"},"sender":{"login":"dev"}}`
	req := httptest.NewRequest("POST", "/webhook", bytes.NewBufferString(payload))
	req.Header.Set("X-GitHub-Event", "pull_request")
	event, err := client.ParseWebhook(req)
	require.NoError(t, err)
	return event.PullRequest
}

// A pull request is a fork unless the head repository is known and is the
// base repository. GitHub sends a null head.repo for a deleted fork.
func TestGitHubForkDetectionFailsClosed(t *testing.T) {
	assert.Nil(t, parseGitHubPR(t, `{"full_name":"o/r"}`, `{"full_name":"o/r"}`).HeadRepository)
	assert.Nil(t, parseGitHubPR(t, `{"full_name":"O/R"}`, `{"full_name":"o/r"}`).HeadRepository, "names are case-insensitive")

	fork := parseGitHubPR(t, `{"full_name":"evil/r","clone_url":"https://github.com/evil/r.git"}`, `{"full_name":"o/r"}`)
	require.NotNil(t, fork.HeadRepository)
	assert.Equal(t, "evil/r", fork.HeadRepository.FullName)

	deleted := parseGitHubPR(t, `null`, `{"full_name":"o/r"}`)
	require.NotNil(t, deleted.HeadRepository, "a deleted fork (null head.repo) is a fork")
	assert.Equal(t, "", deleted.HeadRepository.FullName)

	// No base repo in the pull request: the event repository is the base.
	assert.Nil(t, parseGitHubPR(t, `{"full_name":"o/r"}`, `null`).HeadRepository)
	assert.NotNil(t, parseGitHubPR(t, `{"full_name":"x/r"}`, `null`).HeadRepository)

	facts := (&WebhookEvent{PullRequest: deleted}).ActorFacts()
	assert.Equal(t, "fork", facts.HeadRelation)
}

func parseGitLabMR(t *testing.T, attrs string) *PullRequestInfo {
	t.Helper()
	client, err := NewGitLabClient(Config{Provider: GitLab})
	require.NoError(t, err)
	payload := `{"object_kind":"merge_request","user":{"username":"dev"},"project":{"id":10,"path_with_namespace":"o/r"},
		"object_attributes":{"iid":3,"state":"opened","source_branch":"main","target_branch":"main","last_commit":{"id":"h1"}` + attrs + `}}`
	req := httptest.NewRequest("POST", "/webhook", bytes.NewBufferString(payload))
	req.Header.Set("X-Gitlab-Event", "Merge Request Hook")
	event, err := client.ParseWebhook(req)
	require.NoError(t, err)
	require.NotNil(t, event.PullRequest)
	return event.PullRequest
}

// A GitLab merge request is a fork when its source project is not its
// target project. An unknown source project is a fork.
func TestGitLabForkDetection(t *testing.T) {
	assert.Nil(t, parseGitLabMR(t, `,"source_project_id":10,"target_project_id":10`).HeadRepository)
	assert.Nil(t, parseGitLabMR(t, `,"source_project_id":10`).HeadRepository, "the project is the target when target_project_id is absent")

	fork := parseGitLabMR(t, `,"source_project_id":77,"target_project_id":10,
		"source":{"id":77,"path_with_namespace":"evil/r","http_url":"https://gitlab.com/evil/r.git"}`)
	require.NotNil(t, fork.HeadRepository)
	assert.Equal(t, "evil/r", fork.HeadRepository.FullName)
	assert.Equal(t, "https://gitlab.com/evil/r.git", fork.HeadRepository.CloneURL)

	unknown := parseGitLabMR(t, ``)
	assert.NotNil(t, unknown.HeadRepository, "no source_project_id is a fork (fail closed)")

	client, err := NewGitLabClient(Config{Provider: GitLab})
	require.NoError(t, err)
	assert.Nil(t, client.convertMRInfo(gitlabMergeRequest{SourceProjectID: 4, TargetProjectID: 4}).HeadRepository)
	assert.NotNil(t, client.convertMRInfo(gitlabMergeRequest{SourceProjectID: 5, TargetProjectID: 4}).HeadRepository)
	assert.NotNil(t, client.convertMRInfo(gitlabMergeRequest{}).HeadRepository)
}
