package workerapi

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/jobtelemetry"
	operationalmetrics "github.com/catalystcommunity/reactorcide/coordinator_api/internal/metrics"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/secrets"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/uiapi"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/workerapi/csilapi"
)

func (s *WorkerService) AppendLogBatch(ctx context.Context, req csilapi.AppendLogBatchRequest) (csilapi.AppendLogBatchResponse, error) {
	worker, _, err := s.resolveSession(ctx)
	if err != nil {
		return csilapi.AppendLogBatchResponse{}, err
	}
	lease, err := s.activeLeaseForWorker(ctx, req.LeaseId, worker.WorkerID)
	if err != nil {
		return csilapi.AppendLogBatchResponse{}, err
	}
	masker := secrets.NewMasker()
	masker.RegisterSecrets(s.secrets.get(lease.LeaseID))
	batch := jobtelemetry.LogBatch{
		LeaseID:  lease.LeaseID,
		Stream:   req.Stream,
		Sequence: req.Sequence,
		Entries:  make([]jobtelemetry.LogEntry, 0, len(req.Entries)),
	}
	for _, entry := range req.Entries {
		observedAt, parseErr := time.Parse(time.RFC3339Nano, entry.ObservedAt)
		if parseErr != nil {
			return csilapi.AppendLogBatchResponse{}, uiapi.NewServiceError("invalid_argument", "invalid log timestamp")
		}
		batch.Entries = append(batch.Entries, jobtelemetry.LogEntry{
			ObservedAt: observedAt.UTC(),
			Level:      entry.Level,
			Message:    masker.MaskString(entry.Message),
		})
	}
	if err := jobtelemetry.ValidateLogBatch(&batch); err != nil {
		operationalmetrics.TelemetryBatches.WithLabelValues("logs", "invalid").Inc()
		return csilapi.AppendLogBatchResponse{}, uiapi.NewServiceError("invalid_argument", err.Error())
	}
	started := time.Now()
	if err := jobtelemetry.PutLogBatch(ctx, s.deps.ObjectStore, lease.JobID, batch); err != nil {
		result := "failure"
		if errors.Is(err, jobtelemetry.ErrSequenceConflict) {
			result = "conflict"
			operationalmetrics.TelemetryBatches.WithLabelValues("logs", result).Inc()
			operationalmetrics.TelemetryWriteDuration.WithLabelValues("logs", result).Observe(time.Since(started).Seconds())
			return csilapi.AppendLogBatchResponse{}, uiapi.NewServiceError("conflict", "log batch sequence has different content")
		}
		operationalmetrics.TelemetryBatches.WithLabelValues("logs", result).Inc()
		operationalmetrics.TelemetryWriteDuration.WithLabelValues("logs", result).Observe(time.Since(started).Seconds())
		return csilapi.AppendLogBatchResponse{}, uiapi.NewServiceError("internal", "failed to persist log batch")
	}
	encoded, _ := json.Marshal(batch)
	operationalmetrics.TelemetryBatches.WithLabelValues("logs", "accepted").Inc()
	operationalmetrics.TelemetryBatchBytes.WithLabelValues("logs").Add(float64(len(encoded)))
	operationalmetrics.TelemetryWriteDuration.WithLabelValues("logs", "accepted").Observe(time.Since(started).Seconds())
	s.maybeCompactTelemetry(lease.JobID, req.Sequence)
	if s.deps.Publisher != nil {
		s.deps.Publisher.PublishLogAvailable(ctx, lease.JobID, req.Stream, req.Sequence, int64(len(req.Entries)))
	}
	return csilapi.AppendLogBatchResponse{Ok: true, AcceptedSequence: req.Sequence}, nil
}

func (s *WorkerService) AppendMetricBatch(ctx context.Context, req csilapi.AppendMetricBatchRequest) (csilapi.AppendMetricBatchResponse, error) {
	worker, _, err := s.resolveSession(ctx)
	if err != nil {
		return csilapi.AppendMetricBatchResponse{}, err
	}
	lease, err := s.activeLeaseForWorker(ctx, req.LeaseId, worker.WorkerID)
	if err != nil {
		return csilapi.AppendMetricBatchResponse{}, err
	}
	batch := jobtelemetry.MetricBatch{
		LeaseID:     lease.LeaseID,
		Sequence:    req.Sequence,
		Series:      make([]jobtelemetry.SeriesDefinition, 0, len(req.Series)),
		Samples:     make([]jobtelemetry.Sample, 0, len(req.Samples)),
		Unavailable: make([]jobtelemetry.Unavailable, 0, len(req.Unavailable)),
	}
	for _, definition := range req.Series {
		converted := jobtelemetry.SeriesDefinition{
			SeriesID: definition.SeriesId,
			Name:     definition.Name,
			Unit:     definition.Unit,
			Kind:     definition.Kind,
			Labels:   make([]jobtelemetry.Label, 0, len(definition.Labels)),
		}
		for _, label := range definition.Labels {
			converted.Labels = append(converted.Labels, jobtelemetry.Label{Key: label.Key, Value: label.Value})
		}
		batch.Series = append(batch.Series, converted)
	}
	for _, sample := range req.Samples {
		observedAt, parseErr := time.Parse(time.RFC3339Nano, sample.ObservedAt)
		if parseErr != nil {
			return csilapi.AppendMetricBatchResponse{}, uiapi.NewServiceError("invalid_argument", "invalid metric timestamp")
		}
		converted := jobtelemetry.Sample{ObservedAt: observedAt.UTC(), Values: make([]jobtelemetry.Value, 0, len(sample.Values))}
		for _, value := range sample.Values {
			converted.Values = append(converted.Values, jobtelemetry.Value{SeriesID: value.SeriesId, Value: value.Value})
		}
		batch.Samples = append(batch.Samples, converted)
	}
	for _, unavailable := range req.Unavailable {
		batch.Unavailable = append(batch.Unavailable, jobtelemetry.Unavailable{
			MetricPrefix: unavailable.MetricPrefix,
			Reason:       unavailable.Reason,
		})
	}
	for _, sample := range batch.Samples {
		if !lease.AcquiredAt.IsZero() && sample.ObservedAt.Before(lease.AcquiredAt.Add(-5*time.Minute)) {
			operationalmetrics.TelemetryBatches.WithLabelValues("metrics", "invalid").Inc()
			return csilapi.AppendMetricBatchResponse{}, uiapi.NewServiceError("invalid_argument", "metric timestamp is before the lease")
		}
	}
	if err := jobtelemetry.ValidateMetricBatch(&batch, time.Now().UTC()); err != nil {
		operationalmetrics.TelemetryBatches.WithLabelValues("metrics", "invalid").Inc()
		return csilapi.AppendMetricBatchResponse{}, uiapi.NewServiceError("invalid_argument", err.Error())
	}
	started := time.Now()
	if err := jobtelemetry.PutMetricBatch(ctx, s.deps.ObjectStore, lease.JobID, batch); err != nil {
		result := "failure"
		if errors.Is(err, jobtelemetry.ErrSequenceConflict) {
			result = "conflict"
			operationalmetrics.TelemetryBatches.WithLabelValues("metrics", result).Inc()
			operationalmetrics.TelemetryWriteDuration.WithLabelValues("metrics", result).Observe(time.Since(started).Seconds())
			return csilapi.AppendMetricBatchResponse{}, uiapi.NewServiceError("conflict", "metric batch sequence has different content")
		}
		operationalmetrics.TelemetryBatches.WithLabelValues("metrics", result).Inc()
		operationalmetrics.TelemetryWriteDuration.WithLabelValues("metrics", result).Observe(time.Since(started).Seconds())
		return csilapi.AppendMetricBatchResponse{}, uiapi.NewServiceError("internal", "failed to persist metric batch")
	}
	encoded, _ := json.Marshal(batch)
	operationalmetrics.TelemetryBatches.WithLabelValues("metrics", "accepted").Inc()
	operationalmetrics.TelemetryBatchBytes.WithLabelValues("metrics").Add(float64(len(encoded)))
	operationalmetrics.TelemetryWriteDuration.WithLabelValues("metrics", "accepted").Observe(time.Since(started).Seconds())
	s.maybeCompactTelemetry(lease.JobID, req.Sequence)
	if s.deps.Publisher != nil && len(batch.Samples) > 0 {
		s.deps.Publisher.PublishMetricsAvailable(
			ctx,
			lease.JobID,
			batch.Samples[0].ObservedAt.Format(time.RFC3339Nano),
			batch.Samples[len(batch.Samples)-1].ObservedAt.Format(time.RFC3339Nano),
			req.Sequence,
		)
	}
	return csilapi.AppendMetricBatchResponse{Ok: true, AcceptedSequence: req.Sequence}, nil
}

func (s *WorkerService) maybeCompactTelemetry(jobID string, sequence int64) {
	if sequence < jobtelemetry.CompactionThreshold-1 || (sequence+1)%jobtelemetry.CompactionThreshold != 0 {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := jobtelemetry.CompactJob(ctx, s.deps.ObjectStore, jobID, false); err != nil {
			operationalmetrics.TelemetryBatches.WithLabelValues("compaction", "failure").Inc()
		}
	}()
}

func (s *WorkerService) activeLeaseForWorker(ctx context.Context, leaseID, workerID string) (*models.WorkerLease, error) {
	lease, err := s.deps.Store.GetWorkerLeaseByID(ctx, leaseID)
	if err != nil || lease.WorkerID != workerID {
		return nil, uiapi.NewServiceError("not_found", "lease not found for this worker")
	}
	if !lease.IsActive() {
		return nil, uiapi.NewServiceError("conflict", "lease is no longer active")
	}
	if s.deps.ObjectStore == nil {
		return nil, uiapi.NewServiceError("internal", "object storage is not configured")
	}
	return lease, nil
}
