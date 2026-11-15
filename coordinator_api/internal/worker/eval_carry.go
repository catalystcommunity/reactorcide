package worker

import (
	"context"
	"fmt"

	"github.com/catalystcommunity/app-utils-go/logging"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/vcs"
)

// carriedWorkflow is a workflow of an earlier event that a re-evaluation keeps
// instead of running it again.
type carriedWorkflow struct {
	WorkflowID string
	Name       string
	Reason     models.DecisionReason
}

// carryUnchangedWorkflows applies the re-evaluate rule to an evaluation that
// an approval started: a workflow whose decision (CI origin and profile) is
// the same as in the last evaluation of the same head does not run again. The
// event carries that earlier workflow, so "Reactorcide Checks" still counts
// its result. A rerun, a push, or a webhook event is not changed.
func (tp *TriggerProcessor) carryUnchangedWorkflows(ctx context.Context, parentJob *models.Job, batches []triggerWorkflowSpec,
	notes map[string]models.DecisionReason) ([]triggerWorkflowSpec, map[string]carriedWorkflow) {
	eventID := stringValue(parentJob.EventID)
	events, okEvents := tp.store.(store.CIEventStore)
	heads, okHeads := tp.store.(store.PRHeadStore)
	if eventID == "" || !okEvents || !okHeads || parentJob.ProjectID == nil {
		return batches, nil
	}
	event, err := events.GetCIEvent(ctx, eventID)
	if err != nil || event.Source != models.CIEventSourceApproval || event.PRNumber == nil {
		return batches, nil
	}
	metadata, err := vcs.MetadataFromJob(parentJob)
	if err != nil || metadata == nil {
		return batches, nil
	}
	prior, err := heads.ListCIEventsForPRHead(ctx, *parentJob.ProjectID, *event.PRNumber, event.CommitSHA)
	if err != nil {
		logging.Log.WithError(err).WithField("event_id", eventID).Warn("Could not list earlier events; every workflow runs again")
		return batches, nil
	}
	type earlier struct {
		workflowID, origin, profile, eventID string
	}
	previous := map[string]earlier{}
	for _, candidate := range prior {
		if candidate.EventID == eventID || candidate.Status != models.CIEventStatusEvaluated {
			continue
		}
		decisions, listErr := events.ListCIEventWorkflowDecisions(ctx, candidate.EventID)
		if listErr != nil {
			return batches, nil
		}
		for _, decision := range decisions {
			if decision.WorkflowID == nil || (decision.Decision != models.WorkflowDecisionRun && decision.Decision != models.WorkflowDecisionCarried) {
				continue
			}
			for _, reason := range decision.Reasons {
				if reason.Kind == "policy" {
					previous[decision.WorkflowSecurityID] = earlier{*decision.WorkflowID, reason.CIOrigin, reason.Profile, candidate.EventID}
				}
			}
		}
		// The newest evaluated event of the head is the one to compare with.
		break
	}
	if len(previous) == 0 {
		return batches, nil
	}
	kept := make([]triggerWorkflowSpec, 0, len(batches))
	carried := map[string]carriedWorkflow{}
	for _, batch := range batches {
		id := workflowSecurityIDOf(batch)
		note, hasNote := notes[id]
		before, ran := previous[id]
		if !hasNote || !ran || note.CIOrigin != before.origin || note.Profile != before.profile {
			kept = append(kept, batch)
			continue
		}
		carried[id] = carriedWorkflow{WorkflowID: before.workflowID, Name: batch.Name, Reason: models.DecisionReason{
			Kind: "approval", Value: "unchanged",
			Detail: fmt.Sprintf("the approval did not change this workflow's decision (%s CI, profile %s); the result of event %s counts", note.CIOrigin, note.Profile, before.eventID),
		}}
	}
	return kept, carried
}
