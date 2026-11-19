package ratelimit

import (
	"testing"
	"time"
)

func TestLimiterBucket(t *testing.T) {
	now := time.Unix(1000, 0)
	limiter := New(60, 2) // one token a second, burst 2
	limiter.SetClock(func() time.Time { return now })

	for i := 0; i < 2; i++ {
		if ok, _ := limiter.Allow("p"); !ok {
			t.Fatalf("request %d of the burst was refused", i)
		}
	}
	ok, wait := limiter.Allow("p")
	if ok || wait < time.Second || wait > 2*time.Second {
		t.Fatalf("over the burst: ok=%t wait=%s", ok, wait)
	}
	if ok, _ := limiter.Allow("other"); !ok {
		t.Fatal("each key has its own bucket")
	}
	now = now.Add(1500 * time.Millisecond)
	if ok, _ := limiter.Allow("p"); !ok {
		t.Fatal("the bucket must refill at the rate")
	}
	if ok, _ := limiter.Allow("p"); ok {
		t.Fatal("only one token refilled")
	}
}

func TestLimiterOffAndSweep(t *testing.T) {
	if New(0, 10) != nil {
		t.Fatal("a rate of 0 turns the limit off")
	}
	var off *Limiter
	if ok, _ := off.Allow("x"); !ok {
		t.Fatal("a nil limiter allows everything")
	}
	now := time.Unix(0, 0)
	limiter := New(600, 5)
	limiter.SetClock(func() time.Time { return now })
	limiter.Allow("idle")
	now = now.Add(time.Hour)
	limiter.Allow("active")
	if _, ok := limiter.buckets["idle"]; ok {
		t.Fatal("an idle bucket must be removed")
	}
}

func TestRetryAfterSeconds(t *testing.T) {
	if RetryAfterSeconds(0) != 1 || RetryAfterSeconds(1100*time.Millisecond) != 2 || RetryAfterSeconds(3*time.Second) != 3 {
		t.Fatal("Retry-After rounds up to whole seconds, at least 1")
	}
}
