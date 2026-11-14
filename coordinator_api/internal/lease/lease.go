// Package lease runs work on at most one coordinator replica at a time.
//
// A replica takes a named lease in Postgres (coordinator_leases), renews it on
// a short interval, and runs its work only while the renewals succeed. When a
// renewal fails, or another holder has taken the lease, the work's context is
// cancelled at once. When the holder dies, its lease expires after the TTL and
// another replica takes over.
//
// A lease is mutual exclusion in the normal case, not a proof. A stalled
// holder can briefly overlap a new one (for example a long GC pause just
// before expiry). Work that must be exactly-once still needs its own
// idempotency, such as the unique (schedule_id, fire_time) fire row.
package lease

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"time"

	"github.com/catalystcommunity/app-utils-go/logging"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
)

// Defaults: renew every 10s, expire after 30s. A holder that misses two
// renewals in a row still has its lease; the third miss lets another replica
// in.
const (
	DefaultTTL        = 30 * time.Second
	DefaultRenewEvery = 10 * time.Second
)

// Manager acquires and renews leases for one replica.
type Manager struct {
	Store  store.LeaseStore
	Holder string
	// TTL is how long a lease lasts without renewal.
	TTL time.Duration
	// RenewEvery is the renewal interval, and the retry interval while
	// another replica holds the lease. It must be well under TTL.
	RenewEvery time.Duration
}

// NewManager returns a Manager with the default timings and a holder ID made
// of the host name and a random suffix, so two processes on one host differ.
func NewManager(s store.LeaseStore) *Manager {
	return &Manager{Store: s, Holder: NewHolderID(), TTL: DefaultTTL, RenewEvery: DefaultRenewEvery}
}

// NewHolderID returns "<hostname>-<8 random hex>".
func NewHolderID() string {
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "coordinator"
	}
	suffix := make([]byte, 4)
	if _, err := rand.Read(suffix); err != nil {
		return host + "-" + time.Now().UTC().Format("150405.000000000")
	}
	return host + "-" + hex.EncodeToString(suffix)
}

func (m *Manager) ttl() time.Duration {
	if m.TTL <= 0 {
		return DefaultTTL
	}
	return m.TTL
}

func (m *Manager) renewEvery() time.Duration {
	if m.RenewEvery <= 0 {
		return DefaultRenewEvery
	}
	return m.RenewEvery
}

// Run blocks until ctx is done. Whenever this replica holds the lease name,
// it runs fn with a context that is cancelled as soon as the lease is lost
// or ctx ends. fn should return promptly after its context is cancelled. If
// fn returns on its own, the lease is released and Run tries again after one
// interval. On exit Run releases the lease, so a clean shutdown hands over
// at once instead of after the TTL.
func (m *Manager) Run(ctx context.Context, name string, fn func(ctx context.Context)) {
	log := logging.Log.WithField("lease", name).WithField("holder", m.Holder)
	wait := time.NewTimer(0)
	defer wait.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-wait.C:
		}
		acquired, err := m.Store.TryAcquireLease(ctx, name, m.Holder, m.ttl())
		if err != nil {
			if ctx.Err() == nil {
				log.WithError(err).Warn("lease acquire failed")
			}
		} else if acquired {
			log.Info("lease acquired")
			m.lead(ctx, name, fn)
			m.release(name)
			log.Info("lease released")
		}
		wait.Reset(m.renewEvery())
	}
}

// lead runs fn and renews the lease until the lease is lost, fn returns, or
// ctx ends.
func (m *Manager) lead(ctx context.Context, name string, fn func(ctx context.Context)) {
	leaderCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		fn(leaderCtx)
	}()
	ticker := time.NewTicker(m.renewEvery())
	defer ticker.Stop()
	for {
		select {
		case <-done:
			return
		case <-ctx.Done():
			cancel()
			<-done
			return
		case <-ticker.C:
			held, err := m.Store.TryAcquireLease(ctx, name, m.Holder, m.ttl())
			if err != nil || !held {
				// Stop at once. A renewal that errored may still have
				// succeeded, but acting as leader without proof is the
				// unsafe direction.
				entry := logging.Log.WithField("lease", name).WithField("holder", m.Holder)
				if err != nil {
					entry = entry.WithError(err)
				}
				entry.Warn("lease lost; stopping leader work")
				cancel()
				<-done
				return
			}
		}
	}
}

func (m *Manager) release(name string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := m.Store.ReleaseLease(ctx, name, m.Holder); err != nil {
		logging.Log.WithError(err).WithField("lease", name).Warn("lease release failed; it will expire")
	}
}
