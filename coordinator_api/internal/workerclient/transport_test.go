package workerclient

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	rpctransport "github.com/catalystcommunity/csilgen/transports/go"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/workerclient/csilapi"
	"github.com/stretchr/testify/require"
)

// fakeCoordinatorHandler is a minimal CSIL-RPC envelope-in-body HTTP handler
// exercising the real wire format (unlike coordinatorworker's tests, which
// substitute a fake in-process client) -- it proves CSILRPCTransport/Client
// actually round-trip CBOR envelopes correctly, including the auth field
// carrying the worker session on every call after Register.
type fakeCoordinatorHandler struct {
	t *testing.T
	// dispatch is called per decoded request with the op name, decoded
	// auth (nil if absent), and payload bytes; it returns the response
	// RpcResponse to encode and write back.
	dispatch func(op string, auth *string, payload []byte) rpctransport.RpcResponse
}

func (h *fakeCoordinatorHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.t.Helper()
	require.Equal(h.t, "/csil/v1/rpc", r.URL.Path)
	full, err := io.ReadAll(r.Body)
	require.NoError(h.t, err)

	req, err := rpctransport.DecodeRpcRequest(full)
	require.NoError(h.t, err)
	require.Equal(h.t, "ReactorcideWorker", req.Service)

	resp := h.dispatch(req.Op, req.Auth, req.Payload)
	encoded, err := resp.Encode()
	require.NoError(h.t, err)

	w.Header().Set("Content-Type", "application/cbor")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(encoded)
}

// TestClient_RegisterThenRequestJob_CarriesSessionInAuth drives Register
// over a real HTTP+CBOR round trip against a fake coordinator, then asserts
// the session Register returned is automatically attached to the envelope
// "auth" field on the very next call (RequestJob) without the caller having
// to do anything -- this is the behavior WORKERS_PLAN.md's protocol
// depends on ("Rides the CSIL envelope auth field on every subsequent
// call").
func TestClient_RegisterThenRequestJob_CarriesSessionInAuth(t *testing.T) {
	const wantSession = "sess-abc123"

	handler := &fakeCoordinatorHandler{t: t}
	handler.dispatch = func(op string, auth *string, payload []byte) rpctransport.RpcResponse {
		switch op {
		case "register":
			require.Nil(t, auth, "Register must not carry a session auth -- none exists yet")
			req, err := csilapi.DecodeRegisterRequest(payload)
			require.NoError(t, err)
			require.Equal(t, "enroll-token-xyz", req.EnrollmentToken)
			require.Equal(t, "worker-key-1", req.WorkerInfo.WorkerKey)
			resp := csilapi.RegisterResponse{WorkerSession: wantSession, WorkerId: "worker-1", HeartbeatInterval: 15}
			return rpctransport.NewRpcResponseOk("RegisterResponse", csilapi.EncodeRegisterResponse(resp))
		case "request-job":
			require.NotNil(t, auth, "RequestJob must carry the session auth")
			require.Equal(t, wantSession, *auth)
			resp := csilapi.RequestJobResponse{HasLease: false}
			return rpctransport.NewRpcResponseOk("RequestJobResponse", csilapi.EncodeRequestJobResponse(resp))
		default:
			t.Fatalf("unexpected op %q", op)
			return rpctransport.RpcResponse{}
		}
	}

	server := httptest.NewServer(handler)
	defer server.Close()

	c := New(server.URL)

	regResp, err := c.Register(context.Background(), "enroll-token-xyz", csilapi.WorkerInfo{WorkerKey: "worker-key-1", Os: "linux", Arch: "amd64"})
	require.NoError(t, err)
	require.Equal(t, wantSession, regResp.WorkerSession)
	require.Equal(t, wantSession, c.Session(), "Client must store the session returned by Register")

	jobResp, err := c.RequestJob(context.Background(), csilapi.WorkerCharacteristics{Os: "linux", Arch: "amd64"})
	require.NoError(t, err)
	require.False(t, jobResp.HasLease)
}

// TestClient_ServiceError_TranslatesToServiceCallError asserts a
// "ServiceError" response variant surfaces as a *ServiceCallError with the
// coordinator's code/message, not a generic transport error.
func TestClient_ServiceError_TranslatesToServiceCallError(t *testing.T) {
	handler := &fakeCoordinatorHandler{t: t}
	handler.dispatch = func(op string, auth *string, payload []byte) rpctransport.RpcResponse {
		se := csilapi.ServiceError{Code: "unauthorized", Message: "a valid worker session is required"}
		return rpctransport.NewRpcResponseOk("ServiceError", csilapi.EncodeServiceError(se))
	}
	server := httptest.NewServer(handler)
	defer server.Close()

	c := New(server.URL)
	c.SetSession("some-stale-session")

	_, err := c.Heartbeat(context.Background(), "running", nil)
	require.Error(t, err)
	var svcErr *ServiceCallError
	require.ErrorAs(t, err, &svcErr)
	require.Equal(t, "unauthorized", svcErr.Code)
}

// TestClient_SetSession_OverridesStoredToken asserts SetSession can be used
// to seed a session without a live Register round trip (used by callers
// that persisted a still-valid session, and by other tests).
func TestClient_SetSession_OverridesStoredToken(t *testing.T) {
	var sawAuth *string
	handler := &fakeCoordinatorHandler{t: t}
	handler.dispatch = func(op string, auth *string, payload []byte) rpctransport.RpcResponse {
		sawAuth = auth
		resp := csilapi.HeartbeatResponse{}
		return rpctransport.NewRpcResponseOk("HeartbeatResponse", csilapi.EncodeHeartbeatResponse(resp))
	}
	server := httptest.NewServer(handler)
	defer server.Close()

	c := New(server.URL)
	c.SetSession("preexisting-session")

	_, err := c.Heartbeat(context.Background(), "idle", nil)
	require.NoError(t, err)
	require.NotNil(t, sawAuth)
	require.Equal(t, "preexisting-session", *sawAuth)
}
