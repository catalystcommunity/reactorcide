package githubapp

import (
	"context"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

// ParseRateLimit reads the x-ratelimit-* headers of a GitHub response. ok is
// false when the response has no rate-limit headers.
func ParseRateLimit(header http.Header, now time.Time) (models.RateLimitSnapshot, bool) {
	limit, okLimit := headerInt(header, "X-RateLimit-Limit")
	remaining, okRemaining := headerInt(header, "X-RateLimit-Remaining")
	if !okLimit && !okRemaining {
		return models.RateLimitSnapshot{}, false
	}
	snapshot := models.RateLimitSnapshot{}
	if okLimit {
		snapshot.Limit = &limit
	}
	if okRemaining {
		snapshot.Remaining = &remaining
	}
	if used, ok := headerInt(header, "X-RateLimit-Used"); ok {
		snapshot.Used = &used
	}
	if reset, ok := headerInt(header, "X-RateLimit-Reset"); ok {
		at := time.Unix(int64(reset), 0).UTC()
		snapshot.ResetAt = &at
	}
	observed := now.UTC()
	snapshot.ObservedAt = &observed
	return snapshot, true
}

func headerInt(header http.Header, name string) (int, bool) {
	value := header.Get(name)
	if value == "" {
		return 0, false
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return 0, false
	}
	return parsed, true
}

// rateLimitTarget says which record a response belongs to: the App (JWT
// calls) or one installation (installation-token calls).
type rateLimitTarget struct {
	appID          int64
	installationID int64
}

// observingTransport records the rate-limit headers of every response.
type observingTransport struct {
	base    http.RoundTripper
	target  rateLimitTarget
	record  func(rateLimitTarget, models.RateLimitSnapshot)
	nowFunc func() time.Time
}

func (t *observingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}
	resp, err := base.RoundTrip(req)
	if err == nil && resp != nil && t.record != nil {
		now := time.Now()
		if t.nowFunc != nil {
			now = t.nowFunc()
		}
		if snapshot, ok := ParseRateLimit(resp.Header, now); ok {
			t.record(t.target, snapshot)
		}
	}
	return resp, err
}

// RateLimitStore is the part of the App store the recorder writes.
type RateLimitStore interface {
	RecordGitHubAppRateLimit(ctx context.Context, appID int64, snapshot models.RateLimitSnapshot) error
	RecordGitHubInstallationRateLimit(ctx context.Context, installationID int64, snapshot models.RateLimitSnapshot) error
}

// rateLimitRecorder keeps the newest snapshot of each target in memory and
// writes it to the store at most once each interval, and at once when the
// remaining budget drops below lowWater of the limit. Thus a busy
// installation does not write a row for each API call.
type rateLimitRecorder struct {
	store    RateLimitStore
	interval time.Duration
	mu       sync.Mutex
	latest   map[rateLimitTarget]models.RateLimitSnapshot
	written  map[rateLimitTarget]time.Time
}

const rateLimitLowWater = 0.1

func newRateLimitRecorder(store RateLimitStore, interval time.Duration) *rateLimitRecorder {
	return &rateLimitRecorder{store: store, interval: interval,
		latest: map[rateLimitTarget]models.RateLimitSnapshot{}, written: map[rateLimitTarget]time.Time{}}
}

func (r *rateLimitRecorder) observe(target rateLimitTarget, snapshot models.RateLimitSnapshot) {
	if r == nil {
		return
	}
	now := time.Now()
	if snapshot.ObservedAt != nil {
		now = *snapshot.ObservedAt
	}
	r.mu.Lock()
	r.latest[target] = snapshot
	last, seen := r.written[target]
	low := snapshot.Limit != nil && snapshot.Remaining != nil && *snapshot.Limit > 0 &&
		float64(*snapshot.Remaining)/float64(*snapshot.Limit) < rateLimitLowWater
	due := !seen || now.Sub(last) >= r.interval || low
	if due {
		r.written[target] = now
	}
	r.mu.Unlock()
	if due {
		r.write(target, snapshot)
	}
}

// snapshot returns the newest in-memory snapshot of a target.
func (r *rateLimitRecorder) snapshot(target rateLimitTarget) (models.RateLimitSnapshot, bool) {
	if r == nil {
		return models.RateLimitSnapshot{}, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	snapshot, ok := r.latest[target]
	return snapshot, ok
}

func (r *rateLimitRecorder) write(target rateLimitTarget, snapshot models.RateLimitSnapshot) {
	if r.store == nil {
		return
	}
	// The write has its own context: it must not join (or fail with) the
	// transaction of the request that made the API call.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if target.installationID > 0 {
		_ = r.store.RecordGitHubInstallationRateLimit(ctx, target.installationID, snapshot)
		return
	}
	if target.appID > 0 {
		_ = r.store.RecordGitHubAppRateLimit(ctx, target.appID, snapshot)
	}
}
