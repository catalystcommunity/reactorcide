package uiapi

import (
	"context"
	"testing"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/uiapi/csilapi"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/vcs/githubapp"
)

type fakeGitHubAppPool struct {
	store.GitHubAppStore
	apps    []githubapp.AppSummary
	enabled map[int64]bool
	synced  []int64
}

func (p *fakeGitHubAppPool) Summaries(context.Context) ([]githubapp.AppSummary, error) {
	out := append([]githubapp.AppSummary(nil), p.apps...)
	for i := range out {
		if enabled, ok := p.enabled[out[i].App.AppID]; ok {
			out[i].App.Enabled = enabled
		}
	}
	return out, nil
}
func (p *fakeGitHubAppPool) SetAppEnabled(_ context.Context, appID int64, enabled bool) error {
	p.enabled[appID] = enabled
	return nil
}
func (p *fakeGitHubAppPool) SyncApp(_ context.Context, appID int64) (githubapp.SyncResult, error) {
	p.synced = append(p.synced, appID)
	return githubapp.SyncResult{AppID: appID, Installations: 1, Repositories: []githubapp.RegistrationResult{
		{Repository: "acme/a", Action: "created"}, {Repository: "acme/b", Action: "disabled"}, {Repository: "acme/c", Action: "bound"}}}, nil
}
func (p *fakeGitHubAppPool) Store() store.GitHubAppStore { return p }
func (p *fakeGitHubAppPool) ListGitHubAppInstallations(context.Context, int64) ([]models.GitHubAppInstallation, error) {
	limit, remaining := 5000, 1000
	reset := time.Now().Add(time.Hour)
	return []models.GitHubAppInstallation{{InstallationID: 9, AppID: 42, AccountLogin: "acme", AccountType: "Organization",
		RepositorySelection: "selected", RateLimit: models.RateLimitSnapshot{Limit: &limit, Remaining: &remaining, ResetAt: &reset}}}, nil
}
func (p *fakeGitHubAppPool) ListGitHubAppRepositories(context.Context, int64) ([]models.GitHubAppRepository, error) {
	return []models.GitHubAppRepository{{RepoFullName: "acme/app"}}, nil
}

func withGitHubAppPool(t *testing.T, pool githubAppPool) {
	t.Helper()
	previous := githubAppPoolSource
	githubAppPoolSource = func() githubAppPool { return pool }
	t.Cleanup(func() { githubAppPoolSource = previous })
}

func TestGitHubAppOperationsNeedGlobalAdmin(t *testing.T) {
	deps, st := newTestDeps(t)
	user := st.putUser(models.User{UserID: "user-1"})
	withGitHubAppPool(t, &fakeGitHubAppPool{enabled: map[int64]bool{}})
	ctx := mintSessionCtx(t, deps, user.UserID)
	_, err := NewUiService(deps).ListGithubApps(ctx, csilapi.ListGitHubAppsRequest{})
	requireCode(t, err, "forbidden")
	_, err = NewUiService(deps).SyncGithubApp(ctx, csilapi.SyncGitHubAppRequest{AppId: 42})
	requireCode(t, err, "forbidden")
}

func TestGitHubAppOperationsListToggleSync(t *testing.T) {
	deps, st := newTestDeps(t)
	admin := st.putUser(models.User{UserID: "admin-1"})
	seedGlobalAdmin(st, admin.UserID)
	pool := &fakeGitHubAppPool{enabled: map[int64]bool{}, apps: []githubapp.AppSummary{{
		App:      models.GitHubApp{AppID: 42, Slug: "rc", Enabled: true},
		Headroom: githubapp.AppHeadroom{AppID: 42, Headroom: 0.2, ActiveInstallations: 1, Repositories: 1}, Recommended: true,
		InstallURL: "https://github.com/apps/rc/installations/new"}}}
	withGitHubAppPool(t, pool)
	ctx := mintSessionCtx(t, deps, admin.UserID)
	service := NewUiService(deps)

	list, err := service.ListGithubApps(ctx, csilapi.ListGitHubAppsRequest{})
	requireOK(t, err)
	if len(list.Apps) != 1 || list.Apps[0].HeadroomPercent != 20 || !list.Apps[0].Recommended || list.Apps[0].InstallUrl == nil {
		t.Fatalf("apps = %#v", list.Apps)
	}
	if len(list.Installations) != 1 || list.Installations[0].HeadroomPercent != 20 || list.Installations[0].Repositories[0] != "acme/app" {
		t.Fatalf("installations = %#v", list.Installations)
	}

	toggled, err := service.SetGithubAppEnabled(ctx, csilapi.SetGitHubAppEnabledRequest{AppId: 42, Enabled: false})
	requireOK(t, err)
	if toggled.App.Enabled {
		t.Fatalf("app = %#v, want disabled", toggled.App)
	}

	synced, err := service.SyncGithubApp(ctx, csilapi.SyncGitHubAppRequest{AppId: 42})
	requireOK(t, err)
	if synced.Installations != 1 || synced.Created != 1 || synced.Disabled != 1 || len(pool.synced) != 1 {
		t.Fatalf("sync = %#v", synced)
	}
}

func TestGitHubAppOperationsWithoutPool(t *testing.T) {
	deps, st := newTestDeps(t)
	admin := st.putUser(models.User{UserID: "admin-1"})
	seedGlobalAdmin(st, admin.UserID)
	withGitHubAppPool(t, nil)
	_, err := NewUiService(deps).ListGithubApps(mintSessionCtx(t, deps, admin.UserID), csilapi.ListGitHubAppsRequest{})
	requireCode(t, err, "unavailable")
}
