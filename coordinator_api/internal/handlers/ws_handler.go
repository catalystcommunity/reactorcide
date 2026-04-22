package handlers

import (
	"context"
	"net/http"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/pubsub"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/gorilla/websocket"
	"github.com/sirupsen/logrus"
)

// WSHandler serves WebSocket streams that let clients watch job status
// and logs live without polling.
type WSHandler struct {
	bus      *pubsub.Bus
	store    store.Store
	logger   *logrus.Logger
	upgrader websocket.Upgrader
}

// NewWSHandler constructs a WSHandler. The upgrader accepts any origin
// because the webapp reverse-proxies browser WS connections via its own
// origin — the coordinator only receives requests from the webapp.
func NewWSHandler(bus *pubsub.Bus, s store.Store) *WSHandler {
	return &WSHandler{
		bus:    bus,
		store:  s,
		logger: logrus.New(),
		upgrader: websocket.Upgrader{
			ReadBufferSize:  1024,
			WriteBufferSize: 1024,
			CheckOrigin: func(r *http.Request) bool {
				return true
			},
		},
	}
}

// WS heartbeat tuning. Chosen so a dead peer is detected within a minute
// without flooding idle connections with pings.
const (
	wsPingPeriod  = 30 * time.Second
	wsPongTimeout = 45 * time.Second
	wsWriteWait   = 10 * time.Second
)

// StreamAllJobs upgrades to WebSocket and sends every job-status event to
// the client. No initial snapshot — the caller is expected to have fetched
// the list via REST first and then uses this stream for updates.
func (h *WSHandler) StreamAllJobs(w http.ResponseWriter, r *http.Request) {
	ws, err := h.upgrader.Upgrade(w, r, nil)
	if err != nil {
		h.logger.WithError(err).Warn("WS upgrade failed")
		return
	}
	defer ws.Close()

	// Listen for all job_update events (no per-job filter).
	sub := h.bus.Subscribe(func(evt pubsub.Event) bool {
		return evt.Type == pubsub.EventJobUpdate
	})
	defer h.bus.Unsubscribe(sub)

	h.runStream(r.Context(), ws, sub, "")
}

// StreamJob streams events for a single job to the client. The first
// message sent is the job's current state so the browser can render
// without a separate REST round-trip.
func (h *WSHandler) StreamJob(w http.ResponseWriter, r *http.Request) {
	jobID := GetIDFromContext(r, "job_id")
	if jobID == "" {
		http.Error(w, "missing job id", http.StatusBadRequest)
		return
	}

	ws, err := h.upgrader.Upgrade(w, r, nil)
	if err != nil {
		h.logger.WithError(err).Warn("WS upgrade failed")
		return
	}
	defer ws.Close()

	sub := h.bus.Subscribe(pubsub.FilterByJobID(jobID))
	defer h.bus.Unsubscribe(sub)

	// Send current status as the initial frame. Failures here are non-fatal
	// — the client still gets subsequent live events.
	if job, err := h.store.GetJobByID(r.Context(), jobID); err == nil && job != nil {
		initial := pubsub.Event{
			Type:      pubsub.EventJobUpdate,
			JobID:     job.JobID,
			Status:    job.Status,
			UpdatedAt: job.UpdatedAt.UTC().Format(time.RFC3339Nano),
		}
		if payload, err := pubsub.EncodeEvent(initial); err == nil {
			ws.SetWriteDeadline(time.Now().Add(wsWriteWait))
			_ = ws.WriteMessage(websocket.TextMessage, payload)
		}
	}

	h.runStream(r.Context(), ws, sub, jobID)
}

// runStream drives the read/write loops until either the client closes the
// connection, ctx is canceled, or the subscriber channel is closed by
// Bus.Close on shutdown.
func (h *WSHandler) runStream(ctx context.Context, ws *websocket.Conn, sub *pubsub.Subscription, jobID string) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Read loop: the client is not expected to send anything, but we need
	// to be reading so ping/pong works. Cancels the outer context on any
	// read failure (close, pong timeout, etc.).
	go func() {
		defer cancel()
		ws.SetReadDeadline(time.Now().Add(wsPongTimeout))
		ws.SetPongHandler(func(string) error {
			ws.SetReadDeadline(time.Now().Add(wsPongTimeout))
			return nil
		})
		for {
			if _, _, err := ws.ReadMessage(); err != nil {
				return
			}
		}
	}()

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
			// log_available events are sent through as a ping; the client
			// refreshes logs via REST when it receives one. Cleaner than
			// streaming bytes while log storage still rewrites full files.
			payload, err := pubsub.EncodeEvent(evt)
			if err != nil {
				h.logger.WithError(err).Warn("Failed to serialize WS event")
				continue
			}
			ws.SetWriteDeadline(time.Now().Add(wsWriteWait))
			if err := ws.WriteMessage(websocket.TextMessage, payload); err != nil {
				return
			}
		case <-pingTicker.C:
			ws.SetWriteDeadline(time.Now().Add(wsWriteWait))
			if err := ws.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

