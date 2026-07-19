// Package uiclient is the webapp-side CSIL-RPC carrier for the coordinator's
// ReactorcideAuth / ReactorcideUi services
// (coordinator_api/csil/reactorcide-ui.csil). It implements the generated
// client's Transport interface (./csilapi/client.gen.go) with the
// envelope-in-body HTTP profile (csilgen docs/csil-rpc-transport.md §2.1):
// POST {baseURL}/csil/v1/rpc, application/cbor, envelope
// {v, service, op, payload: tag24(cbor), ?auth}. This mirrors
// coordinator_api/internal/corndogs/csilapi/transport.go's shape (encode
// request, decode response, translate a "ServiceError" variant into a
// structured client error) with one addition: the session token is supplied
// per call via context (WithAuthToken/AuthTokenFromContext in context.go),
// not fixed at transport-construction time, so one CSILRPCTransport safely
// serves concurrent requests for many different logged-in users (or none, in
// REACTORCIDE_UI_AUTH_MODE "none").
package uiclient

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"

	rpctransport "github.com/catalystcommunity/csilgen/transports/go"
	"github.com/catalystcommunity/reactorcide/webapp/internal/uiclient/csilapi"
)

const rpcPath = "/csil/v1/rpc"

// CSILRPCTransport implements csilapi.Transport: the dumb byte carrier the
// generated ReactorcideAuthClient/ReactorcideUiClient call. It owns only the
// CSIL-RPC envelope + HTTP, never application types.
type CSILRPCTransport struct {
	BaseURL    string
	HTTPClient *http.Client
	// Headers are static headers applied to every request (e.g. a proxy
	// auth header). They are unrelated to the per-call session token, which
	// rides the envelope "auth" field instead (see StaticAuth and
	// WithAuthToken).
	Headers map[string]string
	// StaticAuth is the envelope "auth" value used when the call's context
	// carries no token via WithAuthToken. Leave empty for a transport that
	// only ever serves per-request tokens (the normal webapp case); set it
	// for a fixed service-level credential (e.g. tooling/tests).
	StaticAuth string
}

var _ csilapi.Transport = (*CSILRPCTransport)(nil)

// Call encodes req into a CsilRpcRequest envelope, POSTs it, and returns the
// response payload bytes (which the generated client decodes). A
// "ServiceError" response variant or a non-zero transport status becomes a
// *csilapi.ClientError, matching the generated client's documented error
// shape.
func (t *CSILRPCTransport) Call(ctx context.Context, service, op string, req []byte) ([]byte, error) {
	auth := t.StaticAuth
	hasAuth := auth != ""
	if token, ok := AuthTokenFromContext(ctx); ok {
		auth = token
		hasAuth = true
	}

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
		return nil, &csilapi.ClientError{Err: fmt.Errorf("uiclient %s/%s: http %d", service, op, httpResp.StatusCode)}
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
	// distinguish "empty typed payload" from "no payload sent" once
	// decoded through the shared module.
	return resp.Payload, nil
}

// ServiceCallError is the application-level error arm: the coordinator
// returned the "ServiceError" response variant. It is returned directly
// (not wrapped in *csilapi.ClientError) because this service's
// ServiceError.code is text (e.g. "unimplemented", "forbidden",
// "not_found"), not the int64 the generator's generic ClientError.Code field
// assumes — wrapping it there would either truncate the code or misreport
// this as a "transport error" (ClientError.Error() takes that branch
// whenever Err is set). *csilapi.ClientError is still returned, unwrapped,
// for genuine transport-level failures (network, decode, non-2xx HTTP).
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

// Clients bundles the two typed clients the webapp needs, both backed by the
// same CSILRPCTransport.
type Clients struct {
	Auth *csilapi.ReactorcideAuthClient
	Ui   *csilapi.ReactorcideUiClient
}

// New returns Clients wired to a CSILRPCTransport at baseURL. Pass a context
// built with WithAuthToken to each call to carry that request's session
// token; calls made with a bare context are anonymous (or use StaticAuth if
// the caller sets one on the returned transport).
func New(baseURL string) *Clients {
	return NewWithTransport(&CSILRPCTransport{BaseURL: baseURL})
}

// NewWithTransport returns Clients wired to a caller-supplied
// CSILRPCTransport, for tests or non-default HTTP client / header setups.
func NewWithTransport(transport *CSILRPCTransport) *Clients {
	return &Clients{
		Auth: csilapi.NewReactorcideAuthClient(transport),
		Ui:   csilapi.NewReactorcideUiClient(transport),
	}
}
