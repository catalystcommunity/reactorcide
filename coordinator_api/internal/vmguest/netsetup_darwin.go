//go:build darwin

package vmguest

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"os/exec"
	"strings"
	"sync"
	"unsafe"

	"golang.org/x/sys/unix"
)

// The transparent route of a macOS guest (no network card), copied from the
// Magi envd. The daemon runs as root (a launchd daemon), and sets:
//   - routes 0/1 and 128/1 on lo0, so that a connection to any address
//     leaves the program;
//   - a pf rule (anchor com.apple/250.reactorcide) that sends each TCP
//     connection that does not go to 127/8 to the transparent listener;
//   - a resolver for all domains at the fake DNS of the daemon (scutil: a
//     service "reactorcide" with SupplementalMatchDomains ""), because
//     programs on macOS ask mDNSResponder, not /etc/resolv.conf.
//
// The daemon finds the destination before the redirect in the pf state.

func startTransparent(cfg DaemonConfig, t Tunnel, log *slog.Logger) error {
	_, port, err := net.SplitHostPort(cfg.TransparentListen)
	if err != nil {
		return fmt.Errorf("vmguest: transparent listen %q: %w", cfg.TransparentListen, err)
	}
	names := NewFakeDNS()
	var servers []string
	if cfg.DNSListen != "" {
		if err := names.Serve(cfg.DNSListen, log); err != nil {
			return fmt.Errorf("vmguest: fake DNS: %w", err)
		}
		if h, _, err := net.SplitHostPort(cfg.DNSListen); err == nil {
			servers = append(servers, h)
		}
	}
	ln, err := net.Listen("tcp", cfg.TransparentListen)
	if err != nil {
		return err
	}
	if err := macNetwork(port, servers); err != nil {
		ln.Close()
		return err
	}
	go func() { _ = ServeTransparent(ln, macOrigDst, names, t, log) }()
	return nil
}

func applySystemProxy(string) error { return nil }

func macCommand(stdin string, name string, args ...string) error {
	c := exec.Command(name, args...)
	c.Stdin = strings.NewReader(stdin)
	out, err := c.CombinedOutput()
	if err != nil && !bytes.Contains(out, []byte("File exists")) {
		return fmt.Errorf("%s %s: %v: %s", name, strings.Join(args, " "), err, bytes.TrimSpace(out))
	}
	return nil
}

// macNetwork sets the routes, the pf rule, and the resolver.
func macNetwork(port string, dnsServers []string) error {
	for _, step := range macNetworkSteps(port, dnsServers) {
		if err := macCommand(step.stdin, step.name, step.args...); err != nil {
			return err
		}
	}
	return nil
}

// pfioc_natlook of xnu (bsd/net/pfvar.h): addresses, then the ports (union
// pf_state_xport, 4 bytes, in network order), then af, proto, proto_variant,
// and direction.
type pfNatLook struct {
	saddr, daddr, rsaddr, rdaddr     [16]byte
	sxport, dxport, rsxport, rdxport [4]byte
	af, proto, protoVariant, dir     uint8
}

// diocNatLook is _IOWR('D', 23, struct pfioc_natlook).
const diocNatLook = 0xc0000000 | uintptr(unsafe.Sizeof(pfNatLook{}))<<16 | 'D'<<8 | 23

var pfDev struct {
	once sync.Once
	fd   int
	err  error
}

// macOrigDst returns the destination of a connection before the pf redirect:
// with DIOCNATLOOK, or else from the state list of pfctl.
func macOrigDst(c net.Conn) (netip.AddrPort, error) {
	peer, err1 := netip.ParseAddrPort(c.RemoteAddr().String())
	local, err2 := netip.ParseAddrPort(c.LocalAddr().String())
	if err1 != nil || err2 != nil || !peer.Addr().Unmap().Is4() {
		return netip.AddrPort{}, errors.New("not an IPv4 TCP connection")
	}
	a, err := natLook(peer, local)
	if err != nil {
		a, err = pfctlOrigDst(peer)
	}
	return a, err
}

func natLook(peer, local netip.AddrPort) (netip.AddrPort, error) {
	pfDev.once.Do(func() { pfDev.fd, pfDev.err = unix.Open("/dev/pf", unix.O_RDWR, 0) })
	if pfDev.err != nil {
		return netip.AddrPort{}, pfDev.err
	}
	for _, dir := range []uint8{2, 1} { // PF_OUT, PF_IN
		nl := pfNatLook{af: unix.AF_INET, proto: unix.IPPROTO_TCP, dir: dir}
		s, d := peer.Addr().Unmap().As4(), local.Addr().Unmap().As4()
		copy(nl.saddr[:], s[:])
		copy(nl.daddr[:], d[:])
		binary.BigEndian.PutUint16(nl.sxport[:], peer.Port())
		binary.BigEndian.PutUint16(nl.dxport[:], local.Port())
		if _, _, e := unix.Syscall(unix.SYS_IOCTL, uintptr(pfDev.fd), diocNatLook, uintptr(unsafe.Pointer(&nl))); e != 0 {
			continue
		}
		a := netip.AddrFrom4([4]byte(nl.rdaddr[:4]))
		return netip.AddrPortFrom(a, binary.BigEndian.Uint16(nl.rdxport[:])), nil
	}
	return netip.AddrPort{}, errors.New("DIOCNATLOOK found no state")
}

func pfctlOrigDst(peer netip.AddrPort) (netip.AddrPort, error) {
	out, err := exec.Command("/sbin/pfctl", "-q", "-s", "state").Output()
	if err != nil {
		return netip.AddrPort{}, fmt.Errorf("pfctl -s state: %w", err)
	}
	return parsePfState(out, peer)
}
