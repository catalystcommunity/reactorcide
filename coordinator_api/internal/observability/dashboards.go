package observability

import (
	"context"
	"errors"
	"sort"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

// Look-back ranges of the dashboards, in days.
var LookBackDays = []int{7, 30, 90}

// ErrActorAttributionScope: actor attribution needs every project of the
// organization, because the actor rollups have no project.
var ErrActorAttributionScope = errors.New("actor attribution needs read access to every project of the organization")

// ClampDays returns a supported look-back (7, 30, or 90; default 30).
func ClampDays(days int) int {
	switch {
	case days <= 0:
		return 30
	case days <= 7:
		return 7
	case days <= 30:
		return 30
	}
	return 90
}

// Summary is the display form of an aggregate.
type Summary struct {
	Runs                   int64            `json:"runs"`
	Successes              int64            `json:"successes"`
	PipelineFailures       int64            `json:"pipeline_failures"`
	InfraFailures          int64            `json:"infra_failures"`
	Cancelled              int64            `json:"cancelled"`
	PassRate               *float64         `json:"pass_rate,omitempty"`
	InfraFailureRate       *float64         `json:"infra_failure_rate,omitempty"`
	DurationP50Seconds     *float64         `json:"duration_p50_seconds,omitempty"`
	DurationP95Seconds     *float64         `json:"duration_p95_seconds,omitempty"`
	QueueWaitP50Seconds    *float64         `json:"queue_wait_p50_seconds,omitempty"`
	QueueWaitP95Seconds    *float64         `json:"queue_wait_p95_seconds,omitempty"`
	ByClass                map[string]int64 `json:"by_class"`
	MeasuredRuns           int64            `json:"measured_runs"`
	CPUCoreHours           float64          `json:"cpu_core_hours"`
	MemoryGiBHours         float64          `json:"memory_gib_hours"`
	CPUReservedCoreHours   float64          `json:"cpu_reserved_core_hours"`
	MemoryReservedGiBHours float64          `json:"memory_reserved_gib_hours"`
}

// Summarize converts an aggregate.
func Summarize(a *Aggregate) Summary {
	return Summary{
		Runs: a.Runs, Successes: a.Successes, PipelineFailures: a.PipelineFailures, InfraFailures: a.InfraFailures,
		Cancelled: a.Cancelled, PassRate: ptr(a.PassRate()), InfraFailureRate: ptr(a.InfraFailureRate()),
		DurationP50Seconds: ptr(a.DurationQuantile(0.5)), DurationP95Seconds: ptr(a.DurationQuantile(0.95)),
		QueueWaitP50Seconds: ptr(a.QueueWaitQuantile(0.5)), QueueWaitP95Seconds: ptr(a.QueueWaitQuantile(0.95)),
		ByClass: a.ByClass, MeasuredRuns: a.MeasuredRuns, CPUCoreHours: a.CPUCoreHours, MemoryGiBHours: a.MemoryGiBHours,
		CPUReservedCoreHours: a.CPUReservedCoreHours, MemoryReservedGiBHours: a.MemoryReservedGiBHours,
	}
}

// WorkflowHealth is the health of one workflow in a pipeline dashboard.
type WorkflowHealth struct {
	ProjectID          *string `json:"project_id,omitempty"`
	WorkflowSecurityID string  `json:"workflow_security_id"`
	OwnerGroup         string  `json:"owner_group"`
	Summary
	// DefaultBranch is the summary of runs with the ref class default.
	DefaultBranch Summary `json:"default_branch"`
	// CPUCoreHoursPerRun is the average of the measured runs.
	CPUCoreHoursPerRun *float64     `json:"cpu_core_hours_per_run,omitempty"`
	Trend              []TrendPoint `json:"trend"`
}

// PipelineDashboard is the pipeline owner view (story 6).
type PipelineDashboard struct {
	From       time.Time                 `json:"from"`
	To         time.Time                 `json:"to"`
	Days       int                       `json:"days"`
	Totals     Summary                   `json:"totals"`
	Trend      []TrendPoint              `json:"trend"`
	Workflows  []WorkflowHealth          `json:"workflows"`
	FlakyTests []models.FlakyTestSummary `json:"flaky_tests"`
	Alerts     []models.AlertRule        `json:"alerts"`
}

// MaxDashboardWorkflows bounds the workflow list of a pipeline dashboard.
const MaxDashboardWorkflows = 200

// BuildPipelineDashboard reads the daily rollups of scope (OrgID and the
// project visibility of it are used; From and To are set here).
func BuildPipelineDashboard(ctx context.Context, st store.ObservabilityStore, scope store.RollupFilter, days int, now time.Time) (*PipelineDashboard, error) {
	days = ClampDays(days)
	scope.From, scope.To = DayRange(now, days)
	rows, err := st.ListJobRollups(ctx, string(Daily), scope)
	if err != nil {
		return nil, err
	}
	d := &PipelineDashboard{From: scope.From, To: scope.To, Days: days, Totals: Summarize(AggregateRows(rows)),
		Trend: Trend(rows, scope.From, scope.To, Daily)}
	aggs, parts := GroupBy(rows, func(r *models.JobRollup) string { return strOr(r.ProjectID) + "\x00" + r.WorkflowSecurityID })
	for key, a := range aggs {
		part := parts[key]
		def := NewAggregate()
		owner := ""
		for i := range part {
			if part[i].RefClassBucket == models.RefClassBucketDefault {
				def.Add(&part[i])
			}
			if part[i].OwnerGroup != "" {
				owner = part[i].OwnerGroup
			}
		}
		w := WorkflowHealth{ProjectID: part[0].ProjectID, WorkflowSecurityID: part[0].WorkflowSecurityID, OwnerGroup: owner,
			Summary: Summarize(a), DefaultBranch: Summarize(def), Trend: Trend(part, scope.From, scope.To, Daily)}
		if a.MeasuredRuns > 0 {
			v := a.CPUCoreHours / float64(a.MeasuredRuns)
			w.CPUCoreHoursPerRun = &v
		}
		d.Workflows = append(d.Workflows, w)
	}
	sort.Slice(d.Workflows, func(i, j int) bool {
		a, b := d.Workflows[i], d.Workflows[j]
		ar, br := rateOr(a.DefaultBranch.PassRate, a.PassRate), rateOr(b.DefaultBranch.PassRate, b.PassRate)
		if ar != br {
			return ar < br
		}
		if a.Runs != b.Runs {
			return a.Runs > b.Runs
		}
		return a.WorkflowSecurityID < b.WorkflowSecurityID
	})
	if len(d.Workflows) > MaxDashboardWorkflows {
		d.Workflows = d.Workflows[:MaxDashboardWorkflows]
	}
	projects := scope.ProjectIDs
	if scope.AllProjects {
		seen := map[string]bool{}
		projects = nil
		for i := range rows {
			if id := strOr(rows[i].ProjectID); id != "" && !seen[id] {
				seen[id] = true
				projects = append(projects, id)
			}
		}
	}
	if d.FlakyTests, err = st.ListFlakyTestsForProjects(ctx, projects, scope.From, 20); err != nil {
		return nil, err
	}
	rules, err := st.ListAlertRules(ctx, scope.OrgID)
	if err != nil {
		return nil, err
	}
	visible := map[string]bool{}
	for _, id := range scope.ProjectIDs {
		visible[id] = true
	}
	d.Alerts = []models.AlertRule{}
	for _, r := range rules {
		if scope.AllProjects || (r.ProjectID != nil && visible[*r.ProjectID]) {
			d.Alerts = append(d.Alerts, r)
		}
	}
	if d.Workflows == nil {
		d.Workflows = []WorkflowHealth{}
	}
	return d, nil
}

// rateOr returns the first rate, else 2 (sorts last).
func rateOr(rates ...*float64) float64 {
	for _, r := range rates {
		if r != nil {
			return *r
		}
	}
	return 2
}

// LaneHealth is the infrastructure health of one lane.
type LaneHealth struct {
	Lane string `json:"lane"`
	Summary
}

// EvalSummary is the eval latency of a range.
type EvalSummary struct {
	Events     int64    `json:"events"`
	P50Seconds *float64 `json:"p50_seconds,omitempty"`
	P95Seconds *float64 `json:"p95_seconds,omitempty"`
}

// InfraTrendPoint is one day of infrastructure health.
type InfraTrendPoint struct {
	BucketStart         time.Time `json:"bucket_start"`
	Runs                int64     `json:"runs"`
	InfraFailures       int64     `json:"infra_failures"`
	InfraFailureRate    *float64  `json:"infra_failure_rate,omitempty"`
	QueueWaitP95Seconds *float64  `json:"queue_wait_p95_seconds,omitempty"`
	EvalEvents          int64     `json:"eval_events"`
	EvalP95Seconds      *float64  `json:"eval_p95_seconds,omitempty"`
}

// InfraDashboard is the CI owner view (stories 7, 9). The caller adds the
// backlog and the worker counts.
type InfraDashboard struct {
	From   time.Time         `json:"from"`
	To     time.Time         `json:"to"`
	Days   int               `json:"days"`
	Totals Summary           `json:"totals"`
	Lanes  []LaneHealth      `json:"lanes"`
	Eval   EvalSummary       `json:"eval"`
	Trend  []InfraTrendPoint `json:"trend"`
	SLOs   []SLOStatus       `json:"slos"`
}

// BuildInfraDashboard reads the daily job and eval rollups of scope and the
// SLO statuses.
func BuildInfraDashboard(ctx context.Context, st store.ObservabilityStore, scope store.RollupFilter, days int, now time.Time) (*InfraDashboard, error) {
	days = ClampDays(days)
	scope.From, scope.To = DayRange(now, days)
	rows, err := st.ListJobRollups(ctx, string(Daily), scope)
	if err != nil {
		return nil, err
	}
	evalRows, err := st.ListEvalRollups(ctx, string(Daily), scope)
	if err != nil {
		return nil, err
	}
	d := &InfraDashboard{From: scope.From, To: scope.To, Days: days, Totals: Summarize(AggregateRows(rows)), Lanes: []LaneHealth{}}
	aggs, _ := GroupBy(rows, func(r *models.JobRollup) string { return r.Lane })
	for lane, a := range aggs {
		d.Lanes = append(d.Lanes, LaneHealth{Lane: lane, Summary: Summarize(a)})
	}
	sort.Slice(d.Lanes, func(i, j int) bool { return d.Lanes[i].Lane < d.Lanes[j].Lane })
	ea := AggregateEvalRows(evalRows)
	d.Eval = EvalSummary{Events: ea.Events, P50Seconds: ptr(ea.Latency.Quantile(0.5)), P95Seconds: ptr(ea.Latency.Quantile(0.95))}
	evalBy := map[time.Time][]models.EvalRollup{}
	for _, r := range evalRows {
		evalBy[r.BucketStart] = append(evalBy[r.BucketStart], r)
	}
	for _, p := range Trend(rows, scope.From, scope.To, Daily) {
		e := AggregateEvalRows(evalBy[p.BucketStart])
		d.Trend = append(d.Trend, InfraTrendPoint{BucketStart: p.BucketStart, Runs: p.Runs, InfraFailures: p.InfraFailures,
			InfraFailureRate: p.InfraFailureRate, QueueWaitP95Seconds: p.QueueWaitP95Seconds, EvalEvents: e.Events,
			EvalP95Seconds: ptr(e.Latency.Quantile(0.95))})
	}
	if d.SLOs, err = SLOStatuses(ctx, st, scope, now); err != nil {
		return nil, err
	}
	return d, nil
}

// Usage attributions.
const (
	AttributionOwner = "owner"
	AttributionActor = "actor"
)

// UsagePoint is one day of usage of a group.
type UsagePoint struct {
	BucketStart  time.Time `json:"bucket_start"`
	Runs         int64     `json:"runs"`
	CPUCoreHours float64   `json:"cpu_core_hours"`
}

// GroupUsage is the usage of one group.
type GroupUsage struct {
	// Group is the owner group or the actor group name; "" is no group.
	Group                string       `json:"group"`
	Runs                 int64        `json:"runs"`
	Successes            int64        `json:"successes"`
	PipelineFailures     int64        `json:"pipeline_failures"`
	InfraFailures        int64        `json:"infra_failures"`
	PassRate             *float64     `json:"pass_rate,omitempty"`
	CPUCoreHours         float64      `json:"cpu_core_hours"`
	MemoryGiBHours       float64      `json:"memory_gib_hours"`
	CPUReservedCoreHours float64      `json:"cpu_reserved_core_hours"`
	Series               []UsagePoint `json:"series"`
}

// UsageDashboard is the director view (stories 10, 11).
type UsageDashboard struct {
	From        time.Time    `json:"from"`
	To          time.Time    `json:"to"`
	Days        int          `json:"days"`
	Attribution string       `json:"attribution"`
	Groups      []GroupUsage `json:"groups"`
}

// BuildUsageDashboard sums usage by group. Owner attribution uses the owner
// group snapshot on each job (WP7). Actor attribution uses the groups of the
// actor at the time of the run (WP8 membership history); it needs
// scope.AllProjects.
func BuildUsageDashboard(ctx context.Context, st store.ObservabilityStore, scope store.RollupFilter, days int, attribution string, now time.Time) (*UsageDashboard, error) {
	days = ClampDays(days)
	scope.From, scope.To = DayRange(now, days)
	if attribution == "" {
		attribution = AttributionOwner
	}
	d := &UsageDashboard{From: scope.From, To: scope.To, Days: days, Attribution: attribution, Groups: []GroupUsage{}}
	buckets := Buckets(scope.From, scope.To, Daily)
	series := func(points map[time.Time]UsagePoint) []UsagePoint {
		out := make([]UsagePoint, 0, len(buckets))
		for _, b := range buckets {
			p := points[b]
			p.BucketStart = b
			out = append(out, p)
		}
		return out
	}
	switch attribution {
	case AttributionOwner:
		rows, err := st.ListJobRollups(ctx, string(Daily), scope)
		if err != nil {
			return nil, err
		}
		aggs, parts := GroupBy(rows, func(r *models.JobRollup) string { return r.OwnerGroup })
		for group, a := range aggs {
			points := map[time.Time]UsagePoint{}
			for _, r := range parts[group] {
				p := points[r.BucketStart]
				p.Runs += r.Runs
				p.CPUCoreHours += r.CPUCoreHours
				points[r.BucketStart] = p
			}
			d.Groups = append(d.Groups, GroupUsage{Group: group, Runs: a.Runs, Successes: a.Successes, PipelineFailures: a.PipelineFailures,
				InfraFailures: a.InfraFailures, PassRate: ptr(a.PassRate()), CPUCoreHours: a.CPUCoreHours, MemoryGiBHours: a.MemoryGiBHours,
				CPUReservedCoreHours: a.CPUReservedCoreHours, Series: series(points)})
		}
	case AttributionActor:
		if !scope.AllProjects {
			return nil, ErrActorAttributionScope
		}
		rows, err := st.ListActorGroupRollups(ctx, scope.OrgID, scope.From, scope.To)
		if err != nil {
			return nil, err
		}
		byGroup := map[string]*GroupUsage{}
		points := map[string]map[time.Time]UsagePoint{}
		for _, r := range rows {
			g := byGroup[r.GroupID]
			if g == nil {
				g = &GroupUsage{Group: r.GroupName}
				byGroup[r.GroupID] = g
				points[r.GroupID] = map[time.Time]UsagePoint{}
			}
			g.Runs += r.Runs
			g.Successes += r.Successes
			g.PipelineFailures += r.PipelineFailures
			g.InfraFailures += r.InfraFailures
			g.CPUCoreHours += r.CPUCoreHours
			g.MemoryGiBHours += r.MemoryGiBHours
			g.CPUReservedCoreHours += r.CPUReservedCoreHours
			p := points[r.GroupID][r.BucketStart]
			p.Runs += r.Runs
			p.CPUCoreHours += r.CPUCoreHours
			points[r.GroupID][r.BucketStart] = p
		}
		for id, g := range byGroup {
			if n := g.Successes + g.PipelineFailures; n > 0 {
				v := float64(g.Successes) / float64(n)
				g.PassRate = &v
			}
			g.Series = series(points[id])
			d.Groups = append(d.Groups, *g)
		}
	default:
		return nil, errors.New("attribution must be owner or actor")
	}
	sort.Slice(d.Groups, func(i, j int) bool {
		if d.Groups[i].CPUCoreHours != d.Groups[j].CPUCoreHours {
			return d.Groups[i].CPUCoreHours > d.Groups[j].CPUCoreHours
		}
		if d.Groups[i].Runs != d.Groups[j].Runs {
			return d.Groups[i].Runs > d.Groups[j].Runs
		}
		return d.Groups[i].Group < d.Groups[j].Group
	})
	return d, nil
}
