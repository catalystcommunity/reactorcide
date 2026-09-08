package coordinatorworker

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/jobtelemetry"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/worker"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/workerclient/csilapi"
)

func TestPumpMetricsUsesSlowerStorageInterval(t *testing.T) {
	runner := &fakeRunner{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		pumpMetrics(ctx, &fakeClient{}, runner, "lease", "runner", Config{
			MetricsInterval:        5 * time.Millisecond,
			StorageMetricsInterval: 20 * time.Millisecond,
			TelemetrySendInterval:  time.Hour,
		})
	}()

	deadline := time.Now().Add(time.Second)
	for {
		runner.mu.Lock()
		options := make([]bool, len(runner.SampleOptions))
		for index, option := range runner.SampleOptions {
			options[index] = option.IncludeStorage
		}
		runner.mu.Unlock()
		hasFast := false
		storageSamples := 0
		for _, includeStorage := range options {
			if includeStorage {
				storageSamples++
			} else {
				hasFast = true
			}
		}
		if hasFast && storageSamples >= 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("sample options = %v", options)
		}
		time.Sleep(time.Millisecond)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("metrics pump did not stop")
	}
}

// A snapshot that fails outright (the pod is not listable yet, the container
// is already gone) is reported as temporarily unavailable under the names of
// the series the collectors emit. It used to be runtime_not_supported under
// cpu.usage, which the UI then read as a permanent statement about the
// runtime, and which no later cpu.utilization sample could clear.
func TestPumpMetricsReportsFailedSnapshotAsTemporarilyUnavailable(t *testing.T) {
	var mu sync.Mutex
	var batches []csilapi.AppendMetricBatchRequest
	c := &fakeClient{AppendMetricBatchFunc: func(_ context.Context, req csilapi.AppendMetricBatchRequest) (csilapi.AppendMetricBatchResponse, error) {
		mu.Lock()
		batches = append(batches, req)
		mu.Unlock()
		return csilapi.AppendMetricBatchResponse{Ok: true, AcceptedSequence: req.Sequence}, nil
	}}
	calls := 0
	runner := &fakeRunner{SampleFunc: func(options worker.ResourceSampleOptions) (worker.ResourceSnapshot, error) {
		calls++
		if calls == 1 {
			return worker.ResourceSnapshot{}, errors.New("job pod is not available")
		}
		return worker.ResourceSnapshot{
			ObservedAt: time.Now().UTC(),
			Series:     []jobtelemetry.SeriesDefinition{{SeriesID: 0, Name: "cpu.utilization", Unit: "millicores", Kind: "gauge"}},
			Values:     []jobtelemetry.Value{{SeriesID: 0, Value: 5}},
		}, nil
	}}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		pumpMetrics(ctx, c, runner, "lease", "runner", Config{
			MetricsInterval: 5 * time.Millisecond, StorageMetricsInterval: time.Hour, TelemetrySendInterval: time.Hour,
			DataDir: t.TempDir(),
		})
	}()
	deadline := time.Now().Add(time.Second)
	for {
		runner.mu.Lock()
		n := len(runner.SampleOptions)
		runner.mu.Unlock()
		if n >= 3 || time.Now().After(deadline) {
			break
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	<-done

	mu.Lock()
	defer mu.Unlock()
	if len(batches) == 0 {
		t.Fatal("expected at least one flushed batch")
	}
	reasons := map[string]string{}
	for _, batch := range batches {
		for _, item := range batch.Unavailable {
			reasons[item.MetricPrefix] = item.Reason
		}
	}
	if reasons["cpu.utilization"] != "temporarily_unavailable" || reasons["memory.usage"] != "temporarily_unavailable" || reasons["storage.used"] != "temporarily_unavailable" {
		t.Fatalf("unavailable = %v, want temporarily_unavailable for cpu.utilization, memory.usage and storage.used", reasons)
	}
	if _, old := reasons["cpu.usage"]; old {
		t.Fatalf("the pump must name cpu.utilization, not cpu.usage: %v", reasons)
	}
	for _, batch := range batches {
		for _, item := range batch.Unavailable {
			if item.Reason == "runtime_not_supported" {
				t.Fatalf("a failed snapshot is not a runtime limitation: %+v", batch.Unavailable)
			}
		}
	}
}
