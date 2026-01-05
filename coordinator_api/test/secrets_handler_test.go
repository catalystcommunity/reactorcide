package test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/checkauth"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/handlers"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/secrets"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// testMasterKeyBytes is a test master key (32 bytes)
var testMasterKeyBytes = []byte{
	0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08,
	0x09, 0x0a, 0x0b, 0x0c, 0x0d, 0x0e, 0x0f, 0x10,
	0x11, 0x12, 0x13, 0x14, 0x15, 0x16, 0x17, 0x18,
	0x19, 0x1a, 0x1b, 0x1c, 0x1d, 0x1e, 0x1f, 0x20,
}

// setupSecretsTestEnv sets up the master keys environment variable for testing
func setupSecretsTestEnv(t *testing.T) func() {
	t.Helper()

	// Create base64-encoded master key
	encodedKey := base64.StdEncoding.EncodeToString(testMasterKeyBytes)
	oldEnv := os.Getenv("REACTORCIDE_MASTER_KEYS")

	os.Setenv("REACTORCIDE_MASTER_KEYS", "test-key:"+encodedKey)

	return func() {
		if oldEnv == "" {
			os.Unsetenv("REACTORCIDE_MASTER_KEYS")
		} else {
			os.Setenv("REACTORCIDE_MASTER_KEYS", oldEnv)
		}
	}
}

// createTestMasterKeyManager creates a MasterKeyManager for testing
func createTestMasterKeyManager(t *testing.T) *secrets.MasterKeyManager {
	t.Helper()
	cleanup := setupSecretsTestEnv(t)
	t.Cleanup(cleanup)

	mgr, err := secrets.LoadMasterKeys()
	require.NoError(t, err)
	return mgr
}

// setupSecretsTestUser creates a user with initialized secrets for testing
func setupSecretsTestUser(t *testing.T, tx *gorm.DB, keyManager *secrets.MasterKeyManager) *models.User {
	t.Helper()

	// Create a test user
	dataUtils := &DataUtils{db: tx}
	user, err := dataUtils.CreateUser(DataSetup{
		"Username": "secretstestuser",
		"Email":    "secretstest@example.com",
	})
	require.NoError(t, err)

	// Check if test-key already exists (may have been auto-generated or created by previous test setup)
	var existingKey models.MasterKey
	err = tx.Where("name = ?", "test-key").First(&existingKey).Error

	var masterKey *models.MasterKey
	if err == nil {
		// Key exists, use it
		masterKey = &existingKey
	} else {
		// Check if there's already a primary key
		var primaryCount int64
		tx.Model(&models.MasterKey{}).Where("is_primary = true").Count(&primaryCount)

		// Create the key - only set as primary if no primary exists
		masterKey = &models.MasterKey{
			Name:        "test-key",
			IsActive:    true,
			IsPrimary:   primaryCount == 0,
			Description: "Test master key",
		}
		err = tx.Create(masterKey).Error
		require.NoError(t, err)
	}

	// Initialize org secrets
	// Generate random org encryption key
	orgKey := make([]byte, 32)
	for i := range orgKey {
		orgKey[i] = byte(i + 50)
	}

	// Encrypt org key with master key
	encodedMasterKey := make([]byte, base64.URLEncoding.EncodedLen(len(testMasterKeyBytes)))
	base64.URLEncoding.Encode(encodedMasterKey, testMasterKeyBytes)

	encryptedOrgKey, err := createTestFernetToken(encodedMasterKey, orgKey)
	require.NoError(t, err)

	// Create salt
	salt := make([]byte, 32)
	for i := range salt {
		salt[i] = byte(i + 100)
	}

	// Create org encryption key entry - let database generate id
	orgEncKey := &models.OrgEncryptionKey{
		UserID:       user.UserID,
		MasterKeyID:  masterKey.KeyID,
		EncryptedKey: encryptedOrgKey,
		Salt:         salt,
	}
	err = tx.Create(orgEncKey).Error
	require.NoError(t, err)

	return user
}

// TestSecretsHandler_GetSecret tests the GetSecret handler
func TestSecretsHandler_GetSecret(t *testing.T) {
	t.Run("Missing path parameter returns 400", func(t *testing.T) {
		RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
			keyManager := createTestMasterKeyManager(t)
			handler := handlers.NewSecretsHandler(store.AppStore, keyManager)

			user := setupSecretsTestUser(t, tx, keyManager)

			req := httptest.NewRequest("GET", "/api/v1/secrets/value?key=mykey", nil)
			ctx = checkauth.SetUserContext(ctx, user)
			req = req.WithContext(ctx)

			w := httptest.NewRecorder()
			handler.GetSecret(w, req)

			assert.Equal(t, http.StatusBadRequest, w.Code)

			var resp handlers.ErrorResponse
			err := json.NewDecoder(w.Body).Decode(&resp)
			require.NoError(t, err)
			assert.Equal(t, "invalid_input", resp.Error)
		})
	})

	t.Run("Missing key parameter returns 400", func(t *testing.T) {
		RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
			keyManager := createTestMasterKeyManager(t)
			handler := handlers.NewSecretsHandler(store.AppStore, keyManager)

			user := setupSecretsTestUser(t, tx, keyManager)

			req := httptest.NewRequest("GET", "/api/v1/secrets/value?path=mypath", nil)
			ctx = checkauth.SetUserContext(ctx, user)
			req = req.WithContext(ctx)

			w := httptest.NewRecorder()
			handler.GetSecret(w, req)

			assert.Equal(t, http.StatusBadRequest, w.Code)
		})
	})

	t.Run("Non-existent secret returns 404", func(t *testing.T) {
		RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
			keyManager := createTestMasterKeyManager(t)
			handler := handlers.NewSecretsHandler(store.AppStore, keyManager)

			user := setupSecretsTestUser(t, tx, keyManager)

			req := httptest.NewRequest("GET", "/api/v1/secrets/value?path=test&key=NONEXISTENT", nil)
			ctx = checkauth.SetUserContext(ctx, user)
			req = req.WithContext(ctx)

			w := httptest.NewRecorder()
			handler.GetSecret(w, req)

			assert.Equal(t, http.StatusNotFound, w.Code)
		})
	})
}

// TestSecretsHandler_SetSecret tests the SetSecret handler
func TestSecretsHandler_SetSecret(t *testing.T) {
	t.Run("Missing path parameter returns 400", func(t *testing.T) {
		RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
			keyManager := createTestMasterKeyManager(t)
			handler := handlers.NewSecretsHandler(store.AppStore, keyManager)

			user := setupSecretsTestUser(t, tx, keyManager)

			body := bytes.NewBufferString(`{"value": "secret"}`)
			req := httptest.NewRequest("PUT", "/api/v1/secrets/value?key=mykey", body)
			ctx = checkauth.SetUserContext(ctx, user)
			req = req.WithContext(ctx)

			w := httptest.NewRecorder()
			handler.SetSecret(w, req)

			assert.Equal(t, http.StatusBadRequest, w.Code)
		})
	})

	t.Run("Invalid request body returns 400", func(t *testing.T) {
		RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
			keyManager := createTestMasterKeyManager(t)
			handler := handlers.NewSecretsHandler(store.AppStore, keyManager)

			user := setupSecretsTestUser(t, tx, keyManager)

			body := bytes.NewBufferString(`{invalid json}`)
			req := httptest.NewRequest("PUT", "/api/v1/secrets/value?path=test&key=KEY", body)
			ctx = checkauth.SetUserContext(ctx, user)
			req = req.WithContext(ctx)

			w := httptest.NewRecorder()
			handler.SetSecret(w, req)

			assert.Equal(t, http.StatusBadRequest, w.Code)

			var resp handlers.ErrorResponse
			err := json.NewDecoder(w.Body).Decode(&resp)
			require.NoError(t, err)
			assert.Equal(t, "invalid_input", resp.Error)
		})
	})

	t.Run("Invalid path returns 400", func(t *testing.T) {
		RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
			keyManager := createTestMasterKeyManager(t)
			handler := handlers.NewSecretsHandler(store.AppStore, keyManager)

			user := setupSecretsTestUser(t, tx, keyManager)

			body := bytes.NewBufferString(`{"value": "secret"}`)
			req := httptest.NewRequest("PUT", "/api/v1/secrets/value?path=../escape&key=KEY", body)
			ctx = checkauth.SetUserContext(ctx, user)
			req = req.WithContext(ctx)

			w := httptest.NewRecorder()
			handler.SetSecret(w, req)

			assert.Equal(t, http.StatusBadRequest, w.Code)
		})
	})

	t.Run("Set and get secret succeeds", func(t *testing.T) {
		RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
			keyManager := createTestMasterKeyManager(t)
			handler := handlers.NewSecretsHandler(store.AppStore, keyManager)

			user := setupSecretsTestUser(t, tx, keyManager)

			// Set a secret
			body := bytes.NewBufferString(`{"value": "my-secret-value"}`)
			req := httptest.NewRequest("PUT", "/api/v1/secrets/value?path=myapp&key=API_KEY", body)
			ctx = checkauth.SetUserContext(ctx, user)
			req = req.WithContext(ctx)

			w := httptest.NewRecorder()
			handler.SetSecret(w, req)

			assert.Equal(t, http.StatusOK, w.Code)

			// Get the secret
			req = httptest.NewRequest("GET", "/api/v1/secrets/value?path=myapp&key=API_KEY", nil)
			req = req.WithContext(ctx)

			w = httptest.NewRecorder()
			handler.GetSecret(w, req)

			assert.Equal(t, http.StatusOK, w.Code)

			var resp handlers.SecretValueResponse
			err := json.NewDecoder(w.Body).Decode(&resp)
			require.NoError(t, err)
			assert.Equal(t, "my-secret-value", resp.Value)
		})
	})
}

// TestSecretsHandler_DeleteSecret tests the DeleteSecret handler
func TestSecretsHandler_DeleteSecret(t *testing.T) {
	t.Run("Missing parameters returns 400", func(t *testing.T) {
		RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
			keyManager := createTestMasterKeyManager(t)
			handler := handlers.NewSecretsHandler(store.AppStore, keyManager)

			user := setupSecretsTestUser(t, tx, keyManager)

			req := httptest.NewRequest("DELETE", "/api/v1/secrets/value?path=test", nil)
			ctx = checkauth.SetUserContext(ctx, user)
			req = req.WithContext(ctx)

			w := httptest.NewRecorder()
			handler.DeleteSecret(w, req)

			assert.Equal(t, http.StatusBadRequest, w.Code)
		})
	})

	t.Run("Deleting non-existent secret returns 404", func(t *testing.T) {
		RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
			keyManager := createTestMasterKeyManager(t)
			handler := handlers.NewSecretsHandler(store.AppStore, keyManager)

			user := setupSecretsTestUser(t, tx, keyManager)

			req := httptest.NewRequest("DELETE", "/api/v1/secrets/value?path=nonexistent&key=MISSING", nil)
			ctx = checkauth.SetUserContext(ctx, user)
			req = req.WithContext(ctx)

			w := httptest.NewRecorder()
			handler.DeleteSecret(w, req)

			assert.Equal(t, http.StatusNotFound, w.Code)
		})
	})

	t.Run("Delete existing secret succeeds", func(t *testing.T) {
		RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
			keyManager := createTestMasterKeyManager(t)
			handler := handlers.NewSecretsHandler(store.AppStore, keyManager)

			user := setupSecretsTestUser(t, tx, keyManager)

			// First set a secret
			body := bytes.NewBufferString(`{"value": "to-be-deleted"}`)
			req := httptest.NewRequest("PUT", "/api/v1/secrets/value?path=test&key=DELETE_ME", body)
			ctx = checkauth.SetUserContext(ctx, user)
			req = req.WithContext(ctx)

			w := httptest.NewRecorder()
			handler.SetSecret(w, req)
			require.Equal(t, http.StatusOK, w.Code)

			// Now delete it
			req = httptest.NewRequest("DELETE", "/api/v1/secrets/value?path=test&key=DELETE_ME", nil)
			req = req.WithContext(ctx)

			w = httptest.NewRecorder()
			handler.DeleteSecret(w, req)

			assert.Equal(t, http.StatusOK, w.Code)

			// Verify it's gone
			req = httptest.NewRequest("GET", "/api/v1/secrets/value?path=test&key=DELETE_ME", nil)
			req = req.WithContext(ctx)

			w = httptest.NewRecorder()
			handler.GetSecret(w, req)

			assert.Equal(t, http.StatusNotFound, w.Code)
		})
	})
}

// TestSecretsHandler_ListKeys tests the ListKeys handler
func TestSecretsHandler_ListKeys(t *testing.T) {
	t.Run("Missing path parameter returns 400", func(t *testing.T) {
		RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
			keyManager := createTestMasterKeyManager(t)
			handler := handlers.NewSecretsHandler(store.AppStore, keyManager)

			user := setupSecretsTestUser(t, tx, keyManager)

			req := httptest.NewRequest("GET", "/api/v1/secrets", nil)
			ctx = checkauth.SetUserContext(ctx, user)
			req = req.WithContext(ctx)

			w := httptest.NewRecorder()
			handler.ListKeys(w, req)

			assert.Equal(t, http.StatusBadRequest, w.Code)
		})
	})

	t.Run("Empty path returns empty keys list", func(t *testing.T) {
		RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
			keyManager := createTestMasterKeyManager(t)
			handler := handlers.NewSecretsHandler(store.AppStore, keyManager)

			user := setupSecretsTestUser(t, tx, keyManager)

			req := httptest.NewRequest("GET", "/api/v1/secrets?path=emptypath", nil)
			ctx = checkauth.SetUserContext(ctx, user)
			req = req.WithContext(ctx)

			w := httptest.NewRecorder()
			handler.ListKeys(w, req)

			assert.Equal(t, http.StatusOK, w.Code)

			var resp handlers.ListKeysResponse
			err := json.NewDecoder(w.Body).Decode(&resp)
			require.NoError(t, err)
			assert.Empty(t, resp.Keys)
		})
	})

	t.Run("List keys returns existing keys", func(t *testing.T) {
		RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
			keyManager := createTestMasterKeyManager(t)
			handler := handlers.NewSecretsHandler(store.AppStore, keyManager)

			user := setupSecretsTestUser(t, tx, keyManager)

			// Create some secrets
			for _, key := range []string{"KEY1", "KEY2", "KEY3"} {
				body := bytes.NewBufferString(`{"value": "value"}`)
				req := httptest.NewRequest("PUT", "/api/v1/secrets/value?path=mypath&key="+key, body)
				ctx = checkauth.SetUserContext(ctx, user)
				req = req.WithContext(ctx)

				w := httptest.NewRecorder()
				handler.SetSecret(w, req)
				require.Equal(t, http.StatusOK, w.Code)
			}

			// List keys
			req := httptest.NewRequest("GET", "/api/v1/secrets?path=mypath", nil)
			req = req.WithContext(ctx)

			w := httptest.NewRecorder()
			handler.ListKeys(w, req)

			assert.Equal(t, http.StatusOK, w.Code)

			var resp handlers.ListKeysResponse
			err := json.NewDecoder(w.Body).Decode(&resp)
			require.NoError(t, err)
			assert.Len(t, resp.Keys, 3)
			assert.Contains(t, resp.Keys, "KEY1")
			assert.Contains(t, resp.Keys, "KEY2")
			assert.Contains(t, resp.Keys, "KEY3")
		})
	})
}

// TestSecretsHandler_ListPaths tests the ListPaths handler
func TestSecretsHandler_ListPaths(t *testing.T) {
	t.Run("User with no secrets returns empty paths list", func(t *testing.T) {
		RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
			keyManager := createTestMasterKeyManager(t)
			handler := handlers.NewSecretsHandler(store.AppStore, keyManager)

			user := setupSecretsTestUser(t, tx, keyManager)

			req := httptest.NewRequest("GET", "/api/v1/secrets/paths", nil)
			ctx = checkauth.SetUserContext(ctx, user)
			req = req.WithContext(ctx)

			w := httptest.NewRecorder()
			handler.ListPaths(w, req)

			assert.Equal(t, http.StatusOK, w.Code)

			var resp handlers.ListPathsResponse
			err := json.NewDecoder(w.Body).Decode(&resp)
			require.NoError(t, err)
			assert.Empty(t, resp.Paths)
		})
	})

	t.Run("List paths returns existing paths", func(t *testing.T) {
		RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
			keyManager := createTestMasterKeyManager(t)
			handler := handlers.NewSecretsHandler(store.AppStore, keyManager)

			user := setupSecretsTestUser(t, tx, keyManager)

			// Create secrets in different paths
			for _, path := range []string{"path1", "path2", "path3"} {
				body := bytes.NewBufferString(`{"value": "value"}`)
				req := httptest.NewRequest("PUT", "/api/v1/secrets/value?path="+path+"&key=KEY", body)
				ctx = checkauth.SetUserContext(ctx, user)
				req = req.WithContext(ctx)

				w := httptest.NewRecorder()
				handler.SetSecret(w, req)
				require.Equal(t, http.StatusOK, w.Code)
			}

			// List paths
			req := httptest.NewRequest("GET", "/api/v1/secrets/paths", nil)
			req = req.WithContext(ctx)

			w := httptest.NewRecorder()
			handler.ListPaths(w, req)

			assert.Equal(t, http.StatusOK, w.Code)

			var resp handlers.ListPathsResponse
			err := json.NewDecoder(w.Body).Decode(&resp)
			require.NoError(t, err)
			assert.Len(t, resp.Paths, 3)
			assert.Contains(t, resp.Paths, "path1")
			assert.Contains(t, resp.Paths, "path2")
			assert.Contains(t, resp.Paths, "path3")
		})
	})
}

// TestSecretsHandler_BatchGet tests the BatchGet handler
func TestSecretsHandler_BatchGet(t *testing.T) {
	t.Run("Invalid request body returns 400", func(t *testing.T) {
		RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
			keyManager := createTestMasterKeyManager(t)
			handler := handlers.NewSecretsHandler(store.AppStore, keyManager)

			user := setupSecretsTestUser(t, tx, keyManager)

			body := bytes.NewBufferString(`{invalid}`)
			req := httptest.NewRequest("POST", "/api/v1/secrets/batch/get", body)
			ctx = checkauth.SetUserContext(ctx, user)
			req = req.WithContext(ctx)

			w := httptest.NewRecorder()
			handler.BatchGet(w, req)

			assert.Equal(t, http.StatusBadRequest, w.Code)
		})
	})

	t.Run("Empty refs returns empty map", func(t *testing.T) {
		RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
			keyManager := createTestMasterKeyManager(t)
			handler := handlers.NewSecretsHandler(store.AppStore, keyManager)

			user := setupSecretsTestUser(t, tx, keyManager)

			body := bytes.NewBufferString(`{"refs": []}`)
			req := httptest.NewRequest("POST", "/api/v1/secrets/batch/get", body)
			ctx = checkauth.SetUserContext(ctx, user)
			req = req.WithContext(ctx)

			w := httptest.NewRecorder()
			handler.BatchGet(w, req)

			assert.Equal(t, http.StatusOK, w.Code)

			var resp handlers.BatchGetResponse
			err := json.NewDecoder(w.Body).Decode(&resp)
			require.NoError(t, err)
			assert.Empty(t, resp.Secrets)
		})
	})

	t.Run("Batch get returns multiple secrets", func(t *testing.T) {
		RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
			keyManager := createTestMasterKeyManager(t)
			handler := handlers.NewSecretsHandler(store.AppStore, keyManager)

			user := setupSecretsTestUser(t, tx, keyManager)

			// Create some secrets
			secrets := []struct {
				path, key, value string
			}{
				{"app1", "KEY1", "value1"},
				{"app1", "KEY2", "value2"},
				{"app2", "KEY1", "value3"},
			}

			for _, s := range secrets {
				body := bytes.NewBufferString(`{"value": "` + s.value + `"}`)
				req := httptest.NewRequest("PUT", "/api/v1/secrets/value?path="+s.path+"&key="+s.key, body)
				ctx = checkauth.SetUserContext(ctx, user)
				req = req.WithContext(ctx)

				w := httptest.NewRecorder()
				handler.SetSecret(w, req)
				require.Equal(t, http.StatusOK, w.Code)
			}

			// Batch get
			body := bytes.NewBufferString(`{"refs": [{"path": "app1", "key": "KEY1"}, {"path": "app1", "key": "KEY2"}, {"path": "app2", "key": "KEY1"}]}`)
			req := httptest.NewRequest("POST", "/api/v1/secrets/batch/get", body)
			req = req.WithContext(ctx)

			w := httptest.NewRecorder()
			handler.BatchGet(w, req)

			assert.Equal(t, http.StatusOK, w.Code)

			var resp handlers.BatchGetResponse
			err := json.NewDecoder(w.Body).Decode(&resp)
			require.NoError(t, err)
			assert.Len(t, resp.Secrets, 3)
			assert.Equal(t, "value1", resp.Secrets["app1:KEY1"])
			assert.Equal(t, "value2", resp.Secrets["app1:KEY2"])
			assert.Equal(t, "value3", resp.Secrets["app2:KEY1"])
		})
	})
}

// TestSecretsHandler_BatchSet tests the BatchSet handler
func TestSecretsHandler_BatchSet(t *testing.T) {
	t.Run("Invalid request body returns 400", func(t *testing.T) {
		RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
			keyManager := createTestMasterKeyManager(t)
			handler := handlers.NewSecretsHandler(store.AppStore, keyManager)

			user := setupSecretsTestUser(t, tx, keyManager)

			body := bytes.NewBufferString(`{invalid}`)
			req := httptest.NewRequest("POST", "/api/v1/secrets/batch/set", body)
			ctx = checkauth.SetUserContext(ctx, user)
			req = req.WithContext(ctx)

			w := httptest.NewRecorder()
			handler.BatchSet(w, req)

			assert.Equal(t, http.StatusBadRequest, w.Code)
		})
	})

	t.Run("Empty secrets list succeeds", func(t *testing.T) {
		RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
			keyManager := createTestMasterKeyManager(t)
			handler := handlers.NewSecretsHandler(store.AppStore, keyManager)

			user := setupSecretsTestUser(t, tx, keyManager)

			body := bytes.NewBufferString(`{"secrets": []}`)
			req := httptest.NewRequest("POST", "/api/v1/secrets/batch/set", body)
			ctx = checkauth.SetUserContext(ctx, user)
			req = req.WithContext(ctx)

			w := httptest.NewRecorder()
			handler.BatchSet(w, req)

			assert.Equal(t, http.StatusOK, w.Code)
		})
	})

	t.Run("Invalid path in batch returns 400", func(t *testing.T) {
		RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
			keyManager := createTestMasterKeyManager(t)
			handler := handlers.NewSecretsHandler(store.AppStore, keyManager)

			user := setupSecretsTestUser(t, tx, keyManager)

			body := bytes.NewBufferString(`{"secrets": [{"path": "../escape", "key": "KEY", "value": "val"}]}`)
			req := httptest.NewRequest("POST", "/api/v1/secrets/batch/set", body)
			ctx = checkauth.SetUserContext(ctx, user)
			req = req.WithContext(ctx)

			w := httptest.NewRecorder()
			handler.BatchSet(w, req)

			assert.Equal(t, http.StatusBadRequest, w.Code)
		})
	})

	t.Run("Batch set creates multiple secrets", func(t *testing.T) {
		RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
			keyManager := createTestMasterKeyManager(t)
			handler := handlers.NewSecretsHandler(store.AppStore, keyManager)

			user := setupSecretsTestUser(t, tx, keyManager)

			body := bytes.NewBufferString(`{"secrets": [
				{"path": "batch", "key": "KEY1", "value": "val1"},
				{"path": "batch", "key": "KEY2", "value": "val2"},
				{"path": "batch", "key": "KEY3", "value": "val3"}
			]}`)
			req := httptest.NewRequest("POST", "/api/v1/secrets/batch/set", body)
			ctx = checkauth.SetUserContext(ctx, user)
			req = req.WithContext(ctx)

			w := httptest.NewRecorder()
			handler.BatchSet(w, req)

			assert.Equal(t, http.StatusOK, w.Code)

			// Verify all secrets were created
			for i := 1; i <= 3; i++ {
				req = httptest.NewRequest("GET", "/api/v1/secrets/value?path=batch&key=KEY"+string(rune('0'+i)), nil)
				req = req.WithContext(ctx)

				w = httptest.NewRecorder()
				handler.GetSecret(w, req)

				assert.Equal(t, http.StatusOK, w.Code)
			}
		})
	})
}

// TestSecretsHandler_InitSecrets tests the InitSecrets handler
func TestSecretsHandler_InitSecrets(t *testing.T) {
	t.Run("Unauthenticated request returns 401", func(t *testing.T) {
		RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
			keyManager := createTestMasterKeyManager(t)
			handler := handlers.NewSecretsHandler(store.AppStore, keyManager)

			req := httptest.NewRequest("POST", "/api/v1/secrets/init", nil)
			req = req.WithContext(ctx)
			// No user in context

			w := httptest.NewRecorder()
			handler.InitSecrets(w, req)

			assert.Equal(t, http.StatusUnauthorized, w.Code)
		})
	})

	t.Run("Already initialized returns 409", func(t *testing.T) {
		RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
			keyManager := createTestMasterKeyManager(t)
			handler := handlers.NewSecretsHandler(store.AppStore, keyManager)

			// Create user that's already initialized
			user := setupSecretsTestUser(t, tx, keyManager)
			// Mark as initialized
			err := tx.Model(&models.User{}).
				Where("user_id = ?", user.UserID).
				Update("secrets_initialized_at", tx.NowFunc()).Error
			require.NoError(t, err)

			req := httptest.NewRequest("POST", "/api/v1/secrets/init", nil)
			ctx = checkauth.SetUserContext(ctx, user)
			req = req.WithContext(ctx)

			w := httptest.NewRecorder()
			handler.InitSecrets(w, req)

			assert.Equal(t, http.StatusConflict, w.Code)

			var resp handlers.ErrorResponse
			err = json.NewDecoder(w.Body).Decode(&resp)
			require.NoError(t, err)
			assert.Equal(t, "already_exists", resp.Error)
		})
	})

	t.Run("Initialize secrets succeeds", func(t *testing.T) {
		RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
			keyManager := createTestMasterKeyManager(t)
			handler := handlers.NewSecretsHandler(store.AppStore, keyManager)

			// Create a user without initialized secrets
			dataUtils := &DataUtils{db: tx}
			user, err := dataUtils.CreateUser(DataSetup{
				"Username": "newuser",
				"Email":    "new@example.com",
			})
			require.NoError(t, err)

			// Deactivate any master keys that aren't "test-key" and clear their primary flag
			// to avoid InitializeOrgSecrets failing on keys not in our test keyManager
			// and to avoid constraint violations when setting test-key as primary
			err = tx.Model(&models.MasterKey{}).
				Where("name != ?", "test-key").
				Updates(map[string]interface{}{"is_active": false, "is_primary": false}).Error
			require.NoError(t, err)

			// Check if test-key already exists
			var existingKey models.MasterKey
			err = tx.Where("name = ?", "test-key").First(&existingKey).Error
			if err != nil {
				// Register master key in database
				masterKey := &models.MasterKey{
					Name:        "test-key",
					IsActive:    true,
					IsPrimary:   true,
					Description: "Test master key",
				}
				err = tx.Create(masterKey).Error
				require.NoError(t, err)
			} else {
				// Ensure existing key is active and primary
				err = tx.Model(&existingKey).Updates(map[string]interface{}{
					"is_active":  true,
					"is_primary": true,
				}).Error
				require.NoError(t, err)
			}

			req := httptest.NewRequest("POST", "/api/v1/secrets/init", nil)
			ctx = checkauth.SetUserContext(ctx, user)
			req = req.WithContext(ctx)

			w := httptest.NewRecorder()
			handler.InitSecrets(w, req)

			assert.Equal(t, http.StatusCreated, w.Code)

			var resp handlers.InitResponse
			err = json.NewDecoder(w.Body).Decode(&resp)
			require.NoError(t, err)
			assert.Equal(t, "initialized", resp.Status)
			assert.Equal(t, user.UserID, resp.OrgID)
		})
	})
}

// TestSecretsHandler_AdminEndpoints tests admin-only endpoints
func TestSecretsHandler_AdminEndpoints(t *testing.T) {
	t.Run("CreateMasterKey with empty name returns 400", func(t *testing.T) {
		RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
			keyManager := createTestMasterKeyManager(t)
			handler := handlers.NewSecretsHandler(store.AppStore, keyManager)

			body := bytes.NewBufferString(`{"name": ""}`)
			req := httptest.NewRequest("POST", "/api/v1/admin/secrets/master-keys", body)
			req = req.WithContext(ctx)

			w := httptest.NewRecorder()
			handler.CreateMasterKey(w, req)

			assert.Equal(t, http.StatusBadRequest, w.Code)
		})
	})

	t.Run("CreateMasterKey with invalid body returns 400", func(t *testing.T) {
		RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
			keyManager := createTestMasterKeyManager(t)
			handler := handlers.NewSecretsHandler(store.AppStore, keyManager)

			body := bytes.NewBufferString(`{invalid}`)
			req := httptest.NewRequest("POST", "/api/v1/admin/secrets/master-keys", body)
			req = req.WithContext(ctx)

			w := httptest.NewRecorder()
			handler.CreateMasterKey(w, req)

			assert.Equal(t, http.StatusBadRequest, w.Code)
		})
	})

	t.Run("CreateMasterKey with valid name succeeds", func(t *testing.T) {
		RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
			keyManager := createTestMasterKeyManager(t)
			handler := handlers.NewSecretsHandler(store.AppStore, keyManager)

			// Use the "test-key" name which matches the key in the env var
			// Note: if no key exists in DB yet, this will create it
			body := bytes.NewBufferString(`{"name": "test-key", "description": "Test key"}`)
			req := httptest.NewRequest("POST", "/api/v1/admin/secrets/master-keys", body)
			req = req.WithContext(ctx)

			w := httptest.NewRecorder()
			handler.CreateMasterKey(w, req)

			// Either 201 (created) or 409 (already exists) is acceptable
			// since another test may have created it in the same transaction
			assert.True(t, w.Code == http.StatusCreated || w.Code == http.StatusConflict,
				"Expected 201 or 409, got %d", w.Code)

			if w.Code == http.StatusCreated {
				var resp handlers.MasterKeyResponse
				err := json.NewDecoder(w.Body).Decode(&resp)
				require.NoError(t, err)
				assert.Equal(t, "test-key", resp.Name)
				assert.True(t, resp.IsActive)
			}
		})
	})

	t.Run("RotateMasterKey with empty key name returns 400", func(t *testing.T) {
		RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
			keyManager := createTestMasterKeyManager(t)
			handler := handlers.NewSecretsHandler(store.AppStore, keyManager)

			req := httptest.NewRequest("POST", "/api/v1/admin/secrets/master-keys//rotate", nil)
			req = req.WithContext(ctx)
			// No key_name in context

			w := httptest.NewRecorder()
			handler.RotateMasterKey(w, req)

			assert.Equal(t, http.StatusBadRequest, w.Code)
		})
	})

	t.Run("DecommissionMasterKey with empty key name returns 400", func(t *testing.T) {
		RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
			keyManager := createTestMasterKeyManager(t)
			handler := handlers.NewSecretsHandler(store.AppStore, keyManager)

			req := httptest.NewRequest("DELETE", "/api/v1/admin/secrets/master-keys/", nil)
			req = req.WithContext(ctx)
			// No key_name in context

			w := httptest.NewRecorder()
			handler.DecommissionMasterKey(w, req)

			assert.Equal(t, http.StatusBadRequest, w.Code)
		})
	})

	t.Run("ListMasterKeys returns keys", func(t *testing.T) {
		RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
			keyManager := createTestMasterKeyManager(t)
			handler := handlers.NewSecretsHandler(store.AppStore, keyManager)

			// Create a master key in the database for the test
			masterKey := &models.MasterKey{
				Name:        "list-test-key",
				IsActive:    true,
				IsPrimary:   false,
				Description: "Key for list test",
			}
			err := tx.Create(masterKey).Error
			require.NoError(t, err)

			req := httptest.NewRequest("GET", "/api/v1/admin/secrets/master-keys", nil)
			req = req.WithContext(ctx)

			w := httptest.NewRecorder()
			handler.ListMasterKeys(w, req)

			assert.Equal(t, http.StatusOK, w.Code)

			var resp handlers.MasterKeysListResponse
			err = json.NewDecoder(w.Body).Decode(&resp)
			require.NoError(t, err)
			assert.NotNil(t, resp.MasterKeys)
			// Should have at least one key (we created one above)
			assert.True(t, len(resp.MasterKeys) > 0, "Should have at least one master key")
		})
	})

	t.Run("SyncPrimary succeeds", func(t *testing.T) {
		RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
			keyManager := createTestMasterKeyManager(t)
			handler := handlers.NewSecretsHandler(store.AppStore, keyManager)

			// Ensure test-key exists in the database (may already exist from auto-generation)
			var existingKey models.MasterKey
			err := tx.Where("name = ?", "test-key").First(&existingKey).Error
			if err != nil {
				// Create the key if it doesn't exist
				masterKey := &models.MasterKey{
					Name:        "test-key",
					IsActive:    true,
					IsPrimary:   false,
					Description: "Test key",
				}
				err = tx.Create(masterKey).Error
				require.NoError(t, err)
			}

			req := httptest.NewRequest("POST", "/api/v1/admin/secrets/sync-primary", nil)
			req = req.WithContext(ctx)

			w := httptest.NewRecorder()
			handler.SyncPrimary(w, req)

			assert.Equal(t, http.StatusOK, w.Code)

			var resp map[string]string
			err = json.NewDecoder(w.Body).Decode(&resp)
			require.NoError(t, err)
			assert.Equal(t, "synced", resp["status"])
		})
	})
}
