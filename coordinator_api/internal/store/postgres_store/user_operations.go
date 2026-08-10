package postgres_store

import (
	"context"
	"fmt"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"gorm.io/gorm"
)

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

func (ps PostgresDbStore) GetUserByUsername(ctx context.Context, username string) (*models.User, error) {
	var user models.User
	if err := ps.getDB(ctx).Where("username = ?", username).First(&user).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, store.ErrNotFound
		}
		return nil, fmt.Errorf("failed to get user by username: %w", err)
	}
	return &user, nil
}

func (ps PostgresDbStore) CreateUser(ctx context.Context, user *models.User) error {
	if err := ps.getDB(ctx).Create(user).Error; err != nil {
		return fmt.Errorf("failed to create user: %w", err)
	}
	return nil
}

func (ps PostgresDbStore) UpdateUser(ctx context.Context, user *models.User) error {
	if err := ps.getDB(ctx).Save(user).Error; err != nil {
		return fmt.Errorf("failed to update user: %w", err)
	}
	return nil
}
