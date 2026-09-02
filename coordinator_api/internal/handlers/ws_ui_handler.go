package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/auth"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/authz"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/pubsub"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/gorilla/websocket"
	"github.com/sirupsen/logrus"
)

// The UI event stream.
//
// This replaces two things at once.
//
// The webapp used to proxy /api/v1/jobs/stream using its own SERVICE token and
// then drop frames the browser could not see by making a GetJobMetrics RPC PER
// EVENT. That is one round trip per event per connected browser, and it only
// ever covered jobs.
//
// The coordinator's own StreamAllJobs authorizes by calling GetJobByID inside
// its subscription filter — one database query per event per subscriber.
//
// Both disappear here. The connection resolves the caller's identity ONCE, and
// every frame is authorized from the fields the event already carries
// (pubsub.Event.ProjectID / OwnerUserID). The caller is the browser's own
// session, not a service token, so nothing is trusted from the client.

// UIStreamPath is where the webapp proxies the browser's socket to.
const UIStreamPath = "/api/v1/ui/stream"

// uiTopic is a subscription the client asked for. A topic never widens access:
// it narrows an already-authorized stream to the slice the page is showing.
type uiTopic string

const (
	// topicJobs is every job event the caller may see. The jobs list uses it.
	topicJobs uiTopic = "jobs"
	// topicWorkflows is every workflow event the caller may see.
	topicWorkflows uiTopic = "workflows"
	// topicProjects is every project event the caller may see. The project
	// LIST needs this: "project:<id>" only reaches a page that already knows
	// the id, so without a collection topic a list view can never learn that a
	// project's settings or visibility changed.
	topicProjects uiTopic = "projects"
	// topicWorkflowPrefix scopes to one workflow: its own events, its nodes'
	// events, and its jobs' events.
	topicWorkflowPrefix = "workflow:"
	// topicJobPrefix scopes to one job.
	topicJobPrefix = "job:"
	// topicProjectPrefix scopes to one project.
	topicProjectPrefix = "project:"
)

// maxTopicsPerConnection bounds how much a single socket can ask for. A UI
// showing a workflow with many jobs subscribes to the workflow, not to each
// job, so this is generous for real pages while still bounding a hostile one.
const maxTopicsPerConnection = 256

// uiClientMessage is the only thing a client may send.
type uiClientMessage struct {
	Subscribe   []string `json:"subscribe,omitempty"`
	Unsubscribe []string `json:"unsubscribe,omitempty"`
}

// UIStreamHandler serves the SPA's single multiplexed event socket.
type UIStreamHandler struct {
	bus      *pubsub.Bus
	store    store.Store
	sessions *auth.Sessions
	resolver *authz.Resolver
	logger   *logrus.Logger
	upgrader websocket.Upgrader
}

// NewUIStreamHandler constructs the handler. A store that does not satisfy
// authz.RoleStore leaves resolver nil, and the stream then refuses every
// connection rather than degrading to an unfiltered feed: this endpoint exists
// to enforce visibility, so a configuration that cannot enforce it must not
// serve.
func NewUIStreamHandler(bus *pubsub.Bus, s store.Store) *UIStreamHandler {
	h := &UIStreamHandler{
		bus:    bus,
		store:  s,
		logger: logrus.New(),
		upgrader: websocket.Upgrader{
			ReadBufferSize:  1024,
			WriteBufferSize: 1024,
			// The webapp reverse-proxies the browser's socket from its own
			// origin, so the coordinator only ever sees the webapp as peer.
			CheckOrigin: func(*http.Request) bool { return true },
		},
	}
	if roleStore, ok := s.(authz.RoleStore); ok {
		h.resolver = authz.NewResolver(roleStore)
	}
	// Session resolution is separate from role resolution: a store can satisfy
	// one and not the other, and an unchecked assertion here would panic at
	// construction rather than degrade. Without it every caller resolves as
	// anonymous, which is safe (public data only) rather than permissive.
	if sessionStore, ok := s.(auth.SessionStore); ok {
		h.sessions = auth.NewSessions(sessionStore)
	}
	return h
}

// Stream upgrades the connection and serves authorized events.
//
// Authentication is the browser's own UI SESSION token, presented as a bearer
// token by the webapp proxy. An unauthenticated connection is allowed and is
// treated as anonymous: the UI supports logged-out browsing of public data, and
// the visibility rule already answers "what can an anonymous caller see" —
// refusing the socket outright would break that page rather than protect
// anything.
func (h *UIStreamHandler) Stream(w http.ResponseWriter, r *http.Request) {
	if h.resolver == nil {
		http.Error(w, "event stream is not available on this server", http.StatusServiceUnavailable)
		return
	}

	identity := h.identityFor(r)

	// Resolve the caller's visibility ONCE, before the upgrade. Every frame is
	// then a field comparison against this snapshot.
	scope, err := h.newScope(r.Context(), identity)
	if err != nil {
		h.logger.WithError(err).Warn("UI stream: failed to resolve caller visibility")
		http.Error(w, "could not resolve permissions", http.StatusInternalServerError)
		return
	}

	ws, err := h.upgrader.Upgrade(w, r, nil)
	if err != nil {
		h.logger.WithError(err).Warn("UI stream: upgrade failed")
		return
	}
	defer ws.Close()

	// The topic set retains a cross-replica NOTIFY topic for every job the
	// client watches. Per-job events (log_available, metrics_available) are
	// published on per-job channels, so without this the listener never LISTENs
	// for them and a live log tail receives nothing.
	subscriptions := newTopicSet(h.bus.RetainJobTopic)
	defer subscriptions.releaseAll()

	sub := h.bus.Subscribe(func(evt pubsub.Event) bool {
		// Authorization first, ALWAYS. A topic filter must never be able to
		// admit a frame the caller cannot see, so it is only consulted after
		// the visibility check has already passed.
		if !scope.canSee(evt) {
			return false
		}
		return subscriptions.matches(evt)
	})
	defer h.bus.Unsubscribe(sub)

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	go h.readLoop(ctx, cancel, ws, subscriptions)
	h.writeLoop(ctx, ws, sub)
}

// identityFor resolves the bearer token as a UI SESSION, falling back to
// anonymous. API tokens are deliberately not accepted here: this endpoint
// exists for the browser, whose credential is a session, and accepting a
// service token would re-open the very hole this replaces.
func (h *UIStreamHandler) identityFor(r *http.Request) authz.Identity {
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if token == "" || h.sessions == nil {
		return authz.AnonymousIdentity()
	}
	user, _, err := h.sessions.ResolveSession(r.Context(), token)
	if err == nil && user != nil {
		return authz.IdentityFromUser(user)
	}
	return authz.AnonymousIdentity()
}

// readLoop consumes client messages. The client may only manage its topic set;
// there is no message that changes what it is allowed to see.
func (h *UIStreamHandler) readLoop(ctx context.Context, cancel context.CancelFunc, ws *websocket.Conn, topics *topicSet) {
	defer cancel()
	_ = ws.SetReadDeadline(time.Now().Add(wsPongTimeout))
	ws.SetPongHandler(func(string) error {
		return ws.SetReadDeadline(time.Now().Add(wsPongTimeout))
	})
	// Bound a single client message. Topic names are short.
	ws.SetReadLimit(64 * 1024)

	for {
		if ctx.Err() != nil {
			return
		}
		_, payload, err := ws.ReadMessage()
		if err != nil {
			return
		}
		var msg uiClientMessage
		if json.Unmarshal(payload, &msg) != nil {
			// A malformed frame is ignored rather than fatal: a client bug
			// should not drop a working stream.
			continue
		}
		topics.apply(msg)
	}
}

func (h *UIStreamHandler) writeLoop(ctx context.Context, ws *websocket.Conn, sub *pubsub.Subscription) {
	pingTicker := time.NewTicker(wsPingPeriod)
	defer pingTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case evt, ok := <-sub.Ch:
			if !ok {
				return
			}
			payload, err := pubsub.EncodeEvent(evt)
			if err != nil {
				h.logger.WithError(err).Warn("UI stream: failed to serialize event")
				continue
			}
			_ = ws.SetWriteDeadline(time.Now().Add(wsWriteWait))
			if err := ws.WriteMessage(websocket.TextMessage, payload); err != nil {
				return
			}
		case <-pingTicker.C:
			_ = ws.SetWriteDeadline(time.Now().Add(wsWriteWait))
			if err := ws.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

// topicSet is the client's current subscriptions. It is read from the bus's
// publish goroutine and written from the socket's read goroutine, so it is
// mutex-guarded.
type topicSet struct {
	mu     sync.RWMutex
	topics map[uiTopic]struct{}

	// retainJob activates a job's cross-replica NOTIFY topic and returns the
	// release. jobReleases holds one release per subscribed "job:<id>" topic,
	// so unsubscribing (or closing the socket) hands the topic back and the
	// listener can UNLISTEN once nothing wants it.
	retainJob   func(jobID string) func()
	jobReleases map[uiTopic]func()
}

func newTopicSet(retainJob func(jobID string) func()) *topicSet {
	if retainJob == nil {
		retainJob = func(string) func() { return func() {} }
	}
	return &topicSet{
		topics:      make(map[uiTopic]struct{}),
		retainJob:   retainJob,
		jobReleases: make(map[uiTopic]func()),
	}
}

func (t *topicSet) apply(msg uiClientMessage) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, name := range msg.Unsubscribe {
		topic := uiTopic(name)
		delete(t.topics, topic)
		if release, ok := t.jobReleases[topic]; ok {
			release()
			delete(t.jobReleases, topic)
		}
	}
	for _, name := range msg.Subscribe {
		if len(t.topics) >= maxTopicsPerConnection {
			return
		}
		if name == "" || len(name) > 128 {
			continue
		}
		topic := uiTopic(name)
		if _, already := t.topics[topic]; already {
			continue
		}
		t.topics[topic] = struct{}{}
		// A reconnect re-declares the whole set, so guard against retaining the
		// same job twice on one connection.
		if jobID := strings.TrimPrefix(name, topicJobPrefix); jobID != name && jobID != "" {
			t.jobReleases[topic] = t.retainJob(jobID)
		}
	}
}

// releaseAll hands back every retained job topic. Called when the socket ends.
func (t *topicSet) releaseAll() {
	t.mu.Lock()
	defer t.mu.Unlock()
	for topic, release := range t.jobReleases {
		release()
		delete(t.jobReleases, topic)
	}
}

// matches reports whether any subscribed topic wants this event. A connection
// with no topics receives nothing: a page that has not said what it is showing
// does not need a firehose.
func (t *topicSet) matches(evt pubsub.Event) bool {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if len(t.topics) == 0 {
		return false
	}
	for topic := range t.topics {
		if topicMatches(topic, evt) {
			return true
		}
	}
	return false
}

func topicMatches(topic uiTopic, evt pubsub.Event) bool {
	name := string(topic)
	switch {
	case topic == topicJobs:
		return isJobEvent(evt.Type)
	case topic == topicWorkflows:
		return isWorkflowEvent(evt.Type)
	case topic == topicProjects:
		return evt.Type == pubsub.EventProjectUpdate
	case strings.HasPrefix(name, topicWorkflowPrefix):
		// A workflow topic covers the workflow, its nodes, and its jobs, so a
		// detail page needs one subscription rather than one per node.
		return evt.WorkflowID != "" && evt.WorkflowID == strings.TrimPrefix(name, topicWorkflowPrefix)
	case strings.HasPrefix(name, topicJobPrefix):
		return evt.JobID != "" && evt.JobID == strings.TrimPrefix(name, topicJobPrefix)
	case strings.HasPrefix(name, topicProjectPrefix):
		return evt.ProjectID != "" && evt.ProjectID == strings.TrimPrefix(name, topicProjectPrefix)
	default:
		return false
	}
}

func isJobEvent(t pubsub.EventType) bool {
	switch t {
	case pubsub.EventJobUpdate, pubsub.EventJobCreated,
		pubsub.EventLogAvailable, pubsub.EventMetricsAvailable:
		return true
	}
	return false
}

func isWorkflowEvent(t pubsub.EventType) bool {
	switch t {
	case pubsub.EventWorkflowCreated, pubsub.EventWorkflowUpdate, pubsub.EventWorkflowNodeUpdate:
		return true
	}
	return false
}
