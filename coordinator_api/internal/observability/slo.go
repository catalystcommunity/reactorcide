package observability

import (
	"context"
	"fmt"
	"math/bits"
	"sort"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

// DefaultTargets are the SLO targets of an organization with no stored row
// (lane "").
var DefaultTargets = map[string]models.SLOTarget{
	models.SLIEvalLatency:      {SLI: models.SLIEvalLatency, Objective: 0.95, ThresholdSeconds: 120, WindowDays: 30},
	models.SLIQueueWait:        {SLI: models.SLIQueueWait, Objective: 0.95, ThresholdSeconds: 300, WindowDays: 30},
	models.SLIInfraFailureRate: {SLI: models.SLIInfraFailureRate, Objective: 0.99, WindowDays: 30},
	models.SLIAvailability:     {SLI: models.SLIAvailability, Objective: 0.995, WindowDays: 30},
}

// EffectiveTargets returns the stored targets of an organization and the
// built-in default of each SLI that has no stored lane "" row.
func EffectiveTargets(orgID string, stored []models.SLOTarget) []models.SLOTarget {
	out := append([]models.SLOTarget{}, stored...)
	have := map[string]bool{}
	for _, t := range stored {
		if t.Lane == "" {
			have[t.SLI] = true
		}
	}
	for _, sli := range models.SLIs {
		if !have[sli] {
			t := DefaultTargets[sli]
			t.OrgID, t.Builtin = orgID, true
			out = append(out, t)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].SLI != out[j].SLI {
			return sliOrder(out[i].SLI) < sliOrder(out[j].SLI)
		}
		return out[i].Lane < out[j].Lane
	})
	return out
}

func sliOrder(sli string) int {
	for i, s := range models.SLIs {
		if s == sli {
			return i
		}
	}
	return len(models.SLIs)
}

// TargetFor returns the target of a lane: the lane row, else the lane ""
// row of the SLI.
func TargetFor(targets []models.SLOTarget, sli, lane string) models.SLOTarget {
	var fallback *models.SLOTarget
	for i := range targets {
		if targets[i].SLI != sli {
			continue
		}
		if targets[i].Lane == lane && models.SLIHasLanes(sli) {
			return targets[i]
		}
		if targets[i].Lane == "" {
			fallback = &targets[i]
		}
	}
	if fallback != nil {
		return *fallback
	}
	return DefaultTargets[sli]
}

// ValidateSLOTarget checks a target before a save.
func ValidateSLOTarget(t *models.SLOTarget) error {
	if _, ok := DefaultTargets[t.SLI]; !ok {
		return fmt.Errorf("sli must be one of %v", models.SLIs)
	}
	if t.Lane != "" && !models.SLIHasLanes(t.SLI) {
		return fmt.Errorf("only queue_wait and infra_failure_rate have lane targets")
	}
	if t.Objective <= 0 || t.Objective >= 1 {
		return fmt.Errorf("objective must be in (0, 1), for example 0.99")
	}
	if (t.SLI == models.SLIEvalLatency || t.SLI == models.SLIQueueWait) && t.ThresholdSeconds <= 0 {
		return fmt.Errorf("threshold_seconds must be above 0 for %s", t.SLI)
	}
	if t.WindowDays == 0 {
		t.WindowDays = 30
	}
	if t.WindowDays < 1 || t.WindowDays > 90 {
		return fmt.Errorf("window_days must be 1-90")
	}
	return nil
}

// Budget states.
const (
	BudgetOK        = "ok"
	BudgetWarning   = "warning"
	BudgetExhausted = "exhausted"
	BudgetNoData    = "no_data"
)

// ErrorBudget returns the fraction of the error budget that remains: 1 -
// bad / ((1 - objective) x total). It is negative when the budget is
// overspent. No data gives 1 and BudgetNoData. Less than 25 percent left is
// BudgetWarning.
func ErrorBudget(objective, good, total float64) (float64, string) {
	if total <= 0 {
		return 1, BudgetNoData
	}
	allowed := (1 - objective) * total
	bad := total - good
	if bad < 0 {
		bad = 0
	}
	var remaining float64
	if allowed <= 0 {
		remaining = 1
		if bad > 0 {
			remaining = -1
		}
	} else {
		remaining = 1 - bad/allowed
	}
	switch {
	case remaining <= 0:
		return remaining, BudgetExhausted
	case remaining < 0.25:
		return remaining, BudgetWarning
	}
	return remaining, BudgetOK
}

// SLOStatus is the state of one SLI (and lane) over its window.
type SLOStatus struct {
	SLI              string    `json:"sli"`
	Lane             string    `json:"lane"`
	Objective        float64   `json:"objective"`
	ThresholdSeconds float64   `json:"threshold_seconds"`
	WindowDays       int       `json:"window_days"`
	From             time.Time `json:"from"`
	To               time.Time `json:"to"`
	Total            float64   `json:"total"`
	Good             float64   `json:"good"`
	// Compliance is good / total.
	Compliance *float64 `json:"compliance,omitempty"`
	// P95Seconds is the estimated 95th percentile (latency SLIs only).
	P95Seconds           *float64 `json:"p95_seconds,omitempty"`
	ErrorBudgetRemaining float64  `json:"error_budget_remaining"`
	BudgetState          string   `json:"budget_state"`
	Measures             string   `json:"measures"`
}

func newStatus(t models.SLOTarget, lane string, from, to time.Time, good, total float64, measures string) SLOStatus {
	s := SLOStatus{SLI: t.SLI, Lane: lane, Objective: t.Objective, ThresholdSeconds: t.ThresholdSeconds, WindowDays: t.WindowDays,
		From: from, To: to, Total: total, Good: good, Measures: measures}
	if total > 0 {
		c := good / total
		s.Compliance = &c
	}
	s.ErrorBudgetRemaining, s.BudgetState = ErrorBudget(t.Objective, good, total)
	return s
}

func windowOf(t models.SLOTarget, now time.Time) (time.Time, time.Time) {
	days := t.WindowDays
	if days <= 0 {
		days = 30
	}
	return DayRange(now, days)
}

// Availability returns the up minutes and the measured minutes in [from,
// to): the measure starts at the first heartbeat ever seen in the window, so
// the time before an install does not count as down. The current minute is
// not measured.
func Availability(beats []models.HeartbeatHour, from, to, now time.Time) (up, total float64) {
	end := now.UTC().Truncate(time.Minute)
	if to.Before(end) {
		end = to
	}
	var first time.Time
	for _, b := range beats {
		if b.UpMask == 0 {
			continue
		}
		minute := b.HourStart.Add(time.Duration(bits.TrailingZeros64(uint64(b.UpMask))) * time.Minute)
		if first.IsZero() || minute.Before(first) {
			first = minute
		}
	}
	if first.IsZero() {
		return 0, 0
	}
	if first.Before(from) {
		first = from
	}
	for _, b := range beats {
		mask := uint64(b.UpMask)
		for m := 0; m < 60; m++ {
			t := b.HourStart.Add(time.Duration(m) * time.Minute)
			if mask&(1<<uint(m)) != 0 && !t.Before(first) && t.Before(end) {
				up++
			}
		}
	}
	total = end.Sub(first).Minutes()
	if up > total {
		total = up
	}
	return up, total
}

// SLOStatuses computes every SLO of an organization. scope limits the job
// and eval rollups (visibility); its OrgID, From, and To are set here.
// Availability is global: it does not depend on the scope.
func SLOStatuses(ctx context.Context, st store.ObservabilityStore, scope store.RollupFilter, now time.Time) ([]SLOStatus, error) {
	stored, err := st.ListSLOTargets(ctx, scope.OrgID)
	if err != nil {
		return nil, err
	}
	targets := EffectiveTargets(scope.OrgID, stored)
	var out []SLOStatus

	eval := TargetFor(targets, models.SLIEvalLatency, "")
	from, to := windowOf(eval, now)
	f := scope
	f.From, f.To = from, to
	evalRows, err := st.ListEvalRollups(ctx, string(Daily), f)
	if err != nil {
		return nil, err
	}
	ea := AggregateEvalRows(evalRows)
	s := newStatus(eval, "", from, to, ea.Latency.CountAtMost(eval.ThresholdSeconds), float64(ea.Latency.Total()),
		"CI events with a first commit status; good is a first status within the threshold")
	s.P95Seconds = ptr(ea.Latency.Quantile(0.95))
	out = append(out, s)

	for _, sli := range []string{models.SLIQueueWait, models.SLIInfraFailureRate} {
		// Read the longest window; each lane uses its own window below.
		f := scope
		f.From, f.To = DayRange(now, 90)
		rows, err := st.ListJobRollups(ctx, string(Daily), f)
		if err != nil {
			return nil, err
		}
		_, parts := GroupBy(rows, func(r *models.JobRollup) string { return r.Lane })
		lanes := make([]string, 0, len(parts))
		for lane := range parts {
			lanes = append(lanes, lane)
		}
		sort.Strings(lanes)
		if len(lanes) == 0 {
			lanes = []string{""}
		}
		for _, lane := range lanes {
			t := TargetFor(targets, sli, lane)
			from, to := windowOf(t, now)
			a := NewAggregate()
			for i := range parts[lane] {
				if !parts[lane][i].BucketStart.Before(from) {
					a.Add(&parts[lane][i])
				}
			}
			if sli == models.SLIQueueWait {
				st := newStatus(t, lane, from, to, a.QueueWait.CountAtMost(t.ThresholdSeconds), float64(a.QueueWait.Total()),
					"jobs that a worker took; good is a queue wait within the threshold")
				st.P95Seconds = ptr(a.QueueWaitQuantile(0.95))
				out = append(out, st)
				continue
			}
			total := float64(a.Runs - a.Cancelled)
			out = append(out, newStatus(t, lane, from, to, total-float64(a.InfraFailures), total,
				"finished jobs that were not cancelled; bad is an infra.* failure class"))
		}
	}

	avail := TargetFor(targets, models.SLIAvailability, "")
	from, to = windowOf(avail, now)
	beats, err := st.ListHeartbeats(ctx, from, to)
	if err != nil {
		return nil, err
	}
	up, total := Availability(beats, from, to, now)
	out = append(out, newStatus(avail, "", from, to, up, total,
		"minutes in which a coordinator held the rollup lease and wrote to the database"))
	return out, nil
}
