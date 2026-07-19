package uiapi

import (
	"bytes"
	"context"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	rpctransport "github.com/catalystcommunity/csilgen/transports/go"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/uiapi/csilapi"
)

// -----------------------------------------------------------------------
// Test-only envelope helpers (the reverse of what the dispatcher itself
// does, which now just calls rpctransport.DecodeRpcRequest /
// rpctransport.RpcResponse.Encode directly). Production code never needs to
// build a request envelope or read a response envelope from the coordinator
// side; the real counterpart of this direction is
// webapp/internal/uiclient's transport.go, which lives in a separate Go
// module (webapp/go.mod is independent of coordinator_api/go.mod, and
// there's no go.work tying them together), so a literal cross-module round
// trip isn't possible here. These helpers build/parse envelopes with the
// same shared rpctransport module the dispatcher and
// webapp/internal/uiclient's transport.go both use, to exercise the real
// Handler end-to-end over net/http/httptest.
// -----------------------------------------------------------------------

func testBuildRequestEnvelope(service, op string, payload []byte, auth string, hasAuth bool) []byte {
	req := rpctransport.NewRpcRequest(service, op, payload)
	if hasAuth {
		req = req.WithAuth(auth)
	}
	body, err := req.Encode()
	if err != nil {
		panic(err) // rpctransport.RpcRequest.Encode never fails
	}
	return body
}

type testResponseEnvelope struct {
	status     int64
	variant    string
	errMsg     string
	payload    []byte
	hasPayload bool
}

func testParseResponseEnvelope(t *testing.T, body []byte) testResponseEnvelope {
	t.Helper()
	resp, err := rpctransport.DecodeRpcResponse(body)
	if err != nil {
		t.Fatalf("decode response envelope: %v", err)
	}
	out := testResponseEnvelope{status: resp.Status.Code()}
	if resp.Variant != nil {
		out.variant = *resp.Variant
	}
	if resp.Error != nil {
		out.errMsg = *resp.Error
	}
	// rpctransport.DecodeRpcResponse always yields a non-nil Payload (empty
	// when the wire envelope omitted "payload" or sent an empty tag-24
	// byte string), so hasPayload tracks "does this response carry an
	// application payload" via the status/variant convention instead of a
	// bare nil check.
	if resp.Status.IsOk() && resp.Variant != nil {
		out.payload = resp.Payload
		out.hasPayload = true
	}
	return out
}

func testPostEnvelope(t *testing.T, srv *httptest.Server, body []byte) testResponseEnvelope {
	t.Helper()
	resp, err := http.Post(srv.URL+RpcPath, "application/cbor", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST %s: %v", RpcPath, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST %s: http status %d, want 200 (CSIL-RPC always returns 200 for a CsilRpcResponse)", RpcPath, resp.StatusCode)
	}
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response body: %v", err)
	}
	return testParseResponseEnvelope(t, respBody)
}

// TestDispatcher_StubReturnsUnimplemented proves the webapp-client wire shape
// round-trips through the real Handler end-to-end (encode request envelope
// -> HTTP POST -> Handler.ServeHTTP -> decode request -> route ->
// StubAuth.GetAuthConfig -> ServiceErr -> encode "ServiceError" response ->
// decode response) with the stub implementations mounted, exactly as they
// are in the coordinator router before Task G lands.
func TestDispatcher_StubReturnsUnimplemented(t *testing.T) {
	h := NewHandler(NewStubAuth(), NewStubUi())
	srv := httptest.NewServer(h)
	defer srv.Close()

	reqPayload := csilapi.EncodeGetAuthConfigRequest(csilapi.GetAuthConfigRequest{})
	body := testBuildRequestEnvelope("ReactorcideAuth", "get-auth-config", reqPayload, "", false)

	env := testPostEnvelope(t, srv, body)
	if env.status != 0 {
		t.Fatalf("status = %d, want 0 (application errors are status 0, variant ServiceError)", env.status)
	}
	if env.variant != "ServiceError" {
		t.Fatalf("variant = %q, want ServiceError", env.variant)
	}
	if !env.hasPayload {
		t.Fatalf("response has no payload")
	}
	se, err := csilapi.DecodeServiceError(env.payload)
	if err != nil {
		t.Fatalf("decode ServiceError: %v", err)
	}
	if se.Code != "unimplemented" {
		t.Errorf("ServiceError.Code = %q, want %q", se.Code, "unimplemented")
	}
	if se.Message == "" {
		t.Errorf("ServiceError.Message is empty")
	}
}

// TestDispatcher_UnknownServiceAndOp proves malformed routing produces a
// transport-level failure (non-zero status, no payload), never an
// application ServiceError.
func TestDispatcher_UnknownServiceAndOp(t *testing.T) {
	h := NewHandler(NewStubAuth(), NewStubUi())
	srv := httptest.NewServer(h)
	defer srv.Close()

	t.Run("unknown service", func(t *testing.T) {
		body := testBuildRequestEnvelope("NoSuchService", "get-auth-config", []byte{0xa0}, "", false)
		env := testPostEnvelope(t, srv, body)
		if env.status == 0 {
			t.Fatalf("status = 0, want non-zero for an unknown service")
		}
		if env.hasPayload {
			t.Errorf("transport error response has a payload; want none")
		}
		if env.errMsg == "" {
			t.Errorf("transport error response has no error message")
		}
	})

	t.Run("unknown op", func(t *testing.T) {
		body := testBuildRequestEnvelope("ReactorcideAuth", "no-such-op", []byte{0xa0}, "", false)
		env := testPostEnvelope(t, srv, body)
		if env.status == 0 {
			t.Fatalf("status = 0, want non-zero for an unknown op")
		}
		if env.hasPayload {
			t.Errorf("transport error response has a payload; want none")
		}
	})
}

// fakeAuth overrides just the ops these tests exercise; every other
// ReactorcideAuth method is promoted from the embedded StubAuth (still
// returning ServiceError{code:"unimplemented"}), so fakeAuth satisfies the
// full interface without restating all six methods.
type fakeAuth struct {
	StubAuth
}

func (fakeAuth) GetAuthConfig(ctx context.Context, req csilapi.GetAuthConfigRequest) (csilapi.GetAuthConfigResponse, error) {
	return csilapi.GetAuthConfigResponse{
		AuthMode:                "local-rp",
		BootstrapAdminAvailable: true,
		HasGlobalAdmin:          false,
	}, nil
}

// Authenticate echoes the context's auth token into the response identity so
// the test can prove the envelope's "auth" field reached the implementation
// via AuthTokenFromContext.
func (fakeAuth) Authenticate(ctx context.Context, req csilapi.AuthenticateRequest) (csilapi.AuthenticateResponse, error) {
	token, ok := AuthTokenFromContext(ctx)
	if !ok || token == "" {
		return csilapi.AuthenticateResponse{Authenticated: false}, nil
	}
	return csilapi.AuthenticateResponse{
		Authenticated: true,
		Identity: &csilapi.AuthenticatedIdentity{
			UserId: "user-for-" + token,
		},
	}, nil
}

var _ csilapi.ReactorcideAuth = fakeAuth{}

// TestDispatcher_RealImplementationReturnsData proves a real (non-stub)
// implementation's success arm round-trips: the dispatcher picks the
// "GetAuthConfigResponse" variant and the payload decodes back to exactly
// what the implementation returned.
func TestDispatcher_RealImplementationReturnsData(t *testing.T) {
	h := NewHandler(fakeAuth{}, NewStubUi())
	srv := httptest.NewServer(h)
	defer srv.Close()

	reqPayload := csilapi.EncodeGetAuthConfigRequest(csilapi.GetAuthConfigRequest{})
	body := testBuildRequestEnvelope("ReactorcideAuth", "get-auth-config", reqPayload, "", false)

	env := testPostEnvelope(t, srv, body)
	if env.status != 0 {
		t.Fatalf("status = %d, want 0", env.status)
	}
	if env.variant != "GetAuthConfigResponse" {
		t.Fatalf("variant = %q, want GetAuthConfigResponse", env.variant)
	}
	resp, err := csilapi.DecodeGetAuthConfigResponse(env.payload)
	if err != nil {
		t.Fatalf("decode GetAuthConfigResponse: %v", err)
	}
	if resp.AuthMode != "local-rp" {
		t.Errorf("AuthMode = %q, want local-rp", resp.AuthMode)
	}
	if !resp.BootstrapAdminAvailable {
		t.Errorf("BootstrapAdminAvailable = false, want true")
	}
	if resp.HasGlobalAdmin {
		t.Errorf("HasGlobalAdmin = true, want false")
	}
}

// TestDispatcher_AuthTokenPropagatesToContext proves the envelope "auth"
// field reaches the implementation via AuthTokenFromContext, and that an
// envelope with no "auth" field leaves the implementation unauthenticated —
// the mechanism Task G's authorization checks and Task H's session
// forwarding both depend on.
func TestDispatcher_AuthTokenPropagatesToContext(t *testing.T) {
	h := NewHandler(fakeAuth{}, NewStubUi())
	srv := httptest.NewServer(h)
	defer srv.Close()

	reqPayload := csilapi.EncodeAuthenticateRequest(csilapi.AuthenticateRequest{})

	t.Run("with auth token", func(t *testing.T) {
		body := testBuildRequestEnvelope("ReactorcideAuth", "authenticate", reqPayload, "sekrit-session-token", true)
		env := testPostEnvelope(t, srv, body)
		if env.status != 0 || env.variant != "AuthenticateResponse" {
			t.Fatalf("status=%d variant=%q, want status=0 variant=AuthenticateResponse", env.status, env.variant)
		}
		resp, err := csilapi.DecodeAuthenticateResponse(env.payload)
		if err != nil {
			t.Fatalf("decode AuthenticateResponse: %v", err)
		}
		if !resp.Authenticated {
			t.Fatalf("Authenticated = false, want true")
		}
		if resp.Identity == nil || resp.Identity.UserId != "user-for-sekrit-session-token" {
			t.Fatalf("Identity = %+v, want UserId echoing the auth token", resp.Identity)
		}
	})

	t.Run("without auth token", func(t *testing.T) {
		body := testBuildRequestEnvelope("ReactorcideAuth", "authenticate", reqPayload, "", false)
		env := testPostEnvelope(t, srv, body)
		if env.status != 0 || env.variant != "AuthenticateResponse" {
			t.Fatalf("status=%d variant=%q, want status=0 variant=AuthenticateResponse", env.status, env.variant)
		}
		resp, err := csilapi.DecodeAuthenticateResponse(env.payload)
		if err != nil {
			t.Fatalf("decode AuthenticateResponse: %v", err)
		}
		if resp.Authenticated {
			t.Fatalf("Authenticated = true, want false for an anonymous (no auth field) call")
		}
	})
}

// goldenAuthenticateRequestHex is a byte-golden fixture of exactly the
// request envelope webapp/internal/uiclient's CSILRPCTransport.Call
// produces for a call to ReactorcideAuth/authenticate with auth token
// "golden-token": rpctransport.NewRpcRequest("ReactorcideAuth",
// "authenticate", <encoded AuthenticateRequest{}>).WithAuth("golden-token").
// Encode(). Captured once from that exact call (both this package and
// uiclient's transport.go build requests through the identical
// rpctransport.RpcRequest.Encode() call, so this fixture is the wire proof
// that survives even though a literal cross-module test isn't possible —
// coordinator_api and webapp are separate Go modules with no go.work tying
// them together, and neither module's "internal" packages are importable
// from the other). If this hex ever changes, either rpctransport's
// canonical CBOR encoding changed upstream, or something in how a request
// envelope is built regressed — either way this test should fail loudly
// rather than silently decoding something a real webapp client would never
// send.
const goldenAuthenticateRequestHex = "a5617601626f706c61757468656e74696361746564617574686c676f6c64656e2d746f6b656e677061796c6f6164d81841a067736572766963656f52656163746f726369646541757468"

// TestDispatcher_WireCompatGoldenRequestBytes decodes a byte-golden fixture
// of the exact wire bytes uiclient.CSILRPCTransport.Call produces (see
// goldenAuthenticateRequestHex) directly through the real Handler, proving
// the coordinator-side rewrite onto rpctransport.DecodeRpcRequest still
// accepts byte-identical input to what the webapp client emits — including
// the "auth" field propagating to AuthTokenFromContext — without requiring
// a literal cross-module call.
func TestDispatcher_WireCompatGoldenRequestBytes(t *testing.T) {
	golden, err := hex.DecodeString(goldenAuthenticateRequestHex)
	if err != nil {
		t.Fatalf("decode golden hex fixture: %v", err)
	}

	// Sanity: the fixture really is what rpctransport.RpcRequest.Encode
	// produces for this call today, so a future rpctransport change that
	// alters wire bytes fails this assertion (not just a mysterious
	// Handler decode error) and a stale/hand-edited fixture is caught too.
	reqPayload := csilapi.EncodeAuthenticateRequest(csilapi.AuthenticateRequest{})
	rebuilt, err := rpctransport.NewRpcRequest("ReactorcideAuth", "authenticate", reqPayload).WithAuth("golden-token").Encode()
	if err != nil {
		t.Fatalf("re-encode golden request: %v", err)
	}
	if !bytes.Equal(golden, rebuilt) {
		t.Fatalf("golden fixture is stale: fixture = %x, rpctransport now produces = %x", golden, rebuilt)
	}

	h := NewHandler(fakeAuth{}, NewStubUi())
	srv := httptest.NewServer(h)
	defer srv.Close()

	env := testPostEnvelope(t, srv, golden)
	if env.status != 0 || env.variant != "AuthenticateResponse" {
		t.Fatalf("status=%d variant=%q, want status=0 variant=AuthenticateResponse", env.status, env.variant)
	}
	resp, err := csilapi.DecodeAuthenticateResponse(env.payload)
	if err != nil {
		t.Fatalf("decode AuthenticateResponse: %v", err)
	}
	if !resp.Authenticated {
		t.Fatalf("Authenticated = false, want true")
	}
	if resp.Identity == nil || resp.Identity.UserId != "user-for-golden-token" {
		t.Fatalf("Identity = %+v, want UserId echoing the golden auth token", resp.Identity)
	}
}

// TestDispatcher_WireCompatServiceErrorResponseDecodesLikeTheClient proves
// the dispatcher's ServiceError response arm round-trips through
// rpctransport.DecodeRpcResponse exactly the way uiclient's
// CSILRPCTransport.Call decodes it — status 0, variant "ServiceError",
// payload present — the decode path that maps to *uiclient.ServiceCallError
// on the webapp side.
func TestDispatcher_WireCompatServiceErrorResponseDecodesLikeTheClient(t *testing.T) {
	h := NewHandler(NewStubAuth(), NewStubUi())
	srv := httptest.NewServer(h)
	defer srv.Close()

	reqPayload := csilapi.EncodeGetAuthConfigRequest(csilapi.GetAuthConfigRequest{})
	reqBody, err := rpctransport.NewRpcRequest("ReactorcideAuth", "get-auth-config", reqPayload).Encode()
	if err != nil {
		t.Fatalf("encode request: %v", err)
	}

	httpResp, err := http.Post(srv.URL+RpcPath, "application/cbor", bytes.NewReader(reqBody))
	if err != nil {
		t.Fatalf("POST %s: %v", RpcPath, err)
	}
	defer httpResp.Body.Close()
	respBody, err := io.ReadAll(httpResp.Body)
	if err != nil {
		t.Fatalf("read response body: %v", err)
	}

	// Decode exactly the way uiclient.CSILRPCTransport.Call does.
	resp, err := rpctransport.DecodeRpcResponse(respBody)
	if err != nil {
		t.Fatalf("rpctransport.DecodeRpcResponse: %v", err)
	}
	if !resp.Status.IsOk() {
		t.Fatalf("Status = %v, want ok (ServiceError rides status 0)", resp.Status)
	}
	if resp.Variant == nil || *resp.Variant != "ServiceError" {
		t.Fatalf("Variant = %v, want ServiceError", resp.Variant)
	}
	se, err := csilapi.DecodeServiceError(resp.Payload)
	if err != nil {
		t.Fatalf("decode ServiceError payload: %v", err)
	}
	if se.Code != "unimplemented" {
		t.Errorf("ServiceError.Code = %q, want unimplemented", se.Code)
	}
}
