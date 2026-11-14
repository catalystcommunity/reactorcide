package jobtelemetry

import (
	"context"
	"testing"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/objects"
)

var summaryBase = time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)

func at(seconds int) time.Time { return summaryBase.Add(time.Duration(seconds) * time.Second) }

func TestSummarizeKubernetesGaugesUsesRollupOnly(t *testing.T) {
	batch := MetricBatch{
		LeaseID: "lease-1",
		Series: []SeriesDefinition{
			{SeriesID: 0, Name: "cpu.utilization", Unit: "millicores", Kind: "gauge", Labels: []Label{{Key: "cpu", Value: "total"}}},
			{SeriesID: 1, Name: "cpu.utilization", Unit: "millicores", Kind: "gauge", Labels: []Label{{Key: "component", Value: "builder"}}},
			{SeriesID: 2, Name: "memory.usage", Unit: "bytes", Kind: "gauge"},
			{SeriesID: 3, Name: "cpu.request", Unit: "millicores", Kind: "gauge"},
			{SeriesID: 4, Name: "cpu.limit", Unit: "millicores", Kind: "gauge"},
			{SeriesID: 5, Name: "memory.limit", Unit: "bytes", Kind: "gauge"},
			// Memory is limit-only; a stray memory.request series is ignored.
			{SeriesID: 6, Name: "memory.request", Unit: "bytes", Kind: "gauge"},
		},
	}
	cpu := []int64{100, 300, 200, 900, 100}
	for i, value := range cpu {
		batch.Samples = append(batch.Samples, Sample{ObservedAt: at(i * 2), Values: []Value{
			{SeriesID: 0, Value: value},
			{SeriesID: 1, Value: 99999}, // a component: must not count
			{SeriesID: 2, Value: int64(1000 * (i + 1))},
			{SeriesID: 3, Value: 1000},
			{SeriesID: 4, Value: 2000},
			{SeriesID: 5, Value: 4 << 30},
			{SeriesID: 6, Value: 1 << 30},
		}})
	}
	summary := SummarizeBatches([]MetricBatch{batch})
	if summary.CPU == nil {
		t.Fatal("cpu not summarized")
	}
	if summary.CPU.Max != 900 || !summary.CPU.PeakAt.Equal(at(6)) {
		t.Fatalf("cpu max %d at %v", summary.CPU.Max, summary.CPU.PeakAt)
	}
	if summary.CPU.Avg != 320 {
		t.Fatalf("cpu avg %d, want 320", summary.CPU.Avg)
	}
	// Nearest rank of 5 samples at 95% is rank 5: the max.
	if summary.CPU.P95 != 900 {
		t.Fatalf("cpu p95 %d", summary.CPU.P95)
	}
	if summary.Memory == nil || summary.Memory.Max != 5000 || summary.Memory.Avg != 3000 {
		t.Fatalf("memory %+v", summary.Memory)
	}
	if summary.SampleCount != 5 {
		t.Fatalf("sample count %d", summary.SampleCount)
	}
	if *summary.CPURequestMillicores != 1000 || *summary.CPULimitMillicores != 2000 || *summary.MemoryLimitBytes != 4<<30 {
		t.Fatalf("requests/limits %+v", summary)
	}
	if summary.StorageMaxBytes != nil {
		t.Fatal("storage measured without storage series")
	}
}

func TestSummarizeDockerCounterRateDropsReset(t *testing.T) {
	batch := MetricBatch{
		LeaseID: "lease-1",
		Series:  []SeriesDefinition{{SeriesID: 0, Name: "cpu.usage", Unit: "nanoseconds", Kind: "counter", Labels: []Label{{Key: "cpu", Value: "total"}}}},
	}
	// 1s, 2s, 1s of CPU over 2s windows = 500, 1000, (reset dropped), 500 millicores.
	values := []int64{0, 1e9, 3e9, 1e8, 1.1e9}
	for i, value := range values {
		batch.Samples = append(batch.Samples, Sample{ObservedAt: at(i * 2), Values: []Value{{SeriesID: 0, Value: value}}})
	}
	summary := SummarizeBatches([]MetricBatch{batch})
	if summary.CPU == nil || summary.CPU.Count != 3 {
		t.Fatalf("cpu %+v", summary.CPU)
	}
	if summary.CPU.Max != 1000 || !summary.CPU.PeakAt.Equal(at(4)) {
		t.Fatalf("max %d at %v", summary.CPU.Max, summary.CPU.PeakAt)
	}
	if summary.CPU.Avg != 666 {
		t.Fatalf("avg %d", summary.CPU.Avg)
	}
	if summary.Memory != nil {
		t.Fatal("memory measured without memory series")
	}
}

func TestSummarizeCounterRateDoesNotSpanAttempts(t *testing.T) {
	series := []SeriesDefinition{{SeriesID: 0, Name: "cpu.usage", Unit: "nanoseconds", Kind: "counter"}}
	first := MetricBatch{LeaseID: "a", Series: series, Samples: []Sample{
		{ObservedAt: at(0), Values: []Value{{Value: 0}}},
		{ObservedAt: at(2), Values: []Value{{Value: 2e9}}},
	}}
	// A second attempt starts its counter at a high value; a rate across the
	// two attempts would be a huge false spike.
	second := MetricBatch{LeaseID: "b", Series: series, Samples: []Sample{
		{ObservedAt: at(4), Values: []Value{{Value: 50e9}}},
		{ObservedAt: at(6), Values: []Value{{Value: 51e9}}},
	}}
	summary := SummarizeBatches([]MetricBatch{second, first})
	if summary.CPU == nil || summary.CPU.Count != 2 || summary.CPU.Max != 1000 {
		t.Fatalf("cpu %+v", summary.CPU)
	}
}

func TestSummarizePrefersWorkingSetAndLegacyScope(t *testing.T) {
	batch := MetricBatch{
		LeaseID: "lease-1",
		Series: []SeriesDefinition{
			// Legacy Docker: scope=job + component=main is the roll-up.
			{SeriesID: 0, Name: "memory.usage", Unit: "bytes", Kind: "gauge", Labels: []Label{{Key: "scope", Value: "job"}, {Key: "component", Value: "main"}}},
			{SeriesID: 1, Name: "memory.working_set", Unit: "bytes", Kind: "gauge", Labels: []Label{{Key: "scope", Value: "job"}, {Key: "component", Value: "main"}}},
			// Legacy Kubernetes per-container: a component, must not count.
			{SeriesID: 2, Name: "memory.working_set", Unit: "bytes", Kind: "gauge", Labels: []Label{{Key: "scope", Value: "component"}, {Key: "component", Value: "main"}}},
		},
		Samples: []Sample{
			{ObservedAt: at(0), Values: []Value{{SeriesID: 0, Value: 900}, {SeriesID: 1, Value: 400}, {SeriesID: 2, Value: 7777}}},
			{ObservedAt: at(2), Values: []Value{{SeriesID: 0, Value: 1000}, {SeriesID: 1, Value: 600}, {SeriesID: 2, Value: 7777}}},
		},
	}
	summary := SummarizeBatches([]MetricBatch{batch})
	if summary.Memory == nil || summary.Memory.Max != 600 || summary.Memory.Avg != 500 {
		t.Fatalf("memory %+v", summary.Memory)
	}
}

func TestSummarizeStorageUsesVolumeTotalElseSum(t *testing.T) {
	k8s := MetricBatch{
		LeaseID: "lease-1",
		Series: []SeriesDefinition{
			{SeriesID: 0, Name: "storage.used", Unit: "bytes", Kind: "gauge", Labels: []Label{{Key: "volume", Value: "total"}, {Key: "kind", Value: "ephemeral"}}},
			{SeriesID: 1, Name: "storage.used", Unit: "bytes", Kind: "gauge", Labels: []Label{{Key: "volume", Value: "workspace"}, {Key: "kind", Value: "ephemeral"}}},
			{SeriesID: 2, Name: "storage.used", Unit: "bytes", Kind: "gauge", Labels: []Label{{Key: "volume", Value: "rootfs-job"}, {Key: "kind", Value: "rootfs"}, {Key: "component", Value: "main"}}},
		},
		Samples: []Sample{
			{ObservedAt: at(0), Values: []Value{{SeriesID: 0, Value: 500}, {SeriesID: 1, Value: 400}, {SeriesID: 2, Value: 50}}},
			{ObservedAt: at(10), Values: []Value{{SeriesID: 0, Value: 800}, {SeriesID: 1, Value: 700}, {SeriesID: 2, Value: 50}}},
		},
	}
	if got := SummarizeBatches([]MetricBatch{k8s}).StorageMaxBytes; got == nil || *got != 800 {
		t.Fatalf("k8s storage %v", got)
	}
	docker := MetricBatch{
		LeaseID: "lease-1",
		Series: []SeriesDefinition{
			{SeriesID: 0, Name: "storage.used", Unit: "bytes", Kind: "gauge", Labels: []Label{{Key: "volume", Value: "rootfs"}, {Key: "kind", Value: "rootfs"}}},
			{SeriesID: 1, Name: "storage.used", Unit: "bytes", Kind: "gauge", Labels: []Label{{Key: "volume", Value: "cache"}, {Key: "kind", Value: "ephemeral"}}},
		},
		Samples: []Sample{
			{ObservedAt: at(0), Values: []Value{{SeriesID: 0, Value: 100}, {SeriesID: 1, Value: 10}}},
			{ObservedAt: at(10), Values: []Value{{SeriesID: 0, Value: 300}, {SeriesID: 1, Value: 20}}},
		},
	}
	if got := SummarizeBatches([]MetricBatch{docker}).StorageMaxBytes; got == nil || *got != 320 {
		t.Fatalf("docker storage %v", got)
	}
}

func TestSummarizeP95NearestRank(t *testing.T) {
	values := map[time.Time]int64{}
	for i := 1; i <= 100; i++ {
		values[at(i)] = int64(i)
	}
	stat := statOf(values)
	if stat.P95 != 95 || stat.Max != 100 || stat.Avg != 50 {
		t.Fatalf("stat %+v", stat)
	}
	if statOf(map[time.Time]int64{}) != nil {
		t.Fatal("empty stat must be nil")
	}
}

func TestSummarizeJobEmptyStoreIsUnmeasured(t *testing.T) {
	summary, err := SummarizeJob(context.Background(), objects.NewMemoryObjectStore(), "job-1")
	if err != nil {
		t.Fatal(err)
	}
	if summary.JobID != "job-1" || summary.SampleCount != 0 || summary.CPU != nil || summary.Memory != nil {
		t.Fatalf("summary %+v", summary)
	}
}
