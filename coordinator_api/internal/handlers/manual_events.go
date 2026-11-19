package handlers

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"unicode"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/sirupsen/logrus"
)

// maxIdempotencyKeyLen bounds the client-supplied Idempotency-Key header.
const maxIdempotencyKeyLen = 255

// errIdempotencyInProgress: another request with the same Idempotency-Key is
// creating its eval job right now.
var errIdempotencyInProgress = errors.New("a request with this Idempotency-Key is still being processed")

// isManualEvalRequest reports whether a submitted job runs an eval: the
// submitter marks it with REACTORCIDE_JOB_KIND=eval, as the webhook eval
// jobs carry.
func isManualEvalRequest(job *models.Job) bool {
	if job == nil || job.JobEnvVars == nil {
		return false
	}
	kind, _ := job.JobEnvVars["REACTORCIDE_JOB_KIND"].(string)
	return strings.TrimSpace(kind) == "eval"
}

func jobEnvString(job *models.Job, key string) string {
	if job.JobEnvVars == nil {
		return ""
	}
	value, _ := job.JobEnvVars[key].(string)
	return strings.TrimSpace(value)
}

// validIdempotencyKey accepts printable, non-space text of a bounded length.
func validIdempotencyKey(key string) bool {
	if key == "" || len(key) > maxIdempotencyKeyLen {
		return false
	}
	for _, r := range key {
		if unicode.IsControl(r) || unicode.IsSpace(r) {
			return false
		}
	}
	return true
}

// prepareManualEvent records a `manual` CI event for an eval submitted
// through the API, before the job is stored, and links the job to it.
//
// The optional Idempotency-Key header makes the submission idempotent within
// the organization: a repeat with the same key returns the job the first
// request created (existing != nil) instead of creating another. A job that
// is not an eval, or a store without event support, gets no event (nil, nil,
// nil): the pre-event behavior.
func (h *JobHandler) prepareManualEvent(ctx context.Context, r *http.Request, job *models.Job, actor string) (*models.CIEvent, *models.Job, error) {
	if !isManualEvalRequest(job) {
		return nil, nil, nil
	}
	events, ok := h.store.(store.CIEventStore)
	if !ok {
		return nil, nil, nil
	}
	ciEvent := &models.CIEvent{
		OrgID:     job.OrgID,
		ProjectID: job.ProjectID,
		Source:    models.CIEventSourceManual,
		// Always "manual". The submitter states REACTORCIDE_EVENT_TYPE and
		// REACTORCIDE_BRANCH, and no signed VCS event confirms them, so
		// they never become the event type or a verified ref of the event
		// (docs/oidc.md, docs/ref-classes.md). The branch is kept only as
		// the submitted ref.
		EventType: models.CIEventSourceManual,
		Repo:      jobEnvString(job, "REACTORCIDE_REPO"),
		Branch:    jobEnvString(job, "REACTORCIDE_BRANCH"),
		CommitSHA: jobEnvString(job, "REACTORCIDE_SHA"),
		Actor:     actor,
		Status:    models.CIEventStatusReceived,
		// The principal that submitted the eval (the token's principal).
		ActorPrincipalID: job.ActorPrincipalID,
		ActorIdentity:    job.ActorIdentity,
	}
	if ciEvent.CommitSHA == "" && job.SourceRef != nil {
		ciEvent.CommitSHA = *job.SourceRef
	}
	if key := strings.TrimSpace(r.Header.Get("Idempotency-Key")); key != "" {
		if !validIdempotencyKey(key) {
			return nil, nil, store.ErrInvalidInput
		}
		// Scoped to the organization, so two tenants cannot collide or probe
		// each other's keys.
		scoped := fmt.Sprintf("manual:%s:%s", job.OrgID, key)
		ciEvent.IdempotencyKey = &scoped
	}
	// A manual event has the classes "manual" and the classes of rules that
	// name the event type "manual" (docs/ref-classes.md).
	applyRefClassesToEvent(ctx, h.store, ciEvent, models.RefClassFacts{Branch: ciEvent.Branch})
	recorded, outcome, err := events.RecordCIEvent(ctx, ciEvent)
	if err != nil {
		return nil, nil, err
	}
	if outcome == store.CIEventRedelivery && recorded.EvalJobID != nil {
		existing, err := h.store.GetJobByID(ctx, *recorded.EvalJobID)
		if err != nil {
			return nil, nil, err
		}
		return recorded, existing, nil
	}
	if claimer, ok := h.store.(store.CIEventClaimStore); ok {
		claimed, err := claimer.ClaimCIEventForEval(ctx, recorded.EventID)
		if err != nil {
			return nil, nil, err
		}
		if !claimed {
			return nil, nil, errIdempotencyInProgress
		}
	}
	applyCIEventToEvalJob(job, recorded)
	return recorded, nil, nil
}

// finishManualEvent links the stored eval job to its event, or gives the
// claim back when the job could not be stored.
func (h *JobHandler) finishManualEvent(ctx context.Context, ciEvent *models.CIEvent, job *models.Job, created bool) {
	if ciEvent == nil {
		return
	}
	if !created {
		if claimer, ok := h.store.(store.CIEventClaimStore); ok {
			if err := claimer.ReleaseCIEventClaim(ctx, ciEvent.EventID); err != nil {
				logrus.WithError(err).WithField("event_id", ciEvent.EventID).Warn("Failed to release manual event claim")
			}
		}
		return
	}
	events, ok := h.store.(store.CIEventStore)
	if !ok {
		return
	}
	if err := events.AttachCIEventEvalJob(ctx, ciEvent.EventID, job.JobID); err != nil {
		logrus.WithError(err).WithField("event_id", ciEvent.EventID).Warn("Failed to link eval job to manual event")
	}
}
