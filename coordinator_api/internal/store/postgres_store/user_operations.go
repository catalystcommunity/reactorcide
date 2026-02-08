package postgres_store

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/checkauth"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/config"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/secrets"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// GetUserByID retrieves a user by their ID
func (ps PostgresDbStore) GetUserByID(ctx context.Context, userID string) (*models.User, error) {
	if !isValidUUID(userID) {
		return nil, store.ErrNotFound
	}

	var user models.User

	if err := ps.getDB(ctx).Where("user_id = ?", userID).First(&user).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, store.ErrNotFound
		}
		return nil, fmt.Errorf("failed to get user %s: %w", userID, err)
	}

	return &user, nil
}

// CreateUser creates a new user
func (ps PostgresDbStore) CreateUser(ctx context.Context, user *models.User) error {
	if err := ps.getDB(ctx).Create(user).Error; err != nil {
		return fmt.Errorf("failed to create user: %w", err)
	}
	return nil
}

// EnsureDefaultUser creates a default user if DEFAULT_USER_ID is configured and the user doesn't exist
func (ps PostgresDbStore) EnsureDefaultUser() error {
	if config.DefaultUserID == "" {
		return nil // No default user configured
	}

	// Parse UUID
	userUUID, err := uuid.Parse(config.DefaultUserID)
	if err != nil {
		return fmt.Errorf("invalid DEFAULT_USER_ID format: %w", err)
	}

	// Use a background context for this operation
	ctx := context.Background()

	// Check if user exists
	var existingUser models.User
	result := ps.getDB(ctx).Where("user_id = ?", userUUID.String()).First(&existingUser)

	if result.Error == nil {
		// User exists, nothing to do
		return nil
	}

	if result.Error != gorm.ErrRecordNotFound {
		return fmt.Errorf("error checking for default user: %w", result.Error)
	}

	// User doesn't exist, create it
	defaultUser := &models.User{
		UserID:   userUUID.String(),
		Username: "default-user",
		Email:    "default@reactorcide.local",
		Roles:    []string{"admin"}, // Give admin role for convenience
	}

	if err := ps.getDB(ctx).Create(defaultUser).Error; err != nil {
		return fmt.Errorf("failed to create default user: %w", err)
	}

	// Try to auto-initialize secrets using LoadOrCreateMasterKeys
	// This will load from env, DB, or auto-generate keys if needed
	if keyMgr, err := secrets.LoadOrCreateMasterKeys(ps.getDB(ctx)); err == nil {
		if err := keyMgr.InitializeOrgSecretsWithMark(ps.getDB(ctx), userUUID.String()); err != nil {
			log.Printf("Warning: failed to auto-initialize secrets for default user: %v", err)
		} else {
			log.Printf("Initialized secrets for default user %s", userUUID)
		}
	} else {
		log.Printf("Warning: could not load or create master keys for default user: %v", err)
	}

	// Create a default API token
	tokenString, err := generateSecureToken()
	if err != nil {
		return fmt.Errorf("failed to generate secure token: %w", err)
	}

	tokenHash := checkauth.HashAPIToken(tokenString)

	apiToken := &models.APIToken{
		UserID:    userUUID.String(),
		TokenHash: tokenHash,
		Name:      "Default System Token",
		IsActive:  true,
	}

	if err := ps.getDB(ctx).Create(apiToken).Error; err != nil {
		return fmt.Errorf("failed to create default API token: %w", err)
	}

	// Log the token creation for retrieval (this is obviously not production-ready)
	log.Printf("Created default user %s with API token ID %s", userUUID, apiToken.TokenID)
	log.Printf("Default user API token (SAVE THIS - it won't be shown again):")
	log.Printf("Token: %s", tokenString)
	log.Printf("Use this token with: Authorization: Bearer %s", tokenString)
	log.Printf("NOTE: The actual token value is not stored - only its hash. You'll need to use a different method to retrieve tokens in production.")

	return nil
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
