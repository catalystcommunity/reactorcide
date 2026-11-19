// Package observability computes the rollups, pipeline owner alerts, and
// SLOs of plan items F2, F3, and F4 (docs/observability.md).
//
//   - Rollups: a lease-guarded loop (Loop, lease "observability-rollups")
//     reads finished jobs and CI events after a watermark and writes hourly
//     and daily rollup rows. A missing watermark starts a backfill over all
//     existing rows. Each changed bucket is computed again from the raw rows,
//     so a computation is idempotent.
//   - Prometheus: the same loop observes each newly finished job one time in
//     low-cardinality histograms (metrics.go).
//   - Alerts: rules on the hourly rollups, with hysteresis. A transition
//     writes an alert.firing or alert.resolved event to the outbound event
//     outbox (docs/outbound-events.md).
//   - SLOs: SLIs and error budgets computed from the daily rollups.
package observability

import (
	"math"
	"sort"
)

// Bounds are the upper bounds, in seconds, of the rollup histograms. A
// histogram has len(Bounds)+1 counts: count i holds the values v with
// Bounds[i-1] < v <= Bounds[i], and the last count holds the values above
// the last bound. Do not change the bounds: stored histograms use them. If
// they must change, add a version and compute the rollups again.
var Bounds = []float64{
	1, 2, 5, 10, 15, 30, 45, 60, 90, 120, 180, 300, 450, 600, 900, 1200,
	1800, 2700, 3600, 5400, 7200, 10800, 14400, 21600, 43200, 86400,
}

// Histogram is a count for each bucket of Bounds.
type Histogram []int64

// NewHistogram returns an empty histogram.
func NewHistogram() Histogram { return make(Histogram, len(Bounds)+1) }

// bucketOf returns the bucket index of a value.
func bucketOf(v float64) int {
	return sort.SearchFloat64s(Bounds, v)
}

// Observe adds one value. A negative value counts as 0.
func (h Histogram) Observe(v float64) {
	if v < 0 || math.IsNaN(v) {
		v = 0
	}
	h[bucketOf(v)]++
}

// Merge adds other to h. A histogram of a different length (an empty
// stored array) is ignored.
func (h Histogram) Merge(other []int64) {
	if len(other) != len(h) {
		return
	}
	for i, c := range other {
		h[i] += c
	}
}

// Total returns the number of values.
func (h Histogram) Total() int64 {
	var n int64
	for _, c := range h {
		n += c
	}
	return n
}

func lowerBound(i int) float64 {
	if i == 0 {
		return 0
	}
	return Bounds[i-1]
}

// Quantile estimates the q quantile (0 < q <= 1) with linear interpolation in
// the bucket that holds it. A value in the +Inf bucket gives the last bound.
// ok is false for an empty histogram.
func (h Histogram) Quantile(q float64) (value float64, ok bool) {
	total := h.Total()
	if total == 0 {
		return 0, false
	}
	if q <= 0 {
		q = math.SmallestNonzeroFloat64
	}
	if q > 1 {
		q = 1
	}
	rank := q * float64(total)
	var cumulative float64
	for i, c := range h {
		if c == 0 {
			continue
		}
		next := cumulative + float64(c)
		if next >= rank {
			if i == len(Bounds) {
				return Bounds[len(Bounds)-1], true
			}
			lo, hi := lowerBound(i), Bounds[i]
			return lo + (hi-lo)*((rank-cumulative)/float64(c)), true
		}
		cumulative = next
	}
	return Bounds[len(Bounds)-1], true
}

// CountAtMost estimates how many values are <= limit, with linear
// interpolation in the bucket that holds limit. A value in the +Inf bucket is
// above every limit.
func (h Histogram) CountAtMost(limit float64) float64 {
	if limit < 0 {
		return 0
	}
	var n float64
	for i, c := range h {
		if i == len(Bounds) {
			break
		}
		lo, hi := lowerBound(i), Bounds[i]
		if limit >= hi {
			n += float64(c)
			continue
		}
		if limit > lo {
			n += float64(c) * (limit - lo) / (hi - lo)
		}
		break
	}
	return n
}

// HistogramFrom converts a stored array. An array of a wrong length gives an
// empty histogram.
func HistogramFrom(values []int64) Histogram {
	h := NewHistogram()
	h.Merge(values)
	return h
}

// ExactQuantile returns the q quantile of values with linear interpolation
// between the closest ranks (the same as PostgreSQL percentile_cont). ok is
// false for no values. values is sorted in place.
func ExactQuantile(values []float64, q float64) (float64, bool) {
	if len(values) == 0 {
		return 0, false
	}
	sort.Float64s(values)
	if q <= 0 {
		return values[0], true
	}
	if q >= 1 {
		return values[len(values)-1], true
	}
	pos := q * float64(len(values)-1)
	lo := int(math.Floor(pos))
	hi := int(math.Ceil(pos))
	if lo == hi {
		return values[lo], true
	}
	return values[lo] + (values[hi]-values[lo])*(pos-float64(lo)), true
}
