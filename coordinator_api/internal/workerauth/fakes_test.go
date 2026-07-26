package workerauth

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

// fakeStore is an in-memory implementation of EnrollmentTokenStore and
// WorkerSessionStore, following this repo's convention of consumer-defined
// narrow interfaces backed by hand-rolled fakes in tests (see
// internal/auth/fakes_test.go). No real network/DB.
type fakeStore struct {
	mu sync.Mutex

	pools   map[string]models.WorkerPool          // key: pool_id
	tokens  map[string]models.PoolEnrollmentToken // key: string(token_hash)
	workers map[string]models.Worker              // key: worker_id

	sessions map[string]models.WorkerSession // key: string(token_hash)

	touchedTokenIDs   []string
	touchedSessionIDs []string

	nextID int
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		pools:    map[string]models.WorkerPool{},
		tokens:   map[string]models.PoolEnrollmentToken{},
		workers:  map[string]models.Worker{},
		sessions: map[string]models.WorkerSession{},
	}
}

func (f *fakeStore) genID(prefix string) string {
	f.nextID++
	return fmt.Sprintf("%s-%d", prefix, f.nextID)
}

// --- fixtures ---------------------------------------------------------

func (f *fakeStore) addPool(pool models.WorkerPool) models.WorkerPool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if pool.PoolID == "" {
		pool.PoolID = f.genID("pool")
	}
	f.pools[pool.PoolID] = pool
	return pool
}

func (f *fakeStore) addToken(tok models.PoolEnrollmentToken) models.PoolEnrollmentToken {
	f.mu.Lock()
	defer f.mu.Unlock()
	if tok.TokenID == "" {
		tok.TokenID = f.genID("token")
	}
	f.tokens[string(tok.TokenHash)] = tok
	return tok
}

func (f *fakeStore) addWorker(w models.Worker) models.Worker {
	f.mu.Lock()
	defer f.mu.Unlock()
	if w.WorkerID == "" {
		w.WorkerID = f.genID("worker")
	}
	f.workers[w.WorkerID] = w
	return w
}

// --- EnrollmentTokenStore -----------------------------------------------

func (f *fakeStore) GetActiveEnrollmentTokenByHash(_ context.Context, tokenHash []byte) (*models.PoolEnrollmentToken, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	tok, ok := f.tokens[string(tokenHash)]
	if !ok || !tok.IsActive {
		return nil, store.ErrNotFound
	}
	return &tok, nil
}

func (f *fakeStore) TouchEnrollmentTokenLastUsed(_ context.Context, tokenID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.touchedTokenIDs = append(f.touchedTokenIDs, tokenID)
	for hash, tok := range f.tokens {
		if tok.TokenID == tokenID {
			now := fakeNow()
			tok.LastUsedAt = &now
			f.tokens[hash] = tok
			return nil
		}
	}
	return store.ErrNotFound
}

func (f *fakeStore) GetWorkerPoolByID(_ context.Context, poolID string) (*models.WorkerPool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	pool, ok := f.pools[poolID]
	if !ok {
		return nil, store.ErrNotFound
	}
	return &pool, nil
}

// --- WorkerSessionStore ---------------------------------------------------

func (f *fakeStore) CreateWorkerSession(_ context.Context, session *models.WorkerSession) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if session.SessionID == "" {
		session.SessionID = f.genID("session")
	}
	f.sessions[string(session.TokenHash)] = *session
	return nil
}

func (f *fakeStore) GetActiveWorkerSessionByTokenHash(_ context.Context, tokenHash []byte) (*models.WorkerSession, *models.Worker, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	session, ok := f.sessions[string(tokenHash)]
	if !ok {
		return nil, nil, store.ErrNotFound
	}
	if session.IsRevoked() || session.IsExpired() {
		return nil, nil, store.ErrNotFound
	}
	worker, ok := f.workers[session.WorkerID]
	if !ok {
		return nil, nil, store.ErrNotFound
	}
	return &session, &worker, nil
}

func (f *fakeStore) TouchWorkerSessionLastSeen(_ context.Context, sessionID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.touchedSessionIDs = append(f.touchedSessionIDs, sessionID)
	for hash, session := range f.sessions {
		if session.SessionID == sessionID {
			session.LastSeenAt = fakeNow()
			f.sessions[hash] = session
			return nil
		}
	}
	return store.ErrNotFound
}

func (f *fakeStore) RevokeWorkerSession(_ context.Context, sessionID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for hash, session := range f.sessions {
		if session.SessionID == sessionID {
			now := fakeNow()
			session.RevokedAt = &now
			f.sessions[hash] = session
			return nil
		}
	}
	return store.ErrNotFound
}

// fakeNow is a var (rather than a direct time.Now() call at each site) so a
// future test could override it; today it's just real wall-clock time.
var fakeNow = time.Now
