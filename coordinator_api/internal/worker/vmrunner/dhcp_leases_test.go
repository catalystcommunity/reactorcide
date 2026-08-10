package vmrunner

import (
	"os"
	"path/filepath"
	"testing"
)

const sampleLeases = `{
	name=guest-a
	ip_address=192.168.64.5
	hw_address=1,a2:3b:04:05:6d:7e
	identifier=1,a2:3b:04:05:6d:7e
	lease=0x664f0000
}
{
	name=guest-b
	ip_address=192.168.64.6
	hw_address=1,0a:0b:0c:0d:0e:0f
	identifier=1,0a:0b:0c:0d:0e:0f
	lease=0x664f1111
}
`

func writeLeases(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "dhcpd_leases")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatalf("write leases: %v", err)
	}
	return p
}

func TestParseDHCPLeases_MatchesZeroPaddedMAC(t *testing.T) {
	p := writeLeases(t, sampleLeases)
	// vz's MACAddress.String() zero-pads octets; the lease file strips them.
	ip, ok := parseDHCPLeases(p, "A2:3B:04:05:6D:7E")
	if !ok {
		t.Fatalf("expected to find lease")
	}
	if ip != "192.168.64.5" {
		t.Fatalf("got ip %q, want 192.168.64.5", ip)
	}
}

func TestParseDHCPLeases_MatchesZeroStrippedMAC(t *testing.T) {
	p := writeLeases(t, sampleLeases)
	// A MAC written zero-stripped in the file, queried zero-padded.
	ip, ok := parseDHCPLeases(p, "0a:0b:0c:0d:0e:0f")
	if !ok {
		t.Fatalf("expected to find lease")
	}
	if ip != "192.168.64.6" {
		t.Fatalf("got ip %q, want 192.168.64.6", ip)
	}
}

func TestParseDHCPLeases_NoMatch(t *testing.T) {
	p := writeLeases(t, sampleLeases)
	if _, ok := parseDHCPLeases(p, "ff:ff:ff:ff:ff:ff"); ok {
		t.Fatalf("expected no match")
	}
}

func TestParseDHCPLeases_LastMatchWins(t *testing.T) {
	content := sampleLeases + `{
	name=guest-a-again
	ip_address=192.168.64.99
	hw_address=1,a2:3b:4:5:6d:7e
	lease=0x664f2222
}
`
	p := writeLeases(t, content)
	ip, ok := parseDHCPLeases(p, "a2:3b:04:05:6d:7e")
	if !ok {
		t.Fatalf("expected to find lease")
	}
	if ip != "192.168.64.99" {
		t.Fatalf("got ip %q, want 192.168.64.99 (most recent lease)", ip)
	}
}

func TestParseDHCPLeases_MissingFile(t *testing.T) {
	if _, ok := parseDHCPLeases("/nonexistent/dhcpd_leases", "a2:3b:04:05:6d:7e"); ok {
		t.Fatalf("expected no match for missing file")
	}
}

func TestNormalizeMAC(t *testing.T) {
	cases := map[string]string{
		"A2:3B:04:05:6D:7E": "a2:3b:4:5:6d:7e",
		"a2:3b:4:5:6d:7e":   "a2:3b:4:5:6d:7e",
		"00:00:00:00:00:00": "0:0:0:0:0:0",
		"":                  "",
	}
	for in, want := range cases {
		if got := normalizeMAC(in); got != want {
			t.Errorf("normalizeMAC(%q) = %q, want %q", in, got, want)
		}
	}
}
