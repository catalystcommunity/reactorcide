package coordinatorworker

import (
	"context"
	"testing"
	"time"
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
