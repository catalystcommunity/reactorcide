// Package jobtelemetry stores and queries immutable job telemetry batches.
package jobtelemetry

import "time"

const (
	ObjectPrefix       = "telemetry/v1/jobs"
	MaxSeriesPerBatch  = 256
	MaxSamplesPerBatch = 120
	MaxValuesPerSample = 512
	MaxLabelsPerSeries = 8
	DefaultMaxPoints   = 600
	MaxPointsPerSeries = 2000
)

type Label struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

type SeriesDefinition struct {
	SeriesID int64   `json:"series_id"`
	Name     string  `json:"name"`
	Unit     string  `json:"unit"`
	Kind     string  `json:"kind"`
	Labels   []Label `json:"labels,omitempty"`
}

type Value struct {
	SeriesID int64 `json:"series_id"`
	Value    int64 `json:"value"`
}

type Sample struct {
	ObservedAt time.Time `json:"observed_at"`
	Values     []Value   `json:"values"`
}

type Unavailable struct {
	MetricPrefix string `json:"metric_prefix"`
	Reason       string `json:"reason"`
}

type MetricBatch struct {
	LeaseID     string             `json:"lease_id"`
	Sequence    int64              `json:"sequence"`
	Series      []SeriesDefinition `json:"series"`
	Samples     []Sample           `json:"samples"`
	Unavailable []Unavailable      `json:"unavailable,omitempty"`
}

type LogEntry struct {
	ObservedAt time.Time `json:"observed_at"`
	Level      string    `json:"level"`
	Message    string    `json:"message"`
}

type LogBatch struct {
	LeaseID  string     `json:"lease_id"`
	Stream   string     `json:"stream"`
	Sequence int64      `json:"sequence"`
	Entries  []LogEntry `json:"entries"`
}

type metricArchive struct {
	Batches []MetricBatch `json:"batches"`
}

type logArchive struct {
	Batches []LogBatch `json:"batches"`
}

type Point struct {
	ObservedAt time.Time `json:"observed_at"`
	Value      int64     `json:"value"`
	Min        *int64    `json:"min,omitempty"`
	Max        *int64    `json:"max,omitempty"`
}

type Series struct {
	Name   string  `json:"name"`
	Unit   string  `json:"unit"`
	Labels []Label `json:"labels,omitempty"`
	Points []Point `json:"points"`
	kind   string
}

type Query struct {
	JobID     string
	From      *time.Time
	To        *time.Time
	Metrics   []string
	MaxPoints int
	Cursor    string
}

type QueryResponse struct {
	Series      []Series      `json:"series"`
	Unavailable []Unavailable `json:"unavailable,omitempty"`
	Complete    bool          `json:"complete"`
	NextCursor  string        `json:"next_cursor"`
}

type LogResultEntry struct {
	LogEntry
	Stream string
}

type LogPage struct {
	Entries    []LogResultEntry
	NextCursor string
	HasMore    bool
	Complete   bool
}
