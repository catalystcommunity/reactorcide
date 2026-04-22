// Package pubsub provides in-memory fan-out of job-related events and a
// Postgres LISTEN/NOTIFY bridge so events cross coordinator replicas.
//
// Publishers emit Events via NOTIFY (see notify.go). A dedicated
// NotifyListener goroutine on each replica LISTENs and forwards every
// received event to the local Bus. WebSocket handlers subscribe to the
// local Bus with a filter; Bus fans out to all matching subscribers.
//
// This split means a single-replica deployment has one listener and one
// bus; multi-replica deployments have N listeners each feeding its own
// bus, and every replica sees every event uniformly.
package pubsub

import (
	"encoding/json"
	"sync"

	"github.com/sirupsen/logrus"
)

// EventType discriminates the payload variants an Event can carry.
type EventType string

const (
	// EventJobUpdate fires on any persisted transition of a job's status.
	EventJobUpdate EventType = "job_update"
	// EventLogAvailable fires when a new log chunk has been flushed to
	// object storage and is ready to be read.
	EventLogAvailable EventType = "log_available"
)

// Event is the unit of work on the bus. Not all fields are meaningful for
// every Type — only the ones relevant to the variant are populated.
type Event struct {
	Type      EventType `json:"type"`
	JobID     string    `json:"job_id"`
	Status    string    `json:"status,omitempty"`
	UpdatedAt string    `json:"updated_at,omitempty"`
	Stream    string    `json:"stream,omitempty"`
	Offset    int64     `json:"offset,omitempty"`
	Length    int64     `json:"length,omitempty"`
}

// Subscription is the handle a caller holds onto while listening. Close
// the returned channel via Bus.Unsubscribe to free resources.
type Subscription struct {
	Ch     chan Event
	filter func(Event) bool
}

// Bus is the in-process fan-out. Safe for concurrent use.
type Bus struct {
	mu      sync.RWMutex
	subs    map[*Subscription]struct{}
	closed  bool
	logger  *logrus.Logger
	bufSize int
}

// NewBus constructs a bus with the given per-subscriber buffer size.
// When a subscriber's channel is full, events for that subscriber are
// dropped (with a logged warning) rather than blocking the publisher.
func NewBus(logger *logrus.Logger, bufSize int) *Bus {
	if bufSize <= 0 {
		bufSize = 64
	}
	if logger == nil {
		logger = logrus.New()
	}
	return &Bus{
		subs:    make(map[*Subscription]struct{}),
		logger:  logger,
		bufSize: bufSize,
	}
}

// Subscribe returns a Subscription whose Ch emits events matching filter.
// A nil filter matches everything.
func (b *Bus) Subscribe(filter func(Event) bool) *Subscription {
	sub := &Subscription{
		Ch:     make(chan Event, b.bufSize),
		filter: filter,
	}

	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		close(sub.Ch)
		return sub
	}
	b.subs[sub] = struct{}{}
	return sub
}

// Unsubscribe removes sub from the bus and closes its channel. Idempotent.
func (b *Bus) Unsubscribe(sub *Subscription) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, ok := b.subs[sub]; !ok {
		return
	}
	delete(b.subs, sub)
	close(sub.Ch)
}

// Publish sends evt to every matching subscriber non-blockingly. Drops
// (with a log line) when a subscriber's buffer is full rather than stalling
// the publisher — slow consumers shouldn't hold up the event stream.
func (b *Bus) Publish(evt Event) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	if b.closed {
		return
	}
	for sub := range b.subs {
		if sub.filter != nil && !sub.filter(evt) {
			continue
		}
		select {
		case sub.Ch <- evt:
		default:
			b.logger.WithField("job_id", evt.JobID).Warn("WebSocket subscriber buffer full; dropping event")
		}
	}
}

// Close shuts the bus down and closes every subscriber channel. After
// Close, Publish is a no-op and Subscribe returns an already-closed channel.
func (b *Bus) Close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return
	}
	b.closed = true
	for sub := range b.subs {
		close(sub.Ch)
	}
	b.subs = nil
}

// EncodeEvent serializes an event to JSON for transport over NOTIFY or
// WebSocket. Exposed so callers can share the same wire format.
func EncodeEvent(evt Event) ([]byte, error) {
	return json.Marshal(evt)
}

// DecodeEvent parses a JSON payload back into an Event.
func DecodeEvent(payload []byte) (Event, error) {
	var evt Event
	err := json.Unmarshal(payload, &evt)
	return evt, err
}

// FilterByJobID returns a subscription filter that only matches events
// for the given job id.
func FilterByJobID(jobID string) func(Event) bool {
	return func(e Event) bool { return e.JobID == jobID }
}
