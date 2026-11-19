package handlers

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/vcs"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/vcs/githubapp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeGitHubApps is the App pool of the webhook tests: two Apps with
// different webhook secrets.
type fakeGitHubApps struct {
	secrets       map[int64]string
	installations []string
	prs           map[string]int
	apps          *fakeGitHubAppStore
}

type fakeGitHubAppStore struct {
	store.GitHubAppStore
	prs map[string]int
	// bindings are the active repository bindings, by lowercase owner/name.
	bindings map[string]*models.GitHubAppBinding
}

func (f *fakeGitHubAppStore) ResolveGitHubAppBinding(_ context.Context, name string) (*models.GitHubAppBinding, error) {
	return f.bindings[models.NormalizeGitHubRepoFullName(name)], nil
}

// bind binds a repository to an installation of an App.
func (f *fakeGitHubApps) bind(repo string, appID int64, projectID string, repositoryID int64) {
	binding := &models.GitHubAppBinding{App: models.GitHubApp{AppID: appID, Enabled: true},
		Installation: models.GitHubAppInstallation{InstallationID: 1000 + appID, AppID: appID},
		Repository:   models.GitHubAppRepository{RepoFullName: models.NormalizeGitHubRepoFullName(repo), InstallationID: 1000 + appID}}
	if projectID != "" {
		binding.Repository.ProjectID = &projectID
	}
	if repositoryID > 0 {
		binding.Repository.RepositoryID = &repositoryID
	}
	f.apps.bindings[binding.Repository.RepoFullName] = binding
}

func (f *fakeGitHubAppStore) LatestPRNumberForCommit(_ context.Context, _ string, sha string) (int, bool, error) {
	n, ok := f.prs[sha]
	return n, ok, nil
}

func newFakeGitHubApps() *fakeGitHubApps {
	return &fakeGitHubApps{secrets: map[int64]string{1: "secret-app-one", 2: "secret-app-two"},
		apps: &fakeGitHubAppStore{prs: map[string]int{}, bindings: map[string]*models.GitHubAppBinding{}}}
}

func (f *fakeGitHubApps) WebhookSecret(_ context.Context, appID int64) (string, bool, error) {
	secret, ok := f.secrets[appID]
	return secret, ok, nil
}

func (f *fakeGitHubApps) HandleInstallationEvent(_ context.Context, appID int64, eventType string, _ []byte) (githubapp.EventResult, error) {
	f.installations = append(f.installations, eventType)
	return githubapp.EventResult{InstallationID: 1001, Action: "created"}, nil
}

func (f *fakeGitHubApps) Store() store.GitHubAppStore { return f.apps }

func signBody(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// appHarness is the approval harness with a real GitHub signature check.
func newAppHarness(t *testing.T, subjects map[string][]string) (*approvalHarness, *fakeGitHubApps) {
	t.Helper()
	h := newApprovalHarness(t, subjects)
	real, err := vcs.NewGitHubClient(vcs.Config{Provider: vcs.GitHub})
	require.NoError(t, err)
	h.client.ValidateWebhookFunc = real.ValidateWebhook
	apps := newFakeGitHubApps()
	// The repository of the harness is in an installation of App 1.
	apps.bind("test-org/test-repo", 1, h.project.ProjectID, 0)
	h.handler.SetGitHubApps(apps)
	return h, apps
}

func appDelivery(t *testing.T, h *approvalHarness, appID, eventType, secret string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/webhooks/github-app", bytes.NewReader(body))
	req.Header.Set("X-GitHub-Event", eventType)
	req.Header.Set("X-GitHub-Delivery", "delivery-"+eventType)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(githubapp.HeaderHookTargetType, "integration")
	req.Header.Set(githubapp.HeaderHookTargetID, appID)
	req.Header.Set("X-Hub-Signature-256", signBody(secret, body))
	w := httptest.NewRecorder()
	h.handler.HandleGitHubWebhook(w, req)
	return w
}

func TestGitHubAppWebhook_VerifiesWithThatAppsSecret(t *testing.T) {
	h, apps := newAppHarness(t, nil)
	body := []byte(`{"action":"created","installation":{"id":1001,"app_id":2,"account":{"login":"acme"}}}`)

	w := appDelivery(t, h, "2", "installation", "secret-app-two", body)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.Equal(t, []string{"installation"}, apps.installations)

	// The secret of App 1 does not verify a delivery that names App 2.
	w = appDelivery(t, h, "2", "installation", "secret-app-one", body)
	assert.Equal(t, http.StatusUnauthorized, w.Code)
	// An unknown App is refused.
	w = appDelivery(t, h, "9", "installation", "secret-app-two", body)
	assert.Equal(t, http.StatusUnauthorized, w.Code)
	assert.Len(t, apps.installations, 1)

	w = appDelivery(t, h, "1", "installation_repositories", "secret-app-one", body)
	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, []string{"installation", "installation_repositories"}, apps.installations)
}

func TestGitHubAppWebhook_NoPoolRefusesAppDeliveries(t *testing.T) {
	h := newApprovalHarness(t, nil)
	w := appDelivery(t, h, "2", "installation", "secret-app-two", []byte(`{}`))
	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

// A pull request comment from an App delivery runs the normal command path.
// The project has a webhook secret of its own; the App secret is enough.
func TestGitHubAppWebhook_CommentEventsContinue(t *testing.T) {
	h, _ := newAppHarness(t, map[string][]string{"maintainer": {"repository_write"}})
	h.client.ParseWebhookFunc = func(*http.Request) (*vcs.WebhookEvent, error) {
		return &vcs.WebhookEvent{Provider: vcs.GitHub, EventType: "issue_comment", SenderLogin: "maintainer",
			Repository:   testRepository(),
			IssueComment: &vcs.IssueCommentInfo{Action: "created", IssueNumber: 42, Body: "/reactorcide rerun", IsPullRequest: true, CommentID: 9}}, nil
	}
	body := makePRWebhookBody("test-org/test-repo", "https://github.com/test-org/test-repo.git", "head123", "feature", "main", 42)
	w := appDelivery(t, h, "1", "issue_comment", "secret-app-one", body)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.Len(t, h.store.CreateJobCalls, 1, "the rerun comment started a new evaluation")
}

func checkRunBody(sender string, appID int, prs string) []byte {
	return []byte(`{"action":"rerequested","check_run":{"id":5,"name":"Reactorcide Checks","head_sha":"head123","app":{"id":` +
		itoa(appID) + `},"pull_requests":` + prs + `},"repository":{"full_name":"test-org/test-repo"},"sender":{"login":"` + sender + `"}}`)
}

func itoa(n int) string {
	return string(rune('0' + n))
}

func TestGitHubAppWebhook_CheckRunRerunNeedsRunApprover(t *testing.T) {
	h, apps := newAppHarness(t, map[string][]string{"maintainer": {"repository_write"}})

	w := appDelivery(t, h, "1", "check_run", "secret-app-one", checkRunBody("nobody", 1, `[{"number":42}]`))
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.Empty(t, h.store.CreateJobCalls, "a sender who is not a run approver cannot re-run")

	w = appDelivery(t, h, "1", "check_run", "secret-app-one", checkRunBody("maintainer", 1, `[{"number":42}]`))
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Len(t, h.store.CreateJobCalls, 1, "a run approver re-runs the head")

	// A run of another App is not ours.
	w = appDelivery(t, h, "1", "check_run", "secret-app-one", checkRunBody("maintainer", 2, `[{"number":42}]`))
	require.Equal(t, http.StatusOK, w.Code)
	assert.Len(t, h.store.CreateJobCalls, 1)

	// A fork pull request has no pull_requests list: the CI event of the
	// commit gives the number.
	apps.apps.prs["head123"] = 42
	body := []byte(`{"action":"requested_action","requested_action":{"identifier":"rerun"},"check_run":{"id":6,"name":"Reactorcide Checks",
"head_sha":"head123","app":{"id":1},"pull_requests":[]},"repository":{"full_name":"test-org/test-repo"},"sender":{"login":"maintainer"}}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/webhooks/github-app", bytes.NewReader(body))
	req.Header.Set("X-GitHub-Event", "check_run")
	req.Header.Set("X-GitHub-Delivery", "delivery-2")
	req.Header.Set(githubapp.HeaderHookTargetType, "integration")
	req.Header.Set(githubapp.HeaderHookTargetID, "1")
	req.Header.Set("X-Hub-Signature-256", signBody("secret-app-one", body))
	rec := httptest.NewRecorder()
	h.handler.HandleGitHubWebhook(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Len(t, h.store.CreateJobCalls, 2)
}

func TestGitHubAppWebhook_CheckRunRerunOfOldHeadIsRefused(t *testing.T) {
	h, _ := newAppHarness(t, map[string][]string{"maintainer": {"repository_write"}})
	h.pr.HeadSHA = "newhead"
	w := appDelivery(t, h, "1", "check_run", "secret-app-one", checkRunBody("maintainer", 1, `[{"number":42}]`))
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.Empty(t, h.store.CreateJobCalls, "the button of an old head does not run the new head")
}

// A repository webhook (no App headers) keeps the project secret path.
func TestGitHubAppWebhook_RepositoryWebhookUnchanged(t *testing.T) {
	h, apps := newAppHarness(t, nil)
	body := []byte(`{"zen":"x"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/webhooks/github", bytes.NewReader(body))
	req.Header.Set("X-GitHub-Event", "installation")
	req.Header.Set("X-Hub-Signature-256", signBody("secret-app-one", body))
	w := httptest.NewRecorder()
	h.handler.HandleGitHubWebhook(w, req)
	assert.NotEqual(t, http.StatusOK, w.Code, "an App secret does not verify a repository webhook")
	assert.Empty(t, apps.installations)
}

// An App secret only proves the App. A delivery of App 2 for a repository
// that only App 1 holds, a delivery for a repository that no installation
// holds, a repository ID that is not the ID of the binding, and a project
// of another repository with the same name are all ignored: no project
// secret is skipped for them.
func TestGitHubAppWebhook_DeliveryNeedsBindingOfThatApp(t *testing.T) {
	commentEvent := func(h *approvalHarness) {
		h.client.ParseWebhookFunc = func(*http.Request) (*vcs.WebhookEvent, error) {
			return &vcs.WebhookEvent{Provider: vcs.GitHub, EventType: "issue_comment", SenderLogin: "maintainer",
				Repository:   testRepository(),
				IssueComment: &vcs.IssueCommentInfo{Action: "created", IssueNumber: 42, Body: "/reactorcide rerun", IsPullRequest: true, CommentID: 9}}, nil
		}
	}
	body := makePRWebhookBody("test-org/test-repo", "https://github.com/test-org/test-repo.git", "head123", "feature", "main", 42)
	withID := []byte(`{"action":"created","repository":{"id":900,"full_name":"test-org/test-repo","clone_url":"https://github.com/test-org/test-repo.git"},` +
		`"issue":{"number":42,"pull_request":{}},"comment":{"id":9,"body":"/reactorcide rerun"},"sender":{"login":"maintainer"}}`)

	t.Run("another App", func(t *testing.T) {
		h, _ := newAppHarness(t, map[string][]string{"maintainer": {"repository_write"}})
		commentEvent(h)
		w := appDelivery(t, h, "2", "issue_comment", "secret-app-two", body)
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		assert.Contains(t, w.Body.String(), "ignored")
		assert.Empty(t, h.store.CreateJobCalls)
	})
	t.Run("no installation", func(t *testing.T) {
		h, apps := newAppHarness(t, map[string][]string{"maintainer": {"repository_write"}})
		delete(apps.apps.bindings, "test-org/test-repo")
		commentEvent(h)
		w := appDelivery(t, h, "1", "issue_comment", "secret-app-one", body)
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		assert.Contains(t, w.Body.String(), "not in an active installation")
		assert.Empty(t, h.store.CreateJobCalls)
	})
	t.Run("binding of another project", func(t *testing.T) {
		h, apps := newAppHarness(t, map[string][]string{"maintainer": {"repository_write"}})
		apps.bind("test-org/test-repo", 1, "00000000-0000-0000-0000-00000000dead", 0)
		commentEvent(h)
		w := appDelivery(t, h, "1", "issue_comment", "secret-app-one", body)
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		assert.Empty(t, h.store.CreateJobCalls)
	})
	t.Run("repository ID of the binding differs", func(t *testing.T) {
		h, apps := newAppHarness(t, map[string][]string{"maintainer": {"repository_write"}})
		apps.bind("test-org/test-repo", 1, h.project.ProjectID, 901)
		commentEvent(h)
		w := appDelivery(t, h, "1", "issue_comment", "secret-app-one", withID)
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		assert.Contains(t, w.Body.String(), "repository ID")
		assert.Empty(t, h.store.CreateJobCalls)
	})
	t.Run("project of another repository with the same name", func(t *testing.T) {
		h, apps := newAppHarness(t, map[string][]string{"maintainer": {"repository_write"}})
		apps.bind("test-org/test-repo", 1, h.project.ProjectID, 900)
		old := int64(55)
		h.project.VCSRepositoryID = &old
		commentEvent(h)
		w := appDelivery(t, h, "1", "issue_comment", "secret-app-one", withID)
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		assert.Contains(t, w.Body.String(), "another repository with the same name")
		assert.Empty(t, h.store.CreateJobCalls)
	})
	t.Run("bound to this App with the same ID", func(t *testing.T) {
		h, apps := newAppHarness(t, map[string][]string{"maintainer": {"repository_write"}})
		apps.bind("test-org/test-repo", 1, h.project.ProjectID, 900)
		id := int64(900)
		h.project.VCSRepositoryID = &id
		commentEvent(h)
		w := appDelivery(t, h, "1", "issue_comment", "secret-app-one", withID)
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		assert.Len(t, h.store.CreateJobCalls, 1)
	})
}

// A check run re-run of App 1 for a repository that App 1 does not hold for
// the project does nothing.
func TestGitHubAppWebhook_CheckRunRerunNeedsBinding(t *testing.T) {
	h, apps := newAppHarness(t, map[string][]string{"maintainer": {"repository_write"}})
	delete(apps.apps.bindings, "test-org/test-repo")
	w := appDelivery(t, h, "1", "check_run", "secret-app-one", checkRunBody("maintainer", 1, `[{"number":42}]`))
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.Empty(t, h.store.CreateJobCalls)
}
