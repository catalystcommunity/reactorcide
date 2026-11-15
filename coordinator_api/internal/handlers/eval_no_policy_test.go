package handlers

import (
	"context"
	"errors"
	"testing"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/vcs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Without a stored CI policy nothing is gated: an eval runs the event's own
// CI code, so anybody can develop CI changes on a branch or in a PR.

func noPolicyPushEvent() *vcs.WebhookEvent {
	return &vcs.WebhookEvent{
		Provider: vcs.GitHub, EventType: "push", GenericEvent: vcs.EventPush,
		Repository: vcs.RepositoryInfo{FullName: "org/repo", CloneURL: "https://github.com/org/repo.git", DefaultBranch: "main"},
		Push:       &vcs.PushInfo{Ref: "refs/heads/feature", Before: "before-sha", After: "after-sha"},
	}
}

func noPolicyPREvent(fork bool) *vcs.WebhookEvent {
	event := &vcs.WebhookEvent{
		Provider: vcs.GitHub, EventType: "pull_request", GenericEvent: vcs.EventPullRequestUpdated,
		Repository: vcs.RepositoryInfo{FullName: "org/repo", CloneURL: "https://github.com/org/repo.git"},
		PullRequest: &vcs.PullRequestInfo{
			Number: 9, Action: "synchronize", HeadSHA: "head-sha", HeadRef: "feature",
			BaseSHA: "base-sha", BaseRef: "main",
		},
	}
	if fork {
		event.PullRequest.HeadRepository = &vcs.RepositoryInfo{FullName: "someone/repo", CloneURL: "https://github.com/someone/repo.git"}
	}
	return event
}

func TestUseEventCIWithoutPolicy_PushRunsPushedCI(t *testing.T) {
	project := evalTestProject()
	project.DefaultCISourceURL = ""
	project.DefaultCISourceRef = ""
	event := noPolicyPushEvent()
	job := BuildEvalJob(project, event)
	require.Equal(t, "true", job.JobEnvVars["REACTORCIDE_EVAL_DUAL_CI"], "precondition: policy mode prepares base/head")

	useEventCIWithoutPolicy(job, project, event)

	require.NotNil(t, job.CISourceRef)
	assert.Equal(t, "after-sha", *job.CISourceRef)
	assert.Equal(t, "after-sha", job.CISHA)
	assert.Equal(t, "after-sha", job.JobEnvVars["REACTORCIDE_CI_SOURCE_REF"])
	assert.NotContains(t, job.JobEnvVars, "REACTORCIDE_EVAL_DUAL_CI")
	assert.NotContains(t, job.JobEnvVars, "REACTORCIDE_HEAD_URL")
	// The changed files are still the push's commits.
	assert.Equal(t, "before-sha", job.JobEnvVars["REACTORCIDE_DIFF_BASE"])
}

func TestUseEventCIWithoutPolicy_SameRepoPRRunsHeadCI(t *testing.T) {
	project := evalTestProject()
	project.DefaultCISourceURL = ""
	project.DefaultCISourceRef = ""
	event := noPolicyPREvent(false)
	job := BuildEvalJob(project, event)

	useEventCIWithoutPolicy(job, project, event)

	require.NotNil(t, job.CISourceRef)
	assert.Equal(t, "head-sha", *job.CISourceRef)
	assert.Equal(t, "head-sha", job.JobEnvVars["REACTORCIDE_CI_SOURCE_REF"])
	assert.Equal(t, "base-sha", job.JobEnvVars["REACTORCIDE_DIFF_BASE"])
}

func TestUseEventCIWithoutPolicy_ForkPRKeepsTrustedBase(t *testing.T) {
	project := evalTestProject()
	project.DefaultCISourceURL = ""
	project.DefaultCISourceRef = ""
	event := noPolicyPREvent(true)
	job := BuildEvalJob(project, event)

	useEventCIWithoutPolicy(job, project, event)

	require.NotNil(t, job.CISourceRef)
	assert.Equal(t, "base-sha", *job.CISourceRef, "fork content is never the CI source")
	assert.Equal(t, "https://github.com/org/repo.git", *job.CISourceURL)
}

func TestUseEventCIWithoutPolicy_ConfiguredCISourceWins(t *testing.T) {
	project := evalTestProject()
	require.NotEmpty(t, project.DefaultCISourceURL, "precondition: the test project configures a CI source")
	event := noPolicyPushEvent()
	job := BuildEvalJob(project, event)
	before := *job.CISourceRef

	useEventCIWithoutPolicy(job, project, event)

	assert.Equal(t, before, *job.CISourceRef)
	assert.Equal(t, project.DefaultCISourceURL, *job.CISourceURL)
}

func TestAttachCIPolicy_ReportsState(t *testing.T) {
	project := evalTestProject()

	none := NewWebhookHandler(&WebhookMockStore{}, nil)
	job := BuildEvalJob(project, noPolicyPushEvent())
	assert.Equal(t, ciPolicyNone, none.attachCIPolicy(context.Background(), job, project))

	// A policy that may exist but cannot be read must NOT open the gate:
	// the eval keeps its trusted base and head CI fails closed.
	broken := NewWebhookHandler(&WebhookMockStore{GetCIPolicyFunc: func(context.Context, string) (*models.CIPolicy, error) {
		return nil, errors.New("database unavailable")
	}}, nil)
	job = BuildEvalJob(project, noPolicyPushEvent())
	assert.Equal(t, ciPolicyUnavailable, broken.attachCIPolicy(context.Background(), job, project))
	assert.NotContains(t, job.JobEnvVars, "REACTORCIDE_CI_POLICY")
}
