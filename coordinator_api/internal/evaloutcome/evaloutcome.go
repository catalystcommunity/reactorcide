// Package evaloutcome closes the CI event of an eval job that has finished.
//
// An eval job can finish on three paths: the worker reports a result
// (workerapi/result.go), the lost-job reaper fails it (workerapi/reaper.go),
// or a cancel moves a queued eval straight to "cancelled"
// (jobcontrol). Every path calls Finish BEFORE it publishes the event's
// "Reactorcide Checks" status, so the status reads the final event state.
package evaloutcome

import (
	"context"
	"fmt"

	"github.com/catalystcommunity/app-utils-go/logging"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

// Finish marks the eval job's event evaluated (finalStatus "completed") or
// failed (anything else). For a failed scheduled eval it also records the
// failure on the schedule (last_error) and on the fire that started it. A
// later fire that finds its workflow clears last_error again
// (worker.recordScheduleOutcome).
//
// Jobs without an event, workflow node jobs and non-eval jobs are ignored.
// st is the coordinator store; capabilities are found by type assertion so
// stores without CI events keep working.
func Finish(ctx context.Context, st any, job *models.Job, finalStatus string) {
	if job == nil || job.EventID == nil || *job.EventID == "" || job.WorkflowNodeID != nil {
		return
	}
	if kind, _ := job.JobEnvVars["REACTORCIDE_JOB_KIND"].(string); kind != "eval" {
		return
	}
	events, ok := st.(store.CIEventStore)
	if !ok {
		return
	}
	eventID := *job.EventID
	status := models.CIEventStatusFailed
	if finalStatus == "completed" {
		status = models.CIEventStatusEvaluated
	}
	if err := events.SetCIEventStatus(ctx, eventID, status); err != nil {
		logging.Log.WithError(err).WithField("event_id", eventID).Warn("Failed to close CI event of eval job")
	}
	if status != models.CIEventStatusFailed {
		return
	}
	event, err := events.GetCIEvent(ctx, eventID)
	if err != nil || event.ScheduleID == nil || *event.ScheduleID == "" {
		return
	}
	message := FailureMessage(job.JobID, finalStatus)
	if maintenance, ok := st.(store.ScheduleMaintenanceStore); ok {
		if err := maintenance.SetScheduleLastError(ctx, *event.ScheduleID, message); err != nil {
			logging.Log.WithError(err).WithField("schedule_id", *event.ScheduleID).Warn("Failed to record failed scheduled eval on schedule")
		}
	}
	if fires, ok := st.(store.ScheduleEvalOutcomeStore); ok {
		if err := fires.FailScheduleFireForEvent(ctx, eventID, message); err != nil {
			logging.Log.WithError(err).WithField("event_id", eventID).Warn("Failed to record failed scheduled eval on its fire")
		}
	}
}

// FailureMessage is the schedule error text for an eval job that did not
// complete: "eval job <id> failed", "... timed out", "... was cancelled".
func FailureMessage(jobID, finalStatus string) string {
	switch finalStatus {
	case "timeout":
		return fmt.Sprintf("eval job %s timed out", jobID)
	case "cancelled", "cancelling":
		return fmt.Sprintf("eval job %s was cancelled", jobID)
	case "", "failed":
		return fmt.Sprintf("eval job %s failed", jobID)
	default:
		return fmt.Sprintf("eval job %s ended with status %s", jobID, finalStatus)
	}
}
