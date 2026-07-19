package postgres_store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// --- ui_sessions ---------------------------------------------------------

// CreateUISession creates a new UI session.
func (ps PostgresDbStore) CreateUISession(ctx context.Context, session *models.UISession) error {
	if err := ps.getDB(ctx).Create(session).Error; err != nil {
		return fmt.Errorf("failed to create ui session: %w", err)
	}
	return nil
}

// GetActiveUISessionByTokenHash retrieves a non-revoked, non-expired session
// by its token hash. Returns store.ErrNotFound if no such session exists
// (including if it exists but is revoked or expired).
func (ps PostgresDbStore) GetActiveUISessionByTokenHash(ctx context.Context, tokenHash []byte) (*models.UISession, error) {
	var session models.UISession
	err := ps.getDB(ctx).
		Where("token_hash = ? AND revoked_at IS NULL AND expires_at > ?", tokenHash, time.Now().UTC()).
		First(&session).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, store.ErrNotFound
		}
		return nil, fmt.Errorf("failed to get ui session: %w", err)
	}
	return &session, nil
}

// TouchUISessionLastSeen refreshes a session's last_seen_at to now.
func (ps PostgresDbStore) TouchUISessionLastSeen(ctx context.Context, sessionID string) error {
	result := ps.getDB(ctx).Model(&models.UISession{}).
		Where("session_id = ?", sessionID).
		Update("last_seen_at", time.Now().UTC())
	if result.Error != nil {
		return fmt.Errorf("failed to touch ui session: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return store.ErrNotFound
	}
	return nil
}

// RevokeUISession marks a session as revoked.
func (ps PostgresDbStore) RevokeUISession(ctx context.Context, sessionID string) error {
	result := ps.getDB(ctx).Model(&models.UISession{}).
		Where("session_id = ? AND revoked_at IS NULL", sessionID).
		Update("revoked_at", time.Now().UTC())
	if result.Error != nil {
		return fmt.Errorf("failed to revoke ui session: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return store.ErrNotFound
	}
	return nil
}

// DeleteExpiredUISessions deletes sessions past their expiry and returns the
// number of rows removed.
func (ps PostgresDbStore) DeleteExpiredUISessions(ctx context.Context) (int64, error) {
	result := ps.getDB(ctx).Where("expires_at <= ?", time.Now().UTC()).Delete(&models.UISession{})
	if result.Error != nil {
		return 0, fmt.Errorf("failed to delete expired ui sessions: %w", result.Error)
	}
	return result.RowsAffected, nil
}

// --- auth_identities -------------------------------------------------------

// GetAuthIdentityBySubject retrieves an identity by its LinkKeys subject.
func (ps PostgresDbStore) GetAuthIdentityBySubject(ctx context.Context, subject string) (*models.AuthIdentity, error) {
	var identity models.AuthIdentity
	if err := ps.getDB(ctx).Where("subject = ?", subject).First(&identity).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, store.ErrNotFound
		}
		return nil, fmt.Errorf("failed to get auth identity: %w", err)
	}
	return &identity, nil
}

// GetAuthIdentityByUserID retrieves the auth identity linked to a user.
// Added for Task G (the CSIL UI service's Authenticate/CompleteLogin ops,
// which need to render subject/handle/domain for the already-resolved user
// their session belongs to; auth_identities.user_id is unique, per the
// migration 000017 schema).
func (ps PostgresDbStore) GetAuthIdentityByUserID(ctx context.Context, userID string) (*models.AuthIdentity, error) {
	var identity models.AuthIdentity
	if err := ps.getDB(ctx).Where("user_id = ?", userID).First(&identity).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, store.ErrNotFound
		}
		return nil, fmt.Errorf("failed to get auth identity by user: %w", err)
	}
	return &identity, nil
}

// CreateAuthIdentity creates a new auth identity.
func (ps PostgresDbStore) CreateAuthIdentity(ctx context.Context, identity *models.AuthIdentity) error {
	if err := ps.getDB(ctx).Create(identity).Error; err != nil {
		return fmt.Errorf("failed to create auth identity: %w", err)
	}
	return nil
}

// UpdateAuthIdentityLogin stamps last_login_at=now() and refreshes
// display_name for an identity.
func (ps PostgresDbStore) UpdateAuthIdentityLogin(ctx context.Context, identityID string, displayName string) error {
	now := time.Now().UTC()
	result := ps.getDB(ctx).Model(&models.AuthIdentity{}).
		Where("identity_id = ?", identityID).
		Updates(map[string]interface{}{
			"last_login_at": now,
			"display_name":  displayName,
		})
	if result.Error != nil {
		return fmt.Errorf("failed to update auth identity login: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return store.ErrNotFound
	}
	return nil
}

// --- auth_credentials -------------------------------------------------------

// UpsertAuthCredential creates or replaces an encrypted auth credential by
// name.
func (ps PostgresDbStore) UpsertAuthCredential(ctx context.Context, credential *models.AuthCredential) error {
	err := ps.getDB(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "name"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"master_key_id", "encrypted_value", "updated_at",
		}),
	}).Create(credential).Error
	if err != nil {
		return fmt.Errorf("failed to upsert auth credential: %w", err)
	}
	return nil
}

// GetAuthCredential retrieves an auth credential by name.
func (ps PostgresDbStore) GetAuthCredential(ctx context.Context, name string) (*models.AuthCredential, error) {
	var credential models.AuthCredential
	if err := ps.getDB(ctx).Where("name = ?", name).First(&credential).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, store.ErrNotFound
		}
		return nil, fmt.Errorf("failed to get auth credential: %w", err)
	}
	return &credential, nil
}

// --- auth_trusted_identities -------------------------------------------------

// ListTrustedIdentities lists all trusted-identity admission rows.
func (ps PostgresDbStore) ListTrustedIdentities(ctx context.Context) ([]models.AuthTrustedIdentity, error) {
	var identities []models.AuthTrustedIdentity
	if err := ps.getDB(ctx).Order("domain ASC, handle ASC").Find(&identities).Error; err != nil {
		return nil, fmt.Errorf("failed to list trusted identities: %w", err)
	}
	return identities, nil
}

// UpsertTrustedIdentity creates or replaces a trusted-identity row keyed by
// (domain, handle).
func (ps PostgresDbStore) UpsertTrustedIdentity(ctx context.Context, identity *models.AuthTrustedIdentity) error {
	err := ps.getDB(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "domain"}, {Name: "handle"}},
		DoUpdates: clause.AssignmentColumns([]string{"source"}),
	}).Create(identity).Error
	if err != nil {
		return fmt.Errorf("failed to upsert trusted identity: %w", err)
	}
	return nil
}

// DeleteTrustedIdentity removes a trusted-identity row.
func (ps PostgresDbStore) DeleteTrustedIdentity(ctx context.Context, domain, handle string) error {
	result := ps.getDB(ctx).Where("domain = ? AND handle = ?", domain, handle).Delete(&models.AuthTrustedIdentity{})
	if result.Error != nil {
		return fmt.Errorf("failed to delete trusted identity: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return store.ErrNotFound
	}
	return nil
}

// TrustedIdentityExists reports whether an admitted identity row matches
// (domain, handle), honoring bare-domain wildcard semantics: a row with
// handle="" matches any handle at that domain.
func (ps PostgresDbStore) TrustedIdentityExists(ctx context.Context, domain, handle string) (bool, error) {
	var count int64
	err := ps.getDB(ctx).Model(&models.AuthTrustedIdentity{}).
		Where("domain = ? AND (handle = ? OR handle = '')", domain, handle).
		Count(&count).Error
	if err != nil {
		return false, fmt.Errorf("failed to check trusted identity: %w", err)
	}
	return count > 0, nil
}

// --- auth_trusted_domain_patterns --------------------------------------------

// ListTrustedDomainPatterns lists all trusted domain regex patterns.
func (ps PostgresDbStore) ListTrustedDomainPatterns(ctx context.Context) ([]models.AuthTrustedDomainPattern, error) {
	var patterns []models.AuthTrustedDomainPattern
	if err := ps.getDB(ctx).Order("created_at ASC").Find(&patterns).Error; err != nil {
		return nil, fmt.Errorf("failed to list trusted domain patterns: %w", err)
	}
	return patterns, nil
}

// CreateTrustedDomainPattern creates a new trusted domain regex pattern.
func (ps PostgresDbStore) CreateTrustedDomainPattern(ctx context.Context, pattern *models.AuthTrustedDomainPattern) error {
	if err := ps.getDB(ctx).Create(pattern).Error; err != nil {
		return fmt.Errorf("failed to create trusted domain pattern: %w", err)
	}
	return nil
}

// DeleteTrustedDomainPattern deletes a trusted domain regex pattern by ID.
func (ps PostgresDbStore) DeleteTrustedDomainPattern(ctx context.Context, patternID string) error {
	if !isValidUUID(patternID) {
		return store.ErrNotFound
	}

	result := ps.getDB(ctx).Where("pattern_id = ?", patternID).Delete(&models.AuthTrustedDomainPattern{})
	if result.Error != nil {
		return fmt.Errorf("failed to delete trusted domain pattern: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return store.ErrNotFound
	}
	return nil
}

// --- auth_login_attempts -----------------------------------------------------

// CreateLoginAttempt persists a pending single-use login attempt.
func (ps PostgresDbStore) CreateLoginAttempt(ctx context.Context, attempt *models.AuthLoginAttempt) error {
	if err := ps.getDB(ctx).Create(attempt).Error; err != nil {
		return fmt.Errorf("failed to create login attempt: %w", err)
	}
	return nil
}

// ConsumeLoginAttempt atomically fetches and deletes a pending login attempt
// by its hash, so it can only ever be used once. Returns store.ErrNotFound if
// no matching attempt exists (already consumed, expired-and-swept, or never
// created).
func (ps PostgresDbStore) ConsumeLoginAttempt(ctx context.Context, attemptHash []byte) (*models.AuthLoginAttempt, error) {
	var attempt models.AuthLoginAttempt
	err := ps.getDB(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("attempt_hash = ?", attemptHash).First(&attempt).Error; err != nil {
			return err
		}
		return tx.Where("attempt_hash = ?", attemptHash).Delete(&models.AuthLoginAttempt{}).Error
	})
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, store.ErrNotFound
		}
		return nil, fmt.Errorf("failed to consume login attempt: %w", err)
	}
	return &attempt, nil
}

// DeleteExpiredLoginAttempts deletes pending login attempts past their expiry
// and returns the number of rows removed.
func (ps PostgresDbStore) DeleteExpiredLoginAttempts(ctx context.Context) (int64, error) {
	result := ps.getDB(ctx).Where("expires_at <= ?", time.Now().UTC()).Delete(&models.AuthLoginAttempt{})
	if result.Error != nil {
		return 0, fmt.Errorf("failed to delete expired login attempts: %w", result.Error)
	}
	return result.RowsAffected, nil
}
