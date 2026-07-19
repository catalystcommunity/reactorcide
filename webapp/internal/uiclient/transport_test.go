package uiclient

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	rpctransport "github.com/catalystcommunity/csilgen/transports/go"
	"github.com/catalystcommunity/reactorcide/webapp/internal/uiclient/csilapi"
)

// -----------------------------------------------------------------------
// Test-only stand-in dispatcher. This package is the webapp-side generated
// client + hand-written HTTP carrier; the real dispatcher
// (coordinator_api/internal/uiapi) lives in the separate coordinator_api Go
// module (no go.work ties the two modules together in this repo), so a
// literal cross-module round trip isn't possible from here. This local
// httptest.Server speaks the exact same CSIL-RPC envelope-in-body wire
// contract (csilgen docs/csil-rpc-transport.md) the real dispatcher does —
// decode request envelope, route on (service, op), encode a
// success/ServiceError response — using the same shared rpctransport module
// the real dispatcher and this package's transport.go both build on, so
// these tests exercise CSILRPCTransport (encode request, decode response,
// ServiceError translation, auth propagation) exactly as it behaves against
// the real coordinator. The dispatcher decode/route/encode logic itself is
// covered independently by coordinator_api/internal/uiapi's own tests.
// -----------------------------------------------------------------------

type stubRequestEnvelope struct {
	Service string
	Op      string
	Payload []byte
	Auth    string
	HasAuth bool
}

func stubDecodeRequestEnvelope(t *testing.T, body []byte) stubRequestEnvelope {
	t.Helper()
	req, err := rpctransport.DecodeRpcRequest(body)
	if err != nil {
		t.Fatalf("decode request envelope: %v", err)
	}
	env := stubRequestEnvelope{Service: req.Service, Op: req.Op, Payload: req.Payload}
	if req.Auth != nil {
		env.Auth = *req.Auth
		env.HasAuth = true
	}
	return env
}

func stubEncodeSuccessResponse(t *testing.T, variant string, payload []byte) []byte {
	t.Helper()
	body, err := rpctransport.NewRpcResponseOk(variant, payload).Encode()
	if err != nil {
		t.Fatalf("encode success response: %v", err)
	}
	return body
}

func stubEncodeTransportErrorResponse(t *testing.T, status int64, msg string) []byte {
	t.Helper()
	body, err := rpctransport.NewRpcResponseTransportError(rpctransport.StatusFromCode(status), msg).Encode()
	if err != nil {
		t.Fatalf("encode transport error response: %v", err)
	}
	return body
}

// newStubDispatcherServer starts an httptest.Server that answers exactly two
// ops ("ReactorcideAuth"/"get-auth-config" and "ReactorcideAuth"/
// "authenticate", the latter echoing the auth field) and returns
// ServiceError{code:"unimplemented"} for everything else — mirroring
// StubAuth/StubUi in coordinator_api/internal/uiapi/stub.go closely enough
// to exercise the client carrier's success, ServiceError, and auth-
// propagation paths.
func newStubDispatcherServer(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc(rpcPath, func(w http.ResponseWriter, r *http.Request) {
		full, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read request body: %v", err)
		}
		env := stubDecodeRequestEnvelope(t, full)

		var resp []byte
		switch {
		case env.Service == "ReactorcideAuth" && env.Op == "get-auth-config":
			payload := csilapi.EncodeGetAuthConfigResponse(csilapi.GetAuthConfigResponse{
				AuthMode:                "local-rp",
				BootstrapAdminAvailable: true,
			})
			resp = stubEncodeSuccessResponse(t, "GetAuthConfigResponse", payload)
		case env.Service == "ReactorcideAuth" && env.Op == "authenticate":
			if env.HasAuth && env.Auth != "" {
				payload := csilapi.EncodeAuthenticateResponse(csilapi.AuthenticateResponse{
					Authenticated: true,
					Identity:      &csilapi.AuthenticatedIdentity{UserId: "user-for-" + env.Auth},
				})
				resp = stubEncodeSuccessResponse(t, "AuthenticateResponse", payload)
			} else {
				payload := csilapi.EncodeAuthenticateResponse(csilapi.AuthenticateResponse{Authenticated: false})
				resp = stubEncodeSuccessResponse(t, "AuthenticateResponse", payload)
			}
		case env.Service == "" || env.Op == "":
			resp = stubEncodeTransportErrorResponse(t, 2, "malformed request")
		default:
			sePayload := csilapi.EncodeServiceError(csilapi.ServiceError{
				Code:    "unimplemented",
				Message: env.Service + "/" + env.Op + " is not implemented yet",
			})
			resp = stubEncodeSuccessResponse(t, "ServiceError", sePayload)
		}

		w.Header().Set("Content-Type", "application/cbor")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(resp)
	})
	return httptest.NewServer(mux)
}

func TestTransport_SuccessRoundTrip(t *testing.T) {
	srv := newStubDispatcherServer(t)
	defer srv.Close()

	clients := New(srv.URL)
	resp, err := clients.Auth.GetAuthConfig(context.Background(), csilapi.GetAuthConfigRequest{})
	if err != nil {
		t.Fatalf("GetAuthConfig: %v", err)
	}
	if resp.AuthMode != "local-rp" {
		t.Errorf("AuthMode = %q, want local-rp", resp.AuthMode)
	}
	if !resp.BootstrapAdminAvailable {
		t.Errorf("BootstrapAdminAvailable = false, want true")
	}
}

func TestTransport_ServiceErrorSurfacesAsServiceCallError(t *testing.T) {
	srv := newStubDispatcherServer(t)
	defer srv.Close()

	clients := New(srv.URL)
	_, err := clients.Auth.Logout(context.Background(), csilapi.LogoutRequest{})
	if err == nil {
		t.Fatalf("Logout: want an error (unimplemented op), got nil")
	}
	var svcErr *ServiceCallError
	if !errors.As(err, &svcErr) {
		t.Fatalf("Logout error = %T (%v), want *ServiceCallError", err, err)
	}
	if svcErr.Code != "unimplemented" {
		t.Errorf("ServiceCallError.Code = %q, want unimplemented", svcErr.Code)
	}
}

func TestTransport_AuthTokenPropagatesPerRequest(t *testing.T) {
	srv := newStubDispatcherServer(t)
	defer srv.Close()

	clients := New(srv.URL)

	t.Run("with token", func(t *testing.T) {
		ctx := WithAuthToken(context.Background(), "sekrit-session-token")
		resp, err := clients.Auth.Authenticate(ctx, csilapi.AuthenticateRequest{})
		if err != nil {
			t.Fatalf("Authenticate: %v", err)
		}
		if !resp.Authenticated {
			t.Fatalf("Authenticated = false, want true")
		}
		if resp.Identity == nil || resp.Identity.UserId != "user-for-sekrit-session-token" {
			t.Fatalf("Identity = %+v, want UserId echoing the auth token", resp.Identity)
		}
	})

	t.Run("without token", func(t *testing.T) {
		resp, err := clients.Auth.Authenticate(context.Background(), csilapi.AuthenticateRequest{})
		if err != nil {
			t.Fatalf("Authenticate: %v", err)
		}
		if resp.Authenticated {
			t.Fatalf("Authenticated = true, want false for a call with no auth token in context")
		}
	})

	t.Run("two concurrent tokens on one transport don't leak", func(t *testing.T) {
		ctxA := WithAuthToken(context.Background(), "token-a")
		ctxB := WithAuthToken(context.Background(), "token-b")

		respA, err := clients.Auth.Authenticate(ctxA, csilapi.AuthenticateRequest{})
		if err != nil {
			t.Fatalf("Authenticate (A): %v", err)
		}
		respB, err := clients.Auth.Authenticate(ctxB, csilapi.AuthenticateRequest{})
		if err != nil {
			t.Fatalf("Authenticate (B): %v", err)
		}
		if respA.Identity == nil || respA.Identity.UserId != "user-for-token-a" {
			t.Errorf("Identity A = %+v, want UserId user-for-token-a", respA.Identity)
		}
		if respB.Identity == nil || respB.Identity.UserId != "user-for-token-b" {
			t.Errorf("Identity B = %+v, want UserId user-for-token-b", respB.Identity)
		}
	})
}
