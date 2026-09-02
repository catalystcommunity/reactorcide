package test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	rpctransport "github.com/catalystcommunity/csilgen/transports/go"
)

// These exercise the webapp's whole surface against a live coordinator started
// by setup_test.go. That surface is now three things: the SPA shell, the
// CSIL-RPC bridge, and the cookie-owning auth endpoints. There are no
// server-rendered pages left to assert HTML against.

func TestAppShellIsServedForEveryClientRoute(t *testing.T) {
	// A deep link must return the shell rather than a 404, or a pasted URL and
	// a hard refresh both break.
	for _, path := range []string{"/app/", "/app/jobs", "/app/workflows/01ABC", "/app/projects/new"} {
		t.Run(path, func(t *testing.T) {
			resp, err := http.Get(webBaseURL + path)
			if err != nil {
				t.Fatalf("GET %s: %v", path, err)
			}
			defer resp.Body.Close()

			// 503 is the deliberate answer when the binary carries no UI build
			// (see embedui.IndexHTML); both mean "the shell was served".
			if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusServiceUnavailable {
				t.Fatalf("GET %s: status %d, want the SPA shell", path, resp.StatusCode)
			}
			if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
				t.Errorf("GET %s: Content-Type = %q, want HTML", path, ct)
			}
			if cc := resp.Header.Get("Cache-Control"); cc != "no-store" {
				t.Errorf("GET %s: Cache-Control = %q, want no-store; a cached shell "+
					"references asset names that vanish on the next deploy", path, cc)
			}
		})
	}
}

func TestHealthCheckAtRoot(t *testing.T) {
	resp, err := http.Get(webBaseURL + "/")
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /: status %d, want 200", resp.StatusCode)
	}
	var body map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode health body: %v", err)
	}
	if body["status"] != "ok" {
		t.Errorf("health status = %q, want ok", body["status"])
	}
}

// callBridge posts a CSIL-RPC envelope the way the SPA's generated client does.
func callBridge(t *testing.T, client *http.Client, service, op string, payload []byte) *http.Response {
	t.Helper()
	envelope, err := rpctransport.NewRpcRequest(service, op, payload).Encode()
	if err != nil {
		t.Fatalf("encode envelope: %v", err)
	}
	req, err := http.NewRequest(http.MethodPost, webBaseURL+"/app/rpc", bytes.NewReader(envelope))
	if err != nil {
		t.Fatalf("build bridge request: %v", err)
	}
	req.Header.Set("Content-Type", "application/cbor")
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("POST /app/rpc %s/%s: %v", service, op, err)
	}
	return resp
}

// TestBridgeReachesTheCoordinatorAnonymously covers logged-out browsing: an
// anonymous caller gets an answer about public data rather than an error.
//
// The variant check at the end is the important part, and an earlier version of
// this test lacked it. A CSIL ServiceError rides at transport status 0 -- it is
// a successful transport carrying a typed failure -- so asserting only the
// status passes even when the operation itself failed. It did: an anonymous
// caller bound "" into a uuid comparison and PostgreSQL raised 22P02, and this
// test was green throughout. Assert the success arm, not the carrier.
func TestBridgeReachesTheCoordinatorAnonymously(t *testing.T) {
	// An empty CBOR map is a valid ListJobsRequest: every field is optional.
	resp := callBridge(t, nil, "ReactorcideUi", "list-jobs", []byte{0xa0})
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("bridge status = %d, want 200; body = %s", resp.StatusCode, body)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/cbor" {
		t.Errorf("Content-Type = %q, want application/cbor", ct)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read bridge response: %v", err)
	}
	decoded, err := rpctransport.DecodeRpcResponse(body)
	if err != nil {
		t.Fatalf("decode bridge response: %v", err)
	}
	if !decoded.Status.IsOk() {
		message := ""
		if decoded.Error != nil {
			message = *decoded.Error
		}
		t.Fatalf("transport status %d: %s", decoded.Status.Code(), message)
	}

	variant := ""
	if decoded.Variant != nil {
		variant = *decoded.Variant
	}
	if variant == "ServiceError" {
		t.Fatalf("list-jobs failed for an anonymous caller: %s", describeServiceError(decoded.Payload))
	}
	if variant != "ListJobsResponse" {
		t.Fatalf("variant = %q, want ListJobsResponse", variant)
	}
}

// describeServiceError renders the {code, message} error arm for a test
// failure message. Kept deliberately crude: it exists to make a failure
// readable, not to be a decoder.
func describeServiceError(payload []byte) string {
	text := make([]rune, 0, len(payload))
	for _, b := range payload {
		if b >= 0x20 && b < 0x7f {
			text = append(text, rune(b))
		} else {
			text = append(text, ' ')
		}
	}
	return strings.TrimSpace(string(text))
}

// TestAnonymousReadsSucceedAgainstRealSQL covers every list operation an
// anonymous caller can reach, against a REAL PostgreSQL.
//
// This exists because the in-memory fakes in internal/uiapi evaluate visibility
// in Go, so they cannot see how the SQL predicate behaves. Both operations below
// share visibilityPredicateSQL and both were broken by the same uuid coercion;
// only one had a test, so only one would have been caught.
func TestAnonymousReadsSucceedAgainstRealSQL(t *testing.T) {
	for _, op := range []struct{ name, want string }{
		{"list-jobs", "ListJobsResponse"},
		{"list-workflows", "ListWorkflowsResponse"},
		{"list-projects", "ListProjectsResponse"},
	} {
		t.Run(op.name, func(t *testing.T) {
			resp := callBridge(t, nil, "ReactorcideUi", op.name, []byte{0xa0})
			defer resp.Body.Close()

			body, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Fatalf("read response: %v", err)
			}
			decoded, err := rpctransport.DecodeRpcResponse(body)
			if err != nil {
				t.Fatalf("decode response: %v", err)
			}

			variant := ""
			if decoded.Variant != nil {
				variant = *decoded.Variant
			}
			if variant != op.want {
				t.Fatalf("%s: variant = %q, want %q; error arm = %s",
					op.name, variant, op.want, describeServiceError(decoded.Payload))
			}
		})
	}
}

func TestBridgeRefusesTheWorkerService(t *testing.T) {
	resp := callBridge(t, nil, "ReactorcideWorker", "claim-lease", []byte{0xa0})
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("status = %d, want 403: the worker protocol must not be browser-reachable", resp.StatusCode)
	}
}

func TestAuthConfigIsReadableBeforeLogin(t *testing.T) {
	resp, err := http.Get(webBaseURL + "/app/auth/config")
	if err != nil {
		t.Fatalf("GET /app/auth/config: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var config struct {
		AuthMode     string `json:"auth_mode"`
		LoginEnabled bool   `json:"login_enabled"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&config); err != nil {
		t.Fatalf("decode auth config: %v", err)
	}
	if config.AuthMode == "" {
		t.Error("auth_mode should be reported so the SPA knows whether to offer sign-in")
	}
}

func TestSessionEndpointReportsLoggedOut(t *testing.T) {
	resp, err := http.Get(webBaseURL + "/app/auth/session")
	if err != nil {
		t.Fatalf("GET /app/auth/session: %v", err)
	}
	defer resp.Body.Close()

	var session struct {
		LoggedIn bool `json:"logged_in"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&session); err != nil {
		t.Fatalf("decode session: %v", err)
	}
	if session.LoggedIn {
		t.Error("a request with no cookie must report logged_in false")
	}
}
