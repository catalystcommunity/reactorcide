package uiapi

import (
	"context"
	"errors"
	"math"
	"strconv"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/uiapi/csilapi"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/vcs"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/vcs/githubapp"
)

// GitHub App pool operations (WP11, docs/github-app.md). Global admin only.
// The REST API and the CLI register Apps (the key upload); the UI lists them,
// enables or disables them, and starts a sync.

// githubAppPool is the part of the App pool the UI uses. The coordinator
// installs the pool with vcs.SetAppInstallationSource.
type githubAppPool interface {
	Summaries(ctx context.Context) ([]githubapp.AppSummary, error)
	SetAppEnabled(ctx context.Context, appID int64, enabled bool) error
	SyncApp(ctx context.Context, appID int64) (githubapp.SyncResult, error)
	Store() store.GitHubAppStore
}

// githubAppPoolSource returns the pool; tests replace it.
var githubAppPoolSource = func() githubAppPool {
	pool, _ := vcs.AppSource().(githubAppPool)
	return pool
}

func (s *UiService) githubApps(ctx context.Context, read ...bool) (githubAppPool, error) {
	check := s.requireGlobalAdmin
	if len(read) > 0 && read[0] {
		check = s.requireGlobalAdminRead
	}
	if _, err := check(ctx); err != nil {
		return nil, err
	}
	pool := githubAppPoolSource()
	if pool == nil {
		return nil, NewServiceError("unavailable", "GitHub Apps need the master keys and a database store")
	}
	return pool, nil
}

func headroomPercent(value float64) int64 {
	return int64(math.Round(value * 100))
}

func rateLimitFields(snapshot models.RateLimitSnapshot) (limit, remaining *int64, reset *string) {
	if snapshot.Limit != nil {
		v := int64(*snapshot.Limit)
		limit = &v
	}
	if snapshot.Remaining != nil {
		v := int64(*snapshot.Remaining)
		remaining = &v
	}
	if snapshot.ResetAt != nil {
		v := snapshot.ResetAt.UTC().Format(time.RFC3339)
		reset = &v
	}
	return limit, remaining, reset
}

func githubAppSummaryView(summary githubapp.AppSummary) csilapi.GitHubAppSummary {
	app := summary.App
	view := csilapi.GitHubAppSummary{AppId: app.AppID, Slug: app.Slug, Name: app.Name, Enabled: app.Enabled,
		OpenRegistration: app.OpenRegistration,
		HeadroomPercent:  headroomPercent(summary.Headroom.Headroom), ActiveInstallations: int64(summary.Headroom.ActiveInstallations),
		Repositories: int64(summary.Headroom.Repositories), Recommended: summary.Recommended,
		CreatedAt: app.CreatedAt.UTC().Format(time.RFC3339)}
	if summary.InstallURL != "" {
		url := summary.InstallURL
		view.InstallUrl = &url
	}
	view.RatelimitLimit, view.RatelimitRemaining, view.RatelimitResetAt = rateLimitFields(app.RateLimit)
	return view
}

// ListGithubApps lists every App with its headroom, and every installation
// with its bound repositories.
func (s *UiService) ListGithubApps(ctx context.Context, _ csilapi.ListGitHubAppsRequest) (csilapi.ListGitHubAppsResponse, error) {
	pool, err := s.githubApps(ctx, true)
	if err != nil {
		return csilapi.ListGitHubAppsResponse{}, err
	}
	summaries, err := pool.Summaries(ctx)
	if err != nil {
		return csilapi.ListGitHubAppsResponse{}, mapStoreErr(err, "the GitHub Apps could not be read")
	}
	resp := csilapi.ListGitHubAppsResponse{Apps: []csilapi.GitHubAppSummary{}, Installations: []csilapi.GitHubAppInstallationSummary{}}
	for _, summary := range summaries {
		resp.Apps = append(resp.Apps, githubAppSummaryView(summary))
	}
	installations, err := pool.Store().ListGitHubAppInstallations(ctx, 0)
	if err != nil {
		return csilapi.ListGitHubAppsResponse{}, mapStoreErr(err, "the GitHub App installations could not be read")
	}
	now := time.Now()
	for _, installation := range installations {
		repos, err := pool.Store().ListGitHubAppRepositories(ctx, installation.InstallationID)
		if err != nil {
			return csilapi.ListGitHubAppsResponse{}, mapStoreErr(err, "the GitHub App repositories could not be read")
		}
		names := make([]string, 0, len(repos))
		pending := []string{}
		for _, repo := range repos {
			names = append(names, repo.RepoFullName)
			if repo.PendingApproval {
				pending = append(pending, repo.RepoFullName)
			}
		}
		view := csilapi.GitHubAppInstallationSummary{InstallationId: installation.InstallationID, AppId: installation.AppID,
			AccountLogin: installation.AccountLogin, AccountType: installation.AccountType,
			RepositorySelection: installation.RepositorySelection, Suspended: installation.Suspended,
			Deleted: installation.DeletedAt != nil, HeadroomPercent: headroomPercent(installation.RateLimit.Headroom(now)),
			LastSeenAt: installation.LastSeenAt.UTC().Format(time.RFC3339), Repositories: names,
			PendingRepositories: pending}
		view.RatelimitLimit, view.RatelimitRemaining, view.RatelimitResetAt = rateLimitFields(installation.RateLimit)
		resp.Installations = append(resp.Installations, view)
	}
	return resp, nil
}

// SetGithubAppEnabled enables or disables an App. A disabled App gives no
// tokens; its projects use their token path and commit statuses again.
func (s *UiService) SetGithubAppEnabled(ctx context.Context, req csilapi.SetGitHubAppEnabledRequest) (csilapi.SetGitHubAppEnabledResponse, error) {
	pool, err := s.githubApps(ctx)
	if err != nil {
		return csilapi.SetGitHubAppEnabledResponse{}, err
	}
	if err := pool.SetAppEnabled(ctx, req.AppId, req.Enabled); err != nil {
		return csilapi.SetGitHubAppEnabledResponse{}, mapStoreErr(err, "GitHub App not found")
	}
	action := "github_app.disabled"
	if req.Enabled {
		action = "github_app.enabled"
	}
	s.recordAudit(ctx, "", action, "github_app", formatAppID(req.AppId), models.JSONB{"app_id": req.AppId})
	summaries, err := pool.Summaries(ctx)
	if err != nil {
		return csilapi.SetGitHubAppEnabledResponse{}, mapStoreErr(err, "the GitHub Apps could not be read")
	}
	for _, summary := range summaries {
		if summary.App.AppID == req.AppId {
			return csilapi.SetGitHubAppEnabledResponse{App: githubAppSummaryView(summary)}, nil
		}
	}
	return csilapi.SetGitHubAppEnabledResponse{}, NewServiceError("not_found", "GitHub App not found")
}

// SyncGithubApp reads the installations and repositories of an App from
// GitHub and registers projects.
func (s *UiService) SyncGithubApp(ctx context.Context, req csilapi.SyncGitHubAppRequest) (csilapi.SyncGitHubAppResponse, error) {
	pool, err := s.githubApps(ctx)
	if err != nil {
		return csilapi.SyncGitHubAppResponse{}, err
	}
	result, err := pool.SyncApp(ctx, req.AppId)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return csilapi.SyncGitHubAppResponse{}, NewServiceError("not_found", "GitHub App not found")
		}
		return csilapi.SyncGitHubAppResponse{}, NewServiceError("failed_precondition", err.Error())
	}
	resp := csilapi.SyncGitHubAppResponse{Installations: int64(result.Installations), Errors: append([]string{}, result.Errors...)}
	for _, repo := range result.Repositories {
		switch repo.Action {
		case "created":
			resp.Created++
		case "disabled":
			resp.Disabled++
		}
	}
	return resp, nil
}

func formatAppID(id int64) string {
	return strconvFormatInt(id)
}

func strconvFormatInt(id int64) string { return strconv.FormatInt(id, 10) }
