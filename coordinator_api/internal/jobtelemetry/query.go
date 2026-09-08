package jobtelemetry

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	operationalmetrics "github.com/catalystcommunity/reactorcide/coordinator_api/internal/metrics"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/objects"
)

type seriesAccumulator struct {
	definition SeriesDefinition
	leaseID    string
	points     []Point
}

func QueryMetrics(ctx context.Context, store objects.ObjectStore, query Query) (QueryResponse, error) {
	started := time.Now()
	queryResult := "failure"
	defer func() {
		operationalmetrics.TelemetryQueryDuration.WithLabelValues("metrics", queryResult).Observe(time.Since(started).Seconds())
	}()
	response := QueryResponse{Complete: true}
	if store == nil {
		return response, fmt.Errorf("object storage is not configured")
	}
	maxPoints := query.MaxPoints
	if maxPoints <= 0 {
		maxPoints = DefaultMaxPoints
	}
	if maxPoints > MaxPointsPerSeries {
		maxPoints = MaxPointsPerSeries
	}
	batches, complete, err := readMetricBatches(ctx, store, query.JobID)
	if err != nil {
		return response, err
	}
	sort.SliceStable(batches, func(i, j int) bool {
		if batches[i].LeaseID == batches[j].LeaseID {
			return batches[i].Sequence < batches[j].Sequence
		}
		return batches[i].LeaseID < batches[j].LeaseID
	})
	cursor, err := decodeCursor(query.Cursor, "metrics", query.JobID, "")
	if err != nil {
		return response, err
	}
	tracks := cursorTracks(&cursor)
	response.Complete = complete
	accumulators := map[string]*seriesAccumulator{}
	priorCounters := map[string]Point{}
	availability := newAvailabilityIndex(batches, query.From, query.To)
	var unavailableRecords []unavailableRecord
	for _, batch := range batches {
		definitions := make(map[int64]SeriesDefinition, len(batch.Series))
		for _, definition := range batch.Series {
			definitions[definition.SeriesID] = definition
		}
		track := ensureCursorTrack(&cursor, tracks, batch.LeaseID, "")
		emitBatch := !trackHasSequence(track, batch.Sequence)
		if emitBatch {
			for _, item := range batch.Unavailable {
				unavailableRecords = append(unavailableRecords, unavailableRecord{
					item:    Unavailable{MetricPrefix: canonicalMetricName(item.MetricPrefix, ""), Reason: item.Reason},
					leaseID: batch.LeaseID, sequence: batch.Sequence,
				})
			}
		}
		for _, sample := range batch.Samples {
			for _, value := range sample.Values {
				definition, ok := definitions[value.SeriesID]
				if !ok {
					continue
				}
				key := stableSeriesKey(batch.LeaseID, definition)
				point := Point{ObservedAt: sample.ObservedAt, Value: value.Value}
				if !emitBatch || (query.From != nil && sample.ObservedAt.Before(*query.From)) {
					if definition.Kind == "counter" {
						priorCounters[key] = point
					}
					continue
				}
				if query.To != nil && sample.ObservedAt.After(*query.To) {
					continue
				}
				if !metricSelected(definition.Name, query.Metrics) {
					continue
				}
				acc := accumulators[key]
				if acc == nil {
					acc = &seriesAccumulator{definition: definition, leaseID: batch.LeaseID}
					if prior, ok := priorCounters[key]; ok && definition.Kind == "counter" {
						acc.points = append(acc.points, prior)
					}
					accumulators[key] = acc
				}
				acc.points = append(acc.points, point)
			}
		}
		if emitBatch {
			markSequenceSeen(track, batch.Sequence)
		}
	}
	keys := make([]string, 0, len(accumulators))
	for key := range accumulators {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	seriesPointLimit := maxPoints
	if len(keys) > 0 && seriesPointLimit*len(keys) > MaxTotalPoints {
		seriesPointLimit = MaxTotalPoints / len(keys)
		if seriesPointLimit < 1 {
			seriesPointLimit = 1
		}
	}
	totalPoints := 0
	for _, key := range keys {
		if totalPoints >= MaxTotalPoints {
			break
		}
		acc := accumulators[key]
		sort.SliceStable(acc.points, func(i, j int) bool { return acc.points[i].ObservedAt.Before(acc.points[j].ObservedAt) })
		points := deduplicatePoints(acc.points)
		name := acc.definition.Name
		unit := acc.definition.Unit
		if name == "cpu.usage" && acc.definition.Kind == "counter" {
			points = cpuRates(points)
			name = "cpu.utilization"
			unit = "millicores"
		}
		points = downsample(points, seriesPointLimit)
		remaining := MaxTotalPoints - totalPoints
		if len(points) > remaining {
			points = downsample(points, remaining)
		}
		totalPoints += len(points)
		// Rewrite stored labels into the current scheme before anything reads
		// them. Telemetry written before the scope label was removed must
		// still render (see views.go).
		labels := append([]Label{}, normalizeSeriesLabels(acc.definition.Labels)...)
		labels = append(labels, Label{Key: "attempt", Value: acc.leaseID})
		sort.Slice(labels, func(i, j int) bool { return labels[i].Key < labels[j].Key })
		response.Series = append(response.Series, Series{Name: name, Unit: unit, Labels: labels, Points: points})
	}
	// Components is computed over EVERY series, before the view filters any
	// out, so the UI can tell that a component exists even while showing the
	// job roll-up.
	response.Components = AvailableComponents(response.Series)
	response.Series = selectSeriesForView(response.Series, query.View, query.Component)

	response.Unavailable = availability.resolve(unavailableRecords)
	sort.Slice(response.Unavailable, func(i, j int) bool {
		if response.Unavailable[i].MetricPrefix == response.Unavailable[j].MetricPrefix {
			return response.Unavailable[i].Reason < response.Unavailable[j].Reason
		}
		return response.Unavailable[i].MetricPrefix < response.Unavailable[j].MetricPrefix
	})
	response.NextCursor, err = encodeCursor(cursor)
	if err != nil {
		return response, err
	}
	queryResult = "success"
	return response, nil
}

func metricSelected(name string, filters []string) bool {
	if len(filters) == 0 {
		return true
	}
	for _, filter := range filters {
		if name == filter || strings.HasPrefix(name, strings.TrimSuffix(filter, ".")+".") {
			return true
		}
	}
	return false
}

func stableSeriesKey(leaseID string, definition SeriesDefinition) string {
	var b strings.Builder
	b.WriteString(leaseID)
	b.WriteByte(0)
	b.WriteString(definition.Name)
	b.WriteByte(0)
	b.WriteString(definition.Unit)
	b.WriteByte(0)
	b.WriteString(definition.Kind)
	labels := append([]Label{}, definition.Labels...)
	sort.Slice(labels, func(i, j int) bool { return labels[i].Key < labels[j].Key })
	for _, label := range labels {
		b.WriteByte(0)
		b.WriteString(label.Key)
		b.WriteByte('=')
		b.WriteString(label.Value)
	}
	return b.String()
}

func deduplicatePoints(points []Point) []Point {
	if len(points) < 2 {
		return points
	}
	out := points[:0]
	for _, point := range points {
		if len(out) > 0 && point.ObservedAt.Equal(out[len(out)-1].ObservedAt) {
			out[len(out)-1] = point
			continue
		}
		out = append(out, point)
	}
	return out
}

func cpuRates(points []Point) []Point {
	if len(points) < 2 {
		return nil
	}
	rates := make([]Point, 0, len(points)-1)
	for i := 1; i < len(points); i++ {
		wall := points[i].ObservedAt.Sub(points[i-1].ObservedAt).Nanoseconds()
		delta := points[i].Value - points[i-1].Value
		if wall <= 0 || delta < 0 {
			continue
		}
		rates = append(rates, Point{ObservedAt: points[i].ObservedAt, Value: delta * 1000 / wall})
	}
	return rates
}

func downsample(points []Point, limit int) []Point {
	if limit <= 0 || len(points) <= limit {
		return points
	}
	result := make([]Point, 0, limit)
	for bucket := 0; bucket < limit; bucket++ {
		start := bucket * len(points) / limit
		end := (bucket + 1) * len(points) / limit
		if end <= start {
			continue
		}
		minValue, maxValue, sum := points[start].Value, points[start].Value, int64(0)
		for _, point := range points[start:end] {
			sum += point.Value
			if point.Value < minValue {
				minValue = point.Value
			}
			if point.Value > maxValue {
				maxValue = point.Value
			}
		}
		minCopy, maxCopy := minValue, maxValue
		result = append(result, Point{
			ObservedAt: points[end-1].ObservedAt,
			Value:      sum / int64(end-start),
			Min:        &minCopy,
			Max:        &maxCopy,
		})
	}
	return result
}

func ParseOptionalTime(value string) (*time.Time, error) {
	if value == "" {
		return nil, nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return nil, err
	}
	parsed = parsed.UTC()
	return &parsed, nil
}

// canonicalMetricName is the name a series has AFTER the query layer's
// renames. Telemetry written by the Docker and containerd collectors, and by
// the Kubernetes collector before 2026-09, used cpu.usage for the CPU family
// (a counter the query turns into cpu.utilization, or an unavailable prefix
// naming a series never emitted). Availability is decided in canonical names
// so an old cpu.usage warning and a new cpu.utilization sample meet.
func canonicalMetricName(name, kind string) string {
	if name == "cpu.usage" {
		return "cpu.utilization"
	}
	return name
}

// familyMatches reports whether a series name belongs to an unavailable
// prefix: the prefix names the series itself or a parent of it. cpu.request
// is not in the cpu.utilization family; memory.usage is not in memory.rss's.
func familyMatches(prefix, name string) bool {
	return name == prefix || strings.HasPrefix(name, prefix+".")
}

// unavailableRecord is one stored Unavailable item with the batch it came
// from, so it can be placed in time and matched against that lease's samples.
type unavailableRecord struct {
	item     Unavailable
	leaseID  string
	sequence int64
}

type timeSpan struct {
	from, to time.Time
	set      bool
}

func (t *timeSpan) extend(at time.Time) {
	if !t.set || at.Before(t.from) {
		t.from = at
	}
	if !t.set || at.After(t.to) {
		t.to = at
	}
	t.set = true
}

// availabilityIndex answers, for one query, whether a stored unavailable
// record still describes anything.
//
// The rules:
//
//   - An unavailable record has no timestamp of its own. It is placed at the
//     span of the samples in its batch; a batch with no samples takes its
//     lease's span; a lease with no samples is unbounded. A record whose span
//     lies outside the query's From/To is not reported.
//   - A record is suppressed when the SAME lease has a successful sample in
//     the query range for the family the record names. "The metrics API had
//     no sample yet" followed by ten minutes of samples is not a warning.
//   - Suppression is per lease. A retry on another worker that is forbidden
//     from reading metrics keeps its warning even though the first attempt
//     collected fine, because the warning is about that attempt.
//   - Family membership uses canonical names, so stored cpu.usage warnings
//     are cleared by cpu.utilization samples.
//   - A prefix no series ever carries (telemetry.buffer) is never suppressed.
type availabilityIndex struct {
	from, to   *time.Time
	batchSpans map[string]timeSpan            // leaseID\x00sequence
	leaseSpans map[string]timeSpan            // leaseID
	leaseNames map[string]map[string]struct{} // leaseID -> canonical series names sampled in range
}

func newAvailabilityIndex(batches []MetricBatch, from, to *time.Time) *availabilityIndex {
	index := &availabilityIndex{
		from: from, to: to,
		batchSpans: map[string]timeSpan{},
		leaseSpans: map[string]timeSpan{},
		leaseNames: map[string]map[string]struct{}{},
	}
	for _, batch := range batches {
		definitions := make(map[int64]SeriesDefinition, len(batch.Series))
		for _, definition := range batch.Series {
			definitions[definition.SeriesID] = definition
		}
		batchSpan := index.batchSpans[batchSpanKey(batch.LeaseID, batch.Sequence)]
		leaseSpan := index.leaseSpans[batch.LeaseID]
		for _, sample := range batch.Samples {
			batchSpan.extend(sample.ObservedAt)
			leaseSpan.extend(sample.ObservedAt)
			if !index.inRange(sample.ObservedAt) {
				continue
			}
			names := index.leaseNames[batch.LeaseID]
			if names == nil {
				names = map[string]struct{}{}
				index.leaseNames[batch.LeaseID] = names
			}
			for _, value := range sample.Values {
				definition, ok := definitions[value.SeriesID]
				if !ok {
					continue
				}
				names[canonicalMetricName(definition.Name, definition.Kind)] = struct{}{}
			}
		}
		index.batchSpans[batchSpanKey(batch.LeaseID, batch.Sequence)] = batchSpan
		index.leaseSpans[batch.LeaseID] = leaseSpan
	}
	return index
}

func batchSpanKey(leaseID string, sequence int64) string {
	return fmt.Sprintf("%s\x00%d", leaseID, sequence)
}

func (index *availabilityIndex) inRange(at time.Time) bool {
	if index.from != nil && at.Before(*index.from) {
		return false
	}
	if index.to != nil && at.After(*index.to) {
		return false
	}
	return true
}

// relevant reports whether the record's time placement intersects the query
// range. An unbounded record (no samples anywhere in its lease) is always
// relevant: a job that never produced a sample must keep its warning.
func (index *availabilityIndex) relevant(record unavailableRecord) bool {
	span := index.batchSpans[batchSpanKey(record.leaseID, record.sequence)]
	if !span.set {
		span = index.leaseSpans[record.leaseID]
	}
	if !span.set {
		return true
	}
	if index.from != nil && span.to.Before(*index.from) {
		return false
	}
	if index.to != nil && span.from.After(*index.to) {
		return false
	}
	return true
}

func (index *availabilityIndex) suppressed(record unavailableRecord) bool {
	for name := range index.leaseNames[record.leaseID] {
		if familyMatches(record.item.MetricPrefix, name) {
			return true
		}
	}
	return false
}

// resolve returns the records that are both in range and not contradicted by
// a successful sample, deduplicated by prefix and reason across leases.
func (index *availabilityIndex) resolve(records []unavailableRecord) []Unavailable {
	seen := map[string]bool{}
	var out []Unavailable
	for _, record := range records {
		if !index.relevant(record) || index.suppressed(record) {
			continue
		}
		key := record.item.MetricPrefix + "\x00" + record.item.Reason
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, record.item)
	}
	return out
}
