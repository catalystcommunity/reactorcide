package postgres_store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/ctxkey"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"gorm.io/gorm"
)

// oidcKeyLockID is the advisory lock key of OIDC signing key changes.
const oidcKeyLockID int64 = 0x5243_4f49_4443 // "RCOIDC"

// ListOIDCSigningKeys returns every signing key record. The JWKS and the
// signer filter by time.
func (ps PostgresDbStore) ListOIDCSigningKeys(ctx context.Context) ([]models.OIDCSigningKey, error) {
	var keys []models.OIDCSigningKey
	if err := ps.getDB(ctx).Order("activates_at DESC").Find(&keys).Error; err != nil {
		return nil, fmt.Errorf("failed to list OIDC signing keys: %w", err)
	}
	return keys, nil
}

func (ps PostgresDbStore) CreateOIDCSigningKey(ctx context.Context, key *models.OIDCSigningKey) error {
	if err := ps.getDB(ctx).Create(key).Error; err != nil {
		return fmt.Errorf("failed to create OIDC signing key: %w", err)
	}
	return nil
}

func (ps PostgresDbStore) RetireOIDCSigningKeys(ctx context.Context, keep string, retiresAt time.Time) error {
	return ps.getDB(ctx).Model(&models.OIDCSigningKey{}).
		Where("kid <> ? AND (retires_at IS NULL OR retires_at > ?)", keep, retiresAt.UTC()).
		Update("retires_at", retiresAt.UTC()).Error
}

func (ps PostgresDbStore) SetOIDCSigningKeyRetirement(ctx context.Context, kid string, retiresAt time.Time) error {
	result := ps.getDB(ctx).Model(&models.OIDCSigningKey{}).Where("kid = ?", kid).Update("retires_at", retiresAt.UTC())
	if result.Error != nil {
		return fmt.Errorf("failed to retire OIDC signing key: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return store.ErrNotFound
	}
	return nil
}

func (ps PostgresDbStore) MasterKeyIDByName(ctx context.Context, name string) (string, error) {
	var key models.MasterKey
	if err := ps.getDB(ctx).Where("name = ?", name).First(&key).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return "", store.ErrNotFound
		}
		return "", err
	}
	return key.KeyID, nil
}

func (ps PostgresDbStore) MasterKeyNameByID(ctx context.Context, keyID string) (string, error) {
	var key models.MasterKey
	if err := ps.getDB(ctx).Where("key_id = ?", keyID).First(&key).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return "", store.ErrNotFound
		}
		return "", err
	}
	return key.Name, nil
}

// WithOIDCKeyLock runs fn in a transaction that holds the OIDC key advisory
// lock.
func (ps PostgresDbStore) WithOIDCKeyLock(ctx context.Context, fn func(ctx context.Context) error) error {
	return ps.getDB(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("SELECT pg_advisory_xact_lock(?)", oidcKeyLockID).Error; err != nil {
			return err
		}
		return fn(context.WithValue(ctx, ctxkey.TxKey(), tx))
	})
}

// GetOIDCSettings returns the setting of an organization (projectID empty)
// or of a project. It returns nil and no error when no row exists.
func (ps PostgresDbStore) GetOIDCSettings(ctx context.Context, orgID, projectID string) (*models.OIDCSettings, error) {
	if !isValidUUID(orgID) || (projectID != "" && !isValidUUID(projectID)) {
		return nil, nil
	}
	var row models.OIDCSettings
	query := ps.getDB(ctx).Where("org_id = ?", orgID)
	if projectID == "" {
		query = query.Where("project_id IS NULL")
	} else {
		query = query.Where("project_id = ?", projectID)
	}
	if err := query.First(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to get OIDC settings: %w", err)
	}
	if row.AudienceClaims == nil {
		row.AudienceClaims = map[string]map[string]string{}
	}
	return &row, nil
}

// SaveOIDCSettings creates or replaces the setting of its scope.
func (ps PostgresDbStore) SaveOIDCSettings(ctx context.Context, settings *models.OIDCSettings) error {
	if settings.AudienceClaims == nil {
		settings.AudienceClaims = map[string]map[string]string{}
	}
	projectID := ""
	if settings.ProjectID != nil {
		projectID = *settings.ProjectID
	}
	existing, err := ps.GetOIDCSettings(ctx, settings.OrgID, projectID)
	if err != nil {
		return err
	}
	settings.UpdatedAt = time.Now().UTC()
	if existing == nil {
		return ps.getDB(ctx).Create(settings).Error
	}
	settings.SettingID = existing.SettingID
	return ps.getDB(ctx).Model(&models.OIDCSettings{}).Where("setting_id = ?", existing.SettingID).
		Select("issuer", "audience_claims", "updated_at").Updates(settings).Error
}
