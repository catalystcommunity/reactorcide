package workerauth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

const (
	// WorkerSessionTokenBytes is the raw worker session token size: 256
	// bits, matching internal/auth.SessionTokenBytes.
	WorkerSessionTokenBytes = 32

	// WorkerSessionTTL is how long a freshly minted or heartbeat-refreshed
	// worker session remains valid without another heartbeat. Chosen
	// generously relative to HeartbeatInterval (~2880x it) so a worker that
	// misses a handful of heartbeats -- a GC pause, a transient network
	// blip, a slow job holding up the loop -- doesn't get logged out;
	// expiry only bites a worker that has gone silent for a long time.
	// Refreshed on every successful Heartbeat (WORKERS_PLAN.md "Workers").
	WorkerSessionTTL = 24 * time.Hour

	// HeartbeatInterval is the cadence a worker is told to call Heartbeat
	// at -- returned as-is in Register's heartbeat_interval response field
	// (WORKERS_PLAN.md "Workers"). It is a protocol-pacing constant, not a
	// store/session mechanism: Resolve's session-validity check only cares
	// about WorkerSessionTTL/expires_at, not this value.
	HeartbeatInterval = 30 * time.Second

	// sessionTouchThrottle bounds how often Resolve writes last_seen_at
	// back to the store: once per 5 minutes of session activity, matching
	// internal/auth's sessionTouchThrottle so routine heartbeat/RequestJob
	// polling doesn't hammer worker_sessions on every call. This throttles
	// the *last_seen_at bookkeeping write*, not session expiry -- expiry
	// extension happens explicitly in Heartbeat's caller (P2-A2), not here.
	sessionTouchThrottle = 5 * time.Minute
)

// WorkerSessionStore is the narrow store surface WorkerSessions consumes,
// satisfied by postgres_store/worker_operations.go's CreateWorkerSession +
// GetActiveWorkerSessionByTokenHash + TouchWorkerSessionLastSeen +
// RevokeWorkerSession, and easily faked in tests.
type WorkerSessionStore interface {
	CreateWorkerSession(ctx context.Context, session *models.WorkerSession) error
	GetActiveWorkerSessionByTokenHash(ctx context.Context, tokenHash []byte) (*models.WorkerSession, *models.Worker, error)
	TouchWorkerSessionLastSeen(ctx context.Context, sessionID string) error
	RevokeWorkerSession(ctx context.Context, sessionID string) error
}

// WorkerSessions mints, resolves, and revokes opaque worker session tokens.
// Mirrors internal/auth.Sessions' shape exactly, for the worker side of
// auth: only the SHA-256 hash of a token is ever persisted, and the raw
// token is returned exactly once, by Mint, and must never be logged.
type WorkerSessions struct {
	store WorkerSessionStore
	now   func() time.Time
}

// NewWorkerSessions constructs a WorkerSessions backed by s.
func NewWorkerSessions(s WorkerSessionStore) *WorkerSessions {
	return &WorkerSessions{store: s, now: time.Now}
}

func generateWorkerSessionToken() (string, error) {
	buf := make([]byte, WorkerSessionTokenBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("workerauth: generating worker session token: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

// Mint creates a new session for workerID, valid for WorkerSessionTTL, and
// returns the raw bearer token. The token is returned exactly once; only
// its SHA-256 hash is persisted. Callers (Register) ride this token in the
// CSIL envelope's auth field on every subsequent worker call.
func (ws *WorkerSessions) Mint(ctx context.Context, workerID string) (string, error) {
	token, err := generateWorkerSessionToken()
	if err != nil {
		return "", err
	}

	now := ws.now()
	session := &models.WorkerSession{
		WorkerID:   workerID,
		TokenHash:  HashToken(token),
		ExpiresAt:  now.Add(WorkerSessionTTL),
		LastSeenAt: now,
		CreatedAt:  now,
	}
	if err := ws.store.CreateWorkerSession(ctx, session); err != nil {
		return "", fmt.Errorf("workerauth: creating worker session: %w", err)
	}
	return token, nil
}

// Resolve looks up the active session for a raw bearer token and its
// owning worker. Returns store.ErrNotFound if the token is empty, unknown,
// expired, or revoked. Lazily (and best-effort) touches last_seen_at,
// throttled to once per sessionTouchThrottle so routine RequestJob
// long-polling doesn't write on every call.
func (ws *WorkerSessions) Resolve(ctx context.Context, token string) (*models.Worker, *models.WorkerSession, error) {
	if token == "" {
		return nil, nil, store.ErrNotFound
	}

	session, worker, err := ws.store.GetActiveWorkerSessionByTokenHash(ctx, HashToken(token))
	if err != nil {
		return nil, nil, err
	}

	if ws.now().Sub(session.LastSeenAt) > sessionTouchThrottle {
		if touchErr := ws.store.TouchWorkerSessionLastSeen(ctx, session.SessionID); touchErr == nil {
			session.LastSeenAt = ws.now()
		}
		// A touch failure (including store.ErrNotFound if the session was
		// concurrently revoked) is not fatal to this resolution: the
		// caller already holds a validly-resolved session and worker as of
		// the GetActiveWorkerSessionByTokenHash call above.
	}
	return worker, session, nil
}

// Revoke revokes the session matching the given raw bearer token, if any.
// Revoking an empty, unknown, already-revoked, or expired token is not an
// error -- worker shutdown/re-enrollment is idempotent from the caller's
// perspective, mirroring internal/auth.Sessions.RevokeSession.
func (ws *WorkerSessions) Revoke(ctx context.Context, token string) error {
	if token == "" {
		return nil
	}

	session, _, err := ws.store.GetActiveWorkerSessionByTokenHash(ctx, HashToken(token))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil
		}
		return err
	}
	if err := ws.store.RevokeWorkerSession(ctx, session.SessionID); err != nil && !errors.Is(err, store.ErrNotFound) {
		return fmt.Errorf("workerauth: revoking worker session: %w", err)
	}
	return nil
}
