package workerauth

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

func TestWorkerSessionsMintAndResolve(t *testing.T) {
	ctx := context.Background()
	fs := newFakeStore()
	fs.addWorker(models.Worker{WorkerID: "worker-1", WorkerKey: "key-1"})

	sessions := NewWorkerSessions(fs)
	token, err := sessions.Mint(ctx, "worker-1")
	if err != nil {
		t.Fatalf("Mint() error = %v", err)
	}
	if len(token) == 0 {
		t.Fatal("Mint() returned an empty token")
	}

	worker, session, err := sessions.Resolve(ctx, token)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if worker.WorkerID != "worker-1" {
		t.Fatalf("Resolve() worker = %q, want worker-1", worker.WorkerID)
	}
	if session.WorkerID != "worker-1" {
		t.Fatalf("Resolve() session.WorkerID = %q, want worker-1", session.WorkerID)
	}
}

func TestWorkerSessionsResolveUnknownToken(t *testing.T) {
	ctx := context.Background()
	fs := newFakeStore()
	sessions := NewWorkerSessions(fs)

	if _, _, err := sessions.Resolve(ctx, "does-not-exist"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Resolve() error = %v, want store.ErrNotFound", err)
	}
	if _, _, err := sessions.Resolve(ctx, ""); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Resolve(\"\") error = %v, want store.ErrNotFound", err)
	}
}

func TestWorkerSessionsResolveExpired(t *testing.T) {
	ctx := context.Background()
	fs := newFakeStore()
	fs.addWorker(models.Worker{WorkerID: "worker-1", WorkerKey: "key-1"})

	sessions := NewWorkerSessions(fs)
	sessions.now = func() time.Time { return time.Now().Add(-2 * WorkerSessionTTL) } // well before TTL
	token, err := sessions.Mint(ctx, "worker-1")
	if err != nil {
		t.Fatalf("Mint() error = %v", err)
	}

	sessions.now = time.Now
	if _, _, err := sessions.Resolve(ctx, token); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Resolve() on an expired session error = %v, want store.ErrNotFound", err)
	}
}

func TestWorkerSessionsRevoke(t *testing.T) {
	ctx := context.Background()
	fs := newFakeStore()
	fs.addWorker(models.Worker{WorkerID: "worker-1", WorkerKey: "key-1"})

	sessions := NewWorkerSessions(fs)
	token, err := sessions.Mint(ctx, "worker-1")
	if err != nil {
		t.Fatalf("Mint() error = %v", err)
	}

	if err := sessions.Revoke(ctx, token); err != nil {
		t.Fatalf("Revoke() error = %v", err)
	}
	if _, _, err := sessions.Resolve(ctx, token); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Resolve() after revoke error = %v, want store.ErrNotFound", err)
	}

	// Revoking again (or an unknown/empty token) must be a no-op, not an error.
	if err := sessions.Revoke(ctx, token); err != nil {
		t.Fatalf("Revoke() on an already-revoked token error = %v", err)
	}
	if err := sessions.Revoke(ctx, "never-existed"); err != nil {
		t.Fatalf("Revoke() on an unknown token error = %v", err)
	}
	if err := sessions.Revoke(ctx, ""); err != nil {
		t.Fatalf("Revoke(\"\") error = %v", err)
	}
}

func TestWorkerSessionsResolveTouchesLastSeenWhenStale(t *testing.T) {
	ctx := context.Background()
	fs := newFakeStore()
	fs.addWorker(models.Worker{WorkerID: "worker-1", WorkerKey: "key-1"})

	sessions := NewWorkerSessions(fs)
	start := time.Now()
	sessions.now = func() time.Time { return start }
	token, err := sessions.Mint(ctx, "worker-1")
	if err != nil {
		t.Fatalf("Mint() error = %v", err)
	}

	// Within the throttle window: last_seen_at must not move.
	sessions.now = func() time.Time { return start.Add(1 * time.Minute) }
	if _, _, err := sessions.Resolve(ctx, token); err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	_, session, err := sessions.Resolve(ctx, token)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if !session.LastSeenAt.Equal(start) {
		t.Fatalf("expected last_seen_at to remain untouched within the throttle window, got %v want %v", session.LastSeenAt, start)
	}

	// Past the throttle window: last_seen_at must advance.
	later := start.Add(10 * time.Minute)
	sessions.now = func() time.Time { return later }
	if _, _, err := sessions.Resolve(ctx, token); err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	_, session, err = sessions.Resolve(ctx, token)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if !session.LastSeenAt.After(start) {
		t.Fatalf("expected last_seen_at to advance past the throttle window, got %v", session.LastSeenAt)
	}
}

func TestWorkerSessionsMintHashOnlyPersisted(t *testing.T) {
	ctx := context.Background()
	fs := newFakeStore()
	fs.addWorker(models.Worker{WorkerID: "worker-1", WorkerKey: "key-1"})

	sessions := NewWorkerSessions(fs)
	token, err := sessions.Mint(ctx, "worker-1")
	if err != nil {
		t.Fatalf("Mint() error = %v", err)
	}

	// The store must never see the raw token as a map key or value -- only
	// its hash. Confirm the raw token isn't present verbatim anywhere in the
	// fake's session table keys (which are hashes).
	for hash := range fs.sessions {
		if hash == token {
			t.Fatal("raw token was persisted verbatim as a session key, want hash-only")
		}
	}
	if len(fs.sessions) != 1 {
		t.Fatalf("expected exactly one session row, got %d", len(fs.sessions))
	}
}
