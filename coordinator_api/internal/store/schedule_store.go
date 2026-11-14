package store

import (
	"context"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

// LeaseStore holds named coordinator leases. At most one holder has a lease
// at a time. All times come from the database clock.
//
// Like ResourceSummaryStore it is separate from Store, so the full Store fakes
// need no new methods. Callers type-assert.
type LeaseStore interface {
	// TryAcquireLease takes the lease when it is free or expired, or renews
	// it when holder already has it. It reports whether holder has the lease
	// afterwards. One statement, so two replicas cannot both win.
	TryAcquireLease(ctx context.Context, name, holder string, ttl time.Duration) (bool, error)
	// ReleaseLease gives the lease up if holder has it. Releasing a lease
	// that another holder has is a no-op.
	ReleaseLease(ctx context.Context, name, holder string) error
}

// ScheduleFirePlan is what the scheduler decides for one due schedule: the
// fire time to record (the newest missed time, never a backlog) and the next
// fire time after now. An error records last_error and retries later.
type ScheduleFirePlan func(schedule models.CISchedule, now time.Time) (fireTime, next time.Time, err error)

// ClaimedScheduleFire is one fire row that ClaimDueScheduleFires inserted.
// The caller must fire it and then call RecordScheduleFireResult.
type ClaimedScheduleFire struct {
	Schedule models.CISchedule
	FireTime time.Time
}

// ScheduleStore holds coordinator-managed schedules and their fires.
type ScheduleStore interface {
	CreateSchedule(ctx context.Context, schedule *models.CISchedule) error
	GetSchedule(ctx context.Context, scheduleID string) (*models.CISchedule, error)
	ListSchedulesByProject(ctx context.Context, projectID string) ([]models.CISchedule, error)
	// UpdateSchedule writes every mutable field of schedule (name, branch,
	// workflow_ref, cron, timezone, enabled, next_fire_at, last_error).
	UpdateSchedule(ctx context.Context, schedule *models.CISchedule) error
	DeleteSchedule(ctx context.Context, scheduleID string) error
	// ListScheduleFires returns the newest fires of one schedule first.
	ListScheduleFires(ctx context.Context, scheduleID string, limit int) ([]models.CIScheduleFire, error)

	// ClaimDueScheduleFires locks up to limit enabled schedules whose
	// next_fire_at is at or before now (FOR UPDATE SKIP LOCKED, so replicas
	// do not wait on each other), asks plan for each, inserts the fire row
	// (ON CONFLICT DO NOTHING) and advances next_fire_at, in one
	// transaction. It returns only the fires this call inserted: a fire that
	// already exists is not returned, which is what makes a fire
	// exactly-once.
	ClaimDueScheduleFires(ctx context.Context, now time.Time, limit int, plan ScheduleFirePlan) ([]ClaimedScheduleFire, error)
	// RecordScheduleFireResult completes a fire row and copies the result to
	// the schedule (last_fire_at, last_event_id, last_error). An empty
	// errMessage means success.
	RecordScheduleFireResult(ctx context.Context, scheduleID string, fireTime time.Time, eventID *string, errMessage string) error
}
