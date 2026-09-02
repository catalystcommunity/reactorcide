package handlers

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/catalystcommunity/reactorcide/webapp/internal/config"
	"github.com/catalystcommunity/reactorcide/webapp/internal/transportsecurity"
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

// UIStream proxies the SPA's single multiplexed event socket to the
// coordinator's /api/v1/ui/stream.
//
// The credential forwarded upstream is the BROWSER'S OWN session token, read
// from the HttpOnly cookie — not this process's service token. That is the
// whole point: the coordinator resolves the real caller and filters every frame
// against what that caller may see, so the webapp is a pipe rather than an
// authorization decision.
//
// It replaces a proxy that forwarded the service token and then tried to drop
// unauthorized frames on this side, by making one GetJobMetrics RPC PER EVENT.
// That was a round trip per event per browser, and it covered only jobs.
func (p *WSProxy) UIStream(w http.ResponseWriter, r *http.Request) {
	p.proxy(w, r, upstreamWSURL(coordinatorUIStreamPath), sessionAuthHeader(r))
}

// coordinatorUIStreamPath mirrors coordinator handlers.UIStreamPath.
const coordinatorUIStreamPath = "/api/v1/ui/stream"

// sessionAuthHeader builds the upstream handshake header carrying the browser's
// own session token. A logged-out browser gets no Authorization header at all,
// and the coordinator treats that as an anonymous caller who may watch public
// data — not as an error.
func sessionAuthHeader(r *http.Request) http.Header {
	header := http.Header{}
	if token, ok := sessionTokenFromRequest(r); ok {
		header.Set("Authorization", "Bearer "+token)
	}
	return header
}

// proxy accepts the browser upgrade, dials the coordinator with the supplied
// handshake header, and copies frames both directions until either side closes.
// Terminates the matching half when its peer goes away.
func (p *WSProxy) proxy(w http.ResponseWriter, r *http.Request, upstream string, header http.Header) {
	if err := transportsecurity.ValidateURL(config.APIUrl, config.AllowInsecureTransport, "web coordinator connection"); err != nil {
		http.Error(w, "coordinator transport is not secure", http.StatusBadGateway)
		return
	}

	upstreamConn, resp, err := p.dialer.DialContext(r.Context(), upstream, header)
	if err != nil {
		p.logger.WithError(err).WithField("upstream", upstream).Warn("Upstream WS dial failed")
		if resp != nil {
			resp.Body.Close()
		}
		http.Error(w, "upstream unavailable", http.StatusBadGateway)
		return
	}
	defer upstreamConn.Close()

	// Connect upstream before the browser upgrade. If the coordinator rejects
	// the request, the browser receives an HTTP failure and keeps its retry
	// backoff. It must not see a successful upgrade for a failed upstream.
	clientConn, err := p.upgrader.Upgrade(w, r, nil)
	if err != nil {
		p.logger.WithError(err).Warn("Browser WS upgrade failed")
		return
	}
	defer clientConn.Close()

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	// Both directions are now plain copies. Frames arriving from the
	// coordinator have ALREADY been filtered against the caller's identity
	// upstream, so this side no longer inspects them — the previous
	// per-frame filter here existed only because the upstream connection was
	// authenticated as a service rather than as the user.
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
