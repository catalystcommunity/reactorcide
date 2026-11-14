package jobtelemetry

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/objects"
)

// SummaryVersion is the version of the summary algorithm below. Increase it
// when the algorithm changes: a stored summary with a lower version is
// computed again when it is read.
const SummaryVersion = 1

// Stat is the summary of one measured family over a run. Avg, P95 and Max are
// in the family's unit; PeakAt is when Max was observed.
type Stat struct {
	Avg    int64
	P95    int64
	Max    int64
	PeakAt time.Time
	Count  int
}

// JobSummary is the resource summary of one run, from its job roll-up series
// only (series with no component label). A nil field was not measured.
type JobSummary struct {
	JobID string
	// SampleCount is the larger of the CPU and memory sample counts.
	SampleCount int

	CPU    *Stat // millicores
	Memory *Stat // bytes

	CPURequestMillicores *int64
	CPULimitMillicores   *int64
	MemoryLimitBytes     *int64

	StorageMaxBytes *int64
}

// SummarizeJob computes the resource summary of one job from all of its
// stored metric batches (every attempt, compacted and not).
func SummarizeJob(ctx context.Context, store objects.ObjectStore, jobID string) (JobSummary, error) {
	if store == nil {
		return JobSummary{}, fmt.Errorf("object storage is not configured")
	}
	batches, _, err := readMetricBatches(ctx, store, jobID)
	if err != nil {
		return JobSummary{}, err
	}
	summary := SummarizeBatches(batches)
	summary.JobID = jobID
	return summary, nil
}

// rollupSeries is the points of one roll-up series in one attempt.
type rollupSeries struct {
	definition SeriesDefinition
	labels     []Label
	points     []Point
}

// SummarizeBatches computes a summary from metric batches. It is the pure
// part of SummarizeJob.
//
// Rules:
//   - Only roll-up series count: no component label after the legacy scope
//     label is normalized (views.go), and no per-core CPU series.
//   - CPU: cpu.utilization gauges in millicores, and cpu.usage counters in
//     nanoseconds converted to millicores by the rate between consecutive
//     samples of one attempt. A negative delta (a counter reset) is dropped.
//   - Memory: memory.working_set when the run has it, else memory.usage.
//   - Requests and limits: the largest value observed.
//   - Storage: per sample, the storage.used series with volume=total when the
//     run has one (Kubernetes: the pod's ephemeral storage, which already
//     includes its emptyDir volumes, so adding the volumes would count them
//     two times); else the sum of the roll-up storage.used series of that
//     sample (Docker and containerd: one rootfs series). The max over the run.
//   - When one family has more than one roll-up series at one instant (the
//     same family from two collectors), their values are added.
//   - P95 is the nearest-rank percentile over the samples of the whole run.
func SummarizeBatches(batches []MetricBatch) JobSummary {
	sort.SliceStable(batches, func(i, j int) bool {
		if batches[i].LeaseID == batches[j].LeaseID {
			return batches[i].Sequence < batches[j].Sequence
		}
		return batches[i].LeaseID < batches[j].LeaseID
	})

	// Points per (attempt, series) key, in sample order. Duplicate
	// (lease, sequence) pairs are removed by readMetricBatches already.
	series := map[string]*rollupSeries{}
	var order []string
	for _, batch := range batches {
		definitions := make(map[int64]SeriesDefinition, len(batch.Series))
		for _, definition := range batch.Series {
			definitions[definition.SeriesID] = definition
		}
		for _, sample := range batch.Samples {
			for _, value := range sample.Values {
				definition, ok := definitions[value.SeriesID]
				if !ok {
					continue
				}
				labels := normalizeSeriesLabels(definition.Labels)
				if SeriesComponent(labels) != "" || isPerCoreSeries(labels) {
					continue
				}
				key := stableSeriesKey(batch.LeaseID, SeriesDefinition{Name: definition.Name, Unit: definition.Unit, Kind: definition.Kind, Labels: labels})
				acc := series[key]
				if acc == nil {
					acc = &rollupSeries{definition: definition, labels: labels}
					series[key] = acc
					order = append(order, key)
				}
				acc.points = append(acc.points, Point{ObservedAt: sample.ObservedAt, Value: value.Value})
			}
		}
	}

	cpu := map[time.Time]int64{}
	memoryUsage := map[time.Time]int64{}
	workingSet := map[time.Time]int64{}
	storageTotal := map[time.Time]int64{}
	storageVolumes := map[time.Time]int64{}
	var summary JobSummary
	for _, key := range order {
		acc := series[key]
		sort.SliceStable(acc.points, func(i, j int) bool { return acc.points[i].ObservedAt.Before(acc.points[j].ObservedAt) })
		points := deduplicatePoints(acc.points)
		name, kind := acc.definition.Name, acc.definition.Kind
		switch {
		case name == "cpu.usage" && kind == "counter":
			addPoints(cpu, cpuRates(points))
		case name == "cpu.utilization":
			addPoints(cpu, points)
		case name == "memory.working_set":
			addPoints(workingSet, points)
		case name == "memory.usage":
			addPoints(memoryUsage, points)
		case name == "storage.used":
			if labelValue(acc.labels, "volume") == "total" {
				addPoints(storageTotal, points)
			} else {
				addPoints(storageVolumes, points)
			}
		case name == "cpu.request":
			summary.CPURequestMillicores = maxInto(summary.CPURequestMillicores, points)
		case name == "cpu.limit":
			summary.CPULimitMillicores = maxInto(summary.CPULimitMillicores, points)
		case name == "memory.limit":
			summary.MemoryLimitBytes = maxInto(summary.MemoryLimitBytes, points)
		}
	}

	summary.CPU = statOf(cpu)
	if len(workingSet) > 0 {
		summary.Memory = statOf(workingSet)
	} else {
		summary.Memory = statOf(memoryUsage)
	}
	storage := storageVolumes
	if len(storageTotal) > 0 {
		storage = storageTotal
	}
	if stat := statOf(storage); stat != nil {
		value := stat.Max
		summary.StorageMaxBytes = &value
	}
	if summary.CPU != nil {
		summary.SampleCount = summary.CPU.Count
	}
	if summary.Memory != nil && summary.Memory.Count > summary.SampleCount {
		summary.SampleCount = summary.Memory.Count
	}
	return summary
}

func addPoints(into map[time.Time]int64, points []Point) {
	for _, point := range points {
		into[point.ObservedAt.UTC()] += point.Value
	}
}

func maxInto(current *int64, points []Point) *int64 {
	for _, point := range points {
		if current == nil || point.Value > *current {
			value := point.Value
			current = &value
		}
	}
	return current
}

func labelValue(labels []Label, key string) string {
	for _, label := range labels {
		if label.Key == key {
			return label.Value
		}
	}
	return ""
}

// statOf summarizes values keyed by observation time. Nil when empty.
func statOf(values map[time.Time]int64) *Stat {
	if len(values) == 0 {
		return nil
	}
	type observed struct {
		at    time.Time
		value int64
	}
	all := make([]observed, 0, len(values))
	for at, value := range values {
		all = append(all, observed{at: at, value: value})
	}
	// Time order first, so the peak is the EARLIEST instant of the max.
	sort.Slice(all, func(i, j int) bool { return all[i].at.Before(all[j].at) })
	stat := &Stat{Count: len(all), Max: all[0].value, PeakAt: all[0].at}
	var sum int64
	sorted := make([]int64, 0, len(all))
	for _, item := range all {
		sum += item.value
		sorted = append(sorted, item.value)
		if item.value > stat.Max {
			stat.Max, stat.PeakAt = item.value, item.at
		}
	}
	stat.Avg = sum / int64(len(all))
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	// Nearest rank: the smallest value with at least 95% of samples at or
	// below it. rank = ceil(0.95 * n), 1-based.
	rank := (95*len(sorted) + 99) / 100
	if rank < 1 {
		rank = 1
	}
	stat.P95 = sorted[rank-1]
	return stat
}
