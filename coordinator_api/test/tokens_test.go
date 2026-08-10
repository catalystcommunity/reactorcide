package test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/checkauth"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/handlers"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/postgres_store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/tokencaps"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// TokenResponse represents the expected token response structure
type TokenResponse struct {
	TokenID    string `json:"token_id"`
	Name       string `json:"name"`
	IsActive   bool   `json:"is_active"`
	ExpiresAt  string `json:"expires_at,omitempty"`
	LastUsedAt string `json:"last_used_at,omitempty"`
	CreatedAt  string `json:"created_at"`
	UpdatedAt  string `json:"updated_at"`
}

// CreateTokenRequest represents the request payload for creating a token
type CreateTokenRequest struct {
	Name      string `json:"name"`
	ExpiresAt string `json:"expires_at,omitempty"`
}

// CreateTokenResponse represents the response when creating a token (includes the token value)
type CreateTokenResponse struct {
	TokenResponse
	Token string `json:"token"`
}

// ListTokensResponse represents the response for listing tokens
type ListTokensResponse struct {
	Tokens []TokenResponse `json:"tokens"`
	Total  int             `json:"total"`
	Limit  int             `json:"limit"`
	Offset int             `json:"offset"`
}

// TestTokensAPI tests the tokens API endpoints with authentication
func TestTokensAPI(t *testing.T) {
	t.Run("POST /api/v1/tokens creates a token", func(t *testing.T) {
		RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
			mux := GetTestMux()
			dataUtils := &DataUtils{db: tx}

			// Create a test admin user (tokens require admin privileges)
			user, err := dataUtils.CreateUser(DataSetup{
				"Username": "admin",
				"Email":    "admin@example.com",
				"Roles":    []string{"admin"},
			})
			require.NoError(t, err)

			// Create auth header
			authHeader, err := createTokenAuthHeader(ctx, tx, user.UserID)
			require.NoError(t, err)
			tokenRequest := CreateTokenRequest{
				Name: "Test API Token",
			}

			jsonData, err := json.Marshal(tokenRequest)
			require.NoError(t, err)

			req, err := http.NewRequestWithContext(ctx, "POST", "/api/v1/tokens", bytes.NewBuffer(jsonData))
			require.NoError(t, err)
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", authHeader)

			rr := httptest.NewRecorder()
			mux.ServeHTTP(rr, req)

			if rr.Code != http.StatusCreated {
				t.Logf("Expected 201, got %d. Response body: %s", rr.Code, rr.Body.String())
			}
			assert.Equal(t, http.StatusCreated, rr.Code)

			var response CreateTokenResponse
			err = json.Unmarshal(rr.Body.Bytes(), &response)
			require.NoError(t, err)

			assert.Equal(t, "Test API Token", response.Name)
			assert.NotEmpty(t, response.TokenID)
			assert.NotEmpty(t, response.Token)
			assert.NotEmpty(t, response.CreatedAt)
		})
	})

	t.Run("POST /api/v1/tokens without auth returns 401", func(t *testing.T) {
		RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
			mux := GetTestMux()
			tokenRequest := CreateTokenRequest{
				Name: "Test Token",
			}

			jsonData, err := json.Marshal(tokenRequest)
			require.NoError(t, err)

			req, err := http.NewRequestWithContext(ctx, "POST", "/api/v1/tokens", bytes.NewBuffer(jsonData))
			require.NoError(t, err)
			req.Header.Set("Content-Type", "application/json")
			// No Authorization header

			rr := httptest.NewRecorder()
			mux.ServeHTTP(rr, req)

			assert.Equal(t, http.StatusUnauthorized, rr.Code)
		})
	})

	t.Run("POST /api/v1/tokens with invalid request returns 400", func(t *testing.T) {
		RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
			mux := GetTestMux()
			dataUtils := &DataUtils{db: tx}

			// Create a test admin user (tokens require admin privileges)
			user, err := dataUtils.CreateUser(DataSetup{
				"Username": "admin",
				"Email":    "admin@example.com",
				"Roles":    []string{"admin"},
			})
			require.NoError(t, err)

			// Create auth header
			authHeader, err := createTokenAuthHeader(ctx, tx, user.UserID)
			require.NoError(t, err)

			// Missing required fields
			tokenRequest := CreateTokenRequest{}

			jsonData, err := json.Marshal(tokenRequest)
			require.NoError(t, err)

			req, err := http.NewRequestWithContext(ctx, "POST", "/api/v1/tokens", bytes.NewBuffer(jsonData))
			require.NoError(t, err)
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", authHeader)

			rr := httptest.NewRecorder()
			mux.ServeHTTP(rr, req)

			assert.Equal(t, http.StatusBadRequest, rr.Code)
		})
	})

	t.Run("GET /api/v1/tokens lists tokens", func(t *testing.T) {
		RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
			mux := GetTestMux()
			dataUtils := &DataUtils{db: tx}

			// Create a test admin user (tokens require admin privileges)
			user, err := dataUtils.CreateUser(DataSetup{
				"Username": "admin",
				"Email":    "admin@example.com",
				"Roles":    []string{"admin"},
			})
			require.NoError(t, err)

			// Create auth header
			authHeader, err := createTokenAuthHeader(ctx, tx, user.UserID)
			require.NoError(t, err)
			// Create test tokens
			token1, err := dataUtils.CreateAPIToken(DataSetup{
				"UserID":   user.UserID,
				"Name":     "Token 1",
				"IsActive": true,
			})
			require.NoError(t, err)

			token2, err := dataUtils.CreateAPIToken(DataSetup{
				"UserID":   user.UserID,
				"Name":     "Token 2",
				"IsActive": false,
			})
			require.NoError(t, err)

			req, err := http.NewRequestWithContext(ctx, "GET", "/api/v1/tokens", nil)
			require.NoError(t, err)
			req.Header.Set("Authorization", authHeader)

			rr := httptest.NewRecorder()
			mux.ServeHTTP(rr, req)

			assert.Equal(t, http.StatusOK, rr.Code)

			var response ListTokensResponse
			err = json.Unmarshal(rr.Body.Bytes(), &response)
			require.NoError(t, err)

			// Should include the auth token we created plus the two test tokens
			assert.Len(t, response.Tokens, 3)
			assert.Equal(t, 3, response.Total)

			// Find our test tokens in the response
			tokenIDs := []string{token1.TokenID, token2.TokenID}
			foundTokens := 0
			for _, token := range response.Tokens {
				for _, expectedID := range tokenIDs {
					if token.TokenID == expectedID {
						foundTokens++
					}
				}
			}
			assert.Equal(t, 2, foundTokens)
		})
	})

	// Note: GET /api/v1/tokens/{token_id} and PUT /api/v1/tokens/{token_id}
	// are not implemented in the current API, skipping those tests

	t.Run("DELETE /api/v1/tokens/{token_id} deletes a token", func(t *testing.T) {
		RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
			mux := GetTestMux()
			dataUtils := &DataUtils{db: tx}

			// Create a test admin user (tokens require admin privileges)
			user, err := dataUtils.CreateUser(DataSetup{
				"Username": "admin",
				"Email":    "admin@example.com",
				"Roles":    []string{"admin"},
			})
			require.NoError(t, err)

			// Create auth header
			authHeader, err := createTokenAuthHeader(ctx, tx, user.UserID)
			require.NoError(t, err)

			// Create a token to delete
			token, err := dataUtils.CreateAPIToken(DataSetup{
				"UserID":   user.UserID,
				"Name":     "Token to Delete",
				"IsActive": true,
			})
			require.NoError(t, err)

			req, err := http.NewRequestWithContext(ctx, "DELETE", "/api/v1/tokens/"+token.TokenID, nil)
			require.NoError(t, err)
			req.Header.Set("Authorization", authHeader)

			rr := httptest.NewRecorder()
			mux.ServeHTTP(rr, req)

			assert.Equal(t, http.StatusNoContent, rr.Code)

			// Verify token is deleted by checking it's not in the list anymore
			listReq, err := http.NewRequestWithContext(ctx, "GET", "/api/v1/tokens", nil)
			require.NoError(t, err)
			listReq.Header.Set("Authorization", authHeader)

			listRR := httptest.NewRecorder()
			mux.ServeHTTP(listRR, listReq)

			assert.Equal(t, http.StatusOK, listRR.Code)
			var listResponse ListTokensResponse
			err = json.Unmarshal(listRR.Body.Bytes(), &listResponse)
			require.NoError(t, err)

			// Revocation keeps the audit record and marks the token inactive.
			foundRevoked := false
			for _, respToken := range listResponse.Tokens {
				if respToken.TokenID == token.TokenID {
					foundRevoked = true
					assert.False(t, respToken.IsActive)
				}
			}
			assert.True(t, foundRevoked)
		})
	})
}

func TestTokenCreationCannotAmplifyAuthority(t *testing.T) {
	RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
		defaultOrg, err := postgres_store.PostgresStore.GetDefaultOrganization(ctx)
		require.NoError(t, err)
		otherOrg := &models.Organization{Name: uniqueName("token-other-org"), DisplayName: "Other", Status: models.OrganizationStatusActive}
		require.NoError(t, postgres_store.PostgresStore.CreateOrganization(ctx, otherOrg))
		owner := defaultOrg.OrgID
		capabilities, err := tokencaps.New(tokencaps.TokensManage)
		require.NoError(t, err)
		principal := &checkauth.Principal{CredentialID: uuid.NewString(), CredentialType: "service_token",
			OwnerOrgID: owner, OrganizationIDs: []string{owner}, Capabilities: capabilities}
		handler := handlers.NewTokenHandler(postgres_store.PostgresStore)

		request := func(body map[string]any) int {
			encoded, err := json.Marshal(body)
			require.NoError(t, err)
			requestContext := checkauth.SetPrincipalContext(ctx, principal)
			req := httptest.NewRequest(http.MethodPost, "/api/v1/tokens", bytes.NewReader(encoded)).WithContext(requestContext)
			req.Header.Set("Content-Type", "application/json")
			recorder := httptest.NewRecorder()
			handler.CreateToken(recorder, req)
			return recorder.Code
		}
		assert.Equal(t, http.StatusForbidden, request(map[string]any{"name": "global"}))
		assert.Equal(t, http.StatusForbidden, request(map[string]any{"name": "other", "organizations": []string{otherOrg.Name}, "capabilities": []string{tokencaps.TokensManage}}))
		assert.Equal(t, http.StatusForbidden, request(map[string]any{"name": "wider", "organizations": []string{defaultOrg.Name}, "capabilities": []string{tokencaps.TokensManage, tokencaps.JobsRead}}))
		assert.Equal(t, http.StatusCreated, request(map[string]any{"name": "subset", "organizations": []string{defaultOrg.Name}, "capabilities": []string{tokencaps.TokensManage}}))
	})
}

func TestDelegatedTokenUsesLiveUserStatus(t *testing.T) {
	RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
		data := &DataUtils{db: tx}
		user, err := data.CreateUser(DataSetup{"Status": "active"})
		require.NoError(t, err)
		raw := "delegated-token-" + uniqueName("live-user")
		token := &models.APIToken{UserID: user.UserID, TokenHash: checkauth.HashAPIToken(raw), Name: "delegated",
			IsActive: true, SubjectType: "user_token", AllOrganizations: true, AllCapabilities: true}
		require.NoError(t, postgres_store.PostgresStore.CreateAPIToken(ctx, token))
		_, resolved, err := postgres_store.PostgresStore.ValidateAPIToken(ctx, raw)
		require.NoError(t, err)
		require.Equal(t, user.UserID, resolved.UserID)
		require.NoError(t, tx.Model(&models.User{}).Where("user_id = ?", user.UserID).Update("status", "disabled").Error)
		_, _, err = postgres_store.PostgresStore.ValidateAPIToken(ctx, raw)
		assert.True(t, errors.Is(err, store.ErrNotFound), "disabled user must invalidate delegated token")
	})
}

// TestTokensAPIValidation tests input validation and edge cases
func TestTokensAPIValidation(t *testing.T) {
	t.Run("invalid JSON returns 400", func(t *testing.T) {
		RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
			mux := GetTestMux()
			dataUtils := &DataUtils{db: tx}

			// Create a test admin user (tokens require admin privileges)
			user, err := dataUtils.CreateUser(DataSetup{
				"Username": "admin",
				"Email":    "admin@example.com",
				"Roles":    []string{"admin"},
			})
			require.NoError(t, err)

			// Create auth header
			authHeader, err := createTokenAuthHeader(ctx, tx, user.UserID)
			require.NoError(t, err)

			req, err := http.NewRequestWithContext(ctx, "POST", "/api/v1/tokens", strings.NewReader("{invalid json"))
			require.NoError(t, err)
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", authHeader)

			rr := httptest.NewRecorder()
			mux.ServeHTTP(rr, req)

			assert.Equal(t, http.StatusBadRequest, rr.Code)
		})
	})

	t.Run("unsupported HTTP methods return 405", func(t *testing.T) {
		RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
			mux := GetTestMux()
			dataUtils := &DataUtils{db: tx}

			// Create a test admin user (tokens require admin privileges)
			user, err := dataUtils.CreateUser(DataSetup{
				"Username": "admin",
				"Email":    "admin@example.com",
				"Roles":    []string{"admin"},
			})
			require.NoError(t, err)

			// Create auth header
			authHeader, err := createTokenAuthHeader(ctx, tx, user.UserID)
			require.NoError(t, err)

			methods := []string{"PATCH", "OPTIONS"}
			for _, method := range methods {
				req, err := http.NewRequestWithContext(ctx, method, "/api/v1/tokens", nil)
				require.NoError(t, err)
				req.Header.Set("Authorization", authHeader)

				rr := httptest.NewRecorder()
				mux.ServeHTTP(rr, req)

				assert.Equal(t, http.StatusMethodNotAllowed, rr.Code,
					"Method %s should return 405", method)
			}
		})
	})

	t.Run("invalid token ID format in URL", func(t *testing.T) {
		RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
			mux := GetTestMux()
			dataUtils := &DataUtils{db: tx}

			// Create a test admin user (tokens require admin privileges)
			user, err := dataUtils.CreateUser(DataSetup{
				"Username": "admin",
				"Email":    "admin@example.com",
				"Roles":    []string{"admin"},
			})
			require.NoError(t, err)

			// Create auth header
			authHeader, err := createTokenAuthHeader(ctx, tx, user.UserID)
			require.NoError(t, err)

			// Test with empty token ID (should return 400 due to path validation)
			req, err := http.NewRequestWithContext(ctx, "GET", "/api/v1/tokens/", nil)
			require.NoError(t, err)
			req.Header.Set("Authorization", authHeader)

			rr := httptest.NewRecorder()
			mux.ServeHTTP(rr, req)

			assert.Equal(t, http.StatusBadRequest, rr.Code)
		})
	})
}

// createTokenAuthHeader creates an Authorization header with a real API token (helper for token tests)
func createTokenAuthHeader(ctx context.Context, tx *gorm.DB, userID string) (string, error) {
	// Create a real token value for authentication
	tokenValue := "test-auth-token-" + userID

	// Use the same hash function that the auth middleware uses
	tokenHash := sha256.Sum256([]byte(tokenValue))

	// Create the token in the database
	dataUtils := &DataUtils{db: tx}
	_, err := dataUtils.CreateAPIToken(DataSetup{
		"UserID":           userID,
		"Name":             "Auth Token",
		"TokenHash":        tokenHash[:], // Convert [32]byte to []byte
		"IsActive":         true,
		"SubjectType":      "instance_token",
		"AllOrganizations": true,
		"AllCapabilities":  true,
	})
	if err != nil {
		return "", err
	}

	return "Bearer " + tokenValue, nil
}
