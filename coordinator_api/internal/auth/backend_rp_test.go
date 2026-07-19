package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	api "github.com/catalystcommunity/linkkeys/sdks/local-rp/go/generated"
)

// fakeRPTransport is a hand-rolled RPTransport test double: canned
// CBOR-encoded responses (or errors) per op, no real network/TLS. This is
// the seam the task description calls out explicitly: "Abstract the
// transport behind an interface so tests use a fake RP."
type fakeRPTransport struct {
	responses map[string][]byte
	errs      map[string]error
	calls     []string
}

func newFakeRPTransport() *fakeRPTransport {
	return &fakeRPTransport{responses: map[string][]byte{}, errs: map[string]error{}}
}

func (f *fakeRPTransport) Call(_ context.Context, op string, _ []byte) ([]byte, error) {
	f.calls = append(f.calls, op)
	if err, ok := f.errs[op]; ok {
		return nil, err
	}
	resp, ok := f.responses[op]
	if !ok {
		return nil, fmt.Errorf("fakeRPTransport: no response configured for op %q", op)
	}
	return resp, nil
}

func (f *fakeRPTransport) calledOp(op string) bool {
	for _, c := range f.calls {
		if c == op {
			return true
		}
	}
	return false
}

// fakeDNSResolver always fails TXT lookups, forcing resolveAPIBase's
// https://<domain> fallback — sufficient for these tests since the API base
// value itself is opaque plumbing to userinfo-fetch's request payload.
type fakeDNSResolver struct{}

func (fakeDNSResolver) TxtLookup(string) ([]string, error) {
	return nil, fmt.Errorf("no DNS in tests")
}

func newTestRPBackend(transport *fakeRPTransport) *RPBackend {
	return &RPBackend{transport: transport, dns: fakeDNSResolver{}, now: time.Now}
}

func strPtr(s string) *string { return &s }

func TestRPBackendBeginLogin(t *testing.T) {
	transport := newFakeRPTransport()
	transport.responses["sign-request"] = api.EncodeRpSignResponse(api.RpSignResponse{SignedRequest: "signed-request-blob"})
	backend := newTestRPBackend(transport)

	redirectURL, pendingBlob, err := backend.BeginLogin(context.Background(), "alice@idp.example.com", "https://app.example.com/auth/callback")
	if err != nil {
		t.Fatalf("BeginLogin() error = %v", err)
	}
	if !strings.HasPrefix(redirectURL, "https://idp.example.com/auth/authorize?") {
		t.Fatalf("redirectURL = %q, want it to start with https://idp.example.com/auth/authorize?", redirectURL)
	}
	if !strings.Contains(redirectURL, "signed_request=signed-request-blob") {
		t.Fatalf("redirectURL = %q, missing signed_request", redirectURL)
	}
	if !strings.Contains(redirectURL, "user_hint=alice") {
		t.Fatalf("redirectURL = %q, missing user_hint for the handle in the selector", redirectURL)
	}

	var pending rpPending
	if err := json.Unmarshal(pendingBlob, &pending); err != nil {
		t.Fatalf("pendingBlob did not unmarshal: %v", err)
	}
	if pending.UserDomain != "idp.example.com" {
		t.Fatalf("pending.UserDomain = %q, want idp.example.com", pending.UserDomain)
	}
	if pending.Nonce == "" {
		t.Fatal("pending.Nonce must not be empty")
	}
}

func TestRPBackendCompleteLoginRejectsUnverified(t *testing.T) {
	pendingBlob, err := json.Marshal(rpPending{Nonce: "the-nonce", UserDomain: "idp.example.com"})
	if err != nil {
		t.Fatalf("marshal pending: %v", err)
	}

	transport := newFakeRPTransport()
	transport.responses["decrypt-token"] = api.EncodeRpDecryptResponse(api.RpDecryptResponse{SignedAssertion: "signed-assertion-blob"})
	transport.responses["verify-assertion"] = api.EncodeRpVerifyResponse(api.RpVerifyResponse{
		Verified: false, // MUST be rejected regardless of a nil transport error
		Assertion: api.IdentityAssertion{
			UserId: "mallory",
			Domain: "idp.example.com",
			Nonce:  "the-nonce",
		},
	})
	backend := newTestRPBackend(transport)

	_, err = backend.CompleteLogin(context.Background(), pendingBlob, "https://app.example.com/auth/callback?encrypted_token=abc123")
	if err != ErrAssertionNotVerified {
		t.Fatalf("CompleteLogin() error = %v, want ErrAssertionNotVerified", err)
	}
	if transport.calledOp("userinfo-fetch") {
		t.Fatal("userinfo-fetch must not be called when the assertion did not verify")
	}
}

func TestRPBackendCompleteLoginHappyPath(t *testing.T) {
	pendingBlob, err := json.Marshal(rpPending{Nonce: "the-nonce", UserDomain: "idp.example.com"})
	if err != nil {
		t.Fatalf("marshal pending: %v", err)
	}

	transport := newFakeRPTransport()
	transport.responses["decrypt-token"] = api.EncodeRpDecryptResponse(api.RpDecryptResponse{SignedAssertion: "signed-assertion-blob"})
	transport.responses["verify-assertion"] = api.EncodeRpVerifyResponse(api.RpVerifyResponse{
		Verified: true,
		Assertion: api.IdentityAssertion{
			UserId:      "alice-uuid",
			Domain:      "idp.example.com",
			Nonce:       "the-nonce",
			DisplayName: strPtr("Alice Assertion"),
		},
	})
	transport.responses["userinfo-fetch"] = api.EncodeUserInfo(api.UserInfo{
		UserId:      "alice-uuid",
		Domain:      "idp.example.com",
		DisplayName: "Alice Userinfo",
		Claims: []api.Claim{
			{ClaimType: "handle", ClaimValue: []byte("alice")},
			{ClaimType: "email", ClaimValue: []byte("alice@idp.example.com")},
		},
	})
	backend := newTestRPBackend(transport)

	identity, err := backend.CompleteLogin(context.Background(), pendingBlob, "https://app.example.com/auth/callback?encrypted_token=abc123")
	if err != nil {
		t.Fatalf("CompleteLogin() error = %v", err)
	}
	if identity.Subject != "alice-uuid" {
		t.Fatalf("Subject = %q, want alice-uuid", identity.Subject)
	}
	if identity.Domain != "idp.example.com" {
		t.Fatalf("Domain = %q, want idp.example.com", identity.Domain)
	}
	if identity.Handle != "alice" {
		t.Fatalf("Handle = %q, want alice", identity.Handle)
	}
	// userinfo-fetch's display_name should win once it's available.
	if identity.DisplayName != "Alice Userinfo" {
		t.Fatalf("DisplayName = %q, want Alice Userinfo", identity.DisplayName)
	}
	if identity.Claims["email"] != "alice@idp.example.com" {
		t.Fatalf("Claims[email] = %q, want alice@idp.example.com", identity.Claims["email"])
	}

	wantOps := []string{"decrypt-token", "verify-assertion", "userinfo-fetch"}
	if len(transport.calls) != len(wantOps) {
		t.Fatalf("transport.calls = %v, want %v", transport.calls, wantOps)
	}
	for i, op := range wantOps {
		if transport.calls[i] != op {
			t.Fatalf("transport.calls[%d] = %q, want %q", i, transport.calls[i], op)
		}
	}
}

func TestRPBackendCompleteLoginDomainMismatch(t *testing.T) {
	pendingBlob, err := json.Marshal(rpPending{Nonce: "the-nonce", UserDomain: "idp.example.com"})
	if err != nil {
		t.Fatalf("marshal pending: %v", err)
	}

	transport := newFakeRPTransport()
	transport.responses["decrypt-token"] = api.EncodeRpDecryptResponse(api.RpDecryptResponse{SignedAssertion: "signed-assertion-blob"})
	transport.responses["verify-assertion"] = api.EncodeRpVerifyResponse(api.RpVerifyResponse{
		Verified: true,
		Assertion: api.IdentityAssertion{
			UserId: "attacker",
			Domain: "attacker-controlled.example.com", // does not match pending.UserDomain
			Nonce:  "the-nonce",
		},
	})
	backend := newTestRPBackend(transport)

	if _, err := backend.CompleteLogin(context.Background(), pendingBlob, "https://app.example.com/callback?encrypted_token=abc"); err == nil {
		t.Fatal("CompleteLogin() succeeded despite a domain mismatch, want an error")
	}
}

func TestRPBackendCompleteLoginNonceMismatch(t *testing.T) {
	pendingBlob, err := json.Marshal(rpPending{Nonce: "the-real-nonce", UserDomain: "idp.example.com"})
	if err != nil {
		t.Fatalf("marshal pending: %v", err)
	}

	transport := newFakeRPTransport()
	transport.responses["decrypt-token"] = api.EncodeRpDecryptResponse(api.RpDecryptResponse{SignedAssertion: "signed-assertion-blob"})
	transport.responses["verify-assertion"] = api.EncodeRpVerifyResponse(api.RpVerifyResponse{
		Verified: true,
		Assertion: api.IdentityAssertion{
			UserId: "alice",
			Domain: "idp.example.com",
			Nonce:  "a-replayed-nonce", // does not match pending.Nonce
		},
	})
	backend := newTestRPBackend(transport)

	if _, err := backend.CompleteLogin(context.Background(), pendingBlob, "https://app.example.com/callback?encrypted_token=abc"); err == nil {
		t.Fatal("CompleteLogin() succeeded despite a nonce mismatch, want an error")
	}
}

func TestRPBackendCompleteLoginMissingEncryptedToken(t *testing.T) {
	pendingBlob, err := json.Marshal(rpPending{Nonce: "n", UserDomain: "idp.example.com"})
	if err != nil {
		t.Fatalf("marshal pending: %v", err)
	}
	backend := newTestRPBackend(newFakeRPTransport())

	if _, err := backend.CompleteLogin(context.Background(), pendingBlob, "https://app.example.com/callback"); err == nil {
		t.Fatal("CompleteLogin() succeeded despite a missing encrypted_token, want an error")
	}
}
