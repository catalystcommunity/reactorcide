package worker

import (
	"context"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

// executeWithRunnerlib hardcodes /tmp/reactorcide-jobs as the base directory
// for per-job workspaces (normally provisioned as a shared volume in
// deployment). Ensure it exists so these tests don't depend on that having
// been set up externally.
func ensureJobWorkspaceBaseDir(t *testing.T) {
	t.Helper()
	if err := os.MkdirAll("/tmp/reactorcide-jobs", 0777); err != nil {
		t.Fatalf("failed to create /tmp/reactorcide-jobs: %v", err)
	}
}

// fakeJobRunner is a minimal JobRunner used to exercise job_processor.go's
// cancel-poll path (pollForCancel) without touching Docker/containerd/k8s.
// WaitForCompletion blocks on waitBlock until either the fake container
// "exits" on its own (test closes waitBlock directly) or Stop/Cleanup is
// invoked (which closes it on the runner's behalf, exactly like a real
// runner unblocking WaitForCompletion once the container actually stops).
type fakeJobRunner struct {
	mu           sync.Mutex
	spawnCalls   int
	stopCalls    []fakeStopCall
	cleanupCalls []string

	waitBlock chan struct{}
	waitOnce  sync.Once
	exitCode  int
	waitErr   error

	stopErr    error
	cleanupErr error
}

type fakeStopCall struct {
	containerID string
	grace       time.Duration
}

func newFakeJobRunner() *fakeJobRunner {
	return &fakeJobRunner{waitBlock: make(chan struct{})}
}

func (f *fakeJobRunner) unblockWait() {
	f.waitOnce.Do(func() { close(f.waitBlock) })
}

func (f *fakeJobRunner) SpawnJob(ctx context.Context, config *JobConfig) (string, error) {
	f.mu.Lock()
	f.spawnCalls++
	f.mu.Unlock()
	return "fake-container-1", nil
}

func (f *fakeJobRunner) StreamLogs(ctx context.Context, jobID string) (io.ReadCloser, io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader("")), io.NopCloser(strings.NewReader("")), nil
}

func (f *fakeJobRunner) WaitForCompletion(ctx context.Context, jobID string) (int, error) {
	<-f.waitBlock
	return f.exitCode, f.waitErr
}

func (f *fakeJobRunner) Stop(ctx context.Context, jobID string, grace time.Duration) error {
	f.mu.Lock()
	f.stopCalls = append(f.stopCalls, fakeStopCall{containerID: jobID, grace: grace})
	f.mu.Unlock()
	// Simulate the container actually stopping in response to Stop, which is
	// what unblocks a real runner's WaitForCompletion.
	f.unblockWait()
	return f.stopErr
}

func (f *fakeJobRunner) Cleanup(ctx context.Context, jobID string) error {
	f.mu.Lock()
	f.cleanupCalls = append(f.cleanupCalls, jobID)
	f.mu.Unlock()
	// A kill-path Cleanup call (from pollForCancel, not the deferred one at
	// the end of executeWithRunnerlib) also needs to unblock WaitForCompletion,
	// exactly as force-removing a real container/pod would.
	f.unblockWait()
	return f.cleanupErr
}

var _ JobRunner = (*fakeJobRunner)(nil)

func (f *fakeJobRunner) stopCallCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.stopCalls)
}

func (f *fakeJobRunner) cleanupCallCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.cleanupCalls)
}

func newCancelPollTestJob() *models.Job {
	return &models.Job{
		JobID:      "cancel-poll-job",
		Status:     "running",
		JobCommand: "echo hi",
	}
}

func newCancelPollTestConfig() *JobProcessorConfig {
	return &JobProcessorConfig{
		HeartbeatInterval: 5 * time.Millisecond,
		HeartbeatTimeout:  time.Minute,
		CancelGrace:       time.Second,
	}
}

// TestJobProcessor_CancelPoll_Graceful verifies that when the heartbeat
// goroutine observes the job's DB status flip to "cancelling" (with no kill
// marker), it calls JobRunner.Stop with the configured grace period exactly
// once, and the resulting JobResult reports Cancelled=true, Killed=false.
func TestJobProcessor_CancelPoll_Graceful(t *testing.T) {
	ensureJobWorkspaceBaseDir(t)
	job := newCancelPollTestJob()
	runner := newFakeJobRunner()
	mockStore := &MockStore{
		GetJobByIDFunc: func(ctx context.Context, jobID string) (*models.Job, error) {
			// Every poll observes the job as already cancelling (simulates a
			// REST CancelJob call having landed concurrently).
			return &models.Job{JobID: jobID, Status: "cancelling"}, nil
		},
	}

	jp := NewJobProcessorWithConfig(mockStore, runner, false, newCancelPollTestConfig())

	execCtx := &JobExecutionContext{HeartbeatFunc: func(ctx context.Context) error { return nil }}

	resultCh := make(chan *JobResult, 1)
	go func() {
		resultCh <- jp.ProcessJobWithContext(context.Background(), job, execCtx)
	}()

	select {
	case result := <-resultCh:
		if runner.stopCallCount() != 1 {
			t.Fatalf("expected exactly 1 Stop call, got %d", runner.stopCallCount())
		}
		if runner.stopCalls[0].grace != time.Second {
			t.Errorf("expected Stop grace of 1s, got %v", runner.stopCalls[0].grace)
		}
		if !result.Cancelled {
			t.Error("expected result.Cancelled to be true")
		}
		if result.Killed {
			t.Error("expected result.Killed to be false for a graceful cancel")
		}
		// Cleanup is still called exactly once, via the normal deferred
		// cleanup path in executeWithRunnerlib.
		if runner.cleanupCallCount() != 1 {
			t.Errorf("expected exactly 1 Cleanup call, got %d", runner.cleanupCallCount())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for ProcessJobWithContext to return")
	}
}

// TestJobProcessor_CancelPoll_Kill verifies that when the heartbeat
// goroutine observes job.Status == "cancelling" with CancelMode == "kill",
// it force-cleans up the container immediately (no Stop call), and the
// resulting JobResult reports Cancelled=true, Killed=true.
func TestJobProcessor_CancelPoll_Kill(t *testing.T) {
	ensureJobWorkspaceBaseDir(t)
	job := newCancelPollTestJob()
	runner := newFakeJobRunner()
	mockStore := &MockStore{
		GetJobByIDFunc: func(ctx context.Context, jobID string) (*models.Job, error) {
			return &models.Job{JobID: jobID, Status: "cancelling", CancelMode: "kill"}, nil
		},
	}

	jp := NewJobProcessorWithConfig(mockStore, runner, false, newCancelPollTestConfig())
	execCtx := &JobExecutionContext{HeartbeatFunc: func(ctx context.Context) error { return nil }}

	resultCh := make(chan *JobResult, 1)
	go func() {
		resultCh <- jp.ProcessJobWithContext(context.Background(), job, execCtx)
	}()

	select {
	case result := <-resultCh:
		if runner.stopCallCount() != 0 {
			t.Errorf("expected 0 Stop calls for a kill, got %d", runner.stopCallCount())
		}
		if !result.Cancelled || !result.Killed {
			t.Errorf("expected Cancelled=true, Killed=true, got Cancelled=%v Killed=%v", result.Cancelled, result.Killed)
		}
		// Cleanup is called twice: once immediately by pollForCancel's kill
		// branch, once more via the normal deferred cleanup at the end of
		// executeWithRunnerlib.
		if runner.cleanupCallCount() != 2 {
			t.Errorf("expected exactly 2 Cleanup calls (poll-triggered + deferred), got %d", runner.cleanupCallCount())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for ProcessJobWithContext to return")
	}
}

// TestJobProcessor_CancelPoll_NaturalCompletionWins verifies the race
// resolution: if the job command exits on its own before the cancel-poll
// ever observes "cancelling", the result reflects normal completion, not a
// cancel — even if the heartbeat goroutine is configured and running.
func TestJobProcessor_CancelPoll_NaturalCompletionWins(t *testing.T) {
	ensureJobWorkspaceBaseDir(t)
	job := newCancelPollTestJob()
	runner := newFakeJobRunner()
	runner.exitCode = 0
	// Container "exits on its own" immediately.
	runner.unblockWait()

	mockStore := &MockStore{
		GetJobByIDFunc: func(ctx context.Context, jobID string) (*models.Job, error) {
			// Never reports cancelling — job stays "running" from the DB's
			// point of view right up until it naturally finishes.
			return &models.Job{JobID: jobID, Status: "running"}, nil
		},
	}

	jp := NewJobProcessorWithConfig(mockStore, runner, false, newCancelPollTestConfig())
	execCtx := &JobExecutionContext{HeartbeatFunc: func(ctx context.Context) error { return nil }}

	result := jp.ProcessJobWithContext(context.Background(), job, execCtx)

	if result.Cancelled {
		t.Error("expected result.Cancelled to be false when the job finished before any cancel was observed")
	}
	if result.ExitCode != 0 {
		t.Errorf("expected ExitCode 0, got %d", result.ExitCode)
	}
}
