package coordinatorworker

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/worker"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/workerclient/csilapi"
)

// oomRunner is a fakeRunner whose runtime reports an OOM kill.
type oomRunner struct {
	*fakeRunner
}

func (oomRunner) ClassifyExit(context.Context, string, int) (string, string) {
	return models.FailureClassOOM, "OOMKilled"
}

func failureLease(timeoutSeconds int64) csilapi.Lease {
	return csilapi.Lease{LeaseId: "lease-f", JobId: "job-f", Image: "alpine", Command: []string{"true"}, TimeoutSeconds: timeoutSeconds, CancelGraceSeconds: 0}
}

func onlyResult(t *testing.T, c *fakeClient) csilapi.ReportResultRequest {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.ExecutionResultCalls) != 1 {
		t.Fatalf("want one result, got %d", len(c.ExecutionResultCalls))
	}
	return c.ExecutionResultCalls[0]
}

func classOf(req csilapi.ReportResultRequest) string {
	if req.FailureClass == nil {
		return ""
	}
	return *req.FailureClass
}

func TestRunLeaseReportsFailureClass(t *testing.T) {
	cases := []struct {
		name       string
		runner     func() worker.JobRunner
		wantStatus string
		wantClass  string
		wantError  string
	}{
		{
			name:       "job exit",
			runner:     func() worker.JobRunner { return &fakeRunner{WaitExit: 3} },
			wantStatus: "failed", wantClass: models.FailureClassJob, wantError: "job exited with code 3",
		},
		{
			name:       "success has no class",
			runner:     func() worker.JobRunner { return &fakeRunner{} },
			wantStatus: "completed", wantClass: "",
		},
		{
			name:       "oom kill",
			runner:     func() worker.JobRunner { return oomRunner{&fakeRunner{WaitExit: 137}} },
			wantStatus: "failed", wantClass: models.FailureClassOOM, wantError: "OOMKilled",
		},
		{
			name: "image pull at spawn",
			runner: func() worker.JobRunner {
				return &fakeRunner{SpawnErr: worker.NewFailureError(models.FailureClassImagePull, "image pull", errors.New("manifest unknown"))}
			},
			wantStatus: "failed", wantClass: models.FailureClassImagePull, wantError: "manifest unknown",
		},
		{
			name:       "runtime spawn error",
			runner:     func() worker.JobRunner { return &fakeRunner{SpawnErr: errors.New("daemon gone")} },
			wantStatus: "failed", wantClass: models.FailureClassPlatform, wantError: "failed to spawn job",
		},
		{
			name: "unschedulable pod",
			runner: func() worker.JobRunner {
				return &fakeRunner{WaitExit: -1, WaitErr: &worker.PodStartupError{Reason: "ResourceRequestTooLarge", Message: "too big"}}
			},
			wantStatus: "failed", wantClass: models.FailureClassScheduling, wantError: "ResourceRequestTooLarge",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := &fakeClient{}
			runLease(c, tc.runner(), failureLease(0), newLeaseTracker(), testConfig())
			req := onlyResult(t, c)
			if req.Status != tc.wantStatus || classOf(req) != tc.wantClass {
				t.Fatalf("result = %s/%q, want %s/%q", req.Status, classOf(req), tc.wantStatus, tc.wantClass)
			}
			if tc.wantError != "" && (req.Error == nil || !strings.Contains(*req.Error, tc.wantError)) {
				t.Fatalf("error = %v, want it to contain %q", req.Error, tc.wantError)
			}
		})
	}
}

func TestRunLeaseEnforcesTimeout(t *testing.T) {
	prior := timeoutPollInterval
	timeoutPollInterval = 10 * time.Millisecond
	t.Cleanup(func() { timeoutPollInterval = prior })

	block := make(chan struct{})
	runner := &fakeRunner{WaitBlock: block, WaitExit: 143}
	runner.StopFunc = func(string, time.Duration) error {
		close(block)
		return nil
	}
	c := &fakeClient{}
	start := time.Now()
	runLease(c, runner, failureLease(1), newLeaseTracker(), testConfig())
	if elapsed := time.Since(start); elapsed < time.Second || elapsed > 5*time.Second {
		t.Fatalf("timeout fired after %s, want about 1s", elapsed)
	}
	req := onlyResult(t, c)
	if req.Status != "timeout" || classOf(req) != models.FailureClassTimeout {
		t.Fatalf("result = %s/%q, want timeout/timeout", req.Status, classOf(req))
	}
	if len(runner.snapshotStopCalls()) != 1 {
		t.Fatalf("want one Stop call, got %d", len(runner.snapshotStopCalls()))
	}
}

func TestEnforceTimeoutIgnoresZeroAndCancelledLeases(t *testing.T) {
	prior := timeoutPollInterval
	timeoutPollInterval = time.Millisecond
	t.Cleanup(func() { timeoutPollInterval = prior })

	runner := &fakeRunner{}
	tl := &trackedLease{runnerID: "r", timing: worker.ExecutionTiming{StartedAt: time.Now().Add(-time.Hour)}}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	enforceTimeout(ctx, runner, tl, 0, noopLogger{})
	if tl.getOutcome() != "" || len(runner.snapshotStopCalls()) != 0 {
		t.Fatal("a zero timeout must never fire")
	}

	// A cancel directive that acted first wins over the timeout.
	tl.setOutcome("cancel")
	enforceTimeout(ctx, runner, tl, time.Second, noopLogger{})
	if tl.getOutcome() != "cancel" || len(runner.snapshotStopCalls()) != 0 {
		t.Fatal("the timeout must not replace a cancel outcome")
	}
}

type noopLogger struct{}

func (noopLogger) Warn(...interface{}) {}

func TestFinalizeStatusClasses(t *testing.T) {
	cases := []struct {
		outcome    string
		exitCode   int
		waitErr    error
		wantStatus string
		wantClass  string
	}{
		{"cancel", 143, nil, "cancelled", models.FailureClassCancelled},
		{"kill", 137, nil, "cancelled", models.FailureClassCancelled},
		{"timeout", 143, nil, "timeout", models.FailureClassTimeout},
		{"", 0, nil, "completed", ""},
		{"", 1, nil, "failed", models.FailureClassJob},
		{"", -1, errors.New("lost the runtime"), "failed", models.FailureClassPlatform},
	}
	for _, tc := range cases {
		got := finalizeStatus(tc.outcome, tc.exitCode, tc.waitErr, 60)
		if got.status != tc.wantStatus || got.class != tc.wantClass {
			t.Errorf("finalizeStatus(%q, %d, %v) = %s/%q, want %s/%q", tc.outcome, tc.exitCode, tc.waitErr, got.status, got.class, tc.wantStatus, tc.wantClass)
		}
	}
}
