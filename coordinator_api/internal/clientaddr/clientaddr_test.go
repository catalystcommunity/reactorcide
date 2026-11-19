package clientaddr

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func request(remote string, forwarded ...string) *http.Request {
	r := httptest.NewRequest("POST", "/api/v1/federation/token", nil)
	r.RemoteAddr = remote
	for _, value := range forwarded {
		r.Header.Add("X-Forwarded-For", value)
	}
	return r
}

func TestSource(t *testing.T) {
	trusted, err := Parse(" 10.0.0.0/8, 192.0.2.7 ,fd00::/8,")
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name      string
		trusted   Trusted
		remote    string
		forwarded []string
		want      string
	}{
		{"default: the TCP peer, header ignored", Trusted{}, "203.0.113.5:4000", []string{"198.51.100.1"}, "203.0.113.5"},
		{"untrusted peer: header ignored", trusted, "203.0.113.5:4000", []string{"198.51.100.1"}, "203.0.113.5"},
		{"trusted peer, no header", trusted, "10.1.2.3:4000", nil, "10.1.2.3"},
		{"trusted peer: right-most untrusted", trusted, "10.1.2.3:4000", []string{"198.51.100.9, 198.51.100.1, 10.4.4.4"}, "198.51.100.1"},
		{"a client-set left entry is not used", trusted, "10.1.2.3:4000", []string{"1.1.1.1", "198.51.100.1"}, "198.51.100.1"},
		{"single trusted address", trusted, "192.0.2.7:80", []string{"198.51.100.2"}, "198.51.100.2"},
		{"all hops trusted: the left-most", trusted, "10.1.2.3:4000", []string{"10.9.9.9, 10.8.8.8"}, "10.9.9.9"},
		{"invalid entry stops the walk", trusted, "10.1.2.3:4000", []string{"198.51.100.3, garbage, 10.5.5.5"}, "10.5.5.5"},
		{"entry with a port", trusted, "10.1.2.3:4000", []string{"198.51.100.4:5555"}, "198.51.100.4"},
		{"IPv6 peer and hop", trusted, "[fd00::1]:443", []string{"2001:db8::5"}, "2001:db8::5"},
		{"IPv4-mapped peer", trusted, "[::ffff:10.1.2.3]:4000", []string{"198.51.100.6"}, "198.51.100.6"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.trusted.Source(request(tc.remote, tc.forwarded...)); got != tc.want {
				t.Fatalf("Source = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestParseRejectsInvalid(t *testing.T) {
	for _, list := range []string{"10.0.0.0/33", "not-an-ip", "10.0.0.0/8,bad/8"} {
		if _, err := Parse(list); err == nil {
			t.Fatalf("Parse(%q) = nil error", list)
		}
	}
	if trusted, err := Parse(""); err != nil || !trusted.Empty() {
		t.Fatalf("empty list: %v, %v", trusted, err)
	}
}
