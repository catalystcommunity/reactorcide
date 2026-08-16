package transportsecurity

import (
	"net/http"
	"testing"
)

func TestValidateURL(t *testing.T) {
	for _, tt := range []struct {
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
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateURL(tt.url, tt.allow, "test transport")
			if (err == nil) != tt.ok {
				t.Fatalf("ValidateURL() error = %v, want success %v", err, tt.ok)
			}
		})
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
