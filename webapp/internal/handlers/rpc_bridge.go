package handlers

import (
	"bytes"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	rpctransport "github.com/catalystcommunity/csilgen/transports/go"
	"github.com/catalystcommunity/reactorcide/webapp/internal/config"
	"github.com/catalystcommunity/reactorcide/webapp/internal/transportsecurity"
	"github.com/sirupsen/logrus"
)

// The browser-facing CSIL-RPC bridge.
//
// The SPA holds no credential of any kind. Its session lives in the HttpOnly
// rc_session cookie, which page JavaScript cannot read, and the coordinator's
// own service token never leaves this process. The bridge is what connects the
// two: it takes the envelope the generated TypeScript client produced, replaces
// its credential with the one bound to this browser's cookie, and forwards.
//
// The single most important line in this file is the one that DISCARDS the
// client's auth field. The envelope is decoded and REBUILT from its service, op
// and payload rather than patched in place, so there is no path by which a
// field the client set survives into the upstream request.

// RPCBridgePath is where the SPA posts its CSIL-RPC envelopes.
const RPCBridgePath = "/app/rpc"

// rpcMaxBodyBytes bounds a single bridged envelope. The coordinator's own
// dispatcher caps the envelope-in-body profile at 16 MiB; matching it here
// rejects an oversized body before it is read into memory rather than after.
const rpcMaxBodyBytes = 16 * 1024 * 1024

// rpcBridgeDeniedServices names services the browser may never reach through
// this bridge, whatever the coordinator would otherwise allow.
//
// ReactorcideWorker is the worker enrollment and lease protocol. It is
// authenticated by worker credentials, has nothing a UI needs, and exposing it
// on a browser-reachable path would be a way to probe worker infrastructure
// through a logged-in user's session.
var rpcBridgeDeniedServices = map[string]bool{
	"ReactorcideWorker": true,
}

// rpcBridgeDeniedOps names operations that must not be reachable here because
// their effect is a COOKIE, not a return value.
//
// begin-login, complete-login and logout all mint or clear the session cookie,
// and bootstrap-admin mints the first session of a new install. A CSIL op
// returns a typed value and cannot set a header, so each of these has a
// dedicated form endpoint that owns the cookie side effect. Reaching them
// through the bridge would appear to succeed while leaving the browser's actual
// session unchanged — or, worse, would let a page mint a session token into a
// JavaScript-visible response body, which is exactly what the HttpOnly cookie
// exists to prevent.
var rpcBridgeDeniedOps = map[string]bool{
	"begin-login":     true,
	"complete-login":  true,
	"logout":          true,
	"bootstrap-admin": true,
}

// RPCBridge forwards browser CSIL-RPC envelopes to the coordinator under the
// caller's own session.
type RPCBridge struct {
	client *http.Client
	logger *logrus.Logger
}

func NewRPCBridge() *RPCBridge {
	return &RPCBridge{
		client: transportsecurity.HTTPClient(
			&http.Client{Timeout: 60 * time.Second},
			config.AllowInsecureTransport,
			"web CSIL-RPC bridge",
		),
		logger: logrus.New(),
	}
}

// ServeHTTP handles POST /app/rpc.
func (b *RPCBridge) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !sameOriginRequest(r) {
		// Third defence against CSRF, behind SameSite=Lax on the cookie and
		// the non-simple application/cbor content type (which forces a
		// preflight this endpoint never answers affirmatively for a foreign
		// origin). Cheap, and it does not depend on browser behaviour.
		http.Error(w, "cross-origin request refused", http.StatusForbidden)
		return
	}
	if err := transportsecurity.ValidateURL(config.APIUrl, config.AllowInsecureTransport, "web CSIL-RPC bridge"); err != nil {
		http.Error(w, "coordinator transport is not secure", http.StatusBadGateway)
		return
	}

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, rpcMaxBodyBytes))
	if err != nil {
		http.Error(w, "request body could not be read", http.StatusBadRequest)
		return
	}

	incoming, err := rpctransport.DecodeRpcRequest(body)
	if err != nil {
		http.Error(w, "malformed CSIL-RPC envelope", http.StatusBadRequest)
		return
	}

	if rpcBridgeDeniedServices[incoming.Service] {
		http.Error(w, "service is not available through the web UI", http.StatusForbidden)
		return
	}
	if rpcBridgeDeniedOps[incoming.Op] {
		http.Error(w, "operation is not available through the web UI; it has its own endpoint", http.StatusForbidden)
		return
	}

	// Rebuild the envelope from scratch. Service, op and payload are carried
	// across; NOTHING else is. In particular the client's Auth field is
	// discarded here and never read: the only credential that reaches the
	// coordinator is the one this server resolves from the browser's own
	// HttpOnly cookie, below. Patching the decoded envelope in place would
	// leave open the possibility of some future field riding along; rebuilding
	// makes that structurally impossible.
	outgoing := rpctransport.NewRpcRequest(incoming.Service, incoming.Op, incoming.Payload)
	if incoming.ID != nil {
		// The correlation id is the client's own bookkeeping, not a
		// credential, and the generated client needs it echoed back.
		outgoing = outgoing.WithID(*incoming.ID)
	}
	if token, ok := sessionTokenFromRequest(r); ok {
		outgoing = outgoing.WithAuth(token)
	}
	// No cookie means no auth field at all: an anonymous call. The coordinator
	// already answers "what may an anonymous caller see", so a logged-out
	// browser gets public data rather than an error.

	encoded, err := outgoing.Encode()
	if err != nil {
		http.Error(w, "could not encode upstream request", http.StatusInternalServerError)
		return
	}

	upstream, err := http.NewRequestWithContext(r.Context(), http.MethodPost,
		strings.TrimSuffix(config.APIUrl, "/")+coordinatorRPCPath, bytes.NewReader(encoded))
	if err != nil {
		http.Error(w, "could not build upstream request", http.StatusInternalServerError)
		return
	}
	upstream.Header.Set("Content-Type", "application/cbor")
	upstream.Header.Set("Accept", "application/cbor")

	resp, err := b.client.Do(upstream)
	if err != nil {
		b.logger.WithError(err).Warn("CSIL-RPC bridge: upstream call failed")
		http.Error(w, "coordinator is unavailable", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	responseBody, err := io.ReadAll(http.MaxBytesReader(nil, resp.Body, rpcMaxBodyBytes))
	if err != nil {
		http.Error(w, "coordinator response could not be read", http.StatusBadGateway)
		return
	}

	w.Header().Set("Content-Type", "application/cbor")
	// A CSIL-RPC response is a point-in-time answer for one session. Nothing
	// about it is cacheable, and a shared cache holding one would serve one
	// user's data to another.
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(responseBody)
}

// coordinatorRPCPath is the coordinator's envelope-in-body mount point. It
// mirrors uiclient's rpcPath and uiapi.RpcPath.
const coordinatorRPCPath = "/csil/v1/rpc"

// sameOriginRequest reports whether the request's Origin (or Referer, for the
// browsers that omit Origin on same-origin POSTs) matches the host serving it.
//
// A request with NEITHER header is allowed: that is a non-browser client such
// as curl or an integration test, which is not the CSRF threat model — CSRF
// requires a browser that will attach the victim's cookie automatically.
func sameOriginRequest(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		origin = r.Header.Get("Referer")
	}
	if origin == "" {
		return true
	}
	parsed, err := url.Parse(origin)
	if err != nil || parsed.Host == "" {
		return false
	}
	return strings.EqualFold(parsed.Host, r.Host)
}
