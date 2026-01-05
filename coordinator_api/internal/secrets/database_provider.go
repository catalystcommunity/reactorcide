package secrets

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"gorm.io/gorm"
)

// DatabaseProvider stores secrets in PostgreSQL with per-org encryption.
type DatabaseProvider struct {
	db            *gorm.DB
	orgID         string // User ID acting as org ID
	encryptionKey []byte // 32-byte Fernet key for this org (already decoded)
}

// NewDatabaseProvider creates a new DatabaseProvider.
// The encryptionKey should be the decrypted 32-byte org encryption key.
func NewDatabaseProvider(db *gorm.DB, orgID string, encryptionKey []byte) (*DatabaseProvider, error) {
	if len(encryptionKey) != 32 {
		return nil, errors.New("encryption key must be 32 bytes")
	}
	return &DatabaseProvider{
		db:            db,
		orgID:         orgID,
		encryptionKey: encryptionKey,
	}, nil
}

// Get retrieves a secret value. Returns empty string if not found.
func (p *DatabaseProvider) Get(ctx context.Context, path, key string) (string, error) {
	if err := validatePath(path); err != nil {
		return "", err
	}
	if err := validateKey(key); err != nil {
		return "", err
	}

	var secret models.Secret
	err := p.db.WithContext(ctx).
		Where("user_id = ? AND path = ? AND key = ?", p.orgID, path, key).
		First(&secret).Error

	if errors.Is(err, gorm.ErrRecordNotFound) {
		return "", nil // Not found returns empty string
	}
	if err != nil {
		return "", fmt.Errorf("failed to get secret: %w", err)
	}

	// Decrypt the value
	value, err := p.decrypt(secret.EncryptedValue)
	if err != nil {
		return "", fmt.Errorf("failed to decrypt secret: %w", err)
	}

	return value, nil
}

// Set stores a secret value, creating or updating as needed.
func (p *DatabaseProvider) Set(ctx context.Context, path, key, value string) error {
	if err := validatePath(path); err != nil {
		return err
	}
	if err := validateKey(key); err != nil {
		return err
	}

	// Encrypt the value
	encrypted, err := p.encrypt(value)
	if err != nil {
		return fmt.Errorf("failed to encrypt secret: %w", err)
	}

	now := time.Now().UTC()

	// Try to find existing secret
	var existing models.Secret
	err = p.db.WithContext(ctx).
		Where("user_id = ? AND path = ? AND key = ?", p.orgID, path, key).
		First(&existing).Error

	if errors.Is(err, gorm.ErrRecordNotFound) {
		// Create new secret - let database generate the secret_id via generate_ulid()
		secret := models.Secret{
			CreatedAt:      now,
			UpdatedAt:      now,
			UserID:         p.orgID,
			Namespace:      p.orgID, // For now, namespace is same as user ID
			Path:           path,
			Key:            key,
			EncryptedValue: encrypted,
		}
		if err := p.db.WithContext(ctx).Create(&secret).Error; err != nil {
			return fmt.Errorf("failed to create secret: %w", err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("failed to check existing secret: %w", err)
	}

	// Update existing secret
	if err := p.db.WithContext(ctx).Model(&existing).Updates(map[string]interface{}{
		"encrypted_value": encrypted,
		"updated_at":      now,
	}).Error; err != nil {
		return fmt.Errorf("failed to update secret: %w", err)
	}

	return nil
}

// Delete removes a secret. Returns true if it existed.
func (p *DatabaseProvider) Delete(ctx context.Context, path, key string) (bool, error) {
	if err := validatePath(path); err != nil {
		return false, err
	}
	if err := validateKey(key); err != nil {
		return false, err
	}

	result := p.db.WithContext(ctx).
		Where("user_id = ? AND path = ? AND key = ?", p.orgID, path, key).
		Delete(&models.Secret{})

	if result.Error != nil {
		return false, fmt.Errorf("failed to delete secret: %w", result.Error)
	}

	return result.RowsAffected > 0, nil
}

// ListKeys returns all keys under a path.
func (p *DatabaseProvider) ListKeys(ctx context.Context, path string) ([]string, error) {
	if err := validatePath(path); err != nil {
		return nil, err
	}

	var keys []string
	err := p.db.WithContext(ctx).
		Model(&models.Secret{}).
		Where("user_id = ? AND path = ?", p.orgID, path).
		Pluck("key", &keys).Error

	if err != nil {
		return nil, fmt.Errorf("failed to list keys: %w", err)
	}

	return keys, nil
}

// ListPaths returns all paths that have secrets.
func (p *DatabaseProvider) ListPaths(ctx context.Context) ([]string, error) {
	var paths []string
	err := p.db.WithContext(ctx).
		Model(&models.Secret{}).
		Where("user_id = ?", p.orgID).
		Distinct("path").
		Pluck("path", &paths).Error

	if err != nil {
		return nil, fmt.Errorf("failed to list paths: %w", err)
	}

	return paths, nil
}

// GetMulti retrieves multiple secrets efficiently.
// Returns a map of "path:key" -> value.
func (p *DatabaseProvider) GetMulti(ctx context.Context, refs []SecretRef) (map[string]string, error) {
	// Validate all refs first
	for _, ref := range refs {
		if err := validatePath(ref.Path); err != nil {
			return nil, fmt.Errorf("%s: %w", ref.Path, err)
		}
		if err := validateKey(ref.Key); err != nil {
			return nil, fmt.Errorf("%s: %w", ref.Key, err)
		}
	}

	if len(refs) == 0 {
		return make(map[string]string), nil
	}

	// Build query for all secrets at once
	var secrets []models.Secret
	tx := p.db.WithContext(ctx).Where("user_id = ?", p.orgID)

	// Build OR conditions for each ref
	conditions := make([]interface{}, 0, len(refs)*2)
	query := ""
	for i, ref := range refs {
		if i > 0 {
			query += " OR "
		}
		query += "(path = ? AND key = ?)"
		conditions = append(conditions, ref.Path, ref.Key)
	}

	err := tx.Where(query, conditions...).Find(&secrets).Error
	if err != nil {
		return nil, fmt.Errorf("failed to get secrets: %w", err)
	}

	// Build a map for quick lookup
	secretMap := make(map[string]*models.Secret)
	for i := range secrets {
		mapKey := fmt.Sprintf("%s:%s", secrets[i].Path, secrets[i].Key)
		secretMap[mapKey] = &secrets[i]
	}

	// Decrypt and build result
	results := make(map[string]string, len(refs))
	for _, ref := range refs {
		mapKey := fmt.Sprintf("%s:%s", ref.Path, ref.Key)
		if secret, ok := secretMap[mapKey]; ok {
			value, err := p.decrypt(secret.EncryptedValue)
			if err != nil {
				return nil, fmt.Errorf("failed to decrypt secret %s: %w", mapKey, err)
			}
			results[mapKey] = value
		} else {
			results[mapKey] = "" // Not found
		}
	}

	return results, nil
}

// encrypt encrypts a plaintext string using Fernet.
func (p *DatabaseProvider) encrypt(plaintext string) ([]byte, error) {
	// Encode key for Fernet
	encodedKey := make([]byte, base64.URLEncoding.EncodedLen(len(p.encryptionKey)))
	base64.URLEncoding.Encode(encodedKey, p.encryptionKey)

	return fernetEncrypt(encodedKey, []byte(plaintext))
}

// decrypt decrypts a Fernet-encrypted value.
func (p *DatabaseProvider) decrypt(encrypted []byte) (string, error) {
	// Encode key for Fernet
	encodedKey := make([]byte, base64.URLEncoding.EncodedLen(len(p.encryptionKey)))
	base64.URLEncoding.Encode(encodedKey, p.encryptionKey)

	plaintext, err := fernetDecrypt(encodedKey, encrypted)
	if err != nil {
		return "", err
	}

	return string(plaintext), nil
}

// Ensure DatabaseProvider implements Provider interface
var _ Provider = (*DatabaseProvider)(nil)
