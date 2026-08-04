package jobtelemetry

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
)

var metricNamePattern = regexp.MustCompile(`^[a-z][a-z0-9]*(\.[a-z][a-z0-9_]*)+$`)

var allowedUnits = map[string]bool{
	"nanoseconds": true,
	"millicores":  true,
	"bytes":       true,
	"count":       true,
}

var allowedLabelKeys = map[string]bool{
	"scope":     true,
	"component": true,
	"cpu":       true,
	"volume":    true,
	"mount":     true,
	"kind":      true,
}

var allowedReasons = map[string]bool{
	"runtime_not_supported":      true,
	"permission_denied":          true,
	"metric_api_not_installed":   true,
	"guest_helper_not_installed": true,
	"not_applicable":             true,
	"buffer_gap":                 true,
}

func ValidateMetricBatch(batch *MetricBatch, now time.Time) error {
	if batch == nil || batch.LeaseID == "" {
		return errors.New("lease_id is required")
	}
	if batch.Sequence < 0 {
		return errors.New("sequence must not be negative")
	}
	if len(batch.Series) > MaxSeriesPerBatch {
		return fmt.Errorf("series exceeds limit %d", MaxSeriesPerBatch)
	}
	if len(batch.Samples) > MaxSamplesPerBatch {
		return fmt.Errorf("samples exceeds limit %d", MaxSamplesPerBatch)
	}
	ids := make(map[int64]struct{}, len(batch.Series))
	for i := range batch.Series {
		s := &batch.Series[i]
		if s.SeriesID < 0 {
			return errors.New("series_id must not be negative")
		}
		if _, exists := ids[s.SeriesID]; exists {
			return fmt.Errorf("duplicate series_id %d", s.SeriesID)
		}
		ids[s.SeriesID] = struct{}{}
		if !metricNamePattern.MatchString(s.Name) {
			return fmt.Errorf("invalid metric name %q", s.Name)
		}
		if !allowedUnits[s.Unit] {
			return fmt.Errorf("invalid unit %q", s.Unit)
		}
		if s.Kind != "counter" && s.Kind != "gauge" {
			return fmt.Errorf("invalid kind %q", s.Kind)
		}
		if len(s.Labels) > MaxLabelsPerSeries {
			return fmt.Errorf("labels exceeds limit %d", MaxLabelsPerSeries)
		}
		seenLabels := map[string]bool{}
		for _, label := range s.Labels {
			if !allowedLabelKeys[label.Key] || len(label.Key) > 32 {
				return fmt.Errorf("invalid label key %q", label.Key)
			}
			if seenLabels[label.Key] {
				return fmt.Errorf("duplicate label key %q", label.Key)
			}
			seenLabels[label.Key] = true
			if label.Value == "" || len(label.Value) > 256 || strings.ContainsAny(label.Value, "\r\n\x00") {
				return fmt.Errorf("invalid label value for %q", label.Key)
			}
		}
		sort.Slice(s.Labels, func(i, j int) bool { return s.Labels[i].Key < s.Labels[j].Key })
	}
	for _, sample := range batch.Samples {
		if sample.ObservedAt.IsZero() {
			return errors.New("sample timestamp is required")
		}
		if sample.ObservedAt.After(now.Add(5 * time.Minute)) {
			return errors.New("sample timestamp is too far in the future")
		}
		if len(sample.Values) > MaxValuesPerSample {
			return fmt.Errorf("sample values exceeds limit %d", MaxValuesPerSample)
		}
		seenValues := map[int64]bool{}
		for _, value := range sample.Values {
			if _, ok := ids[value.SeriesID]; !ok {
				return fmt.Errorf("unknown series_id %d", value.SeriesID)
			}
			if seenValues[value.SeriesID] {
				return fmt.Errorf("duplicate sample series_id %d", value.SeriesID)
			}
			seenValues[value.SeriesID] = true
		}
	}
	for _, unavailable := range batch.Unavailable {
		if !metricNamePattern.MatchString(unavailable.MetricPrefix) {
			return fmt.Errorf("invalid unavailable metric prefix %q", unavailable.MetricPrefix)
		}
		if !allowedReasons[unavailable.Reason] {
			return fmt.Errorf("invalid unavailable reason %q", unavailable.Reason)
		}
	}
	return nil
}

func ValidateLogBatch(batch *LogBatch) error {
	if batch == nil || batch.LeaseID == "" {
		return errors.New("lease_id is required")
	}
	if batch.Stream != "stdout" && batch.Stream != "stderr" {
		return errors.New("stream must be stdout or stderr")
	}
	if batch.Sequence < 0 {
		return errors.New("sequence must not be negative")
	}
	if len(batch.Entries) > 200 {
		return errors.New("entries exceeds limit 200")
	}
	for _, entry := range batch.Entries {
		if entry.ObservedAt.IsZero() {
			return errors.New("log timestamp is required")
		}
		if len(entry.Message) > 256*1024 {
			return errors.New("log message exceeds limit")
		}
	}
	return nil
}
