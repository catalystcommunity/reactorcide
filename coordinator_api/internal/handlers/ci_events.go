package handlers

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/vcs"
	"github.com/sirupsen/logrus"
)

// webhookDeliveryID returns the provider's unique delivery ID, or "" when the
// request has none.
func webhookDeliveryID(r *http.Request, provider vcs.Provider) string {
	switch provider {
	case vcs.GitHub:
		return strings.TrimSpace(r.Header.Get("X-GitHub-Delivery"))
	case vcs.GitLab:
		return strings.TrimSpace(r.Header.Get("X-Gitlab-Event-UUID"))
	}
	return ""
}

// ciEventFromWebhook builds the event row for a pull request or push
// delivery. It holds identifying facts only; it never holds the raw payload.
func ciEventFromWebhook(project *models.Project, event *vcs.WebhookEvent) *models.CIEvent {
	ciEvent := &models.CIEvent{
		OrgID:      project.OwnershipOrgID(),
		ProjectID:  &project.ProjectID,
		Source:     models.CIEventSourceWebhook,
		Provider:   string(event.Provider),
		DeliveryID: event.DeliveryID,
		EventType:  string(event.GenericEvent),
		Repo:       event.Repository.FullName,
		Actor:      event.SenderLogin,
		Status:     models.CIEventStatusReceived,
	}
	prNumber := 0
	switch {
	case event.PullRequest != nil:
		pr := event.PullRequest
		sha := pr.HeadSHA
		if event.GenericEvent == vcs.EventPullRequestMerged && pr.MergeSHA != "" {
			sha = pr.MergeSHA
		}
		prNumber = pr.Number
		number := pr.Number
		ciEvent.PRNumber = &number
		ciEvent.Ref = pr.HeadRef
		ciEvent.Branch = pr.BaseRef
		ciEvent.CommitSHA = sha
		ciEvent.BaseSHA = pr.BaseSHA
		ciEvent.Summary = models.CIEventSummary{Action: pr.Action, HeadRef: pr.HeadRef, BaseRef: pr.BaseRef}
	case event.Push != nil:
		push := event.Push
		ciEvent.Ref = push.Ref
		ciEvent.Branch = extractBranchOrTag(push.Ref)
		ciEvent.CommitSHA = push.After
		if isRealCommit(push.Before) {
			ciEvent.BaseSHA = push.Before
		}
	}
	if event.DeliveryID != "" {
		key := fmt.Sprintf("%s:%s", event.Provider, event.DeliveryID)
		ciEvent.IdempotencyKey = &key
	}
	ciEvent.SemanticKey = fmt.Sprintf("%s:%s:%s:%s:%s:%d", event.Provider, ciEvent.Repo,
		ciEvent.EventType, ciEvent.Ref, ciEvent.CommitSHA, prNumber)
	return ciEvent
}

// recordCIEvent stores the event before any eval job exists and decides
// whether the delivery must be evaluated. It returns proceed=false for a
// redelivery that already has an eval job, and for a duplicate that the
// effective duplicate_events setting skips. A store without event support
// returns (nil, true, nil): the pre-event behavior.
func (h *WebhookHandler) recordCIEvent(ctx context.Context, project *models.Project, event *vcs.WebhookEvent) (*models.CIEvent, bool, error) {
	events, ok := h.store.(store.CIEventStore)
	if !ok {
		return nil, true, nil
	}
	recorded, outcome, err := events.RecordCIEvent(ctx, ciEventFromWebhook(project, event))
	if err != nil {
		return nil, false, err
	}
	fields := logrus.Fields{
		"event_id": recorded.EventID, "delivery_id": event.DeliveryID,
		"project": project.Name, "event_type": recorded.EventType, "outcome": string(outcome),
	}
	switch outcome {
	case store.CIEventRedelivery:
		// A delivery whose first attempt stored the event but failed before
		// the eval job existed has nothing running. Evaluate it now instead of
		// dropping it for good.
		if recorded.EvalJobID == nil && recorded.Status == models.CIEventStatusReceived {
			h.logger.WithFields(fields).Info("Redelivery of an event with no eval job; evaluating it")
			return recorded, true, nil
		}
		h.logger.WithFields(fields).Info("Ignoring webhook redelivery")
		return recorded, false, nil
	case store.CIEventDuplicate:
		fields["duplicate_of_event_id"] = recorded.DuplicateOfEventID
		h.logger.WithFields(fields).Info("Skipping duplicate event per duplicate_events setting")
		return recorded, false, nil
	}
	return recorded, true, nil
}

// applyCIEventToEvalJob links an eval job to its event. The eval uses the
// event ID as its trigger operation ID, so a retried eval of the same event
// reuses the workflows the first eval created.
func applyCIEventToEvalJob(job *models.Job, ciEvent *models.CIEvent) {
	if ciEvent == nil || job == nil {
		return
	}
	eventID := ciEvent.EventID
	job.EventID = &eventID
	if job.JobEnvVars == nil {
		job.JobEnvVars = models.JSONB{}
	}
	job.JobEnvVars["REACTORCIDE_EVENT_ID"] = eventID
}

func (h *WebhookHandler) attachEvalJobToCIEvent(ctx context.Context, ciEvent *models.CIEvent, job *models.Job) {
	if ciEvent == nil {
		return
	}
	events, ok := h.store.(store.CIEventStore)
	if !ok {
		return
	}
	if err := events.AttachCIEventEvalJob(ctx, ciEvent.EventID, job.JobID); err != nil {
		h.logger.WithError(err).WithField("event_id", ciEvent.EventID).Warn("Failed to link eval job to CI event")
	}
}
