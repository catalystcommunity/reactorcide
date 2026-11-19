package store

import (
	"context"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

// GitHubAppStore holds the GitHub App pool (WP11, docs/github-app.md). Like
// the other feature stores it is separate from Store; callers type-assert.
type GitHubAppStore interface {
	// CreateGitHubApp inserts an App. ErrAlreadyExists when the App ID is
	// registered.
	CreateGitHubApp(ctx context.Context, app *models.GitHubApp) error
	GetGitHubApp(ctx context.Context, appID int64) (*models.GitHubApp, error)
	ListGitHubApps(ctx context.Context) ([]models.GitHubApp, error)
	// UpdateGitHubApp writes the mutable fields: slug, name, client ID, the
	// encrypted secrets, the default organization, and enabled.
	UpdateGitHubApp(ctx context.Context, app *models.GitHubApp) error
	SetGitHubAppEnabled(ctx context.Context, appID int64, enabled bool) error

	// UpsertGitHubAppInstallation inserts or updates an installation and
	// sets last_seen_at. A deleted installation becomes active again only
	// when deleted_at is nil in the input.
	UpsertGitHubAppInstallation(ctx context.Context, installation *models.GitHubAppInstallation) error
	GetGitHubAppInstallation(ctx context.Context, installationID int64) (*models.GitHubAppInstallation, error)
	// ListGitHubAppInstallations lists installations with their active
	// repository counts. appID 0 lists all.
	ListGitHubAppInstallations(ctx context.Context, appID int64) ([]models.GitHubAppInstallation, error)
	SetGitHubAppInstallationSuspended(ctx context.Context, installationID int64, suspended bool) error
	// MarkGitHubAppInstallationDeleted sets deleted_at and removes every
	// binding of the installation. It returns the removed bindings.
	MarkGitHubAppInstallationDeleted(ctx context.Context, installationID int64, at time.Time) ([]models.GitHubAppRepository, error)

	// BindGitHubAppRepository records that the installation holds the
	// repository. When an active binding to another active installation
	// exists, that binding stays and bound is false. A removed or inactive
	// binding moves to this installation.
	BindGitHubAppRepository(ctx context.Context, repo *models.GitHubAppRepository) (result models.GitHubAppRepository, bound bool, err error)
	// SetGitHubAppRepositoryProject records the project of a binding.
	SetGitHubAppRepositoryProject(ctx context.Context, repoFullName, projectID string, autoRegistered bool) error
	// SetGitHubAppRepositoryPending marks the project of a binding as
	// created disabled, waiting for approval.
	SetGitHubAppRepositoryPending(ctx context.Context, repoFullName string, pending bool) error
	// RemoveGitHubAppRepository marks the binding of the installation as
	// removed. found is false when the installation does not hold it.
	RemoveGitHubAppRepository(ctx context.Context, installationID int64, repoFullName string, disabledProject bool, at time.Time) (models.GitHubAppRepository, bool, error)
	// ListGitHubAppRepositories lists the active bindings of an installation.
	ListGitHubAppRepositories(ctx context.Context, installationID int64) ([]models.GitHubAppRepository, error)
	// ResolveGitHubAppBinding returns the active binding of a repository: an
	// enabled App, an active installation, and a binding not removed. nil
	// when there is none.
	ResolveGitHubAppBinding(ctx context.Context, repoFullName string) (*models.GitHubAppBinding, error)
	// ListActiveGitHubAppInstallationsForAccount lists the active
	// installations of enabled Apps on one account (for team reads).
	ListActiveGitHubAppInstallationsForAccount(ctx context.Context, accountLogin string) ([]models.GitHubAppInstallation, error)

	// RecordGitHubAppRateLimit stores the snapshot of App calls.
	RecordGitHubAppRateLimit(ctx context.Context, appID int64, snapshot models.RateLimitSnapshot) error
	// RecordGitHubInstallationRateLimit stores the snapshot of
	// installation-token calls.
	RecordGitHubInstallationRateLimit(ctx context.Context, installationID int64, snapshot models.RateLimitSnapshot) error

	// LatestPRNumberForCommit returns the pull request of the newest CI event
	// of the project for the commit (a check run of a fork pull request has
	// no pull_requests list). ok is false when no such event exists.
	LatestPRNumberForCommit(ctx context.Context, projectID, commitSHA string) (number int, ok bool, err error)

	// Account mapping of self-service registration.
	SetGitHubAccountOrg(ctx context.Context, accountLogin, orgID string) error
	DeleteGitHubAccountOrg(ctx context.Context, accountLogin string) (bool, error)
	GetGitHubAccountOrg(ctx context.Context, accountLogin string) (*models.GitHubAccountOrg, error)
	ListGitHubAccountOrgs(ctx context.Context) ([]models.GitHubAccountOrg, error)
}
