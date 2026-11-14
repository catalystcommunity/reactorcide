// Package schedule runs coordinator-managed schedules.
//
// A schedule points at a workflow definition in the repository on a branch
// and fires it at cron times. One replica runs the scheduler loop, under the
// "scheduler" lease (internal/lease). The unique (schedule_id, fire_time) fire
// row makes each fire exactly-once even if two replicas overlap.
package schedule

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/robfig/cron/v3"
)

// parser accepts the standard five fields (minute hour day-of-month month
// day-of-week) and descriptors such as @daily and @every 1h. Seconds are not
// supported: a CI schedule needs no sub-minute precision.
var parser = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor)

// maxCatchUpSteps bounds the walk to the newest missed fire time. An
// every-minute schedule needs about 525,000 steps for a year of downtime.
const maxCatchUpSteps = 1_000_000

// ParseSpec parses a cron expression in a timezone. The timezone is a
// separate field, so a CRON_TZ= or TZ= prefix in the expression is refused.
func ParseSpec(expression, timezone string) (cron.Schedule, *time.Location, error) {
	expression = strings.TrimSpace(expression)
	if expression == "" {
		return nil, nil, errors.New("cron expression is required")
	}
	if strings.Contains(strings.ToUpper(expression), "TZ=") {
		return nil, nil, errors.New("set the timezone in the timezone field, not in the cron expression")
	}
	if timezone == "" {
		timezone = "UTC"
	}
	loc, err := time.LoadLocation(timezone)
	if err != nil {
		return nil, nil, fmt.Errorf("unknown timezone %q", timezone)
	}
	spec, err := parser.Parse(expression)
	if err != nil {
		return nil, nil, fmt.Errorf("invalid cron expression: %v", err)
	}
	return spec, loc, nil
}

// Validate reports whether a cron expression and timezone are usable.
func Validate(expression, timezone string) error {
	_, _, err := ParseSpec(expression, timezone)
	return err
}

// NextFireAfter returns the first fire time strictly after `after`, in UTC.
func NextFireAfter(expression, timezone string, after time.Time) (time.Time, error) {
	spec, loc, err := ParseSpec(expression, timezone)
	if err != nil {
		return time.Time{}, err
	}
	next := spec.Next(after.In(loc))
	if next.IsZero() {
		return time.Time{}, errors.New("cron expression never fires")
	}
	return next.UTC(), nil
}

// PlanFire decides one due fire. dueAt is the stored next fire time (at or
// before now). The fire time is the NEWEST cron time in [dueAt, now], so a
// scheduler that was down for a day fires once, not once per missed time.
// next is the first cron time strictly after now.
func PlanFire(expression, timezone string, dueAt, now time.Time) (fireTime, next time.Time, err error) {
	spec, loc, err := ParseSpec(expression, timezone)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	fire := dueAt.In(loc)
	for i := 0; i < maxCatchUpSteps; i++ {
		candidate := spec.Next(fire)
		if candidate.IsZero() || candidate.After(now) {
			break
		}
		fire = candidate
	}
	next = spec.Next(now.In(loc))
	if next.IsZero() {
		return time.Time{}, time.Time{}, errors.New("cron expression never fires")
	}
	return fire.UTC(), next.UTC(), nil
}
