package test

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/checkauth"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/middleware"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// AuthErrorResponse represents the expected error response structure
type AuthErrorResponse struct {
	Error   string `json:"error"`
	Message string `json:"message"`
}

// TestAuthMiddleware tests the API token authentication middleware
func TestAuthMiddleware(t *testing.T) {
	t.Run("Valid Token Authentication", func(t *testing.T) {
		RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
			testValidTokenAuthentication(t, ctx, tx)
		})
	})

	t.Run("Missing Authorization Header", func(t *testing.T) {
		RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
			testMissingAuthorizationHeader(t, ctx, tx)
		})
	})

	t.Run("Invalid Authorization Header Format", func(t *testing.T) {
		RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
			testInvalidAuthorizationHeaderFormat(t, ctx, tx)
		})
	})

	t.Run("Empty Token", func(t *testing.T) {
		RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
			testEmptyToken(t, ctx, tx)
		})
	})

	t.Run("Invalid Token", func(t *testing.T) {
		RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
			testInvalidToken(t, ctx, tx)
		})
	})

	t.Run("Expired Token", func(t *testing.T) {
		RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
			testExpiredToken(t, ctx, tx)
		})
	})

	t.Run("Inactive Token", func(t *testing.T) {
		RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
			testInactiveToken(t, ctx, tx)
		})
	})

	t.Run("User Context Population", func(t *testing.T) {
		RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
			testUserContextPopulation(t, ctx, tx)
		})
	})
}

// testValidTokenAuthentication tests successful authentication with a valid token
func testValidTokenAuthentication(t *testing.T, ctx context.Context, tx *gorm.DB) {
	// Create test data
	dataUtils := &DataUtils{db: tx}
	user, err := dataUtils.CreateUser(DataSetup{
		"Username": "testuser",
		"Email":    "test@example.com",
	})
	require.NoError(t, err)

	// Generate a raw token
	rawToken := make([]byte, 32)
	_, err = rand.Read(rawToken)
	require.NoError(t, err)
	tokenString := string(rawToken)

	// Create API token with proper hash
	tokenHash := checkauth.HashAPIToken(tokenString)
	apiToken, err := dataUtils.CreateAPIToken(DataSetup{
		"UserID":    user.UserID,
		"Name":      "Test Token",
		"TokenHash": tokenHash,
		"IsActive":  true,
	})
	require.NoError(t, err)

	// Create test handler that inspects the context
	var capturedUser *models.User
	var capturedVerified bool
	testHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedUser = checkauth.GetUserFromContext(r.Context())
		capturedVerified = checkauth.GetVerifiedFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok"}`))
	})

	// Create middleware
	authMiddleware := middleware.APITokenMiddleware(store.AppStore)
	handler := authMiddleware(testHandler)

	// Create request with valid token
	req := httptest.NewRequest("GET", "/test", nil)
	req.Header.Set("Authorization", "Bearer "+tokenString)
	req = req.WithContext(ctx)

	// Execute request
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	// Assertions
	assert.Equal(t, http.StatusOK, w.Code)
	assert.NotNil(t, capturedUser)
	assert.Equal(t, user.UserID, capturedUser.UserID)
	assert.Equal(t, user.Username, capturedUser.Username)
	assert.Equal(t, user.Email, capturedUser.Email)
	assert.True(t, capturedVerified)

	// Verify response
	var response map[string]string
	err = json.Unmarshal(w.Body.Bytes(), &response)
	require.NoError(t, err)
	assert.Equal(t, "ok", response["status"])

	// Verify token still exists in database (not deleted)
	var dbToken models.APIToken
	err = tx.Where("token_id = ?", apiToken.TokenID).First(&dbToken).Error
	require.NoError(t, err)
	assert.Equal(t, apiToken.TokenID, dbToken.TokenID)
}

// testMissingAuthorizationHeader tests behavior when Authorization header is missing
func testMissingAuthorizationHeader(t *testing.T, ctx context.Context, tx *gorm.DB) {
	// Create test handler
	testHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("Handler should not be called when auth fails")
	})

	// Create middleware
	authMiddleware := middleware.APITokenMiddleware(store.AppStore)
	handler := authMiddleware(testHandler)

	// Create request without Authorization header
	req := httptest.NewRequest("GET", "/test", nil)
	req = req.WithContext(ctx)

	// Execute request
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	// Assertions
	assert.Equal(t, http.StatusUnauthorized, w.Code)
	assert.Equal(t, "application/json", w.Header().Get("Content-Type"))

	// Verify error response
	var errorResponse AuthErrorResponse
	err := json.Unmarshal(w.Body.Bytes(), &errorResponse)
	require.NoError(t, err)
	assert.Equal(t, "unauthorized", errorResponse.Error)
	assert.Equal(t, "Missing Authorization header", errorResponse.Message)
}

// testInvalidAuthorizationHeaderFormat tests behavior with malformed Authorization header
func testInvalidAuthorizationHeaderFormat(t *testing.T, ctx context.Context, tx *gorm.DB) {
	testCases := []struct {
		name   string
		header string
	}{
		{"No Bearer prefix", "token123"},
		{"Wrong prefix", "Basic token123"},
		{"Bearer with no space", "Bearertoken123"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Create test handler
			testHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				t.Fatal("Handler should not be called when auth fails")
			})

			// Create middleware
			authMiddleware := middleware.APITokenMiddleware(store.AppStore)
			handler := authMiddleware(testHandler)

			// Create request with invalid Authorization header
			req := httptest.NewRequest("GET", "/test", nil)
			req.Header.Set("Authorization", tc.header)
			req = req.WithContext(ctx)

			// Execute request
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, req)

			// Assertions
			assert.Equal(t, http.StatusUnauthorized, w.Code)
			assert.Equal(t, "application/json", w.Header().Get("Content-Type"))

			// Verify error response
			var errorResponse AuthErrorResponse
			err := json.Unmarshal(w.Body.Bytes(), &errorResponse)
			require.NoError(t, err)
			assert.Equal(t, "unauthorized", errorResponse.Error)
			assert.Contains(t, errorResponse.Message, "Invalid Authorization header format")
		})
	}
}

// testEmptyToken tests behavior when token is empty
func testEmptyToken(t *testing.T, ctx context.Context, tx *gorm.DB) {
	// Create test handler
	testHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("Handler should not be called when auth fails")
	})

	// Create middleware
	authMiddleware := middleware.APITokenMiddleware(store.AppStore)
	handler := authMiddleware(testHandler)

	// Create request with empty token
	req := httptest.NewRequest("GET", "/test", nil)
	req.Header.Set("Authorization", "Bearer ")
	req = req.WithContext(ctx)

	// Execute request
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	// Assertions
	assert.Equal(t, http.StatusUnauthorized, w.Code)
	assert.Equal(t, "application/json", w.Header().Get("Content-Type"))

	// Verify error response
	var errorResponse AuthErrorResponse
	err := json.Unmarshal(w.Body.Bytes(), &errorResponse)
	require.NoError(t, err)
	assert.Equal(t, "unauthorized", errorResponse.Error)
	assert.Equal(t, "Empty token", errorResponse.Message)
}

// testInvalidToken tests behavior with non-existent or invalid token
func testInvalidToken(t *testing.T, ctx context.Context, tx *gorm.DB) {
	testCases := []string{
		"nonexistenttoken123",
		"invalid-token-format",
		"a very long token that definitely does not exist in the database and should return unauthorized",
	}

	for i, invalidToken := range testCases {
		t.Run(fmt.Sprintf("Invalid token %d", i+1), func(t *testing.T) {
			// Create test handler
			testHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				t.Fatal("Handler should not be called when auth fails")
			})

			// Create middleware
			authMiddleware := middleware.APITokenMiddleware(store.AppStore)
			handler := authMiddleware(testHandler)

			// Create request with invalid token
			req := httptest.NewRequest("GET", "/test", nil)
			req.Header.Set("Authorization", "Bearer "+invalidToken)
			req = req.WithContext(ctx)

			// Execute request
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, req)

			// Assertions
			assert.Equal(t, http.StatusUnauthorized, w.Code)
			assert.Equal(t, "application/json", w.Header().Get("Content-Type"))

			// Verify error response
			var errorResponse AuthErrorResponse
			err := json.Unmarshal(w.Body.Bytes(), &errorResponse)
			require.NoError(t, err)
			assert.Equal(t, "unauthorized", errorResponse.Error)
			assert.Equal(t, "Invalid or expired token", errorResponse.Message)
		})
	}
}

// testExpiredToken tests behavior with expired token
func testExpiredToken(t *testing.T, ctx context.Context, tx *gorm.DB) {
	// Create test data
	dataUtils := &DataUtils{db: tx}
	user, err := dataUtils.CreateUser(DataSetup{
		"Username": "testuser",
		"Email":    "test@example.com",
	})
	require.NoError(t, err)

	// Generate a raw token
	rawToken := make([]byte, 32)
	_, err = rand.Read(rawToken)
	require.NoError(t, err)
	tokenString := string(rawToken)

	// Create expired API token
	tokenHash := checkauth.HashAPIToken(tokenString)
	expiredTime := time.Now().UTC().Add(-24 * time.Hour) // Expired 24 hours ago
	_, err = dataUtils.CreateAPIToken(DataSetup{
		"UserID":    user.UserID,
		"Name":      "Expired Token",
		"TokenHash": tokenHash,
		"IsActive":  true,
		"ExpiresAt": expiredTime,
	})
	require.NoError(t, err)

	// Create test handler
	testHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("Handler should not be called when auth fails")
	})

	// Create middleware
	authMiddleware := middleware.APITokenMiddleware(store.AppStore)
	handler := authMiddleware(testHandler)

	// Create request with expired token
	req := httptest.NewRequest("GET", "/test", nil)
	req.Header.Set("Authorization", "Bearer "+tokenString)
	req = req.WithContext(ctx)

	// Execute request
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	// Assertions
	assert.Equal(t, http.StatusUnauthorized, w.Code)
	assert.Equal(t, "application/json", w.Header().Get("Content-Type"))

	// Verify error response
	var errorResponse AuthErrorResponse
	err = json.Unmarshal(w.Body.Bytes(), &errorResponse)
	require.NoError(t, err)
	assert.Equal(t, "unauthorized", errorResponse.Error)
	assert.Equal(t, "Invalid or expired token", errorResponse.Message)
}

// testInactiveToken tests behavior with inactive token
func testInactiveToken(t *testing.T, ctx context.Context, tx *gorm.DB) {
	// Create test data
	dataUtils := &DataUtils{db: tx}
	user, err := dataUtils.CreateUser(DataSetup{
		"Username": "testuser",
		"Email":    "test@example.com",
	})
	require.NoError(t, err)

	// Generate a raw token
	rawToken := make([]byte, 32)
	_, err = rand.Read(rawToken)
	require.NoError(t, err)
	tokenString := string(rawToken)

	// Create API token first (will be active by default)
	apiToken := &models.APIToken{
		UserID:    user.UserID,
		TokenHash: checkauth.HashAPIToken(tokenString),
		Name:      "Inactive Token",
	}

	err = tx.Create(apiToken).Error
	require.NoError(t, err)

	// Then update it to be inactive (this explicitly overrides the default)
	err = tx.Model(apiToken).Update("is_active", false).Error
	require.NoError(t, err)

	// Reload the token to verify
	err = tx.First(apiToken, "token_id = ?", apiToken.TokenID).Error
	require.NoError(t, err)
	require.False(t, apiToken.IsActive, "Token should be inactive after update")

	// Create test handler
	testHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("Handler should not be called when auth fails")
	})

	// Create middleware
	authMiddleware := middleware.APITokenMiddleware(store.AppStore)
	handler := authMiddleware(testHandler)

	// Create request with inactive token
	req := httptest.NewRequest("GET", "/test", nil)
	req.Header.Set("Authorization", "Bearer "+tokenString)
	req = req.WithContext(ctx)

	// Execute request
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	// Assertions
	assert.Equal(t, http.StatusUnauthorized, w.Code)
	assert.Equal(t, "application/json", w.Header().Get("Content-Type"))

	// Verify error response
	var errorResponse AuthErrorResponse
	err = json.Unmarshal(w.Body.Bytes(), &errorResponse)
	require.NoError(t, err)
	assert.Equal(t, "unauthorized", errorResponse.Error)
	assert.Equal(t, "Invalid or expired token", errorResponse.Message)
}

// testUserContextPopulation tests that user context is properly populated
func testUserContextPopulation(t *testing.T, ctx context.Context, tx *gorm.DB) {
	// Create test data
	dataUtils := &DataUtils{db: tx}
	user, err := dataUtils.CreateUser(DataSetup{
		"Username": "contextuser",
		"Email":    "context@example.com",
		"Roles":    []string{"admin", "user"},
	})
	require.NoError(t, err)

	// Generate a raw token
	rawToken := make([]byte, 32)
	_, err = rand.Read(rawToken)
	require.NoError(t, err)
	tokenString := string(rawToken)

	// Create API token
	tokenHash := checkauth.HashAPIToken(tokenString)
	_, err = dataUtils.CreateAPIToken(DataSetup{
		"UserID":    user.UserID,
		"Name":      "Context Test Token",
		"TokenHash": tokenHash,
		"IsActive":  true,
	})
	require.NoError(t, err)

	// Create test handler that verifies context
	testHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify user context
		contextUser := checkauth.GetUserFromContext(r.Context())
		require.NotNil(t, contextUser, "User should be in context")
		assert.Equal(t, user.UserID, contextUser.UserID)
		assert.Equal(t, user.Username, contextUser.Username)
		assert.Equal(t, user.Email, contextUser.Email)
		assert.Equal(t, user.Roles, contextUser.Roles)

		// Verify verification status
		verified := checkauth.GetVerifiedFromContext(r.Context())
		assert.True(t, verified, "Request should be verified")

		// Return success response
		w.WriteHeader(http.StatusOK)
		response := map[string]interface{}{
			"user_id":  contextUser.UserID,
			"username": contextUser.Username,
			"email":    contextUser.Email,
			"verified": verified,
		}
		json.NewEncoder(w).Encode(response)
	})

	// Create middleware
	authMiddleware := middleware.APITokenMiddleware(store.AppStore)
	handler := authMiddleware(testHandler)

	// Create request with valid token
	req := httptest.NewRequest("GET", "/test", nil)
	req.Header.Set("Authorization", "Bearer "+tokenString)
	req = req.WithContext(ctx)

	// Execute request
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	// Assertions
	assert.Equal(t, http.StatusOK, w.Code)

	// Verify response contains user information
	var response map[string]interface{}
	err = json.Unmarshal(w.Body.Bytes(), &response)
	require.NoError(t, err)
	assert.Equal(t, user.UserID, response["user_id"])
	assert.Equal(t, user.Username, response["username"])
	assert.Equal(t, user.Email, response["email"])
	assert.Equal(t, true, response["verified"])
}

// TestTokenHashValidation tests that token hashing and validation work correctly
func TestTokenHashValidation(t *testing.T) {
	t.Run("Valid Token Hash Validation", func(t *testing.T) {
		tokenString := "test-token-123"
		hash := checkauth.HashAPIToken(tokenString)

		// Validate the token against its hash
		isValid := checkauth.ValidateAPIToken(tokenString, hash)
		assert.True(t, isValid, "Token should validate against its own hash")
	})

	t.Run("Invalid Token Hash Validation", func(t *testing.T) {
		tokenString := "test-token-123"
		wrongToken := "wrong-token-456"
		hash := checkauth.HashAPIToken(tokenString)

		// Validate wrong token against hash
		isValid := checkauth.ValidateAPIToken(wrongToken, hash)
		assert.False(t, isValid, "Wrong token should not validate")
	})

	t.Run("Empty Token Hash Validation", func(t *testing.T) {
		tokenString := "test-token-123"
		hash := checkauth.HashAPIToken(tokenString)

		// Validate empty token against hash
		isValid := checkauth.ValidateAPIToken("", hash)
		assert.False(t, isValid, "Empty token should not validate")
	})

	t.Run("Hash Consistency", func(t *testing.T) {
		tokenString := "test-token-123"
		hash1 := checkauth.HashAPIToken(tokenString)
		hash2 := checkauth.HashAPIToken(tokenString)

		// Hashes should be identical for same input
		assert.Equal(t, hash1, hash2, "Hash should be consistent for same input")
	})

	t.Run("Different Tokens Different Hashes", func(t *testing.T) {
		token1 := "test-token-123"
		token2 := "test-token-456"
		hash1 := checkauth.HashAPIToken(token1)
		hash2 := checkauth.HashAPIToken(token2)

		// Different tokens should produce different hashes
		assert.NotEqual(t, hash1, hash2, "Different tokens should produce different hashes")
	})
}

// TestMiddlewareChaining tests that the auth middleware works properly in a chain
func TestMiddlewareChaining(t *testing.T) {
	t.Run("Auth Middleware with Transaction Middleware", func(t *testing.T) {
		RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
			// Create test data
			dataUtils := &DataUtils{db: tx}
			user, err := dataUtils.CreateUser(DataSetup{
				"Username": "chainuser",
				"Email":    "chain@example.com",
			})
			require.NoError(t, err)

			// Generate a raw token
			rawToken := make([]byte, 32)
			_, err = rand.Read(rawToken)
			require.NoError(t, err)
			tokenString := string(rawToken)

			// Create API token using direct method to avoid DataUtils boolean issues
			tokenHash := checkauth.HashAPIToken(tokenString)
			apiToken := &models.APIToken{
				UserID:    user.UserID,
				TokenHash: tokenHash,
				Name:      "Chain Test Token",
				IsActive:  true,
			}
			err = tx.Create(apiToken).Error
			require.NoError(t, err)

			// Reload from database to verify it was stored correctly
			var dbToken models.APIToken
			err = tx.First(&dbToken, "token_id = ?", apiToken.TokenID).Error
			require.NoError(t, err)
			require.True(t, dbToken.IsActive, "Token should be active in database")

			// Create final handler
			finalHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				// Verify both transaction and auth context
				contextUser := checkauth.GetUserFromContext(r.Context())
				verified := checkauth.GetVerifiedFromContext(r.Context())

				assert.NotNil(t, contextUser, "User should be in context")
				assert.True(t, verified, "Request should be verified")

				w.WriteHeader(http.StatusOK)
				w.Write([]byte(`{"status":"success"}`))
			})

			// Create middleware chain: Auth -> Handler (don't use transaction middleware in test)
			authHandler := middleware.APITokenMiddleware(store.AppStore)(finalHandler)

			// Create request with valid token and use the test transaction context
			req := httptest.NewRequest("GET", "/test", nil)
			req.Header.Set("Authorization", "Bearer "+tokenString)
			req = req.WithContext(ctx) // Use the transaction context

			// Execute request
			w := httptest.NewRecorder()
			authHandler.ServeHTTP(w, req)

			// Assertions
			assert.Equal(t, http.StatusOK, w.Code)

			var response map[string]string
			err = json.Unmarshal(w.Body.Bytes(), &response)
			require.NoError(t, err)
			assert.Equal(t, "success", response["status"])
		})
	})
}
