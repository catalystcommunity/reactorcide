package jobcontrol

import (
	"context"

	"github.com/catalystcommunity/app-utils-go/logging"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

// relinkRetriedEvalEvent points a CI event at the retry of its eval job, so
// the event shows the eval that is running now and the result path marks
// the event by the right job. A retry of any other job of the event (a
// workflow node) leaves the event alone. Best-effort: the retry already
// exists, so a failure is logged and not returned.
func relinkRetriedEvalEvent(ctx context.Context, st store.Store, original, retried *models.Job) {
	if original == nil || retried == nil || original.EventID == nil || *original.EventID == "" {
		return
	}
	events, ok := st.(store.CIEventStore)
	if !ok {
		return
	}
	relinker, ok := st.(store.CIEventClaimStore)
	if !ok {
		return
	}
	event, err := events.GetCIEvent(ctx, *original.EventID)
	if err != nil {
		logging.Log.WithError(err).WithField("event_id", *original.EventID).Warn("Could not load CI event of retried job")
		return
	}
	if event.EvalJobID == nil || *event.EvalJobID != original.JobID {
		return
	}
	if err := relinker.RelinkCIEventEvalJob(ctx, event.EventID, retried.JobID); err != nil {
		logging.Log.WithError(err).WithField("event_id", event.EventID).Warn("Could not link CI event to retried eval job")
	}
}
