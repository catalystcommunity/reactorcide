package transportsecurity

import (
	"net/http"
	"testing"
)

func TestValidateURL(t *testing.T) {
	tests := []struct {
		name  string
		url   string
		allow bool
		ok    bool
	}{
		{name: "https", url: "https://coordinator.example.com", ok: true},
		{name: "http rejected", url: "http://127.0.0.1:6080"},
		{name: "explicit insecure", url: "http://127.0.0.1:6080", allow: true, ok: true},
		{name: "missing host", url: "https:///rpc"},
		{name: "unsupported scheme", url: "ssh://host"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateURL(tt.url, tt.allow, "test transport")
			if (err == nil) != tt.ok {
				t.Fatalf("ValidateURL() error = %v, want success %v", err, tt.ok)
			}
		})
	}
}

func TestValidateProperties(t *testing.T) {
	if err := Validate(Properties{Encrypted: true, PeerAuthenticated: false}, false, "native RPC"); err == nil {
		t.Fatal("expected unauthenticated encrypted transport to be rejected")
	}
	if err := Validate(Properties{Encrypted: true, PeerAuthenticated: true}, false, "native RPC"); err != nil {
		t.Fatalf("expected secure native transport: %v", err)
	}
}

func TestHTTPClientRejectsInsecureRedirect(t *testing.T) {
	client := HTTPClient(nil, false, "test transport")
	req, err := http.NewRequest(http.MethodGet, "http://coordinator.example.com/rpc", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.CheckRedirect(req, nil); err == nil {
		t.Fatal("expected redirect without TLS to be rejected")
	}

	client = HTTPClient(nil, true, "test transport")
	if err := client.CheckRedirect(req, nil); err != nil {
		t.Fatalf("explicit insecure redirect was rejected: %v", err)
	}
}
