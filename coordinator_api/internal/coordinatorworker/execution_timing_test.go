package coordinatorworker

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/worker"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/workerclient/csilapi"
)

type timedRunner struct {
	*fakeRunner
	timing  worker.ExecutionTiming
	waiting chan struct{}
}

func (r *timedRunner) ExecutionTiming(context.Context, string) (worker.ExecutionTiming, error) {
	return r.timing, nil
}
func (r *timedRunner) StreamLogs(ctx context.Context, id string) (io.ReadCloser, io.ReadCloser, error) {
	if r.waiting != nil {
		close(r.waiting)
		<-ctx.Done()
		return nil, nil, ctx.Err()
	}
	return r.fakeRunner.StreamLogs(ctx, id)
}
func (r *timedRunner) WaitForCompletion(ctx context.Context, id string) (int, error) {
	if r.waiting != nil {
		return -1, ctx.Err()
	}
	return r.fakeRunner.WaitForCompletion(ctx, id)
}

func TestRuntimeTimingSurvivesShortLease(t *testing.T) {
	now := time.Now().UTC()
	runner := &timedRunner{fakeRunner: &fakeRunner{}, timing: worker.ExecutionTiming{StartedAt: now.Add(-3 * time.Second), FinishedAt: now.Add(-time.Second)}}
	c := &fakeClient{}
	cfg := testConfig()
	cfg.DataDir = t.TempDir()
	runLease(c, runner, csilapi.Lease{LeaseId: "short", JobId: "job", RunAsUser: "1001"}, newLeaseTracker(), cfg)
	if len(c.ExecutionResultCalls) != 1 {
		t.Fatal("missing result")
	}
	result := c.ExecutionResultCalls[0]
	if result.StartedAt == nil || *result.StartedAt != runner.timing.StartedAt.Format(time.RFC3339Nano) || result.FinishedAt == nil || *result.FinishedAt != runner.timing.FinishedAt.Format(time.RFC3339Nano) {
		t.Fatalf("lost runtime timestamps: %+v", result)
	}
	if len(c.HeartbeatCalls) == 0 || c.HeartbeatCalls[0].RunningLeases[0].StartedAt == nil {
		t.Fatal("start was not reported promptly")
	}
}

func TestSchedulingCancellationUnblocksStartupWait(t *testing.T) {
	runner := &timedRunner{fakeRunner: &fakeRunner{}, waiting: make(chan struct{})}
	c := &fakeClient{}
	tracker := newLeaseTracker()
	cfg := testConfig()
	cfg.DataDir = t.TempDir()
	done := make(chan struct{})
	go func() {
		defer close(done)
		runLease(c, runner, csilapi.Lease{LeaseId: "pending", JobId: "job", RunAsUser: "1001"}, tracker, cfg)
	}()
	select {
	case <-runner.waiting:
	case <-time.After(5 * time.Second):
		t.Fatal("did not enter startup wait")
	}
	applyDirective(runner, tracker, csilapi.Directive{LeaseId: "pending", Action: "cancel"})
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("cancel left job scheduling")
	}
	result := c.ExecutionResultCalls[0]
	if result.Status != "cancelled" || result.StartedAt != nil || result.FinishedAt != nil {
		t.Fatalf("cancel invented runtime: %+v", result)
	}
}
