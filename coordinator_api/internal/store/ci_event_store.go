package store

import (
	"context"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

// CIEventRecordOutcome says what RecordCIEvent did with an incoming event.
type CIEventRecordOutcome string

const (
	// CIEventCreated: a new event that must be evaluated.
	CIEventCreated CIEventRecordOutcome = "created"
	// CIEventRedelivery: the same delivery arrived again. The returned event
	// is the first one; nothing new was stored.
	CIEventRedelivery CIEventRecordOutcome = "redelivery"
	// CIEventDuplicate: a new delivery of a change already recorded, and the
	// effective duplicate_events setting is "skip". A duplicate event row was
	// stored that points at the first event; it must not be evaluated.
	CIEventDuplicate CIEventRecordOutcome = "duplicate"
)

// CIEventStore stores CI events and the eval's per-workflow decisions.
//
// It is separate from Store, so the many full Store fakes do not each need
// these methods. A caller type-asserts and keeps the pre-event behavior when
// a store does not implement it.
type CIEventStore interface {
	// RecordCIEvent stores an incoming event. It is safe under concurrent
	// delivery: a redelivery (same idempotency key) returns the first event,
	// and semantic duplicates are decided under a lock on the semantic key.
	RecordCIEvent(ctx context.Context, event *models.CIEvent) (*models.CIEvent, CIEventRecordOutcome, error)
	GetCIEvent(ctx context.Context, eventID string) (*models.CIEvent, error)
	// AttachCIEventEvalJob links the eval job and marks the event evaluating.
	AttachCIEventEvalJob(ctx context.Context, eventID, jobID string) error
	SetCIEventStatus(ctx context.Context, eventID, status string) error
	SetCIEventSummary(ctx context.Context, eventID string, summary models.CIEventSummary) error
	// UpsertCIEventWorkflowDecisions inserts or replaces decisions keyed by
	// (event, workflow security ID).
	UpsertCIEventWorkflowDecisions(ctx context.Context, decisions []models.CIEventWorkflowDecision) error
	ListCIEventWorkflowDecisions(ctx context.Context, eventID string) ([]models.CIEventWorkflowDecision, error)
	// GetCIEventWorkflowDecisionByWorkflow returns the decision that created a
	// workflow, or ErrNotFound.
	GetCIEventWorkflowDecisionByWorkflow(ctx context.Context, workflowID string) (*models.CIEventWorkflowDecision, error)
}

// CIEventActorStore finds the actor of the most recent CI event of a pull
// request head. A re-evaluation uses it so that the actor facts are the
// facts of the event that ran CI for that head, not the approver's.
type CIEventActorStore interface {
	// LatestCIEventActorForPR returns "" and no error when no event matches.
	LatestCIEventActorForPR(ctx context.Context, projectID string, prNumber int, headSHA string) (string, error)
}
