package worker

import (
	"context"

	"github.com/catalystcommunity/app-utils-go/logging"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/requiredset"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/shedding"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

// advisorySkipNotePrefix keys the decision reason of an advisory workflow
// that the shed level skipped, in the notes map of an eval. The prefix keeps
// it apart from the policy note of the same workflow.
const advisorySkipNotePrefix = "advisory-skip:"

// skipAdvisoryWhenShedding removes the advisory workflows of an eval when the
// shed level in force skips them (plan items A1 and D4). An advisory workflow
// never blocks a merge, so it is the first work to drop under overload. A
// required workflow always runs. The skipped workflows get a "skipped"
// decision with the shed reason (recordEvalDecisions).
func (tp *TriggerProcessor) skipAdvisoryWhenShedding(ctx context.Context, parentJob *models.Job, batches []triggerWorkflowSpec, notes map[string]models.DecisionReason) []triggerWorkflowSpec {
	if len(batches) == 0 || parentJob == nil || stringValue(parentJob.EventID) == "" || notes == nil {
		return batches
	}
	if !shedding.SkipAdvisory(ctx, tp.store, parentJob.OrgID) {
		return batches
	}
	events, ok := tp.store.(store.CIEventStore)
	if !ok {
		return batches
	}
	event, err := events.GetCIEvent(ctx, stringValue(parentJob.EventID))
	if err != nil {
		return batches
	}
	classifier := requiredset.ForEvent(ctx, tp.store, event)
	if !classifier.Set.Enabled {
		return batches
	}
	reason := shedding.AdvisorySkipReason(shedding.State(ctx, tp.store, parentJob.OrgID))
	kept := batches[:0:0]
	for _, batch := range batches {
		id := workflowSecurityIDOf(batch)
		if classifier.Classify(id).Blocking() {
			kept = append(kept, batch)
			continue
		}
		notes[advisorySkipNotePrefix+id] = models.DecisionReason{Kind: models.SkipReasonShed, Detail: reason}
		logging.Log.WithField("event_id", stringValue(parentJob.EventID)).WithField("workflow_security_id", id).
			Info("Skipping an advisory workflow at the shed level in force")
	}
	return kept
}
