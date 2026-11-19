// Package clientaddr finds the client address of an HTTP request behind
// trusted proxies (REACTORCIDE_TRUSTED_PROXY_CIDRS).
package clientaddr

import (
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strings"
)

// Trusted is a set of proxy networks. The zero value trusts no proxy.
type Trusted struct {
	prefixes []netip.Prefix
}

// Parse reads a comma-separated list of CIDRs or single addresses. Empty
// entries are ignored. An invalid entry is an error.
func Parse(list string) (Trusted, error) {
	var trusted Trusted
	for _, raw := range strings.Split(list, ",") {
		entry := strings.TrimSpace(raw)
		if entry == "" {
			continue
		}
		if strings.Contains(entry, "/") {
			prefix, err := netip.ParsePrefix(entry)
			if err != nil {
				return Trusted{}, fmt.Errorf("trusted proxy CIDR %q is not valid: %w", entry, err)
			}
			trusted.prefixes = append(trusted.prefixes, prefix.Masked())
			continue
		}
		addr, err := netip.ParseAddr(entry)
		if err != nil {
			return Trusted{}, fmt.Errorf("trusted proxy address %q is not valid: %w", entry, err)
		}
		addr = addr.Unmap()
		trusted.prefixes = append(trusted.prefixes, netip.PrefixFrom(addr, addr.BitLen()))
	}
	return trusted, nil
}

// Empty reports whether no proxy is trusted.
func (t Trusted) Empty() bool { return len(t.prefixes) == 0 }

func (t Trusted) contains(addr netip.Addr) bool {
	addr = addr.Unmap()
	for _, prefix := range t.prefixes {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}

// Source returns the client address of r, without a port. It is the TCP
// peer, except when the peer is a trusted proxy: then it is the right-most
// X-Forwarded-For address that is not a trusted proxy. When every forwarded
// address is a trusted proxy, it is the left-most one. A forwarded entry that
// is not an address stops the walk, and the last trusted hop is the source,
// so a false entry cannot select another bucket past it.
func (t Trusted) Source(r *http.Request) string {
	peer := hostOnly(r.RemoteAddr)
	if t.Empty() {
		return peer
	}
	current, err := netip.ParseAddr(peer)
	if err != nil || !t.contains(current) {
		return peer
	}
	var hops []string
	for _, header := range r.Header.Values("X-Forwarded-For") {
		hops = append(hops, strings.Split(header, ",")...)
	}
	for i := len(hops) - 1; i >= 0; i-- {
		hop, err := netip.ParseAddr(hostOnly(strings.TrimSpace(hops[i])))
		if err != nil {
			break
		}
		current = hop.Unmap()
		if !t.contains(current) {
			return current.String()
		}
	}
	return current.String()
}

// hostOnly removes a port and IPv6 brackets.
func hostOnly(value string) string {
	if host, _, err := net.SplitHostPort(value); err == nil {
		return host
	}
	return strings.TrimSuffix(strings.TrimPrefix(value, "["), "]")
}
