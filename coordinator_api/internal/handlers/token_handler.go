package handlers

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/checkauth"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

// TokenHandler handles API token-related HTTP requests
type TokenHandler struct {
	BaseHandler
	store store.Store
}

// NewTokenHandler creates a new token handler
func NewTokenHandler(store store.Store) *TokenHandler {
	return &TokenHandler{
		store: store,
	}
}

// CreateTokenRequest represents the request payload for creating an API token
type CreateTokenRequest struct {
	Name      string     `json:"name" validate:"required"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
}

// CreateTokenResponse represents the response for creating an API token
type CreateTokenResponse struct {
	TokenID   string     `json:"token_id"`
	Token     string     `json:"token"` // Only returned on creation
	Name      string     `json:"name"`
	CreatedAt time.Time  `json:"created_at"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
}

// TokenResponse represents the response for token operations (without the actual token)
type TokenResponse struct {
	TokenID    string     `json:"token_id"`
	Name       string     `json:"name"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
	ExpiresAt  *time.Time `json:"expires_at,omitempty"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
	IsActive   bool       `json:"is_active"`
}

// ListTokensResponse represents the response for listing tokens
type ListTokensResponse struct {
	Tokens []TokenResponse `json:"tokens"`
	Total  int             `json:"total"`
}

// CreateToken handles POST /api/v1/tokens
func (h *TokenHandler) CreateToken(w http.ResponseWriter, r *http.Request) {
	var req CreateTokenRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.respondWithError(w, http.StatusBadRequest, store.ErrInvalidInput)
		return
	}

	// Get user from context
	user := checkauth.GetUserFromContext(r.Context())
	if user == nil {
		h.respondWithError(w, http.StatusUnauthorized, store.ErrUnauthorized)
		return
	}

	// Validate request
	if req.Name == "" {
		h.respondWithError(w, http.StatusBadRequest, store.ErrInvalidInput)
		return
	}

	// Only admins can create tokens for now
	if !h.isAdmin(user) {
		h.respondWithError(w, http.StatusForbidden, store.ErrForbidden)
		return
	}

	// Generate secure token
	tokenString, err := generateSecureToken()
	if err != nil {
		h.respondWithError(w, http.StatusInternalServerError, err)
		return
	}

	// Hash the token for storage
	tokenHash := checkauth.HashAPIToken(tokenString)

	// Create API token model
	apiToken := &models.APIToken{
		UserID:    user.UserID,
		TokenHash: tokenHash,
		Name:      req.Name,
		ExpiresAt: req.ExpiresAt,
		IsActive:  true,
	}

	// Save to database
	if err := h.store.CreateAPIToken(r.Context(), apiToken); err != nil {
		h.respondWithError(w, http.StatusInternalServerError, err)
		return
	}

	// Return response with actual token (only time it's returned)
	response := CreateTokenResponse{
		TokenID:   apiToken.TokenID,
		Token:     tokenString,
		Name:      apiToken.Name,
		CreatedAt: apiToken.CreatedAt,
		ExpiresAt: apiToken.ExpiresAt,
	}

	h.respondWithJSON(w, http.StatusCreated, response)
}

// ListTokens handles GET /api/v1/tokens
func (h *TokenHandler) ListTokens(w http.ResponseWriter, r *http.Request) {
	user := checkauth.GetUserFromContext(r.Context())
	if user == nil {
		h.respondWithError(w, http.StatusUnauthorized, store.ErrUnauthorized)
		return
	}

	// Get tokens for the user
	tokens, err := h.store.GetAPITokensByUser(r.Context(), user.UserID)
	if err != nil {
		h.respondWithError(w, http.StatusInternalServerError, err)
		return
	}

	// Convert to response format
	tokenResponses := make([]TokenResponse, len(tokens))
	for i, token := range tokens {
		tokenResponses[i] = h.tokenToResponse(&token)
	}

	response := ListTokensResponse{
		Tokens: tokenResponses,
		Total:  len(tokenResponses),
	}

	h.respondWithJSON(w, http.StatusOK, response)
}

// DeleteToken handles DELETE /api/v1/tokens/{token_id}
func (h *TokenHandler) DeleteToken(w http.ResponseWriter, r *http.Request) {
	tokenID := h.getID(r, "token_id")
	if tokenID == "" {
		h.respondWithError(w, http.StatusBadRequest, store.ErrInvalidInput)
		return
	}

	user := checkauth.GetUserFromContext(r.Context())
	if user == nil {
		h.respondWithError(w, http.StatusUnauthorized, store.ErrUnauthorized)
		return
	}

	// Get the token to check ownership (or admin permissions)
	tokens, err := h.store.GetAPITokensByUser(r.Context(), user.UserID)
	if err != nil {
		h.respondWithError(w, http.StatusInternalServerError, err)
		return
	}

	// Check if user owns this token
	var tokenExists bool
	for _, token := range tokens {
		if token.TokenID == tokenID {
			tokenExists = true
			break
		}
	}

	if !tokenExists && !h.isAdmin(user) {
		h.respondWithError(w, http.StatusForbidden, store.ErrForbidden)
		return
	}

	// Delete the token
	if err := h.store.DeleteAPIToken(r.Context(), tokenID); err != nil {
		h.respondWithError(w, http.StatusNotFound, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// Helper methods

func (h *TokenHandler) tokenToResponse(token *models.APIToken) TokenResponse {
	return TokenResponse{
		TokenID:    token.TokenID,
		Name:       token.Name,
		CreatedAt:  token.CreatedAt,
		UpdatedAt:  token.UpdatedAt,
		ExpiresAt:  token.ExpiresAt,
		LastUsedAt: token.LastUsedAt,
		IsActive:   token.IsActive,
	}
}

func (h *TokenHandler) isAdmin(user *models.User) bool {
	for _, role := range user.Roles {
		if role == "admin" || role == "system_admin" {
			return true
		}
	}
	return false
}

// generateSecureToken generates a cryptographically secure random token
func generateSecureToken() (string, error) {
	// Generate 32 bytes of random data
	bytes := make([]byte, 32)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}

	// Convert to hex string (64 characters)
	return hex.EncodeToString(bytes), nil
}
