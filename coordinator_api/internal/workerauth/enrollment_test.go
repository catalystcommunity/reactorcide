package workerauth

import (
	"context"
	"errors"
	"testing"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/checkauth"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

func TestGenerateEnrollmentToken_256BitAndHashOnly(t *testing.T) {
	raw, hash, err := GenerateEnrollmentToken()
	if err != nil {
		t.Fatalf("GenerateEnrollmentToken() error = %v", err)
	}

	// raw is hex-encoded EnrollmentTokenBytes (32) bytes -> 64 hex chars.
	if len(raw) != EnrollmentTokenBytes*2 {
		t.Fatalf("raw token length = %d, want %d (256 bits, hex-encoded)", len(raw), EnrollmentTokenBytes*2)
	}

	// hash is a 32-byte SHA-256 digest, and must match checkauth.HashAPIToken
	// exactly -- this package must never reinvent the hashing.
	if len(hash) != 32 {
		t.Fatalf("hash length = %d, want 32 (SHA-256)", len(hash))
	}
	want := checkauth.HashAPIToken(raw)
	if string(hash) != string(want) {
		t.Fatalf("hash does not match checkauth.HashAPIToken(raw)")
	}

	// Two calls never collide.
	raw2, hash2, err := GenerateEnrollmentToken()
	if err != nil {
		t.Fatalf("GenerateEnrollmentToken() second call error = %v", err)
	}
	if raw == raw2 {
		t.Fatal("two GenerateEnrollmentToken() calls returned the same raw token")
	}
	if string(hash) == string(hash2) {
		t.Fatal("two GenerateEnrollmentToken() calls returned the same hash")
	}
}

func TestValidateEnrollmentToken_Match(t *testing.T) {
	ctx := context.Background()
	fs := newFakeStore()

	pool := fs.addPool(models.WorkerPool{Name: "default"})
	raw, hash, err := GenerateEnrollmentToken()
	if err != nil {
		t.Fatalf("GenerateEnrollmentToken() error = %v", err)
	}
	fs.addToken(models.PoolEnrollmentToken{PoolID: pool.PoolID, TokenHash: hash, IsActive: true})

	e := NewEnrollment(fs)
	got, err := e.ValidateEnrollmentToken(ctx, raw)
	if err != nil {
		t.Fatalf("ValidateEnrollmentToken() error = %v", err)
	}
	if got.PoolID != pool.PoolID {
		t.Fatalf("ValidateEnrollmentToken() pool = %q, want %q", got.PoolID, pool.PoolID)
	}

	if len(fs.touchedTokenIDs) != 1 {
		t.Fatalf("expected exactly one last_used_at touch, got %d", len(fs.touchedTokenIDs))
	}
}

func TestValidateEnrollmentToken_NoMatch(t *testing.T) {
	ctx := context.Background()
	fs := newFakeStore()
	e := NewEnrollment(fs)

	if _, err := e.ValidateEnrollmentToken(ctx, "never-issued-token"); !errors.Is(err, ErrEnrollmentRejected) {
		t.Fatalf("ValidateEnrollmentToken() error = %v, want ErrEnrollmentRejected", err)
	}
	if _, err := e.ValidateEnrollmentToken(ctx, ""); !errors.Is(err, ErrEnrollmentRejected) {
		t.Fatalf("ValidateEnrollmentToken(\"\") error = %v, want ErrEnrollmentRejected", err)
	}
}

func TestValidateEnrollmentToken_InactiveRejected(t *testing.T) {
	ctx := context.Background()
	fs := newFakeStore()

	pool := fs.addPool(models.WorkerPool{Name: "default"})
	raw, hash, err := GenerateEnrollmentToken()
	if err != nil {
		t.Fatalf("GenerateEnrollmentToken() error = %v", err)
	}
	fs.addToken(models.PoolEnrollmentToken{PoolID: pool.PoolID, TokenHash: hash, IsActive: false})

	e := NewEnrollment(fs)
	if _, err := e.ValidateEnrollmentToken(ctx, raw); !errors.Is(err, ErrEnrollmentRejected) {
		t.Fatalf("ValidateEnrollmentToken() on a deactivated token error = %v, want ErrEnrollmentRejected", err)
	}
	if len(fs.touchedTokenIDs) != 0 {
		t.Fatalf("expected no last_used_at touch for a rejected token, got %d", len(fs.touchedTokenIDs))
	}
}

func TestValidateEnrollmentToken_WrongTokenRejected(t *testing.T) {
	ctx := context.Background()
	fs := newFakeStore()

	pool := fs.addPool(models.WorkerPool{Name: "default"})
	_, hash, err := GenerateEnrollmentToken()
	if err != nil {
		t.Fatalf("GenerateEnrollmentToken() error = %v", err)
	}
	fs.addToken(models.PoolEnrollmentToken{PoolID: pool.PoolID, TokenHash: hash, IsActive: true})

	otherRaw, _, err := GenerateEnrollmentToken()
	if err != nil {
		t.Fatalf("GenerateEnrollmentToken() error = %v", err)
	}

	e := NewEnrollment(fs)
	if _, err := e.ValidateEnrollmentToken(ctx, otherRaw); !errors.Is(err, ErrEnrollmentRejected) {
		t.Fatalf("ValidateEnrollmentToken() with a non-matching token error = %v, want ErrEnrollmentRejected", err)
	}
}
