package handlers

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	rpctransport "github.com/catalystcommunity/csilgen/transports/go"
	"github.com/catalystcommunity/reactorcide/webapp/internal/config"
)

// captureUpstream stands in for the coordinator and records the envelope the
// bridge actually forwarded.
func captureUpstream(t *testing.T) (*httptest.Server, *rpctransport.RpcRequest) {
	t.Helper()
	var seen rpctransport.RpcRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("upstream: read body: %v", err)
			return
		}
		decoded, err := rpctransport.DecodeRpcRequest(body)
		if err != nil {
			t.Errorf("upstream: decode envelope: %v", err)
			return
		}
		seen = decoded
		response, err := rpctransport.NewRpcResponseOk("Ok", []byte{0xa0}).Encode()
		if err != nil {
			t.Errorf("upstream: encode response: %v", err)
			return
		}
		w.Header().Set("Content-Type", "application/cbor")
		_, _ = w.Write(response)
	}))
	t.Cleanup(server.Close)
	return server, &seen
}

func bridgeRequest(t *testing.T, envelope rpctransport.RpcRequest, cookie string) *http.Request {
	t.Helper()
	body, err := envelope.Encode()
	if err != nil {
		t.Fatalf("encode envelope: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, RPCBridgePath, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/cbor")
	if cookie != "" {
		req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: cookie})
	}
	return req
}

func withUpstream(t *testing.T, server *httptest.Server) {
	t.Helper()
	previousURL, previousInsecure := config.APIUrl, config.AllowInsecureTransport
	config.APIUrl = server.URL
	config.AllowInsecureTransport = true
	t.Cleanup(func() {
		config.APIUrl, config.AllowInsecureTransport = previousURL, previousInsecure
	})
}

// TestBridgeDiscardsClientSuppliedAuth is the single most important test in
// this package.
//
// A page that could choose its own envelope auth field could present any
// credential it obtained — including one belonging to somebody else — and the
// coordinator would honour it. The bridge must therefore ignore whatever the
// client sent and substitute the token bound to this browser's HttpOnly cookie.
func TestBridgeDiscardsClientSuppliedAuth(t *testing.T) {
	server, seen := captureUpstream(t)
	withUpstream(t, server)

	// The client tries to authenticate as somebody else.
	envelope := rpctransport.NewRpcRequest("ReactorcideUi", "list-jobs", []byte{0xa0}).
		WithAuth("stolen-or-forged-token")

	recorder := httptest.NewRecorder()
	NewRPCBridge().ServeHTTP(recorder, bridgeRequest(t, envelope, "the-real-session"))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", recorder.Code, recorder.Body.String())
	}
	if seen.Auth == nil {
		t.Fatal("upstream received no auth field at all; the cookie's token should have been substituted")
	}
	if *seen.Auth == "stolen-or-forged-token" {
		t.Fatal("the client's own auth field reached the coordinator; it must be discarded")
	}
	if *seen.Auth != "the-real-session" {
		t.Fatalf("upstream auth = %q, want the session cookie's token", *seen.Auth)
	}
}

// TestBridgeSendsNoAuthWhenLoggedOut covers anonymous browsing: no cookie must
// mean no credential, not an empty or fabricated one.
func TestBridgeSendsNoAuthWhenLoggedOut(t *testing.T) {
	server, seen := captureUpstream(t)
	withUpstream(t, server)

	envelope := rpctransport.NewRpcRequest("ReactorcideUi", "list-jobs", []byte{0xa0}).
		WithAuth("attacker-supplied")

	recorder := httptest.NewRecorder()
	NewRPCBridge().ServeHTTP(recorder, bridgeRequest(t, envelope, ""))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}
	if seen.Auth != nil {
		t.Fatalf("upstream auth = %q; a request with no session cookie must carry no credential", *seen.Auth)
	}
}

func TestBridgePreservesServiceOpPayloadAndID(t *testing.T) {
	server, seen := captureUpstream(t)
	withUpstream(t, server)

	payload := []byte{0xa1, 0x63, 'a', 'b', 'c', 0x01}
	envelope := rpctransport.NewRpcRequest("ReactorcideUi", "get-workflow", payload).WithID(42)

	recorder := httptest.NewRecorder()
	NewRPCBridge().ServeHTTP(recorder, bridgeRequest(t, envelope, "session"))

	if seen.Service != "ReactorcideUi" || seen.Op != "get-workflow" {
		t.Errorf("route = %s/%s, want ReactorcideUi/get-workflow", seen.Service, seen.Op)
	}
	if !bytes.Equal(seen.Payload, payload) {
		t.Errorf("payload = %x, want %x", seen.Payload, payload)
	}
	if seen.ID == nil || *seen.ID != 42 {
		t.Error("the correlation id must be carried across; the generated client needs it echoed back")
	}
}

func TestBridgeRefusesWorkerService(t *testing.T) {
	server, _ := captureUpstream(t)
	withUpstream(t, server)

	envelope := rpctransport.NewRpcRequest("ReactorcideWorker", "claim-lease", []byte{0xa0})
	recorder := httptest.NewRecorder()
	NewRPCBridge().ServeHTTP(recorder, bridgeRequest(t, envelope, "session"))

	if recorder.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403: the worker protocol must not be browser-reachable", recorder.Code)
	}
}

// TestBridgeRefusesCookieMintingOps covers the ops whose real effect is a
// Set-Cookie header. Reaching them here would either silently do nothing to the
// browser's session, or hand a session token to page JavaScript.
func TestBridgeRefusesCookieMintingOps(t *testing.T) {
	server, _ := captureUpstream(t)
	withUpstream(t, server)

	for _, op := range []string{"begin-login", "complete-login", "logout", "bootstrap-admin"} {
		t.Run(op, func(t *testing.T) {
			envelope := rpctransport.NewRpcRequest("ReactorcideAuth", op, []byte{0xa0})
			recorder := httptest.NewRecorder()
			NewRPCBridge().ServeHTTP(recorder, bridgeRequest(t, envelope, "session"))

			if recorder.Code != http.StatusForbidden {
				t.Errorf("status = %d, want 403 for %s", recorder.Code, op)
			}
		})
	}
}

func TestBridgeRefusesCrossOriginRequest(t *testing.T) {
	server, _ := captureUpstream(t)
	withUpstream(t, server)

	envelope := rpctransport.NewRpcRequest("ReactorcideUi", "list-jobs", []byte{0xa0})
	req := bridgeRequest(t, envelope, "session")
	req.Header.Set("Origin", "https://evil.example.com")

	recorder := httptest.NewRecorder()
	NewRPCBridge().ServeHTTP(recorder, req)

	if recorder.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403 for a foreign Origin", recorder.Code)
	}
}

func TestBridgeAllowsSameOriginRequest(t *testing.T) {
	server, _ := captureUpstream(t)
	withUpstream(t, server)

	envelope := rpctransport.NewRpcRequest("ReactorcideUi", "list-jobs", []byte{0xa0})
	req := bridgeRequest(t, envelope, "session")
	req.Header.Set("Origin", "http://"+req.Host)

	recorder := httptest.NewRecorder()
	NewRPCBridge().ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 for a same-origin request", recorder.Code)
	}
}

func TestBridgeRejectsMalformedEnvelope(t *testing.T) {
	server, _ := captureUpstream(t)
	withUpstream(t, server)

	req := httptest.NewRequest(http.MethodPost, RPCBridgePath, bytes.NewReader([]byte{0xff, 0xff, 0xff}))
	req.Header.Set("Content-Type", "application/cbor")

	recorder := httptest.NewRecorder()
	NewRPCBridge().ServeHTTP(recorder, req)

	if recorder.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", recorder.Code)
	}
}

func TestBridgeResponseIsNotCacheable(t *testing.T) {
	server, _ := captureUpstream(t)
	withUpstream(t, server)

	envelope := rpctransport.NewRpcRequest("ReactorcideUi", "list-jobs", []byte{0xa0})
	recorder := httptest.NewRecorder()
	NewRPCBridge().ServeHTTP(recorder, bridgeRequest(t, envelope, "session"))

	if got := recorder.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store: a shared cache holding one session's answer would serve it to another user", got)
	}
}
