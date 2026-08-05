package metrics

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	// Job metrics
	JobsSubmitted = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "reactorcide_jobs_submitted_total",
			Help: "Total number of jobs submitted",
		},
		[]string{"queue", "source_type"},
	)

	// Corndogs metrics
	CornDogsTaskSubmissions = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "reactorcide_corndogs_task_submissions_total",
			Help: "Total number of tasks submitted to Corndogs",
		},
		[]string{"queue", "result"},
	)

	TelemetryBatches = promauto.NewCounterVec(
		prometheus.CounterOpts{Name: "reactorcide_telemetry_batches_total", Help: "Telemetry batches by kind and result"},
		[]string{"kind", "result"},
	)
	TelemetryBatchBytes = promauto.NewCounterVec(
		prometheus.CounterOpts{Name: "reactorcide_telemetry_batch_bytes_total", Help: "Accepted telemetry bytes by kind"},
		[]string{"kind"},
	)
	TelemetryWriteDuration = promauto.NewHistogramVec(
		prometheus.HistogramOpts{Name: "reactorcide_telemetry_write_duration_seconds", Help: "Telemetry object write duration", Buckets: prometheus.DefBuckets},
		[]string{"kind", "result"},
	)
	TelemetryQueryDuration = promauto.NewHistogramVec(
		prometheus.HistogramOpts{Name: "reactorcide_telemetry_query_duration_seconds", Help: "Telemetry query duration", Buckets: prometheus.DefBuckets},
		[]string{"kind", "result"},
	)
)

// Handler returns the Prometheus metrics handler
func Handler() http.Handler {
	return promhttp.Handler()
}

// RecordJobSubmission records a job submission metric
func RecordJobSubmission(queue, sourceType string) {
	JobsSubmitted.WithLabelValues(queue, sourceType).Inc()
}

// RecordCornDogsTaskSubmission records a task submission to Corndogs
func RecordCornDogsTaskSubmission(queue string, success bool) {
	result := "failure"
	if success {
		result = "success"
	}
	CornDogsTaskSubmissions.WithLabelValues(queue, result).Inc()
}
