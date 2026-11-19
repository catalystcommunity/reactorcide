package githubapp

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/vcs"
)

func appClient(t *testing.T) (*AppClient, *fakeGitHub) {
	t.Helper()
	service, apps, _, api, _ := newTestService(t)
	seedInstallation(t, apps, 1001, "acme", nil, "acme/app")
	client, ok, err := service.ClientForRepo(context.Background(), vcs.GitHub, "acme/app")
	require.NoError(t, err)
	require.True(t, ok)
	return client.(*AppClient), api
}

func TestBuildCheckRunPayload(t *testing.T) {
	now := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	line := 12
	update := vcs.CheckRunUpdate{Name: "Reactorcide Checks", HeadSHA: "abc", State: vcs.StatusFailure, DetailsURL: "https://ci/app/events/1",
		ExternalID: "ev1", Title: "1 of 2 workflows failed: build", Summary: "| Workflow | Status |",
		Annotations: []vcs.CheckRunAnnotation{{Path: "pkg/a_test.go", StartLine: line, Title: "TestA", Message: "boom"},
			{Path: "", StartLine: 3, Message: "no path"}},
		Actions: []vcs.CheckRunAction{{Label: "Re-run", Description: "Run every workflow of the head again", Identifier: "rerun"}}}

	created := BuildCheckRunPayload(update, true, true, now)
	assert.Equal(t, "Reactorcide Checks", created.Name)
	assert.Equal(t, "abc", created.HeadSHA)
	assert.Equal(t, "completed", created.Status)
	assert.Equal(t, "failure", created.Conclusion)
	assert.Equal(t, "2026-11-16T12:00:00Z", created.CompletedAt)
	assert.Equal(t, "ev1", created.ExternalID)
	require.Len(t, created.Output.Annotations, 1, "an annotation needs a path and a line")
	assert.Equal(t, "failure", created.Output.Annotations[0].AnnotationLevel)
	assert.Equal(t, 12, created.Output.Annotations[0].EndLine)
	require.Len(t, created.Actions, 1)
	assert.Equal(t, "rerun", created.Actions[0].Identifier)

	updated := BuildCheckRunPayload(update, false, false, now)
	assert.Empty(t, updated.Name, "an update does not send the name")
	assert.Empty(t, updated.HeadSHA)
	assert.Empty(t, updated.Output.Annotations)

	pending := BuildCheckRunPayload(vcs.CheckRunUpdate{Name: "Reactorcide Checks", HeadSHA: "abc", State: vcs.StatusPending, Title: "Evaluating event"}, true, false, now)
	assert.Equal(t, "in_progress", pending.Status)
	assert.Empty(t, pending.Conclusion)
	assert.Empty(t, pending.CompletedAt)
	assert.Equal(t, "Evaluating event", pending.Output.Summary, "the summary falls back to the title")

	cancelled := BuildCheckRunPayload(vcs.CheckRunUpdate{Name: "x", HeadSHA: "abc", State: vcs.StatusCancelled}, true, false, now)
	assert.Equal(t, "cancelled", cancelled.Conclusion)

	long := BuildCheckRunPayload(vcs.CheckRunUpdate{Name: "x", HeadSHA: "abc", State: vcs.StatusSuccess, Title: strings.Repeat("é", 400)}, true, false, now)
	assert.LessOrEqual(t, len(long.Output.Title), 255)
}

func TestPublishCheckRun_CreateUpdateAndRerun(t *testing.T) {
	client, api := appClient(t)
	ctx := context.Background()
	update := vcs.CheckRunUpdate{Name: "Reactorcide Checks", HeadSHA: "abc", State: vcs.StatusPending, Title: "Evaluating event"}

	require.NoError(t, client.PublishCheckRun(ctx, "acme/app", update))
	update.State, update.Title = vcs.StatusFailure, "1 of 1 workflow failed"
	update.Annotations = []vcs.CheckRunAnnotation{{Path: "a.go", StartLine: 1, Message: "x"}}
	require.NoError(t, client.PublishCheckRun(ctx, "acme/app", update))
	// The same completed state again (a description change): no new
	// annotations.
	update.Title = "1 of 1 workflow failed (again)"
	require.NoError(t, client.PublishCheckRun(ctx, "acme/app", update))
	// A re-run: pending after completed creates a new run.
	update.State, update.Title = vcs.StatusPending, "Evaluating event"
	require.NoError(t, client.PublishCheckRun(ctx, "acme/app", update))

	var methods []string
	for _, r := range api.snapshot() {
		if strings.Contains(r.Path, "check-runs") {
			methods = append(methods, r.Method)
		}
	}
	assert.Equal(t, []string{"GET", "POST", "PATCH", "PATCH", "POST"}, methods)
	require.Len(t, api.checkRuns, 4)
	assert.Equal(t, "in_progress", api.checkRuns[0]["status"])
	assert.Equal(t, "Reactorcide Checks", api.checkRuns[0]["name"])
	failure := api.checkRuns[1]
	assert.Equal(t, "completed", failure["status"])
	assert.Equal(t, "failure", failure["conclusion"])
	assert.Len(t, failure["output"].(map[string]any)["annotations"], 1)
	_, again := api.checkRuns[2]["output"].(map[string]any)["annotations"]
	assert.False(t, again, "annotations are sent one time")
	assert.Equal(t, "in_progress", api.checkRuns[3]["status"])
}

func TestPublishCheckRun_FindsExistingRun(t *testing.T) {
	client, api := appClient(t)
	api.listRuns = []checkRunRecord{{ID: 77, Status: "in_progress"}}
	require.NoError(t, client.PublishCheckRun(context.Background(), "acme/app",
		vcs.CheckRunUpdate{Name: "Reactorcide Checks", HeadSHA: "def", State: vcs.StatusSuccess, Title: "1 workflow passed"}))
	var paths []string
	for _, r := range api.snapshot() {
		if strings.Contains(r.Path, "check-runs") {
			paths = append(paths, r.Method+" "+r.Path)
		}
	}
	assert.Equal(t, []string{"GET /repos/acme/app/commits/def/check-runs", "PATCH /repos/acme/app/check-runs/77"}, paths)
}

func TestUpdateCommitStatus_ChecksContextsAreCheckRuns(t *testing.T) {
	client, api := appClient(t)
	ctx := context.Background()
	require.NoError(t, client.UpdateCommitStatus(ctx, "acme/app", vcs.StatusUpdate{SHA: "abc", State: vcs.StatusSuccess,
		Description: "All required reviews present", Context: "Reactorcide Reviews"}))
	require.NoError(t, client.UpdateCommitStatus(ctx, "acme/app", vcs.StatusUpdate{SHA: "abc", State: vcs.StatusSuccess,
		Description: "lint passed", Context: "lint"}))
	var paths []string
	for _, r := range api.snapshot() {
		if strings.HasPrefix(r.Path, "/repos/") {
			paths = append(paths, r.Method+" "+r.Path)
		}
	}
	assert.Equal(t, []string{"GET /repos/acme/app/commits/abc/check-runs", "POST /repos/acme/app/check-runs",
		"POST /repos/acme/app/statuses/abc"}, paths, "a workflow status.context stays a commit status")
	assert.Equal(t, "Reactorcide Reviews", api.checkRuns[0]["name"])
	// Installation token auth on repository calls.
	for i, r := range api.snapshot() {
		if strings.HasPrefix(r.Path, "/repos/") {
			assert.True(t, strings.HasPrefix(api.auth[i], "token ghs_token_"), r.Path)
		}
	}
}
