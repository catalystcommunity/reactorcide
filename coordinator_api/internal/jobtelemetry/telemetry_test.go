package jobtelemetry

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/objects"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

func TestMetricBatchIdempotenceQueryAndDelete(t *testing.T) {
	ctx := context.Background()
	store := objects.NewMemoryObjectStore()
	start := time.Now().UTC().Add(-time.Minute)
	batch := MetricBatch{
		LeaseID: "lease-a", Sequence: 7,
		Series: []SeriesDefinition{{SeriesID: 1, Name: "cpu.usage", Unit: "nanoseconds", Kind: "counter", Labels: []Label{{Key: "cpu", Value: "total"}}}},
		Samples: []Sample{
			{ObservedAt: start, Values: []Value{{SeriesID: 1, Value: 1_000_000_000}}},
			{ObservedAt: start.Add(time.Second), Values: []Value{{SeriesID: 1, Value: 1_500_000_000}}},
		},
	}
	if err := PutMetricBatch(ctx, store, "job-a", batch); err != nil {
		t.Fatal(err)
	}
	if err := PutMetricBatch(ctx, store, "job-a", batch); err != nil {
		t.Fatalf("same retry must be idempotent: %v", err)
	}
	conflict := batch
	conflict.Samples = append([]Sample(nil), batch.Samples...)
	conflict.Samples[1].Values = []Value{{SeriesID: 1, Value: 2_000_000_000}}
	if err := PutMetricBatch(ctx, store, "job-a", conflict); !errors.Is(err, ErrSequenceConflict) {
		t.Fatalf("expected sequence conflict, got %v", err)
	}
	response, err := QueryMetrics(ctx, store, Query{JobID: "job-a", MaxPoints: 10})
	if err != nil {
		t.Fatal(err)
	}
	if !response.Complete || len(response.Series) != 1 || response.Series[0].Name != "cpu.utilization" {
		t.Fatalf("unexpected query response: %+v", response)
	}
	if got := response.Series[0].Points[0].Value; got != 500 {
		t.Fatalf("expected 500 millicores, got %d", got)
	}
	if err := CompactJob(ctx, store, "job-a", true); err != nil {
		t.Fatal(err)
	}
	ingress, _ := store.List(ctx, ObjectPrefix+"/job-a/metrics/")
	archives, _ := store.List(ctx, ObjectPrefix+"/job-a/compacted/metrics/")
	if len(ingress) != 0 || len(archives) != 1 {
		t.Fatalf("unexpected compacted layout: ingress=%v archives=%v", ingress, archives)
	}
	afterCompaction, err := QueryMetrics(ctx, store, Query{JobID: "job-a", MaxPoints: 10})
	if err != nil || len(afterCompaction.Series) != 1 || afterCompaction.Series[0].Points[0].Value != 500 {
		t.Fatalf("compaction changed query results: response=%+v err=%v", afterCompaction, err)
	}
	if err := CompactJob(ctx, store, "job-a", true); err != nil {
		t.Fatal(err)
	}
	archives, _ = store.List(ctx, ObjectPrefix+"/job-a/compacted/metrics/")
	if len(archives) != 1 {
		t.Fatalf("idempotent terminal compaction created extra archives: %v", archives)
	}
	if err := DeleteJob(ctx, store, "job-a"); err != nil {
		t.Fatal(err)
	}
	infos, err := store.List(ctx, ObjectPrefix+"/job-a/")
	if err != nil || len(infos) != 0 {
		t.Fatalf("telemetry was not deleted: infos=%v err=%v", infos, err)
	}
}

func TestPruneBeforeDeletesOnlyOldTerminalJobTelemetry(t *testing.T) {
	ctx := context.Background()
	store := objects.NewMemoryObjectStore()
	now := time.Now().UTC()
	for _, jobID := range []string{"old", "new", "running"} {
		if err := PutLogBatch(ctx, store, jobID, LogBatch{LeaseID: "lease", Stream: "stdout", Entries: []LogEntry{{ObservedAt: now, Message: jobID}}}); err != nil {
			t.Fatal(err)
		}
	}
	oldTime, newTime := now.Add(-48*time.Hour), now.Add(-time.Hour)
	jobs := map[string]*models.Job{
		"old":     {JobID: "old", CompletedAt: &oldTime},
		"new":     {JobID: "new", CompletedAt: &newTime},
		"running": {JobID: "running"},
	}
	deleted, err := PruneBefore(ctx, store, now.Add(-24*time.Hour), func(_ context.Context, id string) (*models.Job, error) { return jobs[id], nil })
	if err != nil || deleted != 1 {
		t.Fatalf("unexpected retention result: deleted=%d err=%v", deleted, err)
	}
	for id, want := range map[string]int{"old": 0, "new": 1, "running": 1} {
		infos, _ := store.List(ctx, ObjectPrefix+"/"+id+"/")
		if len(infos) != want {
			t.Fatalf("job %s has %d objects, want %d", id, len(infos), want)
		}
	}
}

func TestQueryMarksCorruptObjectsIncomplete(t *testing.T) {
	ctx := context.Background()
	store := objects.NewMemoryObjectStore()
	if err := store.Put(ctx, MetricObjectKey("job-b", "lease-b", 0), bytes.NewBufferString("not-json"), "application/json"); err != nil {
		t.Fatal(err)
	}
	response, err := QueryMetrics(ctx, store, Query{JobID: "job-b"})
	if err != nil {
		t.Fatal(err)
	}
	if response.Complete {
		t.Fatal("a corrupt object must make the response incomplete")
	}
}

func TestValidateMetricBatchRejectsUnknownLabelsAndFutureSamples(t *testing.T) {
	now := time.Now().UTC()
	batch := MetricBatch{LeaseID: "lease", Series: []SeriesDefinition{{SeriesID: 1, Name: "memory.usage", Unit: "bytes", Kind: "gauge", Labels: []Label{{Key: "secret", Value: "value"}}}}}
	if err := ValidateMetricBatch(&batch, now); err == nil {
		t.Fatal("expected an unknown label to be rejected")
	}
	batch.Series[0].Labels = nil
	batch.Samples = []Sample{{ObservedAt: now.Add(6 * time.Minute), Values: []Value{{SeriesID: 1, Value: 1}}}}
	if err := ValidateMetricBatch(&batch, now); err == nil {
		t.Fatal("expected a future sample to be rejected")
	}
}
