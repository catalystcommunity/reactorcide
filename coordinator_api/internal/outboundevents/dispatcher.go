package outboundevents

import (
	"context"
	"encoding/json"
	"math/rand/v2"
	"time"

	"github.com/catalystcommunity/app-utils-go/logging"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/corndogs"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

// LeaseName is the coordinator lease of the dispatcher.
const LeaseName = "outbound-events"

// DefaultOutboxLag is the default Dispatcher.Lag.
const DefaultOutboxLag = 2 * time.Second

func (d *Dispatcher) cutoff() time.Time {
	lag := d.Lag
	if lag == 0 {
		lag = DefaultOutboxLag
	}
	if lag < 0 {
		lag = 0
	}
	return d.now().Add(-lag)
}

// TaskQueue is the Corndogs part that outbound events use.
// *corndogs.InternalQueue implements it.
type TaskQueue interface {
	SubmitKeyedTask(ctx context.Context, queue, key string, payload []byte) (string, error)
	Claim(ctx context.Context, queue string) (*corndogs.ClaimedTask, error)
	Complete(ctx context.Context, task *corndogs.ClaimedTask) error
	Discard(ctx context.Context, queue, taskID string) error
}

// TaskPayload is the payload of a delivery task. It carries IDs only; the
// event stays in the database.
type TaskPayload struct {
	DeliveryID string `json:"delivery_id"`
	TaskKey    string `json:"task_key"`
}

// Dispatcher fans the outbox out to deliveries and submits delivery tasks.
// Run it on one replica (lease.Manager with LeaseName).
type Dispatcher struct {
	Store     store.OutboundEventStore
	Queue     TaskQueue
	QueueName string
	UIBaseURL string
	// Interval between ticks. Default 1s.
	Interval time.Duration
	// BatchSize is the number of outbox rows in one transaction. Default 200.
	BatchSize int
	// StaleAfter resets a queued or sending delivery that did not change for
	// this long. Default 10 minutes.
	StaleAfter time.Duration
	// Retention deletes dispatched outbox rows (and their final deliveries)
	// after this long, and undispatched rows of the same age. 0 keeps them.
	Retention time.Duration
	// Lag is how old an outbox row must be before the dispatcher reads it.
	// A row's occurred_at is the start of its transaction; the lag lets a
	// transaction that started earlier but commits later be read first, so
	// one workflow delivers in occurrence order. 0 uses DefaultOutboxLag; a
	// negative value reads every row at once (tests).
	Lag time.Duration
	Now func() time.Time

	lastReap  time.Time
	lastPrune time.Time
}

func (d *Dispatcher) now() time.Time {
	if d.Now != nil {
		return d.Now().UTC()
	}
	return time.Now().UTC()
}

func (d *Dispatcher) queueName() string {
	if d.QueueName != "" {
		return d.QueueName
	}
	return corndogs.OutboundEventsQueue
}

// Run ticks until ctx ends.
func (d *Dispatcher) Run(ctx context.Context) {
	interval := d.Interval
	if interval <= 0 {
		interval = time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if err := d.Tick(ctx); err != nil && ctx.Err() == nil {
			logging.Log.WithError(err).Warn("Outbound event dispatcher tick failed")
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// Tick runs one pass: fan-out, submit, reap, prune, and the lag gauge.
func (d *Dispatcher) Tick(ctx context.Context) error {
	if err := d.FanOut(ctx); err != nil {
		return err
	}
	if err := d.SubmitDue(ctx); err != nil {
		return err
	}
	now := d.now()
	if now.Sub(d.lastReap) >= time.Minute {
		d.lastReap = now
		d.ReapStale(ctx)
	}
	if d.Retention > 0 && now.Sub(d.lastPrune) >= time.Hour {
		d.lastPrune = now
		PruneOutbox(ctx, d.Store, now.Add(-d.Retention))
	}
	if oldest, err := d.Store.OldestUndispatchedOutbox(ctx); err == nil {
		if oldest == nil {
			OutboxLag.Set(0)
		} else {
			OutboxLag.Set(now.Sub(*oldest).Seconds())
		}
	}
	return nil
}

// OutboxPruner is the store part of PruneOutbox.
type OutboxPruner interface {
	PruneEventOutbox(ctx context.Context, before time.Time) (int64, error)
	PruneUndispatchedEventOutbox(ctx context.Context, before time.Time) (int64, error)
}

// PruneOutbox deletes dispatched rows (with their final deliveries) and
// undispatched rows that occurred before before. The dispatcher calls it,
// and the coordinator retention loop calls it also when the dispatcher is
// off, so the outbox cannot grow without a limit.
func PruneOutbox(ctx context.Context, st OutboxPruner, before time.Time) {
	if _, err := st.PruneEventOutbox(ctx, before); err != nil {
		logging.Log.WithError(err).Warn("Outbound event outbox retention failed")
	}
	if removed, err := st.PruneUndispatchedEventOutbox(ctx, before); err != nil {
		logging.Log.WithError(err).Warn("Outbound event outbox retention of undispatched rows failed")
	} else if removed > 0 {
		logging.Log.WithField("rows", removed).Info("Removed undispatched outbound events older than the retention")
	}
}

// FanOut dispatches undispatched outbox rows, up to ten batches per call.
func (d *Dispatcher) FanOut(ctx context.Context) error {
	batch := d.BatchSize
	if batch <= 0 {
		batch = 200
	}
	for i := 0; i < 10; i++ {
		rows, err := d.Store.ListUndispatchedOutboxBefore(ctx, d.cutoff(), batch)
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			return nil
		}
		orgSet := map[string]struct{}{}
		var orgIDs []string
		for _, row := range rows {
			if _, ok := orgSet[row.OrgID]; !ok {
				orgSet[row.OrgID] = struct{}{}
				orgIDs = append(orgIDs, row.OrgID)
			}
		}
		subs, err := d.Store.ListEnabledEventSubscriptions(ctx, orgIDs)
		if err != nil {
			return err
		}
		var dispatches []models.OutboxDispatch
		var markOnly []int64
		for idx := range rows {
			row := rows[idx]
			var matched []string
			for s := range subs {
				if Matches(&subs[s], &row) {
					matched = append(matched, subs[s].SubscriptionID)
				}
			}
			if len(matched) == 0 {
				markOnly = append(markOnly, row.OutboxID)
				continue
			}
			base := d.UIBaseURL
			dispatches = append(dispatches, models.OutboxDispatch{
				OutboxID: row.OutboxID, OrderKey: row.OrderKey, EventID: row.EventID, EventType: row.EventType,
				SubscriptionIDs: matched,
				Payload: func(sequence int64) models.JSONB {
					return BuildPayload(row, sequence, base).ToJSONB()
				},
			})
		}
		if err := d.Store.CommitOutboxDispatch(ctx, dispatches, markOnly); err != nil {
			return err
		}
		DispatchedTotal.WithLabelValues("matched").Add(float64(len(dispatches)))
		DispatchedTotal.WithLabelValues("unmatched").Add(float64(len(markOnly)))
		if len(rows) < batch {
			return nil
		}
	}
	return nil
}

// SubmitDue submits one task for each due head delivery. A head is the first
// open delivery of its (subscription, order key), so at most one delivery of
// one workflow is in flight for a subscription.
func (d *Dispatcher) SubmitDue(ctx context.Context) error {
	heads, err := d.Store.ListDueDeliveryHeads(ctx, d.now(), 200)
	if err != nil {
		return err
	}
	for _, head := range heads {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		key, ok, err := d.Store.MarkDeliveryQueued(ctx, head.DeliveryID)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		payload, _ := json.Marshal(TaskPayload{DeliveryID: head.DeliveryID, TaskKey: key})
		taskID, err := d.Queue.SubmitKeyedTask(ctx, d.queueName(), key, payload)
		if err != nil {
			// Corndogs is not available: try again soon. The run is not
			// affected.
			retry := d.now().Add(5*time.Second + time.Duration(rand.Int64N(int64(5*time.Second))))
			if revertErr := d.Store.RevertDeliveryQueued(ctx, head.DeliveryID, key, retry, "task submit failed: "+err.Error()); revertErr != nil {
				logging.Log.WithError(revertErr).Warn("Failed to return an outbound event delivery to pending")
			}
			logging.Log.WithError(err).WithField("delivery_id", head.DeliveryID).Warn("Failed to submit an outbound event delivery task")
			continue
		}
		if err := d.Store.SetDeliveryTaskID(ctx, head.DeliveryID, key, taskID); err != nil {
			logging.Log.WithError(err).Warn("Failed to record an outbound event delivery task")
		}
	}
	return nil
}

// ReapStale returns stuck deliveries to pending and discards their tasks. A
// stale task that runs later has an old key and completes with no send.
func (d *Dispatcher) ReapStale(ctx context.Context) {
	staleAfter := d.StaleAfter
	if staleAfter <= 0 {
		staleAfter = 10 * time.Minute
	}
	taskIDs, err := d.Store.ResetStaleDeliveries(ctx, d.now().Add(-staleAfter))
	if err != nil {
		logging.Log.WithError(err).Warn("Failed to reset stale outbound event deliveries")
		return
	}
	for _, id := range taskIDs {
		if err := d.Queue.Discard(ctx, d.queueName(), id); err != nil {
			logging.Log.WithError(err).WithField("task_id", id).Debug("Failed to discard a stale outbound event task")
		}
	}
}
