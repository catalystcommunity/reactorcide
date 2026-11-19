package observability

import (
	"sync"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

// Prometheus metrics of finished jobs and CI events. The rollup loop
// observes each job and event one time, after its watermark passes it, on
// the replica that holds the lease. Use sum() over replicas.
//
// The labels are low cardinality: queue (queue UUID), lane, failure_class
// (models.FailureClasses, "none", "unknown"), owner_group (bounded, see
// OwnerGroupLabels), principal_kind (human, service, system). There is never
// a workflow, project, or job label.

// PromBuckets are the histogram buckets in seconds.
var PromBuckets = []float64{5, 15, 30, 60, 120, 300, 600, 1200, 1800, 3600, 7200, 14400}

// MetricLabels are the label names of each metric, for tests and docs.
var (
	JobDurationLabels   = []string{"queue", "lane", "failure_class", "owner_group", "principal_kind"}
	QueueWaitLabels     = []string{"queue", "lane", "owner_group", "principal_kind"}
	InfraFailureLabels  = []string{"failure_class", "queue", "lane"}
	EvalLatencyLabels   = []string{"source"}
	AlertsFiringLabels  = []string{"condition"}
	RollupLastRunLabels = []string{"source"}
)

var (
	JobDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name: "reactorcide_job_duration_seconds", Help: "Run time of finished jobs that started",
		Buckets: PromBuckets,
	}, JobDurationLabels)
	JobQueueWait = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name: "reactorcide_job_queue_wait_seconds", Help: "Time from job creation to a worker taking the job",
		Buckets: PromBuckets,
	}, QueueWaitLabels)
	JobInfraFailures = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "reactorcide_job_infra_failures_total", Help: "Finished jobs with an infrastructure failure class",
	}, InfraFailureLabels)
	EvalLatencyHistogram = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name: "reactorcide_eval_latency_seconds", Help: "Time from receipt of a CI event to its first published commit status",
		Buckets: PromBuckets,
	}, EvalLatencyLabels)
	AlertsFiring = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "reactorcide_alert_rules_firing", Help: "Pipeline owner alert rules in the firing state",
	}, AlertsFiringLabels)
	RollupLastSuccess = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "reactorcide_rollup_last_success_timestamp_seconds", Help: "Time of the last successful rollup pass, by source",
	}, RollupLastRunLabels)
)

var registerOnce sync.Once

// RegisterMetrics registers the metrics with the default registry one time.
func RegisterMetrics() {
	registerOnce.Do(func() {
		for _, c := range []prometheus.Collector{JobDuration, JobQueueWait, JobInfraFailures, EvalLatencyHistogram, AlertsFiring, RollupLastSuccess} {
			_ = prometheus.Register(c)
		}
	})
}

// DefaultOwnerGroupLabelLimit is the number of distinct owner group label
// values. Later groups get the value "other".
const DefaultOwnerGroupLabelLimit = 20

// OwnerGroupLabels bounds the owner_group label.
type OwnerGroupLabels struct {
	mu    sync.Mutex
	Limit int
	seen  map[string]bool
}

// Label returns the label value of an owner group: "none" for no group, the
// group while fewer than Limit groups are known, else "other".
func (o *OwnerGroupLabels) Label(group string) string {
	if group == "" {
		return "none"
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.seen == nil {
		o.seen = map[string]bool{}
	}
	if o.seen[group] {
		return group
	}
	limit := o.Limit
	if limit <= 0 {
		limit = DefaultOwnerGroupLabelLimit
	}
	if len(o.seen) >= limit {
		return "other"
	}
	o.seen[group] = true
	return group
}

// JobLabels are the label values of one finished job.
type JobLabels struct {
	Queue, Lane, FailureClass, OwnerGroup, PrincipalKind string
}

// LabelsOf returns the label values of a job.
func LabelsOf(f *models.RollupJobFact, groups *OwnerGroupLabels) JobLabels {
	class := OutcomeClass(f.Status, f.FailureClass)
	if class == "" {
		class = "none"
	}
	lane := f.Lane
	if lane == "" {
		lane = "none"
	}
	queue := f.QueueName
	if queue == "" {
		queue = "none"
	}
	return JobLabels{Queue: queue, Lane: lane, FailureClass: class, OwnerGroup: groups.Label(f.OwnerGroup), PrincipalKind: ActorKindOf(f.ActorKind)}
}

// ObserveJob records one finished job in the Prometheus metrics.
func ObserveJob(f *models.RollupJobFact, groups *OwnerGroupLabels) {
	l := LabelsOf(f, groups)
	if d, ok := Duration(f); ok {
		JobDuration.WithLabelValues(l.Queue, l.Lane, l.FailureClass, l.OwnerGroup, l.PrincipalKind).Observe(d)
	}
	if w, ok := QueueWait(f); ok {
		JobQueueWait.WithLabelValues(l.Queue, l.Lane, l.OwnerGroup, l.PrincipalKind).Observe(w)
	}
	if models.IsInfraFailureClass(l.FailureClass) {
		JobInfraFailures.WithLabelValues(l.FailureClass, l.Queue, l.Lane).Inc()
	}
}

// ObserveEvent records one CI event in the eval latency histogram.
func ObserveEvent(f *models.RollupEventFact) {
	source := f.Source
	if source == "" {
		source = "unknown"
	}
	EvalLatencyHistogram.WithLabelValues(source).Observe(EvalLatency(f))
}
