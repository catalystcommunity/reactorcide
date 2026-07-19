package handlers

// A minimal, dependency-free fake coordinator for exercising the auth/
// session plumbing in this package against something that actually speaks
// the CSIL-RPC envelope-in-body HTTP profile (csilgen docs/
// csil-rpc-transport.md §1, §2.1), the same shape uiclient.CSILRPCTransport
// and the real coordinator's internal/uiapi dispatcher use.
//
// coordinator_api is a separate Go module (replaced locally, but not
// importable as an "internal" package from here), so this cannot reuse the
// coordinator's dispatcher directly. It builds on the same shared
// github.com/catalystcommunity/csilgen/transports/go module the real
// dispatcher and uiclient's transport.go both use for the envelope
// codec — decode a request envelope, encode a response envelope — so this
// fake speaks byte-identical wire format without hand-rolling CBOR itself.
// Request/response *payloads* stay opaque bytes here too, (de)serialized
// only via the generated csilapi Encode<Type>/Decode<Type> functions in the
// test bodies that use this fake.

import (
	"io"
	"net/http"
	"net/http/httptest"
	"sync"

	rpctransport "github.com/catalystcommunity/csilgen/transports/go"
	"github.com/catalystcommunity/reactorcide/webapp/internal/uiclient/csilapi"
)

// fakeOp is a canned handler for one (service, op) pair: given the decoded
// request payload bytes and the envelope's auth field, return the encoded
// response payload, the response variant name, and whether that variant is
// "ServiceError".
type fakeOp func(payload []byte, auth string, hasAuth bool) (respPayload []byte, variant string, isServiceError bool)

// fakeCall records one dispatched RPC for test assertions (e.g. "did
// begin-login see the identity we typed", "did logout carry the session
// cookie's token in auth").
type fakeCall struct {
	Service string
	Op      string
	Auth    string
	HasAuth bool
}

// fakeCoordinator is an httptest-servable stand-in for the coordinator's
// POST /csil/v1/rpc endpoint: decode the envelope, dispatch to a
// test-registered fakeOp by (service, op), encode the response envelope. An
// unregistered (service, op) returns a ServiceError "unimplemented", the
// same convention the real dispatcher uses for ops it hasn't wired up yet.
type fakeCoordinator struct {
	mu    sync.Mutex
	ops   map[string]fakeOp
	calls []fakeCall
}

func newFakeCoordinator() *fakeCoordinator {
	return &fakeCoordinator{ops: map[string]fakeOp{}}
}

func (f *fakeCoordinator) handle(service, op string, fn fakeOp) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ops[service+"/"+op] = fn
}

func (f *fakeCoordinator) callCount(service, op string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if c.Service == service && c.Op == op {
			n++
		}
	}
	return n
}

func (f *fakeCoordinator) lastCall() (fakeCall, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.calls) == 0 {
		return fakeCall{}, false
	}
	return f.calls[len(f.calls)-1], true
}

func (f *fakeCoordinator) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	req, err := rpctransport.DecodeRpcRequest(body)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	var auth string
	var hasAuth bool
	if req.Auth != nil {
		auth = *req.Auth
		hasAuth = true
	}

	f.mu.Lock()
	f.calls = append(f.calls, fakeCall{Service: req.Service, Op: req.Op, Auth: auth, HasAuth: hasAuth})
	fn, ok := f.ops[req.Service+"/"+req.Op]
	f.mu.Unlock()

	var resp rpctransport.RpcResponse
	if !ok {
		se := csilapi.ServiceError{Code: "unimplemented", Message: "no fake handler for " + req.Service + "/" + req.Op}
		resp = rpctransport.NewRpcResponseOk("ServiceError", csilapi.EncodeServiceError(se))
	} else {
		payload, variant, isErr := fn(req.Payload, auth, hasAuth)
		if isErr {
			resp = rpctransport.NewRpcResponseOk("ServiceError", payload)
		} else {
			resp = rpctransport.NewRpcResponseOk(variant, payload)
		}
	}

	respBody, err := resp.Encode()
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/cbor")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(respBody)
}

// fakeServiceErrorPayload encodes a ServiceError response payload for a
// fakeOp to return alongside isServiceError=true.
func fakeServiceErrorPayload(code, message string) []byte {
	return csilapi.EncodeServiceError(csilapi.ServiceError{Code: code, Message: message})
}

func newTestServer(t interface{ Cleanup(func()) }, fc *fakeCoordinator) *httptest.Server {
	srv := httptest.NewServer(fc)
	t.Cleanup(srv.Close)
	return srv
}
