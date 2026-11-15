package schedule

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strings"
	"time"
	"unicode"

	"github.com/catalystcommunity/app-utils-go/logging"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

// LeaseName is the coordinator lease the scheduler loop runs under.
const LeaseName = "scheduler"

// Firer starts the work of one fire: it resolves the branch head, creates a
// `schedule` ci_event, and starts an eval restricted to the schedule's
// workflow_ref. It returns the event ID. An error is recorded on the fire row
// and as the schedule's last_error; a missing workflow on the branch is such
// an error. A Firer never deletes a schedule.
type Firer interface {
	Fire(ctx context.Context, schedule models.CISchedule, fireTime time.Time) (eventID string, err error)
}

// ErrFirerNotWired is what UnwiredFirer returns.
var ErrFirerNotWired = errors.New("event creation not wired yet")

// UnwiredFirer is the placeholder until schedule events exist. Every fire is
// recorded as failed with ErrFirerNotWired, so a fire is visible but starts
// no work.
type UnwiredFirer struct{}

func (UnwiredFirer) Fire(context.Context, models.CISchedule, time.Time) (string, error) {
	return "", ErrFirerNotWired
}

// Default loop timings.
const (
	DefaultInterval = 15 * time.Second
	DefaultBatch    = 50
	// fireTimeout bounds one Fire call, so one stuck VCS call does not stop
	// the loop.
	fireTimeout = 2 * time.Minute
	// StaleFireAfter is how long a fire may stay "firing" before the
	// scheduler marks it failed. A fire takes at most fireTimeout, so a row
	// this old belongs to a coordinator that stopped between claim and
	// result. The fire is not retried: exactly-once wins over at-least-once.
	StaleFireAfter = 10 * time.Minute
)

// StaleFireMessage is the error recorded on a fire the sweep fails.
const StaleFireMessage = "coordinator stopped during fire"

// Scheduler fires due schedules.
type Scheduler struct {
	Store    store.ScheduleStore
	Firer    Firer
	Interval time.Duration
	Batch    int
	// Now is the clock; nil means time.Now.
	Now func() time.Time
}

func (s *Scheduler) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

func (s *Scheduler) firer() Firer {
	if s.Firer == nil {
		return UnwiredFirer{}
	}
	return s.Firer
}

// plan adapts PlanFire to the store's callback.
func plan(schedule models.CISchedule, now time.Time) (time.Time, time.Time, error) {
	dueAt := now
	if schedule.NextFireAt != nil {
		dueAt = *schedule.NextFireAt
	}
	return PlanFire(schedule.Cron, schedule.Timezone, dueAt, now)
}

// RunOnce claims every due schedule (in batches) and fires each claimed one.
// It returns the number of fires it started.
func (s *Scheduler) RunOnce(ctx context.Context) (int, error) {
	batch := s.Batch
	if batch <= 0 {
		batch = DefaultBatch
	}
	if maintenance, ok := s.Store.(store.ScheduleMaintenanceStore); ok {
		if failed, err := maintenance.FailStaleScheduleFires(ctx, StaleFireAfter, StaleFireMessage); err != nil {
			logging.Log.WithError(err).Warn("failed to sweep stale schedule fires")
		} else if failed > 0 {
			logging.Log.WithField("count", failed).Warn("marked stale schedule fires failed")
		}
	}
	total := 0
	for {
		claimed, err := s.Store.ClaimDueScheduleFires(ctx, s.now(), batch, plan)
		if err != nil {
			return total, err
		}
		for _, fire := range claimed {
			s.fire(ctx, fire)
			total++
		}
		// A full batch may mean more are due; a short one means done.
		if len(claimed) < batch || ctx.Err() != nil {
			return total, ctx.Err()
		}
	}
}

func (s *Scheduler) fire(ctx context.Context, fire store.ClaimedScheduleFire) {
	log := logging.Log.WithField("schedule_id", fire.Schedule.ScheduleID).WithField("fire_time", fire.FireTime.Format(time.RFC3339))
	fireCtx, cancel := context.WithTimeout(ctx, fireTimeout)
	eventID, fireErr := s.firer().Fire(fireCtx, fire.Schedule, fire.FireTime)
	cancel()
	var eventRef *string
	message := ""
	if fireErr != nil {
		message = fireErr.Error()
		log.WithError(fireErr).Warn("schedule fire failed")
	} else if eventID != "" {
		eventRef = &eventID
		log.WithField("event_id", eventID).Info("schedule fired")
	}
	// Record with a fresh context: the fire row must be completed even when
	// the leader context ends right after Fire returned.
	recordCtx, recordCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer recordCancel()
	if err := s.Store.RecordScheduleFireResult(recordCtx, fire.Schedule.ScheduleID, fire.FireTime, eventRef, message); err != nil {
		log.WithError(err).Error("failed to record schedule fire result")
	}
}

// Run calls RunOnce at once and then every Interval until ctx is done. It is
// the function to run under the scheduler lease.
func (s *Scheduler) Run(ctx context.Context) {
	interval := s.Interval
	if interval <= 0 {
		interval = DefaultInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if _, err := s.RunOnce(ctx); err != nil && ctx.Err() == nil {
			logging.Log.WithError(err).Warn("scheduler pass failed")
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// Field limits for schedule input.
const (
	maxNameLen        = 128
	maxBranchLen      = 255
	maxWorkflowRefLen = 512
)

// WorkflowFileDir is where workflow definitions live in a repository.
const WorkflowFileDir = ".reactorcide/workflows/"

// ValidateDefinition checks the parts of a schedule that are not cron:
// name, branch and workflow_ref. A workflow_ref with a "/" is a file path and
// must be a .yaml or .yml file directly under .reactorcide/workflows/;
// anything else is a workflow ID.
func ValidateDefinition(name, branch, workflowRef string) error {
	if err := validateToken("name", name, maxNameLen, true); err != nil {
		return err
	}
	if err := validateToken("branch", branch, maxBranchLen, false); err != nil {
		return err
	}
	if strings.Contains(branch, "..") || strings.HasPrefix(branch, "/") || strings.HasSuffix(branch, "/") {
		return fmt.Errorf("branch %q is not a valid branch name", branch)
	}
	if err := validateToken("workflow_ref", workflowRef, maxWorkflowRefLen, false); err != nil {
		return err
	}
	if strings.Contains(workflowRef, "/") {
		clean := path.Clean(workflowRef)
		ext := path.Ext(clean)
		if clean != workflowRef || !strings.HasPrefix(clean, WorkflowFileDir) ||
			strings.Contains(strings.TrimPrefix(clean, WorkflowFileDir), "/") ||
			(ext != ".yaml" && ext != ".yml") {
			return fmt.Errorf("workflow_ref %q must be a workflow ID or a .yaml/.yml file directly under %s", workflowRef, WorkflowFileDir)
		}
	}
	return nil
}

func validateToken(field, value string, max int, allowSpaces bool) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("%s is required", field)
	}
	if len(value) > max {
		return fmt.Errorf("%s is longer than %d characters", field, max)
	}
	for _, r := range value {
		if unicode.IsControl(r) || (!allowSpaces && unicode.IsSpace(r)) {
			return fmt.Errorf("%s contains a character that is not allowed", field)
		}
	}
	return nil
}
