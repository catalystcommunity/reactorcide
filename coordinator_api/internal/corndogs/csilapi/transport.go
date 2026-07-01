// CSIL-RPC transport carrier for the corndogs Go library.
//
//	c := corndogs.New("https://corndogs.example.com")
//	resp, err := c.SubmitTask(ctx, corndogs.SubmitTaskRequest{Queue: "q", Priority: 0})
//
// This package is the single corndogs Go library: generated types, codec, the
// CorndogsService interface (implemented by the server) and the typed CorndogsClient
// (used by Go callers), plus this hand-written transport carrier. The generated client
// owns (de)serialization via the codec; the carrier only moves bytes. The wire is
// CSIL-RPC (csilgen docs/csil-rpc-transport.md), envelope-in-body HTTP profile: the
// already-encoded request payload is wrapped in a CsilRpcRequest envelope, POSTed to
// {baseURL}/csil/v1/rpc, and the response payload bytes are returned for the generated
// client to decode. A "ServiceError" arm is surfaced as a structured *ClientError.
//
// The carrier reuses the generated codec's own CBOR primitives for the tiny RPC
// envelope, so the library has no third-party dependencies. This file is hand-written
// and lives alongside the generated files in the same package; regeneration only
// rewrites the generated artifacts (see csil/generate.sh).
package corndogs

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
)

const (
	rpcPath        = "/csil/v1/rpc"
	tagEncodedCBOR = 24 // RFC 8949 §3.4.5.1 — embedded encoded CBOR data item
)

// CSILRPCTransport implements Transport: the dumb byte carrier the generated client
// calls. It owns only the CSIL-RPC envelope + HTTP, never application types.
type CSILRPCTransport struct {
	BaseURL    string
	HTTPClient *http.Client
	Headers    map[string]string
}

// Call wraps the already-encoded request bytes in a CsilRpcRequest, POSTs it, and
// returns the response payload bytes (which the generated client decodes). A non-zero
// transport status or a "ServiceError" arm becomes a *ClientError.
func (t *CSILRPCTransport) Call(ctx context.Context, service, op string, req []byte) ([]byte, error) {
	env := cborEncode(cborMap{
		{key: cborText("v"), val: cborUint(1)},
		{key: cborText("service"), val: cborText(service)},
		{key: cborText("op"), val: cborText(op)},
		{key: cborText("payload"), val: cborTag{num: tagEncodedCBOR, inner: cborBytes(req)}},
	})

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, trimSlash(t.BaseURL)+rpcPath, bytes.NewReader(env))
	if err != nil {
		return nil, &ClientError{Err: err}
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
		return nil, &ClientError{Err: err}
	}
	defer httpResp.Body.Close()
	body, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return nil, &ClientError{Err: err}
	}
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		return nil, &ClientError{Err: fmt.Errorf("corndogs %s/%s: http %d", service, op, httpResp.StatusCode)}
	}

	val, err := cborDecode(body)
	if err != nil {
		return nil, &ClientError{Err: fmt.Errorf("decode response envelope: %w", err)}
	}
	if sv, ok := cborMapGet(val, "status"); ok {
		if status, e := cborAsI64(sv); e == nil && status != 0 {
			msg := ""
			if ev, ok := cborMapGet(val, "error"); ok {
				msg, _ = cborAsText(ev)
			}
			return nil, &ClientError{Err: fmt.Errorf("transport status %d: %s", status, msg)}
		}
	}

	pv, ok := cborMapGet(val, "payload")
	if !ok {
		return nil, &ClientError{Err: fmt.Errorf("response missing payload")}
	}
	if tag, ok := pv.(cborTag); ok {
		pv = tag.inner
	}
	inner, err := cborAsBytes(pv)
	if err != nil {
		return nil, &ClientError{Err: fmt.Errorf("response payload is not a tag-24 byte string: %w", err)}
	}

	if vv, ok := cborMapGet(val, "variant"); ok {
		if variant, _ := cborAsText(vv); variant == "ServiceError" {
			if se, derr := DecodeServiceError(inner); derr == nil {
				return nil, &ClientError{Code: int64(se.Code), Message: se.Message}
			}
			return nil, &ClientError{Err: fmt.Errorf("undecodable ServiceError payload")}
		}
	}
	return inner, nil
}

func trimSlash(s string) string {
	for len(s) > 0 && s[len(s)-1] == '/' {
		s = s[:len(s)-1]
	}
	return s
}

// New returns a CorndogsClient wired to a CSILRPCTransport at baseURL.
func New(baseURL string) *CorndogsClient {
	return NewCorndogsClient(&CSILRPCTransport{BaseURL: baseURL})
}
