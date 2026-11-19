package githubapp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/vcs"
)

func TestAppJWT_RS256ClaimsAndSignature(t *testing.T) {
	key, pemBytes := testKey(t)
	parsed, err := ParsePrivateKey(pemBytes)
	require.NoError(t, err)
	now := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)

	token, err := AppJWT(42, parsed, now)
	require.NoError(t, err)

	signed, err := jwt.ParseSigned(token, []jose.SignatureAlgorithm{jose.RS256})
	require.NoError(t, err)
	require.Len(t, signed.Headers, 1)
	assert.Equal(t, "RS256", signed.Headers[0].Algorithm)
	var claims jwt.Claims
	require.NoError(t, signed.Claims(&key.PublicKey, &claims))
	assert.Equal(t, "42", claims.Issuer)
	assert.Equal(t, now.Add(-60*time.Second).Unix(), claims.IssuedAt.Time().Unix())
	assert.Equal(t, now.Add(9*time.Minute).Unix(), claims.Expiry.Time().Unix())
	assert.LessOrEqual(t, claims.Expiry.Time().Sub(now), 10*time.Minute)

	// A different key does not verify.
	other, _ := testKey(t)
	assert.Error(t, signed.Claims(&other.PublicKey, &claims))
}

func TestParsePrivateKey_RejectsGarbageWithoutEcho(t *testing.T) {
	_, err := ParsePrivateKey([]byte("not a key SECRETVALUE"))
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "SECRETVALUE")
}

func TestRegisterApp_VerifiesAndEncrypts(t *testing.T) {
	_, apps, _, api, _ := newTestService(t)
	app := apps.apps[42]
	require.NotNil(t, app)
	assert.Equal(t, "reactorcide-a", app.Slug, "slug from GET /app")
	assert.Equal(t, "Iv1.abc", app.ClientID)
	assert.True(t, strings.HasPrefix(string(app.WebhookSecretCiphertext), "enc:"))
	assert.True(t, strings.HasPrefix(string(app.PrivateKeyCiphertext), "enc:"))
	// GET /app used the App JWT.
	assert.True(t, strings.HasPrefix(api.auth[0], "Bearer "))
	// The JSON form of the App has no secret.
	encoded, _ := json.Marshal(app)
	assert.NotContains(t, string(encoded), "hook-secret")
	assert.NotContains(t, string(encoded), "PRIVATE KEY")
}

func TestRegisterApp_RefusesKeyOfAnotherApp(t *testing.T) {
	service, _, _, _, _ := newTestService(t)
	_, pemBytes := testKey(t)
	_, err := service.RegisterApp(context.Background(), RegisterInput{AppID: 7, PrivateKeyPEM: pemBytes, WebhookSecret: "s", Verify: true})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "belongs to App 42")
}

func seedInstallation(t *testing.T, apps *fakeStore, id int64, account string, granted map[string]string, repos ...string) {
	t.Helper()
	require.NoError(t, apps.UpsertGitHubAppInstallation(context.Background(), &models.GitHubAppInstallation{
		InstallationID: id, AppID: 42, AccountLogin: account, Permissions: granted}))
	for _, repo := range repos {
		_, _, err := apps.BindGitHubAppRepository(context.Background(), &models.GitHubAppRepository{RepoFullName: repo, InstallationID: id})
		require.NoError(t, err)
	}
}

func TestInstallationToken_CachedUntilFiveMinutesBeforeExpiry(t *testing.T) {
	service, apps, _, api, clock := newTestService(t)
	seedInstallation(t, apps, 1001, "acme", map[string]string{"contents": "read", "metadata": "read", "checks": "write",
		"statuses": "write", "pull_requests": "write", "issues": "write"}, "acme/app")
	ctx := context.Background()

	first, ok, err := service.ClientForRepo(ctx, vcs.GitHub, "acme/app")
	require.NoError(t, err)
	require.True(t, ok)
	second, _, err := service.ClientForRepo(ctx, vcs.GitHub, "Acme/App")
	require.NoError(t, err)
	assert.Equal(t, 1, api.tokenCalls, "the second client reuses the cached token")
	assert.Equal(t, first.(*AppClient).token, second.(*AppClient).token)

	// The request asked only for granted permissions: members was not
	// granted, so it is not in the request; issues is not wanted.
	require.Len(t, api.tokenBodies, 1)
	assert.Equal(t, map[string]string{"contents": "read", "metadata": "read", "checks": "write", "statuses": "write",
		"pull_requests": "write"}, api.tokenBodies[0].Permissions)
	assert.Empty(t, api.tokenBodies[0].Repositories)

	clock.Advance(54 * time.Minute)
	_, _, err = service.ClientForRepo(ctx, vcs.GitHub, "acme/app")
	require.NoError(t, err)
	assert.Equal(t, 1, api.tokenCalls, "54 minutes into a 60 minute token: still cached")

	clock.Advance(2 * time.Minute)
	third, _, err := service.ClientForRepo(ctx, vcs.GitHub, "acme/app")
	require.NoError(t, err)
	assert.Equal(t, 2, api.tokenCalls, "less than 5 minutes left: a new token")
	assert.NotEqual(t, first.(*AppClient).token, third.(*AppClient).token)
}

func TestCheckoutToken_OneRepositoryReadOnly(t *testing.T) {
	service, apps, _, api, _ := newTestService(t)
	seedInstallation(t, apps, 1001, "acme", map[string]string{"contents": "write", "metadata": "read", "checks": "write"}, "acme/app")
	require.NoError(t, apps.SetGitHubAppRepositoryProject(context.Background(), "acme/app", "proj-acme", false))

	token, ok, err := service.CheckoutToken(context.Background(), vcs.GitHub, "acme/app", "proj-acme")
	require.NoError(t, err)
	require.True(t, ok)
	assert.NotEmpty(t, token)
	require.Len(t, api.tokenBodies, 1)
	assert.Equal(t, []string{"app"}, api.tokenBodies[0].Repositories)
	assert.Equal(t, map[string]string{"contents": "read", "metadata": "read"}, api.tokenBodies[0].Permissions)

	// The system token is a separate cache entry.
	_, _, err = service.ClientForRepo(context.Background(), vcs.GitHub, "acme/app")
	require.NoError(t, err)
	assert.Equal(t, 2, api.tokenCalls)

	// A repository in no installation keeps the token path.
	_, ok, err = service.CheckoutToken(context.Background(), vcs.GitHub, "fork/app", "proj-acme")
	require.NoError(t, err)
	assert.False(t, ok)
}

// A job of another project (another organization) names the source URL of an
// App-bound private repository. It gets no installation token.
func TestCheckoutToken_RefusesAnotherProject(t *testing.T) {
	service, apps, _, api, _ := newTestService(t)
	seedInstallation(t, apps, 1001, "victim", map[string]string{"contents": "read", "metadata": "read"}, "victim/private", "victim/unlinked")
	require.NoError(t, apps.SetGitHubAppRepositoryProject(context.Background(), "victim/private", "proj-victim", false))

	for _, projectID := range []string{"proj-attacker", ""} {
		_, ok, err := service.CheckoutToken(context.Background(), vcs.GitHub, "victim/private", projectID)
		require.NoError(t, err)
		assert.False(t, ok, "project %q", projectID)
	}
	// A binding with no project gives no checkout token to anyone.
	_, ok, err := service.CheckoutToken(context.Background(), vcs.GitHub, "victim/unlinked", "proj-attacker")
	require.NoError(t, err)
	assert.False(t, ok)
	assert.Equal(t, 0, api.tokenCalls, "no token request for a refused checkout")
}

func TestClientForRepo_SuspendedOrDisabledFallsBack(t *testing.T) {
	service, apps, _, _, _ := newTestService(t)
	seedInstallation(t, apps, 1001, "acme", nil, "acme/app")
	ctx := context.Background()
	_, ok, _ := service.ClientForRepo(ctx, vcs.GitHub, "acme/app")
	require.True(t, ok)

	require.NoError(t, service.SetAppEnabled(ctx, 42, false))
	_, ok, _ = service.ClientForRepo(ctx, vcs.GitHub, "acme/app")
	assert.False(t, ok, "a disabled App gives no client")
	require.NoError(t, service.SetAppEnabled(ctx, 42, true))

	require.NoError(t, apps.SetGitHubAppInstallationSuspended(ctx, 1001, true))
	_, ok, _ = service.ClientForRepo(ctx, vcs.GitHub, "acme/app")
	assert.False(t, ok, "a suspended installation gives no client")

	_, ok, _ = service.ClientForRepo(ctx, vcs.GitLab, "acme/app")
	assert.False(t, ok)
}

func TestRateLimitHeadersRecordedPerAppAndInstallation(t *testing.T) {
	service, apps, _, api, _ := newTestService(t)
	seedInstallation(t, apps, 1001, "acme", nil, "acme/app")
	// GET /app during registration was an App call.
	appRate, ok := apps.appRates[42]
	require.True(t, ok)
	assert.Equal(t, 5000, *appRate.Limit)
	assert.Equal(t, 4999, *appRate.Remaining)
	assert.Equal(t, 1, *appRate.Used)
	require.NotNil(t, appRate.ResetAt)

	api.remaining = 120
	client, ok, err := service.ClientForRepo(context.Background(), vcs.GitHub, "acme/app")
	require.NoError(t, err)
	require.True(t, ok)
	require.NoError(t, client.UpdateCommitStatus(context.Background(), "acme/app", vcs.StatusUpdate{SHA: "abc", State: vcs.StatusSuccess, Context: "lint"}))
	instRate, ok := apps.instRates[1001]
	require.True(t, ok, "an installation-token call records the installation")
	assert.Equal(t, 120, *instRate.Remaining)
}

func TestRateLimitRecorder_Throttles(t *testing.T) {
	apps := newFakeStore()
	recorder := newRateLimitRecorder(apps, time.Minute)
	now := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	snap := func(remaining int, at time.Time) models.RateLimitSnapshot {
		limit := 5000
		return models.RateLimitSnapshot{Limit: &limit, Remaining: &remaining, ObservedAt: &at}
	}
	target := rateLimitTarget{installationID: 9}
	recorder.observe(target, snap(4000, now))
	recorder.observe(target, snap(3990, now.Add(time.Second)))
	assert.Equal(t, 4000, *apps.instRates[9].Remaining, "the second write waits for the interval")
	recorder.observe(target, snap(100, now.Add(2*time.Second)))
	assert.Equal(t, 100, *apps.instRates[9].Remaining, "below 10% writes at once")
	latest, _ := recorder.snapshot(target)
	assert.Equal(t, 100, *latest.Remaining)
}

func TestSelectApp_MostHeadroom(t *testing.T) {
	now := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	reset := now.Add(10 * time.Minute)
	past := now.Add(-time.Minute)
	n := func(v int) *int { return &v }
	apps := []models.GitHubApp{
		{AppID: 1, Enabled: true},
		{AppID: 2, Enabled: true},
		{AppID: 3, Enabled: false},
	}
	installations := []models.GitHubAppInstallation{
		{InstallationID: 10, AppID: 1, RepositoryCount: 3, RateLimit: models.RateLimitSnapshot{Limit: n(5000), Remaining: n(500), ResetAt: &reset}},
		{InstallationID: 20, AppID: 2, RepositoryCount: 9, RateLimit: models.RateLimitSnapshot{Limit: n(5000), Remaining: n(4000), ResetAt: &reset}},
		{InstallationID: 30, AppID: 3, RateLimit: models.RateLimitSnapshot{Limit: n(5000), Remaining: n(5000), ResetAt: &reset}},
	}
	selected, headroom, ok := SelectApp(apps, installations, now)
	require.True(t, ok)
	assert.Equal(t, int64(2), selected.AppID, "App 2 has 80% left, App 1 10%, App 3 is disabled")
	assert.InDelta(t, 0.8, headroom.Headroom, 0.001)

	// A snapshot past its reset counts as full: App 1 then ties App 2 at
	// full headroom only if App 2 is also reset; here App 1 wins on reset.
	installations[0].RateLimit.ResetAt = &past
	selected, _, _ = SelectApp(apps, installations, now)
	assert.Equal(t, int64(1), selected.AppID)

	// A tie goes to the App with fewer repositories.
	installations[1].RateLimit.ResetAt = &past
	selected, _, _ = SelectApp(apps, installations, now)
	assert.Equal(t, int64(1), selected.AppID, "3 repositories < 9")

	// A suspended installation does not count.
	installations[0].Suspended = true
	installations[0].RateLimit.ResetAt = &reset
	installations[0].RateLimit.Remaining = n(0)
	h := ComputeHeadroom(apps, installations, now)
	assert.Equal(t, 1.0, h[1].Headroom)
	assert.Equal(t, 0, h[1].ActiveInstallations)

	_, _, ok = SelectApp([]models.GitHubApp{{AppID: 3}}, nil, now)
	assert.False(t, ok)
}

func installationEvent(action string, repos ...Repository) []byte {
	payload := map[string]any{"action": action, "installation": map[string]any{"id": 1001, "app_id": 42,
		"account": map[string]any{"login": "acme", "type": "Organization"}, "repository_selection": "selected",
		"permissions": map[string]string{"checks": "write", "contents": "read"}}, "sender": map[string]any{"login": "owner"}}
	if action == "created" {
		payload["repositories"] = repos
	}
	data, _ := json.Marshal(payload)
	return data
}

func reposEvent(added, removed []Repository) []byte {
	data, _ := json.Marshal(map[string]any{"action": "added", "installation": map[string]any{"id": 1001, "app_id": 42,
		"account": map[string]any{"login": "acme", "type": "Organization"}},
		"repositories_added": added, "repositories_removed": removed})
	return data
}

func TestInstallationEvents_AutoRegisterAndDisable(t *testing.T) {
	service, apps, registry, _, _ := newTestService(t)
	ctx := context.Background()
	mappedOrg := "00000000-0000-0000-0000-0000000000aa"
	require.NoError(t, apps.SetGitHubAccountOrg(ctx, "ACME", mappedOrg))
	// A project from before the App, on the token path.
	registry.projects["github.com/acme/legacy"] = &models.Project{ProjectID: "00000000-0000-0000-0000-00000000beef",
		RepoURL: "github.com/acme/legacy", Enabled: true, VCSTokenSecret: "vcs/acme:token"}

	result, err := service.HandleInstallationEvent(ctx, 42, "installation", installationEvent("created",
		Repository{ID: 1, FullName: "acme/app", Private: true}, Repository{ID: 2, FullName: "acme/legacy"}))
	require.NoError(t, err)
	require.Len(t, result.Repositories, 2)
	assert.Equal(t, "created", result.Repositories[0].Action)
	assert.Equal(t, "bound", result.Repositories[1].Action)

	created := registry.project("github.com/acme/app")
	require.NotNil(t, created)
	assert.True(t, created.Enabled)
	assert.True(t, created.IsPrivate, "is_private follows the repository")
	assert.Equal(t, mappedOrg, created.OrgID, "the account mapping chooses the organization")
	assert.Equal(t, "app", created.Name)
	assert.Equal(t, "acme", apps.installations[1001].AccountLogin)
	assert.Equal(t, "write", apps.installations[1001].Permissions["checks"])

	// installation_repositories added: a public repository, default org when
	// the mapping is gone.
	_, err = apps.DeleteGitHubAccountOrg(ctx, "acme")
	require.NoError(t, err)
	_, err = service.HandleInstallationEvent(ctx, 42, "installation_repositories", reposEvent([]Repository{{ID: 3, FullName: "acme/lib"}}, nil))
	require.NoError(t, err)
	lib := registry.project("github.com/acme/lib")
	require.NotNil(t, lib)
	assert.False(t, lib.IsPrivate)
	assert.Equal(t, registry.defaultOrg, lib.OrgID)

	// removed: the auto-registered project is disabled, not deleted; the
	// legacy project is only unbound.
	result, err = service.HandleInstallationEvent(ctx, 42, "installation_repositories", reposEvent(nil,
		[]Repository{{FullName: "acme/app"}, {FullName: "acme/legacy"}}))
	require.NoError(t, err)
	assert.Equal(t, "disabled", result.Repositories[0].Action)
	assert.Equal(t, "removed", result.Repositories[1].Action)
	assert.False(t, registry.project("github.com/acme/app").Enabled)
	assert.True(t, registry.project("github.com/acme/legacy").Enabled, "a token-path project is not disabled")
	binding, _ := apps.ResolveGitHubAppBinding(ctx, "acme/app")
	assert.Nil(t, binding, "a removed repository gives no token")

	// Added again: the project is enabled again.
	result, err = service.HandleInstallationEvent(ctx, 42, "installation_repositories", reposEvent([]Repository{{ID: 1, FullName: "acme/app", Private: true}}, nil))
	require.NoError(t, err)
	assert.Equal(t, "enabled", result.Repositories[0].Action)
	assert.True(t, registry.project("github.com/acme/app").Enabled)

	// Installation deleted: auto-registered projects are disabled.
	_, err = service.HandleInstallationEvent(ctx, 42, "installation", installationEvent("deleted"))
	require.NoError(t, err)
	assert.False(t, registry.project("github.com/acme/app").Enabled)
	assert.False(t, registry.project("github.com/acme/lib").Enabled)
	assert.NotNil(t, apps.installations[1001].DeletedAt)
}

func TestInstallationEvents_SuspendAndOtherApp(t *testing.T) {
	service, apps, _, _, _ := newTestService(t)
	ctx := context.Background()
	_, err := service.HandleInstallationEvent(ctx, 42, "installation", installationEvent("created", Repository{ID: 1, FullName: "acme/app"}))
	require.NoError(t, err)
	_, err = service.HandleInstallationEvent(ctx, 42, "installation", installationEvent("suspend"))
	require.NoError(t, err)
	assert.True(t, apps.installations[1001].Suspended)
	_, err = service.HandleInstallationEvent(ctx, 42, "installation", installationEvent("unsuspend"))
	require.NoError(t, err)
	assert.False(t, apps.installations[1001].Suspended)

	// An installation of App 42 delivered to App 7 is refused.
	apps.apps[7] = &models.GitHubApp{AppID: 7, Enabled: true}
	_, err = service.HandleInstallationEvent(ctx, 7, "installation", installationEvent("created"))
	require.Error(t, err)
}

func TestBinding_SecondInstallationKeepsFirst(t *testing.T) {
	_, apps, _, _, _ := newTestService(t)
	ctx := context.Background()
	seedInstallation(t, apps, 1001, "acme", nil, "acme/app")
	apps.apps[43] = &models.GitHubApp{AppID: 43, Enabled: true}
	require.NoError(t, apps.UpsertGitHubAppInstallation(ctx, &models.GitHubAppInstallation{InstallationID: 2002, AppID: 43, AccountLogin: "acme"}))
	row, bound, err := apps.BindGitHubAppRepository(ctx, &models.GitHubAppRepository{RepoFullName: "acme/app", InstallationID: 2002})
	require.NoError(t, err)
	assert.False(t, bound)
	assert.Equal(t, int64(1001), row.InstallationID)
}

func TestSyncApp_ReadsInstallationsAndRepositories(t *testing.T) {
	service, apps, registry, api, _ := newTestService(t)
	api.installs = []InstallationInfo{{ID: 1001, AppID: 42, Account: Account{Login: "acme", Type: "Organization"}, RepositorySelection: "all"}}
	api.repos[1001] = []Repository{{ID: 1, FullName: "acme/app"}}
	// A stale binding that GitHub no longer lists.
	seedInstallation(t, apps, 1001, "acme", nil, "acme/old")
	result, err := service.SyncApp(context.Background(), 42)
	require.NoError(t, err)
	assert.Equal(t, 1, result.Installations)
	assert.Empty(t, result.Errors)
	require.NotNil(t, registry.project("github.com/acme/app"))
	binding, _ := apps.ResolveGitHubAppBinding(context.Background(), "acme/old")
	assert.Nil(t, binding)
}

func TestAppIDFromHeadersAndParseRerun(t *testing.T) {
	id, ok := AppIDFromHeaders("integration", "42")
	assert.True(t, ok)
	assert.Equal(t, int64(42), id)
	_, ok = AppIDFromHeaders("repository", "42")
	assert.False(t, ok)
	_, ok = AppIDFromHeaders("integration", "x")
	assert.False(t, ok)

	body := []byte(`{"action":"rerequested","check_run":{"id":5,"name":"Reactorcide Checks","head_sha":"abc","app":{"id":42},
"pull_requests":[{"number":7}]},"repository":{"full_name":"acme/app"},"sender":{"login":"octo"}}`)
	request, rerun, err := ParseRerunRequest("check_run", body)
	require.NoError(t, err)
	assert.True(t, rerun)
	assert.Equal(t, []int{7}, request.PRNumbers)
	assert.Equal(t, int64(42), request.AppID)
	assert.Equal(t, "octo", request.SenderLogin)

	body = []byte(`{"action":"requested_action","requested_action":{"identifier":"rerun"},"check_run":{"id":5,"name":"Reactorcide Checks","head_sha":"abc","app":{"id":42}},"repository":{"full_name":"acme/app"},"sender":{"login":"octo"}}`)
	_, rerun, err = ParseRerunRequest("check_run", body)
	require.NoError(t, err)
	assert.True(t, rerun)

	body = []byte(`{"action":"requested_action","requested_action":{"identifier":"other"},"check_run":{"id":5,"app":{"id":42}}}`)
	_, rerun, _ = ParseRerunRequest("check_run", body)
	assert.False(t, rerun)
	body = []byte(`{"action":"completed","check_run":{"id":5,"app":{"id":42}}}`)
	_, rerun, _ = ParseRerunRequest("check_run", body)
	assert.False(t, rerun)
	body = []byte(`{"action":"rerequested","check_suite":{"id":9,"head_sha":"abc","app":{"id":42},"pull_requests":[]},"repository":{"full_name":"acme/app"}}`)
	request, rerun, _ = ParseRerunRequest("check_suite", body)
	assert.True(t, rerun)
	assert.Equal(t, "check_suite", request.Kind)
}

func TestAccountTokenProvider(t *testing.T) {
	service, apps, registry, _, _ := newTestService(t)
	ctx := context.Background()
	acmeOrg, otherOrg, allowOrg := "00000000-0000-0000-0000-0000000000a1", "00000000-0000-0000-0000-0000000000b2", "00000000-0000-0000-0000-0000000000c3"
	seedInstallation(t, apps, 1001, "acme", map[string]string{"members": "read"}, "acme/app")
	registry.projects["github.com/acme/app"] = &models.Project{ProjectID: "00000000-0000-0000-0000-00000000a0a0", RepoURL: "github.com/acme/app", OrgID: acmeOrg}
	require.NoError(t, apps.SetGitHubAppRepositoryProject(ctx, "acme/app", "00000000-0000-0000-0000-00000000a0a0", false))
	registry.allowlists = map[string][]string{allowOrg: {"acme"}}

	// The organization of a bound project of the installation.
	token, ok, err := service.InstallationTokenForAccount(ctx, vcs.GitHub, "ACME", acmeOrg)
	require.NoError(t, err)
	require.True(t, ok)
	assert.NotEmpty(t, token)
	client, ok, err := service.ClientForAccount(ctx, vcs.GitHub, "acme", acmeOrg)
	require.NoError(t, err)
	require.True(t, ok)
	_, isLister := client.(vcs.TeamMemberLister)
	assert.True(t, isLister, "an App client reads team members as the token client does")

	// An organization with the account in its VCS owner allowlist.
	_, ok, err = service.InstallationTokenForAccount(ctx, vcs.GitHub, "acme", allowOrg)
	require.NoError(t, err)
	assert.True(t, ok)

	// Another organization gets no token of the account.
	for _, org := range []string{otherOrg, ""} {
		_, ok, err = service.InstallationTokenForAccount(ctx, vcs.GitHub, "acme", org)
		require.NoError(t, err)
		assert.False(t, ok, "org %q", org)
		_, ok, err = service.ClientForAccount(ctx, vcs.GitHub, "acme", org)
		require.NoError(t, err)
		assert.False(t, ok, "org %q", org)
	}
	_, ok, _ = service.InstallationTokenForAccount(ctx, vcs.GitHub, "nobody", acmeOrg)
	assert.False(t, ok)
}

// Anybody can install a public App. Without an account mapping and without
// open registration, the new project is created disabled and waits for an
// administrator.
func TestRegistration_PendingApprovalWithoutMappingOrOpenRegistration(t *testing.T) {
	service, apps, registry, _, _ := newTestService(t)
	ctx := context.Background()
	appOrg := "00000000-0000-0000-0000-0000000000bb"
	apps.apps[42].DefaultOrgID = &appOrg

	result, err := service.HandleInstallationEvent(ctx, 42, "installation", installationEvent("created",
		Repository{ID: 11, FullName: "acme/app"}))
	require.NoError(t, err)
	require.Len(t, result.Repositories, 1)
	assert.Equal(t, "pending_approval", result.Repositories[0].Action)
	created := registry.project("github.com/acme/app")
	require.NotNil(t, created)
	assert.False(t, created.Enabled, "a default org alone does not enable the project")
	assert.Equal(t, appOrg, created.OrgID)
	require.NotNil(t, created.VCSRepositoryID)
	assert.Equal(t, int64(11), *created.VCSRepositoryID)
	assert.True(t, apps.repos["acme/app"].PendingApproval)

	// Open registration with an explicit default organization enables.
	apps.apps[42].OpenRegistration = true
	result, err = service.HandleInstallationEvent(ctx, 42, "installation_repositories", reposEvent([]Repository{{ID: 12, FullName: "acme/lib"}}, nil))
	require.NoError(t, err)
	assert.Equal(t, "created", result.Repositories[0].Action)
	assert.True(t, registry.project("github.com/acme/lib").Enabled)
	assert.False(t, apps.repos["acme/lib"].PendingApproval)

	// Open registration without a default organization does not enable a
	// project in the deployment default organization.
	apps.apps[42].DefaultOrgID = nil
	result, err = service.HandleInstallationEvent(ctx, 42, "installation_repositories", reposEvent([]Repository{{ID: 13, FullName: "acme/tool"}}, nil))
	require.NoError(t, err)
	assert.Equal(t, "pending_approval", result.Repositories[0].Action)
	assert.False(t, registry.project("github.com/acme/tool").Enabled)
}

func TestRegistrationApproved(t *testing.T) {
	org := "00000000-0000-0000-0000-0000000000bb"
	assert.True(t, RegistrationApproved(nil, OrgSourceAccountMapping))
	assert.False(t, RegistrationApproved(&models.GitHubApp{DefaultOrgID: &org}, OrgSourceAppDefault))
	assert.True(t, RegistrationApproved(&models.GitHubApp{DefaultOrgID: &org, OpenRegistration: true}, OrgSourceAppDefault))
	assert.False(t, RegistrationApproved(&models.GitHubApp{OpenRegistration: true}, OrgSourceDeploymentDefault))
}

// GitHub can give the name of a deleted repository to a new repository. A
// project with another repository ID is not linked to the new binding, so
// the new repository gets no checkout token of the project.
func TestRegistration_RepositoryIDMismatchIsNotLinked(t *testing.T) {
	service, apps, registry, _, _ := newTestService(t)
	ctx := context.Background()
	require.NoError(t, apps.SetGitHubAccountOrg(ctx, "acme", "00000000-0000-0000-0000-0000000000aa"))
	oldID := int64(500)
	registry.projects["github.com/acme/app"] = &models.Project{ProjectID: "00000000-0000-0000-0000-00000000beef", Name: "app",
		RepoURL: "github.com/acme/app", Enabled: true, VCSRepositoryID: &oldID}
	registry.projects["github.com/acme/old"] = &models.Project{ProjectID: "00000000-0000-0000-0000-00000000cafe", Name: "old",
		RepoURL: "github.com/acme/old", Enabled: true}

	result, err := service.HandleInstallationEvent(ctx, 42, "installation", installationEvent("created",
		Repository{ID: 501, FullName: "acme/app"}, Repository{ID: 77, FullName: "acme/old"}))
	require.NoError(t, err)
	require.Len(t, result.Repositories, 2)
	assert.Equal(t, "bound", result.Repositories[0].Action)
	assert.Contains(t, result.Repositories[0].Detail, "not linked")
	assert.Nil(t, apps.repos["acme/app"].ProjectID, "the binding of the new repository has no project")
	_, ok, err := service.CheckoutToken(ctx, vcs.GitHub, "acme/app", "00000000-0000-0000-0000-00000000beef")
	require.NoError(t, err)
	assert.False(t, ok)

	// A project with no ID yet records the ID of its first binding.
	require.NotNil(t, registry.project("github.com/acme/old").VCSRepositoryID)
	assert.Equal(t, int64(77), *registry.project("github.com/acme/old").VCSRepositoryID)
}
