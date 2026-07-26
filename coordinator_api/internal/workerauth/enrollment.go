// Package workerauth provides the worker-side counterparts to
// internal/auth's UI session/credential primitives (WORKERS_PLAN.md
// "Workers -- registration, auth, protocol"): enrollment-token validation
// (the only human-handled worker credential) and ephemeral, process-
// lifetime worker sessions. Every token this package handles is hashed with
// internal/checkauth.HashAPIToken before it ever reaches a store call or a
// log line -- raw token values are returned to a caller exactly once (at
// generation) and never persisted or logged.
package workerauth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/checkauth"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

// EnrollmentTokenBytes is the raw enrollment token size: 256 bits, matching
// internal/auth.SessionTokenBytes/WorkerSessionTokenBytes.
const EnrollmentTokenBytes = 32

// ErrEnrollmentRejected is returned by Enrollment.ValidateEnrollmentToken
// when the presented token doesn't hash-match any active
// pool_enrollment_tokens row. Deliberately does not distinguish "unknown
// token" from "deactivated token" (or wrap store.ErrNotFound directly) so a
// rejected worker -- and callers that don't want an internal/store import
// just to check this -- can't learn anything about which enrollment tokens
// once existed.
var ErrEnrollmentRejected = errors.New("workerauth: enrollment token rejected")

// HashToken hashes a raw token for storage/lookup. A thin re-export of
// checkauth.HashAPIToken so every hash in this package (and its callers)
// goes through the one shared implementation -- never reinvent the hashing
// here.
func HashToken(token string) []byte {
	return checkauth.HashAPIToken(token)
}

// generateRawToken returns a fresh cryptographically random hex token of
// EnrollmentTokenBytes bytes, matching internal/auth's generateToken shape.
func generateRawToken() (string, error) {
	buf := make([]byte, EnrollmentTokenBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("workerauth: generating enrollment token: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

// GenerateEnrollmentToken returns a fresh cryptographically random 256-bit
// enrollment token (hex-encoded) and its SHA-256 hash. The admin
// create-enrollment-token path returns raw to the caller exactly once and
// persists only hash via postgres_store.CreatePoolEnrollmentToken -- raw is
// never stored or logged.
//
// Note for callers: unlike WORKERS_PLAN.md's literal
// `GenerateEnrollmentToken() (raw string, hash []byte)` sketch, this
// returns a third `error` (crypto/rand.Read can fail), matching
// internal/auth's own generateToken -- silently swallowing that possibility
// would be worse than a slightly wider signature.
func GenerateEnrollmentToken() (raw string, hash []byte, err error) {
	raw, err = generateRawToken()
	if err != nil {
		return "", nil, err
	}
	return raw, HashToken(raw), nil
}

// EnrollmentTokenStore is the narrow store surface Enrollment consumes,
// satisfied by postgres_store/worker_operations.go's
// GetActiveEnrollmentTokenByHash + TouchEnrollmentTokenLastUsed +
// GetWorkerPoolByID, and easily faked in tests.
type EnrollmentTokenStore interface {
	GetActiveEnrollmentTokenByHash(ctx context.Context, tokenHash []byte) (*models.PoolEnrollmentToken, error)
	TouchEnrollmentTokenLastUsed(ctx context.Context, tokenID string) error
	GetWorkerPoolByID(ctx context.Context, poolID string) (*models.WorkerPool, error)
}

// Enrollment validates presented enrollment tokens against
// pool_enrollment_tokens, resolving the owning pool. Mirrors
// internal/auth.Sessions' shape (a thin struct wrapping a narrow store
// interface) for the enrollment side of worker auth.
type Enrollment struct {
	store EnrollmentTokenStore
}

// NewEnrollment constructs an Enrollment backed by s.
func NewEnrollment(s EnrollmentTokenStore) *Enrollment {
	return &Enrollment{store: s}
}

// ValidateEnrollmentToken hashes presentedToken and looks up an active
// pool_enrollment_tokens row for it, returning the owning WorkerPool on
// success. Returns ErrEnrollmentRejected (never store.ErrNotFound) on any
// non-match -- empty token, unknown hash, or a hash whose row exists but is
// deactivated. Best-effort touches last_used_at on success; a touch failure
// does not fail validation, mirroring
// internal/auth.Sessions.ResolveSession's last-seen touch.
func (e *Enrollment) ValidateEnrollmentToken(ctx context.Context, presentedToken string) (*models.WorkerPool, error) {
	if presentedToken == "" {
		return nil, ErrEnrollmentRejected
	}

	tok, err := e.store.GetActiveEnrollmentTokenByHash(ctx, HashToken(presentedToken))
	if err != nil {
		return nil, ErrEnrollmentRejected
	}

	pool, err := e.store.GetWorkerPoolByID(ctx, tok.PoolID)
	if err != nil {
		return nil, fmt.Errorf("workerauth: resolving enrollment token's pool: %w", err)
	}

	// Best-effort: the token is valid and the pool resolved either way; a
	// failure to stamp last_used_at must not turn a valid enrollment into a
	// rejection.
	_ = e.store.TouchEnrollmentTokenLastUsed(ctx, tok.TokenID)

	return pool, nil
}
