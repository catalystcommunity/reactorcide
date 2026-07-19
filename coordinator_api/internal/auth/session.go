package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

const (
	// SessionTokenBytes is the raw session token size: 256 bits, per
	// UI_AUTH_PLAN.md's "Sessions" section.
	SessionTokenBytes = 32

	// SessionExpiry is how long a freshly minted session is valid for.
	SessionExpiry = 30 * 24 * time.Hour

	// sessionTouchThrottle bounds how often ResolveSession writes
	// last_seen_at back to the store: once per 5 minutes of session
	// activity, so routine polling doesn't hammer ui_sessions.
	sessionTouchThrottle = 5 * time.Minute
)

// SessionStore is the narrow store surface Sessions consumes, satisfied by
// Task A's postgres_store/auth_operations.go (ui_sessions) plus the
// existing user lookup.
type SessionStore interface {
	CreateUISession(ctx context.Context, session *models.UISession) error
	GetActiveUISessionByTokenHash(ctx context.Context, tokenHash []byte) (*models.UISession, error)
	TouchUISessionLastSeen(ctx context.Context, sessionID string) error
	RevokeUISession(ctx context.Context, sessionID string) error
	GetUserByID(ctx context.Context, userID string) (*models.User, error)
}

// Sessions mints, resolves, and revokes opaque UI session tokens. Only the
// SHA-256 hash of a token is ever persisted (mirrors checkauth.HashAPIToken/
// ValidateAPIToken's API-token pattern) — the raw token is returned exactly
// once, by MintSession, and must never be logged.
type Sessions struct {
	store SessionStore
	now   func() time.Time
}

// NewSessions constructs a Sessions backed by store.
func NewSessions(s SessionStore) *Sessions {
	return &Sessions{store: s, now: time.Now}
}

func hashToken(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

// generateToken returns a fresh cryptographically random hex token of
// SessionTokenBytes bytes. Shared by session minting and login-attempt
// token minting (login_service.go) — both want the same 256-bit strength.
func generateToken() (string, error) {
	buf := make([]byte, SessionTokenBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("auth: generating random token: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

// MintSession creates a new session for userID, valid for SessionExpiry, and
// returns the raw bearer token. The token is returned exactly once; only its
// SHA-256 hash is persisted.
func (s *Sessions) MintSession(ctx context.Context, userID string) (string, error) {
	token, err := generateToken()
	if err != nil {
		return "", err
	}
	now := s.now()
	session := &models.UISession{
		TokenHash:  hashToken(token),
		UserID:     userID,
		CreatedAt:  now,
		ExpiresAt:  now.Add(SessionExpiry),
		LastSeenAt: now,
	}
	if err := s.store.CreateUISession(ctx, session); err != nil {
		return "", fmt.Errorf("auth: creating session: %w", err)
	}
	return token, nil
}

// ResolveSession looks up the active session for a raw bearer token and its
// owning user. Returns store.ErrNotFound if the token is empty, unknown,
// expired, or revoked. Lazily (and best-effort) touches last_seen_at,
// throttled to once per sessionTouchThrottle so routine polling doesn't
// write on every call.
func (s *Sessions) ResolveSession(ctx context.Context, token string) (*models.User, *models.UISession, error) {
	if token == "" {
		return nil, nil, store.ErrNotFound
	}
	session, err := s.store.GetActiveUISessionByTokenHash(ctx, hashToken(token))
	if err != nil {
		return nil, nil, err
	}
	user, err := s.store.GetUserByID(ctx, session.UserID)
	if err != nil {
		return nil, nil, fmt.Errorf("auth: resolving session user: %w", err)
	}

	if s.now().Sub(session.LastSeenAt) > sessionTouchThrottle {
		if touchErr := s.store.TouchUISessionLastSeen(ctx, session.SessionID); touchErr == nil {
			session.LastSeenAt = s.now()
		}
		// A touch failure (including store.ErrNotFound if the session was
		// concurrently revoked) is not fatal to this resolution: the
		// caller already holds a validly-resolved session and user as of
		// the GetActiveUISessionByTokenHash call above.
	}
	return user, session, nil
}

// RevokeSession revokes the session matching the given raw bearer token, if
// any. Revoking an empty, unknown, already-revoked, or expired token is not
// an error — logout is idempotent from the caller's perspective.
func (s *Sessions) RevokeSession(ctx context.Context, token string) error {
	if token == "" {
		return nil
	}
	session, err := s.store.GetActiveUISessionByTokenHash(ctx, hashToken(token))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil
		}
		return err
	}
	if err := s.store.RevokeUISession(ctx, session.SessionID); err != nil && !errors.Is(err, store.ErrNotFound) {
		return fmt.Errorf("auth: revoking session: %w", err)
	}
	return nil
}
