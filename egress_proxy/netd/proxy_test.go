package netd

import (
	"bufio"
	"encoding/base64"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

func quietLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func basic(user, pass string) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+pass))
}

// fakeProxy is a CONNECT proxy that checks the job credential, answers 200
// and then echoes the destination and the bytes it gets.
func fakeProxy(t *testing.T, user, pass string) (string, chan string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	dests := make(chan string, 16)
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
				if req.Method != http.MethodConnect || req.Header.Get("Proxy-Authorization") != basic(user, pass) {
					c.Write([]byte("HTTP/1.1 407 Proxy Authentication Required\r\nContent-Length: 0\r\n\r\n"))
					return
				}
				dests <- req.RequestURI
				c.Write([]byte("HTTP/1.1 200 Connection established\r\n\r\n"))
				io.Copy(c, br)
			}()
		}
	}()
	return ln.Addr().String(), dests
}

func TestProxyConnect(t *testing.T) {
	addr, dests := fakeProxy(t, "job_a", "cred")
	u, _ := url.Parse("http://job_a:cred@" + addr)
	c, err := proxyConnect(tcpDialer(addr), u, "example.org:22")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if d := <-dests; d != "example.org:22" {
		t.Fatalf("destination %q", d)
	}
	c.Write([]byte("ping"))
	buf := make([]byte, 4)
	if _, err := io.ReadFull(c, buf); err != nil || string(buf) != "ping" {
		t.Fatalf("stream %q %v", buf, err)
	}
	bad, _ := url.Parse("http://job_a:wrong@" + addr)
	if _, err := proxyConnect(tcpDialer(addr), bad, "example.org:22"); err == nil {
		t.Fatal("a refused CONNECT must fail")
	}
}

func TestDNSThroughProxy(t *testing.T) {
	a, b := net.Pipe()
	go func() {
		defer b.Close()
		br := bufio.NewReader(b)
		req, err := http.ReadRequest(br)
		if err != nil || req.RequestURI != ProxyDNS {
			return
		}
		b.Write([]byte("HTTP/1.1 200 OK\r\n\r\n"))
		var n [2]byte
		io.ReadFull(br, n[:])
		q := make([]byte, int(n[0])<<8|int(n[1]))
		io.ReadFull(br, q)
		q[2] |= 0x80
		b.Write(append([]byte{n[0], n[1]}, q...))
	}()
	u, _ := url.Parse("http://job_a:cred@127.0.0.1:3128")
	f := newFakeDNS()
	f.forward = func(q []byte) ([]byte, error) {
		return dnsThroughProxy(func() (net.Conn, error) { return a, nil }, u, q)
	}
	m := query(t, f, "_linkkeys.example.org.", dnsmessage.TypeTXT)
	if !m.Header.Response || m.Header.ID != 7 || m.RCode != dnsmessage.RCodeSuccess {
		t.Fatalf("TXT through the proxy: %+v", m)
	}
}

func TestDestination(t *testing.T) {
	f := newFakeDNS()
	a, _ := f.addr("pypi.org")
	if d, ok := destination(netip.AddrPortFrom(a, 443), f); !ok || d != "pypi.org:443" {
		t.Fatalf("fake address: %q %v", d, ok)
	}
	if d, ok := destination(netip.MustParseAddrPort("203.0.113.7:8080"), f); !ok || d != "203.0.113.7:8080" {
		t.Fatalf("real address: %q %v", d, ok)
	}
	if _, ok := destination(netip.MustParseAddrPort("198.18.9.9:443"), f); ok {
		t.Fatal("an unknown fake address must not go out")
	}
}

// The transparent route sends a redirected connection to the proxy as
// CONNECT name:port with the job credential.
func TestTransparent(t *testing.T) {
	addr, dests := fakeProxy(t, "job_a", "cred")
	u, _ := url.Parse("http://job_a:cred@" + addr)
	names := newFakeDNS()
	fake, _ := names.addr("api.example.org")
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	orig := func(net.Conn) (netip.AddrPort, error) { return netip.AddrPortFrom(fake, 443), nil }
	go transparent(ln, orig, names, tcpDialer(addr), u, quietLog())
	c, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.Write([]byte("hello"))
	select {
	case d := <-dests:
		if d != "api.example.org:443" {
			t.Fatalf("destination %q", d)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no CONNECT")
	}
	buf := make([]byte, 5)
	c.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.ReadFull(c, buf); err != nil || string(buf) != "hello" {
		t.Fatalf("echo %q %v", buf, err)
	}

	// A failed original destination closes the connection.
	ln2, _ := net.Listen("tcp", "127.0.0.1:0")
	defer ln2.Close()
	go transparent(ln2, func(net.Conn) (netip.AddrPort, error) { return netip.AddrPort{}, errors.New("no") }, names, tcpDialer(addr), u, quietLog())
	c2, _ := net.Dial("tcp", ln2.Addr().String())
	defer c2.Close()
	c2.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := c2.Read(buf); err == nil {
		t.Fatal("expected the connection to close")
	}
}

// The forward copies bytes to the proxy: the client sends its own
// credential.
func TestForward(t *testing.T) {
	addr, dests := fakeProxy(t, "job_a", "cred")
	ln, err := startForward("127.0.0.1:0", tcpDialer(addr), quietLog())
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	u, _ := url.Parse("http://job_a:cred@" + ln.Addr().String())
	c, err := proxyConnect(tcpDialer(ln.Addr().String()), u, "registry.example:443")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if d := <-dests; d != "registry.example:443" {
		t.Fatalf("destination %q", d)
	}
}
