package jobtelemetry

import (
	"context"
	"testing"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/objects"
)

// The query's availability rules, case by case. The defect behind these: a
// warning recorded once (metrics-server had no sample for a two-second-old
// pod) stayed on the job forever, next to ten minutes of perfectly good
// samples, and it named cpu.usage while the samples were cpu.utilization.

var (
	cpuUtil = SeriesDefinition{SeriesID: 1, Name: "cpu.utilization", Unit: "millicores", Kind: "gauge", Labels: []Label{{Key: "cpu", Value: "total"}}}
	memUse  = SeriesDefinition{SeriesID: 2, Name: "memory.usage", Unit: "bytes", Kind: "gauge"}
	cpuReq  = SeriesDefinition{SeriesID: 3, Name: "cpu.request", Unit: "millicores", Kind: "gauge"}
	storUse = SeriesDefinition{SeriesID: 4, Name: "storage.used", Unit: "bytes", Kind: "gauge", Labels: []Label{{Key: "volume", Value: "total"}, {Key: "kind", Value: "ephemeral"}}}
	cpuCtr  = SeriesDefinition{SeriesID: 5, Name: "cpu.usage", Unit: "nanoseconds", Kind: "counter", Labels: []Label{{Key: "cpu", Value: "total"}}}
)

func put(t *testing.T, store objects.ObjectStore, job string, batch MetricBatch) {
	t.Helper()
	if err := PutMetricBatch(context.Background(), store, job, batch); err != nil {
		t.Fatal(err)
	}
}

func sample(at time.Time, values ...Value) Sample {
	return Sample{ObservedAt: at, Values: values}
}

func unavailableSet(response QueryResponse) map[string]string {
	out := map[string]string{}
	for _, item := range response.Unavailable {
		out[item.MetricPrefix] = item.Reason
	}
	return out
}

func mustQuery(t *testing.T, store objects.ObjectStore, query Query) QueryResponse {
	t.Helper()
	response, err := QueryMetrics(context.Background(), store, query)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

// Case 6: a later memory sample suppresses the earlier memory warning. The
// startup batch carries only resource settings (cpu.request) and the
// warning; cpu.request is not in the memory.usage family and must not count.
func TestAvailability_LaterMemorySampleSuppressesWarning(t *testing.T) {
	store := objects.NewMemoryObjectStore()
	now := time.Now().UTC().Add(-time.Minute)
	put(t, store, "job", MetricBatch{
		LeaseID: "lease-1", Sequence: 0,
		Series:      []SeriesDefinition{cpuReq},
		Samples:     []Sample{sample(now, Value{SeriesID: 3, Value: 500})},
		Unavailable: []Unavailable{{MetricPrefix: "memory.usage", Reason: "temporarily_unavailable"}, {MetricPrefix: "cpu.utilization", Reason: "temporarily_unavailable"}},
	})
	afterStartup := mustQuery(t, store, Query{JobID: "job"})
	if got := unavailableSet(afterStartup); got["memory.usage"] != "temporarily_unavailable" || got["cpu.utilization"] != "temporarily_unavailable" {
		t.Fatalf("before any sample the warnings must stand, got %v", got)
	}

	put(t, store, "job", MetricBatch{
		LeaseID: "lease-1", Sequence: 1,
		Series:  []SeriesDefinition{cpuReq, memUse},
		Samples: []Sample{sample(now.Add(2*time.Second), Value{SeriesID: 3, Value: 500}, Value{SeriesID: 2, Value: 1 << 20})},
	})
	got := unavailableSet(mustQuery(t, store, Query{JobID: "job"}))
	if _, still := got["memory.usage"]; still {
		t.Fatalf("a memory sample must clear the memory warning, got %v", got)
	}
	if got["cpu.utilization"] != "temporarily_unavailable" {
		t.Fatalf("the cpu warning has no contradicting sample and must remain, got %v", got)
	}
}

// Case 7: stored telemetry from before the prefix fix says cpu.usage. A
// cpu.utilization sample (Kubernetes) clears it, and so does a cpu.usage
// counter (Docker), which the query renames to cpu.utilization. Either way
// the response names the canonical family.
func TestAvailability_CPUUtilizationSampleSuppressesHistoricalCPUUsageWarning(t *testing.T) {
	now := time.Now().UTC().Add(-time.Minute)

	t.Run("kubernetes gauge", func(t *testing.T) {
		store := objects.NewMemoryObjectStore()
		put(t, store, "job", MetricBatch{
			LeaseID: "lease-1", Sequence: 0,
			Unavailable: []Unavailable{{MetricPrefix: "cpu.usage", Reason: "metric_api_not_installed"}},
		})
		before := unavailableSet(mustQuery(t, store, Query{JobID: "job"}))
		if before["cpu.utilization"] != "metric_api_not_installed" {
			t.Fatalf("an old cpu.usage warning must be reported under the canonical cpu.utilization name, got %v", before)
		}
		if _, old := before["cpu.usage"]; old {
			t.Fatalf("cpu.usage must not appear as a prefix, got %v", before)
		}
		put(t, store, "job", MetricBatch{
			LeaseID: "lease-1", Sequence: 1,
			Series:  []SeriesDefinition{cpuUtil},
			Samples: []Sample{sample(now, Value{SeriesID: 1, Value: 250})},
		})
		if got := unavailableSet(mustQuery(t, store, Query{JobID: "job"})); len(got) != 0 {
			t.Fatalf("a cpu.utilization sample must clear the historical cpu.usage warning, got %v", got)
		}
	})

	t.Run("docker counter", func(t *testing.T) {
		store := objects.NewMemoryObjectStore()
		put(t, store, "job", MetricBatch{
			LeaseID: "lease-1", Sequence: 0,
			Unavailable: []Unavailable{{MetricPrefix: "cpu.usage", Reason: "runtime_not_supported"}},
		})
		put(t, store, "job", MetricBatch{
			LeaseID: "lease-1", Sequence: 1,
			Series:  []SeriesDefinition{cpuCtr},
			Samples: []Sample{sample(now, Value{SeriesID: 5, Value: 1_000_000_000}), sample(now.Add(time.Second), Value{SeriesID: 5, Value: 1_500_000_000})},
		})
		response := mustQuery(t, store, Query{JobID: "job"})
		if got := unavailableSet(response); len(got) != 0 {
			t.Fatalf("a cpu.usage counter sample must clear the warning, got %v", got)
		}
		if len(response.Series) != 1 || response.Series[0].Name != "cpu.utilization" {
			t.Fatalf("counter should still render as cpu.utilization, got %+v", response.Series)
		}
	})
}

// Case 8: a storage sample suppresses an earlier storage warning.
func TestAvailability_StorageSampleSuppressesWarning(t *testing.T) {
	store := objects.NewMemoryObjectStore()
	now := time.Now().UTC().Add(-time.Minute)
	put(t, store, "job", MetricBatch{
		LeaseID: "lease-1", Sequence: 0,
		Series:      []SeriesDefinition{cpuUtil},
		Samples:     []Sample{sample(now, Value{SeriesID: 1, Value: 100})},
		Unavailable: []Unavailable{{MetricPrefix: "storage.used", Reason: "temporarily_unavailable"}},
	})
	put(t, store, "job", MetricBatch{
		LeaseID: "lease-1", Sequence: 1,
		Series:  []SeriesDefinition{cpuUtil, storUse},
		Samples: []Sample{sample(now.Add(10*time.Second), Value{SeriesID: 1, Value: 100}, Value{SeriesID: 4, Value: 4096})},
	})
	if got := unavailableSet(mustQuery(t, store, Query{JobID: "job"})); len(got) != 0 {
		t.Fatalf("a storage.used sample must clear the storage warning, got %v", got)
	}
}

// Case 9: a job that ends before any resource sample arrives keeps its
// warnings. Nothing contradicts them.
func TestAvailability_ShortJobWithoutSamplesKeepsWarning(t *testing.T) {
	store := objects.NewMemoryObjectStore()
	now := time.Now().UTC().Add(-time.Minute)
	put(t, store, "job", MetricBatch{
		LeaseID: "lease-1", Sequence: 0,
		Series:  []SeriesDefinition{cpuReq},
		Samples: []Sample{sample(now, Value{SeriesID: 3, Value: 500}), sample(now.Add(time.Second), Value{SeriesID: 3, Value: 500})},
		Unavailable: []Unavailable{
			{MetricPrefix: "cpu.utilization", Reason: "temporarily_unavailable"},
			{MetricPrefix: "memory.usage", Reason: "temporarily_unavailable"},
			{MetricPrefix: "storage.used", Reason: "temporarily_unavailable"},
		},
	})
	got := unavailableSet(mustQuery(t, store, Query{JobID: "job"}))
	for _, prefix := range []string{"cpu.utilization", "memory.usage", "storage.used"} {
		if got[prefix] != "temporarily_unavailable" {
			t.Fatalf("%s warning must survive for a job with no sample, got %v", prefix, got)
		}
	}
	// A time window that includes the batch still reports it; one that lies
	// after the job's samples does not.
	inWindow := Query{JobID: "job", From: timePtr(now.Add(-time.Second)), To: timePtr(now.Add(2 * time.Second))}
	if got := unavailableSet(mustQuery(t, store, inWindow)); len(got) != 3 {
		t.Fatalf("in-window query must keep the warnings, got %v", got)
	}
	later := Query{JobID: "job", From: timePtr(now.Add(time.Hour))}
	if got := unavailableSet(mustQuery(t, store, later)); len(got) != 0 {
		t.Fatalf("a window after the job must not report its startup warnings, got %v", got)
	}
}

// Case 10: time filters select both samples and availability. A warning
// recorded at startup is not reported for a window after the samples
// began, and a sample outside the window does not suppress a warning
// inside it.
func TestAvailability_TimeFilteredQueryUsesOnlyRelevantInformation(t *testing.T) {
	store := objects.NewMemoryObjectStore()
	start := time.Now().UTC().Add(-10 * time.Minute)
	// t+0: startup, no memory yet.
	put(t, store, "job", MetricBatch{
		LeaseID: "lease-1", Sequence: 0,
		Series:      []SeriesDefinition{cpuReq},
		Samples:     []Sample{sample(start, Value{SeriesID: 3, Value: 500})},
		Unavailable: []Unavailable{{MetricPrefix: "memory.usage", Reason: "temporarily_unavailable"}},
	})
	// t+1m..t+2m: memory flowing.
	put(t, store, "job", MetricBatch{
		LeaseID: "lease-1", Sequence: 1,
		Series: []SeriesDefinition{cpuReq, memUse},
		Samples: []Sample{
			sample(start.Add(time.Minute), Value{SeriesID: 3, Value: 500}, Value{SeriesID: 2, Value: 10}),
			sample(start.Add(2*time.Minute), Value{SeriesID: 3, Value: 500}, Value{SeriesID: 2, Value: 20}),
		},
	})
	// t+5m: metrics-server gone; memory unavailable again, cpu.request still sampled.
	put(t, store, "job", MetricBatch{
		LeaseID: "lease-1", Sequence: 2,
		Series:      []SeriesDefinition{cpuReq},
		Samples:     []Sample{sample(start.Add(5*time.Minute), Value{SeriesID: 3, Value: 500})},
		Unavailable: []Unavailable{{MetricPrefix: "memory.usage", Reason: "temporarily_unavailable"}},
	})

	whole := mustQuery(t, store, Query{JobID: "job"})
	if got := unavailableSet(whole); len(got) != 0 {
		t.Fatalf("over the whole job, memory has samples, so no warning: got %v", got)
	}

	// Only the outage window: memory has no sample there, the warning stands,
	// and the earlier samples must not leak in.
	outage := mustQuery(t, store, Query{JobID: "job", From: timePtr(start.Add(4 * time.Minute)), To: timePtr(start.Add(6 * time.Minute))})
	if got := unavailableSet(outage); got["memory.usage"] != "temporarily_unavailable" {
		t.Fatalf("outage window must report the memory warning, got %v", got)
	}
	for _, series := range outage.Series {
		if series.Name == "memory.usage" {
			t.Fatalf("no memory samples belong to the outage window, got %+v", series)
		}
	}

	// Only the healthy window: the startup warning is placed at t+0 and the
	// outage warning at t+5m, neither inside the window.
	healthy := mustQuery(t, store, Query{JobID: "job", From: timePtr(start.Add(30 * time.Second)), To: timePtr(start.Add(3 * time.Minute))})
	if got := unavailableSet(healthy); len(got) != 0 {
		t.Fatalf("healthy window must report nothing, got %v", got)
	}
}

// Case 11: a retry on a forbidden worker keeps its warning even though the
// first attempt collected fine. Suppression is per lease.
func TestAvailability_MultipleLeasesKeepARealWarning(t *testing.T) {
	store := objects.NewMemoryObjectStore()
	now := time.Now().UTC().Add(-time.Minute)
	put(t, store, "job", MetricBatch{
		LeaseID: "lease-a", Sequence: 0,
		Series:  []SeriesDefinition{cpuUtil, memUse},
		Samples: []Sample{sample(now, Value{SeriesID: 1, Value: 100}, Value{SeriesID: 2, Value: 10})},
	})
	put(t, store, "job", MetricBatch{
		LeaseID: "lease-b", Sequence: 0,
		Series:      []SeriesDefinition{cpuReq},
		Samples:     []Sample{sample(now.Add(30*time.Second), Value{SeriesID: 3, Value: 500})},
		Unavailable: []Unavailable{{MetricPrefix: "cpu.utilization", Reason: "permission_denied"}, {MetricPrefix: "memory.usage", Reason: "permission_denied"}},
	})
	got := unavailableSet(mustQuery(t, store, Query{JobID: "job"}))
	if got["cpu.utilization"] != "permission_denied" || got["memory.usage"] != "permission_denied" {
		t.Fatalf("lease-b's permission_denied must not be hidden by lease-a's samples, got %v", got)
	}

	// And the other way round: lease-b's startup hiccup, cleared by lease-b's
	// own later samples, while lease-a never collected memory.
	store2 := objects.NewMemoryObjectStore()
	put(t, store2, "job", MetricBatch{
		LeaseID: "lease-a", Sequence: 0,
		Series:      []SeriesDefinition{cpuUtil},
		Samples:     []Sample{sample(now, Value{SeriesID: 1, Value: 100})},
		Unavailable: []Unavailable{{MetricPrefix: "memory.usage", Reason: "runtime_not_supported"}},
	})
	put(t, store2, "job", MetricBatch{
		LeaseID: "lease-b", Sequence: 0,
		Unavailable: []Unavailable{{MetricPrefix: "memory.usage", Reason: "temporarily_unavailable"}},
	})
	put(t, store2, "job", MetricBatch{
		LeaseID: "lease-b", Sequence: 1,
		Series:  []SeriesDefinition{memUse},
		Samples: []Sample{sample(now.Add(time.Minute), Value{SeriesID: 2, Value: 10})},
	})
	got = unavailableSet(mustQuery(t, store2, Query{JobID: "job"}))
	if got["memory.usage"] != "runtime_not_supported" {
		t.Fatalf("lease-a's real memory warning must remain and lease-b's transient one must go, got %v", got)
	}
}

// Case 12: telemetry.buffer warnings are never contradicted by a sample,
// because no series carries that prefix.
func TestAvailability_BufferGapRemainsVisible(t *testing.T) {
	store := objects.NewMemoryObjectStore()
	now := time.Now().UTC().Add(-time.Minute)
	put(t, store, "job", MetricBatch{
		LeaseID: "lease-1", Sequence: 0,
		Series:      []SeriesDefinition{cpuUtil, memUse, storUse},
		Samples:     []Sample{sample(now, Value{SeriesID: 1, Value: 1}, Value{SeriesID: 2, Value: 2}, Value{SeriesID: 4, Value: 3})},
		Unavailable: []Unavailable{{MetricPrefix: "telemetry.buffer", Reason: "buffer_gap"}, {MetricPrefix: "cpu.utilization", Reason: "temporarily_unavailable"}},
	})
	got := unavailableSet(mustQuery(t, store, Query{JobID: "job"}))
	if got["telemetry.buffer"] != "buffer_gap" {
		t.Fatalf("buffer_gap must stay visible, got %v", got)
	}
	if _, cpu := got["cpu.utilization"]; cpu {
		t.Fatalf("the cpu warning is contradicted by a sample in the same batch, got %v", got)
	}
}

// An unrelated reason on a family that has samples is still suppressed only
// for its own family: not_applicable on storage stays when storage has no
// sample, whatever cpu and memory do.
func TestAvailability_UnrelatedFamilyWarningIsUntouched(t *testing.T) {
	store := objects.NewMemoryObjectStore()
	now := time.Now().UTC().Add(-time.Minute)
	put(t, store, "job", MetricBatch{
		LeaseID: "lease-1", Sequence: 0,
		Series:      []SeriesDefinition{cpuUtil, memUse},
		Samples:     []Sample{sample(now, Value{SeriesID: 1, Value: 1}, Value{SeriesID: 2, Value: 2})},
		Unavailable: []Unavailable{{MetricPrefix: "storage.used", Reason: "not_applicable"}},
	})
	if got := unavailableSet(mustQuery(t, store, Query{JobID: "job"})); got["storage.used"] != "not_applicable" {
		t.Fatalf("storage warning must remain, got %v", got)
	}
}

func timePtr(t time.Time) *time.Time { return &t }
