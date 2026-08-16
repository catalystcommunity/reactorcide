package vmrunner

import (
	"encoding/json"
	"net"
	"strings"
)

// parseVMIPv4 extracts the first usable IPv4 address from the output of
//
//	Get-VMNetworkAdapter -VMName <name> | Select-Object -ExpandProperty IPAddresses | ConvertTo-Json -Compress
//
// which the Hyper-V lifecycle polls to discover a booted guest's IP (this
// requires the guest's Hyper-V integration / Data Exchange service to be
// running so the host can see the guest-reported addresses).
//
// It has no live-VM dependency, so tests can run without a Hyper-V host.
//
// ConvertTo-Json renders the IPAddresses collection three different ways
// depending on its length, and parseVMIPv4 tolerates all of them plus a raw
// (non-JSON) newline/space separated fallback:
//   - empty collection  -> "" or "null"
//   - a single address  -> a bare JSON string, e.g. "10.0.0.5"
//   - multiple addresses -> a JSON array, e.g. ["10.0.0.5","fe80::1"]
//
// The first parseable IPv4 wins; IPv6 entries and APIPA link-local addresses
// (169.254.0.0/16, which Windows self-assigns when DHCP has not yet completed)
// are skipped so a half-booted guest does not resolve to a dead address.
func parseVMIPv4(raw string) (string, bool) {
	addresses := parseVMIPv4s(raw)
	if len(addresses) == 0 {
		return "", false
	}
	return addresses[0], true
}

// parseVMIPv4s returns all usable IPv4 candidates. Hyper-V can briefly report
// a stale address copied from the sealed base image before the clone publishes
// its current DHCP lease, so the Windows lifecycle tests every candidate for
// reachability instead of accepting the first value.
func parseVMIPv4s(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.EqualFold(raw, "null") {
		return nil
	}

	var candidates []string
	switch {
	case strings.HasPrefix(raw, "["):
		var arr []string
		if err := json.Unmarshal([]byte(raw), &arr); err == nil {
			candidates = arr
		}
	case strings.HasPrefix(raw, `"`):
		var s string
		if err := json.Unmarshal([]byte(raw), &s); err == nil {
			candidates = []string{s}
		}
	}
	if candidates == nil {
		// Not JSON (e.g. ConvertTo-Json omitted, or plain line-per-address
		// output): split on any whitespace.
		candidates = strings.Fields(raw)
	}

	var addresses []string
	for _, c := range candidates {
		ip := net.ParseIP(strings.TrimSpace(c))
		if ip == nil {
			continue
		}
		v4 := ip.To4()
		if v4 == nil {
			continue // IPv6 -- SSH transport dials the IPv4 address
		}
		if v4[0] == 169 && v4[1] == 254 {
			continue // APIPA: DHCP has not assigned a real lease yet
		}
		addresses = append(addresses, v4.String())
	}
	return addresses
}

// psQuote renders s as a PowerShell single-quoted string literal, escaping any
// embedded single quotes by doubling them. Single-quoted PowerShell strings are
// literal (no variable/subexpression interpolation), so this is the safe way to
// embed host-controlled values (VM names, file paths, the switch name) into a
// generated script without command injection.
func psQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}
