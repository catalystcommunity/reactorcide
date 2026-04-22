package handlers

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/catalystcommunity/reactorcide/webapp/internal/config"
	"github.com/gorilla/websocket"
	"github.com/sirupsen/logrus"
)

// WSProxy is a minimal WebSocket proxy: browser ↔ webapp ↔ coordinator_api.
// Browsers never see the coordinator's service token — the webapp injects
// it on the upstream handshake — and they never see any coordinator origin
// directly, which sidesteps CORS.
type WSProxy struct {
	upgrader websocket.Upgrader
	dialer   *websocket.Dialer
	logger   *logrus.Logger
}

// NewWSProxy constructs a proxy. CheckOrigin is permissive because
// authentication happens at the upstream handshake via the bearer token,
// not via origin.
func NewWSProxy() *WSProxy {
	return &WSProxy{
		upgrader: websocket.Upgrader{
			ReadBufferSize:  1024,
			WriteBufferSize: 1024,
			CheckOrigin:     func(r *http.Request) bool { return true },
		},
		dialer: &websocket.Dialer{HandshakeTimeout: 15 * time.Second},
		logger: logrus.New(),
	}
}

// AllJobsStream proxies /app/ws/jobs → coordinator /api/v1/jobs/stream.
func (p *WSProxy) AllJobsStream(w http.ResponseWriter, r *http.Request) {
	p.proxy(w, r, upstreamWSURL("/api/v1/jobs/stream"))
}

// JobStream proxies /app/ws/jobs/{id} → coordinator /api/v1/jobs/stream/{id}.
func (p *WSProxy) JobStream(w http.ResponseWriter, r *http.Request) {
	jobID := r.PathValue("id")
	if jobID == "" {
		http.Error(w, "missing job id", http.StatusBadRequest)
		return
	}
	p.proxy(w, r, upstreamWSURL("/api/v1/jobs/stream/"+url.PathEscape(jobID)))
}

// proxy accepts the browser upgrade, dials the coordinator with the
// service token, and copies frames both directions until either side
// closes. Terminates the matching half when its peer goes away.
func (p *WSProxy) proxy(w http.ResponseWriter, r *http.Request, upstream string) {
	clientConn, err := p.upgrader.Upgrade(w, r, nil)
	if err != nil {
		p.logger.WithError(err).Warn("Browser WS upgrade failed")
		return
	}
	defer clientConn.Close()

	header := http.Header{}
	if config.APIToken != "" {
		header.Set("Authorization", "Bearer "+config.APIToken)
	}

	upstreamConn, resp, err := p.dialer.DialContext(r.Context(), upstream, header)
	if err != nil {
		p.logger.WithError(err).WithField("upstream", upstream).Warn("Upstream WS dial failed")
		if resp != nil {
			resp.Body.Close()
		}
		clientConn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseInternalServerErr, "upstream unavailable"))
		return
	}
	defer upstreamConn.Close()

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	var wg sync.WaitGroup
	wg.Add(2)
	go proxyFrames(ctx, cancel, &wg, clientConn, upstreamConn)
	go proxyFrames(ctx, cancel, &wg, upstreamConn, clientConn)
	wg.Wait()
}

// proxyFrames copies messages from src to dst. When src errors (close,
// timeout, network drop), we cancel the outer context so the other
// direction unblocks and exits too.
func proxyFrames(ctx context.Context, cancel context.CancelFunc, wg *sync.WaitGroup, src, dst *websocket.Conn) {
	defer wg.Done()
	defer cancel()

	for {
		if ctx.Err() != nil {
			return
		}
		msgType, msg, err := src.ReadMessage()
		if err != nil {
			return
		}
		if err := dst.WriteMessage(msgType, msg); err != nil {
			return
		}
	}
}

// upstreamWSURL turns an http(s) API base URL into a ws(s) URL for the
// coordinator's WebSocket endpoints.
func upstreamWSURL(path string) string {
	base := strings.TrimSuffix(config.APIUrl, "/")
	switch {
	case strings.HasPrefix(base, "https://"):
		return "wss://" + strings.TrimPrefix(base, "https://") + path
	case strings.HasPrefix(base, "http://"):
		return "ws://" + strings.TrimPrefix(base, "http://") + path
	default:
		return "ws://" + base + path
	}
}
