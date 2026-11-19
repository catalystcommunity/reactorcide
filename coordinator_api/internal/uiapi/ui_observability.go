package uiapi

import (
	"context"
	"errors"
	"sort"
	"time"

	"github.com/catalystcommunity/app-utils-go/logging"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/observability"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/uiapi/csilapi"
)

// Observability dashboards (docs/observability.md). Each op reads only the
// rollups of the projects that the caller can view: a public project is
// visible to an anonymous caller, a private one needs view-private access.
// A caller that sees no project of the organization gets empty data, not an
// error, so a public organization dashboard works with no login.

// observabilityScope returns the rollup filter of the visible projects of an
// organization, and the names of those projects.
func (s *UiService) observabilityScope(ctx context.Context, orgID string, projectID *string) (store.RollupFilter, map[string]string, error) {
	scope := store.RollupFilter{OrgID: orgID}
	names := map[string]string{}
	if err := requireNonEmpty("org_id", orgID, 64); err != nil {
		return scope, names, err
	}
	identity, _ := s.deps.resolveIdentity(ctx)
	all, err := s.deps.Store.ListProjectsByOrg(ctx, orgID, listAllLimit, 0)
	if err != nil {
		return scope, names, NewServiceError("internal", "an internal error occurred")
	}
	visible, err := s.deps.Resolver.FilterVisibleProjects(ctx, identity, all)
	if err != nil {
		return scope, names, NewServiceError("internal", "an internal error occurred")
	}
	for _, p := range visible {
		names[p.ProjectID] = p.Name
	}
	if projectID != nil && *projectID != "" {
		if _, ok := names[*projectID]; !ok {
			return scope, names, NewServiceError("not_found", "project not found")
		}
		scope.ProjectIDs = []string{*projectID}
		return scope, names, nil
	}
	for _, p := range visible {
		scope.ProjectIDs = append(scope.ProjectIDs, p.ProjectID)
	}
	// Rows with no project (jobs submitted outside a project) follow the
	// visibility of a project-less job of the organization.
	noProject, err := s.deps.Resolver.CanViewJob(ctx, identity, &models.Job{OrgID: orgID})
	if err == nil && noProject {
		scope.NoProject = true
	}
	scope.AllProjects = scope.NoProject && len(visible) == len(all)
	return scope, names, nil
}

func obsStore(s *UiService) (store.ObservabilityStore, error) {
	st, ok := any(s.deps.Store).(store.ObservabilityStore)
	if !ok {
		return nil, NewServiceError("unavailable", "observability is not available")
	}
	return st, nil
}

func obsDays(days *int64) int {
	if days == nil {
		return 30
	}
	return observability.ClampDays(int(*days))
}

func f64(p *float64) *float64 { return p }

func summaryToCsil(s observability.Summary) csilapi.ObsSummary {
	out := csilapi.ObsSummary{
		Runs: s.Runs, Successes: s.Successes, PipelineFailures: s.PipelineFailures, InfraFailures: s.InfraFailures,
		Cancelled: s.Cancelled, PassRate: f64(s.PassRate), InfraFailureRate: f64(s.InfraFailureRate),
		DurationP50Seconds: f64(s.DurationP50Seconds), DurationP95Seconds: f64(s.DurationP95Seconds),
		QueueWaitP50Seconds: f64(s.QueueWaitP50Seconds), QueueWaitP95Seconds: f64(s.QueueWaitP95Seconds),
		ByClass: []csilapi.ObsClassCount{}, MeasuredRuns: s.MeasuredRuns, CpuCoreHours: s.CPUCoreHours,
		MemoryGibHours: s.MemoryGiBHours, CpuReservedCoreHours: s.CPUReservedCoreHours, MemoryReservedGibHours: s.MemoryReservedGiBHours,
	}
	classes := make([]string, 0, len(s.ByClass))
	for class := range s.ByClass {
		classes = append(classes, class)
	}
	sort.Strings(classes)
	for _, class := range classes {
		out.ByClass = append(out.ByClass, csilapi.ObsClassCount{FailureClass: class, Count: s.ByClass[class]})
	}
	return out
}

func trendToCsil(points []observability.TrendPoint) []csilapi.ObsTrendPoint {
	out := make([]csilapi.ObsTrendPoint, 0, len(points))
	for _, p := range points {
		out = append(out, csilapi.ObsTrendPoint{BucketStart: formatTime(p.BucketStart), Runs: p.Runs, PassRate: p.PassRate,
			DurationP95Seconds: p.DurationP95Seconds, QueueWaitP95Seconds: p.QueueWaitP95Seconds, InfraFailures: p.InfraFailures,
			InfraFailureRate: p.InfraFailureRate, CpuCoreHours: p.CPUCoreHours})
	}
	return out
}

func alertRuleToCsil(r *models.AlertRule) csilapi.ObsAlertRule {
	out := csilapi.ObsAlertRule{RuleId: r.RuleID, Name: r.Name, Scope: r.Scope, ProjectId: r.ProjectID, Condition: r.Condition,
		RefClassBucket: r.RefClassBucket, Threshold: r.Threshold, Factor: r.Factor, WindowHours: int64(r.WindowHours), State: r.State,
		Enabled: r.Enabled, LastValue: r.LastValue, LastBaseline: r.LastBaseline, LastRuns: int64(r.LastRuns),
		LastEvaluatedAt: formatTimePtr(r.LastEvaluatedAt), StateChangedAt: formatTimePtr(r.StateChangedAt)}
	if r.WorkflowSecurityID != "" {
		v := r.WorkflowSecurityID
		out.WorkflowSecurityId = &v
	}
	if r.OwnerGroup != "" {
		v := r.OwnerGroup
		out.OwnerGroup = &v
	}
	return out
}

// GetPipelineDashboard is the pipeline owner view: health of each workflow,
// failure class mix, flaky tests, resources, and the alert rules.
func (s *UiService) GetPipelineDashboard(ctx context.Context, req csilapi.GetPipelineDashboardRequest) (csilapi.GetPipelineDashboardResponse, error) {
	st, err := obsStore(s)
	if err != nil {
		return csilapi.GetPipelineDashboardResponse{}, err
	}
	scope, names, err := s.observabilityScope(ctx, req.OrgId, req.ProjectId)
	if err != nil {
		return csilapi.GetPipelineDashboardResponse{}, err
	}
	d, err := observability.BuildPipelineDashboard(ctx, st, scope, obsDays(req.Days), time.Now().UTC())
	if err != nil {
		logging.Log.WithError(err).Warn("Failed to build pipeline dashboard")
		return csilapi.GetPipelineDashboardResponse{}, NewServiceError("internal", "failed to read the pipeline dashboard")
	}
	out := csilapi.GetPipelineDashboardResponse{RangeFrom: formatTime(d.From), RangeTo: formatTime(d.To), Days: int64(d.Days),
		Totals: summaryToCsil(d.Totals), Trend: trendToCsil(d.Trend), Workflows: []csilapi.ObsWorkflowHealth{},
		FlakyTests: []csilapi.ObsFlakyTest{}, Alerts: []csilapi.ObsAlertRule{}}
	for _, w := range d.Workflows {
		h := csilapi.ObsWorkflowHealth{ProjectId: w.ProjectID, WorkflowSecurityId: w.WorkflowSecurityID, OwnerGroup: w.OwnerGroup,
			Summary: summaryToCsil(w.Summary), DefaultBranch: summaryToCsil(w.DefaultBranch), CpuCoreHoursPerRun: w.CPUCoreHoursPerRun,
			Trend: trendToCsil(w.Trend)}
		if w.ProjectID != nil {
			if name, ok := names[*w.ProjectID]; ok {
				h.ProjectName = &name
			}
		}
		out.Workflows = append(out.Workflows, h)
	}
	for _, t := range d.FlakyTests {
		out.FlakyTests = append(out.FlakyTests, csilapi.ObsFlakyTest{TestId: t.TestID, Suite: t.Suite, Classname: t.Classname, Name: t.Name,
			FlakyCount: t.FlakyCount, TotalRuns: t.TotalRuns, LastFlakyAt: formatTimePtr(t.LastFlakyAt)})
	}
	for i := range d.Alerts {
		out.Alerts = append(out.Alerts, alertRuleToCsil(&d.Alerts[i]))
	}
	return out, nil
}

// GetInfraDashboard is the CI owner view: infrastructure failure rate by
// class, queue wait by lane, eval latency, SLOs, backlog, and workers.
func (s *UiService) GetInfraDashboard(ctx context.Context, req csilapi.GetInfraDashboardRequest) (csilapi.GetInfraDashboardResponse, error) {
	st, err := obsStore(s)
	if err != nil {
		return csilapi.GetInfraDashboardResponse{}, err
	}
	scope, _, err := s.observabilityScope(ctx, req.OrgId, nil)
	if err != nil {
		return csilapi.GetInfraDashboardResponse{}, err
	}
	now := time.Now().UTC()
	d, err := observability.BuildInfraDashboard(ctx, st, scope, obsDays(req.Days), now)
	if err != nil {
		logging.Log.WithError(err).Warn("Failed to build infrastructure dashboard")
		return csilapi.GetInfraDashboardResponse{}, NewServiceError("internal", "failed to read the infrastructure dashboard")
	}
	out := csilapi.GetInfraDashboardResponse{RangeFrom: formatTime(d.From), RangeTo: formatTime(d.To), Days: int64(d.Days),
		Totals: summaryToCsil(d.Totals), Lanes: []csilapi.ObsLaneHealth{}, EvalEvents: d.Eval.Events,
		EvalP50Seconds: d.Eval.P50Seconds, EvalP95Seconds: d.Eval.P95Seconds, Trend: []csilapi.ObsTrendPoint{}, Slos: []csilapi.ObsSloStatus{}}
	for _, l := range d.Lanes {
		out.Lanes = append(out.Lanes, csilapi.ObsLaneHealth{Lane: l.Lane, Summary: summaryToCsil(l.Summary)})
	}
	for _, p := range d.Trend {
		out.Trend = append(out.Trend, csilapi.ObsTrendPoint{BucketStart: formatTime(p.BucketStart), Runs: p.Runs, InfraFailures: p.InfraFailures,
			InfraFailureRate: p.InfraFailureRate, QueueWaitP95Seconds: p.QueueWaitP95Seconds, EvalEvents: p.EvalEvents, EvalP95Seconds: p.EvalP95Seconds})
	}
	for _, slo := range d.SLOs {
		out.Slos = append(out.Slos, csilapi.ObsSloStatus{Sli: slo.SLI, Lane: slo.Lane, Objective: slo.Objective, ThresholdSeconds: slo.ThresholdSeconds,
			WindowDays: int64(slo.WindowDays), Total: slo.Total, Good: slo.Good, Compliance: slo.Compliance, P95Seconds: slo.P95Seconds,
			ErrorBudgetRemaining: slo.ErrorBudgetRemaining, BudgetState: slo.BudgetState, Measures: slo.Measures})
	}
	c := observability.LoadCapacity(ctx, s.deps.Store, req.OrgId, now)
	out.Capacity = csilapi.ObsCapacity{WorkersTotal: int64(c.Workers.Total), WorkersActive: int64(c.Workers.Active),
		WorkersQuarantined: int64(c.Workers.Quarantined), WorkersDisabled: int64(c.Workers.Disabled), WorkersSeen: int64(c.Workers.Seen),
		BacklogJobs: c.BacklogJobs, BacklogOldestAgeSeconds: c.BacklogOldestSeconds, Queues: []csilapi.ObsQueueBacklog{}}
	// Queue names and depths are organization routing facts; show them
	// only to a caller that sees every project of the organization.
	if scope.AllProjects {
		for _, q := range c.Queues {
			out.Capacity.Queues = append(out.Capacity.Queues, csilapi.ObsQueueBacklog{QueueUuid: q.QueueUUID, DisplayName: q.DisplayName,
				Count: q.Count, OldestAgeSeconds: q.OldestAgeSeconds})
		}
	}
	return out, nil
}

// GetUsageDashboard is the director view: usage by group over time.
func (s *UiService) GetUsageDashboard(ctx context.Context, req csilapi.GetUsageDashboardRequest) (csilapi.GetUsageDashboardResponse, error) {
	st, err := obsStore(s)
	if err != nil {
		return csilapi.GetUsageDashboardResponse{}, err
	}
	scope, _, err := s.observabilityScope(ctx, req.OrgId, nil)
	if err != nil {
		return csilapi.GetUsageDashboardResponse{}, err
	}
	attribution := derefOr(req.Attribution, observability.AttributionOwner)
	d, err := observability.BuildUsageDashboard(ctx, st, scope, obsDays(req.Days), attribution, time.Now().UTC())
	if errors.Is(err, observability.ErrActorAttributionScope) {
		return csilapi.GetUsageDashboardResponse{}, NewServiceError("permission_denied", err.Error())
	}
	if err != nil {
		if attribution != observability.AttributionOwner && attribution != observability.AttributionActor {
			return csilapi.GetUsageDashboardResponse{}, NewServiceError("invalid_argument", "attribution must be owner or actor")
		}
		logging.Log.WithError(err).Warn("Failed to build usage dashboard")
		return csilapi.GetUsageDashboardResponse{}, NewServiceError("internal", "failed to read the usage dashboard")
	}
	out := csilapi.GetUsageDashboardResponse{RangeFrom: formatTime(d.From), RangeTo: formatTime(d.To), Days: int64(d.Days),
		Attribution: d.Attribution, Groups: []csilapi.ObsGroupUsage{}}
	for _, g := range d.Groups {
		u := csilapi.ObsGroupUsage{Group: g.Group, Runs: g.Runs, Successes: g.Successes, PipelineFailures: g.PipelineFailures,
			InfraFailures: g.InfraFailures, PassRate: g.PassRate, CpuCoreHours: g.CPUCoreHours, MemoryGibHours: g.MemoryGiBHours,
			CpuReservedCoreHours: g.CPUReservedCoreHours, Series: []csilapi.ObsUsagePoint{}}
		for _, p := range g.Series {
			u.Series = append(u.Series, csilapi.ObsUsagePoint{BucketStart: formatTime(p.BucketStart), Runs: p.Runs, CpuCoreHours: p.CPUCoreHours})
		}
		out.Groups = append(out.Groups, u)
	}
	return out, nil
}
