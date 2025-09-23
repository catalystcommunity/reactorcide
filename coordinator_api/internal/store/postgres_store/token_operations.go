package postgres_store

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/checkauth"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"gorm.io/gorm"
)

// ValidateAPIToken validates an API token and returns the token and associated user
func (ps PostgresDbStore) ValidateAPIToken(ctx context.Context, token string) (*models.APIToken, *models.User, error) {
	tokenHash := checkauth.HashAPIToken(token)

	var apiToken models.APIToken
	if err := ps.getDB(ctx).Where("token_hash = ? AND is_active = true", tokenHash).First(&apiToken).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, nil, store.ErrNotFound
		}
		return nil, nil, fmt.Errorf("failed to validate API token: %w", err)
	}

	// Check if token is expired
	if apiToken.IsExpired() {
		return nil, nil, store.ErrNotFound
	}

	// Load the associated user separately (Preload wasn't working correctly)
	var user models.User
	if err := ps.getDB(ctx).Where("user_id = ?", apiToken.UserID).First(&user).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, nil, store.ErrNotFound
		}
		return nil, nil, fmt.Errorf("failed to load user for API token: %w", err)
	}

	return &apiToken, &user, nil
}

// CreateAPIToken creates a new API token
func (ps PostgresDbStore) CreateAPIToken(ctx context.Context, apiToken *models.APIToken) error {
	if err := ps.getDB(ctx).Create(apiToken).Error; err != nil {
		return fmt.Errorf("failed to create API token: %w", err)
	}
	return nil
}

// UpdateTokenLastUsed updates the last used timestamp for an API token
func (ps PostgresDbStore) UpdateTokenLastUsed(ctx context.Context, tokenID string, lastUsed time.Time) error {
	result := ps.getDB(ctx).Model(&models.APIToken{}).
		Where("token_id = ?", tokenID).
		Update("last_used_at", lastUsed)

	if result.Error != nil {
		return fmt.Errorf("failed to update token last used: %w", result.Error)
	}

	return nil
}

// GetAPITokensByUser retrieves all API tokens for a user
func (ps PostgresDbStore) GetAPITokensByUser(ctx context.Context, userID string) ([]models.APIToken, error) {
	var tokens []models.APIToken

	if err := ps.getDB(ctx).Where("user_id = ?", userID).
		Order("created_at DESC").
		Find(&tokens).Error; err != nil {
		return nil, fmt.Errorf("failed to get API tokens for user %s: %w", userID, err)
	}

	return tokens, nil
}

// DeleteAPIToken deletes an API token by its ID
func (ps PostgresDbStore) DeleteAPIToken(ctx context.Context, tokenID string) error {
	result := ps.getDB(ctx).Where("token_id = ?", tokenID).Delete(&models.APIToken{})
	if result.Error != nil {
		// Check for PostgreSQL invalid UUID syntax error
		if strings.Contains(result.Error.Error(), "invalid input syntax for type uuid") {
			return store.ErrNotFound
		}
		// Check for PostgreSQL transaction abort error (happens after invalid UUID)
		if strings.Contains(result.Error.Error(), "current transaction is aborted") {
			return store.ErrNotFound
		}
		return fmt.Errorf("failed to delete API token %s: %w", tokenID, result.Error)
	}
	if result.RowsAffected == 0 {
		return store.ErrNotFound
	}
	return nil
}
