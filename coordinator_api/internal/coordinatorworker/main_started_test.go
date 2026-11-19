package coordinatorworker

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/worker"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/workerclient/csilapi"
)

// stateRunner is a runner with ExecutionTimingReader (like Kubernetes).
// Timing reads return before until Stop is called, then after (when set).
type stateRunner struct {
	*fakeRunner
	mu      sync.Mutex
	before  worker.ExecutionTiming
	after   *worker.ExecutionTiming
	stopped bool
	err     error
}

func (r *stateRunner) ExecutionTiming(context.Context, string) (worker.ExecutionTiming, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err != nil {
		return worker.ExecutionTiming{}, r.err
	}
	if r.stopped && r.after != nil {
		return *r.after, nil
	}
	return r.before, nil
}

func (r *stateRunner) Stop(ctx context.Context, id string, grace time.Duration) error {
	r.mu.Lock()
	r.stopped = true
	r.mu.Unlock()
	return r.fakeRunner.Stop(ctx, id, grace)
}

func mainStartedOf(req csilapi.ReportResultRequest) string {
	if req.MainStarted == nil {
		return "unknown"
	}
	if *req.MainStarted {
		return "true"
	}
	return "false"
}

func TestRunLeaseReportsMainStarted(t *testing.T) {
	pending := worker.ExecutionTiming{MainNotStarted: true}
	started := worker.ExecutionTiming{StartedAt: time.Now().Add(-time.Minute), FinishedAt: time.Now()}
	cases := []struct {
		name       string
		runner     func() worker.JobRunner
		wantStatus string
		wantClass  string
		want       string
		wantStops  int
	}{
		{
			name:       "spawn error never started",
			runner:     func() worker.JobRunner { return &fakeRunner{SpawnErr: errors.New("daemon gone")} },
			wantStatus: "failed", wantClass: models.FailureClassPlatform, want: "false",
		},
		{
			name: "runtime lost after a docker start",
			runner: func() worker.JobRunner {
				return &fakeRunner{WaitExit: -1, WaitErr: errors.New("error waiting for container: EOF")}
			},
			wantStatus: "failed", wantClass: models.FailureClassPlatform, want: "true",
		},
		{
			name: "pending pod proved not started",
			runner: func() worker.JobRunner {
				return &stateRunner{fakeRunner: &fakeRunner{WaitExit: -1, WaitErr: errors.New("failed to list pods for job: refused")}, before: pending}
			},
			wantStatus: "failed", wantClass: models.FailureClassPlatform, want: "false", wantStops: 1,
		},
		{
			name: "pod started between the reads",
			runner: func() worker.JobRunner {
				return &stateRunner{fakeRunner: &fakeRunner{WaitExit: -1, WaitErr: errors.New("failed to list pods for job: refused")}, before: pending, after: &started}
			},
			wantStatus: "failed", wantClass: models.FailureClassPlatform, want: "true", wantStops: 1,
		},
		{
			name: "no pod is not proof",
			runner: func() worker.JobRunner {
				return &stateRunner{fakeRunner: &fakeRunner{WaitExit: -1, WaitErr: errors.New("failed to list pods for job: refused")}, before: worker.ExecutionTiming{}}
			},
			wantStatus: "failed", wantClass: models.FailureClassPlatform, want: "unknown",
		},
		{
			name: "runtime read error is not proof",
			runner: func() worker.JobRunner {
				return &stateRunner{fakeRunner: &fakeRunner{WaitExit: -1, WaitErr: errors.New("failed to list pods for job: refused")}, before: pending, err: errors.New("refused")}
			},
			wantStatus: "failed", wantClass: models.FailureClassPlatform, want: "unknown",
		},
		{
			name: "output read error after exit 0",
			runner: func() worker.JobRunner {
				return &stateRunner{fakeRunner: &fakeRunner{WaitExit: 0, WaitErr: errors.New("failed to read workflow output: exec refused")}, before: started}
			},
			wantStatus: "failed", wantClass: models.FailureClassPlatform, want: "true",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := &fakeClient{}
			runner := tc.runner()
			runLease(c, runner, failureLease(0), newLeaseTracker(), testConfig())
			req := onlyResult(t, c)
			if req.Status != tc.wantStatus || classOf(req) != tc.wantClass || mainStartedOf(req) != tc.want {
				t.Fatalf("result = %s/%q main_started=%s, want %s/%q main_started=%s", req.Status, classOf(req), mainStartedOf(req), tc.wantStatus, tc.wantClass, tc.want)
			}
			var stops int
			switch r := runner.(type) {
			case *fakeRunner:
				stops = len(r.snapshotStopCalls())
			case *stateRunner:
				stops = len(r.snapshotStopCalls())
			}
			if stops != tc.wantStops {
				t.Fatalf("Stop calls = %d, want %d", stops, tc.wantStops)
			}
		})
	}
}

func TestTimeoutAfterWaitReturnedDoesNotApply(t *testing.T) {
	tl := &trackedLease{}
	tl.markWaitDone()
	if tl.markTimeout() || tl.getOutcome() != "" {
		t.Fatal("a timeout after WaitForCompletion returned must not apply")
	}
	tl2 := &trackedLease{}
	if !tl2.markTimeout() || tl2.getOutcome() != "timeout" {
		t.Fatal("a timeout before completion must apply")
	}
}

func TestCompletedWithinTimeout(t *testing.T) {
	start := time.Now().Add(-time.Hour)
	inside := worker.ExecutionTiming{StartedAt: start, FinishedAt: start.Add(59 * time.Second)}
	outside := worker.ExecutionTiming{StartedAt: start, FinishedAt: start.Add(61 * time.Second)}
	if !completedWithinTimeout(0, nil, inside, 60) {
		t.Fatal("exit 0 inside the timeout is a completion")
	}
	if completedWithinTimeout(0, nil, outside, 60) {
		t.Fatal("an exit after the timeout is a timeout")
	}
	if completedWithinTimeout(143, nil, inside, 60) || completedWithinTimeout(0, errors.New("x"), inside, 60) {
		t.Fatal("only a clean exit 0 wins over the timeout")
	}
	if completedWithinTimeout(0, nil, worker.ExecutionTiming{StartedAt: start}, 60) {
		t.Fatal("no finish time is no proof")
	}
}

func TestFinalizeStatusRuntimeDeadlineIsTimeout(t *testing.T) {
	err := worker.NewFailureError(models.FailureClassTimeout, "DeadlineExceeded", errors.New("deadline"))
	got := finalizeStatus("", -1, err, 60)
	if got.status != "timeout" || got.class != models.FailureClassTimeout {
		t.Fatalf("got %s/%q, want timeout/timeout", got.status, got.class)
	}
}
