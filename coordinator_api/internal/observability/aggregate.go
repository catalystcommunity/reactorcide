package observability

import (
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

// Aggregate is the sum of many job rollup rows. Percentiles of an aggregate
// are estimates from the merged histograms (Bounds).
type Aggregate struct {
	Runs                   int64            `json:"runs"`
	Successes              int64            `json:"successes"`
	PipelineFailures       int64            `json:"pipeline_failures"`
	InfraFailures          int64            `json:"infra_failures"`
	UnknownFailures        int64            `json:"unknown_failures"`
	Cancelled              int64            `json:"cancelled"`
	ByClass                map[string]int64 `json:"by_class"`
	DurationSumSeconds     float64          `json:"duration_sum_seconds"`
	QueueWaitSumSeconds    float64          `json:"queue_wait_sum_seconds"`
	MeasuredRuns           int64            `json:"measured_runs"`
	CPUCoreHours           float64          `json:"cpu_core_hours"`
	MemoryGiBHours         float64          `json:"memory_gib_hours"`
	CPUReservedCoreHours   float64          `json:"cpu_reserved_core_hours"`
	MemoryReservedGiBHours float64          `json:"memory_reserved_gib_hours"`
	Duration               Histogram        `json:"-"`
	QueueWait              Histogram        `json:"-"`
}

// NewAggregate returns an empty aggregate.
func NewAggregate() *Aggregate {
	return &Aggregate{ByClass: map[string]int64{}, Duration: NewHistogram(), QueueWait: NewHistogram()}
}

// Add adds one rollup row.
func (a *Aggregate) Add(r *models.JobRollup) {
	a.Runs += r.Runs
	a.Successes += r.Successes
	a.PipelineFailures += r.PipelineFailures()
	a.InfraFailures += r.InfraFailures()
	a.UnknownFailures += r.FailedUnknown
	a.Cancelled += r.Cancelled
	for class, n := range map[string]int64{
		models.FailureClassJob: r.FailedJob, models.FailureClassTimeout: r.FailedTimeout, models.FailureClassOOM: r.FailedOOM,
		models.FailureClassImagePull: r.FailedImagePull, models.FailureClassScheduling: r.FailedScheduling,
		models.FailureClassWorkerLost: r.FailedWorkerLost, models.FailureClassPlatform: r.FailedPlatform,
		models.FailureClassCancelled: r.Cancelled, "unknown": r.FailedUnknown,
	} {
		if n > 0 {
			a.ByClass[class] += n
		}
	}
	a.DurationSumSeconds += r.DurationSumSeconds
	a.QueueWaitSumSeconds += r.QueueWaitSumSeconds
	a.MeasuredRuns += r.MeasuredRuns
	a.CPUCoreHours += r.CPUCoreHours
	a.MemoryGiBHours += r.MemoryGiBHours
	a.CPUReservedCoreHours += r.CPUReservedCoreHours
	a.MemoryReservedGiBHours += r.MemoryReservedGiBHours
	a.Duration.Merge(r.DurationHist)
	a.QueueWait.Merge(r.QueueWaitHist)
}

// PassRate is successes / (successes + pipeline failures). Infrastructure
// failures, unknown failures, and cancels do not count: a pipeline owner
// owns only the classes job, timeout, and oom.
func (a *Aggregate) PassRate() (float64, bool) {
	n := a.Successes + a.PipelineFailures
	if n == 0 {
		return 0, false
	}
	return float64(a.Successes) / float64(n), true
}

// PipelineRuns is the denominator of PassRate.
func (a *Aggregate) PipelineRuns() int64 { return a.Successes + a.PipelineFailures }

// InfraFailureRate is infrastructure failures / runs that were not
// cancelled.
func (a *Aggregate) InfraFailureRate() (float64, bool) {
	n := a.Runs - a.Cancelled
	if n <= 0 {
		return 0, false
	}
	return float64(a.InfraFailures) / float64(n), true
}

// DurationQuantile estimates a quantile of pipeline run time.
func (a *Aggregate) DurationQuantile(q float64) (float64, bool) { return a.Duration.Quantile(q) }

// QueueWaitQuantile estimates a quantile of queue wait.
func (a *Aggregate) QueueWaitQuantile(q float64) (float64, bool) { return a.QueueWait.Quantile(q) }

// AggregateRows sums rows.
func AggregateRows(rows []models.JobRollup) *Aggregate {
	a := NewAggregate()
	for i := range rows {
		a.Add(&rows[i])
	}
	return a
}

// DayRange returns the daily buckets of the last days days, including today:
// From is the start of the first day, To the end of today.
func DayRange(now time.Time, days int) (time.Time, time.Time) {
	if days < 1 {
		days = 1
	}
	end := BucketStart(now, Daily).Add(24 * time.Hour)
	return end.Add(-time.Duration(days) * 24 * time.Hour), end
}

// Buckets lists the bucket starts in [from, to).
func Buckets(from, to time.Time, g Granularity) []time.Time {
	var out []time.Time
	for t := from; t.Before(to); t = t.Add(g.Step()) {
		out = append(out, t)
	}
	return out
}

// TrendPoint is the aggregate of one bucket.
type TrendPoint struct {
	BucketStart         time.Time `json:"bucket_start"`
	Runs                int64     `json:"runs"`
	PassRate            *float64  `json:"pass_rate,omitempty"`
	DurationP95Seconds  *float64  `json:"duration_p95_seconds,omitempty"`
	QueueWaitP95Seconds *float64  `json:"queue_wait_p95_seconds,omitempty"`
	InfraFailures       int64     `json:"infra_failures"`
	InfraFailureRate    *float64  `json:"infra_failure_rate,omitempty"`
	CPUCoreHours        float64   `json:"cpu_core_hours"`
}

func ptr(v float64, ok bool) *float64 {
	if !ok {
		return nil
	}
	return &v
}

// Trend returns one point for each bucket in [from, to); a bucket with no
// rows has zero runs and no rates.
func Trend(rows []models.JobRollup, from, to time.Time, g Granularity) []TrendPoint {
	byBucket := map[time.Time]*Aggregate{}
	for i := range rows {
		b := BucketStart(rows[i].BucketStart, g)
		if byBucket[b] == nil {
			byBucket[b] = NewAggregate()
		}
		byBucket[b].Add(&rows[i])
	}
	out := []TrendPoint{}
	for _, b := range Buckets(from, to, g) {
		p := TrendPoint{BucketStart: b}
		if a := byBucket[b]; a != nil {
			p.Runs = a.Runs
			p.PassRate = ptr(a.PassRate())
			p.DurationP95Seconds = ptr(a.DurationQuantile(0.95))
			p.QueueWaitP95Seconds = ptr(a.QueueWaitQuantile(0.95))
			p.InfraFailures = a.InfraFailures
			p.InfraFailureRate = ptr(a.InfraFailureRate())
			p.CPUCoreHours = a.CPUCoreHours
		}
		out = append(out, p)
	}
	return out
}

// GroupBy sums rows by a key.
func GroupBy(rows []models.JobRollup, key func(*models.JobRollup) string) (map[string]*Aggregate, map[string][]models.JobRollup) {
	aggs := map[string]*Aggregate{}
	parts := map[string][]models.JobRollup{}
	for i := range rows {
		k := key(&rows[i])
		if aggs[k] == nil {
			aggs[k] = NewAggregate()
		}
		aggs[k].Add(&rows[i])
		parts[k] = append(parts[k], rows[i])
	}
	return aggs, parts
}

// EvalAggregate is the sum of eval rollup rows.
type EvalAggregate struct {
	Events  int64     `json:"events"`
	Latency Histogram `json:"-"`
}

// AggregateEvalRows sums eval rollup rows.
func AggregateEvalRows(rows []models.EvalRollup) *EvalAggregate {
	a := &EvalAggregate{Latency: NewHistogram()}
	for i := range rows {
		a.Events += rows[i].Events
		a.Latency.Merge(rows[i].LatencyHist)
	}
	return a
}
