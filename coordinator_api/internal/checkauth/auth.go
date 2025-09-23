package checkauth

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

type contextKey string

const (
	UserContextKey     contextKey = "user"
	VerifiedContextKey contextKey = "verified"
)

// GetUserFromContext retrieves the authenticated user from the request context
func GetUserFromContext(ctx context.Context) *models.User {
	if user, ok := ctx.Value(UserContextKey).(*models.User); ok {
		return user
	}
	return nil
}

// GetVerifiedFromContext checks if the request is verified/authenticated
func GetVerifiedFromContext(ctx context.Context) bool {
	if verified, ok := ctx.Value(VerifiedContextKey).(bool); ok {
		return verified
	}
	return false
}

// SetUserContext adds a user to the request context
func SetUserContext(ctx context.Context, user *models.User) context.Context {
	return context.WithValue(ctx, UserContextKey, user)
}

// SetVerifiedContext sets the verification status in the request context
func SetVerifiedContext(ctx context.Context, verified bool) context.Context {
	return context.WithValue(ctx, VerifiedContextKey, verified)
}

// ValidateAPIToken validates an API token against its stored hash
func ValidateAPIToken(tokenString string, hash []byte) bool {
	tokenHash := sha256.Sum256([]byte(tokenString))
	return subtle.ConstantTimeCompare(tokenHash[:], hash) == 1
}

// GenerateVerifier generates a SHA256 hash for verification purposes
// This is a legacy function from the original codebase, keeping for compatibility
func GenerateVerifier(userID string, salt []byte, token string) string {
	data := userID + string(salt) + token
	hash := sha256.Sum256([]byte(data))
	return hex.EncodeToString(hash[:])
}

// VerifySession verifies a session using the legacy verification method
// This is kept for backward compatibility with existing verification patterns
func VerifySession(token, verifier, userID string, salt []byte) bool {
	expectedVerifier := GenerateVerifier(userID, salt, token)
	return subtle.ConstantTimeCompare([]byte(verifier), []byte(expectedVerifier)) == 1
}

// HashAPIToken creates a SHA256 hash of an API token for storage
func HashAPIToken(token string) []byte {
	hash := sha256.Sum256([]byte(token))
	return hash[:]
}
