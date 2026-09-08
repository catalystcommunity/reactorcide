package coordinatorworker

import (
	"context"
	"reflect"
	"time"

	"github.com/catalystcommunity/app-utils-go/logging"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/jobtelemetry"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/worker"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/workerclient/csilapi"
)

func pumpMetrics(ctx context.Context, c client, runner worker.JobRunner, leaseID, runnerID string, cfg Config) {
	interval := cfg.MetricsInterval
	if interval <= 0 {
		interval = 2 * time.Second
	}
	storageInterval := cfg.StorageMetricsInterval
	if storageInterval <= 0 {
		storageInterval = 10 * time.Second
	}
	sendInterval := cfg.TelemetrySendInterval
	if sendInterval <= 0 {
		sendInterval = 10 * time.Second
	}
	sampleTicker := time.NewTicker(interval)
	defer sampleTicker.Stop()
	storageTicker := time.NewTicker(storageInterval)
	defer storageTicker.Stop()
	sendTicker := time.NewTicker(sendInterval)
	defer sendTicker.Stop()

	var sequence int64
	var definitions []csilapi.MetricSeriesDefinition
	var samples []csilapi.MetricSample
	unavailable := map[string]csilapi.MetricUnavailable{}
	bufferGap := false
	flush := func() {
		if len(samples) == 0 && len(unavailable) == 0 {
			return
		}
		unavailableItems := make([]csilapi.MetricUnavailable, 0, len(unavailable))
		if bufferGap {
			unavailable["telemetry.buffer\x00buffer_gap"] = csilapi.MetricUnavailable{MetricPrefix: "telemetry.buffer", Reason: "buffer_gap"}
			bufferGap = false
		}
		for _, item := range unavailable {
			unavailableItems = append(unavailableItems, item)
		}
		req := csilapi.AppendMetricBatchRequest{
			LeaseId: leaseID, Sequence: sequence, Series: definitions,
			Samples: samples, Unavailable: unavailableItems,
		}
		sequence++
		dropped, err := persistAndSendMetrics(c, cfg.DataDir, cfg.TelemetryBufferBatches, req)
		if dropped {
			bufferGap = true
			logging.Log.WithField("lease_id", leaseID).Warn("dropped the oldest unsent telemetry batch because the buffer is full")
		}
		if err != nil {
			logging.Log.WithError(err).WithField("lease_id", leaseID).Warn("failed to append metrics to coordinator")
		}
		definitions = nil
		samples = nil
		unavailable = map[string]csilapi.MetricUnavailable{}
	}
	collect := func(sampleCtx context.Context, includeStorage bool) {
		snapshot, err := runner.SampleResources(sampleCtx, runnerID, worker.ResourceSampleOptions{IncludeStorage: includeStorage})
		if err != nil {
			// A failed snapshot is "no value this time", not a statement about
			// the runtime. The first samples after SpawnJob race the pod (or
			// container) becoming listable, and the final sample after the
			// job ends races its removal. The query layer drops this reason
			// once the lease has a real sample for the family, so it only
			// reaches the UI for a job that never produced one.
			prefixes := []string{"cpu.utilization", "memory.usage"}
			if includeStorage {
				prefixes = append(prefixes, "storage.used")
			}
			for _, prefix := range prefixes {
				unavailable[prefix+"\x00temporarily_unavailable"] = csilapi.MetricUnavailable{MetricPrefix: prefix, Reason: "temporarily_unavailable"}
			}
			return
		}
		convertedDefinitions := convertMetricDefinitions(snapshot.Series)
		if len(definitions) > 0 && !reflect.DeepEqual(definitions, convertedDefinitions) {
			flush()
		}
		definitions = convertedDefinitions
		observedAt := snapshot.ObservedAt
		if observedAt.IsZero() {
			observedAt = time.Now().UTC()
		}
		values := make([]csilapi.MetricValue, 0, len(snapshot.Values))
		for _, value := range snapshot.Values {
			values = append(values, csilapi.MetricValue{SeriesId: value.SeriesID, Value: value.Value})
		}
		if len(values) > 0 {
			samples = append(samples, csilapi.MetricSample{ObservedAt: observedAt.UTC().Format(time.RFC3339Nano), Values: values})
		}
		for _, item := range snapshot.Unavailable {
			unavailable[item.MetricPrefix+"\x00"+item.Reason] = csilapi.MetricUnavailable{MetricPrefix: item.MetricPrefix, Reason: item.Reason}
		}
	}

	collect(ctx, true)
	for {
		select {
		case <-ctx.Done():
			finalCtx, cancel := context.WithTimeout(context.Background(), interval)
			collect(finalCtx, true)
			cancel()
			flush()
			return
		case <-sampleTicker.C:
			collect(ctx, false)
			if len(samples) >= jobtelemetry.MaxSamplesPerBatch {
				flush()
			}
		case <-storageTicker.C:
			collect(ctx, true)
			if len(samples) >= jobtelemetry.MaxSamplesPerBatch {
				flush()
			}
		case <-sendTicker.C:
			flush()
		}
	}
}

func convertMetricDefinitions(input []jobtelemetry.SeriesDefinition) []csilapi.MetricSeriesDefinition {
	output := make([]csilapi.MetricSeriesDefinition, 0, len(input))
	for _, definition := range input {
		converted := csilapi.MetricSeriesDefinition{
			SeriesId: definition.SeriesID,
			Name:     definition.Name,
			Unit:     definition.Unit,
			Kind:     definition.Kind,
			Labels:   make([]csilapi.MetricLabel, 0, len(definition.Labels)),
		}
		for _, label := range definition.Labels {
			converted.Labels = append(converted.Labels, csilapi.MetricLabel{Key: label.Key, Value: label.Value})
		}
		output = append(output, converted)
	}
	return output
}
