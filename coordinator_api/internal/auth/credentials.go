package auth

import (
	"context"
	"fmt"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/secrets"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"gorm.io/gorm"
)

// CredentialStore is the narrow store surface StoreCredential/LoadCredential
// consume: the ctx-scoped auth_credentials CRUD from Task A
// (postgres_store/auth_operations.go), plus direct *gorm.DB access to
// resolve master_keys rows by name/id. The direct-DB half mirrors how
// internal/secrets/master_keys.go itself already operates (e.g.
// GetOrgEncryptionKey(db, orgID)) rather than going through the Store
// interface — auth_credentials.master_key_id is a real FK to
// master_keys.key_id, and MasterKeyManager only knows key *names*.
type CredentialStore interface {
	UpsertAuthCredential(ctx context.Context, credential *models.AuthCredential) error
	GetAuthCredential(ctx context.Context, name string) (*models.AuthCredential, error)
	GetDB() *gorm.DB
}

// StoreCredential Fernet-encrypts plaintext under keys' primary master key
// and upserts it into auth_credentials under name (e.g.
// models.AuthCredentialLocalRPIdentity, models.AuthCredentialRPAPIKey).
// Never logs plaintext; callers must not either.
func StoreCredential(ctx context.Context, store CredentialStore, keys *secrets.MasterKeyManager, name string, plaintext []byte) error {
	keyName, ciphertext, err := keys.EncryptWithPrimary(plaintext)
	if err != nil {
		return fmt.Errorf("auth: encrypting credential %s: %w", name, err)
	}

	var mk models.MasterKey
	if err := store.GetDB().WithContext(ctx).Where("name = ?", keyName).First(&mk).Error; err != nil {
		return fmt.Errorf("auth: resolving primary master key %s: %w", keyName, err)
	}

	return store.UpsertAuthCredential(ctx, &models.AuthCredential{
		Name:           name,
		MasterKeyID:    mk.KeyID,
		EncryptedValue: ciphertext,
	})
}

// LoadCredential loads and decrypts an auth_credentials row by name. Returns
// store.ErrNotFound (via GetAuthCredential) if no row exists by that name.
func LoadCredential(ctx context.Context, store CredentialStore, keys *secrets.MasterKeyManager, name string) ([]byte, error) {
	cred, err := store.GetAuthCredential(ctx, name)
	if err != nil {
		return nil, err
	}

	var mk models.MasterKey
	if err := store.GetDB().WithContext(ctx).Where("key_id = ?", cred.MasterKeyID).First(&mk).Error; err != nil {
		return nil, fmt.Errorf("auth: resolving master key for credential %s: %w", name, err)
	}

	plaintext, err := keys.DecryptWithKey(mk.Name, cred.EncryptedValue)
	if err != nil {
		return nil, fmt.Errorf("auth: decrypting credential %s: %w", name, err)
	}
	return plaintext, nil
}
