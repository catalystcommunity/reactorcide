// Package pubsub provides in-memory fan-out of job-related events and a
// PostgreSQL LISTEN/NOTIFY bridge so events cross coordinator replicas.
//
// Each replica listens to the global status channel. It listens to a job
// telemetry channel only while a local WebSocket watches that job. The local
// Bus then sends each received event to matching subscribers.
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
	// EventMetricsAvailable fires when a metric batch is durable in object
	// storage and is ready for an authorized range query.
	EventMetricsAvailable EventType = "metrics_available"

	// EventJobCreated fires when a job row is first persisted, so a list
	// view can insert it without polling. EventJobUpdate only fires on a
	// TRANSITION, so without this a newly submitted job stays invisible
	// until its first status change.
	EventJobCreated EventType = "job_created"
	// EventWorkflowCreated fires when a workflow instance is persisted.
	EventWorkflowCreated EventType = "workflow_created"
	// EventWorkflowUpdate fires on a workflow status transition.
	EventWorkflowUpdate EventType = "workflow_update"
	// EventWorkflowNodeUpdate fires when a node's status or decision changes,
	// which is what animates a DAG view while a workflow runs.
	EventWorkflowNodeUpdate EventType = "workflow_node_update"
	// EventProjectUpdate fires when project settings change, visibility
	// included. A project turning private must reach open list views: they
	// are showing rows the viewer may no longer see.
	EventProjectUpdate EventType = "project_update"
)

// Event is the unit of work on the bus. Not all fields are meaningful for
// every Type — only the ones relevant to the variant are populated.
//
// ProjectID and OwnerUserID exist so a stream can decide whether a subscriber
// may see a frame FROM THE FRAME, without a database round trip. The previous
// design had no such fields, so WSHandler.StreamAllJobs called GetJobByID
// inside its subscription filter — one query per event per subscriber — and
// the webapp's proxy made a whole GetJobMetrics RPC per event for the same
// reason. Both collapse to a field comparison once the frame carries its own
// owner.
//
// Keep this struct small. Every field is serialized into a pg_notify payload,
// which Postgres caps at 8000 bytes in the default build. Carry identifiers,
// never content.
type Event struct {
	Type      EventType `json:"type"`
	JobID     string    `json:"job_id"`
	Status    string    `json:"status,omitempty"`
	UpdatedAt string    `json:"updated_at,omitempty"`
	Stream    string    `json:"stream,omitempty"`
	Offset    int64     `json:"offset,omitempty"`
	Length    int64     `json:"length,omitempty"`
	From      string    `json:"from,omitempty"`
	To        string    `json:"to,omitempty"`
	Sequence  int64     `json:"sequence,omitempty"`

	// Authorization inputs. OwnerUserID is the owning ORG id (user_id is the
	// org id everywhere in this system); ProjectID is empty for a loose job or
	// a project-less workflow, which the visibility rule treats as owned
	// directly by the org.
	ProjectID   string `json:"project_id,omitempty"`
	OwnerUserID string `json:"owner_user_id,omitempty"`

	// Workflow addressing. WorkflowID is set on every workflow_* event and on
	// a job event belonging to a workflow, so one subscription to
	// "workflow:<id>" catches the workflow, its nodes and its jobs.
	WorkflowID string `json:"workflow_id,omitempty"`
	NodeID     string `json:"node_id,omitempty"`
	NodeName   string `json:"node_name,omitempty"`
}

// Subscription is the handle a caller holds onto while listening. Close
// the returned channel via Bus.Unsubscribe to free resources.
type Subscription struct {
	Ch     chan Event
	filter func(Event) bool
	jobID  string
}

type JobTopicController interface {
	AddJobTopic(jobID string)
	RemoveJobTopic(jobID string)
}

// Bus is the in-process fan-out. Safe for concurrent use.
type Bus struct {
	mu      sync.RWMutex
	subs    map[*Subscription]struct{}
	closed  bool
	logger  *logrus.Logger
	bufSize int
	topics  JobTopicController
}

// SetJobTopicController connects local subscriptions to cross-replica topic
// subscriptions. Set it before WebSocket handlers accept connections.
func (b *Bus) SetJobTopicController(controller JobTopicController) {
	b.mu.Lock()
	b.topics = controller
	b.mu.Unlock()
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

// SubscribeJob subscribes to one job and activates its cross-replica topic
// while at least one local subscriber needs it.
func (b *Bus) SubscribeJob(jobID string) *Subscription {
	sub := b.Subscribe(FilterByJobID(jobID))
	b.mu.Lock()
	sub.jobID = jobID
	controller := b.topics
	closed := b.closed
	b.mu.Unlock()
	if controller != nil && !closed {
		controller.AddJobTopic(jobID)
	}
	return sub
}

// RetainJobTopic activates one job's cross-replica topic for a subscriber that
// did NOT come through SubscribeJob, returning the release to call when it
// stops caring. The returned function is idempotent and always safe to call.
//
// This exists for the multiplexed UI stream: it holds ONE bus subscription and
// selects events by client-declared topics, so it cannot use SubscribeJob's
// one-subscription-per-job shape. Without this, high-volume per-job events
// (log_available, metrics_available — published on per-job NOTIFY channels, see
// pubsub.publishJob) are never LISTENed for on this replica, and the UI's live
// log tail silently receives nothing.
func (b *Bus) RetainJobTopic(jobID string) func() {
	noop := func() {}
	if b == nil || jobID == "" {
		return noop
	}
	b.mu.RLock()
	controller, closed := b.topics, b.closed
	b.mu.RUnlock()
	if controller == nil || closed {
		return noop
	}
	controller.AddJobTopic(jobID)
	var once sync.Once
	return func() { once.Do(func() { controller.RemoveJobTopic(jobID) }) }
}

// Unsubscribe removes sub from the bus and closes its channel. Idempotent.
func (b *Bus) Unsubscribe(sub *Subscription) {
	b.mu.Lock()
	if _, ok := b.subs[sub]; !ok {
		b.mu.Unlock()
		return
	}
	delete(b.subs, sub)
	close(sub.Ch)
	controller, jobID := b.topics, sub.jobID
	b.mu.Unlock()
	if controller != nil && jobID != "" {
		controller.RemoveJobTopic(jobID)
	}
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
