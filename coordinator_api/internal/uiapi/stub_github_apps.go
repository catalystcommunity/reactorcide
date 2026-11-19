package uiapi

import (
	"context"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/uiapi/csilapi"
)

// WP11 stubs (ui_github_apps.go has the real operations).

func (StubUi) ListGithubApps(ctx context.Context, req csilapi.ListGitHubAppsRequest) (csilapi.ListGitHubAppsResponse, error) {
	return csilapi.ListGitHubAppsResponse{}, ErrUnimplemented("ReactorcideUi/list-github-apps")
}

func (StubUi) SetGithubAppEnabled(ctx context.Context, req csilapi.SetGitHubAppEnabledRequest) (csilapi.SetGitHubAppEnabledResponse, error) {
	return csilapi.SetGitHubAppEnabledResponse{}, ErrUnimplemented("ReactorcideUi/set-github-app-enabled")
}

func (StubUi) SyncGithubApp(ctx context.Context, req csilapi.SyncGitHubAppRequest) (csilapi.SyncGitHubAppResponse, error) {
	return csilapi.SyncGitHubAppResponse{}, ErrUnimplemented("ReactorcideUi/sync-github-app")
}
