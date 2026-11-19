package postgres_store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/lib/pq"
	"gorm.io/gorm"
)

var _ store.OutboundEventStore = PostgresDbStore{}

func normalizeSubscriptionArrays(sub *models.EventSubscription) {
	if sub.EventTypes == nil {
		sub.EventTypes = pq.StringArray{}
	}
	if sub.RefClassesAny == nil {
		sub.RefClassesAny = pq.StringArray{}
	}
	if sub.Results == nil {
		sub.Results = pq.StringArray{}
	}
	if sub.ActorKinds == nil {
		sub.ActorKinds = pq.StringArray{}
	}
	if sub.FailureClasses == nil {
		sub.FailureClasses = pq.StringArray{}
	}
	if sub.SinkConfig == nil {
		sub.SinkConfig = models.JSONB{}
	}
}

func (ps PostgresDbStore) CreateEventSubscription(ctx context.Context, sub *models.EventSubscription) error {
	normalizeSubscriptionArrays(sub)
	if err := ps.getDB(ctx).Omit("owner_active").Create(sub).Error; err != nil {
		if isUniqueViolation(err) {
			return fmt.Errorf("a subscription named %q already exists in this organization: %w", sub.Name, store.ErrAlreadyExists)
		}
		return fmt.Errorf("failed to create subscription: %w", err)
	}
	return nil
}

func (ps PostgresDbStore) GetEventSubscription(ctx context.Context, subscriptionID string) (*models.EventSubscription, error) {
	if !isValidUUID(subscriptionID) {
		return nil, store.ErrNotFound
	}
	var sub models.EventSubscription
	err := ps.getDB(ctx).Raw(`SELECT s.*, (p.status = 'active' AND p.merged_into IS NULL) AS owner_active
FROM event_subscriptions s JOIN principals p ON p.principal_id = s.owner_principal_id
WHERE s.subscription_id = ?`, subscriptionID).Scan(&sub).Error
	if err != nil {
		return nil, fmt.Errorf("failed to read subscription: %w", err)
	}
	if sub.SubscriptionID == "" {
		return nil, store.ErrNotFound
	}
	return &sub, nil
}

func (ps PostgresDbStore) ListEventSubscriptions(ctx context.Context, orgID string, projectID *string) ([]models.EventSubscription, error) {
	if !isValidUUID(orgID) {
		return nil, store.ErrNotFound
	}
	query := `SELECT s.*, (p.status = 'active' AND p.merged_into IS NULL) AS owner_active
FROM event_subscriptions s JOIN principals p ON p.principal_id = s.owner_principal_id
WHERE s.org_id = ?`
	args := []any{orgID}
	if projectID != nil {
		if !isValidUUID(*projectID) {
			return nil, store.ErrNotFound
		}
		query += ` AND s.project_id = ?`
		args = append(args, *projectID)
	}
	query += ` ORDER BY s.name`
	var subs []models.EventSubscription
	if err := ps.getDB(ctx).Raw(query, args...).Scan(&subs).Error; err != nil {
		return nil, fmt.Errorf("failed to list subscriptions: %w", err)
	}
	return subs, nil
}

func (ps PostgresDbStore) UpdateEventSubscription(ctx context.Context, sub *models.EventSubscription) error {
	if !isValidUUID(sub.SubscriptionID) {
		return store.ErrNotFound
	}
	normalizeSubscriptionArrays(sub)
	result := ps.getDB(ctx).Model(&models.EventSubscription{}).Where("subscription_id = ?", sub.SubscriptionID).
		Updates(map[string]any{
			"description":               sub.Description,
			"event_types":               sub.EventTypes,
			"ref_classes_any":           sub.RefClassesAny,
			"results":                   sub.Results,
			"actor_kinds":               sub.ActorKinds,
			"failure_classes":           sub.FailureClasses,
			"sink_config":               sub.SinkConfig,
			"signing_secret_ciphertext": sub.SigningSecretCiphertext,
			"signing_secret_key_name":   sub.SigningSecretKeyName,
			"max_attempts":              sub.MaxAttempts,
			"enabled":                   sub.Enabled,
			"updated_at":                gorm.Expr("timezone('utc', now())"),
		})
	if result.Error != nil {
		return fmt.Errorf("failed to update subscription: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return store.ErrNotFound
	}
	return nil
}

func (ps PostgresDbStore) DeleteEventSubscription(ctx context.Context, subscriptionID string) error {
	if !isValidUUID(subscriptionID) {
		return store.ErrNotFound
	}
	result := ps.getDB(ctx).Exec(`DELETE FROM event_subscriptions WHERE subscription_id = ?`, subscriptionID)
	if result.Error != nil {
		return fmt.Errorf("failed to delete subscription: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return store.ErrNotFound
	}
	return nil
}

func (ps PostgresDbStore) ListEventDeliveries(ctx context.Context, subscriptionID, status string, limit int) ([]models.EventDelivery, error) {
	if !isValidUUID(subscriptionID) {
		return nil, store.ErrNotFound
	}
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	db := ps.getDB(ctx).Where("subscription_id = ?", subscriptionID)
	if status != "" {
		db = db.Where("status = ?", status)
	}
	var deliveries []models.EventDelivery
	if err := db.Order("created_at DESC, delivery_id DESC").Limit(limit).Find(&deliveries).Error; err != nil {
		return nil, fmt.Errorf("failed to list deliveries: %w", err)
	}
	return deliveries, nil
}

func (ps PostgresDbStore) GetEventDelivery(ctx context.Context, deliveryID string) (*models.EventDelivery, error) {
	if !isValidUUID(deliveryID) {
		return nil, store.ErrNotFound
	}
	var delivery models.EventDelivery
	if err := ps.getDB(ctx).Where("delivery_id = ?", deliveryID).First(&delivery).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, store.ErrNotFound
		}
		return nil, fmt.Errorf("failed to read delivery: %w", err)
	}
	return &delivery, nil
}

func (ps PostgresDbStore) ListEventDeliveryAttempts(ctx context.Context, deliveryID string) ([]models.EventDeliveryAttempt, error) {
	if !isValidUUID(deliveryID) {
		return nil, store.ErrNotFound
	}
	var attempts []models.EventDeliveryAttempt
	if err := ps.getDB(ctx).Where("delivery_id = ?", deliveryID).Order("attempt_id").Find(&attempts).Error; err != nil {
		return nil, fmt.Errorf("failed to list delivery attempts: %w", err)
	}
	return attempts, nil
}

const replayDeliverySet = `status = 'pending', attempts = 0, next_attempt_at = timezone('utc', now()),
    dead_lettered_at = NULL, last_error = '', updated_at = timezone('utc', now())`

func (ps PostgresDbStore) ReplayEventDelivery(ctx context.Context, deliveryID string) (*models.EventDelivery, error) {
	if !isValidUUID(deliveryID) {
		return nil, store.ErrNotFound
	}
	var rows []models.EventDelivery
	err := ps.getDB(ctx).Raw(`UPDATE event_deliveries SET `+replayDeliverySet+`
WHERE delivery_id = ? AND status IN ('delivered', 'dead_letter') RETURNING *`, deliveryID).Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("failed to replay delivery: %w", err)
	}
	if len(rows) == 0 {
		delivery, getErr := ps.GetEventDelivery(ctx, deliveryID)
		if getErr != nil {
			return nil, getErr
		}
		return nil, fmt.Errorf("delivery is %s; only a delivered or dead_letter delivery can be replayed: %w", delivery.Status, store.ErrConflict)
	}
	return &rows[0], nil
}

func (ps PostgresDbStore) ReplayDeadLetterDeliveries(ctx context.Context, subscriptionID string) (int64, error) {
	if !isValidUUID(subscriptionID) {
		return 0, store.ErrNotFound
	}
	result := ps.getDB(ctx).Exec(`UPDATE event_deliveries SET `+replayDeliverySet+`
WHERE subscription_id = ? AND status = 'dead_letter'`, subscriptionID)
	if result.Error != nil {
		return 0, fmt.Errorf("failed to replay deliveries: %w", result.Error)
	}
	return result.RowsAffected, nil
}

// ListUndispatchedOutbox reads the next undispatched rows in occurrence
// order with the names the payload needs. A merged actor resolves to its
// target.
func (ps PostgresDbStore) ListUndispatchedOutbox(ctx context.Context, limit int) ([]models.OutboxEventFacts, error) {
	return ps.ListUndispatchedOutboxBefore(ctx, time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC), limit)
}

// ListUndispatchedOutboxBefore reads the undispatched rows that occurred at
// or before cutoff, ordered by (occurred_at, outbox_id). occurred_at is the
// start of the writing transaction, so two transactions on one workflow are
// read in the order in which they started, also when the later one commits
// first (the dispatcher waits a short lag before it reads a row).
func (ps PostgresDbStore) ListUndispatchedOutboxBefore(ctx context.Context, cutoff time.Time, limit int) ([]models.OutboxEventFacts, error) {
	if limit <= 0 {
		limit = 200
	}
	var rows []models.OutboxEventFacts
	err := ps.getDB(ctx).Raw(`
SELECT o.*,
    coalesce(org.name, '') AS org_name,
    coalesce(p.name, '') AS project_name,
    coalesce(w.name, o.details->>'workflow_name', '') AS workflow_name,
    coalesce(nullif(o.workflow_security_id, ''), w.workflow_security_id, '') AS wf_security_id,
    coalesce(n.name, nullif(o.details->>'node_name', ''), '') AS node_name,
    coalesce(j.name, o.details->>'job_name', '') AS job_name,
    coalesce(pm.kind, pr.kind, '') AS actor_kind,
    coalesce(nullif(pm.display_name, ''), pm.name, nullif(pr.display_name, ''), pr.name, '') AS actor_display_name,
    coalesce(nullif(e.ref, ''), e.branch, '') AS event_ref,
    coalesce(e.commit_sha, '') AS event_sha,
    coalesce(nullif(w.vcs_repo, ''), e.repo, '') AS vcs_repo
FROM event_outbox o
JOIN organizations org ON org.org_id = o.org_id
LEFT JOIN projects p ON p.project_id = o.project_id
LEFT JOIN workflow_instances w ON w.workflow_id = o.workflow_id
LEFT JOIN workflow_nodes n ON n.node_id = o.node_id
LEFT JOIN jobs j ON j.job_id = o.job_id
LEFT JOIN principals pr ON pr.principal_id = o.actor_principal_id
LEFT JOIN principals pm ON pm.principal_id = pr.merged_into
LEFT JOIN ci_events e ON e.event_id = o.ci_event_id
WHERE o.dispatched_at IS NULL AND o.occurred_at <= ?
ORDER BY o.occurred_at, o.outbox_id
LIMIT ?`, cutoff, limit).Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("failed to read event outbox: %w", err)
	}
	return rows, nil
}

func (ps PostgresDbStore) ListEnabledEventSubscriptions(ctx context.Context, orgIDs []string) ([]models.EventSubscription, error) {
	if len(orgIDs) == 0 {
		return nil, nil
	}
	var subs []models.EventSubscription
	err := ps.getDB(ctx).Raw(`SELECT s.*, (p.status = 'active' AND p.merged_into IS NULL) AS owner_active
FROM event_subscriptions s JOIN principals p ON p.principal_id = s.owner_principal_id
WHERE s.enabled AND s.org_id = ANY(?::uuid[])`, pq.StringArray(orgIDs)).Scan(&subs).Error
	if err != nil {
		return nil, fmt.Errorf("failed to list enabled subscriptions: %w", err)
	}
	return subs, nil
}

func (ps PostgresDbStore) CommitOutboxDispatch(ctx context.Context, dispatches []models.OutboxDispatch, markOnly []int64) error {
	return ps.getDB(ctx).Transaction(func(tx *gorm.DB) error {
		if len(markOnly) > 0 {
			if err := tx.Exec(`UPDATE event_outbox SET dispatched_at = timezone('utc', now())
WHERE outbox_id = ANY(?::bigint[]) AND dispatched_at IS NULL`, pq.Int64Array(markOnly)).Error; err != nil {
				return fmt.Errorf("failed to mark outbox rows: %w", err)
			}
		}
		for _, d := range dispatches {
			var taken []int64
			if err := tx.Raw(`UPDATE event_outbox SET dispatched_at = timezone('utc', now())
WHERE outbox_id = ? AND dispatched_at IS NULL RETURNING outbox_id`, d.OutboxID).Scan(&taken).Error; err != nil {
				return fmt.Errorf("failed to take outbox row %d: %w", d.OutboxID, err)
			}
			if len(taken) == 0 {
				continue
			}
			var sequence []int64
			if err := tx.Raw(`INSERT INTO event_outbox_sequences (order_key, last_sequence) VALUES (?, 1)
ON CONFLICT (order_key) DO UPDATE SET last_sequence = event_outbox_sequences.last_sequence + 1,
    updated_at = timezone('utc', now())
RETURNING last_sequence`, d.OrderKey).Scan(&sequence).Error; err != nil || len(sequence) != 1 {
				if err == nil {
					err = errors.New("no sequence returned")
				}
				return fmt.Errorf("failed to assign the sequence of outbox row %d: %w", d.OutboxID, err)
			}
			var payload models.JSONB
			if d.Payload != nil {
				payload = d.Payload(sequence[0])
			}
			if err := tx.Exec(`UPDATE event_outbox SET sequence = ?, payload = ? WHERE outbox_id = ?`,
				sequence[0], payload, d.OutboxID).Error; err != nil {
				return fmt.Errorf("failed to store the payload of outbox row %d: %w", d.OutboxID, err)
			}
			for _, subscriptionID := range d.SubscriptionIDs {
				if err := tx.Exec(`INSERT INTO event_deliveries (subscription_id, outbox_id, event_id, event_type, order_key, sequence)
VALUES (?, ?, ?, ?, ?, ?) ON CONFLICT (subscription_id, event_id) DO NOTHING`,
					subscriptionID, d.OutboxID, d.EventID, d.EventType, d.OrderKey, sequence[0]).Error; err != nil {
					return fmt.Errorf("failed to create a delivery for outbox row %d: %w", d.OutboxID, err)
				}
			}
		}
		return nil
	})
}

func (ps PostgresDbStore) ListDueDeliveryHeads(ctx context.Context, now time.Time, limit int) ([]models.EventDelivery, error) {
	if limit <= 0 {
		limit = 100
	}
	var heads []models.EventDelivery
	err := ps.getDB(ctx).Raw(`
SELECT h.* FROM (
    SELECT DISTINCT ON (subscription_id, order_key) *
    FROM event_deliveries
    WHERE status IN ('pending', 'queued', 'sending')
    ORDER BY subscription_id, order_key, sequence, created_at
) h
WHERE h.status = 'pending' AND h.next_attempt_at <= ?
ORDER BY h.next_attempt_at, h.sequence
LIMIT ?`, now, limit).Scan(&heads).Error
	if err != nil {
		return nil, fmt.Errorf("failed to list due deliveries: %w", err)
	}
	return heads, nil
}

func (ps PostgresDbStore) MarkDeliveryQueued(ctx context.Context, deliveryID string) (string, bool, error) {
	var keys []string
	err := ps.getDB(ctx).Raw(`UPDATE event_deliveries SET status = 'queued', generation = generation + 1,
    task_key = delivery_id::text || ':' || (generation + 1)::text, task_id = '',
    queued_at = timezone('utc', now()), updated_at = timezone('utc', now())
WHERE delivery_id = ? AND status = 'pending' RETURNING task_key`, deliveryID).Scan(&keys).Error
	if err != nil {
		return "", false, fmt.Errorf("failed to queue delivery: %w", err)
	}
	if len(keys) == 0 {
		return "", false, nil
	}
	return keys[0], true, nil
}

func (ps PostgresDbStore) SetDeliveryTaskID(ctx context.Context, deliveryID, taskKey, taskID string) error {
	return ps.getDB(ctx).Exec(`UPDATE event_deliveries SET task_id = ? WHERE delivery_id = ? AND task_key = ?`,
		taskID, deliveryID, taskKey).Error
}

func (ps PostgresDbStore) RevertDeliveryQueued(ctx context.Context, deliveryID, taskKey string, nextAttemptAt time.Time, reason string) error {
	return ps.getDB(ctx).Exec(`UPDATE event_deliveries SET status = 'pending', next_attempt_at = ?, last_error = ?,
    updated_at = timezone('utc', now())
WHERE delivery_id = ? AND task_key = ? AND status = 'queued'`, nextAttemptAt, truncateOutboundText(reason, 1000), deliveryID, taskKey).Error
}

func (ps PostgresDbStore) ResetStaleDeliveries(ctx context.Context, before time.Time) ([]string, error) {
	var taskIDs []string
	err := ps.getDB(ctx).Raw(`UPDATE event_deliveries d SET status = 'pending', next_attempt_at = timezone('utc', now()),
    last_error = CASE WHEN d.status = 'sending' THEN 'the delivery loop stopped during a send' ELSE 'the queued task was not claimed in time' END,
    updated_at = timezone('utc', now())
FROM (SELECT delivery_id, task_id FROM event_deliveries
      WHERE status IN ('queued', 'sending') AND updated_at < ? FOR UPDATE SKIP LOCKED) stale
WHERE d.delivery_id = stale.delivery_id
RETURNING stale.task_id`, before).Scan(&taskIDs).Error
	if err != nil {
		return nil, fmt.Errorf("failed to reset stale deliveries: %w", err)
	}
	out := taskIDs[:0]
	for _, id := range taskIDs {
		if id != "" {
			out = append(out, id)
		}
	}
	return out, nil
}

func (ps PostgresDbStore) BeginDeliverySend(ctx context.Context, deliveryID, taskKey string) (*models.DeliveryWork, bool, error) {
	if !isValidUUID(deliveryID) {
		return nil, false, nil
	}
	var rows []models.EventDelivery
	err := ps.getDB(ctx).Raw(`UPDATE event_deliveries SET status = 'sending', attempts = attempts + 1,
    updated_at = timezone('utc', now())
WHERE delivery_id = ? AND task_key = ? AND status = 'queued' RETURNING *`, deliveryID, taskKey).Scan(&rows).Error
	if err != nil {
		return nil, false, fmt.Errorf("failed to start delivery: %w", err)
	}
	if len(rows) == 0 {
		return nil, false, nil
	}
	work := &models.DeliveryWork{Delivery: rows[0]}
	sub, err := ps.GetEventSubscription(ctx, rows[0].SubscriptionID)
	if err != nil {
		return nil, false, err
	}
	work.Subscription = *sub
	var outbox models.EventOutbox
	if err := ps.getDB(ctx).Where("outbox_id = ?", rows[0].OutboxID).First(&outbox).Error; err != nil {
		return nil, false, fmt.Errorf("failed to read the event of the delivery: %w", err)
	}
	work.Payload = outbox.Payload
	work.OccurredAt = outbox.OccurredAt
	return work, true, nil
}

func (ps PostgresDbStore) FinishDeliveryAttempt(ctx context.Context, result models.DeliveryAttemptResult) error {
	return ps.getDB(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec(`INSERT INTO event_delivery_attempts (delivery_id, attempt, started_at, finished_at, result, http_status, latency_ms, error)
SELECT delivery_id, attempts, ?, ?, ?, ?, ?, ? FROM event_deliveries WHERE delivery_id = ?`,
			result.StartedAt, result.FinishedAt, result.Result, result.HTTPStatus, result.LatencyMs,
			truncateOutboundText(result.Error, 2000), result.DeliveryID).Error; err != nil {
			return fmt.Errorf("failed to record delivery attempt: %w", err)
		}
		set := `status = ?, last_http_status = ?, last_latency_ms = ?, last_error = ?, updated_at = timezone('utc', now())`
		args := []any{result.NextStatus, result.HTTPStatus, result.LatencyMs, truncateOutboundText(result.Error, 1000)}
		switch result.NextStatus {
		case models.DeliveryStatusDelivered:
			set += `, delivered_at = timezone('utc', now())`
		case models.DeliveryStatusDeadLetter:
			set += `, dead_lettered_at = timezone('utc', now())`
		default:
			set += `, next_attempt_at = ?`
			args = append(args, result.NextAttemptAt)
		}
		args = append(args, result.DeliveryID, result.TaskKey)
		return tx.Exec(`UPDATE event_deliveries SET `+set+`
WHERE delivery_id = ? AND task_key = ? AND status = 'sending'`, args...).Error
	})
}

func (ps PostgresDbStore) OldestUndispatchedOutbox(ctx context.Context) (*time.Time, error) {
	var oldest []time.Time
	if err := ps.getDB(ctx).Raw(`SELECT occurred_at FROM event_outbox WHERE dispatched_at IS NULL
ORDER BY occurred_at, outbox_id LIMIT 1`).Scan(&oldest).Error; err != nil {
		return nil, fmt.Errorf("failed to read outbox lag: %w", err)
	}
	if len(oldest) == 0 {
		return nil, nil
	}
	return &oldest[0], nil
}

// PruneEventOutbox deletes dispatched outbox rows older than before that no
// open delivery needs (their final deliveries go with them), and the
// sequence counters that did not change since before.
func (ps PostgresDbStore) PruneEventOutbox(ctx context.Context, before time.Time) (int64, error) {
	result := ps.getDB(ctx).Exec(`DELETE FROM event_outbox o
WHERE o.occurred_at < ? AND o.dispatched_at IS NOT NULL
  AND NOT EXISTS (SELECT 1 FROM event_deliveries d WHERE d.outbox_id = o.outbox_id
                  AND d.status IN ('pending', 'queued', 'sending'))`, before)
	if result.Error != nil {
		return 0, fmt.Errorf("failed to prune event outbox: %w", result.Error)
	}
	if err := ps.getDB(ctx).Exec(`DELETE FROM event_outbox_sequences WHERE updated_at < ?`, before).Error; err != nil {
		return result.RowsAffected, fmt.Errorf("failed to prune event sequences: %w", err)
	}
	return result.RowsAffected, nil
}

// PruneUndispatchedEventOutbox deletes undispatched outbox rows older than
// before. No delivery refers to an undispatched row. A coordinator with the
// dispatcher off (or with no Corndogs) still runs it, so the outbox cannot
// grow without a limit.
func (ps PostgresDbStore) PruneUndispatchedEventOutbox(ctx context.Context, before time.Time) (int64, error) {
	result := ps.getDB(ctx).Exec(`DELETE FROM event_outbox WHERE dispatched_at IS NULL AND occurred_at < ?`, before)
	if result.Error != nil {
		return 0, fmt.Errorf("failed to prune undispatched outbox rows: %w", result.Error)
	}
	return result.RowsAffected, nil
}

func truncateOutboundText(value string, max int) string {
	value = strings.ToValidUTF8(value, "")
	if len(value) <= max {
		return value
	}
	cut := max
	for cut > 0 && (value[cut]&0xC0) == 0x80 {
		cut--
	}
	return value[:cut]
}
