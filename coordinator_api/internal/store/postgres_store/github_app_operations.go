package postgres_store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var _ store.GitHubAppStore = PostgresDbStore{}

func (ps PostgresDbStore) CreateGitHubApp(ctx context.Context, app *models.GitHubApp) error {
	if app == nil || app.AppID <= 0 {
		return store.ErrInvalidInput
	}
	// ON CONFLICT DO NOTHING: a duplicate does not abort the transaction of
	// the request.
	result := ps.getDB(ctx).Clauses(clause.OnConflict{DoNothing: true}).Omit("created_at", "updated_at").Create(app)
	if result.Error != nil {
		return fmt.Errorf("failed to create GitHub App: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("GitHub App %d is already registered: %w", app.AppID, store.ErrAlreadyExists)
	}
	return nil
}

func (ps PostgresDbStore) GetGitHubApp(ctx context.Context, appID int64) (*models.GitHubApp, error) {
	var app models.GitHubApp
	err := ps.getDB(ctx).Where("app_id = ?", appID).First(&app).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, store.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("failed to read GitHub App: %w", err)
	}
	return &app, nil
}

func (ps PostgresDbStore) ListGitHubApps(ctx context.Context) ([]models.GitHubApp, error) {
	var apps []models.GitHubApp
	if err := ps.getDB(ctx).Order("app_id").Find(&apps).Error; err != nil {
		return nil, fmt.Errorf("failed to list GitHub Apps: %w", err)
	}
	return apps, nil
}

func (ps PostgresDbStore) UpdateGitHubApp(ctx context.Context, app *models.GitHubApp) error {
	if app == nil {
		return store.ErrInvalidInput
	}
	result := ps.getDB(ctx).Model(&models.GitHubApp{}).Where("app_id = ?", app.AppID).Updates(map[string]any{
		"slug": app.Slug, "name": app.Name, "client_id": app.ClientID,
		"private_key_key_name": app.PrivateKeyKeyName, "private_key_ciphertext": app.PrivateKeyCiphertext,
		"webhook_secret_key_name": app.WebhookSecretKeyName, "webhook_secret_ciphertext": app.WebhookSecretCiphertext,
		"default_org_id": app.DefaultOrgID, "open_registration": app.OpenRegistration, "enabled": app.Enabled,
		"updated_at": gorm.Expr("timezone('utc', now())"),
	})
	if result.Error != nil {
		return fmt.Errorf("failed to update GitHub App: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return store.ErrNotFound
	}
	return nil
}

func (ps PostgresDbStore) SetGitHubAppEnabled(ctx context.Context, appID int64, enabled bool) error {
	result := ps.getDB(ctx).Model(&models.GitHubApp{}).Where("app_id = ?", appID).Updates(map[string]any{
		"enabled": enabled, "updated_at": gorm.Expr("timezone('utc', now())"),
	})
	if result.Error != nil {
		return fmt.Errorf("failed to update GitHub App: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return store.ErrNotFound
	}
	return nil
}

func (ps PostgresDbStore) UpsertGitHubAppInstallation(ctx context.Context, installation *models.GitHubAppInstallation) error {
	if installation == nil || installation.InstallationID <= 0 || installation.AppID <= 0 {
		return store.ErrInvalidInput
	}
	if installation.Permissions == nil {
		installation.Permissions = map[string]string{}
	}
	err := ps.getDB(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "installation_id"}},
		DoUpdates: clause.Assignments(map[string]any{
			"app_id":               gorm.Expr("EXCLUDED.app_id"),
			"account_login":        gorm.Expr("EXCLUDED.account_login"),
			"account_type":         gorm.Expr("EXCLUDED.account_type"),
			"repository_selection": gorm.Expr("EXCLUDED.repository_selection"),
			"permissions":          gorm.Expr("EXCLUDED.permissions"),
			"suspended":            gorm.Expr("EXCLUDED.suspended"),
			"deleted_at":           gorm.Expr("EXCLUDED.deleted_at"),
			"last_seen_at":         gorm.Expr("timezone('utc', now())"),
		}),
	}).Omit("created_at", "last_seen_at", "repository_count").Create(installation).Error
	if err != nil {
		return fmt.Errorf("failed to save GitHub App installation: %w", err)
	}
	return nil
}

func (ps PostgresDbStore) GetGitHubAppInstallation(ctx context.Context, installationID int64) (*models.GitHubAppInstallation, error) {
	var installation models.GitHubAppInstallation
	err := ps.getDB(ctx).Where("installation_id = ?", installationID).First(&installation).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, store.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("failed to read GitHub App installation: %w", err)
	}
	return &installation, nil
}

func (ps PostgresDbStore) ListGitHubAppInstallations(ctx context.Context, appID int64) ([]models.GitHubAppInstallation, error) {
	query := ps.getDB(ctx).Table("github_app_installations i").
		Select(`i.*, (SELECT count(*) FROM github_app_installation_repositories r
WHERE r.installation_id = i.installation_id AND r.removed_at IS NULL) AS repository_count`)
	if appID > 0 {
		query = query.Where("i.app_id = ?", appID)
	}
	var installations []models.GitHubAppInstallation
	if err := query.Order("i.app_id, i.installation_id").Find(&installations).Error; err != nil {
		return nil, fmt.Errorf("failed to list GitHub App installations: %w", err)
	}
	return installations, nil
}

func (ps PostgresDbStore) SetGitHubAppInstallationSuspended(ctx context.Context, installationID int64, suspended bool) error {
	result := ps.getDB(ctx).Model(&models.GitHubAppInstallation{}).Where("installation_id = ?", installationID).
		Updates(map[string]any{"suspended": suspended, "last_seen_at": gorm.Expr("timezone('utc', now())")})
	if result.Error != nil {
		return fmt.Errorf("failed to update GitHub App installation: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return store.ErrNotFound
	}
	return nil
}

func (ps PostgresDbStore) MarkGitHubAppInstallationDeleted(ctx context.Context, installationID int64, at time.Time) ([]models.GitHubAppRepository, error) {
	var removed []models.GitHubAppRepository
	err := ps.getDB(ctx).Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&models.GitHubAppInstallation{}).Where("installation_id = ?", installationID).
			Updates(map[string]any{"deleted_at": at, "last_seen_at": gorm.Expr("timezone('utc', now())")})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return store.ErrNotFound
		}
		return tx.Raw(`UPDATE github_app_installation_repositories SET removed_at = ?, updated_at = timezone('utc', now())
WHERE installation_id = ? AND removed_at IS NULL RETURNING *`, at, installationID).Scan(&removed).Error
	})
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, err
		}
		return nil, fmt.Errorf("failed to delete GitHub App installation: %w", err)
	}
	return removed, nil
}

// bindingActive reports whether a binding still gives tokens: not removed,
// its installation is active, and its App is enabled.
func bindingActive(tx *gorm.DB, row models.GitHubAppRepository) (bool, error) {
	if row.RemovedAt != nil {
		return false, nil
	}
	var active bool
	err := tx.Raw(`SELECT EXISTS (SELECT 1 FROM github_app_installations i JOIN github_apps a ON a.app_id = i.app_id
WHERE i.installation_id = ? AND NOT i.suspended AND i.deleted_at IS NULL AND a.enabled)`, row.InstallationID).Scan(&active).Error
	return active, err
}

func (ps PostgresDbStore) BindGitHubAppRepository(ctx context.Context, repo *models.GitHubAppRepository) (models.GitHubAppRepository, bool, error) {
	if repo == nil || repo.InstallationID <= 0 {
		return models.GitHubAppRepository{}, false, store.ErrInvalidInput
	}
	repo.RepoFullName = models.NormalizeGitHubRepoFullName(repo.RepoFullName)
	if repo.RepoFullName == "" || !strings.Contains(repo.RepoFullName, "/") {
		return models.GitHubAppRepository{}, false, store.ErrInvalidInput
	}
	var result models.GitHubAppRepository
	bound := false
	err := ps.getDB(ctx).Transaction(func(tx *gorm.DB) error {
		var existing models.GitHubAppRepository
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("repo_full_name = ?", repo.RepoFullName).First(&existing).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			if err := tx.Omit("added_at", "updated_at").Create(repo).Error; err != nil {
				return err
			}
			bound = true
			return tx.Where("repo_full_name = ?", repo.RepoFullName).First(&result).Error
		}
		if err != nil {
			return err
		}
		if existing.InstallationID != repo.InstallationID {
			active, err := bindingActive(tx, existing)
			if err != nil {
				return err
			}
			if active {
				result = existing
				return nil
			}
		}
		updates := map[string]any{
			"installation_id": repo.InstallationID, "private": repo.Private, "removed_at": nil,
			"updated_at": gorm.Expr("timezone('utc', now())"),
		}
		if repo.RepositoryID != nil {
			updates["repository_id"] = *repo.RepositoryID
		}
		if err := tx.Model(&models.GitHubAppRepository{}).Where("repo_full_name = ?", repo.RepoFullName).Updates(updates).Error; err != nil {
			return err
		}
		bound = true
		return tx.Where("repo_full_name = ?", repo.RepoFullName).First(&result).Error
	})
	if err != nil {
		return models.GitHubAppRepository{}, false, fmt.Errorf("failed to bind GitHub repository: %w", err)
	}
	return result, bound, nil
}

func (ps PostgresDbStore) SetGitHubAppRepositoryProject(ctx context.Context, repoFullName, projectID string, autoRegistered bool) error {
	if !isValidUUID(projectID) {
		return store.ErrInvalidInput
	}
	result := ps.getDB(ctx).Model(&models.GitHubAppRepository{}).
		Where("repo_full_name = ?", models.NormalizeGitHubRepoFullName(repoFullName)).
		Updates(map[string]any{"project_id": projectID, "auto_registered": autoRegistered, "disabled_by_removal": false,
			"updated_at": gorm.Expr("timezone('utc', now())")})
	if result.Error != nil {
		return fmt.Errorf("failed to update GitHub repository binding: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return store.ErrNotFound
	}
	return nil
}

func (ps PostgresDbStore) SetGitHubAppRepositoryPending(ctx context.Context, repoFullName string, pending bool) error {
	result := ps.getDB(ctx).Model(&models.GitHubAppRepository{}).
		Where("repo_full_name = ?", models.NormalizeGitHubRepoFullName(repoFullName)).
		Updates(map[string]any{"pending_approval": pending, "updated_at": gorm.Expr("timezone('utc', now())")})
	if result.Error != nil {
		return fmt.Errorf("failed to update GitHub repository binding: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return store.ErrNotFound
	}
	return nil
}

func (ps PostgresDbStore) RemoveGitHubAppRepository(ctx context.Context, installationID int64, repoFullName string, disabledProject bool, at time.Time) (models.GitHubAppRepository, bool, error) {
	var rows []models.GitHubAppRepository
	err := ps.getDB(ctx).Raw(`UPDATE github_app_installation_repositories
SET removed_at = ?, disabled_by_removal = ?, updated_at = timezone('utc', now())
WHERE repo_full_name = ? AND installation_id = ? RETURNING *`,
		at, disabledProject, models.NormalizeGitHubRepoFullName(repoFullName), installationID).Scan(&rows).Error
	if err != nil {
		return models.GitHubAppRepository{}, false, fmt.Errorf("failed to remove GitHub repository binding: %w", err)
	}
	if len(rows) == 0 {
		return models.GitHubAppRepository{}, false, nil
	}
	return rows[0], true, nil
}

func (ps PostgresDbStore) ListGitHubAppRepositories(ctx context.Context, installationID int64) ([]models.GitHubAppRepository, error) {
	var rows []models.GitHubAppRepository
	if err := ps.getDB(ctx).Where("installation_id = ? AND removed_at IS NULL", installationID).
		Order("repo_full_name").Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("failed to list GitHub repository bindings: %w", err)
	}
	// A pending project stops being pending when an administrator enables
	// it (any path: projects update, the UI). Report the current state.
	pending := []string{}
	for _, row := range rows {
		if row.PendingApproval && row.ProjectID != nil {
			pending = append(pending, *row.ProjectID)
		}
	}
	if len(pending) > 0 {
		var enabled []string
		if err := ps.getDB(ctx).Model(&models.Project{}).Where("project_id IN ? AND enabled", pending).
			Pluck("project_id", &enabled).Error; err != nil {
			return nil, fmt.Errorf("failed to read the projects of GitHub repository bindings: %w", err)
		}
		on := map[string]bool{}
		for _, id := range enabled {
			on[id] = true
		}
		for i := range rows {
			if rows[i].PendingApproval && (rows[i].ProjectID == nil || on[*rows[i].ProjectID]) {
				rows[i].PendingApproval = false
			}
		}
	}
	return rows, nil
}

func (ps PostgresDbStore) ResolveGitHubAppBinding(ctx context.Context, repoFullName string) (*models.GitHubAppBinding, error) {
	name := models.NormalizeGitHubRepoFullName(repoFullName)
	if name == "" {
		return nil, nil
	}
	db := ps.getDB(ctx)
	var repo models.GitHubAppRepository
	err := db.Where("repo_full_name = ? AND removed_at IS NULL", name).First(&repo).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to read GitHub repository binding: %w", err)
	}
	var installation models.GitHubAppInstallation
	err = db.Where("installation_id = ? AND NOT suspended AND deleted_at IS NULL", repo.InstallationID).First(&installation).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to read GitHub App installation: %w", err)
	}
	var app models.GitHubApp
	err = db.Where("app_id = ? AND enabled", installation.AppID).First(&app).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to read GitHub App: %w", err)
	}
	return &models.GitHubAppBinding{App: app, Installation: installation, Repository: repo}, nil
}

func (ps PostgresDbStore) ListActiveGitHubAppInstallationsForAccount(ctx context.Context, accountLogin string) ([]models.GitHubAppInstallation, error) {
	var installations []models.GitHubAppInstallation
	err := ps.getDB(ctx).Table("github_app_installations i").Select("i.*").
		Joins("JOIN github_apps a ON a.app_id = i.app_id").
		Where("lower(i.account_login) = lower(?) AND NOT i.suspended AND i.deleted_at IS NULL AND a.enabled", accountLogin).
		Order("i.installation_id").Find(&installations).Error
	if err != nil {
		return nil, fmt.Errorf("failed to list GitHub App installations: %w", err)
	}
	return installations, nil
}

func rateLimitUpdates(snapshot models.RateLimitSnapshot) map[string]any {
	return map[string]any{
		"ratelimit_limit": snapshot.Limit, "ratelimit_remaining": snapshot.Remaining, "ratelimit_used": snapshot.Used,
		"ratelimit_reset_at": snapshot.ResetAt, "ratelimit_observed_at": snapshot.ObservedAt,
	}
}

func (ps PostgresDbStore) RecordGitHubAppRateLimit(ctx context.Context, appID int64, snapshot models.RateLimitSnapshot) error {
	if err := ps.getDB(ctx).Model(&models.GitHubApp{}).Where("app_id = ?", appID).
		Updates(rateLimitUpdates(snapshot)).Error; err != nil {
		return fmt.Errorf("failed to record GitHub App rate limit: %w", err)
	}
	return nil
}

func (ps PostgresDbStore) RecordGitHubInstallationRateLimit(ctx context.Context, installationID int64, snapshot models.RateLimitSnapshot) error {
	updates := rateLimitUpdates(snapshot)
	updates["last_seen_at"] = gorm.Expr("timezone('utc', now())")
	if err := ps.getDB(ctx).Model(&models.GitHubAppInstallation{}).Where("installation_id = ?", installationID).
		Updates(updates).Error; err != nil {
		return fmt.Errorf("failed to record GitHub installation rate limit: %w", err)
	}
	return nil
}

func (ps PostgresDbStore) SetGitHubAccountOrg(ctx context.Context, accountLogin, orgID string) error {
	login := strings.ToLower(strings.TrimSpace(accountLogin))
	if login == "" || !isValidUUID(orgID) {
		return store.ErrInvalidInput
	}
	err := ps.getDB(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "account_login"}},
		DoUpdates: clause.AssignmentColumns([]string{"org_id"}),
	}).Omit("created_at").Create(&models.GitHubAccountOrg{AccountLogin: login, OrgID: orgID}).Error
	if err != nil {
		return fmt.Errorf("failed to save GitHub account mapping: %w", err)
	}
	return nil
}

func (ps PostgresDbStore) DeleteGitHubAccountOrg(ctx context.Context, accountLogin string) (bool, error) {
	result := ps.getDB(ctx).Where("account_login = ?", strings.ToLower(strings.TrimSpace(accountLogin))).
		Delete(&models.GitHubAccountOrg{})
	if result.Error != nil {
		return false, fmt.Errorf("failed to delete GitHub account mapping: %w", result.Error)
	}
	return result.RowsAffected > 0, nil
}

func (ps PostgresDbStore) GetGitHubAccountOrg(ctx context.Context, accountLogin string) (*models.GitHubAccountOrg, error) {
	var row models.GitHubAccountOrg
	err := ps.getDB(ctx).Where("account_login = ?", strings.ToLower(strings.TrimSpace(accountLogin))).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, store.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("failed to read GitHub account mapping: %w", err)
	}
	return &row, nil
}

func (ps PostgresDbStore) ListGitHubAccountOrgs(ctx context.Context) ([]models.GitHubAccountOrg, error) {
	var rows []models.GitHubAccountOrg
	if err := ps.getDB(ctx).Order("account_login").Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("failed to list GitHub account mappings: %w", err)
	}
	return rows, nil
}

func (ps PostgresDbStore) LatestPRNumberForCommit(ctx context.Context, projectID, commitSHA string) (int, bool, error) {
	if !isValidUUID(projectID) || commitSHA == "" {
		return 0, false, nil
	}
	var numbers []int
	err := ps.getDB(ctx).Raw(`SELECT pr_number FROM ci_events WHERE project_id = ? AND commit_sha = ? AND pr_number IS NOT NULL
ORDER BY received_at DESC LIMIT 1`, projectID, commitSHA).Scan(&numbers).Error
	if err != nil {
		return 0, false, fmt.Errorf("failed to find the pull request of a commit: %w", err)
	}
	if len(numbers) == 0 {
		return 0, false, nil
	}
	return numbers[0], true, nil
}
