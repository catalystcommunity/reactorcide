package observability

import (
	"context"
	"sort"
	"time"

	"github.com/catalystcommunity/app-utils-go/logging"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

// LeaseName is the coordinator lease of the rollup loop.
const LeaseName = "observability-rollups"

// Watermark names in observability_state.
const (
	StateJobs      = "jobs"
	StateSummaries = "summaries"
	StateEvents    = "events"
)

// Config is the configuration of the loop. Zero values use the defaults.
type Config struct {
	// Interval between rollup passes (default 60s).
	Interval time.Duration
	// Lag: rows newer than now - Lag wait for the next pass, so a
	// transaction that commits a little late is not missed (default 2m).
	Lag time.Duration
	// Every ReconcileEvery (default 1h) a pass also computes the last
	// ReconcileSpan (default 48h) again: a job whose worker reported an older
	// completion time, or a late resource summary, is then counted.
	ReconcileEvery time.Duration
	ReconcileSpan  time.Duration
	// AlertEvery is the alert evaluation interval (default 5m).
	AlertEvery time.Duration
	// Retention of hourly rows (default 35 days) and of daily rows,
	// heartbeats, and alert transitions (default 400 days).
	HourlyRetention time.Duration
	DailyRetention  time.Duration
	// HeartbeatEvery is the availability heartbeat interval (default 15s).
	HeartbeatEvery time.Duration
	// OwnerGroupLabelLimit bounds the owner_group metric label.
	OwnerGroupLabelLimit int
}

func (c Config) withDefaults() Config {
	def := func(v *time.Duration, d time.Duration) {
		if *v <= 0 {
			*v = d
		}
	}
	def(&c.Interval, time.Minute)
	def(&c.Lag, 2*time.Minute)
	def(&c.ReconcileEvery, time.Hour)
	def(&c.ReconcileSpan, 48*time.Hour)
	def(&c.AlertEvery, 5*time.Minute)
	def(&c.HourlyRetention, 35*24*time.Hour)
	def(&c.DailyRetention, 400*24*time.Hour)
	def(&c.HeartbeatEvery, 15*time.Second)
	return c
}

// Loop computes the rollups, evaluates alerts, records heartbeats, and
// observes the Prometheus metrics. Run it under the lease LeaseName.
type Loop struct {
	Store  store.ObservabilityStore
	Config Config
	Alerts *Evaluator
	// Now is the clock (tests).
	Now func() time.Time
	// Observe is false in tests that must not touch the global metrics.
	Observe bool

	groups        OwnerGroupLabels
	lastReconcile time.Time
	lastAlerts    time.Time
	lastRetention time.Time
}

func (l *Loop) now() time.Time {
	if l.Now != nil {
		return l.Now().UTC()
	}
	return time.Now().UTC()
}

// Run runs passes until ctx ends.
func (l *Loop) Run(ctx context.Context) {
	cfg := l.Config.withDefaults()
	l.groups.Limit = cfg.OwnerGroupLabelLimit
	if l.Observe {
		RegisterMetrics()
	}
	pass := time.NewTimer(0)
	defer pass.Stop()
	beat := time.NewTicker(cfg.HeartbeatEvery)
	defer beat.Stop()
	l.heartbeat(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-beat.C:
			l.heartbeat(ctx)
		case <-pass.C:
			if err := l.Tick(ctx); err != nil && ctx.Err() == nil {
				logging.Log.WithError(err).Warn("observability rollup pass failed")
			}
			pass.Reset(cfg.Interval)
		}
	}
}

func (l *Loop) heartbeat(ctx context.Context) {
	if err := l.Store.RecordHeartbeat(ctx, l.now()); err != nil && ctx.Err() == nil {
		logging.Log.WithError(err).Debug("observability heartbeat failed")
	}
}

// Tick runs one pass.
func (l *Loop) Tick(ctx context.Context) error {
	cfg := l.Config.withDefaults()
	now := l.now()
	upper := now.Add(-cfg.Lag)
	reconcile := l.lastReconcile.IsZero() || now.Sub(l.lastReconcile) >= cfg.ReconcileEvery
	if err := l.jobsPass(ctx, now, upper, reconcile, cfg); err != nil {
		return err
	}
	if err := l.eventsPass(ctx, now, upper, reconcile, cfg); err != nil {
		return err
	}
	if reconcile {
		l.lastReconcile = now
	}
	if l.Alerts != nil && (l.lastAlerts.IsZero() || now.Sub(l.lastAlerts) >= cfg.AlertEvery) {
		firing, err := l.Alerts.EvaluateAll(ctx, now)
		if err != nil {
			logging.Log.WithError(err).Warn("alert evaluation failed")
		} else if l.Observe {
			for condition, n := range firing {
				AlertsFiring.WithLabelValues(condition).Set(float64(n))
			}
		}
		l.lastAlerts = now
	}
	if l.lastRetention.IsZero() || now.Sub(l.lastRetention) >= time.Hour {
		if err := l.Store.DeleteRollupsBefore(ctx, BucketStart(now.Add(-cfg.HourlyRetention), Hourly), BucketStart(now.Add(-cfg.DailyRetention), Daily)); err != nil {
			logging.Log.WithError(err).Warn("rollup retention failed")
		}
		l.lastRetention = now
	}
	return nil
}

func (l *Loop) state(ctx context.Context, name string) (*models.ObservabilityState, bool, error) {
	st, err := l.Store.GetObservabilityState(ctx, name)
	if err != nil {
		return nil, false, err
	}
	if st == nil {
		return &models.ObservabilityState{Name: name, Watermark: time.Unix(0, 0).UTC()}, true, nil
	}
	return st, false, nil
}

// days returns the distinct UTC days of a set of times, sorted.
func days(times []time.Time) []time.Time {
	seen := map[time.Time]bool{}
	var out []time.Time
	for _, t := range times {
		d := BucketStart(t, Daily)
		if !seen[d] {
			seen[d] = true
			out = append(out, d)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Before(out[j]) })
	return out
}

func reconcileDays(upper time.Time, span time.Duration) []time.Time {
	var out []time.Time
	for t := upper.Add(-span); !t.After(upper); t = t.Add(24 * time.Hour) {
		out = append(out, t)
	}
	return append(out, upper)
}

func (l *Loop) jobsPass(ctx context.Context, now, upper time.Time, reconcile bool, cfg Config) error {
	jobs, backfill, err := l.state(ctx, StateJobs)
	if err != nil {
		return err
	}
	summaries, _, err := l.state(ctx, StateSummaries)
	if err != nil {
		return err
	}
	if !upper.After(jobs.Watermark) && !reconcile {
		return nil
	}
	changed, err := l.Store.ListChangedJobBuckets(ctx, jobs.Watermark, summaries.Watermark, upper)
	if err != nil {
		return err
	}
	if reconcile && !backfill {
		changed = append(changed, reconcileDays(upper, cfg.ReconcileSpan)...)
	}
	hourlyFrom := BucketStart(now.Add(-cfg.HourlyRetention), Daily)
	for _, day := range days(changed) {
		end := day.Add(24 * time.Hour)
		facts, err := l.Store.ListRollupJobFacts(ctx, day, end)
		if err != nil {
			return err
		}
		if !day.Before(hourlyFrom) {
			if err := l.Store.ReplaceJobRollups(ctx, string(Hourly), day, end, ComputeJobRollups(facts, Hourly)); err != nil {
				return err
			}
		}
		if err := l.Store.ReplaceJobRollups(ctx, string(Daily), day, end, ComputeJobRollups(facts, Daily)); err != nil {
			return err
		}
		if err := l.actorGroups(ctx, facts, day, end); err != nil {
			return err
		}
		// Observe each newly finished job one time. A backfill observes
		// nothing: old jobs would distort the rates.
		if l.Observe && !backfill {
			for i := range facts {
				if facts[i].CompletedAt.After(jobs.Watermark) && !facts[i].CompletedAt.After(upper) {
					ObserveJob(&facts[i], &l.groups)
				}
			}
		}
	}
	ran := now
	jobs.Watermark, jobs.LastRunAt, jobs.LastError = upper, &ran, ""
	summaries.Watermark, summaries.LastRunAt = upper, &ran
	if backfill {
		jobs.BackfilledAt = &ran
		summaries.BackfilledAt = &ran
	}
	if err := l.Store.SaveObservabilityState(ctx, jobs); err != nil {
		return err
	}
	if err := l.Store.SaveObservabilityState(ctx, summaries); err != nil {
		return err
	}
	if l.Observe {
		RollupLastSuccess.WithLabelValues(StateJobs).Set(float64(ran.Unix()))
	}
	return nil
}

func (l *Loop) actorGroups(ctx context.Context, facts []models.RollupJobFact, day, end time.Time) error {
	ids := map[string]bool{}
	for _, f := range facts {
		if f.ActorPrincipalID != nil {
			ids[*f.ActorPrincipalID] = true
		}
	}
	list := make([]string, 0, len(ids))
	for id := range ids {
		list = append(list, id)
	}
	changes, err := l.Store.ListGroupMembershipChanges(ctx, list, end)
	if err != nil {
		return err
	}
	return l.Store.ReplaceActorGroupRollups(ctx, day, end, ComputeActorGroupRollups(facts, NewMembershipIndex(changes)))
}

func (l *Loop) eventsPass(ctx context.Context, now, upper time.Time, reconcile bool, cfg Config) error {
	events, backfill, err := l.state(ctx, StateEvents)
	if err != nil {
		return err
	}
	if !upper.After(events.Watermark) && !reconcile {
		return nil
	}
	changed, err := l.Store.ListChangedEventBuckets(ctx, events.Watermark, upper)
	if err != nil {
		return err
	}
	if reconcile && !backfill {
		changed = append(changed, reconcileDays(upper, cfg.ReconcileSpan)...)
	}
	hourlyFrom := BucketStart(now.Add(-cfg.HourlyRetention), Daily)
	for _, day := range days(changed) {
		end := day.Add(24 * time.Hour)
		facts, err := l.Store.ListRollupEventFacts(ctx, day, end)
		if err != nil {
			return err
		}
		if !day.Before(hourlyFrom) {
			if err := l.Store.ReplaceEvalRollups(ctx, string(Hourly), day, end, ComputeEvalRollups(facts, Hourly)); err != nil {
				return err
			}
		}
		if err := l.Store.ReplaceEvalRollups(ctx, string(Daily), day, end, ComputeEvalRollups(facts, Daily)); err != nil {
			return err
		}
		if l.Observe && !backfill {
			for i := range facts {
				if facts[i].FirstStatusAt.After(events.Watermark) && !facts[i].FirstStatusAt.After(upper) {
					ObserveEvent(&facts[i])
				}
			}
		}
	}
	ran := now
	events.Watermark, events.LastRunAt, events.LastError = upper, &ran, ""
	if backfill {
		events.BackfilledAt = &ran
	}
	if err := l.Store.SaveObservabilityState(ctx, events); err != nil {
		return err
	}
	if l.Observe {
		RollupLastSuccess.WithLabelValues(StateEvents).Set(float64(ran.Unix()))
	}
	return nil
}
