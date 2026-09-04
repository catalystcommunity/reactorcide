package jobtelemetry

// Metric views.
//
// Two problems are solved here, and they are separate.
//
// FIRST, the label scheme changed. Collectors used to emit a `scope` label with
// exactly two values, "job" and "component" — which carried no information the
// presence of a `component` label did not already carry. Docker compounded it
// by labelling its single-container roll-up `component=main`, so the job total
// looked like one component among several. Collectors no longer emit `scope`,
// and the rule is now simply: a series with NO component label is the job
// roll-up; a series WITH one belongs to that component.
//
// Already-stored telemetry still carries the old labels, and jobs that ran
// before the change must keep rendering. normalizeSeriesLabels is the exact
// inverse of the old scheme, applied on read.
//
// SECOND, even with clean labels the default view was too much: the job total
// and its parts in one flat list, memory answered four ways, and one series per
// host core. SummaryView returns what a person actually asks of a CI job —
// how much memory, how much swap, what were the requests and limits, how much
// CPU — and RawView returns everything for when that is not enough.

// MetricView selects how much of a job's telemetry a query returns.
type MetricView string

const (
	// ViewSummary is the default: job-level series only, restricted to the
	// summary metric set.
	ViewSummary MetricView = "summary"
	// ViewRaw returns every series, per component, unfiltered.
	ViewRaw MetricView = "raw"
)

// ParseMetricView maps a request value to a view. It NEVER returns the empty
// view: an empty request value means the caller expressed no preference and
// gets the summary, and an unrecognized value also gets the summary rather than
// an error, so a newer client asking for a view this server does not know still
// gets a usable answer instead of a failure.
//
// This is what makes ViewSummary the API-level default while Query's zero value
// stays "no filtering" at the storage layer (see Query.View).
func ParseMetricView(value string) MetricView {
	if MetricView(value) == ViewRaw {
		return ViewRaw
	}
	return ViewSummary
}

// summaryMetrics is the set ViewSummary keeps.
//
// memory.rss, memory.cache, memory.working_set and memory.committed are all
// deliberately absent: they are four more answers to "how much memory is in
// use", and memory.usage is the one that question means. cpu.usage is absent
// because the query layer already converts the counter into cpu.utilization.
// storage.capacity and storage.available are absent because the interesting
// number is what the job used.
var summaryMetrics = map[string]bool{
	"memory.usage":      true,
	"memory.swap.usage": true,
	"memory.request":    true,
	"memory.limit":      true,
	"cpu.utilization":   true,
	"cpu.request":       true,
	"cpu.limit":         true,
	"storage.used":      true,
}

// componentLabel is the label whose presence marks a series as belonging to one
// container rather than to the job as a whole.
const componentLabel = "component"

// scopeLabel is the removed label. Read-side normalization is the only place
// that still knows it existed.
const scopeLabel = "scope"

// normalizeSeriesLabels rewrites a stored series' labels into the current
// scheme. It is the exact inverse of what collectors used to emit:
//
//   - scope=job with component=X came from Docker/containerd, whose single
//     container IS the job. The component label was noise; both labels go.
//   - scope=component with component=X came from Kubernetes per-container
//     metrics. The component label is real; only scope goes.
//   - scope=job with no component was already a roll-up; scope goes.
//
// A series that never had a scope label is already current and passes through.
func normalizeSeriesLabels(labels []Label) []Label {
	scope := ""
	hasScope := false
	for _, label := range labels {
		if label.Key == scopeLabel {
			scope, hasScope = label.Value, true
			break
		}
	}
	if !hasScope {
		return labels
	}

	dropComponent := scope == "job"
	out := make([]Label, 0, len(labels))
	for _, label := range labels {
		if label.Key == scopeLabel {
			continue
		}
		if dropComponent && label.Key == componentLabel {
			continue
		}
		out = append(out, label)
	}
	return out
}

// SeriesComponent returns the component a series belongs to, or "" for the job
// roll-up.
func SeriesComponent(labels []Label) string {
	for _, label := range labels {
		if label.Key == componentLabel {
			return label.Value
		}
	}
	return ""
}

// isPerCoreSeries reports whether a series is one host core's CPU rather than
// the total. Collectors no longer emit these, but stored data has them, and
// they are exactly the noise the summary view exists to remove.
func isPerCoreSeries(labels []Label) bool {
	for _, label := range labels {
		if label.Key == "cpu" && label.Value != "total" {
			return true
		}
	}
	return false
}

// selectSeriesForView filters an assembled series set.
//
// component narrows to one component when non-empty, and is honored in both
// views: a person who has opened the component picker wants that component's
// numbers whichever view they are in.
func selectSeriesForView(series []Series, view MetricView, component string) []Series {
	out := make([]Series, 0, len(series))
	for _, s := range series {
		seriesComponent := SeriesComponent(s.Labels)

		if component != "" {
			if seriesComponent != component {
				continue
			}
		} else if view == ViewSummary {
			// The summary is the job roll-up: no component breakdown, no
			// per-core noise, and only the metrics the default charts read.
			if seriesComponent != "" {
				continue
			}
			if isPerCoreSeries(s.Labels) {
				continue
			}
			if !summaryMetrics[s.Name] {
				continue
			}
		}
		out = append(out, s)
	}
	return out
}

// AvailableComponents lists the distinct components present in a series set,
// excluding the job roll-up. The UI builds its component picker from this and
// shows the control only when there is more than one thing to pick, so a
// single-container job never grows a pointless dropdown — and a job whose
// builder sidecar starts ten minutes in grows one at that moment.
func AvailableComponents(series []Series) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, s := range series {
		component := SeriesComponent(s.Labels)
		if component == "" || seen[component] {
			continue
		}
		seen[component] = true
		out = append(out, component)
	}
	return out
}
