package coordinatorworker

import (
	"context"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/worker"
)

func captureExecutionTiming(ctx context.Context, runner worker.JobRunner, tl *trackedLease) {
	reader, ok := runner.(worker.ExecutionTimingReader)
	if !ok {
		return
	}
	tl.mu.Lock()
	id := tl.runnerID
	tl.mu.Unlock()
	if id == "" {
		return
	}
	readCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	timing, err := reader.ExecutionTiming(readCtx, id)
	if err != nil {
		return
	}
	tl.mu.Lock()
	defer tl.mu.Unlock()
	if tl.timing.StartedAt.IsZero() {
		tl.timing.StartedAt = timing.StartedAt
	}
	if !timing.FinishedAt.IsZero() {
		tl.timing.FinishedAt = timing.FinishedAt
	}
}

func timestampText(at time.Time) *string {
	if at.IsZero() {
		return nil
	}
	s := at.UTC().Format(time.RFC3339Nano)
	return &s
}
