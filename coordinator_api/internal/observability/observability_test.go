package observability

import (
	"context"
	"math"
	"sort"
	"testing"
	"time"

	"github.com/lib/pq"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

func sp(s string) *string       { return &s }
func tp(t time.Time) *time.Time { return &t }

var base = time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)

// fact makes a finished job that was created at created, waited wait, and
// ran run.
func fact(org, project, wf, status, class string, created time.Time, wait, run time.Duration) models.RollupJobFact {
	started := created.Add(wait)
	return models.RollupJobFact{
		JobID: created.String() + status, OrgID: org, ProjectID: sp(project), WorkflowSecurityID: wf, Status: status,
		FailureClass: class, CreatedAt: created, StartedAt: tp(started), CompletedAt: started.Add(run),
		RefClasses: pq.StringArray{"default"}, ActorKind: "human", Lane: "default", QueueName: "q1",
	}
}

func TestHistogramQuantileAndCountAtMost(t *testing.T) {
	h := NewHistogram()
	_, ok := h.Quantile(0.95)
	assert.False(t, ok)
	for i := 0; i < 100; i++ {
		h.Observe(float64(i%10) + 0.5) // 0.5 .. 9.5
	}
	assert.Equal(t, int64(100), h.Total())
	p50, ok := h.Quantile(0.5)
	require.True(t, ok)
	assert.InDelta(t, 5, p50, 0.01, "rank 50 is the top of the (2, 5] bucket")
	p95, _ := h.Quantile(0.95)
	assert.Greater(t, p95, 5.0)
	assert.LessOrEqual(t, p95, 10.0)
	assert.InDelta(t, 50, h.CountAtMost(5), 0.001)
	assert.InDelta(t, 100, h.CountAtMost(10), 0.001)
	assert.InDelta(t, 0, h.CountAtMost(-1), 0.001)

	inf := NewHistogram()
	inf.Observe(1e9)
	v, _ := inf.Quantile(0.5)
	assert.Equal(t, Bounds[len(Bounds)-1], v)
	assert.Equal(t, 0.0, inf.CountAtMost(1e6), "a +Inf value is above every limit")

	// Merge ignores a wrong length (an empty stored array).
	h.Merge([]int64{1, 2})
	assert.Equal(t, int64(100), h.Total())
	assert.Equal(t, int64(200), func() int64 { g := HistogramFrom(h); g.Merge(h); return g.Total() }())
}

func TestExactQuantileMatchesPercentileCont(t *testing.T) {
	v, ok := ExactQuantile([]float64{4, 1, 3, 2}, 0.5)
	require.True(t, ok)
	assert.Equal(t, 2.5, v)
	v, _ = ExactQuantile([]float64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}, 0.95)
	assert.InDelta(t, 9.55, v, 1e-9)
	v, _ = ExactQuantile([]float64{7}, 0.95)
	assert.Equal(t, 7.0, v)
	_, ok = ExactQuantile(nil, 0.5)
	assert.False(t, ok)
}

func TestRefClassBucketAndOutcome(t *testing.T) {
	assert.Equal(t, "default", RefClassBucket([]string{"production", "default"}))
	assert.Equal(t, "pr", RefClassBucket([]string{"fork", "pr"}))
	assert.Equal(t, "other", RefClassBucket(nil))
	assert.Equal(t, "", OutcomeClass("completed", ""))
	assert.Equal(t, "timeout", OutcomeClass("timeout", ""))
	assert.Equal(t, "unknown", OutcomeClass("failed", ""))
	assert.Equal(t, "cancelled", OutcomeClass("cancelled", ""))
	assert.True(t, IsPipelineRun("failed", "oom"))
	assert.False(t, IsPipelineRun("failed", "infra.worker_lost"))
	assert.False(t, IsPipelineRun("cancelled", "cancelled"))
}

func TestComputeJobRollups(t *testing.T) {
	facts := []models.RollupJobFact{
		fact("o", "p", "ci", "completed", "", base.Add(10*time.Minute), 10*time.Second, 100*time.Second),
		fact("o", "p", "ci", "completed", "", base.Add(20*time.Minute), 20*time.Second, 200*time.Second),
		fact("o", "p", "ci", "failed", "job", base.Add(30*time.Minute), 30*time.Second, 300*time.Second),
		// Infra failures count in runs and wait but never in duration.
		fact("o", "p", "ci", "failed", "infra.worker_lost", base.Add(31*time.Minute), 40*time.Second, 9000*time.Second),
		fact("o", "p", "ci", "cancelled", "cancelled", base.Add(32*time.Minute), 5*time.Second, time.Second),
	}
	// A run with a resource summary: 2 cores average for 1800 s = 1 core-hour.
	samples, cpu, mem, req := 5, int64(2000), int64(1<<30), int64(4000)
	facts[2].SampleCount, facts[2].CPUAvgMillicores, facts[2].MemoryAvgBytes, facts[2].CPURequestMillicores = &samples, &cpu, &mem, &req
	facts[2].CompletedAt = facts[2].StartedAt.Add(1800 * time.Second)
	// A job that no worker took.
	never := models.RollupJobFact{OrgID: "o", ProjectID: sp("p"), WorkflowSecurityID: "ci", Status: "failed", FailureClass: "infra.scheduling",
		CreatedAt: base.Add(5 * time.Minute), CompletedAt: base.Add(50 * time.Minute), RefClasses: pq.StringArray{"default"}, ActorKind: "human", Lane: "default"}
	facts = append(facts, never)

	require.Len(t, ComputeJobRollups(facts, Hourly), 3, "rows go to the hour of completion")
	rows := ComputeJobRollups(facts, Daily)
	require.Len(t, rows, 1)
	r := rows[0]
	assert.Equal(t, int64(6), r.Runs)
	assert.Equal(t, int64(2), r.Successes)
	assert.Equal(t, int64(1), r.FailedJob)
	assert.Equal(t, int64(1), r.FailedWorkerLost)
	assert.Equal(t, int64(1), r.FailedScheduling)
	assert.Equal(t, int64(1), r.Cancelled)
	assert.Equal(t, int64(3), r.PipelineFailures()+r.Successes)
	require.NotNil(t, r.DurationP50Seconds)
	assert.Equal(t, 200.0, *r.DurationP50Seconds, "durations of pipeline runs only: 100, 200, 1800")
	assert.Equal(t, int64(3), HistogramFrom(r.DurationHist).Total())
	assert.Equal(t, int64(5), HistogramFrom(r.QueueWaitHist).Total(), "the never-started job has no wait")
	assert.InDelta(t, 1.0, r.CPUCoreHours, 1e-9)
	assert.InDelta(t, 0.5, r.MemoryGiBHours, 1e-9)
	assert.InDelta(t, 2.0, r.CPUReservedCoreHours, 1e-9)
	assert.Equal(t, int64(1), r.MeasuredRuns)

	// Two hours and two dimension sets.
	facts = append(facts, fact("o", "p", "deploy", "completed", "", base.Add(70*time.Minute), time.Second, time.Second))
	daily := ComputeJobRollups(facts, Daily)
	require.Len(t, daily, 2)
	assert.Equal(t, base, daily[0].BucketStart)
}

func TestMembershipIndexAndActorGroups(t *testing.T) {
	org := "o"
	changes := []models.GroupMembershipChange{
		{GroupID: "g1", OrgID: &org, GroupName: "team-a", PrincipalID: "u1", Change: "added", ChangedAt: base},
		{GroupID: "g2", OrgID: &org, GroupName: "team-b", PrincipalID: "u1", Change: "added", ChangedAt: base.Add(2 * time.Hour)},
		{GroupID: "g1", OrgID: &org, GroupName: "team-a", PrincipalID: "u1", Change: "removed", ChangedAt: base.Add(5 * time.Hour)},
	}
	idx := NewMembershipIndex(changes)
	assert.Len(t, idx.GroupsAt("u1", org, base.Add(time.Hour)), 1)
	assert.Len(t, idx.GroupsAt("u1", org, base.Add(3*time.Hour)), 2)
	assert.Equal(t, "g2", idx.GroupsAt("u1", org, base.Add(6*time.Hour))[0].ID)
	assert.Empty(t, idx.GroupsAt("u1", "other-org", base.Add(3*time.Hour)))

	early := fact("o", "p", "ci", "completed", "", base.Add(time.Hour), 0, time.Minute)
	early.ActorPrincipalID = sp("u1")
	late := fact("o", "p", "ci", "failed", "job", base.Add(6*time.Hour), 0, time.Minute)
	late.ActorPrincipalID = sp("u1")
	none := fact("o", "p", "ci", "completed", "", base.Add(6*time.Hour), 0, time.Minute)
	rows := ComputeActorGroupRollups([]models.RollupJobFact{early, late, none}, idx)
	require.Len(t, rows, 2)
	byGroup := map[string]models.ActorGroupRollup{}
	for _, r := range rows {
		byGroup[r.GroupName] = r
	}
	assert.Equal(t, int64(1), byGroup["team-a"].Successes, "the group at the time of the run")
	assert.Equal(t, int64(1), byGroup["team-b"].PipelineFailures)
}

func TestComputeEvalRollups(t *testing.T) {
	facts := []models.RollupEventFact{
		{OrgID: "o", ProjectID: sp("p"), Source: "webhook", ReceivedAt: base, FirstStatusAt: base.Add(10 * time.Second)},
		{OrgID: "o", ProjectID: sp("p"), Source: "webhook", ReceivedAt: base, FirstStatusAt: base.Add(30 * time.Second)},
	}
	rows := ComputeEvalRollups(facts, Hourly)
	require.Len(t, rows, 1)
	assert.Equal(t, int64(2), rows[0].Events)
	assert.Equal(t, 20.0, *rows[0].LatencyP50Seconds)
}

func rollup(bucket time.Time, successes, jobFail, infraFail int64, durations ...float64) models.JobRollup {
	h := NewHistogram()
	for _, d := range durations {
		h.Observe(d)
	}
	return models.JobRollup{BucketStart: bucket, OrgID: "o", Runs: successes + jobFail + infraFail, Successes: successes,
		FailedJob: jobFail, FailedPlatform: infraFail, DurationHist: pq.Int64Array(h), Lane: "default"}
}

func passRule() *models.AlertRule {
	r := &models.AlertRule{RuleID: "r1", OrgID: "o", Name: "ci-pass", Scope: models.AlertScopeWorkflow, ProjectID: sp("p"),
		WorkflowSecurityID: "ci", Condition: models.AlertConditionPassRateBelow, Threshold: 0.8, ResolveMargin: 0.05,
		RefClassBucket: "default"}
	ApplyAlertDefaults(r)
	return r
}

func TestMeasureIgnoresInfraFailures(t *testing.T) {
	rule := passRule()
	// 9 successes, 1 job failure, 50 infra failures: pass rate 0.9.
	m := Measure(rule, []models.JobRollup{rollup(base, 9, 1, 50)}, nil)
	require.True(t, m.OK)
	assert.InDelta(t, 0.9, m.Value, 1e-9)
	assert.False(t, m.Breach)
	// Fewer than min_runs pipeline runs: no data, even with many infra failures.
	m = Measure(rule, []models.JobRollup{rollup(base, 1, 1, 500)}, nil)
	assert.False(t, m.OK)
}

func TestStepHysteresis(t *testing.T) {
	rule := passRule()
	now := base
	bad := Measurement{OK: true, Value: 0.5, Breach: true, Runs: 10}
	band := Measurement{OK: true, Value: 0.82, Runs: 10} // above 0.8, below 0.85
	good := Measurement{OK: true, Value: 0.95, Recovered: true, Runs: 10}

	assert.Equal(t, "", Step(rule, bad, now), "fire_after is 2")
	assert.Equal(t, "", Step(rule, band, now), "a non-breach resets the streak")
	assert.Equal(t, "", Step(rule, bad, now))
	assert.Equal(t, models.AlertTransitionFiring, Step(rule, bad, now))
	assert.Equal(t, models.AlertStateFiring, rule.State)
	assert.Equal(t, "", Step(rule, good, now))
	assert.Equal(t, "", Step(rule, band, now), "the hysteresis band keeps the rule firing")
	assert.Equal(t, 0, rule.RecoverStreak)
	assert.Equal(t, "", Step(rule, Measurement{}, now), "no data keeps the state")
	assert.Equal(t, models.AlertStateFiring, rule.State)
	assert.Equal(t, "", Step(rule, good, now))
	assert.Equal(t, models.AlertTransitionResolved, Step(rule, good, now))
	assert.Equal(t, models.AlertStateOK, rule.State)
}

func TestMeasureDurationRegression(t *testing.T) {
	rule := passRule()
	rule.Condition = models.AlertConditionDurationRegression
	rule.Factor = 1.5
	baseline := []models.JobRollup{rollup(base, 10, 0, 0, 100, 100, 100, 100, 100, 100, 100, 100, 100, 100)}
	slow := []models.JobRollup{rollup(base.Add(48*time.Hour), 10, 0, 0, 400, 400, 400, 400, 400, 400, 400, 400, 400, 400)}
	m := Measure(rule, slow, baseline)
	require.True(t, m.OK)
	assert.True(t, m.Breach)
	same := Measure(rule, baseline, baseline)
	assert.False(t, same.Breach)
	assert.True(t, same.Recovered)
	assert.False(t, Measure(rule, slow, nil).OK, "no baseline is no data")
}

func TestErrorBudget(t *testing.T) {
	r, s := ErrorBudget(0.99, 1000, 1000)
	assert.Equal(t, 1.0, r)
	assert.Equal(t, BudgetOK, s)
	r, s = ErrorBudget(0.99, 995, 1000) // 5 bad of 10 allowed
	assert.InDelta(t, 0.5, r, 1e-9)
	assert.Equal(t, BudgetOK, s)
	r, s = ErrorBudget(0.99, 992, 1000)
	assert.InDelta(t, 0.2, r, 1e-9)
	assert.Equal(t, BudgetWarning, s)
	r, s = ErrorBudget(0.99, 980, 1000)
	assert.InDelta(t, -1.0, r, 1e-9)
	assert.Equal(t, BudgetExhausted, s)
	_, s = ErrorBudget(0.99, 0, 0)
	assert.Equal(t, BudgetNoData, s)
}

func TestAvailability(t *testing.T) {
	hour := base
	beats := []models.HeartbeatHour{{HourStart: hour, UpMask: int64(^uint64(0) >> 4)}} // minutes 0..59
	up, total := Availability(beats, base.Add(-24*time.Hour), base.Add(24*time.Hour), base.Add(2*time.Hour))
	assert.Equal(t, 60.0, up)
	assert.Equal(t, 120.0, total, "measured from the first heartbeat to now")
	_, total = Availability(nil, base, base.Add(time.Hour), base.Add(time.Hour))
	assert.Equal(t, 0.0, total)
}

func TestTargetsAndValidation(t *testing.T) {
	stored := []models.SLOTarget{{OrgID: "o", SLI: models.SLIQueueWait, Lane: "pr", Objective: 0.9, ThresholdSeconds: 900, WindowDays: 30}}
	targets := EffectiveTargets("o", stored)
	assert.Len(t, targets, 5)
	assert.Equal(t, 900.0, TargetFor(targets, models.SLIQueueWait, "pr").ThresholdSeconds)
	assert.Equal(t, 300.0, TargetFor(targets, models.SLIQueueWait, "default").ThresholdSeconds)
	assert.Error(t, ValidateSLOTarget(&models.SLOTarget{SLI: models.SLIAvailability, Lane: "pr", Objective: 0.9}))
	assert.Error(t, ValidateSLOTarget(&models.SLOTarget{SLI: models.SLIQueueWait, Objective: 1}))
	assert.NoError(t, ValidateSLOTarget(&models.SLOTarget{SLI: models.SLIInfraFailureRate, Objective: 0.99}))
}

func TestValidateAlertRule(t *testing.T) {
	assert.NoError(t, ValidateAlertRule(passRule()))
	r := passRule()
	r.Scope = models.AlertScopeOwnerGroup
	assert.Error(t, ValidateAlertRule(r))
	r.WorkflowSecurityID, r.OwnerGroup = "", "team-a"
	assert.NoError(t, ValidateAlertRule(r))
	r.Threshold = 2
	assert.Error(t, ValidateAlertRule(r))
}

func TestMetricLabelsAreLowCardinality(t *testing.T) {
	for _, labels := range [][]string{JobDurationLabels, QueueWaitLabels, InfraFailureLabels, EvalLatencyLabels} {
		for _, l := range labels {
			assert.NotContains(t, []string{"workflow", "project", "job", "job_id", "workflow_id", "project_id", "org"}, l)
		}
	}
	groups := &OwnerGroupLabels{Limit: 2}
	assert.Equal(t, "none", groups.Label(""))
	assert.Equal(t, "a", groups.Label("a"))
	assert.Equal(t, "b", groups.Label("b"))
	assert.Equal(t, "other", groups.Label("c"))
	assert.Equal(t, "a", groups.Label("a"))

	f := fact("o", "p", "ci", "failed", "infra.platform", base, time.Second, time.Second)
	l := LabelsOf(&f, groups)
	assert.Equal(t, JobLabels{Queue: "q1", Lane: "default", FailureClass: "infra.platform", OwnerGroup: "none", PrincipalKind: "human"}, l)
	f.Lane, f.ActorKind = "", ""
	l = LabelsOf(&f, groups)
	assert.Equal(t, "none", l.Lane)
	assert.Equal(t, "system", l.PrincipalKind)
}

func histogramCount(t *testing.T, c prometheus.Collector) uint64 {
	ch := make(chan prometheus.Metric, 100)
	c.Collect(ch)
	close(ch)
	var n uint64
	for m := range ch {
		var out dto.Metric
		require.NoError(t, m.Write(&out))
		if out.Histogram != nil {
			n += out.Histogram.GetSampleCount()
		}
	}
	return n
}

// fakeStore is an in-memory ObservabilityStore.
type fakeStore struct {
	store.ObservabilityStore
	states      map[string]*models.ObservabilityState
	jobs        []models.RollupJobFact
	events      []models.RollupEventFact
	hourly      map[time.Time][]models.JobRollup
	daily       map[time.Time][]models.JobRollup
	evalDaily   map[time.Time][]models.EvalRollup
	factReads   int
	rules       []models.AlertRule
	transitions []models.AlertTransition
	outbox      []models.AlertOutboxEvent
	beats       map[time.Time]int64
}

func newFake() *fakeStore {
	return &fakeStore{states: map[string]*models.ObservabilityState{}, hourly: map[time.Time][]models.JobRollup{},
		daily: map[time.Time][]models.JobRollup{}, evalDaily: map[time.Time][]models.EvalRollup{}, beats: map[time.Time]int64{}}
}

func (f *fakeStore) GetObservabilityState(_ context.Context, name string) (*models.ObservabilityState, error) {
	if s, ok := f.states[name]; ok {
		c := *s
		return &c, nil
	}
	return nil, nil
}
func (f *fakeStore) SaveObservabilityState(_ context.Context, s *models.ObservabilityState) error {
	c := *s
	f.states[s.Name] = &c
	return nil
}
func (f *fakeStore) ListChangedJobBuckets(_ context.Context, from, _ time.Time, to time.Time) ([]time.Time, error) {
	var out []time.Time
	for _, j := range f.jobs {
		if j.CompletedAt.After(from) && !j.CompletedAt.After(to) {
			out = append(out, BucketStart(j.CompletedAt, Hourly))
		}
	}
	return out, nil
}
func (f *fakeStore) ListRollupJobFacts(_ context.Context, from, to time.Time) ([]models.RollupJobFact, error) {
	f.factReads++
	var out []models.RollupJobFact
	for _, j := range f.jobs {
		if !j.CompletedAt.Before(from) && j.CompletedAt.Before(to) {
			out = append(out, j)
		}
	}
	return out, nil
}
func (f *fakeStore) ReplaceJobRollups(_ context.Context, g string, from, _ time.Time, rows []models.JobRollup) error {
	if g == "hour" {
		for k := range f.hourly {
			if !k.Before(from) && k.Before(from.Add(24*time.Hour)) {
				delete(f.hourly, k)
			}
		}
		for _, r := range rows {
			f.hourly[r.BucketStart] = append(f.hourly[r.BucketStart], r)
		}
		return nil
	}
	delete(f.daily, from)
	f.daily[from] = rows
	return nil
}
func (f *fakeStore) ReplaceActorGroupRollups(context.Context, time.Time, time.Time, []models.ActorGroupRollup) error {
	return nil
}
func (f *fakeStore) ListGroupMembershipChanges(context.Context, []string, time.Time) ([]models.GroupMembershipChange, error) {
	return nil, nil
}
func (f *fakeStore) ListChangedEventBuckets(_ context.Context, from, to time.Time) ([]time.Time, error) {
	var out []time.Time
	for _, e := range f.events {
		if e.FirstStatusAt.After(from) && !e.FirstStatusAt.After(to) {
			out = append(out, e.FirstStatusAt)
		}
	}
	return out, nil
}
func (f *fakeStore) ListRollupEventFacts(_ context.Context, from, to time.Time) ([]models.RollupEventFact, error) {
	var out []models.RollupEventFact
	for _, e := range f.events {
		if !e.FirstStatusAt.Before(from) && e.FirstStatusAt.Before(to) {
			out = append(out, e)
		}
	}
	return out, nil
}
func (f *fakeStore) ReplaceEvalRollups(_ context.Context, g string, from, _ time.Time, rows []models.EvalRollup) error {
	if g == "day" {
		f.evalDaily[from] = rows
	}
	return nil
}
func (f *fakeStore) RecordHeartbeat(_ context.Context, t time.Time) error {
	f.beats[t.Truncate(time.Hour)] |= 1 << uint(t.Minute())
	return nil
}
func (f *fakeStore) DeleteRollupsBefore(context.Context, time.Time, time.Time) error { return nil }
func (f *fakeStore) ListJobRollups(_ context.Context, g string, filter store.RollupFilter) ([]models.JobRollup, error) {
	src := f.daily
	if g == "hour" {
		src = f.hourly
	}
	var out []models.JobRollup
	for k, rows := range src {
		if k.Before(filter.From) || !k.Before(filter.To) {
			continue
		}
		for _, r := range rows {
			if filter.WorkflowSecurityID != "" && r.WorkflowSecurityID != filter.WorkflowSecurityID {
				continue
			}
			if filter.RefClassBucket != "" && r.RefClassBucket != filter.RefClassBucket {
				continue
			}
			out = append(out, r)
		}
	}
	return out, nil
}
func (f *fakeStore) ListEnabledAlertRules(context.Context) ([]models.AlertRule, error) {
	return f.rules, nil
}
func (f *fakeStore) ApplyAlertEvaluation(_ context.Context, rule *models.AlertRule, tr *models.AlertTransition, ev *models.AlertOutboxEvent) error {
	for i := range f.rules {
		if f.rules[i].RuleID == rule.RuleID {
			f.rules[i] = *rule
		}
	}
	if tr != nil {
		f.transitions = append(f.transitions, *tr)
	}
	if ev != nil {
		f.outbox = append(f.outbox, *ev)
	}
	return nil
}

func totalRuns(rows map[time.Time][]models.JobRollup) int64 {
	var n int64
	for _, list := range rows {
		for _, r := range list {
			n += r.Runs
		}
	}
	return n
}

func TestLoopBackfillThenIncremental(t *testing.T) {
	fs := newFake()
	now := base.Add(72 * time.Hour)
	for d := 0; d < 3; d++ {
		for i := 0; i < 4; i++ {
			fs.jobs = append(fs.jobs, fact("o", "p", "ci", "completed", "", base.Add(time.Duration(d)*24*time.Hour+time.Duration(i)*time.Hour), time.Second, time.Minute))
		}
	}
	loop := &Loop{Store: fs, Now: func() time.Time { return now }, Observe: true}
	before := histogramCount(t, JobDuration)
	require.NoError(t, loop.Tick(context.Background()))
	assert.Equal(t, int64(12), totalRuns(fs.daily))
	assert.Equal(t, int64(12), totalRuns(fs.hourly))
	require.NotNil(t, fs.states[StateJobs].BackfilledAt)
	assert.Equal(t, now.Add(-2*time.Minute), fs.states[StateJobs].Watermark)
	assert.Equal(t, before, histogramCount(t, JobDuration), "a backfill observes no metric")
	assert.Equal(t, 3, fs.factReads, "one read for each day")

	// One new job: only its day is read again; the metric sees it one time.
	now = now.Add(10 * time.Minute)
	fs.jobs = append(fs.jobs, fact("o", "p", "ci", "failed", "job", now.Add(-9*time.Minute), time.Second, time.Minute))
	fs.factReads = 0
	require.NoError(t, loop.Tick(context.Background()))
	assert.Equal(t, 1, fs.factReads)
	assert.Equal(t, int64(13), totalRuns(fs.daily))
	assert.Equal(t, before+1, histogramCount(t, JobDuration))

	// Nothing new: no fact read, the same totals, no new observation.
	now = now.Add(time.Minute)
	fs.factReads = 0
	require.NoError(t, loop.Tick(context.Background()))
	assert.Equal(t, 0, fs.factReads)
	assert.Equal(t, int64(13), totalRuns(fs.daily))
	assert.Equal(t, before+1, histogramCount(t, JobDuration))

	// A job reported late with an old completion time is counted by the
	// reconcile pass.
	fs.jobs = append(fs.jobs, fact("o", "p", "ci", "completed", "", now.Add(-30*time.Hour), time.Second, time.Minute))
	loop.lastReconcile = now.Add(-2 * time.Hour)
	require.NoError(t, loop.Tick(context.Background()))
	assert.Equal(t, int64(14), totalRuns(fs.daily))
}

func TestLoopEventsAndHeartbeat(t *testing.T) {
	fs := newFake()
	now := base.Add(time.Hour)
	fs.events = []models.RollupEventFact{{OrgID: "o", Source: "webhook", ReceivedAt: base, FirstStatusAt: base.Add(5 * time.Second)}}
	loop := &Loop{Store: fs, Now: func() time.Time { return now }}
	require.NoError(t, loop.Tick(context.Background()))
	require.Len(t, fs.evalDaily[base], 1)
	loop.heartbeat(context.Background())
	assert.NotZero(t, fs.beats[now.Truncate(time.Hour)])
}

func TestEvaluatorEmitsOutboxEventsWithHysteresis(t *testing.T) {
	fs := newFake()
	now := base.Add(10 * time.Hour)
	rule := passRule()
	rule.FireAfter, rule.ResolveAfter = 1, 1
	fs.rules = []models.AlertRule{*rule}
	// Pass rate 0.5 in the window; infra failures do not matter.
	bucket := BucketStart(now, Hourly)
	r := rollup(bucket, 5, 5, 100)
	r.WorkflowSecurityID, r.RefClassBucket = "ci", "default"
	fs.hourly[bucket] = []models.JobRollup{r}
	e := &Evaluator{Store: fs}
	firing, err := e.EvaluateAll(context.Background(), now)
	require.NoError(t, err)
	assert.Equal(t, 1, firing[models.AlertConditionPassRateBelow])
	require.Len(t, fs.outbox, 1)
	ev := fs.outbox[0]
	assert.Equal(t, models.OutboundEventAlertFiring, ev.EventType)
	assert.Equal(t, "ci", ev.WorkflowSecurityID)
	alert := ev.Details["alert"].(map[string]interface{})
	assert.InDelta(t, 0.5, alert["value"].(float64), 1e-9)
	assert.Equal(t, []string{"job", "timeout", "oom"}, alert["counted_failure_classes"])

	// Still bad: no second event.
	_, err = e.EvaluateAll(context.Background(), now.Add(5*time.Minute))
	require.NoError(t, err)
	assert.Len(t, fs.outbox, 1)

	// Recovered: one resolved event.
	r.Successes, r.FailedJob = 10, 0
	fs.hourly[bucket] = []models.JobRollup{r}
	_, err = e.EvaluateAll(context.Background(), now.Add(10*time.Minute))
	require.NoError(t, err)
	require.Len(t, fs.outbox, 2)
	assert.Equal(t, models.OutboundEventAlertResolved, fs.outbox[1].EventType)
	assert.Len(t, fs.transitions, 2)

	// Infra failures alone never fire a pass-rate rule.
	r = rollup(bucket, 10, 0, 1000)
	r.WorkflowSecurityID, r.RefClassBucket = "ci", "default"
	fs.hourly[bucket] = []models.JobRollup{r}
	_, err = e.EvaluateAll(context.Background(), now.Add(15*time.Minute))
	require.NoError(t, err)
	assert.Len(t, fs.outbox, 2)
}

func TestTrendFillsEmptyBuckets(t *testing.T) {
	rows := []models.JobRollup{rollup(base, 1, 1, 0, 10)}
	points := Trend(rows, base, base.Add(3*24*time.Hour), Daily)
	require.Len(t, points, 3)
	assert.Equal(t, int64(2), points[0].Runs)
	assert.InDelta(t, 0.5, *points[0].PassRate, 1e-9)
	assert.Nil(t, points[1].PassRate)
	keys := []string{}
	aggs, _ := GroupBy([]models.JobRollup{rollup(base, 1, 0, 0), {Lane: "pr", Runs: 1}}, func(r *models.JobRollup) string { return r.Lane })
	for k := range aggs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	assert.Equal(t, []string{"default", "pr"}, keys)
	assert.False(t, math.IsNaN(float64(aggs["pr"].Runs)))
}
