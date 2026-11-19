package handlers

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/audit"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/jobcontrol"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/shedding"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/tokencaps"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/vcs"
)

// draftSkipReason decides whether a pull request event of a draft runs (plan
// item D4). Only a provider event of an open draft can be skipped. A
// coordinator event (re-evaluation after an approval, rerun) is a person's
// explicit request and always runs. It returns nil when the event runs.
func (h *WebhookHandler) draftSkipReason(ctx context.Context, project *models.Project, event *vcs.WebhookEvent, state models.ShedState) *models.DecisionReason {
	pr := event.PullRequest
	if pr == nil || !pr.Draft || event.Coordinator != nil {
		return nil
	}
	if event.GenericEvent != vcs.EventPullRequestOpened && event.GenericEvent != vcs.EventPullRequestUpdated {
		return nil
	}
	settings, ok := h.store.(store.DraftShedStore)
	if !ok {
		return nil
	}
	draftPRs, err := settings.EffectiveDraftPRs(ctx, project.ProjectID)
	if err != nil {
		h.logger.WithError(err).WithField("project", project.Name).Warn("Could not read draft_prs; running the draft")
		return nil
	}
	skip, kind, reason := shedding.DraftDecision(draftPRs, state)
	if !skip {
		return nil
	}
	return &models.DecisionReason{Kind: kind, Value: draftPRs, Detail: reason}
}

// skipCIEvent records that the coordinator did not evaluate an event, with
// the reason, and posts "Reactorcide Checks" with that reason.
func (h *WebhookHandler) skipCIEvent(ctx context.Context, project *models.Project, ciEvent *models.CIEvent, status string, reason models.DecisionReason) {
	if ciEvent == nil {
		return
	}
	events, ok := h.store.(store.CIEventStore)
	if !ok {
		return
	}
	summary := ciEvent.Summary
	summary.SkipReasons = []models.DecisionReason{reason}
	if err := events.SetCIEventSummary(ctx, ciEvent.EventID, summary); err != nil {
		h.logger.WithError(err).WithField("event_id", ciEvent.EventID).Warn("Could not record the skip reason of the event")
	}
	if err := events.SetCIEventStatus(ctx, ciEvent.EventID, status); err != nil {
		h.logger.WithError(err).WithField("event_id", ciEvent.EventID).Warn("Could not mark the event as skipped")
		return
	}
	h.logger.WithField("event_id", ciEvent.EventID).WithField("project", project.Name).
		WithField("reason", reason.Detail).Info("Skipped CI event")
	audit.Record(ctx, h.store, project.OwnershipOrgID(), "ci_event."+status, "ci_event", ciEvent.EventID,
		models.JSONB{"project_id": project.ProjectID, "kind": reason.Kind, "reason": reason.Detail})
	if err := jobcontrol.PublishEventChecks(ctx, ciEvent.EventID); err != nil {
		h.logger.WithError(err).WithField("event_id", ciEvent.EventID).Warn("Could not post the checks status of a skipped event")
	}
}

// shedManualRunRefusal returns a non-empty reason when the shed level in
// force refuses a manual run from the caller. An organization administrator
// (an admin token, or a token with organizations:manage that the caller's
// role permits) may still start runs.
func shedManualRunRefusal(ctx context.Context, appStore store.Store, orgID string) string {
	state := shedding.State(ctx, appStore, orgID)
	if !state.Actions.RefuseManualRuns {
		return ""
	}
	admin := principalAllowsOrg(ctx, appStore, orgID, tokencaps.OrganizationsManage)
	return shedding.ManualRunRefusal(state, admin)
}

// respondShedRefused writes 503 with Retry-After for work that a shed level
// refuses.
func respondShedRefused(w http.ResponseWriter, reason string) {
	w.Header().Set("Retry-After", "60")
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusServiceUnavailable)
	_ = json.NewEncoder(w).Encode(ErrorResponse{Error: "shed_level", Message: reason})
}
