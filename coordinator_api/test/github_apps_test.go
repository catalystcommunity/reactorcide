package test

// Postgres-backed coverage for migration 000057 and internal/vcs/githubapp
// (WP11, docs/github-app.md): the App registry, installation bindings, rate
// limit snapshots, self-service registration against an httptest GitHub API,
// and the REST API with the admin token.

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/postgres_store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/vcs"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/vcs/githubapp"
)

func githubAppTestKey(t *testing.T) []byte {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	return pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
}

func insertGitHubApp(t *testing.T, ctx context.Context, appID int64) {
	t.Helper()
	require.NoError(t, postgres_store.PostgresStore.CreateGitHubApp(ctx, &models.GitHubApp{AppID: appID, Slug: fmt.Sprintf("app-%d", appID),
		PrivateKeyKeyName: "test-key", PrivateKeyCiphertext: []byte("enc:x"), WebhookSecretKeyName: "test-key",
		WebhookSecretCiphertext: []byte("enc:s"), Enabled: true}))
}

func TestGitHubAppStore(t *testing.T) {
	RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
		ps := postgres_store.PostgresStore
		appID := time.Now().UnixNano()%1_000_000_000 + 1
		insertGitHubApp(t, ctx, appID)
		insertGitHubApp(t, ctx, appID+1)
		err := ps.CreateGitHubApp(ctx, &models.GitHubApp{AppID: appID, PrivateKeyKeyName: "k", PrivateKeyCiphertext: []byte("x"),
			WebhookSecretKeyName: "k", WebhookSecretCiphertext: []byte("x")})
		assert.ErrorIs(t, err, store.ErrAlreadyExists)

		installA, installB := appID*10, appID*10+1
		require.NoError(t, ps.UpsertGitHubAppInstallation(ctx, &models.GitHubAppInstallation{InstallationID: installA, AppID: appID,
			AccountLogin: "Acme", AccountType: "Organization", RepositorySelection: "selected",
			Permissions: map[string]string{"checks": "write"}}))
		require.NoError(t, ps.UpsertGitHubAppInstallation(ctx, &models.GitHubAppInstallation{InstallationID: installB, AppID: appID + 1,
			AccountLogin: "acme"}))
		got, err := ps.GetGitHubAppInstallation(ctx, installA)
		require.NoError(t, err)
		assert.Equal(t, "write", got.Permissions["checks"])

		repo := fmt.Sprintf("acme/app-%d", appID)
		row, bound, err := ps.BindGitHubAppRepository(ctx, &models.GitHubAppRepository{RepoFullName: strings.ToUpper(repo), InstallationID: installA, Private: true})
		require.NoError(t, err)
		assert.True(t, bound)
		assert.Equal(t, repo, row.RepoFullName, "the name is stored in lowercase")

		// The second App's installation does not take the binding.
		row, bound, err = ps.BindGitHubAppRepository(ctx, &models.GitHubAppRepository{RepoFullName: repo, InstallationID: installB})
		require.NoError(t, err)
		assert.False(t, bound)
		assert.Equal(t, installA, row.InstallationID)

		binding, err := ps.ResolveGitHubAppBinding(ctx, strings.ToUpper(repo))
		require.NoError(t, err)
		require.NotNil(t, binding)
		assert.Equal(t, appID, binding.App.AppID)
		assert.Equal(t, installA, binding.Installation.InstallationID)

		list, err := ps.ListGitHubAppInstallations(ctx, appID)
		require.NoError(t, err)
		require.Len(t, list, 1)
		assert.Equal(t, 1, list[0].RepositoryCount)

		// A disabled App or a suspended installation resolves to nothing;
		// then the other installation can take the binding.
		require.NoError(t, ps.SetGitHubAppEnabled(ctx, appID, false))
		binding, err = ps.ResolveGitHubAppBinding(ctx, repo)
		require.NoError(t, err)
		assert.Nil(t, binding)
		row, bound, err = ps.BindGitHubAppRepository(ctx, &models.GitHubAppRepository{RepoFullName: repo, InstallationID: installB})
		require.NoError(t, err)
		assert.True(t, bound)
		assert.Equal(t, installB, row.InstallationID)
		require.NoError(t, ps.SetGitHubAppInstallationSuspended(ctx, installB, true))
		binding, err = ps.ResolveGitHubAppBinding(ctx, repo)
		require.NoError(t, err)
		assert.Nil(t, binding)
		require.NoError(t, ps.SetGitHubAppInstallationSuspended(ctx, installB, false))

		// Rate limit snapshots.
		limit, remaining, used := 5000, 4200, 800
		reset := time.Now().Add(30 * time.Minute).UTC().Truncate(time.Second)
		observed := time.Now().UTC().Truncate(time.Second)
		snapshot := models.RateLimitSnapshot{Limit: &limit, Remaining: &remaining, Used: &used, ResetAt: &reset, ObservedAt: &observed}
		require.NoError(t, ps.RecordGitHubInstallationRateLimit(ctx, installB, snapshot))
		require.NoError(t, ps.RecordGitHubAppRateLimit(ctx, appID+1, snapshot))
		inst, err := ps.GetGitHubAppInstallation(ctx, installB)
		require.NoError(t, err)
		require.NotNil(t, inst.RateLimit.Remaining)
		assert.Equal(t, 4200, *inst.RateLimit.Remaining)
		assert.InDelta(t, 0.84, inst.RateLimit.Headroom(time.Now()), 0.001)
		app, err := ps.GetGitHubApp(ctx, appID+1)
		require.NoError(t, err)
		assert.Equal(t, 800, *app.RateLimit.Used)

		accounts, err := ps.ListActiveGitHubAppInstallationsForAccount(ctx, "ACME")
		require.NoError(t, err)
		require.Len(t, accounts, 1, "only the enabled App's installation")
		assert.Equal(t, installB, accounts[0].InstallationID)

		// Remove and delete.
		removed, found, err := ps.RemoveGitHubAppRepository(ctx, installB, repo, true, time.Now())
		require.NoError(t, err)
		assert.True(t, found)
		assert.True(t, removed.DisabledByRemoval)
		_, found, err = ps.RemoveGitHubAppRepository(ctx, installA, repo, false, time.Now())
		require.NoError(t, err)
		assert.False(t, found, "installation A does not hold the repository")
		_, _, err = ps.BindGitHubAppRepository(ctx, &models.GitHubAppRepository{RepoFullName: repo, InstallationID: installB})
		require.NoError(t, err)
		deleted, err := ps.MarkGitHubAppInstallationDeleted(ctx, installB, time.Now())
		require.NoError(t, err)
		require.Len(t, deleted, 1)
		binding, err = ps.ResolveGitHubAppBinding(ctx, repo)
		require.NoError(t, err)
		assert.Nil(t, binding)

		// Account mapping.
		org := newPrincipalOrg(t, ctx, uniqueName("gha-org"))
		require.NoError(t, ps.SetGitHubAccountOrg(ctx, "Acme-Corp", org.OrgID))
		mapping, err := ps.GetGitHubAccountOrg(ctx, "acme-corp")
		require.NoError(t, err)
		assert.Equal(t, org.OrgID, mapping.OrgID)
		ok, err := ps.DeleteGitHubAccountOrg(ctx, "ACME-CORP")
		require.NoError(t, err)
		assert.True(t, ok)
		_, err = ps.GetGitHubAccountOrg(ctx, "acme-corp")
		assert.ErrorIs(t, err, store.ErrNotFound)

		// The pull request of a commit, from ci_events.
		var projectID string
		require.NoError(t, tx.Raw(`INSERT INTO projects (name, repo_url, org_id) VALUES ('x', ?, ?) RETURNING project_id`,
			"github.com/"+uniqueName("gha")+"/x", org.OrgID).Scan(&projectID).Error)
		require.NoError(t, tx.Exec(`INSERT INTO ci_events (org_id, project_id, provider, repo, commit_sha, pr_number, event_type, source)
VALUES (?, ?, 'github', 'acme/x', 'sha1', 17, 'pull_request_updated', 'webhook')`, org.OrgID, projectID).Error)
		number, ok, err := ps.LatestPRNumberForCommit(ctx, projectID, "sha1")
		require.NoError(t, err)
		assert.True(t, ok)
		assert.Equal(t, 17, number)
		_, ok, err = ps.LatestPRNumberForCommit(ctx, projectID, "nosuch")
		require.NoError(t, err)
		assert.False(t, ok)
	})
}

// fakeAppAPI is the GitHub API of the Postgres tests.
type fakeAppAPI struct {
	mu       sync.Mutex
	appID    int64
	repos    []githubapp.Repository
	accounts string
}

func (f *fakeAppAPI) handler(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-RateLimit-Limit", "5000")
	w.Header().Set("X-RateLimit-Remaining", "4000")
	switch {
	case r.URL.Path == "/app":
		_ = json.NewEncoder(w).Encode(githubapp.AppInfo{ID: f.appID, Slug: "rc-test"})
	case r.URL.Path == "/app/installations":
		_ = json.NewEncoder(w).Encode([]githubapp.InstallationInfo{{ID: f.appID * 10, AppID: f.appID,
			Account: githubapp.Account{Login: f.accounts, Type: "Organization"}, RepositorySelection: "selected"}})
	case strings.HasSuffix(r.URL.Path, "/access_tokens"):
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(githubapp.InstallationToken{Token: "ghs_test", ExpiresAt: time.Now().Add(time.Hour)})
	case r.URL.Path == "/installation/repositories":
		_ = json.NewEncoder(w).Encode(map[string]any{"repositories": f.repos})
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func TestGitHubAppSelfServiceRegistration(t *testing.T) {
	RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
		ps := postgres_store.PostgresStore
		appID := time.Now().UnixNano()%1_000_000_000 + 7
		owner := uniqueName("ghacct")
		org := newPrincipalOrg(t, ctx, uniqueName("gha-reg"))
		api := &fakeAppAPI{appID: appID, accounts: owner, repos: []githubapp.Repository{
			{ID: 1, FullName: owner + "/private-app", Private: true}, {ID: 2, FullName: owner + "/public-lib"}}}
		server := httptest.NewServer(http.HandlerFunc(api.handler))
		defer server.Close()

		service, err := githubapp.NewService(githubapp.Options{Store: ps, Crypto: fakeCrypto{},
			Projects: githubapp.StoreRegistry{Store: ps, Apps: ps}, APIBaseURL: server.URL, AllowInsecureTransport: true})
		require.NoError(t, err)
		_, err = service.RegisterApp(ctx, githubapp.RegisterInput{AppID: appID, PrivateKeyPEM: githubAppTestKey(t),
			WebhookSecret: "hook", Verify: true})
		require.NoError(t, err)
		app, err := ps.GetGitHubApp(ctx, appID)
		require.NoError(t, err)
		assert.Equal(t, "rc-test", app.Slug)
		assert.NotContains(t, string(app.WebhookSecretCiphertext), "hook\x00")
		secret, ok, err := service.WebhookSecret(ctx, appID)
		require.NoError(t, err)
		assert.True(t, ok)
		assert.Equal(t, "hook", secret)

		require.NoError(t, ps.SetGitHubAccountOrg(ctx, owner, org.OrgID))
		result, err := service.SyncApp(ctx, appID)
		require.NoError(t, err)
		require.Empty(t, result.Errors)
		require.Len(t, result.Repositories, 2)

		private, err := ps.GetProjectByRepoURL(ctx, "github.com/"+owner+"/private-app")
		require.NoError(t, err)
		assert.True(t, private.Enabled)
		assert.True(t, private.IsPrivate)
		assert.Equal(t, org.OrgID, private.OrgID)
		public, err := ps.GetProjectByRepoURL(ctx, "github.com/"+owner+"/public-lib")
		require.NoError(t, err)
		assert.False(t, public.IsPrivate, "a public repository stays publicly visible")

		// The project now resolves to the App installation.
		client, ok, err := service.ClientForRepo(ctx, vcs.GitHub, owner+"/private-app")
		require.NoError(t, err)
		require.True(t, ok)
		_, isPublisher := client.(vcs.CheckRunPublisher)
		assert.True(t, isPublisher)

		// installation_repositories removed: the project is disabled, not
		// deleted.
		body, _ := json.Marshal(map[string]any{"action": "removed", "installation": map[string]any{"id": appID * 10, "app_id": appID,
			"account": map[string]any{"login": owner}}, "repositories_removed": []map[string]any{{"full_name": owner + "/private-app"}}})
		event, err := service.HandleInstallationEvent(ctx, appID, "installation_repositories", body)
		require.NoError(t, err)
		require.Len(t, event.Repositories, 1)
		assert.Equal(t, "disabled", event.Repositories[0].Action)
		private, err = ps.GetProjectByRepoURL(ctx, "github.com/"+owner+"/private-app")
		require.NoError(t, err)
		assert.False(t, private.Enabled)
		_, ok, err = service.ClientForRepo(ctx, vcs.GitHub, owner+"/private-app")
		require.NoError(t, err)
		assert.False(t, ok, "a removed repository uses the token path")

		var audits int64
		require.NoError(t, tx.Raw(`SELECT count(*) FROM audit_events WHERE action IN ('github_app.project_registered', 'github_app.project_disabled')
AND subject_id IN (?, ?)`, private.ProjectID, public.ProjectID).Scan(&audits).Error)
		assert.Equal(t, int64(3), audits)
	})
}

// Without an account mapping or open registration, a new project is
// created disabled and listed as pending approval until an administrator
// enables it. Org-wide secret grants do not reach it meanwhile. The project
// records the GitHub repository ID; the App scopes checkout and account
// tokens to the organization of the bound project.
func TestGitHubAppPendingRegistrationPostgres(t *testing.T) {
	RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
		ps := postgres_store.PostgresStore
		appID := time.Now().UnixNano()%1_000_000_000 + 11
		owner := uniqueName("ghpend")
		org := newPrincipalOrg(t, ctx, uniqueName("gha-pend"))
		other := newPrincipalOrg(t, ctx, uniqueName("gha-other"))
		api := &fakeAppAPI{appID: appID, accounts: owner, repos: []githubapp.Repository{{ID: 4242, FullName: owner + "/svc", Private: true}}}
		server := httptest.NewServer(http.HandlerFunc(api.handler))
		defer server.Close()
		service, err := githubapp.NewService(githubapp.Options{Store: ps, Crypto: fakeCrypto{},
			Projects: githubapp.StoreRegistry{Store: ps, Apps: ps}, APIBaseURL: server.URL, AllowInsecureTransport: true})
		require.NoError(t, err)
		_, err = service.RegisterApp(ctx, githubapp.RegisterInput{AppID: appID, PrivateKeyPEM: githubAppTestKey(t),
			WebhookSecret: "hook", Verify: true, DefaultOrgID: &org.OrgID})
		require.NoError(t, err)
		app, err := ps.GetGitHubApp(ctx, appID)
		require.NoError(t, err)
		assert.False(t, app.OpenRegistration, "open registration is off by default")

		result, err := service.SyncApp(ctx, appID)
		require.NoError(t, err)
		require.Len(t, result.Repositories, 1)
		assert.Equal(t, "pending_approval", result.Repositories[0].Action)
		project, err := ps.GetProjectByRepoURL(ctx, "github.com/"+owner+"/svc")
		require.NoError(t, err)
		assert.False(t, project.Enabled)
		require.NotNil(t, project.VCSRepositoryID)
		assert.Equal(t, int64(4242), *project.VCSRepositoryID)

		rows, err := ps.ListGitHubAppRepositories(ctx, appID*10)
		require.NoError(t, err)
		require.Len(t, rows, 1)
		assert.True(t, rows[0].PendingApproval)

		var audits int64
		require.NoError(t, tx.Raw(`SELECT count(*) FROM audit_events WHERE action = 'github_app.project_pending_approval' AND subject_id = ?`,
			project.ProjectID).Scan(&audits).Error)
		assert.Equal(t, int64(1), audits)

		// The checkout token is only for a job of the bound project.
		_, ok, err := service.CheckoutToken(ctx, vcs.GitHub, owner+"/svc", project.ProjectID)
		require.NoError(t, err)
		assert.True(t, ok)
		_, ok, err = service.CheckoutToken(ctx, vcs.GitHub, owner+"/svc", "00000000-0000-0000-0000-000000000000")
		require.NoError(t, err)
		assert.False(t, ok)
		// Account tokens are only for the organization of a bound project.
		_, ok, err = service.InstallationTokenForAccount(ctx, vcs.GitHub, owner, org.OrgID)
		require.NoError(t, err)
		assert.True(t, ok)
		_, ok, err = service.InstallationTokenForAccount(ctx, vcs.GitHub, owner, other.OrgID)
		require.NoError(t, err)
		assert.False(t, ok)

		// An administrator approves by enabling the project.
		project.Enabled = true
		require.NoError(t, ps.UpdateProject(ctx, project))
		rows, err = ps.ListGitHubAppRepositories(ctx, appID*10)
		require.NoError(t, err)
		assert.False(t, rows[0].PendingApproval, "an enabled project is not pending")
	})
}

// The REST routes with the admin token (no logins).
func TestGitHubAppRestAPI(t *testing.T) {
	RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
		du := &DataUtils{db: tx}
		admin, err := du.CreateUser(DataSetup{"Username": "gha-rest-admin", "Email": "gha@example.com"})
		require.NoError(t, err)
		auth, err := createTokenAuthHeader(ctx, tx, admin.UserID)
		require.NoError(t, err)
		org := newPrincipalOrg(t, ctx, uniqueName("gha-rest"))
		appID := time.Now().UnixNano()%1_000_000_000 + 3

		rr := principalRequest(t, ctx, http.MethodGet, "/api/v1/github-apps", "", nil)
		assert.Equal(t, http.StatusUnauthorized, rr.Code)

		rr = principalRequest(t, ctx, http.MethodPost, "/api/v1/github-apps", auth, map[string]any{"app_id": appID,
			"private_key": "garbage", "webhook_secret": "s", "verify": false})
		assert.Equal(t, http.StatusBadRequest, rr.Code, rr.Body.String())

		key := string(githubAppTestKey(t))
		rr = principalRequest(t, ctx, http.MethodPost, "/api/v1/github-apps", auth, map[string]any{"app_id": appID,
			"private_key": key, "webhook_secret": "very-secret-hook", "slug": "rc-rest", "default_org": org.Name, "verify": false})
		require.Equal(t, http.StatusCreated, rr.Code, rr.Body.String())
		assert.NotContains(t, rr.Body.String(), "very-secret-hook")
		assert.NotContains(t, rr.Body.String(), "PRIVATE KEY")
		var view struct {
			AppID        int64   `json:"app_id"`
			Slug         string  `json:"slug"`
			Enabled      bool    `json:"enabled"`
			DefaultOrgID *string `json:"default_org_id"`
			InstallURL   string  `json:"install_url"`
			Headroom     float64 `json:"headroom"`
		}
		require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &view))
		assert.Equal(t, appID, view.AppID)
		assert.True(t, view.Enabled)
		assert.Equal(t, "https://github.com/apps/rc-rest/installations/new", view.InstallURL)
		require.NotNil(t, view.DefaultOrgID)
		assert.Equal(t, org.OrgID, *view.DefaultOrgID)
		assert.Equal(t, 1.0, view.Headroom)

		rr = principalRequest(t, ctx, http.MethodPost, "/api/v1/github-apps", auth, map[string]any{"app_id": appID,
			"private_key": key, "webhook_secret": "s", "verify": false})
		assert.Equal(t, http.StatusConflict, rr.Code)

		rr = principalRequest(t, ctx, http.MethodGet, "/api/v1/github-apps", auth, nil)
		require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
		assert.Contains(t, rr.Body.String(), fmt.Sprintf(`"app_id":%d`, appID))
		assert.NotContains(t, rr.Body.String(), "very-secret-hook")

		rr = principalRequest(t, ctx, http.MethodGet, fmt.Sprintf("/api/v1/github-apps/%d", appID), auth, nil)
		require.Equal(t, http.StatusOK, rr.Code)
		rr = principalRequest(t, ctx, http.MethodGet, "/api/v1/github-apps/recommended", auth, nil)
		require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())

		rr = principalRequest(t, ctx, http.MethodPost, fmt.Sprintf("/api/v1/github-apps/%d/disable", appID), auth, nil)
		require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
		assert.Contains(t, rr.Body.String(), `"enabled":false`)
		rr = principalRequest(t, ctx, http.MethodPost, fmt.Sprintf("/api/v1/github-apps/%d/enable", appID), auth, nil)
		require.Equal(t, http.StatusOK, rr.Code)
		assert.Contains(t, rr.Body.String(), `"enabled":true`)

		rr = principalRequest(t, ctx, http.MethodGet, fmt.Sprintf("/api/v1/github-apps/%d/installations", appID), auth, nil)
		require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
		assert.Contains(t, rr.Body.String(), `"installations":[]`)
		rr = principalRequest(t, ctx, http.MethodGet, "/api/v1/github-apps/999999999999/installations", auth, nil)
		assert.Equal(t, http.StatusNotFound, rr.Code)

		rr = principalRequest(t, ctx, http.MethodPut, "/api/v1/github-apps/account-orgs/Acme", auth, map[string]string{"org": org.Name})
		require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
		assert.Contains(t, rr.Body.String(), `"account_login":"acme"`)
		rr = principalRequest(t, ctx, http.MethodGet, "/api/v1/github-apps/account-orgs", auth, nil)
		require.Equal(t, http.StatusOK, rr.Code)
		assert.Contains(t, rr.Body.String(), org.OrgID)
		rr = principalRequest(t, ctx, http.MethodDelete, "/api/v1/github-apps/account-orgs/acme", auth, nil)
		assert.Equal(t, http.StatusNoContent, rr.Code)

		var audits int64
		require.NoError(t, tx.Raw(`SELECT count(*) FROM audit_events WHERE action LIKE 'github_app.%' AND subject_id = ?`,
			fmt.Sprint(appID)).Scan(&audits).Error)
		assert.Equal(t, int64(3), audits, "registered, disabled, enabled")
	})
}
