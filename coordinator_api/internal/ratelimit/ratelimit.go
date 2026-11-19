// Package ratelimit is a token bucket limiter for overload protection (plan
// item D4). It protects the coordinator from a flood of requests. It is not a
// tenant capacity mechanism (owner decision D10): the limits are generous and
// the same for every key.
//
// The state is in memory in each coordinator replica. With N replicas behind
// a load balancer, a key can get up to N times the configured rate. A
// restart resets the buckets.
package ratelimit

import (
	"math"
	"sync"
	"time"
)

// Limiter holds one token bucket for each key. The zero value is not usable;
// use New. A nil *Limiter allows everything.
type Limiter struct {
	ratePerSecond float64
	burst         float64
	idleAfter     time.Duration

	mu        sync.Mutex
	buckets   map[string]*bucket
	lastSweep time.Time
	now       func() time.Time
}

type bucket struct {
	tokens float64
	last   time.Time
}

// New returns a limiter that permits perMinute requests a minute for each key
// with bursts of up to burst requests. perMinute <= 0 returns nil: no limit.
// A burst below 1 becomes 1.
func New(perMinute, burst int) *Limiter {
	if perMinute <= 0 {
		return nil
	}
	if burst < 1 {
		burst = 1
	}
	rate := float64(perMinute) / 60
	return &Limiter{
		ratePerSecond: rate,
		burst:         float64(burst),
		// A bucket that has been idle long enough to refill is the same as
		// no bucket, so the sweep can drop it.
		idleAfter: time.Duration(float64(burst)/rate*float64(time.Second)) + time.Minute,
		buckets:   map[string]*bucket{},
		now:       time.Now,
	}
}

// SetClock replaces the clock, for tests.
func (l *Limiter) SetClock(now func() time.Time) {
	if l != nil {
		l.now = now
	}
}

// Allow takes one token for key. It returns true when the request may go on.
// When it returns false, retryAfter is the time until one token is free.
func (l *Limiter) Allow(key string) (bool, time.Duration) {
	if l == nil {
		return true, 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	l.sweep(now)
	b, ok := l.buckets[key]
	if !ok {
		b = &bucket{tokens: l.burst, last: now}
		l.buckets[key] = b
	} else if elapsed := now.Sub(b.last).Seconds(); elapsed > 0 {
		b.tokens = math.Min(l.burst, b.tokens+elapsed*l.ratePerSecond)
		b.last = now
	}
	if b.tokens >= 1 {
		b.tokens--
		return true, 0
	}
	wait := time.Duration((1 - b.tokens) / l.ratePerSecond * float64(time.Second))
	if wait < time.Second {
		wait = time.Second
	}
	return false, wait
}

// sweep removes idle buckets at most once a minute. The caller holds mu.
func (l *Limiter) sweep(now time.Time) {
	if now.Sub(l.lastSweep) < time.Minute {
		return
	}
	l.lastSweep = now
	for key, b := range l.buckets {
		if now.Sub(b.last) > l.idleAfter {
			delete(l.buckets, key)
		}
	}
}

// RetryAfterSeconds returns the Retry-After header value for a wait: whole
// seconds, rounded up, at least 1.
func RetryAfterSeconds(wait time.Duration) int {
	seconds := int(math.Ceil(wait.Seconds()))
	if seconds < 1 {
		return 1
	}
	return seconds
}
