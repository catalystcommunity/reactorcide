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
	response.Complete = complete
	accumulators := map[string]*seriesAccumulator{}
	unavailable := map[string]Unavailable{}
	for _, batch := range batches {
		definitions := make(map[int64]SeriesDefinition, len(batch.Series))
		for _, definition := range batch.Series {
			definitions[definition.SeriesID] = definition
		}
		for _, item := range batch.Unavailable {
			unavailable[item.MetricPrefix+"\x00"+item.Reason] = item
		}
		for _, sample := range batch.Samples {
			if query.From != nil && sample.ObservedAt.Before(*query.From) {
				continue
			}
			if query.To != nil && sample.ObservedAt.After(*query.To) {
				continue
			}
			for _, value := range sample.Values {
				definition, ok := definitions[value.SeriesID]
				if !ok || !metricSelected(definition.Name, query.Metrics) {
					continue
				}
				key := stableSeriesKey(batch.LeaseID, definition)
				acc := accumulators[key]
				if acc == nil {
					acc = &seriesAccumulator{definition: definition, leaseID: batch.LeaseID}
					accumulators[key] = acc
				}
				acc.points = append(acc.points, Point{ObservedAt: sample.ObservedAt, Value: value.Value})
			}
		}
	}
	keys := make([]string, 0, len(accumulators))
	for key := range accumulators {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
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
		points = downsample(points, maxPoints)
		labels := append([]Label{}, acc.definition.Labels...)
		labels = append(labels, Label{Key: "attempt", Value: acc.leaseID})
		sort.Slice(labels, func(i, j int) bool { return labels[i].Key < labels[j].Key })
		response.Series = append(response.Series, Series{Name: name, Unit: unit, Labels: labels, Points: points})
	}
	for _, item := range unavailable {
		response.Unavailable = append(response.Unavailable, item)
	}
	sort.Slice(response.Unavailable, func(i, j int) bool {
		if response.Unavailable[i].MetricPrefix == response.Unavailable[j].MetricPrefix {
			return response.Unavailable[i].Reason < response.Unavailable[j].Reason
		}
		return response.Unavailable[i].MetricPrefix < response.Unavailable[j].MetricPrefix
	})
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
