package postgres_store

import (
	"context"
	"fmt"
	"strings"

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

const (
	listUsersDefaultLimit = 100
	listUsersMaxLimit     = 200
)

// ListUsers returns active users ordered by username, at most limit rows
// (default 100 when limit <= 0, capped at 200). A non-empty query is a
// case-insensitive substring match over users.username, users.email, and the
// subject/handle/display_name of any linked auth_identities row. The identity
// match is an EXISTS subquery rather than a JOIN so a user with several
// identities is returned once.
func (ps PostgresDbStore) ListUsers(ctx context.Context, query string, limit int) ([]models.User, error) {
	if limit <= 0 {
		limit = listUsersDefaultLimit
	}
	if limit > listUsersMaxLimit {
		limit = listUsersMaxLimit
	}
	db := ps.getDB(ctx).Model(&models.User{}).Where("users.status = ?", "active")
	if trimmed := strings.TrimSpace(query); trimmed != "" {
		pattern := "%" + escapeLikePattern(trimmed) + "%"
		db = db.Where(
			`users.username ILIKE ? ESCAPE '\' OR users.email ILIKE ? ESCAPE '\' OR EXISTS (
				SELECT 1 FROM auth_identities ai
				WHERE ai.user_id = users.user_id
				  AND (ai.subject ILIKE ? ESCAPE '\' OR ai.handle ILIKE ? ESCAPE '\' OR ai.display_name ILIKE ? ESCAPE '\')
			)`,
			pattern, pattern, pattern, pattern, pattern,
		)
	}
	var users []models.User
	if err := db.Order("users.username ASC").Limit(limit).Find(&users).Error; err != nil {
		return nil, fmt.Errorf("failed to list users: %w", err)
	}
	return users, nil
}

// escapeLikePattern escapes the LIKE metacharacters in a user-supplied
// substring so it matches literally under `ESCAPE '\'`.
func escapeLikePattern(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `%`, `\%`)
	s = strings.ReplaceAll(s, `_`, `\_`)
	return s
}
