// Package jobtelemetry stores and queries immutable job telemetry batches.
package jobtelemetry

import "time"

const (
	ObjectPrefix         = "telemetry/v1/jobs"
	MaxSeriesPerBatch    = 256
	MaxSamplesPerBatch   = 120
	MaxValuesPerSample   = 512
	MaxLabelsPerSeries   = 8
	MaxEncodedBatchBytes = 1 << 20
	DefaultMaxPoints     = 600
	MaxPointsPerSeries   = 2000
	MaxTotalPoints       = 20000
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
	// View selects the summary roll-up or every series. See views.go.
	//
	// The ZERO VALUE means no view filtering, i.e. every series -- this is a
	// storage-layer mechanism, and "show the summary" is a product decision
	// that belongs to the caller. Service callers pass ParseMetricView's
	// result, which never returns empty, so the API default is the summary.
	// Both views are authorized identically; this only controls noise.
	View MetricView
	// Component narrows to one container's series. Empty means no narrowing.
	Component string
}

type QueryResponse struct {
	Series      []Series      `json:"series"`
	Unavailable []Unavailable `json:"unavailable,omitempty"`
	Complete    bool          `json:"complete"`
	NextCursor  string        `json:"next_cursor"`
	// Components lists every component present in this job's telemetry, even
	// when the current view filtered them out. The UI needs it to decide
	// whether to show a component picker at all, and it must reflect the whole
	// dataset rather than the current selection or the control would vanish
	// the moment it was used.
	Components []string `json:"components,omitempty"`
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
