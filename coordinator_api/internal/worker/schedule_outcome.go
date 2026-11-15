package worker

import (
	"context"
	"strings"

	"github.com/catalystcommunity/app-utils-go/logging"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

// recordScheduleOutcome copies a scheduled eval's result to its schedule. A
// workflow_ref that no longer resolves on the branch comes back as an
// `invalid` decision; the schedule then shows that error (it is never
// deleted). A scheduled eval that found its workflow clears an old error.
func (tp *TriggerProcessor) recordScheduleOutcome(ctx context.Context, events store.CIEventStore, eventID string,
	decisions []models.CIEventWorkflowDecision) {
	maintenance, ok := tp.store.(store.ScheduleMaintenanceStore)
	if !ok {
		return
	}
	event, err := events.GetCIEvent(ctx, eventID)
	if err != nil || event.ScheduleID == nil || *event.ScheduleID == "" {
		return
	}
	message := ""
	for _, decision := range decisions {
		if decision.Decision != models.WorkflowDecisionInvalid {
			continue
		}
		detail := ""
		for _, reason := range decision.Reasons {
			if reason.Detail != "" {
				detail = reason.Detail
				break
			}
		}
		if detail == "" {
			detail = "workflow definition is invalid"
		}
		ref := strings.TrimPrefix(decision.WorkflowSecurityID, "ref:")
		message = detail + ": " + ref
		break
	}
	if err := maintenance.SetScheduleLastError(ctx, *event.ScheduleID, message); err != nil {
		logging.Log.WithError(err).WithField("schedule_id", *event.ScheduleID).Warn("Failed to record scheduled eval outcome")
	}
}
