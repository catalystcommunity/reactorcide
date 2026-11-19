package vmrunner

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/vmguest"
)

// echoServer accepts TCP connections and echoes them.
func echoServer(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skip(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() { defer c.Close(); _, _ = io.Copy(c, c) }()
		}
	}()
	return ln.Addr().String()
}

// guestTunnel connects a vmguest.Tunnel to a forwarder through in-memory
// pipes, like the hypervisor socket.
func guestTunnel(f *EgressForwarder) vmguest.Tunnel {
	return vmguest.Tunnel{Dial: func() (net.Conn, error) {
		guest, host := net.Pipe()
		go f.ServeConn(host)
		return guest, nil
	}}
}

func echoThrough(t *testing.T, c net.Conn) {
	t.Helper()
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.WriteString(c, "ping"); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 4)
	if _, err := io.ReadFull(c, buf); err != nil || string(buf) != "ping" {
		t.Fatalf("echo = %q, %v", buf, err)
	}
}

func TestEgressForwarderDirect(t *testing.T) {
	dest := echoServer(t)
	f := &EgressForwarder{Dialer: &DirectEgressDialer{AllowLoopback: true}, JobID: "job-1"}
	c, err := guestTunnel(f).Connect(dest)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	echoThrough(t, c)
}

func TestEgressForwarderDirectRefusesHostLoopbackAndLinkLocal(t *testing.T) {
	dest := echoServer(t)
	f := &EgressForwarder{Dialer: &DirectEgressDialer{}, JobID: "job-1"}
	for _, d := range []string{dest, "169.254.169.254:80", "[::1]:22", "0.0.0.0:80"} {
		_, err := guestTunnel(f).Connect(d)
		var te *vmguest.TunnelError
		if !errors.As(err, &te) || te.StatusCode != http.StatusForbidden {
			t.Fatalf("Connect(%s) err = %v, want 403", d, err)
		}
	}
}

func TestEgressForwarderRejectsBadRequests(t *testing.T) {
	f := &EgressForwarder{Dialer: &DirectEgressDialer{AllowLoopback: true}}
	for req, want := range map[string]int{
		"GET http://example.com/ HTTP/1.1\r\nHost: example.com\r\n\r\n": http.StatusMethodNotAllowed,
		"CONNECT example.com HTTP/1.1\r\nHost: example.com\r\n\r\n":     http.StatusBadRequest,
		"CONNECT example.com:0 HTTP/1.1\r\nHost: example.com:0\r\n\r\n": http.StatusBadRequest,
	} {
		guest, host := net.Pipe()
		go f.ServeConn(host)
		go func() { _, _ = io.WriteString(guest, req) }()
		res, err := http.ReadResponse(bufio.NewReader(guest), &http.Request{Method: http.MethodConnect})
		guest.Close()
		if err != nil || res.StatusCode != want {
			t.Fatalf("%q: response %v, %v; want %d", req, res, err, want)
		}
	}
}

// fakeConnectProxy is an HTTP CONNECT proxy that records each request and
// refuses one destination.
type fakeConnectProxy struct {
	addr   string
	mu     sync.Mutex
	reqs   []*http.Request
	refuse string
}

func newFakeConnectProxy(t *testing.T, refuse string) *fakeConnectProxy {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skip(err)
	}
	t.Cleanup(func() { ln.Close() })
	p := &fakeConnectProxy{addr: ln.Addr().String(), refuse: refuse}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				br := bufio.NewReader(c)
				req, err := http.ReadRequest(br)
				if err != nil {
					return
				}
				p.mu.Lock()
				p.reqs = append(p.reqs, req)
				p.mu.Unlock()
				if req.Host == p.refuse {
					_, _ = io.WriteString(c, "HTTP/1.1 403 Forbidden\r\nContent-Length: 0\r\n\r\n")
					return
				}
				up, err := net.Dial("tcp", req.Host)
				if err != nil {
					_, _ = io.WriteString(c, "HTTP/1.1 502 Bad Gateway\r\nContent-Length: 0\r\n\r\n")
					return
				}
				defer up.Close()
				_, _ = io.WriteString(c, "HTTP/1.1 200 OK\r\n\r\n")
				vmguest.Splice(vmguest.NewBufferedConn(c, br), up)
			}()
		}
	}()
	return p
}

func TestEgressForwarderThroughProxy(t *testing.T) {
	dest := echoServer(t)
	proxy := newFakeConnectProxy(t, "blocked.example:443")
	t.Setenv(EgressProxyEnv, proxy.addr)
	dialer, err := EgressDialerFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	pd, ok := dialer.(*ProxyEgressDialer)
	if !ok {
		t.Fatalf("dialer = %T, want *ProxyEgressDialer", dialer)
	}
	pd.Header = func(flow EgressFlow) http.Header {
		return http.Header{"X-Reactorcide-Job": {flow.JobID}, "X-Bad": {"a\r\nInjected: 1"}}
	}
	f := &EgressForwarder{Dialer: pd, JobID: "job-42"}

	c, err := guestTunnel(f).Connect(dest)
	if err != nil {
		t.Fatal(err)
	}
	echoThrough(t, c)
	c.Close()

	_, err = guestTunnel(f).Connect("blocked.example:443")
	var te *vmguest.TunnelError
	if !errors.As(err, &te) || te.StatusCode != http.StatusForbidden {
		t.Fatalf("refused flow err = %v, want 403 from the proxy", err)
	}

	proxy.mu.Lock()
	defer proxy.mu.Unlock()
	if len(proxy.reqs) != 2 {
		t.Fatalf("proxy requests = %d", len(proxy.reqs))
	}
	first := proxy.reqs[0]
	if first.Method != http.MethodConnect || first.Host != dest {
		t.Fatalf("proxy request = %s %s", first.Method, first.Host)
	}
	if first.Header.Get("X-Reactorcide-Job") != "job-42" {
		t.Fatalf("header hook not applied: %v", first.Header)
	}
	if first.Header.Get("Injected") != "" || first.Header.Get("X-Bad") != "" {
		t.Fatalf("header with a line break was sent: %v", first.Header)
	}
}

func TestEgressForwarderDropsGuestProxyAuthorization(t *testing.T) {
	proxy := newFakeConnectProxy(t, "")
	dest := echoServer(t)
	f := &EgressForwarder{Dialer: &ProxyEgressDialer{Addr: proxy.addr}}
	guest, host := net.Pipe()
	go f.ServeConn(host)
	go func() {
		_, _ = io.WriteString(guest, "CONNECT "+dest+" HTTP/1.1\r\nHost: "+dest+"\r\nProxy-Authorization: Basic Z3Vlc3Q6Zm9yZ2Vk\r\n\r\n")
	}()
	res, err := http.ReadResponse(bufio.NewReader(guest), &http.Request{Method: http.MethodConnect})
	guest.Close()
	if err != nil || res.StatusCode != 200 {
		t.Fatalf("response = %v, %v", res, err)
	}
	proxy.mu.Lock()
	defer proxy.mu.Unlock()
	if got := proxy.reqs[0].Header.Get("Proxy-Authorization"); got != "" {
		t.Fatalf("guest credential reached the proxy: %q", got)
	}
}

func TestEgressDialerFromEnv(t *testing.T) {
	t.Setenv(EgressProxyEnv, "")
	if d, err := EgressDialerFromEnv(); err != nil {
		t.Fatal(err)
	} else if _, ok := d.(*DirectEgressDialer); !ok {
		t.Fatalf("dialer = %T", d)
	}
	for _, bad := range []string{"proxy.example", "http://proxy:3128", "proxy:99999", ":3128"} {
		t.Setenv(EgressProxyEnv, bad)
		if _, err := EgressDialerFromEnv(); err == nil || !strings.Contains(err.Error(), EgressProxyEnv) {
			t.Fatalf("%q: err = %v", bad, err)
		}
	}
}

func TestProxyEgressDialerUnreachableProxy(t *testing.T) {
	d := &ProxyEgressDialer{Addr: "127.0.0.1:1"}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := d.DialEgress(ctx, EgressFlow{Destination: "example.com:443"}); err == nil {
		t.Fatal("expected an error")
	}
}
