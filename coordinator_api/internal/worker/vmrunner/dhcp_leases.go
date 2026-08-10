package vmrunner

import (
	"bufio"
	"os"
	"strings"
)

// defaultDHCPLeasesPath is where macOS' bootpd (the DHCP server behind
// Virtualization.framework's NAT network) records leases it hands out to
// guests. The darwin VMLifecycle boots each guest with a known,
// locally-administered MAC and then matches that MAC against this file to
// discover the IP the guest was assigned.
const defaultDHCPLeasesPath = "/var/db/dhcpd_leases"

// parseDHCPLeases scans a macOS dhcpd_leases file for the lease whose
// hardware address matches mac and returns its ip_address. It lives in a
// platform-neutral file so tests can run without the Cgo/vz toolchain or an
// actual VM.
//
// The file bootpd writes looks like:
//
//	{
//		name=guest
//		ip_address=192.168.64.7
//		hw_address=1,a2:3b:4:5:6d:7e
//		identifier=1,a2:3b:4:5:6d:7e
//		lease=0x664f...
//	}
//
// Two format quirks this handles:
//   - hw_address carries a leading "<htype>," (1 = ethernet) before the MAC.
//   - bootpd strips leading zeros from each octet ("4" not "04"), so a naive
//     string compare against vz's MACAddress.String() ("a2:3b:04:05:6d:7e")
//     misses. normalizeMAC canonicalizes both sides before comparing.
//
// When a MAC appears more than once (a guest re-leasing across boots), the
// LAST matching entry wins, since bootpd appends and the most recent lease
// is the live one.
func parseDHCPLeases(path, mac string) (string, bool) {
	f, err := os.Open(path)
	if err != nil {
		return "", false
	}
	defer f.Close()

	want := normalizeMAC(mac)
	if want == "" {
		return "", false
	}

	var (
		curIP   string
		curMAC  string
		foundIP string
		found   bool
	)

	flush := func() {
		if curMAC != "" && curMAC == want && curIP != "" {
			foundIP = curIP
			found = true
		}
		curIP = ""
		curMAC = ""
	}

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		switch {
		case line == "{":
			curIP = ""
			curMAC = ""
		case line == "}":
			flush()
		case strings.HasPrefix(line, "ip_address="):
			curIP = strings.TrimSpace(strings.TrimPrefix(line, "ip_address="))
		case strings.HasPrefix(line, "hw_address="):
			v := strings.TrimSpace(strings.TrimPrefix(line, "hw_address="))
			// Drop the leading "<htype>," (e.g. "1,") if present.
			if comma := strings.IndexByte(v, ','); comma >= 0 {
				v = v[comma+1:]
			}
			curMAC = normalizeMAC(v)
		}
	}
	// Tolerate a trailing record with no closing brace.
	flush()

	return foundIP, found
}

// normalizeMAC canonicalizes a MAC string for comparison: lowercased, with
// each colon-separated octet stripped of leading zeros (but never emptied).
// This bridges vz's zero-padded MACAddress.String() and bootpd's
// zero-stripped hw_address so the two compare equal.
func normalizeMAC(mac string) string {
	mac = strings.TrimSpace(strings.ToLower(mac))
	if mac == "" {
		return ""
	}
	octets := strings.Split(mac, ":")
	for i, o := range octets {
		o = strings.TrimLeft(o, "0")
		if o == "" {
			o = "0"
		}
		octets[i] = o
	}
	return strings.Join(octets, ":")
}
