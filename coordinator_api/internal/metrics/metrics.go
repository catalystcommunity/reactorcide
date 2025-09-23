package metrics

import (
	"net/http"
	"sync"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	once sync.Once

	// Job metrics
	JobsSubmitted = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "reactorcide_jobs_submitted_total",
			Help: "Total number of jobs submitted",
		},
		[]string{"queue", "source_type"},
	)

	JobsProcessed = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "reactorcide_jobs_processed_total",
			Help: "Total number of jobs processed",
		},
		[]string{"queue", "status", "worker_id"},
	)

	JobDuration = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "reactorcide_job_duration_seconds",
			Help:    "Time taken to process a job",
			Buckets: prometheus.ExponentialBuckets(1, 2, 15), // 1s to ~8 hours
		},
		[]string{"queue", "status"},
	)

	JobRetries = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "reactorcide_job_retries_total",
			Help: "Total number of job retry attempts",
		},
		[]string{"queue", "worker_id"},
	)

	// Queue metrics
	QueueDepth = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "reactorcide_queue_depth",
			Help: "Current number of jobs in queue",
		},
		[]string{"queue", "status"},
	)

	// Worker metrics
	WorkersActive = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "reactorcide_workers_active",
			Help: "Number of active workers",
		},
		[]string{"queue"},
	)

	WorkerJobsActive = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "reactorcide_worker_jobs_active",
			Help: "Number of jobs currently being processed by worker",
		},
		[]string{"worker_id"},
	)

	// Corndogs metrics
	CornDogsTaskSubmissions = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "reactorcide_corndogs_task_submissions_total",
			Help: "Total number of tasks submitted to Corndogs",
		},
		[]string{"queue", "result"},
	)

	CornDogsTaskPolls = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "reactorcide_corndogs_task_polls_total",
			Help: "Total number of task poll attempts from Corndogs",
		},
		[]string{"queue", "result"},
	)

	// API metrics
	APIRequests = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "reactorcide_api_requests_total",
			Help: "Total number of API requests",
		},
		[]string{"method", "endpoint", "status_code"},
	)

	APIRequestDuration = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "reactorcide_api_request_duration_seconds",
			Help:    "API request duration",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"method", "endpoint"},
	)

	// Resource metrics (for worker monitoring)
	WorkerCPUUsage = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "reactorcide_worker_cpu_usage_percent",
			Help: "Current CPU usage percentage of worker",
		},
		[]string{"worker_id"},
	)

	WorkerMemoryUsage = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "reactorcide_worker_memory_usage_bytes",
			Help: "Current memory usage of worker in bytes",
		},
		[]string{"worker_id"},
	)

	// Error metrics
	JobErrors = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "reactorcide_job_errors_total",
			Help: "Total number of job errors",
		},
		[]string{"queue", "error_type", "retryable"},
	)
)

// Handler returns the Prometheus metrics handler
func Handler() http.Handler {
	return promhttp.Handler()
}

// UpdateQueueDepth updates the queue depth metric for a given queue and status
func UpdateQueueDepth(queue, status string, count float64) {
	QueueDepth.WithLabelValues(queue, status).Set(count)
}

// RecordJobSubmission records a job submission metric
func RecordJobSubmission(queue, sourceType string) {
	JobsSubmitted.WithLabelValues(queue, sourceType).Inc()
}

// RecordJobProcessed records a job processing metric
func RecordJobProcessed(queue, status, workerID string, duration float64) {
	JobsProcessed.WithLabelValues(queue, status, workerID).Inc()
	JobDuration.WithLabelValues(queue, status).Observe(duration)
}

// RecordJobRetry records a job retry attempt
func RecordJobRetry(queue, workerID string) {
	JobRetries.WithLabelValues(queue, workerID).Inc()
}

// RecordCornDogsTaskSubmission records a task submission to Corndogs
func RecordCornDogsTaskSubmission(queue string, success bool) {
	result := "failure"
	if success {
		result = "success"
	}
	CornDogsTaskSubmissions.WithLabelValues(queue, result).Inc()
}

// RecordCornDogsTaskPoll records a task poll attempt from Corndogs
func RecordCornDogsTaskPoll(queue string, success bool) {
	result := "failure"
	if success {
		result = "success"
	}
	CornDogsTaskPolls.WithLabelValues(queue, result).Inc()
}

// RecordAPIRequest records an API request metric
func RecordAPIRequest(method, endpoint, statusCode string) {
	APIRequests.WithLabelValues(method, endpoint, statusCode).Inc()
}

// RecordAPIRequestDuration records the duration of an API request
func RecordAPIRequestDuration(method, endpoint string, duration float64) {
	APIRequestDuration.WithLabelValues(method, endpoint).Observe(duration)
}

// UpdateWorkerResourceUsage updates worker resource usage metrics
func UpdateWorkerResourceUsage(workerID string, cpuPercent, memoryBytes float64) {
	WorkerCPUUsage.WithLabelValues(workerID).Set(cpuPercent)
	WorkerMemoryUsage.WithLabelValues(workerID).Set(memoryBytes)
}

// RecordJobError records a job error metric
func RecordJobError(queue, errorType string, retryable bool) {
	retryableStr := "false"
	if retryable {
		retryableStr = "true"
	}
	JobErrors.WithLabelValues(queue, errorType, retryableStr).Inc()
}

// SetWorkersActive sets the number of active workers
func SetWorkersActive(queue string, count float64) {
	WorkersActive.WithLabelValues(queue).Set(count)
}

// SetWorkerJobsActive sets the number of active jobs for a worker
func SetWorkerJobsActive(workerID string, count float64) {
	WorkerJobsActive.WithLabelValues(workerID).Set(count)
}
