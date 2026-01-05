package test

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"testing"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/secrets"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

const (
	testFernetVersion  = 0x80
	testFernetIVSize   = 16
	testFernetHMACSize = 32
)

// setupTestMasterKeyAndOrg creates a master key and org encryption key for testing.
// Returns the org encryption key bytes that can be used with DatabaseProvider.
func setupTestMasterKeyAndOrg(t *testing.T, tx *gorm.DB, userID string) []byte {
	t.Helper()

	// Create a 32-byte master key
	masterKeyBytes := make([]byte, 32)
	for i := range masterKeyBytes {
		masterKeyBytes[i] = byte(i + 1) // Simple deterministic key for testing
	}

	// Create master key in database - let database generate key_id
	masterKey := &models.MasterKey{
		Name:        "test-master-key",
		IsActive:    true,
		IsPrimary:   true,
		Description: "Test master key",
	}
	err := tx.Create(masterKey).Error
	require.NoError(t, err)

	// Create a 32-byte org encryption key
	orgKeyBytes := make([]byte, 32)
	for i := range orgKeyBytes {
		orgKeyBytes[i] = byte(i + 100) // Different from master key
	}

	// Encrypt the org key with the master key using Fernet
	encodedMasterKey := make([]byte, base64.URLEncoding.EncodedLen(len(masterKeyBytes)))
	base64.URLEncoding.Encode(encodedMasterKey, masterKeyBytes)

	encryptedOrgKey, err := createTestFernetToken(encodedMasterKey, orgKeyBytes)
	require.NoError(t, err)

	// Create org encryption key in database
	salt := make([]byte, 32)
	for i := range salt {
		salt[i] = byte(i + 200)
	}

	// Let database generate id
	orgEncKey := &models.OrgEncryptionKey{
		UserID:       userID,
		MasterKeyID:  masterKey.KeyID,
		EncryptedKey: encryptedOrgKey,
		Salt:         salt,
	}
	err = tx.Create(orgEncKey).Error
	require.NoError(t, err)

	return orgKeyBytes
}

// createTestFernetToken creates a Fernet-compatible token for testing.
// This is a test-only implementation that mirrors the production code.
func createTestFernetToken(encodedKey, plaintext []byte) ([]byte, error) {
	// Decode base64 key
	decodedKey := make([]byte, base64.URLEncoding.DecodedLen(len(encodedKey)))
	n, err := base64.URLEncoding.Decode(decodedKey, encodedKey)
	if err != nil {
		return nil, err
	}
	decodedKey = decodedKey[:n]

	signKey := decodedKey[:16]
	encKey := decodedKey[16:]

	// Generate IV
	iv := make([]byte, testFernetIVSize)
	if _, err := rand.Read(iv); err != nil {
		return nil, err
	}

	// PKCS7 padding
	block, err := aes.NewCipher(encKey)
	if err != nil {
		return nil, err
	}
	padLen := aes.BlockSize - (len(plaintext) % aes.BlockSize)
	padded := make([]byte, len(plaintext)+padLen)
	copy(padded, plaintext)
	for i := len(plaintext); i < len(padded); i++ {
		padded[i] = byte(padLen)
	}

	// Encrypt
	mode := cipher.NewCBCEncrypter(block, iv)
	ciphertext := make([]byte, len(padded))
	mode.CryptBlocks(ciphertext, padded)

	// Build Fernet token
	timestamp := time.Now().Unix()
	tokenLen := 1 + 8 + testFernetIVSize + len(ciphertext) + testFernetHMACSize
	token := make([]byte, tokenLen)

	token[0] = testFernetVersion
	binary.BigEndian.PutUint64(token[1:9], uint64(timestamp))
	copy(token[9:25], iv)
	copy(token[25:25+len(ciphertext)], ciphertext)

	// HMAC
	h := hmac.New(sha256.New, signKey)
	h.Write(token[:25+len(ciphertext)])
	copy(token[25+len(ciphertext):], h.Sum(nil))

	return token, nil
}

// TestNewDatabaseProvider tests DatabaseProvider creation
func TestNewDatabaseProvider(t *testing.T) {
	t.Run("Valid 32-byte key", func(t *testing.T) {
		RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
			key := make([]byte, 32)
			provider, err := secrets.NewDatabaseProvider(tx, "test-org", key)
			require.NoError(t, err)
			assert.NotNil(t, provider)
		})
	})

	t.Run("Invalid key length", func(t *testing.T) {
		RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
			// Too short
			shortKey := make([]byte, 16)
			_, err := secrets.NewDatabaseProvider(tx, "test-org", shortKey)
			assert.Error(t, err)
			assert.Contains(t, err.Error(), "32 bytes")

			// Too long
			longKey := make([]byte, 64)
			_, err = secrets.NewDatabaseProvider(tx, "test-org", longKey)
			assert.Error(t, err)
			assert.Contains(t, err.Error(), "32 bytes")
		})
	})
}

// TestDatabaseProviderGetSet tests basic Get and Set operations
func TestDatabaseProviderGetSet(t *testing.T) {
	t.Run("Set and Get secret", func(t *testing.T) {
		RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
			// Create test user
			dataUtils := &DataUtils{db: tx}
			user, err := dataUtils.CreateUser(DataSetup{
				"Username": "secretsuser",
				"Email":    "secrets@example.com",
			})
			require.NoError(t, err)

			// Create 32-byte key for the provider
			orgKey := make([]byte, 32)
			for i := range orgKey {
				orgKey[i] = byte(i + 1)
			}

			provider, err := secrets.NewDatabaseProvider(tx, user.UserID, orgKey)
			require.NoError(t, err)

			// Set a secret
			err = provider.Set(ctx, "project/prod", "API_KEY", "secret-value-123")
			require.NoError(t, err)

			// Get the secret
			value, err := provider.Get(ctx, "project/prod", "API_KEY")
			require.NoError(t, err)
			assert.Equal(t, "secret-value-123", value)
		})
	})

	t.Run("Get non-existent secret returns empty string", func(t *testing.T) {
		RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
			dataUtils := &DataUtils{db: tx}
			user, err := dataUtils.CreateUser(DataSetup{})
			require.NoError(t, err)

			orgKey := make([]byte, 32)
			provider, err := secrets.NewDatabaseProvider(tx, user.UserID, orgKey)
			require.NoError(t, err)

			value, err := provider.Get(ctx, "nonexistent/path", "MISSING_KEY")
			require.NoError(t, err)
			assert.Equal(t, "", value)
		})
	})

	t.Run("Update existing secret", func(t *testing.T) {
		RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
			dataUtils := &DataUtils{db: tx}
			user, err := dataUtils.CreateUser(DataSetup{})
			require.NoError(t, err)

			orgKey := make([]byte, 32)
			for i := range orgKey {
				orgKey[i] = byte(i)
			}
			provider, err := secrets.NewDatabaseProvider(tx, user.UserID, orgKey)
			require.NoError(t, err)

			// Set initial value
			err = provider.Set(ctx, "myapp/config", "DB_PASSWORD", "original")
			require.NoError(t, err)

			// Update value
			err = provider.Set(ctx, "myapp/config", "DB_PASSWORD", "updated")
			require.NoError(t, err)

			// Verify updated value
			value, err := provider.Get(ctx, "myapp/config", "DB_PASSWORD")
			require.NoError(t, err)
			assert.Equal(t, "updated", value)

			// Verify only one record exists
			var count int64
			tx.Model(&models.Secret{}).Where("user_id = ?", user.UserID).Count(&count)
			assert.Equal(t, int64(1), count)
		})
	})
}

// TestDatabaseProviderDelete tests Delete operations
func TestDatabaseProviderDelete(t *testing.T) {
	t.Run("Delete existing secret", func(t *testing.T) {
		RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
			dataUtils := &DataUtils{db: tx}
			user, err := dataUtils.CreateUser(DataSetup{})
			require.NoError(t, err)

			orgKey := make([]byte, 32)
			provider, err := secrets.NewDatabaseProvider(tx, user.UserID, orgKey)
			require.NoError(t, err)

			// Set a secret
			err = provider.Set(ctx, "test/path", "KEY", "value")
			require.NoError(t, err)

			// Delete it
			deleted, err := provider.Delete(ctx, "test/path", "KEY")
			require.NoError(t, err)
			assert.True(t, deleted)

			// Verify it's gone
			value, err := provider.Get(ctx, "test/path", "KEY")
			require.NoError(t, err)
			assert.Equal(t, "", value)
		})
	})

	t.Run("Delete non-existent secret returns false", func(t *testing.T) {
		RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
			dataUtils := &DataUtils{db: tx}
			user, err := dataUtils.CreateUser(DataSetup{})
			require.NoError(t, err)

			orgKey := make([]byte, 32)
			provider, err := secrets.NewDatabaseProvider(tx, user.UserID, orgKey)
			require.NoError(t, err)

			deleted, err := provider.Delete(ctx, "nonexistent/path", "KEY")
			require.NoError(t, err)
			assert.False(t, deleted)
		})
	})
}

// TestDatabaseProviderListKeys tests ListKeys operations
func TestDatabaseProviderListKeys(t *testing.T) {
	t.Run("List keys in path", func(t *testing.T) {
		RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
			dataUtils := &DataUtils{db: tx}
			user, err := dataUtils.CreateUser(DataSetup{})
			require.NoError(t, err)

			orgKey := make([]byte, 32)
			provider, err := secrets.NewDatabaseProvider(tx, user.UserID, orgKey)
			require.NoError(t, err)

			// Set multiple secrets in same path
			err = provider.Set(ctx, "app/config", "KEY1", "value1")
			require.NoError(t, err)
			err = provider.Set(ctx, "app/config", "KEY2", "value2")
			require.NoError(t, err)
			err = provider.Set(ctx, "app/config", "KEY3", "value3")
			require.NoError(t, err)

			// Set secret in different path
			err = provider.Set(ctx, "other/path", "OTHER_KEY", "other")
			require.NoError(t, err)

			// List keys in app/config
			keys, err := provider.ListKeys(ctx, "app/config")
			require.NoError(t, err)
			assert.Len(t, keys, 3)
			assert.Contains(t, keys, "KEY1")
			assert.Contains(t, keys, "KEY2")
			assert.Contains(t, keys, "KEY3")
		})
	})

	t.Run("List keys for empty path returns empty slice", func(t *testing.T) {
		RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
			dataUtils := &DataUtils{db: tx}
			user, err := dataUtils.CreateUser(DataSetup{})
			require.NoError(t, err)

			orgKey := make([]byte, 32)
			provider, err := secrets.NewDatabaseProvider(tx, user.UserID, orgKey)
			require.NoError(t, err)

			keys, err := provider.ListKeys(ctx, "empty/path")
			require.NoError(t, err)
			assert.Empty(t, keys)
		})
	})
}

// TestDatabaseProviderListPaths tests ListPaths operations
func TestDatabaseProviderListPaths(t *testing.T) {
	t.Run("List all paths", func(t *testing.T) {
		RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
			dataUtils := &DataUtils{db: tx}
			user, err := dataUtils.CreateUser(DataSetup{})
			require.NoError(t, err)

			orgKey := make([]byte, 32)
			provider, err := secrets.NewDatabaseProvider(tx, user.UserID, orgKey)
			require.NoError(t, err)

			// Set secrets in multiple paths
			err = provider.Set(ctx, "path/one", "KEY", "value1")
			require.NoError(t, err)
			err = provider.Set(ctx, "path/two", "KEY", "value2")
			require.NoError(t, err)
			err = provider.Set(ctx, "path/three", "KEY", "value3")
			require.NoError(t, err)

			// List all paths
			paths, err := provider.ListPaths(ctx)
			require.NoError(t, err)
			assert.Len(t, paths, 3)
			assert.Contains(t, paths, "path/one")
			assert.Contains(t, paths, "path/two")
			assert.Contains(t, paths, "path/three")
		})
	})

	t.Run("List paths for user with no secrets returns empty slice", func(t *testing.T) {
		RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
			dataUtils := &DataUtils{db: tx}
			user, err := dataUtils.CreateUser(DataSetup{})
			require.NoError(t, err)

			orgKey := make([]byte, 32)
			provider, err := secrets.NewDatabaseProvider(tx, user.UserID, orgKey)
			require.NoError(t, err)

			paths, err := provider.ListPaths(ctx)
			require.NoError(t, err)
			assert.Empty(t, paths)
		})
	})

	t.Run("Paths are isolated by user", func(t *testing.T) {
		RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
			dataUtils := &DataUtils{db: tx}

			// Create two users
			user1, err := dataUtils.CreateUser(DataSetup{"Username": "user1", "Email": "user1@example.com"})
			require.NoError(t, err)
			user2, err := dataUtils.CreateUser(DataSetup{"Username": "user2", "Email": "user2@example.com"})
			require.NoError(t, err)

			orgKey1 := make([]byte, 32)
			orgKey2 := make([]byte, 32)
			for i := range orgKey2 {
				orgKey2[i] = byte(i + 1) // Different key
			}

			provider1, err := secrets.NewDatabaseProvider(tx, user1.UserID, orgKey1)
			require.NoError(t, err)
			provider2, err := secrets.NewDatabaseProvider(tx, user2.UserID, orgKey2)
			require.NoError(t, err)

			// Set secrets for user1
			err = provider1.Set(ctx, "user1/path", "KEY", "value1")
			require.NoError(t, err)

			// Set secrets for user2
			err = provider2.Set(ctx, "user2/path", "KEY", "value2")
			require.NoError(t, err)

			// User1 should only see their paths
			paths1, err := provider1.ListPaths(ctx)
			require.NoError(t, err)
			assert.Len(t, paths1, 1)
			assert.Contains(t, paths1, "user1/path")

			// User2 should only see their paths
			paths2, err := provider2.ListPaths(ctx)
			require.NoError(t, err)
			assert.Len(t, paths2, 1)
			assert.Contains(t, paths2, "user2/path")
		})
	})
}

// TestDatabaseProviderGetMulti tests GetMulti operations
func TestDatabaseProviderGetMulti(t *testing.T) {
	t.Run("Get multiple secrets", func(t *testing.T) {
		RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
			dataUtils := &DataUtils{db: tx}
			user, err := dataUtils.CreateUser(DataSetup{})
			require.NoError(t, err)

			orgKey := make([]byte, 32)
			provider, err := secrets.NewDatabaseProvider(tx, user.UserID, orgKey)
			require.NoError(t, err)

			// Set multiple secrets
			err = provider.Set(ctx, "app/prod", "DB_HOST", "db.example.com")
			require.NoError(t, err)
			err = provider.Set(ctx, "app/prod", "DB_PORT", "5432")
			require.NoError(t, err)
			err = provider.Set(ctx, "app/staging", "API_KEY", "staging-key")
			require.NoError(t, err)

			// Get multiple
			refs := []secrets.SecretRef{
				{Path: "app/prod", Key: "DB_HOST"},
				{Path: "app/prod", Key: "DB_PORT"},
				{Path: "app/staging", Key: "API_KEY"},
			}
			results, err := provider.GetMulti(ctx, refs)
			require.NoError(t, err)

			assert.Equal(t, "db.example.com", results["app/prod:DB_HOST"])
			assert.Equal(t, "5432", results["app/prod:DB_PORT"])
			assert.Equal(t, "staging-key", results["app/staging:API_KEY"])
		})
	})

	t.Run("GetMulti with missing secrets returns empty strings", func(t *testing.T) {
		RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
			dataUtils := &DataUtils{db: tx}
			user, err := dataUtils.CreateUser(DataSetup{})
			require.NoError(t, err)

			orgKey := make([]byte, 32)
			provider, err := secrets.NewDatabaseProvider(tx, user.UserID, orgKey)
			require.NoError(t, err)

			// Set one secret
			err = provider.Set(ctx, "app/config", "EXISTS", "value")
			require.NoError(t, err)

			// Request existing and missing
			refs := []secrets.SecretRef{
				{Path: "app/config", Key: "EXISTS"},
				{Path: "app/config", Key: "MISSING"},
				{Path: "nonexistent/path", Key: "KEY"},
			}
			results, err := provider.GetMulti(ctx, refs)
			require.NoError(t, err)

			assert.Equal(t, "value", results["app/config:EXISTS"])
			assert.Equal(t, "", results["app/config:MISSING"])
			assert.Equal(t, "", results["nonexistent/path:KEY"])
		})
	})

	t.Run("GetMulti with empty refs returns empty map", func(t *testing.T) {
		RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
			dataUtils := &DataUtils{db: tx}
			user, err := dataUtils.CreateUser(DataSetup{})
			require.NoError(t, err)

			orgKey := make([]byte, 32)
			provider, err := secrets.NewDatabaseProvider(tx, user.UserID, orgKey)
			require.NoError(t, err)

			results, err := provider.GetMulti(ctx, []secrets.SecretRef{})
			require.NoError(t, err)
			assert.Empty(t, results)
		})
	})
}

// TestDatabaseProviderValidation tests path and key validation
func TestDatabaseProviderValidation(t *testing.T) {
	t.Run("Invalid path rejected", func(t *testing.T) {
		RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
			dataUtils := &DataUtils{db: tx}
			user, err := dataUtils.CreateUser(DataSetup{})
			require.NoError(t, err)

			orgKey := make([]byte, 32)
			provider, err := secrets.NewDatabaseProvider(tx, user.UserID, orgKey)
			require.NoError(t, err)

			// Path with spaces
			err = provider.Set(ctx, "path with spaces", "KEY", "value")
			assert.Error(t, err)

			// Path traversal attempt
			_, err = provider.Get(ctx, "../escape", "KEY")
			assert.Error(t, err)

			// Empty path
			_, err = provider.Delete(ctx, "", "KEY")
			assert.Error(t, err)
		})
	})

	t.Run("Invalid key rejected", func(t *testing.T) {
		RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
			dataUtils := &DataUtils{db: tx}
			user, err := dataUtils.CreateUser(DataSetup{})
			require.NoError(t, err)

			orgKey := make([]byte, 32)
			provider, err := secrets.NewDatabaseProvider(tx, user.UserID, orgKey)
			require.NoError(t, err)

			// Key with slash (not allowed)
			err = provider.Set(ctx, "valid/path", "key/with/slash", "value")
			assert.Error(t, err)

			// Empty key
			_, err = provider.Get(ctx, "valid/path", "")
			assert.Error(t, err)

			// Key with spaces
			_, err = provider.Delete(ctx, "valid/path", "key with spaces")
			assert.Error(t, err)
		})
	})
}

// TestDatabaseProviderImplementsInterface verifies interface compliance
func TestDatabaseProviderImplementsInterface(t *testing.T) {
	// Compile-time check that DatabaseProvider implements Provider
	var _ secrets.Provider = (*secrets.DatabaseProvider)(nil)
}
