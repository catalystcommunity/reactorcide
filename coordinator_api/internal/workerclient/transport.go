// Package workerclient is the worker-side CSIL-RPC carrier for the
// coordinator's ReactorcideWorker service. It implements the generated
// client's Transport interface (/csilapi/client.gen.go) with the
// envelope-in-body HTTP profile (csilgen docs/csil-rpc-transport.md §2.1):
// POST {baseURL}/csil/v1/rpc, application/cbor, envelope {v, service, op,
// payload: tag24(cbor), ?auth}. This mirrors
// webapp/internal/uiclient/transport.go's shape (encode request, decode
// response, translate a "ServiceError" variant into a structured client
// error) with one difference: a worker process holds exactly one session at a
// time (there is no per-request "which logged-in user" question the way the
// webapp has), so the session token is carried as internal transport state --
// set once by Client.Register and refreshed by Client.Heartbeat -- rather
// than pulled from the call's context on every request. A caller that needs
// to override the token for a single call (e.g. tests exercising a
// stale/garbage session) can still do so via WithSessionToken.
package workerclient

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"sync"

	rpctransport "github.com/catalystcommunity/csilgen/transports/go"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/workerclient/csilapi"
)

const rpcPath = "/csil/v1/rpc"

// CSILRPCTransport implements csilapi.Transport: the dumb byte carrier the
// generated ReactorcideWorkerClient calls. It owns only the CSIL-RPC
// envelope + HTTP, never application types.
type CSILRPCTransport struct {
	BaseURL    string
	HTTPClient *http.Client
	// Headers are static headers applied to every request (e.g. a proxy
	// auth header). They are unrelated to the per-call session token, which
	// rides the envelope "auth" field instead (see SetSession).
	Headers map[string]string

	mu      sync.RWMutex
	session string
}

var _ csilapi.Transport = (*CSILRPCTransport)(nil)

// SetSession sets the worker session token attached to the envelope "auth"
// field on every subsequent call. An empty token means "no session yet" --
// the envelope's auth field is omitted, matching Register (which carries
// its own enrollment_token in the request body, not the envelope auth).
func (t *CSILRPCTransport) SetSession(token string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.session = token
}

// Session returns the currently set worker session token, if any.
func (t *CSILRPCTransport) Session() string {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.session
}

// sessionTokenKey is an unexported context key so this package's per-call
// auth override never collides with keys set elsewhere.
type sessionTokenKey struct{}

// WithSessionToken returns a context that overrides the transport's stored
// session token for a single call. Ordinary run-loop usage never needs
// this -- Client.Register/Heartbeat manage the stored session automatically
// -- but tests exercising a stale, revoked, or forged session value use it
// to make one call under a specific token without mutating shared transport
// state.
//
// NOTE: nothing calls this yet. It is kept for the stale/revoked/forged
// session tests it was written for; Call already reads the override via
// sessionTokenFromContext, so those tests need no further plumbing. Delete
// both if that coverage is dropped.
func WithSessionToken(ctx context.Context, token string) context.Context {
	return context.WithValue(ctx, sessionTokenKey{}, token)
}

func sessionTokenFromContext(ctx context.Context) (string, bool) {
	token, ok := ctx.Value(sessionTokenKey{}).(string)
	return token, ok
}

// Call encodes req into a CsilRpcRequest envelope, POSTs it, and returns the
// response payload bytes (which the generated client decodes). A
// "ServiceError" response variant or a non-zero transport status becomes a
// *csilapi.ClientError, matching the generated client's documented error
// shape.
func (t *CSILRPCTransport) Call(ctx context.Context, service, op string, req []byte) ([]byte, error) {
	auth := t.Session()
	if override, ok := sessionTokenFromContext(ctx); ok {
		auth = override
	}
	hasAuth := auth != ""

	rpcReq := rpctransport.NewRpcRequest(service, op, req)
	if hasAuth {
		rpcReq = rpcReq.WithAuth(auth)
	}
	body, err := rpcReq.Encode()
	if err != nil {
		return nil, &csilapi.ClientError{Err: err}
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, trimSlash(t.BaseURL)+rpcPath, bytes.NewReader(body))
	if err != nil {
		return nil, &csilapi.ClientError{Err: err}
	}
	httpReq.Header.Set("Content-Type", "application/cbor")
	httpReq.Header.Set("Accept", "application/cbor")
	for k, v := range t.Headers {
		httpReq.Header.Set(k, v)
	}

	client := t.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	httpResp, err := client.Do(httpReq)
	if err != nil {
		return nil, &csilapi.ClientError{Err: err}
	}
	defer httpResp.Body.Close()
	respBody, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return nil, &csilapi.ClientError{Err: err}
	}
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		return nil, &csilapi.ClientError{Err: fmt.Errorf("workerclient %s/%s: http %d", service, op, httpResp.StatusCode)}
	}

	resp, err := rpctransport.DecodeRpcResponse(respBody)
	if err != nil {
		return nil, &csilapi.ClientError{Err: fmt.Errorf("decode response envelope: %w", err)}
	}
	if !resp.Status.IsOk() {
		msg := ""
		if resp.Error != nil {
			msg = *resp.Error
		}
		return nil, &csilapi.ClientError{Err: fmt.Errorf("transport status %d: %s", resp.Status.Code(), msg)}
	}
	variant := ""
	if resp.Variant != nil {
		variant = *resp.Variant
	}
	if variant == "ServiceError" {
		se, derr := csilapi.DecodeServiceError(resp.Payload)
		if derr != nil {
			return nil, &csilapi.ClientError{Err: fmt.Errorf("undecodable ServiceError payload: %w", derr)}
		}
		return nil, &ServiceCallError{Code: se.Code, Message: se.Message}
	}
	// rpctransport.DecodeRpcResponse always yields a non-nil Payload (empty
	// bytes when the wire envelope's "payload" was absent or an empty
	// tag-24 byte string), so a Status-ok, non-ServiceError response's
	// Payload is trusted as-is here; there is no wire-level way to
	// distinguish "empty typed payload" from "no payload sent" once decoded
	// through the shared module.
	return resp.Payload, nil
}

// ServiceCallError is the application-level error arm: the coordinator
// returned the "ServiceError" response variant. It is returned directly
// (not wrapped in *csilapi.ClientError) because this service's
// ServiceError.code is text (e.g. "unauthorized", "invalid_argument",
// "not_found", "conflict", "internal" -- see internal/workerapi/service.go),
// not the int64 the generated client's generic ClientError.Code field
// assumes.
type ServiceCallError struct {
	Code    string
	Message string
}

func (e *ServiceCallError) Error() string {
	return fmt.Sprintf("service error %s: %s", e.Code, e.Message)
}

func trimSlash(s string) string {
	for len(s) > 0 && s[len(s)-1] == '/' {
		s = s[:len(s)-1]
	}
	return s
}
