package postgres_store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"gorm.io/gorm"
)

// --- project_webhook_secrets -------------------------------------------------

// GetProjectWebhookSecretByID retrieves a single rotatable webhook secret row
// by its ID, with no project/org scoping. Added for Task G (the CSIL UI
// service's DeactivateWebhookSecret/DeleteWebhookSecret ops, which identify
// their target by ID alone): callers must load the row first to discover its
// ProjectID before they can authorize the request against that project's
// owning org.
func (ps PostgresDbStore) GetProjectWebhookSecretByID(ctx context.Context, id string) (*models.ProjectWebhookSecret, error) {
	if !isValidUUID(id) {
		return nil, store.ErrNotFound
	}

	var secret models.ProjectWebhookSecret
	if err := ps.getDB(ctx).Where("id = ?", id).First(&secret).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, store.ErrNotFound
		}
		return nil, fmt.Errorf("failed to get project webhook secret: %w", err)
	}
	return &secret, nil
}

// CreateProjectWebhookSecret creates a new rotatable webhook secret row.
func (ps PostgresDbStore) CreateProjectWebhookSecret(ctx context.Context, secret *models.ProjectWebhookSecret) error {
	if err := ps.getDB(ctx).Create(secret).Error; err != nil {
		return fmt.Errorf("failed to create project webhook secret: %w", err)
	}
	return nil
}

// ListProjectWebhookSecrets lists webhook secrets for a project, optionally
// filtered to a single provider.
func (ps PostgresDbStore) ListProjectWebhookSecrets(ctx context.Context, projectID string, provider *string) ([]models.ProjectWebhookSecret, error) {
	query := ps.getDB(ctx).Where("project_id = ?", projectID)
	if provider != nil && *provider != "" {
		query = query.Where("provider = ?", *provider)
	}

	var secrets []models.ProjectWebhookSecret
	if err := query.Order("created_at ASC").Find(&secrets).Error; err != nil {
		return nil, fmt.Errorf("failed to list project webhook secrets: %w", err)
	}
	return secrets, nil
}

// ListActiveProjectWebhookSecrets lists the active webhook secrets for a
// project+provider. Verification tries every row returned here.
func (ps PostgresDbStore) ListActiveProjectWebhookSecrets(ctx context.Context, projectID, provider string) ([]models.ProjectWebhookSecret, error) {
	var secrets []models.ProjectWebhookSecret
	if err := ps.getDB(ctx).
		Where("project_id = ? AND provider = ? AND is_active", projectID, provider).
		Order("created_at ASC").
		Find(&secrets).Error; err != nil {
		return nil, fmt.Errorf("failed to list active project webhook secrets: %w", err)
	}
	return secrets, nil
}

// DeactivateProjectWebhookSecret marks a webhook secret inactive and stamps
// deactivated_at. Idempotent: deactivating a row that's already inactive
// succeeds as a no-op (the original deactivated_at is preserved) rather than
// reporting store.ErrNotFound — a double-click or client retry on an
// already-deactivated secret shouldn't surface as "not found" to operators.
// Only a row that doesn't exist at all returns store.ErrNotFound.
func (ps PostgresDbStore) DeactivateProjectWebhookSecret(ctx context.Context, id string) error {
	if !isValidUUID(id) {
		return store.ErrNotFound
	}

	now := time.Now().UTC()
	result := ps.getDB(ctx).Model(&models.ProjectWebhookSecret{}).
		Where("id = ? AND is_active", id).
		Updates(map[string]interface{}{
			"is_active":      false,
			"deactivated_at": now,
		})
	if result.Error != nil {
		return fmt.Errorf("failed to deactivate project webhook secret: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		// Either the row doesn't exist, or it exists but was already
		// inactive (the "WHERE ... AND is_active" guard excluded it from
		// the update in that case too). Distinguish the two by existence.
		var count int64
		if err := ps.getDB(ctx).Model(&models.ProjectWebhookSecret{}).
			Where("id = ?", id).
			Count(&count).Error; err != nil {
			return fmt.Errorf("failed to check project webhook secret existence: %w", err)
		}
		if count == 0 {
			return store.ErrNotFound
		}
		// Row exists and is already inactive: no-op success, original
		// deactivated_at is left untouched.
	}
	return nil
}

// DeleteProjectWebhookSecret deletes a webhook secret row by ID.
func (ps PostgresDbStore) DeleteProjectWebhookSecret(ctx context.Context, id string) error {
	if !isValidUUID(id) {
		return store.ErrNotFound
	}

	result := ps.getDB(ctx).Where("id = ?", id).Delete(&models.ProjectWebhookSecret{})
	if result.Error != nil {
		return fmt.Errorf("failed to delete project webhook secret: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return store.ErrNotFound
	}
	return nil
}

// TouchProjectWebhookSecretLastUsed stamps last_used_at=now() for a webhook
// secret by ID.
func (ps PostgresDbStore) TouchProjectWebhookSecretLastUsed(ctx context.Context, id string) error {
	result := ps.getDB(ctx).Model(&models.ProjectWebhookSecret{}).
		Where("id = ?", id).
		Update("last_used_at", time.Now().UTC())
	if result.Error != nil {
		return fmt.Errorf("failed to stamp project webhook secret last used: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return store.ErrNotFound
	}
	return nil
}

// --- project_vcs_credentials -------------------------------------------------

// GetProjectVCSCredentialByID retrieves a single rotatable VCS credential row
// by its ID, with no project/org scoping. See GetProjectWebhookSecretByID for
// the rationale (Task G).
func (ps PostgresDbStore) GetProjectVCSCredentialByID(ctx context.Context, id string) (*models.ProjectVCSCredential, error) {
	if !isValidUUID(id) {
		return nil, store.ErrNotFound
	}

	var cred models.ProjectVCSCredential
	if err := ps.getDB(ctx).Where("id = ?", id).First(&cred).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, store.ErrNotFound
		}
		return nil, fmt.Errorf("failed to get project vcs credential: %w", err)
	}
	return &cred, nil
}

// CreateProjectVCSCredential creates a new rotatable VCS credential row.
func (ps PostgresDbStore) CreateProjectVCSCredential(ctx context.Context, cred *models.ProjectVCSCredential) error {
	if err := ps.getDB(ctx).Create(cred).Error; err != nil {
		return fmt.Errorf("failed to create project vcs credential: %w", err)
	}
	return nil
}

// ListProjectVCSCredentials lists VCS credentials for a project, optionally
// filtered to a single provider.
func (ps PostgresDbStore) ListProjectVCSCredentials(ctx context.Context, projectID string, provider *string) ([]models.ProjectVCSCredential, error) {
	query := ps.getDB(ctx).Where("project_id = ?", projectID)
	if provider != nil && *provider != "" {
		query = query.Where("provider = ?", *provider)
	}

	var creds []models.ProjectVCSCredential
	if err := query.Order("created_at ASC").Find(&creds).Error; err != nil {
		return nil, fmt.Errorf("failed to list project vcs credentials: %w", err)
	}
	return creds, nil
}

// ListActiveProjectVCSCredentials lists the active VCS credentials for a
// project+provider, ordered oldest-first. Callers resolving a single
// credential to use should take the highest-precedence (most recently
// created) active row.
func (ps PostgresDbStore) ListActiveProjectVCSCredentials(ctx context.Context, projectID, provider string) ([]models.ProjectVCSCredential, error) {
	var creds []models.ProjectVCSCredential
	if err := ps.getDB(ctx).
		Where("project_id = ? AND provider = ? AND is_active", projectID, provider).
		Order("created_at ASC").
		Find(&creds).Error; err != nil {
		return nil, fmt.Errorf("failed to list active project vcs credentials: %w", err)
	}
	return creds, nil
}

// DeactivateProjectVCSCredential marks a VCS credential inactive and stamps
// deactivated_at. Idempotent: see DeactivateProjectWebhookSecret's doc
// comment — deactivating an already-inactive-but-existing row succeeds as a
// no-op instead of returning store.ErrNotFound.
func (ps PostgresDbStore) DeactivateProjectVCSCredential(ctx context.Context, id string) error {
	if !isValidUUID(id) {
		return store.ErrNotFound
	}

	now := time.Now().UTC()
	result := ps.getDB(ctx).Model(&models.ProjectVCSCredential{}).
		Where("id = ? AND is_active", id).
		Updates(map[string]interface{}{
			"is_active":      false,
			"deactivated_at": now,
		})
	if result.Error != nil {
		return fmt.Errorf("failed to deactivate project vcs credential: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		var count int64
		if err := ps.getDB(ctx).Model(&models.ProjectVCSCredential{}).
			Where("id = ?", id).
			Count(&count).Error; err != nil {
			return fmt.Errorf("failed to check project vcs credential existence: %w", err)
		}
		if count == 0 {
			return store.ErrNotFound
		}
		// Row exists and is already inactive: no-op success, original
		// deactivated_at is left untouched.
	}
	return nil
}

// DeleteProjectVCSCredential deletes a VCS credential row by ID.
func (ps PostgresDbStore) DeleteProjectVCSCredential(ctx context.Context, id string) error {
	if !isValidUUID(id) {
		return store.ErrNotFound
	}

	result := ps.getDB(ctx).Where("id = ?", id).Delete(&models.ProjectVCSCredential{})
	if result.Error != nil {
		return fmt.Errorf("failed to delete project vcs credential: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return store.ErrNotFound
	}
	return nil
}

// TouchProjectVCSCredentialLastUsed stamps last_used_at=now() for a VCS
// credential by ID.
func (ps PostgresDbStore) TouchProjectVCSCredentialLastUsed(ctx context.Context, id string) error {
	result := ps.getDB(ctx).Model(&models.ProjectVCSCredential{}).
		Where("id = ?", id).
		Update("last_used_at", time.Now().UTC())
	if result.Error != nil {
		return fmt.Errorf("failed to stamp project vcs credential last used: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return store.ErrNotFound
	}
	return nil
}
