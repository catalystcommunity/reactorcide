package vmrunner

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

type fakeNSError struct {
	domain string
	code   int
}

func (e *fakeNSError) Error() string {
	return "Error Domain=" + e.domain + " Code=" + string(rune('0'+e.code)) + ` Description="x" UserInfo=map[]`
}

func fakeIsLimit(err error) bool {
	var ns *fakeNSError
	return errors.As(err, &ns) && isVZLimitExceeded(ns.domain, ns.code)
}

func TestIsVZLimitExceeded(t *testing.T) {
	if !isVZLimitExceeded("VZErrorDomain", 6) {
		t.Fatal("VZErrorDomain code 6 is the VM limit")
	}
	for _, c := range []struct {
		domain string
		code   int
	}{{"VZErrorDomain", 1}, {"VZErrorDomain", 7}, {"NSPOSIXErrorDomain", 6}, {"", 6}} {
		if isVZLimitExceeded(c.domain, c.code) {
			t.Fatalf("%s %d is not the VM limit", c.domain, c.code)
		}
	}
	if !isVZLimitExceededText(`vmrunner/darwin: start vm: Error Domain=VZErrorDomain Code=6 Description="The number of virtual machines exceeds the limit." UserInfo=map[]`) {
		t.Fatal("text form of code 6 must match")
	}
	if isVZLimitExceededText(`Error Domain=VZErrorDomain Code=60 Description="x"`) || isVZLimitExceededText(`Error Domain=VZErrorDomain Code=1 Description="x"`) {
		t.Fatal("other codes must not match")
	}
}

func fastPolicy() startRetryPolicy {
	return startRetryPolicy{Initial: time.Millisecond, Max: 4 * time.Millisecond, Total: 50 * time.Millisecond}
}

func TestStartWithLimitRetrySucceedsAfterLimit(t *testing.T) {
	calls := 0
	var waits []time.Duration
	err := startWithLimitRetry(context.Background(), func() error {
		calls++
		if calls < 4 {
			return &fakeNSError{"VZErrorDomain", 6}
		}
		return nil
	}, fakeIsLimit, fastPolicy(), func(_ int, w time.Duration, _ error) { waits = append(waits, w) })
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if calls != 4 {
		t.Fatalf("calls = %d, want 4", calls)
	}
	want := []time.Duration{time.Millisecond, 2 * time.Millisecond, 4 * time.Millisecond}
	if len(waits) != len(want) {
		t.Fatalf("waits = %v, want %v", waits, want)
	}
	for i := range want {
		if waits[i] != want[i] {
			t.Fatalf("waits = %v, want %v (backoff doubles, capped at Max)", waits, want)
		}
	}
}

func TestStartWithLimitRetryOtherErrorIsNotRetried(t *testing.T) {
	calls := 0
	other := &fakeNSError{"VZErrorDomain", 2}
	err := startWithLimitRetry(context.Background(), func() error { calls++; return other }, fakeIsLimit, fastPolicy(), nil)
	if calls != 1 || !errors.Is(err, other) {
		t.Fatalf("calls = %d err = %v, want one call and the original error", calls, err)
	}
}

func TestStartWithLimitRetryStopsAtTotal(t *testing.T) {
	calls := 0
	limit := &fakeNSError{"VZErrorDomain", 6}
	p := startRetryPolicy{Initial: time.Millisecond, Max: 2 * time.Millisecond, Total: 7 * time.Millisecond}
	err := startWithLimitRetry(context.Background(), func() error { calls++; return limit }, fakeIsLimit, p, nil)
	// waits 1 + 2 + 2 = 5 ms; the next 2 ms wait would make 7 (allowed),
	// then 9 > 7 stops: 5 calls in total.
	if calls != 5 {
		t.Fatalf("calls = %d, want 5", calls)
	}
	if !errors.Is(err, limit) || !strings.Contains(err.Error(), "VM limit stayed full") {
		t.Fatalf("err = %v, want the wrapped limit error", err)
	}
}

func TestStartWithLimitRetryHonoursCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	limit := &fakeNSError{"VZErrorDomain", 6}
	p := startRetryPolicy{Initial: time.Hour, Max: time.Hour, Total: 10 * time.Hour}
	done := make(chan error, 1)
	go func() {
		done <- startWithLimitRetry(ctx, func() error { calls++; return limit }, fakeIsLimit, p, nil)
	}()
	time.Sleep(10 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if calls != 1 || !errors.Is(err, limit) || !strings.Contains(err.Error(), "context canceled") {
			t.Fatalf("calls = %d err = %v", calls, err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the retry loop did not stop on cancel")
	}
}
