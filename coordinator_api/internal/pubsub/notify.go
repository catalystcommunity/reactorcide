package pubsub

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v4/pgxpool"
	"github.com/sirupsen/logrus"
)

// NotifyChannel is the Postgres NOTIFY channel name all Reactorcide events
// flow through. Single channel, many event types — discriminated by the
// Event.Type JSON field.
const NotifyChannel = "reactorcide_events"

// Publish emits evt via Postgres pg_notify so every replica's LISTEN picks
// it up. Safe to call from any context that has access to the pool.
func Publish(ctx context.Context, pool *pgxpool.Pool, evt Event) error {
	payload, err := EncodeEvent(evt)
	if err != nil {
		return fmt.Errorf("encoding event: %w", err)
	}
	// pg_notify's payload is limited to 8000 bytes in the default build.
	// Our events are well under that — log chunks carry only offset/length,
	// not bytes.
	if _, err := pool.Exec(ctx, "SELECT pg_notify($1, $2)", NotifyChannel, string(payload)); err != nil {
		return fmt.Errorf("pg_notify: %w", err)
	}
	return nil
}

// Publisher is a thin handle that workers and log shippers hold to emit
// events without having to know about pgxpool directly. Nil-safe — a nil
// Publisher silently drops publishes (useful for tests and single-replica
// deployments that don't wire up NOTIFY).
type Publisher struct {
	pool *pgxpool.Pool
}

// NewPublisher wraps a pool. Pass nil to disable publishing.
func NewPublisher(pool *pgxpool.Pool) *Publisher {
	return &Publisher{pool: pool}
}

// PublishJobUpdate emits a job-status event. Errors are swallowed (logged
// by the listener side on delivery failures) because a failed NOTIFY should
// never block a job transition.
func (p *Publisher) PublishJobUpdate(ctx context.Context, jobID, status, updatedAt string) {
	if p == nil || p.pool == nil {
		return
	}
	_ = Publish(ctx, p.pool, Event{
		Type:      EventJobUpdate,
		JobID:     jobID,
		Status:    status,
		UpdatedAt: updatedAt,
	})
}

// PublishLogAvailable signals that a new log chunk has been flushed for
// a job. Clients receiving this are expected to pull the fresh log via
// REST; the payload itself doesn't carry the bytes (see Publish note on
// NOTIFY size limits).
func (p *Publisher) PublishLogAvailable(ctx context.Context, jobID, stream string, offset, length int64) {
	if p == nil || p.pool == nil {
		return
	}
	_ = Publish(ctx, p.pool, Event{
		Type:   EventLogAvailable,
		JobID:  jobID,
		Stream: stream,
		Offset: offset,
		Length: length,
	})
}

// NotifyListener holds a dedicated Postgres connection that LISTENs on
// NotifyChannel and forwards every notification into the local Bus.
//
// Start launches the listener goroutine. It self-reconnects with backoff
// if the connection drops. Stopping happens via ctx cancel.
type NotifyListener struct {
	pool   *pgxpool.Pool
	bus    *Bus
	logger *logrus.Logger
}

// NewNotifyListener constructs a listener. Call Start to run it.
func NewNotifyListener(pool *pgxpool.Pool, bus *Bus, logger *logrus.Logger) *NotifyListener {
	if logger == nil {
		logger = logrus.New()
	}
	return &NotifyListener{pool: pool, bus: bus, logger: logger}
}

// Start runs the listen loop in a goroutine. It returns immediately;
// the loop exits when ctx is canceled.
func (l *NotifyListener) Start(ctx context.Context) {
	go l.loop(ctx)
}

func (l *NotifyListener) loop(ctx context.Context) {
	backoff := time.Second
	const maxBackoff = 30 * time.Second

	for {
		if err := ctx.Err(); err != nil {
			return
		}

		if err := l.runOnce(ctx); err != nil {
			l.logger.WithError(err).Warn("NotifyListener disconnected; reconnecting")
			select {
			case <-time.After(backoff):
			case <-ctx.Done():
				return
			}
			backoff *= 2
			if backoff > maxBackoff {
				backoff = maxBackoff
			}
			continue
		}
		backoff = time.Second
	}
}

// runOnce acquires a conn, subscribes, and blocks until the conn dies or
// ctx is canceled. A nil return means clean shutdown via ctx; any other
// return means we should reconnect.
func (l *NotifyListener) runOnce(ctx context.Context) error {
	conn, err := l.pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquiring pool conn: %w", err)
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, "LISTEN "+NotifyChannel); err != nil {
		return fmt.Errorf("LISTEN: %w", err)
	}

	l.logger.WithField("channel", NotifyChannel).Info("NotifyListener subscribed")

	for {
		notification, err := conn.Conn().WaitForNotification(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("waiting for notification: %w", err)
		}

		evt, err := DecodeEvent([]byte(notification.Payload))
		if err != nil {
			l.logger.WithError(err).WithField("payload", notification.Payload).Warn("Unparseable NOTIFY payload; dropping")
			continue
		}
		l.bus.Publish(evt)
	}
}
