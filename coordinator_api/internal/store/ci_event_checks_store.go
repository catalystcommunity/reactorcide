package store

import (
	"context"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

// CIEventChecksStore holds what the "Reactorcide Checks" aggregate status of
// a CI event needs. It is separate from Store, like CIEventStore, so callers
// type-assert it and keep the pre-event behavior when it is absent.
type CIEventChecksStore interface {
	// WithCIEventChecksLock runs fn in a transaction that holds an advisory
	// lock on the event. Reads and writes through the ctx passed to fn use
	// that transaction, so a recompute and publish on one replica cannot
	// interleave with another replica's for the same event.
	WithCIEventChecksLock(ctx context.Context, eventID string, fn func(ctx context.Context) error) error
	// ListRootWorkflowInstancesForEvent returns the root workflows the event
	// created, oldest first.
	ListRootWorkflowInstancesForEvent(ctx context.Context, eventID string) ([]models.WorkflowInstance, error)
	// SaveCIEventChecks records the last published checks state.
	SaveCIEventChecks(ctx context.Context, eventID string, checks models.CIEventChecks) error
	// SetCIEventPolicyViolationCount records how many changed CI files the
	// coordinator policy refused for the event.
	SetCIEventPolicyViolationCount(ctx context.Context, eventID string, count int) error
}
