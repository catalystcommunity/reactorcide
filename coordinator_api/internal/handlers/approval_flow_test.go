package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/approvals"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/corndogs"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/vcs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// resolvingVCSClient is a MockVCSClient that also resolves actor subjects for
// each login, and lists pull request files and commit authors.
type resolvingVCSClient struct {
	*MockVCSClient
	subjects      map[string][]string
	files         []string
	commitLogins  []string
	statusUpdates []vcs.StatusUpdate
}

func (c *resolvingVCSClient) ResolveActorSubjects(_ context.Context, _ string, login string) ([]string, error) {
	return append([]string(nil), c.subjects[strings.ToLower(login)]...), nil
}

func (c *resolvingVCSClient) ListPRFiles(context.Context, string, int) ([]string, error) {
	return append([]string(nil), c.files...), nil
}

func (c *resolvingVCSClient) ListPRCommitLogins(context.Context, string, int) ([]string, error) {
	return append([]string(nil), c.commitLogins...), nil
}

type approvalHarness struct {
	store   *WebhookMockStore
	handler *WebhookHandler
	client  *resolvingVCSClient
	pr      vcs.PullRequestInfo
	project *models.Project
}

func newApprovalHarness(t *testing.T, subjects map[string][]string) *approvalHarness {
	t.Helper()
	project := webhookTestProject()
	project.OrgID = "00000000-0000-0000-0000-000000000001"
	h := &approvalHarness{project: project, pr: vcs.PullRequestInfo{Number: 42, State: "open", HeadSHA: "head123", HeadRef: "feature",
		BaseSHA: "base456", BaseRef: "main", AuthorLogin: "author", AuthorAssociation: "CONTRIBUTOR"}}
	h.store = &WebhookMockStore{GetProjectByRepoURLFunc: func(context.Context, string) (*models.Project, error) { return project, nil }}
	h.handler = NewWebhookHandler(h.store, corndogs.NewMockClient())
	h.handler.SetTokenResolver(testTokenResolver())
	h.client = &resolvingVCSClient{subjects: subjects, MockVCSClient: &MockVCSClient{
		GetPRInfoFunc: func(context.Context, string, int) (*vcs.PullRequestInfo, error) {
			current := h.pr
			return &current, nil
		},
	}}
	h.client.UpdateCommitStatusFunc = func(_ context.Context, _ string, update vcs.StatusUpdate) error {
		h.client.statusUpdates = append(h.client.statusUpdates, update)
		return nil
	}
	h.handler.AddVCSClient(vcs.GitHub, h.client)
	return h
}

func (h *approvalHarness) deliver(t *testing.T, githubEvent string, event *vcs.WebhookEvent) {
	t.Helper()
	h.client.ParseWebhookFunc = func(*http.Request) (*vcs.WebhookEvent, error) { return event, nil }
	body := makePRWebhookBody("test-org/test-repo", "https://github.com/test-org/test-repo.git", "head123", "feature", "main", 42)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/webhooks/github", bytes.NewReader(body))
	req.Header.Set("X-GitHub-Event", githubEvent)
	w := httptest.NewRecorder()
	h.handler.HandleGitHubWebhook(w, req)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
}

func testRepository() vcs.RepositoryInfo {
	return vcs.RepositoryInfo{FullName: "test-org/test-repo", CloneURL: "https://github.com/test-org/test-repo.git"}
}

func (h *approvalHarness) comment(t *testing.T, sender, body string, commentID int64) {
	t.Helper()
	h.deliver(t, "issue_comment", &vcs.WebhookEvent{Provider: vcs.GitHub, EventType: "issue_comment", SenderLogin: sender,
		Repository:   testRepository(),
		IssueComment: &vcs.IssueCommentInfo{Action: "created", IssueNumber: 42, Body: body, IsPullRequest: true, CommentID: commentID}})
}

func (h *approvalHarness) review(t *testing.T, reviewer, action, state, body string, reviewID int64) {
	t.Helper()
	h.deliver(t, "pull_request_review", &vcs.WebhookEvent{Provider: vcs.GitHub, EventType: "pull_request_review", SenderLogin: reviewer,
		Repository: testRepository(),
		PullRequestReview: &vcs.PullRequestReviewInfo{Action: action, PRNumber: 42, ReviewID: reviewID, State: state, Body: body,
			CommitSHA: h.pr.HeadSHA, ReviewerLogin: reviewer}})
}

func (h *approvalHarness) pullRequestEvent(t *testing.T) {
	t.Helper()
	pr := h.pr
	pr.Action = "opened"
	h.deliver(t, "pull_request", &vcs.WebhookEvent{Provider: vcs.GitHub, EventType: "pull_request",
		GenericEvent: vcs.EventPullRequestOpened, SenderLogin: pr.AuthorLogin, Repository: testRepository(), PullRequest: &pr})
}

func (h *approvalHarness) runAuthority(t *testing.T, job *models.Job) approvals.RunAuthority {
	t.Helper()
	value, _ := job.JobEnvVars[approvals.RunAuthorityEnv].(string)
	authority, err := approvals.DecodeRunAuthority(value)
	require.NoError(t, err)
	return authority
}

func (h *approvalHarness) reviewStatuses() []vcs.StatusUpdate {
	result := []vcs.StatusUpdate{}
	for _, update := range h.client.statusUpdates {
		if update.Context == ReviewsStatusContext {
			result = append(result, update)
		}
	}
	return result
}

func forkGate() *models.CITrustSettings {
	return &models.CITrustSettings{RunGate: []string{models.RunGateForks}}
}

// A fork PR is held by the run gate until a run approver comments approve-ci.
// The approved run uses trusted base CI with the pr-untrusted profile.
func TestRunGate_ForkHeldUntilApproveCI(t *testing.T) {
	h := newApprovalHarness(t, map[string][]string{"maintainer": {"repository_write"}})
	h.store.OrgTrust = forkGate()
	h.pr.HeadRepository = &vcs.RepositoryInfo{FullName: "someone/test-repo", CloneURL: "https://github.com/someone/test-repo.git"}

	h.pullRequestEvent(t)
	require.Empty(t, h.store.CreateJobCalls, "the gate must hold the fork PR")

	h.comment(t, "stranger", "/reactorcide approve-ci", 1)
	require.Empty(t, h.store.CreatedCIApprovals, "a person with no run approver subject must not record approve-ci")
	require.Empty(t, h.store.CreateJobCalls)

	h.comment(t, "Maintainer", "/reactorcide approve-ci", 2)
	require.Len(t, h.store.CreatedCIApprovals, 1)
	approval := h.store.CreatedCIApprovals[0]
	assert.Equal(t, models.CIApprovalKindRun, approval.Kind)
	assert.Equal(t, "github/maintainer", approval.Person)
	assert.ElementsMatch(t, []string{"vcs_user:github/maintainer", "repository_write"}, []string(approval.Subjects))

	require.Len(t, h.store.CreateJobCalls, 1, "approve-ci must evaluate the held head")
	job := h.store.CreateJobCalls[0]
	authority := h.runAuthority(t, job)
	assert.True(t, authority.Gated)
	assert.Equal(t, models.GatedRunNoSecretsProfile, authority.Profile)
	assert.Equal(t, approval.ApprovalID, authority.RunApprovalID)
	assert.Nil(t, authority.Override)
	assert.Equal(t, "author", job.JobEnvVars["REACTORCIDE_HEAD_ACTOR"], "the approver is not the actor")

	// A redelivery of the same comment does nothing.
	h.comment(t, "Maintainer", "/reactorcide approve-ci", 2)
	assert.Len(t, h.store.CreatedCIApprovals, 1)
	assert.Len(t, h.store.CreateJobCalls, 1)
}

func TestRunGate_NormalAuthorityKeepsProfile(t *testing.T) {
	h := newApprovalHarness(t, map[string][]string{"maintainer": {"repository_write"}})
	normal := models.GatedRunNormal
	h.store.OrgTrust = &models.CITrustSettings{RunGate: []string{models.RunGateFirstTimeContributors}, GatedRunAuthority: &normal}
	h.pr.AuthorAssociation = "FIRST_TIME_CONTRIBUTOR"
	h.pullRequestEvent(t)
	require.Empty(t, h.store.CreateJobCalls)
	h.comment(t, "maintainer", "/reactorcide approve-ci", 1)
	require.Len(t, h.store.CreateJobCalls, 1)
	authority := h.runAuthority(t, h.store.CreateJobCalls[0])
	assert.True(t, authority.Gated)
	assert.Empty(t, authority.Profile, "normal keeps the normal profile")
}

func TestRunGate_NoGateRunsWithoutApproval(t *testing.T) {
	h := newApprovalHarness(t, nil)
	h.pr.HeadRepository = &vcs.RepositoryInfo{FullName: "someone/test-repo"}
	h.pullRequestEvent(t)
	require.Len(t, h.store.CreateJobCalls, 1)
	_, hasAuthority := h.store.CreateJobCalls[0].JobEnvVars[approvals.RunAuthorityEnv]
	assert.False(t, hasAuthority)
}

// override-ci lets the head's own CI run, and it also releases the run gate.
func TestOverrideCI_GrantsHeadCIAndReleasesGate(t *testing.T) {
	h := newApprovalHarness(t, map[string][]string{"maintainer": {"repository_write"}})
	h.store.OrgTrust = forkGate()
	h.pr.HeadRepository = &vcs.RepositoryInfo{FullName: "someone/test-repo"}
	h.pullRequestEvent(t)
	require.Empty(t, h.store.CreateJobCalls)

	h.comment(t, "maintainer", "/reactorcide override-ci", 1)
	require.Len(t, h.store.CreatedCIApprovals, 1)
	assert.Equal(t, models.CIApprovalKindHeadCI, h.store.CreatedCIApprovals[0].Kind)
	require.Len(t, h.store.CreateJobCalls, 1)
	authority := h.runAuthority(t, h.store.CreateJobCalls[0])
	require.NotNil(t, authority.Override)
	assert.Equal(t, models.DefaultOverrideProfile, authority.Override.Profile)
	assert.Equal(t, "github/maintainer", authority.Override.Person)
	assert.True(t, authority.Gated)
}

func TestOverrideCI_RefusedWithoutOverrideSubject(t *testing.T) {
	h := newApprovalHarness(t, map[string][]string{"maintainer": {"repository_write"}})
	h.store.OrgTrust = &models.CITrustSettings{OverrideApprovers: []string{"reactorcide_group:release"}}
	h.comment(t, "maintainer", "/reactorcide override-ci", 1)
	assert.Empty(t, h.store.CreatedCIApprovals)
	assert.Empty(t, h.store.CreateJobCalls)
}

// accept and revoke start no CI. They only post the "Reactorcide Reviews"
// status, and only when the project needs a review.
func TestAccept_PostsReviewsStatusAndStartsNoCI(t *testing.T) {
	h := newApprovalHarness(t, map[string][]string{"todpunk": {"repository_write"}})
	h.comment(t, "todpunk", "/reactorcide accept", 1)
	require.Len(t, h.store.CreatedCIApprovals, 1)
	assert.Empty(t, h.store.CreateJobCalls)
	assert.Empty(t, h.reviewStatuses(), "no required review: no status")

	reviews := []models.RequiredReview{{ID: "trust-anchors", Paths: []string{"crypto/**"}, Accept: []string{"vcs_user:github/todpunk"}}}
	h.store.ProjectTrust = &models.CITrustSettings{RequiredReviews: &reviews}
	h.client.files = []string{"crypto/keys.rs"}
	h.comment(t, "todpunk", "/reactorcide accept trust-anchors", 2)
	require.Len(t, h.store.CreatedCIApprovals, 2)
	assert.Equal(t, "trust-anchors", h.store.CreatedCIApprovals[1].Label)
	statuses := h.reviewStatuses()
	require.Len(t, statuses, 1)
	assert.Equal(t, vcs.StatusSuccess, statuses[0].State, statuses[0].Description)
	assert.Equal(t, "head123", statuses[0].SHA)

	h.comment(t, "todpunk", "/reactorcide revoke", 3)
	kinds := []string{}
	for _, action := range h.store.CreatedCIApprovals[2:] {
		assert.Equal(t, models.CIApprovalActionRevoked, action.Action)
		kinds = append(kinds, action.Kind)
	}
	assert.ElementsMatch(t, []string{models.CIApprovalKindRun, models.CIApprovalKindHeadCI, models.CIApprovalKindAccept}, kinds)
	statuses = h.reviewStatuses()
	require.Len(t, statuses, 2)
	assert.Equal(t, vcs.StatusPending, statuses[1].State)
	assert.Contains(t, statuses[1].Description, "trust-anchors (0 of 1)")
	assert.Empty(t, h.store.CreateJobCalls)
}

func TestNonAuthorReview_AuthorCannotSatisfy(t *testing.T) {
	h := newApprovalHarness(t, map[string][]string{"author": {"repository_write"}, "committer": {"repository_write"}, "reviewer": {"repository_write"}})
	reviews := []models.RequiredReview{{ID: "compliance", Accept: []string{models.SubjectNonAuthor}}}
	h.store.OrgTrust = &models.CITrustSettings{RequiredReviews: &reviews}
	h.client.commitLogins = []string{"Committer"}
	for i, login := range []string{"author", "committer"} {
		h.comment(t, login, "/reactorcide accept", int64(i+1))
		statuses := h.reviewStatuses()
		assert.Equal(t, vcs.StatusPending, statuses[len(statuses)-1].State, login+" wrote the change")
	}
	h.comment(t, "reviewer", "/reactorcide accept", 3)
	statuses := h.reviewStatuses()
	assert.Equal(t, vcs.StatusSuccess, statuses[len(statuses)-1].State)
}

// A review never grants execution. Approved is an accept; changes requested
// revokes accepts only; a dismissed review cancels only that reviewer.
func TestReviewEvents(t *testing.T) {
	h := newApprovalHarness(t, map[string][]string{"todpunk": {"repository_write"}})
	h.review(t, "todpunk", "submitted", "approved", "Fine.\n/reactorcide accept trust-anchors", 10)
	require.Len(t, h.store.CreatedCIApprovals, 1)
	accept := h.store.CreatedCIApprovals[0]
	assert.Equal(t, models.CIApprovalKindAccept, accept.Kind)
	assert.Equal(t, models.CIApprovalChannelReview, accept.Channel)
	assert.Equal(t, "trust-anchors", accept.Label)

	h.review(t, "todpunk", "submitted", "commented", "", 11)
	assert.Len(t, h.store.CreatedCIApprovals, 1, "a comment review changes nothing")

	h.review(t, "todpunk", "submitted", "changes_requested", "", 12)
	require.Len(t, h.store.CreatedCIApprovals, 2)
	assert.Equal(t, models.CIApprovalKindAccept, h.store.CreatedCIApprovals[1].Kind)
	assert.Equal(t, models.CIApprovalActionRevoked, h.store.CreatedCIApprovals[1].Action)

	h.review(t, "todpunk", "dismissed", "dismissed", "", 10)
	require.Len(t, h.store.CreatedCIApprovals, 3)
	dismissed := h.store.CreatedCIApprovals[2]
	assert.Equal(t, models.CIApprovalActionRevoked, dismissed.Action)
	assert.Equal(t, []string{"vcs_user:github/todpunk"}, []string(dismissed.Subjects), "a dismissal cancels only the reviewer")
	assert.Empty(t, h.store.CreateJobCalls)
}

func TestRerun_StartsNewEvaluation(t *testing.T) {
	h := newApprovalHarness(t, map[string][]string{"maintainer": {"repository_write"}})
	h.comment(t, "maintainer", "/reactorcide rerun", 5)
	require.Len(t, h.store.CreateJobCalls, 1)
	assert.Empty(t, h.store.CreatedCIApprovals, "rerun records no approval")
	h.comment(t, "nobody", "/reactorcide rerun", 6)
	assert.Len(t, h.store.CreateJobCalls, 1, "rerun needs a run approver")
}

func TestCommandsOnClosedPullRequest(t *testing.T) {
	h := newApprovalHarness(t, map[string][]string{"maintainer": {"repository_write"}})
	h.pr.State = "closed"
	h.comment(t, "maintainer", "/reactorcide approve-ci", 1)
	assert.Empty(t, h.store.CreatedCIApprovals)
	assert.Empty(t, h.store.CreateJobCalls)
}

func TestExecute_HeadChangedIsRefused(t *testing.T) {
	h := newApprovalHarness(t, map[string][]string{"maintainer": {"repository_write"}})
	_, err := h.handler.Execute(context.Background(), approvals.Request{Project: h.project, PRNumber: 42, HeadSHA: "old",
		Command: approvals.Command{Name: approvals.CommandAccept}, Channel: models.CIApprovalChannelAPI, Login: "maintainer"})
	assert.ErrorIs(t, err, approvals.ErrHeadChanged)
	result, err := h.handler.Execute(context.Background(), approvals.Request{Project: h.project, PRNumber: 42, HeadSHA: "head123",
		Command: approvals.Command{Name: approvals.CommandAccept}, Channel: models.CIApprovalChannelAPI, Login: "maintainer"})
	require.NoError(t, err)
	encoded, _ := json.Marshal(result)
	assert.Contains(t, string(encoded), `"effect":"reviews"`)
}
