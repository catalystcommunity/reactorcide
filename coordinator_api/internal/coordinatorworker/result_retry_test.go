package coordinatorworker

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/workerclient"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/workerclient/csilapi"
)

func fastResultRetries(t *testing.T) {
	t.Helper()
	window, min, max := resultRetryWindow, resultRetryMin, resultRetryMax
	resultRetryWindow, resultRetryMin, resultRetryMax = 2*time.Second, time.Millisecond, 5*time.Millisecond
	t.Cleanup(func() { resultRetryWindow, resultRetryMin, resultRetryMax = window, min, max })
}

func testRetryConfig() Config {
	return Config{CoordinatorURL: "http://coordinator", EnrollmentToken: "tok", WorkerKey: "wk", OS: "linux", Arch: "amd64"}
}

// failingResults makes the first n ReportResult calls fail with err.
func failingResults(n int, err error) func(context.Context, string, int, string, string) (csilapi.ReportResultResponse, error) {
	var mu sync.Mutex
	calls := 0
	return func(context.Context, string, int, string, string) (csilapi.ReportResultResponse, error) {
		mu.Lock()
		defer mu.Unlock()
		calls++
		if calls <= n {
			return csilapi.ReportResultResponse{}, err
		}
		return csilapi.ReportResultResponse{Ok: true}, nil
	}
}

// TestReportResultRetriesUnavailable reproduces the incident: the
// coordinator store was down for a few seconds when the eval job finished.
func TestReportResultRetriesUnavailable(t *testing.T) {
	fastResultRetries(t)
	c := &fakeClient{ReportResultFunc: failingResults(2, &workerclient.ServiceCallError{Code: "unavailable", Message: "coordinator store unavailable; retry"})}

	reportResultWithOutput(c, testRetryConfig(), "lease-1", 0, "completed", "", "")

	if got := len(c.snapshotReportResults()); got != 3 {
		t.Fatalf("ReportResult calls = %d, want 3 (two failures, then success)", got)
	}
}

func TestReportResultRetriesTransportError(t *testing.T) {
	fastResultRetries(t)
	c := &fakeClient{ReportResultFunc: failingResults(1, errors.New("dial tcp: connection refused"))}

	reportResult(c, testRetryConfig(), "lease-1", 1, "failed", "boom")

	if got := len(c.snapshotReportResults()); got != 2 {
		t.Fatalf("ReportResult calls = %d, want 2", got)
	}
}

func TestReportResultStopsWhenLeaseGone(t *testing.T) {
	fastResultRetries(t)
	c := &fakeClient{ReportResultFunc: failingResults(100, &workerclient.ServiceCallError{Code: "not_found", Message: "lease not found for this worker"})}

	reportResult(c, testRetryConfig(), "lease-1", 0, "completed", "")

	if got := len(c.snapshotReportResults()); got != 1 {
		t.Fatalf("ReportResult calls = %d, want 1 (not_found is final)", got)
	}
}

// TestReportResultRegistersAgainWhenUnauthorized: a lease goroutine holds
// its concurrency slot, so it must open the new session itself.
func TestReportResultRegistersAgainWhenUnauthorized(t *testing.T) {
	fastResultRetries(t)
	c := &fakeClient{ReportResultFunc: failingResults(1, &workerclient.ServiceCallError{Code: "unauthorized", Message: "a valid worker session is required"})}

	reportResult(c, testRetryConfig(), "lease-1", 0, "completed", "")

	if got := len(c.snapshotReportResults()); got != 2 {
		t.Fatalf("ReportResult calls = %d, want 2", got)
	}
	c.mu.Lock()
	registers := len(c.RegisterCalls)
	c.mu.Unlock()
	if registers != 1 {
		t.Fatalf("Register calls = %d, want 1 new session before the retry", registers)
	}
}

func TestReportResultGivesUpAfterWindow(t *testing.T) {
	fastResultRetries(t)
	resultRetryWindow = 20 * time.Millisecond
	c := &fakeClient{ReportResultFunc: failingResults(1<<30, &workerclient.ServiceCallError{Code: "unavailable"})}

	start := time.Now()
	reportResult(c, testRetryConfig(), "lease-1", 0, "completed", "")
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("deliverResult ran %v, want it to stop at the retry window", elapsed)
	}
	if got := len(c.snapshotReportResults()); got < 2 {
		t.Fatalf("ReportResult calls = %d, want retries before giving up", got)
	}
}
