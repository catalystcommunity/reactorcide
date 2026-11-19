package vmrunner

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// Virtualization.framework allows at most two macOS guest VMs at a time.
// A stopped VM keeps its slot until its VZVirtualMachine object is released
// (Code-Hex/vz does that in a Go finalizer; Destroy forces a GC, see
// releaseStoppedVMs), and the release completes asynchronously. So a VM
// that starts right after another VM stopped (while a second VM runs) can
// fail with VZErrorDomain Code=6 (VZErrorVirtualMachineLimitExceeded). That
// is a short wait, not a job failure, so the macOS lifecycle tries the
// start again (startWithLimitRetry).
//
// This file has no build tag: the classifier and the retry loop are tested
// on every OS. lifecycle_darwin_vz.go connects them to *vz.NSError.

const (
	vzErrorDomain = "VZErrorDomain"
	// vzErrorVirtualMachineLimitExceeded is VZErrorVirtualMachineLimitExceeded
	// (vz.ErrorVirtualMachineLimitExceeded in Code-Hex/vz/v3).
	vzErrorVirtualMachineLimitExceeded = 6
)

// isVZLimitExceeded reports whether an NSError domain and code are the
// Virtualization.framework concurrent VM limit.
func isVZLimitExceeded(domain string, code int) bool {
	return domain == vzErrorDomain && code == vzErrorVirtualMachineLimitExceeded
}

// isVZLimitExceededText is the fallback classifier for an error that is not
// a *vz.NSError value (for example, wrapped as text). It matches the
// Code-Hex/vz NSError.Error format "Error Domain=VZErrorDomain Code=6 ...".
func isVZLimitExceededText(msg string) bool {
	return strings.Contains(msg, "Domain="+vzErrorDomain+" Code=6 ") ||
		strings.HasSuffix(msg, "Domain="+vzErrorDomain+" Code=6")
}

// startRetryPolicy bounds startWithLimitRetry.
type startRetryPolicy struct {
	// Initial is the first wait. Each next wait is two times longer, up to
	// Max.
	Initial time.Duration
	Max     time.Duration
	// Total is the maximum time of all waits together. When the next wait
	// would go past Total, the loop stops and returns the last error.
	Total time.Duration
}

// defaultStartRetryPolicy waits 2 s, 4 s, 8 s, 10 s, 10 s, ... for a total of
// at most 60 s.
var defaultStartRetryPolicy = startRetryPolicy{Initial: 2 * time.Second, Max: 10 * time.Second, Total: 60 * time.Second}

// startWithLimitRetry calls start. When start fails with an error that
// isLimit accepts, it waits (bounded exponential backoff) and calls start
// again. Any other error, a context cancel, or the end of the total wait
// stops the loop. onRetry (optional) is called before each wait.
func startWithLimitRetry(ctx context.Context, start func() error, isLimit func(error) bool, policy startRetryPolicy, onRetry func(attempt int, wait time.Duration, err error)) error {
	wait := policy.Initial
	var waited time.Duration
	for attempt := 1; ; attempt++ {
		err := start()
		if err == nil || !isLimit(err) {
			return err
		}
		if wait <= 0 || waited+wait > policy.Total {
			return fmt.Errorf("the Virtualization.framework VM limit stayed full after %s: %w", waited, err)
		}
		if onRetry != nil {
			onRetry(attempt, wait, err)
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return fmt.Errorf("%w (start stopped while waiting for a free VM slot: %v)", err, ctx.Err())
		case <-timer.C:
		}
		waited += wait
		wait *= 2
		if policy.Max > 0 && wait > policy.Max {
			wait = policy.Max
		}
	}
}
