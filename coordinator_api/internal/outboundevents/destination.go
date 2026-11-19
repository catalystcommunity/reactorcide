package outboundevents

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"strings"
	"syscall"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/transportsecurity"
)

// ErrDestinationRefused marks a sink destination that the destination
// policy does not permit. A refused delivery goes to dead_letter at once.
var ErrDestinationRefused = errors.New("destination refused")

// DestinationPolicy controls where a sink can send.
//
// By default a sink sends only to HTTPS URLs whose addresses are public. An
// operator can permit HTTP with --allow-insecure-transport (development
// only, never from an environment variable) and can permit private networks
// with AllowedNetworks.
type DestinationPolicy struct {
	AllowInsecure   bool
	AllowedNetworks []netip.Prefix
}

// ParseAllowedNetworks parses a comma-separated list of CIDR prefixes or
// single addresses.
func ParseAllowedNetworks(value string) ([]netip.Prefix, error) {
	var out []netip.Prefix
	for _, part := range strings.Split(value, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if !strings.Contains(part, "/") {
			addr, err := netip.ParseAddr(part)
			if err != nil {
				return nil, fmt.Errorf("invalid network %q: %w", part, err)
			}
			out = append(out, netip.PrefixFrom(addr.Unmap(), addr.Unmap().BitLen()))
			continue
		}
		prefix, err := netip.ParsePrefix(part)
		if err != nil {
			return nil, fmt.Errorf("invalid network %q: %w", part, err)
		}
		out = append(out, prefix.Masked())
	}
	return out, nil
}

var specialPrefixes = func() []netip.Prefix {
	var out []netip.Prefix
	for _, cidr := range []string{
		"0.0.0.0/8",       // this network
		"100.64.0.0/10",   // carrier-grade NAT
		"192.0.0.0/24",    // IETF protocol assignments
		"192.0.2.0/24",    // documentation
		"198.18.0.0/15",   // benchmarking
		"198.51.100.0/24", // documentation
		"203.0.113.0/24",  // documentation
		"240.0.0.0/4",     // reserved, and broadcast
		"64:ff9b::/96",    // NAT64 can reach IPv4 private space
		"64:ff9b:1::/48",
		"2001:db8::/32", // documentation
	} {
		out = append(out, netip.MustParsePrefix(cidr))
	}
	return out
}()

// addressPermitted reports whether a sink may connect to addr.
func (p DestinationPolicy) addressPermitted(addr netip.Addr) bool {
	addr = addr.Unmap()
	for _, allowed := range p.AllowedNetworks {
		if allowed.Contains(addr) {
			return true
		}
	}
	if !addr.IsValid() || addr.IsLoopback() || addr.IsPrivate() || addr.IsLinkLocalUnicast() ||
		addr.IsLinkLocalMulticast() || addr.IsInterfaceLocalMulticast() || addr.IsMulticast() || addr.IsUnspecified() {
		return false
	}
	for _, prefix := range specialPrefixes {
		if prefix.Contains(addr) {
			return false
		}
	}
	return true
}

// ValidateURL checks the form of a sink URL: HTTPS (or HTTP with the
// insecure development option), a host, no user information, and, for an
// IP literal host, a permitted address. A host name is checked again at
// connect time, for each resolved address.
func (p DestinationPolicy) ValidateURL(rawURL string) error {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return errors.New("the webhook URL is required")
	}
	if len(rawURL) > 2048 {
		return errors.New("the webhook URL is longer than 2048 characters")
	}
	if err := transportsecurity.ValidateURL(rawURL, p.AllowInsecure, "an outbound event webhook"); err != nil {
		return err
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid webhook URL: %w", err)
	}
	if u.User != nil {
		return errors.New("the webhook URL must not contain user information; the payload is signed instead")
	}
	if u.Fragment != "" {
		return errors.New("the webhook URL must not contain a fragment")
	}
	host := u.Hostname()
	if addr, err := netip.ParseAddr(host); err == nil && !p.addressPermitted(addr) {
		return fmt.Errorf("%w: %s is a loopback, private, link-local, or reserved address", ErrDestinationRefused, host)
	}
	if strings.EqualFold(host, "localhost") || strings.HasSuffix(strings.ToLower(host), ".localhost") {
		if !p.addressPermitted(netip.MustParseAddr("127.0.0.1")) {
			return fmt.Errorf("%w: %s is a loopback host", ErrDestinationRefused, host)
		}
	}
	return nil
}

// dialControl refuses a connection to an address that the policy does not
// permit. It runs after DNS resolution for each address that the dialer
// tries, so a DNS answer cannot move a request to a private address.
func (p DestinationPolicy) dialControl(network, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrDestinationRefused, err)
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return fmt.Errorf("%w: %s is not an IP address", ErrDestinationRefused, host)
	}
	if !p.addressPermitted(addr) {
		return fmt.Errorf("%w: %s is a loopback, private, link-local, or reserved address", ErrDestinationRefused, addr)
	}
	return nil
}

// dialer returns a dialer that applies the policy at connect time.
func (p DestinationPolicy) dialer() *net.Dialer {
	return &net.Dialer{Control: p.dialControl}
}

// dialContext is the DialContext of the sink transport.
func (p DestinationPolicy) dialContext(ctx context.Context, network, address string) (net.Conn, error) {
	return p.dialer().DialContext(ctx, network, address)
}
