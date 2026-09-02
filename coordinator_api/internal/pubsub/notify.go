package pubsub

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v4/pgxpool"
	"github.com/sirupsen/logrus"
)

// NotifyChannel carries global job-status events. High-volume telemetry
// availability events use per-job channels.
const NotifyChannel = "reactorcide_events"

// JobNotifyChannel returns a fixed-size PostgreSQL channel for one job.
func JobNotifyChannel(jobID string) string {
	digest := sha256.Sum256([]byte(jobID))
	return fmt.Sprintf("reactorcide_job_%x", digest[:16])
}

// Publish emits evt via Postgres pg_notify so every replica's LISTEN picks
// it up. Safe to call from any context that has access to the pool.
func Publish(ctx context.Context, pool *pgxpool.Pool, evt Event) error {
	return publishChannel(ctx, pool, NotifyChannel, evt)
}

func publishJob(ctx context.Context, pool *pgxpool.Pool, jobID string, evt Event) error {
	return publishChannel(ctx, pool, JobNotifyChannel(jobID), evt)
}

func publishChannel(ctx context.Context, pool *pgxpool.Pool, channel string, evt Event) error {
	payload, err := EncodeEvent(evt)
	if err != nil {
		return fmt.Errorf("encoding event: %w", err)
	}
	// pg_notify's payload is limited to 8000 bytes in the default build.
	// Our events are well under that — log chunks carry only offset/length,
	// not bytes.
	if _, err := pool.Exec(ctx, "SELECT pg_notify($1, $2)", channel, string(payload)); err != nil {
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

// JobRef carries the identity and ownership of a job an event is about.
//
// ProjectID and OwnerUserID are what let a stream decide whether a subscriber
// may see the frame without a database lookup. Populate them: an event that
// carries neither forces the consumer into a per-job query, and the UI stream
// treats an event it cannot authorize as invisible rather than public.
type JobRef struct {
	JobID       string
	Status      string
	UpdatedAt   string
	ProjectID   string
	OwnerUserID string
	WorkflowID  string
}

func (r JobRef) event(t EventType) Event {
	return Event{
		Type:        t,
		JobID:       r.JobID,
		Status:      r.Status,
		UpdatedAt:   r.UpdatedAt,
		ProjectID:   r.ProjectID,
		OwnerUserID: r.OwnerUserID,
		WorkflowID:  r.WorkflowID,
	}
}

// PublishJobUpdate emits a job-status event. Errors are swallowed (logged
// by the listener side on delivery failures) because a failed NOTIFY should
// never block a job transition.
func (p *Publisher) PublishJobUpdate(ctx context.Context, ref JobRef) {
	if p == nil || p.pool == nil {
		return
	}
	_ = Publish(ctx, p.pool, ref.event(EventJobUpdate))
}

// PublishJobCreated emits a job-created event so a list view can insert a row
// the moment it is submitted.
//
// This is separate from PublishJobUpdate because that one fires on a
// TRANSITION. Without a created event, a freshly submitted job is invisible
// until its status first changes, which is exactly the "workflows do not appear
// until I reload" complaint.
func (p *Publisher) PublishJobCreated(ctx context.Context, ref JobRef) {
	if p == nil || p.pool == nil {
		return
	}
	_ = Publish(ctx, p.pool, ref.event(EventJobCreated))
}

// WorkflowRef carries the identity and ownership of a workflow an event is
// about. See JobRef for why the ownership fields matter.
type WorkflowRef struct {
	WorkflowID  string
	Status      string
	UpdatedAt   string
	ProjectID   string
	OwnerUserID string
}

func (r WorkflowRef) event(t EventType) Event {
	return Event{
		Type:        t,
		WorkflowID:  r.WorkflowID,
		Status:      r.Status,
		UpdatedAt:   r.UpdatedAt,
		ProjectID:   r.ProjectID,
		OwnerUserID: r.OwnerUserID,
	}
}

// PublishWorkflowCreated emits a workflow-created event.
func (p *Publisher) PublishWorkflowCreated(ctx context.Context, ref WorkflowRef) {
	if p == nil || p.pool == nil {
		return
	}
	_ = Publish(ctx, p.pool, ref.event(EventWorkflowCreated))
}

// PublishWorkflowUpdate emits a workflow status transition.
func (p *Publisher) PublishWorkflowUpdate(ctx context.Context, ref WorkflowRef) {
	if p == nil || p.pool == nil {
		return
	}
	_ = Publish(ctx, p.pool, ref.event(EventWorkflowUpdate))
}

// PublishWorkflowNodeUpdate emits a node transition. This is what animates a
// DAG view while a workflow runs.
func (p *Publisher) PublishWorkflowNodeUpdate(ctx context.Context, ref WorkflowRef, nodeID, nodeName, status string) {
	if p == nil || p.pool == nil {
		return
	}
	evt := ref.event(EventWorkflowNodeUpdate)
	evt.NodeID = nodeID
	evt.NodeName = nodeName
	evt.Status = status
	_ = Publish(ctx, p.pool, evt)
}

// PublishProjectUpdate emits a project settings change, visibility included.
//
// A stream that has cached "this caller may see project X" must drop that
// answer when this arrives: the project may have just become private, and an
// already-open socket would otherwise keep feeding it.
func (p *Publisher) PublishProjectUpdate(ctx context.Context, projectID, ownerUserID string) {
	if p == nil || p.pool == nil {
		return
	}
	_ = Publish(ctx, p.pool, Event{
		Type:        EventProjectUpdate,
		ProjectID:   projectID,
		OwnerUserID: ownerUserID,
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
	_ = publishJob(ctx, p.pool, jobID, Event{
		Type:   EventLogAvailable,
		JobID:  jobID,
		Stream: stream,
		Offset: offset,
		Length: length,
	})
}

// PublishMetricsAvailable signals that a metric range is durable. The event
// carries no metric values. Authorized clients fetch the range separately.
func (p *Publisher) PublishMetricsAvailable(ctx context.Context, jobID, from, to string, sequence int64) {
	if p == nil || p.pool == nil {
		return
	}
	_ = publishJob(ctx, p.pool, jobID, Event{
		Type:     EventMetricsAvailable,
		JobID:    jobID,
		From:     from,
		To:       to,
		Sequence: sequence,
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
	mu     sync.Mutex
	jobs   map[string]int
}

// NewNotifyListener constructs a listener. Call Start to run it.
func NewNotifyListener(pool *pgxpool.Pool, bus *Bus, logger *logrus.Logger) *NotifyListener {
	if logger == nil {
		logger = logrus.New()
	}
	return &NotifyListener{pool: pool, bus: bus, logger: logger, jobs: map[string]int{}}
}

func (l *NotifyListener) AddJobTopic(jobID string) {
	if jobID == "" {
		return
	}
	l.mu.Lock()
	l.jobs[jobID]++
	l.mu.Unlock()
}

func (l *NotifyListener) RemoveJobTopic(jobID string) {
	l.mu.Lock()
	if l.jobs[jobID] <= 1 {
		delete(l.jobs, jobID)
	} else {
		l.jobs[jobID]--
	}
	l.mu.Unlock()
}

func (l *NotifyListener) desiredJobChannels() map[string]bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	channels := make(map[string]bool, len(l.jobs))
	for jobID := range l.jobs {
		channels[JobNotifyChannel(jobID)] = true
	}
	return channels
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

	listened := map[string]bool{}
	for {
		desired := l.desiredJobChannels()
		for channel := range desired {
			if !listened[channel] {
				if _, err := conn.Exec(ctx, "LISTEN "+channel); err != nil {
					return fmt.Errorf("listen on job channel: %w", err)
				}
				listened[channel] = true
			}
		}
		for channel := range listened {
			if !desired[channel] {
				if _, err := conn.Exec(ctx, "UNLISTEN "+channel); err != nil {
					return fmt.Errorf("unlisten from job channel: %w", err)
				}
				delete(listened, channel)
			}
		}

		waitCtx, cancel := context.WithTimeout(ctx, time.Second)
		notification, err := conn.Conn().WaitForNotification(waitCtx)
		cancel()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			if errors.Is(err, context.DeadlineExceeded) {
				continue
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

// defaultPublisher is the process-wide publisher the workflow lifecycle uses.
//
// Why a package-level value rather than an injected dependency: workflow
// transitions happen in internal/jobcontrol and internal/worker, reached
// through half a dozen consumer-defined narrow store interfaces. Threading a
// Publisher to each transition point would widen every one of those interfaces
// for a side effect none of them is about. This mirrors the existing
// handlers.SetPubSubBus singleton, and it is safe by construction: Default
// returns nil until wired, and every Publisher method is a no-op on a nil
// receiver, so an unwired process silently publishes nothing rather than
// panicking.
//
// Prefer an injected *Publisher where one is already threaded (workerapi has
// one on its Deps). Use this only where injection would mean widening an
// unrelated interface.
var defaultPublisher atomic.Pointer[Publisher]

// SetDefaultPublisher wires the process-wide publisher. Call once at startup,
// alongside the bus.
func SetDefaultPublisher(p *Publisher) {
	defaultPublisher.Store(p)
}

// Default returns the process-wide publisher, or nil if none was wired. The
// result is always safe to call methods on.
func Default() *Publisher {
	return defaultPublisher.Load()
}
