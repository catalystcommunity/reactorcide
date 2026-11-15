package store

import (
	"context"
	"time"
)

// EventSettingsStore resolves the organization/project event settings
// (dynamic_jobs, duplicate_events) to the value in force. Like the other
// optional stores it is separate from Store; callers type-assert and keep the
// pre-setting behavior when a store does not implement it.
type EventSettingsStore interface {
	// EffectiveDynamicJobs returns "allow" or "deny" for a job in orgID and,
	// when set, projectID.
	EffectiveDynamicJobs(ctx context.Context, orgID string, projectID *string) (string, error)
	// EffectiveDuplicateEvents returns "run" or "skip" for a project.
	EffectiveDuplicateEvents(ctx context.Context, projectID string) (string, error)
}

// CIEventClaimStore guards the start of an event's evaluation, so two
// concurrent deliveries of one event cannot both create an eval job.
type CIEventClaimStore interface {
	// ClaimCIEventForEval moves an event that has no eval job from
	// "received" to "evaluating" and reports whether this call did it. A
	// claim with no eval job that is older than the stale window can be
	// taken again, so a coordinator that stopped between claim and job
	// creation does not block the event for good.
	ClaimCIEventForEval(ctx context.Context, eventID string) (bool, error)
	// ReleaseCIEventClaim gives a claim back when the eval job could not be
	// created, so a redelivery can evaluate the event.
	ReleaseCIEventClaim(ctx context.Context, eventID string) error
	// RelinkCIEventEvalJob points the event at a new eval job (a retry) and
	// marks it evaluating again.
	RelinkCIEventEvalJob(ctx context.Context, eventID, jobID string) error
}

// ScheduleMaintenanceStore holds the scheduler's housekeeping writes.
type ScheduleMaintenanceStore interface {
	// FailStaleScheduleFires marks fires still "firing" after olderThan as
	// failed with message, and copies message to their schedules. It returns
	// how many fires it changed.
	FailStaleScheduleFires(ctx context.Context, olderThan time.Duration, message string) (int64, error)
	// SetScheduleLastError records (or, with "", clears) a schedule's
	// last_error without touching anything else.
	SetScheduleLastError(ctx context.Context, scheduleID, message string) error
}

// ScheduleEvalOutcomeStore records the outcome of a scheduled eval job on the
// fire that started it.
type ScheduleEvalOutcomeStore interface {
	// FailScheduleFireForEvent marks the fire linked to eventID failed with
	// message. A fire that is no longer linked to the event is left alone.
	FailScheduleFireForEvent(ctx context.Context, eventID, message string) error
}
