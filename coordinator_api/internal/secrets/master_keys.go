package secrets

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"gorm.io/gorm"
)

// Environment variable name for master keys
const MasterKeysEnvVar = "REACTORCIDE_MASTER_KEYS"

var (
	// ErrNoMasterKeys is returned when no master keys are available (env or database)
	ErrNoMasterKeys = errors.New("no master keys configured")
	// ErrMasterKeyNotFound is returned when a requested master key doesn't exist
	ErrMasterKeyNotFound = errors.New("master key not found")
	// ErrCannotDecommissionPrimary is returned when trying to decommission the primary key
	ErrCannotDecommissionPrimary = errors.New("cannot decommission the primary key")
)

// DefaultKeyCount is the number of keys to auto-generate when none exist
const DefaultKeyCount = 3

// MasterKeyManager handles multiple master keys for rotation.
type MasterKeyManager struct {
	keys       map[string][]byte // name -> 32-byte key
	primaryKey string            // name of primary key (first in list)
}

// LoadMasterKeys loads master keys from environment.
// Format: REACTORCIDE_MASTER_KEYS=name1:base64key1,name2:base64key2
// First key in list is the primary (used for new encryptions).
func LoadMasterKeys() (*MasterKeyManager, error) {
	mgr := &MasterKeyManager{keys: make(map[string][]byte)}

	envValue := os.Getenv("REACTORCIDE_MASTER_KEYS")
	if envValue == "" {
		return nil, ErrNoMasterKeys
	}

	pairs := strings.Split(envValue, ",")
	for i, pair := range pairs {
		parts := strings.SplitN(strings.TrimSpace(pair), ":", 2)
		if len(parts) != 2 {
			return nil, fmt.Errorf("invalid master key format: %s (expected name:base64key)", pair)
		}

		name := strings.TrimSpace(parts[0])
		keyBytes, err := base64.StdEncoding.DecodeString(strings.TrimSpace(parts[1]))
		if err != nil {
			return nil, fmt.Errorf("invalid base64 for master key %s: %w", name, err)
		}
		if len(keyBytes) != 32 {
			return nil, fmt.Errorf("master key %s must be 32 bytes, got %d", name, len(keyBytes))
		}

		mgr.keys[name] = keyBytes

		// First key is primary
		if i == 0 {
			mgr.primaryKey = name
		}
	}

	if len(mgr.keys) == 0 {
		return nil, ErrNoMasterKeys
	}

	return mgr, nil
}

// GetKey returns the key bytes for a given key name.
func (m *MasterKeyManager) GetKey(name string) []byte {
	return m.keys[name]
}

// GetPrimaryKey returns the primary key name and bytes.
func (m *MasterKeyManager) GetPrimaryKey() (string, []byte) {
	return m.primaryKey, m.keys[m.primaryKey]
}

// HasKey checks if a key with the given name exists.
func (m *MasterKeyManager) HasKey(name string) bool {
	_, ok := m.keys[name]
	return ok
}

// KeyNames returns all key names.
func (m *MasterKeyManager) KeyNames() []string {
	names := make([]string, 0, len(m.keys))
	for name := range m.keys {
		names = append(names, name)
	}
	return names
}

// GetOrgEncryptionKey decrypts an org's key using any available master key.
func (m *MasterKeyManager) GetOrgEncryptionKey(db *gorm.DB, orgID string) ([]byte, error) {
	// Join with master_keys to get the name for lookup
	var orgKeys []struct {
		models.OrgEncryptionKey
		MasterKeyName string
	}
	if err := db.Table("org_encryption_keys").
		Select("org_encryption_keys.*, master_keys.name as master_key_name").
		Joins("JOIN master_keys ON master_keys.key_id = org_encryption_keys.master_key_id").
		Where("org_encryption_keys.user_id = ?", orgID).
		Find(&orgKeys).Error; err != nil {
		return nil, err
	}

	if len(orgKeys) == 0 {
		return nil, ErrNotInitialized
	}

	// Try each org key row until one decrypts successfully
	for _, orgKey := range orgKeys {
		masterKey := m.keys[orgKey.MasterKeyName]
		if masterKey == nil {
			continue // We don't have this master key in environment
		}

		// Encode master key for Fernet
		encodedKey := make([]byte, base64.URLEncoding.EncodedLen(len(masterKey)))
		base64.URLEncoding.Encode(encodedKey, masterKey)

		decrypted, err := fernetDecrypt(encodedKey, orgKey.EncryptedKey)
		if err == nil {
			return decrypted, nil
		}
	}

	return nil, errors.New("no valid master key available for org")
}

// RegisterMasterKey registers a new master key in the database.
// The key must already exist in REACTORCIDE_MASTER_KEYS environment variable.
func (m *MasterKeyManager) RegisterMasterKey(db *gorm.DB, name, description string) (*models.MasterKey, error) {
	// Verify key exists in environment
	if !m.HasKey(name) {
		return nil, fmt.Errorf("master key %s not found in REACTORCIDE_MASTER_KEYS", name)
	}

	// Check if there's already a primary key in the database
	var primaryCount int64
	db.Model(&models.MasterKey{}).Where("is_primary = true").Count(&primaryCount)

	// Only set as primary if this is the primary in the manager AND no primary exists yet
	isPrimary := (name == m.primaryKey) && (primaryCount == 0)

	// Let database generate key_id via generate_ulid()
	mk := &models.MasterKey{
		Name:        name,
		IsActive:    true, // Active since it's in environment
		IsPrimary:   isPrimary,
		Description: description,
	}

	if err := db.Create(mk).Error; err != nil {
		return nil, err
	}

	return mk, nil
}

// RotateToKey encrypts all org keys with the specified master key.
// The key must already be in the environment.
func (m *MasterKeyManager) RotateToKey(db *gorm.DB, keyName string) error {
	// Verify we have the key in environment
	newKey := m.keys[keyName]
	if newKey == nil {
		return fmt.Errorf("master key %s not found in environment", keyName)
	}

	// Get the master key record
	var mk models.MasterKey
	if err := db.Where("name = ?", keyName).First(&mk).Error; err != nil {
		return fmt.Errorf("master key %s not registered in database", keyName)
	}

	// Get all unique org IDs
	var orgIDs []string
	if err := db.Model(&models.OrgEncryptionKey{}).
		Distinct("user_id").
		Pluck("user_id", &orgIDs).Error; err != nil {
		return err
	}

	for _, orgID := range orgIDs {
		// Get the org's decrypted key using any existing master key
		orgKey, err := m.GetOrgEncryptionKey(db, orgID)
		if err != nil {
			return fmt.Errorf("failed to get org key for %s: %w", orgID, err)
		}

		// Get the salt from existing entry
		var existing models.OrgEncryptionKey
		if err := db.Where("user_id = ?", orgID).First(&existing).Error; err != nil {
			return err
		}

		// Check if already encrypted with this key
		var count int64
		db.Model(&models.OrgEncryptionKey{}).
			Where("user_id = ? AND master_key_id = ?", orgID, mk.KeyID).
			Count(&count)
		if count > 0 {
			continue // Already done
		}

		// Encode master key for Fernet
		encodedKey := make([]byte, base64.URLEncoding.EncodedLen(len(newKey)))
		base64.URLEncoding.Encode(encodedKey, newKey)

		// Encrypt with new master key
		encryptedWithNew, err := fernetEncrypt(encodedKey, orgKey)
		if err != nil {
			return err
		}

		// Insert new row - let database generate id via generate_ulid()
		newEntry := models.OrgEncryptionKey{
			UserID:       orgID,
			MasterKeyID:  mk.KeyID,
			EncryptedKey: encryptedWithNew,
			Salt:         existing.Salt,
		}
		if err := db.Create(&newEntry).Error; err != nil {
			return err
		}
	}

	// Mark key as active
	return db.Model(&models.MasterKey{}).
		Where("key_id = ?", mk.KeyID).
		Update("is_active", true).Error
}

// SyncPrimaryFromEnv updates the database to match which key is primary in environment.
func (m *MasterKeyManager) SyncPrimaryFromEnv(db *gorm.DB) error {
	return db.Transaction(func(tx *gorm.DB) error {
		// Unset all primary flags
		if err := tx.Model(&models.MasterKey{}).
			Where("is_primary = true").
			Update("is_primary", false).Error; err != nil {
			return err
		}
		// Set primary based on environment (first key in list)
		return tx.Model(&models.MasterKey{}).
			Where("name = ?", m.primaryKey).
			Update("is_primary", true).Error
	})
}

// DecommissionKey removes a master key after rotation is complete.
func (m *MasterKeyManager) DecommissionKey(db *gorm.DB, keyName string) error {
	// Get the master key record
	var mk models.MasterKey
	if err := db.Where("name = ?", keyName).First(&mk).Error; err != nil {
		return fmt.Errorf("master key %s not found", keyName)
	}

	if mk.IsPrimary {
		return ErrCannotDecommissionPrimary
	}

	// Delete all org key entries encrypted with this key
	if err := db.Where("master_key_id = ?", mk.KeyID).
		Delete(&models.OrgEncryptionKey{}).Error; err != nil {
		return err
	}

	// Mark key as inactive
	return db.Model(&models.MasterKey{}).
		Where("key_id = ?", mk.KeyID).
		Update("is_active", false).Error
}

// MarkSecretsInitialized marks a user's secrets as initialized.
func MarkSecretsInitialized(db *gorm.DB, orgID string) error {
	return db.Model(&models.User{}).
		Where("user_id = ?", orgID).
		Update("secrets_initialized_at", db.NowFunc()).Error
}

// InitializeOrgSecretsWithMark creates encryption keys for a new org and marks them as initialized.
func (m *MasterKeyManager) InitializeOrgSecretsWithMark(db *gorm.DB, orgID string) error {
	if err := m.InitializeOrgSecrets(db, orgID); err != nil {
		return err
	}
	return MarkSecretsInitialized(db, orgID)
}

// InitializeOrgSecrets creates encryption keys for a new org.
func (m *MasterKeyManager) InitializeOrgSecrets(db *gorm.DB, orgID string) error {
	// Generate random org encryption key
	orgKey := make([]byte, 32)
	if _, err := cryptoRandRead(orgKey); err != nil {
		return fmt.Errorf("failed to generate encryption key: %w", err)
	}

	// Generate salt
	salt := make([]byte, 32)
	if _, err := cryptoRandRead(salt); err != nil {
		return fmt.Errorf("failed to generate salt: %w", err)
	}

	// Get all active master keys
	var masterKeys []models.MasterKey
	if err := db.Where("is_active = true").Find(&masterKeys).Error; err != nil {
		return fmt.Errorf("failed to get master keys: %w", err)
	}

	if len(masterKeys) == 0 {
		return errors.New("no active master keys configured")
	}

	// Encrypt org key with each active master key
	for _, mk := range masterKeys {
		masterKeyBytes := m.keys[mk.Name]
		if masterKeyBytes == nil {
			return fmt.Errorf("master key %s not in environment", mk.Name)
		}

		// Encode master key for Fernet
		encodedKey := make([]byte, base64.URLEncoding.EncodedLen(len(masterKeyBytes)))
		base64.URLEncoding.Encode(encodedKey, masterKeyBytes)

		encryptedOrgKey, err := fernetEncrypt(encodedKey, orgKey)
		if err != nil {
			return fmt.Errorf("failed to encrypt org key: %w", err)
		}

		// Let database generate id via generate_ulid()
		orgKeyEntry := models.OrgEncryptionKey{
			UserID:       orgID,
			MasterKeyID:  mk.KeyID,
			EncryptedKey: encryptedOrgKey,
			Salt:         salt,
		}

		if err := db.Create(&orgKeyEntry).Error; err != nil {
			return fmt.Errorf("failed to store org key: %w", err)
		}
	}

	return nil
}

// cryptoRandRead is a variable to allow mocking in tests
var cryptoRandRead = rand.Read

// AutoRegisterKeys registers all keys from the environment that aren't already in the database.
// This should be called on startup to ensure the database reflects the environment configuration.
// Returns the number of keys registered and any error.
func (m *MasterKeyManager) AutoRegisterKeys(db *gorm.DB) (int, error) {
	registered := 0

	for _, keyName := range m.KeyNames() {
		// Check if key already exists in database
		var existingKey models.MasterKey
		err := db.Where("name = ?", keyName).First(&existingKey).Error
		if err == nil {
			// Key already exists, just ensure is_active status matches
			isPrimary := keyName == m.primaryKey
			if existingKey.IsPrimary != isPrimary || !existingKey.IsActive {
				// Update primary and active status
				db.Model(&existingKey).Updates(map[string]interface{}{
					"is_active":  true,
					"is_primary": isPrimary,
				})
			}
			continue
		}

		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return registered, fmt.Errorf("failed to check existing key %s: %w", keyName, err)
		}

		// Key doesn't exist, register it
		description := "Auto-registered on startup"
		if keyName == m.primaryKey {
			description = "Auto-registered on startup (primary)"
		}

		if _, err := m.RegisterMasterKey(db, keyName, description); err != nil {
			return registered, fmt.Errorf("failed to register key %s: %w", keyName, err)
		}
		registered++
	}

	// Sync primary status to match environment
	if err := m.SyncPrimaryFromEnv(db); err != nil {
		return registered, fmt.Errorf("failed to sync primary from env: %w", err)
	}

	return registered, nil
}

// LoadOrCreateMasterKeys is the main entry point for loading master keys.
// It tries sources in this order:
// 1. Environment variable REACTORCIDE_MASTER_KEYS (for bring-your-own-key scenarios)
// 2. Database (for auto-generated keys stored in key_material column)
// 3. If neither has keys, generates DefaultKeyCount new keys and stores them in DB
//
// This enables a "just works" experience where users don't need to manage keys,
// while still allowing power users to provide their own keys via env var.
//
// Handles race conditions when multiple services start simultaneously by retrying
// the database load if key generation fails due to duplicate key constraints.
func LoadOrCreateMasterKeys(db *gorm.DB) (*MasterKeyManager, error) {
	// 1. Try environment variable first (takes precedence)
	if mgr, err := LoadMasterKeys(); err == nil {
		// Register env keys in DB if not already there
		if _, err := mgr.AutoRegisterKeys(db); err != nil {
			return nil, fmt.Errorf("failed to register env keys in database: %w", err)
		}
		return mgr, nil
	}

	// 2. Try loading from database
	if mgr, err := LoadMasterKeysFromDB(db); err == nil {
		return mgr, nil
	}

	// 3. No keys anywhere - generate new ones
	if err := GenerateAndStoreMasterKeys(db, DefaultKeyCount); err != nil {
		// If generation failed due to duplicate key (race condition with another service),
		// retry loading from database - the other service may have just created the keys
		if strings.Contains(err.Error(), "duplicate key") || strings.Contains(err.Error(), "unique constraint") {
			if mgr, loadErr := LoadMasterKeysFromDB(db); loadErr == nil {
				return mgr, nil
			}
		}
		return nil, fmt.Errorf("failed to generate master keys: %w", err)
	}

	// Load the newly generated keys
	return LoadMasterKeysFromDB(db)
}

// LoadMasterKeysFromDB loads master keys from the database.
// Only loads keys that have key_material stored (auto-generated keys).
// Keys provided via env var won't have key_material and are loaded via LoadMasterKeys().
func LoadMasterKeysFromDB(db *gorm.DB) (*MasterKeyManager, error) {
	var masterKeys []models.MasterKey
	if err := db.Where("is_active = true AND key_material IS NOT NULL").
		Order("is_primary DESC, created_at ASC").
		Find(&masterKeys).Error; err != nil {
		return nil, fmt.Errorf("failed to load master keys from database: %w", err)
	}

	if len(masterKeys) == 0 {
		return nil, ErrNoMasterKeys
	}

	mgr := &MasterKeyManager{keys: make(map[string][]byte)}

	for i, mk := range masterKeys {
		if len(mk.KeyMaterial) != 32 {
			return nil, fmt.Errorf("master key %s has invalid key material length: %d", mk.Name, len(mk.KeyMaterial))
		}

		mgr.keys[mk.Name] = mk.KeyMaterial

		// First key (primary or earliest) is the primary for new encryptions
		if i == 0 {
			mgr.primaryKey = mk.Name
		}
	}

	return mgr, nil
}

// GenerateAndStoreMasterKeys generates the specified number of master keys
// and stores them in the database with their key material.
// The first generated key is marked as primary.
func GenerateAndStoreMasterKeys(db *gorm.DB, count int) error {
	if count <= 0 {
		return errors.New("count must be positive")
	}

	return db.Transaction(func(tx *gorm.DB) error {
		for i := 0; i < count; i++ {
			// Generate 32 random bytes for the key
			keyMaterial := make([]byte, 32)
			if _, err := cryptoRandRead(keyMaterial); err != nil {
				return fmt.Errorf("failed to generate key material: %w", err)
			}

			// Generate a unique name
			name, err := generateKeyName(tx)
			if err != nil {
				return fmt.Errorf("failed to generate key name: %w", err)
			}

			mk := &models.MasterKey{
				Name:        name,
				IsActive:    true,
				IsPrimary:   i == 0, // First key is primary
				Description: "Auto-generated on first startup",
				KeyMaterial: keyMaterial,
			}

			if err := tx.Create(mk).Error; err != nil {
				return fmt.Errorf("failed to store master key: %w", err)
			}
		}

		return nil
	})
}

// generateKeyName generates a unique key name in the format "mk-XXXXXX"
// where XXXXXX is a random hex string.
func generateKeyName(db *gorm.DB) (string, error) {
	for attempts := 0; attempts < 10; attempts++ {
		// Generate 3 random bytes = 6 hex chars
		randomBytes := make([]byte, 3)
		if _, err := cryptoRandRead(randomBytes); err != nil {
			return "", err
		}

		name := fmt.Sprintf("mk-%x", randomBytes)

		// Check if name already exists
		var count int64
		if err := db.Model(&models.MasterKey{}).Where("name = ?", name).Count(&count).Error; err != nil {
			return "", err
		}

		if count == 0 {
			return name, nil
		}
	}

	return "", errors.New("failed to generate unique key name after 10 attempts")
}
