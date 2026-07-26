package coordinatorworker

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/worker"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/workerclient/csilapi"
	"github.com/stretchr/testify/require"
)

var (
	errSpawnFailed      = errors.New("fake: spawn failed")
	errRequestJobFailed = errors.New("fake: request-job transport error")
)

func testConfig() Config {
	return Config{
		CoordinatorURL:  "http://fake-coordinator.invalid",
		EnrollmentToken: "test-enrollment-token",
		WorkerKey:       "test-worker-key",
		OS:              "linux",
		Arch:            "amd64",
		Concurrency:     1,
	}
}

func runnerFactoryFor(r worker.JobRunner) runnerFactory {
	return func(string) (worker.JobRunner, error) { return r, nil }
}

// singleLeaseThenNone returns a RequestJobFunc that hands out lease exactly
// once and returns has_lease=false on every later call, so the run loop's
// poll loop keeps ticking (and can be observed doing so) without claiming a
// second job.
func singleLeaseThenNone(lease csilapi.Lease) func(ctx context.Context, wc csilapi.WorkerCharacteristics) (csilapi.RequestJobResponse, error) {
	var handedOut int32
	return func(ctx context.Context, wc csilapi.WorkerCharacteristics) (csilapi.RequestJobResponse, error) {
		if atomic.CompareAndSwapInt32(&handedOut, 0, 1) {
			l := lease
			return csilapi.RequestJobResponse{HasLease: true, Lease: &l}, nil
		}
		return csilapi.RequestJobResponse{HasLease: false}, nil
	}
}

// TestRunLoop_HappyPath drives a single lease end-to-end (Register ->
// RequestJob -> Spawn/Stream/Wait -> AppendLogs -> ReportResult) and asserts
// the secret injected into the job's env never appears in a shipped log
// chunk even though it appears verbatim in the container's simulated
// stdout -- the core secret-isolation guarantee this run loop must uphold.
func TestRunLoop_HappyPath(t *testing.T) {
	const secretValue = "sooper-seekrit-9f3a"
	stdoutContent := "starting up\nusing token " + secretValue + " to auth\nall done\n"

	lease := csilapi.Lease{
		LeaseId:            "lease-1",
		JobId:              "job-1",
		Image:              "alpine:latest",
		Command:            []string{"sh", "-c", "echo hi"},
		WorkingDir:         "/job",
		Env:                []csilapi.EnvVar{{Key: "PLAIN", Value: "not-a-secret"}},
		Secrets:            []csilapi.EnvVar{{Key: "API_KEY", Value: secretValue}},
		Resources:          csilapi.Resources{CpuRequest: "1", CpuLimit: "2", MemoryLimit: "4Gi"},
		TimeoutSeconds:     30,
		CancelGraceSeconds: 5,
	}

	fc := &fakeClient{}
	fc.RequestJobFunc = singleLeaseThenNone(lease)

	fr := &fakeRunner{Stdout: io.NopCloser(strings.NewReader(stdoutContent))}

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- runLoop(ctx, testConfig(), fc, runnerFactoryFor(fr)) }()

	require.Eventually(t, func() bool { return len(fc.snapshotReportResults()) == 1 }, 5*time.Second, 20*time.Millisecond, "expected ReportResult to be called")

	cancel()
	require.ErrorIs(t, <-done, context.Canceled)

	require.Equal(t, 1, fc.snapshotRegisterCalls(), "expected exactly one Register call on the happy path")
	require.GreaterOrEqual(t, fc.snapshotRequestJobCalls(), 1)

	spawnCalls := fr.snapshotSpawnCalls()
	require.Len(t, spawnCalls, 1, "expected exactly one SpawnJob call")
	require.Equal(t, "alpine:latest", spawnCalls[0].Image)
	require.Equal(t, secretValue, spawnCalls[0].Env["API_KEY"], "the secret must still be injected into the job's real env")
	require.Equal(t, "not-a-secret", spawnCalls[0].Env["PLAIN"])

	logChunks := fc.snapshotAppendLogs()
	require.NotEmpty(t, logChunks, "expected at least one AppendLogs call")
	for _, l := range logChunks {
		require.Equal(t, "lease-1", l.LeaseID)
		require.NotContains(t, l.Chunk, secretValue, "a secret value must never appear in a shipped log chunk")
	}
	// The masked line should still be present in some form (redacted), not
	// silently dropped.
	var sawRedacted bool
	for _, l := range logChunks {
		if strings.Contains(l.Chunk, "[REDACTED]") {
			sawRedacted = true
		}
	}
	require.True(t, sawRedacted, "expected the secret-bearing line to appear masked, not dropped")

	results := fc.snapshotReportResults()
	require.Len(t, results, 1)
	require.Equal(t, "lease-1", results[0].LeaseID)
	require.Equal(t, "completed", results[0].Status)
	require.Equal(t, 0, results[0].ExitCode)

	require.Len(t, fr.snapshotCleanupCalls(), 1, "expected Cleanup to be called once the job finished")
}

// TestRunLoop_NoLeaseBackoff asserts the poll loop keeps calling RequestJob
// (rather than exiting or hammering with no delay) when the coordinator has
// no work, and that it shuts down cleanly on context cancellation.
func TestRunLoop_NoLeaseBackoff(t *testing.T) {
	fc := &fakeClient{}
	fc.RequestJobFunc = func(ctx context.Context, wc csilapi.WorkerCharacteristics) (csilapi.RequestJobResponse, error) {
		return csilapi.RequestJobResponse{HasLease: false}, nil
	}
	fr := &fakeRunner{}

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- runLoop(ctx, testConfig(), fc, runnerFactoryFor(fr)) }()

	require.Eventually(t, func() bool { return fc.snapshotRequestJobCalls() >= 2 }, 6*time.Second, 50*time.Millisecond, "expected RequestJob to be polled repeatedly")

	cancel()
	require.ErrorIs(t, <-done, context.Canceled)

	require.Empty(t, fr.snapshotSpawnCalls(), "no lease was ever handed out, so nothing should have been spawned")
}

// TestRunLoop_CancelDirective asserts a Heartbeat "cancel" directive for a
// running lease results in JobRunner.Stop being called with the lease's
// cancel-grace period, and the job is reported as cancelled.
func TestRunLoop_CancelDirective(t *testing.T) {
	lease := csilapi.Lease{
		LeaseId:            "lease-cancel",
		JobId:              "job-cancel",
		Image:              "alpine:latest",
		Command:            []string{"sleep", "300"},
		CancelGraceSeconds: 7,
	}

	fc := &fakeClient{}
	fc.RequestJobFunc = singleLeaseThenNone(lease)

	var directiveSent int32
	fc.HeartbeatFunc = func(ctx context.Context, status string, running []csilapi.RunningLease) (csilapi.HeartbeatResponse, error) {
		for _, rl := range running {
			if rl.LeaseId == "lease-cancel" && atomic.CompareAndSwapInt32(&directiveSent, 0, 1) {
				return csilapi.HeartbeatResponse{Directives: []csilapi.Directive{{LeaseId: "lease-cancel", Action: "cancel"}}}, nil
			}
		}
		return csilapi.HeartbeatResponse{}, nil
	}

	waitBlock := make(chan struct{})
	fr := &fakeRunner{WaitBlock: waitBlock}
	fr.StopFunc = func(id string, grace time.Duration) error {
		close(waitBlock)
		return nil
	}

	cfg := testConfig()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- runLoop(ctx, cfg, fc, runnerFactoryFor(fr)) }()

	require.Eventually(t, func() bool { return len(fc.snapshotReportResults()) == 1 }, 8*time.Second, 20*time.Millisecond, "expected ReportResult after the cancel directive unblocks WaitForCompletion")

	cancel()
	require.ErrorIs(t, <-done, context.Canceled)

	stops := fr.snapshotStopCalls()
	require.Len(t, stops, 1, "expected exactly one Stop call for the cancel directive")
	require.Equal(t, 7*time.Second, stops[0].Grace)

	results := fc.snapshotReportResults()
	require.Equal(t, "cancelled", results[0].Status)
}

// TestRunLoop_KillDirective asserts a Heartbeat "kill" directive results in
// an immediate JobRunner.Cleanup call (no Stop/grace), and the job is
// reported as cancelled.
func TestRunLoop_KillDirective(t *testing.T) {
	lease := csilapi.Lease{
		LeaseId:            "lease-kill",
		JobId:              "job-kill",
		Image:              "alpine:latest",
		Command:            []string{"sleep", "300"},
		CancelGraceSeconds: 5,
	}

	fc := &fakeClient{}
	fc.RequestJobFunc = singleLeaseThenNone(lease)

	var directiveSent int32
	fc.HeartbeatFunc = func(ctx context.Context, status string, running []csilapi.RunningLease) (csilapi.HeartbeatResponse, error) {
		for _, rl := range running {
			if rl.LeaseId == "lease-kill" && atomic.CompareAndSwapInt32(&directiveSent, 0, 1) {
				return csilapi.HeartbeatResponse{Directives: []csilapi.Directive{{LeaseId: "lease-kill", Action: "kill"}}}, nil
			}
		}
		return csilapi.HeartbeatResponse{}, nil
	}

	waitBlock := make(chan struct{})
	blockClosed := int32(0)
	fr := &fakeRunner{WaitBlock: waitBlock}
	fr.CleanupFunc = func(id string) error {
		if atomic.CompareAndSwapInt32(&blockClosed, 0, 1) {
			close(waitBlock)
		}
		return nil
	}

	cfg := testConfig()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- runLoop(ctx, cfg, fc, runnerFactoryFor(fr)) }()

	require.Eventually(t, func() bool { return len(fc.snapshotReportResults()) == 1 }, 8*time.Second, 20*time.Millisecond, "expected ReportResult after the kill directive unblocks WaitForCompletion")

	cancel()
	require.ErrorIs(t, <-done, context.Canceled)

	require.Empty(t, fr.snapshotStopCalls(), "a kill directive must not call Stop")
	require.NotEmpty(t, fr.snapshotCleanupCalls(), "a kill directive must call Cleanup")

	results := fc.snapshotReportResults()
	require.Equal(t, "cancelled", results[0].Status)
}

// TestRunLoop_SpawnFailureReportsResult asserts a JobRunner.SpawnJob
// failure still reaches a terminal ReportResult (failed) instead of hanging
// the job as running forever.
func TestRunLoop_SpawnFailureReportsResult(t *testing.T) {
	lease := csilapi.Lease{
		LeaseId: "lease-spawn-fail",
		JobId:   "job-spawn-fail",
		Image:   "alpine:latest",
		Command: []string{"true"},
	}

	fc := &fakeClient{}
	fc.RequestJobFunc = singleLeaseThenNone(lease)

	fr := &fakeRunner{SpawnErr: errSpawnFailed}

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- runLoop(ctx, testConfig(), fc, runnerFactoryFor(fr)) }()

	require.Eventually(t, func() bool { return len(fc.snapshotReportResults()) == 1 }, 5*time.Second, 20*time.Millisecond)

	cancel()
	require.ErrorIs(t, <-done, context.Canceled)

	results := fc.snapshotReportResults()
	require.Equal(t, "failed", results[0].Status)
	require.NotEqual(t, 0, results[0].ExitCode)
}

// TestRunLoop_RequestJobErrorReregisters asserts a RequestJob transport
// error triggers a fresh Register call (the session may have expired) and
// the loop keeps polling afterward rather than exiting.
func TestRunLoop_RequestJobErrorReregisters(t *testing.T) {
	fc := &fakeClient{}
	var calls int32
	fc.RequestJobFunc = func(ctx context.Context, wc csilapi.WorkerCharacteristics) (csilapi.RequestJobResponse, error) {
		if atomic.AddInt32(&calls, 1) == 1 {
			return csilapi.RequestJobResponse{}, errRequestJobFailed
		}
		return csilapi.RequestJobResponse{HasLease: false}, nil
	}
	fr := &fakeRunner{}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- runLoop(ctx, testConfig(), fc, runnerFactoryFor(fr)) }()

	require.Eventually(t, func() bool { return fc.snapshotRegisterCalls() >= 2 }, 8*time.Second, 50*time.Millisecond, "expected a second Register after RequestJob failed")

	cancel()
	require.ErrorIs(t, <-done, context.Canceled)
}
