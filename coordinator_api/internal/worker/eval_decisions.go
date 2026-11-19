package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/catalystcommunity/app-utils-go/logging"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/objects"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/vcs"
)

// Changed-file limits. The full list goes to object storage; the event row
// keeps a short preview for display.
const (
	changedFilesPreviewLimit = 50
	maxChangedFilesAccepted  = 20000
)

// evalWorkflowDecision is runnerlib's decision for one workflow file.
type evalWorkflowDecision struct {
	ID         string                  `json:"id"`
	Name       string                  `json:"name"`
	SourceFile string                  `json:"source_file,omitempty"`
	Decision   string                  `json:"decision"`
	Reasons    []models.DecisionReason `json:"reasons,omitempty"`
}

// SetObjectStore lets the processor keep an event's full changed-file list.
// Without one, only the preview on the event row is kept.
func (tp *TriggerProcessor) SetObjectStore(objectStore objects.ObjectStore) {
	tp.objectStore = objectStore
}

// ChangedFilesObjectKey is where an event's full changed-file list lives.
func ChangedFilesObjectKey(eventID string) string {
	return fmt.Sprintf("reactorcide/events/v1/%s/changed-files.json", eventID)
}

// policyNote turns one policy outcome into a structured reason.
func policyNote(origin, profile, workerClass, ruleID, revision string, reasons ...string) models.DecisionReason {
	return models.DecisionReason{
		Kind: "policy", CIOrigin: origin, Profile: profile, WorkerClass: workerClass,
		RuleID: ruleID, PolicyRevision: revision, Reasons: nonEmptyStrings(reasons),
	}
}

func nonEmptyStrings(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			out = append(out, value)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// decisionsAreBaseView reports whether runnerlib's decisions describe the
// TRUSTED base definitions of a pull request: the eval read its CI from the
// base SHA or a separate CI repository, not from the head or the merge
// commit. A push has no such view (its CI is the pushed commit, or
// push.before, which the pusher wrote).
func decisionsAreBaseView(parentJob *models.Job) bool {
	if envString(parentJob, "REACTORCIDE_PR_NUMBER") == "" {
		return false
	}
	if envString(parentJob, "REACTORCIDE_EVENT_TYPE") == string(vcs.EventPullRequestMerged) {
		return false
	}
	return parentJob.CISHA != "" && parentJob.CISHA != stringValue(parentJob.SourceRef)
}

// recordEvalDecisions stores one decision per workflow the eval considered:
// runnerlib's matches and skips, the coordinator's policy outcome for each
// workflow that runs, and admission refusals. It is best-effort: a failure
// is logged and never fails the trigger request, because the workflows are
// already created.
func (tp *TriggerProcessor) recordEvalDecisions(ctx context.Context, parentJob *models.Job, tf *triggersFile,
	selected []triggerWorkflowSpec, notes map[string]models.DecisionReason, outcomes []TriggerWorkflowOutcome,
	carried map[string]carriedWorkflow) {
	eventID := stringValue(parentJob.EventID)
	if eventID == "" {
		return
	}
	events, ok := tp.store.(store.CIEventStore)
	if !ok {
		return
	}
	baseView := decisionsAreBaseView(parentJob)
	byID := map[string]*models.CIEventWorkflowDecision{}
	order := []string{}
	put := func(decision models.CIEventWorkflowDecision) *models.CIEventWorkflowDecision {
		if existing, ok := byID[decision.WorkflowSecurityID]; ok {
			return existing
		}
		value := decision
		byID[decision.WorkflowSecurityID] = &value
		order = append(order, decision.WorkflowSecurityID)
		return &value
	}
	for _, item := range tf.Decisions {
		id := strings.TrimSpace(item.ID)
		if id == "" {
			// An invalid file may have no ID; key it by its file so it still shows.
			id = "file:" + strings.TrimSpace(item.SourceFile)
		}
		decision := item.Decision
		switch decision {
		case models.WorkflowDecisionRun, models.WorkflowDecisionSkipped, models.WorkflowDecisionInvalid:
		default:
			decision = models.WorkflowDecisionSkipped
		}
		entry := put(models.CIEventWorkflowDecision{
			EventID: eventID, WorkflowSecurityID: id, WorkflowName: item.Name, SourceFile: item.SourceFile,
			Decision: decision, Reasons: append([]models.DecisionReason(nil), item.Reasons...),
		})
		if baseView && decision == models.WorkflowDecisionRun && !strings.HasPrefix(id, "file:") {
			entry.Reasons = append(entry.Reasons, models.DecisionReason{Kind: models.DecisionReasonBaseDefinition,
				Detail: "the trusted base definition matched this event"})
		}
	}
	selectedIDs := map[string]bool{}
	for _, spec := range selected {
		id := workflowSecurityIDOf(spec)
		selectedIDs[id] = true
		entry := put(models.CIEventWorkflowDecision{
			EventID: eventID, WorkflowSecurityID: id, WorkflowName: spec.Name, SourceFile: spec.SourceFile,
		})
		// The coordinator's selection is the authority: a head workflow can run
		// even when the base definition did not match the event.
		entry.Decision = models.WorkflowDecisionRun
		if entry.WorkflowName == "" {
			entry.WorkflowName = spec.Name
		}
		if note, ok := notes[id]; ok {
			entry.Reasons = append(entry.Reasons, note)
		}
		if note, ok := notes[pathAcceptanceNotePrefix+id]; ok {
			entry.Reasons = append(entry.Reasons, note)
		}
	}
	for key, note := range notes {
		// A workflow that path acceptance skipped (path_acceptance.go).
		id, ok := strings.CutPrefix(key, pathAcceptanceNotePrefix)
		if !ok || selectedIDs[id] {
			continue
		}
		entry := put(models.CIEventWorkflowDecision{EventID: eventID, WorkflowSecurityID: id})
		entry.Decision = models.WorkflowDecisionSkipped
		entry.Reasons = append(entry.Reasons, note)
	}
	for key, note := range notes {
		// An advisory workflow that the shed level skipped (advisory_shed.go).
		id, ok := strings.CutPrefix(key, advisorySkipNotePrefix)
		if !ok || selectedIDs[id] {
			continue
		}
		entry := put(models.CIEventWorkflowDecision{EventID: eventID, WorkflowSecurityID: id})
		entry.Decision = models.WorkflowDecisionSkipped
		entry.Reasons = append(entry.Reasons, note)
	}
	for id, carry := range carried {
		entry := put(models.CIEventWorkflowDecision{EventID: eventID, WorkflowSecurityID: id, WorkflowName: carry.Name})
		workflowID := carry.WorkflowID
		entry.Decision = models.WorkflowDecisionCarried
		entry.WorkflowID = &workflowID
		if note, ok := notes[id]; ok {
			entry.Reasons = append(entry.Reasons, note)
		}
		entry.Reasons = append(entry.Reasons, carry.Reason)
		selectedIDs[id] = true
	}
	for _, id := range order {
		entry := byID[id]
		if entry.Decision == models.WorkflowDecisionRun && !selectedIDs[id] && (len(selected) > 0 || len(carried) > 0) {
			entry.Decision = models.WorkflowDecisionSkipped
			entry.Reasons = append(entry.Reasons, models.DecisionReason{Kind: "admission", Detail: "not selected for this event"})
		}
	}
	for _, outcome := range outcomes {
		id := outcome.WorkflowSecurityID
		entry, ok := byID[id]
		if !ok {
			entry = put(models.CIEventWorkflowDecision{EventID: eventID, WorkflowSecurityID: id, WorkflowName: outcome.Name,
				Decision: models.WorkflowDecisionRun})
		}
		if outcome.WorkflowID != "" {
			workflowID := outcome.WorkflowID
			entry.WorkflowID = &workflowID
		}
		if outcome.Disposition != "" && outcome.Disposition != "accepted" {
			entry.Decision = models.WorkflowDecisionNotAdmitted
			entry.Reasons = append(entry.Reasons, models.DecisionReason{Kind: "admission", Value: outcome.Disposition, Detail: outcome.Reason})
		}
	}
	rows := make([]models.CIEventWorkflowDecision, 0, len(order))
	refClassReason := tp.refClassDecisionReason(ctx, events, eventID, parentJob)
	for _, id := range order {
		row := *byID[id]
		row.Reasons = append(row.Reasons, refClassReason)
		rows = append(rows, row)
	}
	if err := events.UpsertCIEventWorkflowDecisions(ctx, rows); err != nil {
		logging.Log.WithError(err).WithField("event_id", eventID).Warn("Failed to store eval workflow decisions")
	}
	tp.recordScheduleOutcome(ctx, events, eventID, rows)
	tp.recordChangedFiles(ctx, events, eventID, tf)
	if err := events.SetCIEventStatus(ctx, eventID, models.CIEventStatusEvaluated); err != nil {
		logging.Log.WithError(err).WithField("event_id", eventID).Warn("Failed to mark CI event evaluated")
	}
	// Not-admitted decisions can fail the event's checks before any
	// workflow status changes, so recompute now.
	tp.publishEventChecks(ctx, eventID)
}

// eventChecksPublisher is the part of the VCS status updater that posts a CI
// event's "Reactorcide Checks" status.
type eventChecksPublisher interface {
	PublishEventChecks(ctx context.Context, eventID string) error
}

// publishEventChecks recomputes the event's aggregate status. Best effort.
func (tp *TriggerProcessor) publishEventChecks(ctx context.Context, eventID string) {
	publisher, ok := tp.statusUpdater.(eventChecksPublisher)
	if !ok || eventID == "" {
		return
	}
	if err := publisher.PublishEventChecks(ctx, eventID); err != nil {
		logging.Log.WithError(err).WithField("event_id", eventID).Warn("Failed to update event checks status")
	}
}

// recordChangedFiles keeps the full changed-file list in object storage and
// a capped preview on the event row.
func (tp *TriggerProcessor) recordChangedFiles(ctx context.Context, events store.CIEventStore, eventID string, tf *triggersFile) {
	if tf.ChangedFiles == nil {
		return
	}
	files := append([]string(nil), tf.ChangedFiles...)
	sort.Strings(files)
	total := tf.ChangedFilesTotal
	if total < len(files) {
		total = len(files)
	}
	summary := models.CIEventSummary{ChangedFilesCount: total}
	if event, err := events.GetCIEvent(ctx, eventID); err == nil {
		// Keep the facts the webhook stored (action, refs).
		summary.Action, summary.HeadRef, summary.BaseRef = event.Summary.Action, event.Summary.HeadRef, event.Summary.BaseRef
	}
	summary.ChangedFilesPreview = files
	if len(files) > changedFilesPreviewLimit {
		summary.ChangedFilesPreview = files[:changedFilesPreviewLimit]
	}
	if tp.objectStore != nil && len(files) > 0 {
		encoded, err := json.Marshal(map[string]any{"total": total, "files": files})
		if err == nil {
			key := ChangedFilesObjectKey(eventID)
			if putErr := tp.objectStore.Put(ctx, key, bytes.NewReader(encoded), "application/json"); putErr == nil {
				summary.ChangedFilesKey = key
			} else {
				logging.Log.WithError(putErr).WithField("event_id", eventID).Warn("Failed to store changed-file list")
			}
		}
	}
	if err := events.SetCIEventSummary(ctx, eventID, summary); err != nil {
		logging.Log.WithError(err).WithField("event_id", eventID).Warn("Failed to store CI event summary")
	}
}

func workflowSecurityIDOf(spec triggerWorkflowSpec) string {
	if id := strings.TrimSpace(spec.ID); id != "" {
		return id
	}
	return strings.TrimSpace(spec.Name)
}
