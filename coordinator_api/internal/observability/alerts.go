package observability

import (
	"context"
	"fmt"
	"math"
	"regexp"
	"time"

	"github.com/catalystcommunity/app-utils-go/logging"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/outboundevents"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/owners"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

// Measurement is the value of an alert condition at one evaluation.
type Measurement struct {
	// OK is false when there is not enough data (fewer than min_runs runs
	// in the window, or in the baseline).
	OK       bool
	Value    float64
	Baseline float64
	// Limit is the value at which the rule fires.
	Limit float64
	Runs  int64
	// Breach is true when the value is past the limit; Recovered is true
	// when it is better than the limit by the resolve margin.
	Breach    bool
	Recovered bool
}

// AlertWindow returns the hourly buckets of the window and of the baseline
// of a rule at now. The window includes the current hour.
func AlertWindow(rule *models.AlertRule, now time.Time) (windowFrom, windowTo, baselineFrom time.Time) {
	windowTo = BucketStart(now, Hourly).Add(time.Hour)
	windowFrom = windowTo.Add(-time.Duration(rule.WindowHours) * time.Hour)
	baselineFrom = windowFrom.Add(-time.Duration(rule.BaselineDays) * 24 * time.Hour)
	return
}

// AlertFilter returns the rollup filter of the scope of a rule.
func AlertFilter(rule *models.AlertRule, from, to time.Time) store.RollupFilter {
	f := store.RollupFilter{OrgID: rule.OrgID, From: from, To: to, AllProjects: true, RefClassBucket: rule.RefClassBucket}
	if rule.ProjectID != nil {
		f.AllProjects = false
		f.ProjectIDs = []string{*rule.ProjectID}
	}
	switch rule.Scope {
	case models.AlertScopeWorkflow:
		f.WorkflowSecurityID = rule.WorkflowSecurityID
	case models.AlertScopeOwnerGroup:
		f.OwnerGroup = rule.OwnerGroup
	}
	return f
}

// Measure computes the condition of a rule from its window rows and its
// baseline rows (hourly rollups). Only pipeline runs count: a failure of an
// infra.* class never changes a pass rate or a duration.
func Measure(rule *models.AlertRule, window, baseline []models.JobRollup) Measurement {
	w := AggregateRows(window)
	minRuns := int64(rule.MinRuns)
	if minRuns < 1 {
		minRuns = 1
	}
	switch rule.Condition {
	case models.AlertConditionPassRateBelow:
		m := Measurement{Runs: w.PipelineRuns(), Limit: rule.Threshold}
		rate, ok := w.PassRate()
		if !ok || m.Runs < minRuns {
			return m
		}
		m.OK, m.Value = true, rate
		m.Breach = rate < rule.Threshold
		m.Recovered = rate >= math.Min(1, rule.Threshold+rule.ResolveMargin)
		return m
	case models.AlertConditionDurationRegression:
		b := AggregateRows(baseline)
		m := Measurement{Runs: w.Duration.Total()}
		if m.Runs < minRuns || b.Duration.Total() < minRuns {
			return m
		}
		value, _ := w.DurationQuantile(0.95)
		base, _ := b.DurationQuantile(0.95)
		m.OK, m.Value, m.Baseline = true, value, base
		m.Limit = base * rule.Factor
		m.Breach = value > m.Limit
		m.Recovered = value <= m.Limit*(1-rule.ResolveMargin)
		return m
	}
	return Measurement{}
}

// Step applies one measurement to the state of a rule, with hysteresis. It
// returns the transition (AlertTransitionFiring or AlertTransitionResolved)
// or "". A rule fires after fire_after breaches in a row, and resolves after
// resolve_after recoveries in a row. A value between the limit and the
// resolve margin resets the recover streak, so the rule stays firing. With
// no data, a rule keeps its state and its streaks reset.
func Step(rule *models.AlertRule, m Measurement, now time.Time) string {
	rule.LastEvaluatedAt = &now
	rule.LastRuns = int(m.Runs)
	if !m.OK {
		rule.LastValue, rule.LastBaseline = nil, nil
		rule.BreachStreak, rule.RecoverStreak = 0, 0
		return ""
	}
	value := m.Value
	rule.LastValue = &value
	if rule.Condition == models.AlertConditionDurationRegression {
		base := m.Baseline
		rule.LastBaseline = &base
	}
	fireAfter, resolveAfter := max(rule.FireAfter, 1), max(rule.ResolveAfter, 1)
	if rule.State != models.AlertStateFiring {
		rule.RecoverStreak = 0
		if !m.Breach {
			rule.BreachStreak = 0
			return ""
		}
		rule.BreachStreak++
		if rule.BreachStreak < fireAfter {
			return ""
		}
		rule.State, rule.BreachStreak, rule.StateChangedAt = models.AlertStateFiring, 0, &now
		return models.AlertTransitionFiring
	}
	rule.BreachStreak = 0
	if !m.Recovered {
		rule.RecoverStreak = 0
		return ""
	}
	rule.RecoverStreak++
	if rule.RecoverStreak < resolveAfter {
		return ""
	}
	rule.State, rule.RecoverStreak, rule.StateChangedAt = models.AlertStateOK, 0, &now
	return models.AlertTransitionResolved
}

// Evaluator evaluates the enabled alert rules.
type Evaluator struct {
	Store store.ObservabilityStore
	// Owners reads the owner snapshot of a workflow (owners.ForWorkflow).
	// It is the application store; nil gives no owner contact.
	Owners interface{}
	// UIBaseURL is the base of the dashboard link.
	UIBaseURL string
}

// subscriptionLister lists the event subscriptions of an organization
// (store.OutboundEventStore).
type subscriptionLister interface {
	ListEventSubscriptions(ctx context.Context, orgID string, projectID *string) ([]models.EventSubscription, error)
}

// EvaluateAll evaluates every enabled rule at now and returns the count of
// firing rules by condition. A rule whose alert.firing and alert.resolved
// events no subscription receives is not evaluated: Reactorcide only emits
// alerts, so a rule with no receiver does no work. Its state stays as it
// is, and it does not count in the result. If the subscriptions cannot be
// read, the rule is evaluated.
func (e *Evaluator) EvaluateAll(ctx context.Context, now time.Time) (map[string]int, error) {
	rules, err := e.Store.ListEnabledAlertRules(ctx)
	if err != nil {
		return nil, err
	}
	firing := map[string]int{models.AlertConditionPassRateBelow: 0, models.AlertConditionDurationRegression: 0}
	lister, _ := e.Store.(subscriptionLister)
	subsByOrg := map[string][]models.EventSubscription{}
	for i := range rules {
		if lister != nil {
			subs, ok := subsByOrg[rules[i].OrgID]
			if !ok {
				var listErr error
				subs, listErr = lister.ListEventSubscriptions(ctx, rules[i].OrgID, nil)
				if listErr != nil {
					logging.Log.WithError(listErr).WithField("alert_rule", rules[i].Name).Warn("alert rule: subscriptions not readable; evaluating the rule")
					subs = nil
				} else {
					subsByOrg[rules[i].OrgID] = subs
				}
				ok = listErr == nil
			}
			if ok && !e.HasReceiver(ctx, &rules[i], subs, now) {
				continue
			}
		}
		if err := e.Evaluate(ctx, &rules[i], now); err != nil {
			logging.Log.WithError(err).WithField("alert_rule", rules[i].Name).Warn("alert rule evaluation failed")
		}
		if rules[i].State == models.AlertStateFiring {
			firing[rules[i].Condition]++
		}
	}
	return firing, nil
}

// HasReceiver reports whether a subscription in subs receives the
// alert.firing or the alert.resolved event of rule. It builds the event as
// Evaluate does (scope, project, workflow, owner group) and uses the
// dispatcher's matcher.
func (e *Evaluator) HasReceiver(ctx context.Context, rule *models.AlertRule, subs []models.EventSubscription, now time.Time) bool {
	enabled := false
	for i := range subs {
		if subs[i].Enabled && subs[i].OwnerActive {
			enabled = true
			break
		}
	}
	if !enabled {
		return false
	}
	for _, transition := range []string{models.AlertTransitionFiring, models.AlertTransitionResolved} {
		ev := e.outboxEvent(ctx, rule, transition, Measurement{}, now)
		facts := &models.OutboxEventFacts{EventOutbox: models.EventOutbox{
			EventType: ev.EventType, OrgID: ev.OrgID, ProjectID: ev.ProjectID,
			WorkflowSecurityID: ev.WorkflowSecurityID, OwnerGroup: ev.OwnerGroup, Status: ev.Status,
		}}
		for i := range subs {
			if outboundevents.Matches(&subs[i], facts) {
				return true
			}
		}
	}
	return false
}

// Evaluate evaluates one rule and saves its state. A transition writes an
// alert transition and an alert.firing or alert.resolved outbox event.
func (e *Evaluator) Evaluate(ctx context.Context, rule *models.AlertRule, now time.Time) error {
	windowFrom, windowTo, baselineFrom := AlertWindow(rule, now)
	window, err := e.Store.ListJobRollups(ctx, string(Hourly), AlertFilter(rule, windowFrom, windowTo))
	if err != nil {
		return err
	}
	var baseline []models.JobRollup
	if rule.Condition == models.AlertConditionDurationRegression {
		// The baseline can be older than the hourly retention: read whole
		// days before the window.
		baseline, err = e.Store.ListJobRollups(ctx, string(Daily), AlertFilter(rule, BucketStart(baselineFrom, Daily), BucketStart(windowFrom, Daily)))
		if err != nil {
			return err
		}
	}
	m := Measure(rule, window, baseline)
	transition := Step(rule, m, now)
	if transition == "" {
		return e.Store.ApplyAlertEvaluation(ctx, rule, nil, nil)
	}
	value, limit := m.Value, m.Limit
	t := &models.AlertTransition{RuleID: rule.RuleID, OrgID: rule.OrgID, State: transition, Value: &value, Threshold: &limit, Runs: int(m.Runs), OccurredAt: now}
	if rule.Condition == models.AlertConditionDurationRegression {
		base := m.Baseline
		t.Baseline = &base
	}
	return e.Store.ApplyAlertEvaluation(ctx, rule, t, e.outboxEvent(ctx, rule, transition, m, now))
}

func (e *Evaluator) outboxEvent(ctx context.Context, rule *models.AlertRule, transition string, m Measurement, now time.Time) *models.AlertOutboxEvent {
	eventType := models.OutboundEventAlertFiring
	if transition == models.AlertTransitionResolved {
		eventType = models.OutboundEventAlertResolved
	}
	alert := map[string]interface{}{
		"rule_id": rule.RuleID, "name": rule.Name, "description": rule.Description, "condition": rule.Condition,
		"scope": rule.Scope, "state": transition, "value": m.Value, "limit": m.Limit, "runs": m.Runs,
		"threshold": rule.Threshold, "factor": rule.Factor, "window_hours": rule.WindowHours,
		"ref_class_bucket": rule.RefClassBucket,
		// Only these classes count. Infrastructure classes never page a
		// pipeline owner (docs/observability.md).
		"counted_failure_classes": []string{models.FailureClassJob, models.FailureClassTimeout, models.FailureClassOOM},
	}
	if rule.Condition == models.AlertConditionDurationRegression {
		alert["baseline"] = m.Baseline
		alert["baseline_days"] = rule.BaselineDays
	}
	details := models.JSONB{"alert": alert}
	ownerGroup := rule.OwnerGroup
	if rule.Scope == models.AlertScopeWorkflow && rule.ProjectID != nil && e.Owners != nil {
		details["workflow_name"] = rule.WorkflowSecurityID
		ownership, err := owners.ForWorkflow(ctx, e.Owners, *rule.ProjectID, rule.WorkflowSecurityID)
		if err == nil && ownership.Known() {
			ownerGroup = ownership.GroupName()
			if !ownership.Contact.IsZero() {
				details["owner_contact"] = map[string]interface{}{
					"channel": ownership.Contact.Channel, "help_url": ownership.Contact.HelpURL, "email": ownership.Contact.Email,
				}
			}
			if len(ownership.Subjects) > 0 {
				details["owner_subjects"] = ownership.Subjects
			}
		}
	}
	if e.UIBaseURL != "" {
		details["dashboard_url"] = fmt.Sprintf("%s/app/insights?view=pipeline", trimSlash(e.UIBaseURL))
	}
	return &models.AlertOutboxEvent{
		EventType: eventType, OrgID: rule.OrgID, ProjectID: rule.ProjectID, WorkflowSecurityID: rule.WorkflowSecurityID,
		OwnerGroup: ownerGroup, Status: transition, OccurredAt: now, Details: details,
	}
}

func trimSlash(s string) string {
	for len(s) > 0 && s[len(s)-1] == '/' {
		s = s[:len(s)-1]
	}
	return s
}

var alertNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// ApplyAlertDefaults fills unset fields of a new rule.
func ApplyAlertDefaults(rule *models.AlertRule) {
	if rule.WindowHours == 0 {
		rule.WindowHours = 24
	}
	if rule.BaselineDays == 0 {
		rule.BaselineDays = 7
	}
	if rule.MinRuns == 0 {
		rule.MinRuns = 5
	}
	if rule.FireAfter == 0 {
		rule.FireAfter = 2
	}
	if rule.ResolveAfter == 0 {
		rule.ResolveAfter = 2
	}
	if rule.Factor == 0 {
		rule.Factor = 1.5
	}
	if rule.Condition == models.AlertConditionPassRateBelow && rule.Threshold == 0 {
		rule.Threshold = 0.9
	}
	if rule.State == "" {
		rule.State = models.AlertStateOK
	}
}

// ValidateAlertRule checks a rule before a save.
func ValidateAlertRule(rule *models.AlertRule) error {
	if !alertNamePattern.MatchString(rule.Name) {
		return fmt.Errorf("name must be lowercase letters, digits, and hyphens (at most 63)")
	}
	switch rule.Scope {
	case models.AlertScopeWorkflow:
		if rule.ProjectID == nil || rule.WorkflowSecurityID == "" || rule.OwnerGroup != "" {
			return fmt.Errorf("a workflow rule needs a project and a workflow, and no owner group")
		}
	case models.AlertScopeProject:
		if rule.ProjectID == nil || rule.WorkflowSecurityID != "" || rule.OwnerGroup != "" {
			return fmt.Errorf("a project rule needs a project, and no workflow or owner group")
		}
	case models.AlertScopeOwnerGroup:
		if rule.OwnerGroup == "" || rule.WorkflowSecurityID != "" {
			return fmt.Errorf("an owner_group rule needs an owner group, and no workflow")
		}
	default:
		return fmt.Errorf("scope must be workflow, project, or owner_group")
	}
	switch rule.Condition {
	case models.AlertConditionPassRateBelow:
		if rule.Threshold <= 0 || rule.Threshold > 1 {
			return fmt.Errorf("threshold must be a pass rate in (0, 1]")
		}
	case models.AlertConditionDurationRegression:
		if rule.Factor < 1 || rule.Factor > 100 {
			return fmt.Errorf("factor must be between 1 and 100")
		}
	default:
		return fmt.Errorf("condition must be pass_rate_below or duration_regression")
	}
	if rule.RefClassBucket != "" {
		valid := false
		for _, b := range models.RefClassBuckets {
			valid = valid || b == rule.RefClassBucket
		}
		if !valid {
			return fmt.Errorf("ref_class_bucket must be empty or one of %v", models.RefClassBuckets)
		}
	}
	if rule.WindowHours < 1 || rule.WindowHours > 720 || rule.BaselineDays < 1 || rule.BaselineDays > 90 {
		return fmt.Errorf("window_hours must be 1-720 and baseline_days 1-90")
	}
	if rule.MinRuns < 1 || rule.FireAfter < 1 || rule.FireAfter > 100 || rule.ResolveAfter < 1 || rule.ResolveAfter > 100 {
		return fmt.Errorf("min_runs, fire_after, and resolve_after must be at least 1 (fire_after and resolve_after at most 100)")
	}
	if rule.ResolveMargin < 0 || rule.ResolveMargin >= 1 {
		return fmt.Errorf("resolve_margin must be in [0, 1)")
	}
	return nil
}
