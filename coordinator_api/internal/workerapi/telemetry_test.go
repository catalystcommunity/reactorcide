package workerapi

import (
	"testing"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/jobtelemetry"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/workerapi/csilapi"
)

func TestAppendMetricBatchStoresQueryableMetricsAndIsIdempotent(t *testing.T) {
	h := newTestHarness()
	job, leaseID, token := claimOneJob(t, h)
	now := time.Now().UTC()
	req := csilapi.AppendMetricBatchRequest{
		LeaseId: leaseID, Sequence: 0,
		Series:  []csilapi.MetricSeriesDefinition{{SeriesId: 1, Name: "memory.usage", Unit: "bytes", Kind: "gauge", Labels: []csilapi.MetricLabel{{Key: "scope", Value: "job"}}}},
		Samples: []csilapi.MetricSample{{ObservedAt: now.Format(time.RFC3339Nano), Values: []csilapi.MetricValue{{SeriesId: 1, Value: 4096}}}},
	}
	for i := 0; i < 2; i++ {
		response, err := h.service.AppendMetricBatch(ctxWithAuth(token), req)
		if err != nil || !response.Ok || response.AcceptedSequence != 0 {
			t.Fatalf("append %d failed: response=%+v err=%v", i, response, err)
		}
	}
	query, err := jobtelemetry.QueryMetrics(ctxWithAuth(token), h.objectStore, jobtelemetry.Query{JobID: job.JobID})
	if err != nil || len(query.Series) != 1 || query.Series[0].Points[0].Value != 4096 {
		t.Fatalf("unexpected query: response=%+v err=%v", query, err)
	}
}

func TestAppendMetricBatchRejectsConflictMalformedAndForeignLease(t *testing.T) {
	h := newTestHarness()
	_, leaseID, token := claimOneJob(t, h)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	valid := csilapi.AppendMetricBatchRequest{LeaseId: leaseID, Sequence: 3,
		Series:  []csilapi.MetricSeriesDefinition{{SeriesId: 1, Name: "memory.usage", Unit: "bytes", Kind: "gauge"}},
		Samples: []csilapi.MetricSample{{ObservedAt: now, Values: []csilapi.MetricValue{{SeriesId: 1, Value: 1}}}},
	}
	if _, err := h.service.AppendMetricBatch(ctxWithAuth(token), valid); err != nil {
		t.Fatal(err)
	}
	changed := valid
	changed.Samples = []csilapi.MetricSample{{ObservedAt: now, Values: []csilapi.MetricValue{{SeriesId: 1, Value: 2}}}}
	if _, err := h.service.AppendMetricBatch(ctxWithAuth(token), changed); err == nil {
		t.Fatal("expected a sequence conflict")
	}
	malformed := valid
	malformed.Sequence = 4
	malformed.Series = []csilapi.MetricSeriesDefinition{{SeriesId: 1, Name: "bad name", Unit: "bytes", Kind: "gauge"}}
	if _, err := h.service.AppendMetricBatch(ctxWithAuth(token), malformed); err == nil {
		t.Fatal("expected malformed metrics to be rejected")
	}
	otherToken, _ := h.registerWorker(t, "other-telemetry-worker", "linux", "amd64", nil)
	valid.Sequence = 5
	if _, err := h.service.AppendMetricBatch(ctxWithAuth(otherToken), valid); err == nil {
		t.Fatal("expected a foreign lease to be rejected")
	}
}
