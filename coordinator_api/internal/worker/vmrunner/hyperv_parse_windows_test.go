//go:build windows

package vmrunner

import "testing"

func TestParseVMIPv4(t *testing.T) {
	cases := []struct {
		name   string
		raw    string
		wantIP string
		wantOK bool
	}{
		{name: "empty", raw: "", wantOK: false},
		{name: "whitespace", raw: "   \n\t ", wantOK: false},
		{name: "json null", raw: "null", wantOK: false},
		{name: "single json string", raw: `"10.0.0.5"`, wantIP: "10.0.0.5", wantOK: true},
		{name: "json array v4 first", raw: `["10.0.0.7","fe80::1"]`, wantIP: "10.0.0.7", wantOK: true},
		{name: "json array v6 first", raw: `["fe80::abcd","192.168.1.20"]`, wantIP: "192.168.1.20", wantOK: true},
		{name: "json array only v6", raw: `["fe80::abcd","::1"]`, wantOK: false},
		{name: "skips apipa", raw: `["169.254.10.1","172.16.5.9"]`, wantIP: "172.16.5.9", wantOK: true},
		{name: "only apipa", raw: `["169.254.10.1"]`, wantOK: false},
		{name: "plain text fallback", raw: "10.1.2.3\nfe80::1\n", wantIP: "10.1.2.3", wantOK: true},
		{name: "space separated fallback", raw: "fe80::1 10.2.3.4", wantIP: "10.2.3.4", wantOK: true},
		{name: "garbage", raw: "not-an-ip", wantOK: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ip, ok := parseVMIPv4(tc.raw)
			if ok != tc.wantOK {
				t.Fatalf("parseVMIPv4(%q) ok=%v, want %v", tc.raw, ok, tc.wantOK)
			}
			if ip != tc.wantIP {
				t.Fatalf("parseVMIPv4(%q) ip=%q, want %q", tc.raw, ip, tc.wantIP)
			}
		})
	}
}

func TestPSQuote(t *testing.T) {
	cases := map[string]string{
		"Default Switch":     "'Default Switch'",
		`C:\vm\job.vhdx`:     `'C:\vm\job.vhdx'`,
		"reactorcide-vm-abc": "'reactorcide-vm-abc'",
		"weird'name":         "'weird''name'",
		"two'quotes'here":    "'two''quotes''here'",
		"":                   "''",
	}
	for in, want := range cases {
		if got := psQuote(in); got != want {
			t.Errorf("psQuote(%q) = %q, want %q", in, got, want)
		}
	}
}
