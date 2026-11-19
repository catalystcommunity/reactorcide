package egress

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The same vector is in egress_proxy/src/proxy.rs (credential_vector).
func TestCredentialVector(t *testing.T) {
	got := Credential([]byte("0123456789abcdef0123456789abcdef"), "job_a")
	want := "2c896a41f17c5e4c9702168deb85d829ad3d5a1a7d00ae400638f3d0330548e0"
	if got != want {
		t.Fatalf("credential = %s, want %s", got, want)
	}
	if Credential([]byte("0123456789abcdef0123456789abcdef"), "job_b") == want {
		t.Fatal("credential must depend on the job")
	}
}

func TestPlaceholders(t *testing.T) {
	a, err := NewPlaceholder()
	if err != nil {
		t.Fatal(err)
	}
	b, _ := NewPlaceholder()
	if a == b {
		t.Fatal("placeholders must be random")
	}
	if !IsPlaceholder(a) || len(a) != len(PlaceholderPrefix)+32 {
		t.Fatalf("bad placeholder %q", a)
	}
	if IsPlaceholder("Bearer " + a) {
		t.Fatal("a placeholder with a prefix is not one whole placeholder")
	}
	if got := FindPlaceholders.FindString("token " + a + " end"); got != a {
		t.Fatalf("find = %q", got)
	}
}

func TestValidation(t *testing.T) {
	for _, ok := range []string{"api.github.com", "*.example.com", "10.0.0.1", "*.svc.cluster.local", "octo.github.io", "*.app.herokuapp.com"} {
		if err := ValidateHostPattern(ok); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "*", "com", "a.*.com", "bad host.com", "-x.com",
		"localhost", "*.localhost", "api.localhost", "co.uk", "*.co.uk", "*.github.io", "*.herokuapp.com", "github.io", "*.com"} {
		if ValidateHostPattern(bad) == nil {
			t.Errorf("%q must be refused", bad)
		}
	}
	if ValidateHeaderFormat("Bearer {}") != nil || ValidateHeaderFormat("") != nil {
		t.Fatal("valid formats refused")
	}
	if ValidateHeaderFormat("Bearer") == nil || ValidateHeaderFormat("{} {}") == nil || ValidateHeaderFormat("a{}\r\n") == nil {
		t.Fatal("bad formats accepted")
	}
	if ValidateHeader("X-Api-Key") != nil || ValidateHeader("") != nil {
		t.Fatal("valid header refused")
	}
	if ValidateHeader("Host") == nil || ValidateHeader("Proxy-Authorization") == nil || ValidateHeader("bad header") == nil {
		t.Fatal("bad header accepted")
	}
}

func TestHostMatch(t *testing.T) {
	cases := []struct {
		p, h string
		ok   bool
	}{
		{"api.github.com", "API.github.com.", true},
		{"*.example.com", "a.b.example.com", true},
		{"*.example.com", "example.com", false},
		{"*.example.com", "badexample.com", false},
		{"api.github.com", "api.github.com.evil", false},
	}
	for _, c := range cases {
		if HostMatch(c.p, c.h) != c.ok {
			t.Errorf("HostMatch(%q, %q) != %v", c.p, c.h, c.ok)
		}
	}
}

func TestPolicyValidate(t *testing.T) {
	ph, _ := NewPlaceholder()
	p := JobPolicy{JobID: "j1", Grants: []Grant{{ID: "g", Placeholder: ph, Value: "v-123456789", Hosts: []string{"api.github.com"}, Format: "Bearer {}"}}}
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	dup := p
	dup.Grants = append(dup.Grants, p.Grants[0])
	if dup.Validate() == nil {
		t.Fatal("duplicate placeholder accepted")
	}
	noHost := p
	noHost.Grants = []Grant{{ID: "g", Placeholder: ph, Value: "v-123456789"}}
	if noHost.Validate() == nil {
		t.Fatal("grant without hosts accepted")
	}
	short := p
	short.Grants = []Grant{{ID: "g", Placeholder: ph, Value: "1234567", Hosts: []string{"api.github.com"}}}
	if err := short.Validate(); err == nil || strings.Contains(err.Error(), "1234567") {
		t.Fatalf("a grant value shorter than the scanner minimum must be refused without the value: %v", err)
	}
	if (&JobPolicy{}).Validate() == nil {
		t.Fatal("empty job id accepted")
	}
}

func TestClientRegisterAndUnregister(t *testing.T) {
	var got JobPolicy
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(401)
			w.Write([]byte(`{"code":401,"message":"control token required"}`))
			return
		}
		switch {
		case r.Method == http.MethodPut && r.URL.Path == "/v1/jobs/j1":
			json.NewDecoder(r.Body).Decode(&got)
			w.Write([]byte(`{"job_id":"j1","grants":1}`))
		case r.Method == http.MethodDelete && r.URL.Path == "/v1/jobs/j1":
			w.Write([]byte(`{"job_id":"j1","flows":3,"blocked":1,"secrets_used":["g"]}`))
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	ph, _ := NewPlaceholder()
	c := &Client{BaseURL: srv.URL + "/", Token: "tok"}
	p := &JobPolicy{JobID: "j1", Grants: []Grant{{ID: "g", SecretRef: "a:b", Placeholder: ph, Value: "secret-value", Hosts: []string{"api.github.com"}}}}
	if err := c.Register(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	if got.Grants[0].Value != "secret-value" || got.Grants[0].Placeholder != ph {
		t.Fatalf("proxy got %+v", got)
	}
	s, err := c.Unregister(context.Background(), "j1")
	if err != nil || s.Flows != 3 || s.Blocked != 1 || s.SecretsUsed[0] != "g" {
		t.Fatalf("summary %+v %v", s, err)
	}
	bad := &Client{BaseURL: srv.URL, Token: "wrong"}
	err = bad.Register(context.Background(), p)
	if se, ok := err.(*StatusError); !ok || se.Code != 401 || !strings.Contains(se.Message, "control token") {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(err.Error(), "secret-value") {
		t.Fatal("an error must not contain the secret")
	}
}

func TestControlHTTPClientTrustsOnlyTheProxyCA(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"job_id":"j1","grants":1}`))
	}))
	defer srv.Close()
	caPEM := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}))
	hc, err := ControlHTTPClient(caPEM)
	if err != nil {
		t.Fatal(err)
	}
	ph, _ := NewPlaceholder()
	p := &JobPolicy{JobID: "j1", Grants: []Grant{{ID: "g", Placeholder: ph, Value: "secret-value", Hosts: []string{"api.github.com"}}}}
	if err := (&Client{BaseURL: srv.URL, Token: "tok", HTTP: hc}).Register(context.Background(), p); err != nil {
		t.Fatalf("TLS with the proxy CA: %v", err)
	}
	if err := (&Client{BaseURL: srv.URL, Token: "tok"}).Register(context.Background(), p); err == nil {
		t.Fatal("a client without the proxy CA must not trust the control API")
	}
	if _, err := ControlHTTPClient("not a certificate"); err == nil {
		t.Fatal("a CA file without a certificate must be refused")
	}
}
