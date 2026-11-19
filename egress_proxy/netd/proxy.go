package netd

import (
	"bufio"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"time"
)

// Copied from Magi primitives/execution/envd (vm.go, main.go): the CONNECT
// client, DNS through the proxy, the transparent route, and the forward.

// ProxyDNS is the CONNECT destination of the DNS service of the egress
// proxy: DNS over TCP.
const ProxyDNS = "reactorcide-dns.internal:53"

// proxyConnect opens a tunnel to dest ("host:port") through the egress
// proxy with an HTTP CONNECT request. proxy has the user and the
// credential of the job.
func proxyConnect(dial func() (net.Conn, error), proxy *url.URL, dest string) (net.Conn, error) {
	c, err := dial()
	if err != nil {
		return nil, err
	}
	c.SetDeadline(time.Now().Add(30 * time.Second))
	req := "CONNECT " + dest + " HTTP/1.1\r\nHost: " + dest + "\r\n"
	if u := proxy.User; u != nil {
		pw, _ := u.Password()
		req += "Proxy-Authorization: Basic " + base64.StdEncoding.EncodeToString([]byte(u.Username()+":"+pw)) + "\r\n"
	}
	if _, err := c.Write([]byte(req + "\r\n")); err != nil {
		c.Close()
		return nil, err
	}
	br := bufio.NewReader(c)
	res, err := http.ReadResponse(br, &http.Request{Method: http.MethodConnect})
	if err != nil {
		c.Close()
		return nil, err
	}
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		c.Close()
		return nil, fmt.Errorf("proxy: %s", res.Status)
	}
	c.SetDeadline(time.Time{})
	return &bufferedConn{Conn: c, r: br}, nil
}

type bufferedConn struct {
	net.Conn
	r *bufio.Reader
}

func (c *bufferedConn) Read(p []byte) (int, error) { return c.r.Read(p) }

func (c *bufferedConn) CloseWrite() error {
	if cw, ok := c.Conn.(interface{ CloseWrite() error }); ok {
		return cw.CloseWrite()
	}
	return c.Conn.Close()
}

// dnsThroughProxy sends one DNS query to the egress proxy.
func dnsThroughProxy(dial func() (net.Conn, error), proxy *url.URL, q []byte) ([]byte, error) {
	c, err := proxyConnect(dial, proxy, ProxyDNS)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(10 * time.Second))
	if _, err := c.Write(append(binary.BigEndian.AppendUint16(nil, uint16(len(q))), q...)); err != nil {
		return nil, err
	}
	var n uint16
	if err := binary.Read(c, binary.BigEndian, &n); err != nil {
		return nil, err
	}
	out := make([]byte, n)
	if _, err := io.ReadFull(c, out); err != nil {
		return nil, err
	}
	return out, nil
}

// destination returns the CONNECT destination of a redirected connection:
// a fake address becomes its name. ok is false for a fake address with no
// name.
func destination(dst netip.AddrPort, names *fakeDNS) (string, bool) {
	host := dst.Addr().Unmap().String()
	if n, ok := names.Name(dst.Addr()); ok {
		host = n
	} else if FakeNet.Contains(dst.Addr().Unmap()) {
		return "", false
	}
	return net.JoinHostPort(host, strconv.Itoa(int(dst.Port()))), true
}

// transparent serves the connections that the firewall redirects to ln: it
// finds the destination, then copies the connection through the proxy.
func transparent(ln net.Listener, origDst func(net.Conn) (netip.AddrPort, error), names *fakeDNS,
	dial func() (net.Conn, error), proxy *url.URL, log *slog.Logger) {
	for {
		c, err := ln.Accept()
		if err != nil {
			log.Error("transparent", "err", err)
			return
		}
		go func() {
			defer c.Close()
			dst, err := origDst(c)
			if err != nil {
				log.Warn("transparent", "err", err)
				return
			}
			dest, ok := destination(dst, names)
			if !ok {
				log.Warn("transparent: unknown fake address", "addr", dst)
				return
			}
			up, err := proxyConnect(dial, proxy, dest)
			if err != nil {
				log.Warn("transparent", "dest", dest, "err", err)
				return
			}
			defer up.Close()
			Splice(c, up)
		}()
	}
}

// startForward listens on addr and copies each TCP connection to the
// proxy (dial). Proxy-aware tools (HTTP(S)_PROXY) use it; they send their
// own proxy credential.
func startForward(addr string, dial func() (net.Conn, error), log *slog.Logger) (net.Listener, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				log.Debug("forward", "err", err)
				return
			}
			go func() {
				defer c.Close()
				u, err := dial()
				if err != nil {
					log.Warn("forward", "err", err)
					return
				}
				defer u.Close()
				Splice(c, u)
			}()
		}
	}()
	return ln, nil
}

// tcpDialer dials addr with a time limit.
func tcpDialer(addr string) func() (net.Conn, error) {
	return func() (net.Conn, error) { return net.DialTimeout("tcp", addr, 20*time.Second) }
}

// Splice copies a to b and b to a until both directions end.
func Splice(a, b net.Conn) {
	done := make(chan struct{}, 2)
	cp := func(dst, src net.Conn) {
		io.Copy(dst, src)
		if cw, ok := dst.(interface{ CloseWrite() error }); ok {
			cw.CloseWrite()
		} else {
			dst.Close()
		}
		done <- struct{}{}
	}
	go cp(a, b)
	go cp(b, a)
	<-done
	<-done
}
