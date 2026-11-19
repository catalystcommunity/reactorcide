package vmrunner

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/catalystcommunity/app-utils-go/logging"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/vmguest"
)

// EgressProxyEnv names an HTTP CONNECT proxy ("host:port") for all guest
// egress. When it is not set, the forwarder dials each destination directly,
// which keeps the open egress of the old NAT network.
const EgressProxyEnv = "REACTORCIDE_VM_EGRESS_PROXY"

const egressDialTimeout = 30 * time.Second

// EgressFlow is one guest TCP flow: the CONNECT destination ("host:port")
// and the job that owns the VM.
type EgressFlow struct {
	JobID       string
	Destination string
}

// EgressDialer opens the host side of a guest egress flow. It is the point
// where an egress policy or a secrets proxy (WP18) plugs in.
type EgressDialer interface {
	DialEgress(ctx context.Context, flow EgressFlow) (net.Conn, error)
}

// EgressRefusedError is a refusal with an HTTP status, which the forwarder
// sends back to the guest.
type EgressRefusedError struct {
	StatusCode int
	Status     string
}

func (e *EgressRefusedError) Error() string { return "egress refused: " + e.Status }

// EgressDialerFromEnv returns a ProxyEgressDialer when EgressProxyEnv is set,
// and a DirectEgressDialer when it is not.
func EgressDialerFromEnv() (EgressDialer, error) {
	addr := strings.TrimSpace(os.Getenv(EgressProxyEnv))
	if addr == "" {
		return &DirectEgressDialer{}, nil
	}
	if err := validateHostPort(addr); err != nil {
		return nil, fmt.Errorf("invalid %s %q: %w", EgressProxyEnv, addr, err)
	}
	header, err := proxyHeaderFromEnv()
	if err != nil {
		return nil, err
	}
	return &ProxyEgressDialer{Addr: addr, Header: header}, nil
}

// DirectEgressDialer dials the destination from the host. It refuses
// loopback, unspecified, link-local, and multicast addresses after name
// resolution: the old NAT network did not reach host loopback services, and
// link-local holds cloud metadata services.
type DirectEgressDialer struct {
	// AllowLoopback permits loopback destinations. Tests only.
	AllowLoopback bool
}

func (d *DirectEgressDialer) DialEgress(ctx context.Context, flow EgressFlow) (net.Conn, error) {
	dialer := net.Dialer{
		Timeout: egressDialTimeout,
		Control: func(_, address string, _ syscall.RawConn) error {
			return checkEgressAddress(address, d.AllowLoopback)
		},
	}
	return dialer.DialContext(ctx, "tcp", flow.Destination)
}

func checkEgressAddress(address string, allowLoopback bool) error {
	ap, err := netip.ParseAddrPort(address)
	if err != nil {
		return &EgressRefusedError{StatusCode: http.StatusForbidden, Status: "403 Forbidden (unparsable address)"}
	}
	a := ap.Addr().Unmap()
	switch {
	case a.IsLoopback() && allowLoopback:
		return nil
	case a.IsLoopback(), a.IsUnspecified(), a.IsLinkLocalUnicast(), a.IsLinkLocalMulticast(),
		a.IsInterfaceLocalMulticast(), a.IsMulticast():
		return &EgressRefusedError{StatusCode: http.StatusForbidden, Status: "403 Forbidden (address " + a.String() + " is not permitted)"}
	}
	return nil
}

// ProxyEgressDialer sends each flow to an HTTP CONNECT proxy. The guest
// cannot set proxy credentials: Header (optional) adds the CONNECT headers
// for a flow, for example a proxy credential for the job.
type ProxyEgressDialer struct {
	Addr   string
	Header func(EgressFlow) http.Header
}

func (p *ProxyEgressDialer) DialEgress(ctx context.Context, flow EgressFlow) (net.Conn, error) {
	d := net.Dialer{Timeout: egressDialTimeout}
	c, err := d.DialContext(ctx, "tcp", p.Addr)
	if err != nil {
		return nil, fmt.Errorf("dial egress proxy: %w", err)
	}
	if deadline, ok := ctx.Deadline(); ok {
		_ = c.SetDeadline(deadline)
	}
	var b strings.Builder
	b.WriteString("CONNECT " + flow.Destination + " HTTP/1.1\r\nHost: " + flow.Destination + "\r\n")
	if p.Header != nil {
		for k, vs := range p.Header(flow) {
			for _, v := range vs {
				if strings.ContainsAny(k+v, "\r\n") {
					continue
				}
				b.WriteString(k + ": " + v + "\r\n")
			}
		}
	}
	b.WriteString("\r\n")
	if _, err := io.WriteString(c, b.String()); err != nil {
		c.Close()
		return nil, fmt.Errorf("write CONNECT to egress proxy: %w", err)
	}
	br := bufio.NewReader(c)
	res, err := http.ReadResponse(br, &http.Request{Method: http.MethodConnect})
	if err != nil {
		c.Close()
		return nil, fmt.Errorf("read egress proxy response: %w", err)
	}
	if res.StatusCode != http.StatusOK {
		c.Close()
		return nil, &EgressRefusedError{StatusCode: res.StatusCode, Status: res.Status}
	}
	_ = c.SetDeadline(time.Time{})
	return vmguest.NewBufferedConn(c, br), nil
}

// EgressForwarder serves the guest side of the egress tunnel: each
// connection carries one HTTP CONNECT request from the guest daemon. The
// forwarder ignores any Proxy-Authorization from the guest.
type EgressForwarder struct {
	Dialer EgressDialer
	JobID  string
}

// Serve accepts tunnel connections until ln is closed.
func (f *EgressForwarder) Serve(ln net.Listener) error {
	for {
		c, err := ln.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return err
			}
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				continue
			}
			return err
		}
		go f.ServeConn(c)
	}
}

// ServeConn serves one tunnel connection and closes it.
func (f *EgressForwarder) ServeConn(c net.Conn) {
	defer c.Close()
	_ = c.SetReadDeadline(time.Now().Add(30 * time.Second))
	br := bufio.NewReader(c)
	req, err := http.ReadRequest(br)
	if err != nil {
		return
	}
	_ = c.SetReadDeadline(time.Time{})
	if req.Method != http.MethodConnect {
		writeEgressStatus(c, http.StatusMethodNotAllowed)
		return
	}
	dest := req.Host
	if err := validateHostPort(dest); err != nil {
		writeEgressStatus(c, http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), egressDialTimeout)
	up, err := f.Dialer.DialEgress(ctx, EgressFlow{JobID: f.JobID, Destination: dest})
	cancel()
	if err != nil {
		code := http.StatusBadGateway
		var refused *EgressRefusedError
		if errors.As(err, &refused) && refused.StatusCode >= 400 && refused.StatusCode <= 599 {
			code = refused.StatusCode
		}
		logging.Log.WithField("job_id", f.JobID).WithField("destination", dest).WithError(err).Debug("VM egress flow refused")
		writeEgressStatus(c, code)
		return
	}
	defer up.Close()
	if _, err := io.WriteString(c, "HTTP/1.1 200 Connection established\r\n\r\n"); err != nil {
		return
	}
	vmguest.Splice(vmguest.NewBufferedConn(c, br), up)
}

func writeEgressStatus(w io.Writer, code int) {
	_, _ = fmt.Fprintf(w, "HTTP/1.1 %d %s\r\nContent-Length: 0\r\nConnection: close\r\n\r\n", code, http.StatusText(code))
}

func validateHostPort(addr string) error {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return err
	}
	if host == "" || strings.ContainsAny(host, " \t\r\n/") {
		return errors.New("empty or invalid host")
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return errors.New("invalid port")
	}
	return nil
}
