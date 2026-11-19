package store

import (
	"context"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

// OutboundEventStore holds the event outbox, the event subscriptions, and
// their deliveries (docs/outbound-events.md). Like LeaseStore it is separate
// from Store; callers type-assert.
type OutboundEventStore interface {
	// Subscriptions.
	CreateEventSubscription(ctx context.Context, sub *models.EventSubscription) error
	GetEventSubscription(ctx context.Context, subscriptionID string) (*models.EventSubscription, error)
	// ListEventSubscriptions lists the subscriptions of an organization.
	// A non-nil projectID lists only project and workflow subscriptions of
	// that project.
	ListEventSubscriptions(ctx context.Context, orgID string, projectID *string) ([]models.EventSubscription, error)
	// UpdateEventSubscription writes the mutable fields: description,
	// filters, sink config, signing secret, max attempts, enabled.
	UpdateEventSubscription(ctx context.Context, sub *models.EventSubscription) error
	DeleteEventSubscription(ctx context.Context, subscriptionID string) error

	// Delivery log.
	ListEventDeliveries(ctx context.Context, subscriptionID, status string, limit int) ([]models.EventDelivery, error)
	GetEventDelivery(ctx context.Context, deliveryID string) (*models.EventDelivery, error)
	ListEventDeliveryAttempts(ctx context.Context, deliveryID string) ([]models.EventDeliveryAttempt, error)
	// ReplayEventDelivery puts a delivered or dead-lettered delivery back to
	// pending, with a new attempt budget.
	ReplayEventDelivery(ctx context.Context, deliveryID string) (*models.EventDelivery, error)
	// ReplayDeadLetterDeliveries replays every dead-lettered delivery of a
	// subscription and returns the count.
	ReplayDeadLetterDeliveries(ctx context.Context, subscriptionID string) (int64, error)

	// Dispatcher.
	ListUndispatchedOutbox(ctx context.Context, limit int) ([]models.OutboxEventFacts, error)
	// ListUndispatchedOutboxBefore is ListUndispatchedOutbox for rows that
	// occurred at or before cutoff, in occurrence order (occurred_at, then
	// outbox_id).
	ListUndispatchedOutboxBefore(ctx context.Context, cutoff time.Time, limit int) ([]models.OutboxEventFacts, error)
	ListEnabledEventSubscriptions(ctx context.Context, orgIDs []string) ([]models.EventSubscription, error)
	// CommitOutboxDispatch writes, in one transaction: the sequence and
	// payload of each dispatch with subscriptions, its deliveries, and the
	// dispatched time of every row (also markOnly, the rows with no match).
	// A row that another dispatcher already took is skipped.
	CommitOutboxDispatch(ctx context.Context, dispatches []models.OutboxDispatch, markOnly []int64) error
	// ListDueDeliveryHeads returns pending deliveries that are the first open
	// delivery of their (subscription, order key) and are due at now.
	ListDueDeliveryHeads(ctx context.Context, now time.Time, limit int) ([]models.EventDelivery, error)
	// MarkDeliveryQueued moves a pending delivery to queued with a new task
	// key. ok is false when the delivery is not pending.
	MarkDeliveryQueued(ctx context.Context, deliveryID string) (taskKey string, ok bool, err error)
	SetDeliveryTaskID(ctx context.Context, deliveryID, taskKey, taskID string) error
	// RevertDeliveryQueued returns a queued delivery to pending after a
	// failed submit.
	RevertDeliveryQueued(ctx context.Context, deliveryID, taskKey string, nextAttemptAt time.Time, reason string) error
	// ResetStaleDeliveries returns queued or sending deliveries that did not
	// change since before to pending. It returns the task IDs of the reset
	// deliveries so the caller can discard the tasks.
	ResetStaleDeliveries(ctx context.Context, before time.Time) ([]string, error)

	// Delivery loop.
	// BeginDeliverySend moves a queued delivery with taskKey to sending and
	// counts the attempt. ok is false for a stale task.
	BeginDeliverySend(ctx context.Context, deliveryID, taskKey string) (*models.DeliveryWork, bool, error)
	FinishDeliveryAttempt(ctx context.Context, result models.DeliveryAttemptResult) error

	// Metrics and retention.
	OldestUndispatchedOutbox(ctx context.Context) (*time.Time, error)
	PruneEventOutbox(ctx context.Context, before time.Time) (int64, error)
	// PruneUndispatchedEventOutbox deletes undispatched rows that occurred
	// before before. It runs also when the dispatcher does not.
	PruneUndispatchedEventOutbox(ctx context.Context, before time.Time) (int64, error)
}
