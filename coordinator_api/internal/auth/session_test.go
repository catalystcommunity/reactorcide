package auth

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

func TestSessionsMintAndResolve(t *testing.T) {
	ctx := context.Background()
	fs := newFakeStore()
	must(t, fs.CreateUser(ctx, &models.User{UserID: "user-1", Username: "alice"}))

	sessions := NewSessions(fs)
	token, err := sessions.MintSession(ctx, "user-1")
	if err != nil {
		t.Fatalf("MintSession() error = %v", err)
	}
	if len(token) == 0 {
		t.Fatal("MintSession() returned an empty token")
	}

	user, session, err := sessions.ResolveSession(ctx, token)
	if err != nil {
		t.Fatalf("ResolveSession() error = %v", err)
	}
	if user.UserID != "user-1" {
		t.Fatalf("ResolveSession() user = %q, want user-1", user.UserID)
	}
	if session.UserID != "user-1" {
		t.Fatalf("ResolveSession() session.UserID = %q, want user-1", session.UserID)
	}
}

func TestSessionsResolveUnknownToken(t *testing.T) {
	ctx := context.Background()
	fs := newFakeStore()
	sessions := NewSessions(fs)

	if _, _, err := sessions.ResolveSession(ctx, "does-not-exist"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("ResolveSession() error = %v, want store.ErrNotFound", err)
	}
	if _, _, err := sessions.ResolveSession(ctx, ""); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("ResolveSession(\"\") error = %v, want store.ErrNotFound", err)
	}
}

func TestSessionsResolveExpired(t *testing.T) {
	ctx := context.Background()
	fs := newFakeStore()
	must(t, fs.CreateUser(ctx, &models.User{UserID: "user-1", Username: "alice"}))

	sessions := NewSessions(fs)
	sessions.now = func() time.Time { return time.Now().Add(-40 * 24 * time.Hour) } // well before SessionExpiry
	token, err := sessions.MintSession(ctx, "user-1")
	if err != nil {
		t.Fatalf("MintSession() error = %v", err)
	}

	sessions.now = time.Now
	if _, _, err := sessions.ResolveSession(ctx, token); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("ResolveSession() on an expired session error = %v, want store.ErrNotFound", err)
	}
}

func TestSessionsRevoke(t *testing.T) {
	ctx := context.Background()
	fs := newFakeStore()
	must(t, fs.CreateUser(ctx, &models.User{UserID: "user-1", Username: "alice"}))

	sessions := NewSessions(fs)
	token, err := sessions.MintSession(ctx, "user-1")
	if err != nil {
		t.Fatalf("MintSession() error = %v", err)
	}

	if err := sessions.RevokeSession(ctx, token); err != nil {
		t.Fatalf("RevokeSession() error = %v", err)
	}
	if _, _, err := sessions.ResolveSession(ctx, token); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("ResolveSession() after revoke error = %v, want store.ErrNotFound", err)
	}

	// Revoking again (or an unknown/empty token) must be a no-op, not an error.
	if err := sessions.RevokeSession(ctx, token); err != nil {
		t.Fatalf("RevokeSession() on an already-revoked token error = %v", err)
	}
	if err := sessions.RevokeSession(ctx, "never-existed"); err != nil {
		t.Fatalf("RevokeSession() on an unknown token error = %v", err)
	}
	if err := sessions.RevokeSession(ctx, ""); err != nil {
		t.Fatalf("RevokeSession(\"\") error = %v", err)
	}
}

func TestSessionsResolveTouchesLastSeenWhenStale(t *testing.T) {
	ctx := context.Background()
	fs := newFakeStore()
	must(t, fs.CreateUser(ctx, &models.User{UserID: "user-1", Username: "alice"}))

	sessions := NewSessions(fs)
	start := time.Now()
	sessions.now = func() time.Time { return start }
	token, err := sessions.MintSession(ctx, "user-1")
	if err != nil {
		t.Fatalf("MintSession() error = %v", err)
	}

	// Within the throttle window: last_seen_at must not move.
	sessions.now = func() time.Time { return start.Add(1 * time.Minute) }
	if _, _, err := sessions.ResolveSession(ctx, token); err != nil {
		t.Fatalf("ResolveSession() error = %v", err)
	}
	_, session, err := sessions.ResolveSession(ctx, token)
	if err != nil {
		t.Fatalf("ResolveSession() error = %v", err)
	}
	if !session.LastSeenAt.Equal(start) {
		t.Fatalf("expected last_seen_at to remain untouched within the throttle window, got %v want %v", session.LastSeenAt, start)
	}

	// Past the throttle window: last_seen_at must advance.
	later := start.Add(10 * time.Minute)
	sessions.now = func() time.Time { return later }
	if _, _, err := sessions.ResolveSession(ctx, token); err != nil {
		t.Fatalf("ResolveSession() error = %v", err)
	}
	_, session, err = sessions.ResolveSession(ctx, token)
	if err != nil {
		t.Fatalf("ResolveSession() error = %v", err)
	}
	if !session.LastSeenAt.After(start) {
		t.Fatalf("expected last_seen_at to advance past the throttle window, got %v", session.LastSeenAt)
	}
}
