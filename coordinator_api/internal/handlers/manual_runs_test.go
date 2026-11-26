package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/corndogs"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/principals"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/vcs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// refClient resolves branches and tags for a manual run.
type refClient struct {
	*MockVCSClient
	branches map[string]string
	tags     map[string]string
}

func (c *refClient) GetBranchHead(_ context.Context, _ string, branch string) (string, error) {
	if sha, ok := c.branches[branch]; ok {
		return sha, nil
	}
	return "", vcs.ErrBranchNotFound
}

func (c *refClient) GetTagCommit(_ context.Context, _ string, tag string) (string, error) {
	if sha, ok := c.tags[tag]; ok {
		return sha, nil
	}
	return "", vcs.ErrTagNotFound
}

func (c *refClient) GetDefaultBranch(context.Context, string) (string, error) { return "main", nil }

func newManualRunHandler(t *testing.T) (*firerStore, *WebhookHandler) {
	t.Helper()
	project := webhookTestProject()
	project.TargetBranches = []string{"main", "v*"}
	st := &firerStore{WebhookMockStore: &WebhookMockStore{}, project: project}
	handler := NewWebhookHandler(st, corndogs.NewMockClient())
	handler.AddVCSClient(vcs.GitHub, &refClient{
		MockVCSClient: &MockVCSClient{GetPRInfoFunc: func(_ context.Context, _ string, number int) (*vcs.PullRequestInfo, error) {
			return &vcs.PullRequestInfo{Number: number, State: "open", HeadSHA: "pr-head-sha", HeadRef: "feature",
				BaseRef: "main", BaseSHA: "base-sha", AuthorLogin: "author"}, nil
		}},
		branches: map[string]string{"main": "main-head-sha", "release": "release-sha"},
		tags:     map[string]string{"v1.2.0": "tag-sha"},
	})
	return st, handler
}

var manualActor = principals.Actor{PrincipalID: "11111111-2222-3333-4444-555555555555", Identity: "token:abc"}

func TestStartManualRun_Branch(t *testing.T) {
	st, handler := newManualRunHandler(t)
	result, err := handler.StartManualRun(context.Background(), st.project,
		ManualRunRequest{Ref: "main", Workflow: ".reactorcide/workflows/build.yaml"}, manualActor)
	require.NoError(t, err)
	require.Len(t, st.events, 1)
	event := st.events[0]
	assert.Equal(t, result.EventID, event.EventID)
	assert.Equal(t, models.CIEventSourceManual, event.Source)
	assert.Equal(t, "refs/heads/main", event.Ref)
	assert.Equal(t, "main-head-sha", event.CommitSHA)
	require.NotNil(t, event.ActorPrincipalID)
	assert.Equal(t, manualActor.PrincipalID, *event.ActorPrincipalID, "the starter is the actor")
	assert.Equal(t, "main-head-sha", result.CommitSHA)
	assert.Equal(t, "refs/heads/main", result.Ref)

	require.Len(t, st.CreateJobCalls, 1)
	job := st.CreateJobCalls[0]
	assert.Equal(t, event.EventID, *job.EventID)
	assert.Equal(t, result.EvalJobID, job.JobID)
	env := job.JobEnvVars
	assert.Equal(t, "push", env["REACTORCIDE_EVENT_TYPE"])
	assert.Equal(t, "manual", env["REACTORCIDE_EVENT_SOURCE"])
	assert.Equal(t, ".reactorcide/workflows/build.yaml", env["REACTORCIDE_WORKFLOW_REF"])
	assert.Equal(t, "main-head-sha", env["REACTORCIDE_SHA"])
	assert.NotContains(t, env, "REACTORCIDE_DIFF_BASE")
}

func TestStartManualRun_TagUsesDefaultBranchAsTrustedBase(t *testing.T) {
	st, handler := newManualRunHandler(t)
	st.project.AllowedEventTypes = append(st.project.AllowedEventTypes, "tag_created")
	st.project.TargetBranches = nil
	result, err := handler.StartManualRun(context.Background(), st.project, ManualRunRequest{Ref: "refs/tags/v1.2.0"}, manualActor)
	require.NoError(t, err)
	assert.Equal(t, "tag-sha", result.CommitSHA)
	require.Len(t, st.CreateJobCalls, 1)
	job := st.CreateJobCalls[0]
	assert.Equal(t, "tag_created", job.JobEnvVars["REACTORCIDE_EVENT_TYPE"])
	assert.NotContains(t, job.JobEnvVars, "REACTORCIDE_WORKFLOW_REF")
	// A tag is trusted content: its own commit is the CI, with or without a
	// stored CI policy (pushCIBaseRef).
	assert.Equal(t, "tag-sha", *job.CISourceRef)
	webhookTag := BuildEvalJob(st.project, &vcs.WebhookEvent{Provider: vcs.GitHub, GenericEvent: vcs.EventTagCreated,
		Repository: vcs.RepositoryInfo{FullName: "test-org/test-repo", CloneURL: "https://github.com/test-org/test-repo.git", DefaultBranch: "main"},
		Push:       &vcs.PushInfo{Ref: "refs/tags/v1.2.0", After: "tag-sha"}})
	assert.Equal(t, "tag-sha", webhookTag.JobEnvVars["REACTORCIDE_CI_SOURCE_REF"])
	assert.NotContains(t, webhookTag.JobEnvVars, "REACTORCIDE_EVAL_DUAL_CI")
}

func TestStartManualRun_PullRequestHead(t *testing.T) {
	st, handler := newManualRunHandler(t)
	result, err := handler.StartManualRun(context.Background(), st.project, ManualRunRequest{PRNumber: 12}, manualActor)
	require.NoError(t, err)
	require.Len(t, st.events, 1)
	event := st.events[0]
	assert.Equal(t, models.CIEventSourceManual, event.Source)
	require.NotNil(t, event.PRNumber)
	assert.Equal(t, 12, *event.PRNumber)
	assert.Equal(t, "pr-head-sha", event.CommitSHA)
	assert.Equal(t, "pr-head-sha", result.CommitSHA)
	require.Len(t, st.CreateJobCalls, 1)
	job := st.CreateJobCalls[0]
	assert.Equal(t, "12", job.JobEnvVars["REACTORCIDE_PR_NUMBER"])
	assert.Equal(t, "manual", job.JobEnvVars["REACTORCIDE_EVENT_SOURCE"])
	// The same CI source as a webhook update of this head (here: no stored
	// policy), never an earlier run's authority.
	webhookJob := BuildEvalJob(st.project, &vcs.WebhookEvent{Provider: vcs.GitHub, GenericEvent: vcs.EventPullRequestUpdated,
		Repository:  vcs.RepositoryInfo{FullName: "test-org/test-repo", CloneURL: "https://github.com/test-org/test-repo.git"},
		PullRequest: &vcs.PullRequestInfo{Number: 12, HeadSHA: "pr-head-sha", HeadRef: "feature", BaseRef: "main", BaseSHA: "base-sha"}})
	assert.Equal(t, *webhookJob.CISourceURL, *job.CISourceURL)
}

func TestStartManualRun_Refusals(t *testing.T) {
	st, handler := newManualRunHandler(t)
	for _, request := range []ManualRunRequest{
		{}, {Ref: "main", PRNumber: 1}, {Ref: "refs/pull/1/head"}, {Ref: "a b"}, {Ref: "../x"}, {Ref: "main", Workflow: "a b"},
	} {
		_, err := handler.StartManualRun(context.Background(), st.project, request, manualActor)
		assert.ErrorIs(t, err, store.ErrInvalidInput, "%+v", request)
	}
	_, err := handler.StartManualRun(context.Background(), st.project, ManualRunRequest{Ref: "nope"}, manualActor)
	assert.ErrorIs(t, err, store.ErrNotFound)
	// The project does not process pushes to "release".
	_, err = handler.StartManualRun(context.Background(), st.project, ManualRunRequest{Ref: "release"}, manualActor)
	assert.ErrorIs(t, err, ErrManualRunFiltered)
	assert.Empty(t, st.CreateJobCalls)
}

func TestStartManualRun_IdempotencyKey(t *testing.T) {
	st, handler := newManualRunHandler(t)
	first, err := handler.StartManualRun(context.Background(), st.project, ManualRunRequest{Ref: "main", IdempotencyKey: "k-1"}, manualActor)
	require.NoError(t, err)
	again, err := handler.StartManualRun(context.Background(), st.project, ManualRunRequest{Ref: "main", IdempotencyKey: "k-1"}, manualActor)
	require.NoError(t, err)
	assert.Equal(t, first.EventID, again.EventID)
	assert.Len(t, st.CreateJobCalls, 1)
	other, err := handler.StartManualRun(context.Background(), st.project, ManualRunRequest{Ref: "main"}, manualActor)
	require.NoError(t, err)
	assert.NotEqual(t, first.EventID, other.EventID, "a manual run is never a duplicate of an earlier event")
}

func TestManualRunHandler_RequiresPermissionAndStrictBody(t *testing.T) {
	st, handler := newManualRunHandler(t)
	rest := NewManualRunHandler(st, handler)
	request := func(body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/projects/x/runs", bytes.NewBufferString(body))
		req = req.WithContext(setIDContext(req.Context(), "project_id", st.project.ProjectID))
		rr := httptest.NewRecorder()
		rest.StartRun(rr, req)
		return rr
	}
	// No credential.
	assert.Equal(t, http.StatusForbidden, request(`{"ref":"main"}`).Code)

	var decoded ErrorResponse
	rr := request(`{"ref":"main","image":"x"}`)
	_ = json.Unmarshal(rr.Body.Bytes(), &decoded)
	// The permission check comes first; without one the body is not read.
	assert.Equal(t, http.StatusForbidden, rr.Code)
}
