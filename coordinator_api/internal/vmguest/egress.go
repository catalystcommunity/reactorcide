package vmguest

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"time"
)

// Tunnel opens egress flows through the host. Each flow is a new hypervisor
// socket connection with one HTTP CONNECT request.
type Tunnel struct {
	Dial func() (net.Conn, error)
}

// Connect opens a tunnel to dest ("host:port").
func (t Tunnel) Connect(dest string) (net.Conn, error) {
	if t.Dial == nil {
		return nil, errors.New("vmguest: no egress tunnel")
	}
	c, err := t.Dial()
	if err != nil {
		return nil, err
	}
	_ = c.SetDeadline(time.Now().Add(30 * time.Second))
	req := "CONNECT " + dest + " HTTP/1.1\r\nHost: " + dest + "\r\n\r\n"
	if _, err := io.WriteString(c, req); err != nil {
		c.Close()
		return nil, err
	}
	br := bufio.NewReader(c)
	res, err := http.ReadResponse(br, &http.Request{Method: http.MethodConnect})
	if err != nil {
		c.Close()
		return nil, err
	}
	if res.StatusCode != http.StatusOK {
		c.Close()
		return nil, &TunnelError{StatusCode: res.StatusCode, Status: res.Status}
	}
	_ = c.SetDeadline(time.Time{})
	return &bufferedConn{Conn: c, r: br}, nil
}

// TunnelError is a refusal from the host egress forwarder or its proxy.
type TunnelError struct {
	StatusCode int
	Status     string
}

func (e *TunnelError) Error() string { return "vmguest: egress refused: " + e.Status }

// NewBufferedConn returns c with reads from r, which holds bytes that a
// bufio.Reader read from c after an HTTP head.
func NewBufferedConn(c net.Conn, r *bufio.Reader) net.Conn { return &bufferedConn{Conn: c, r: r} }

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

// Splice copies a to b and b to a until both directions end.
func Splice(a, b net.Conn) {
	done := make(chan struct{}, 2)
	cp := func(dst, src net.Conn) {
		_, _ = io.Copy(dst, src)
		if cw, ok := dst.(interface{ CloseWrite() error }); ok {
			_ = cw.CloseWrite()
		} else {
			_ = dst.Close()
		}
		done <- struct{}{}
	}
	go cp(a, b)
	go cp(b, a)
	<-done
	<-done
}

// ServeProxy serves a local HTTP proxy on ln for proxy-aware programs. A
// CONNECT request becomes a tunnel. A request in absolute form
// ("GET http://host/path") goes through a tunnel to the host and port of its
// URL. The proxy needs no credential: only programs in the guest reach it.
func ServeProxy(ln net.Listener, t Tunnel, log *slog.Logger) error {
	for {
		c, err := ln.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return err
			}
			log.Warn("egress proxy accept", "err", err)
			time.Sleep(100 * time.Millisecond)
			continue
		}
		go serveProxyConn(c, t, log)
	}
}

func serveProxyConn(c net.Conn, t Tunnel, log *slog.Logger) {
	defer c.Close()
	_ = c.SetReadDeadline(time.Now().Add(30 * time.Second))
	br := bufio.NewReader(c)
	req, err := http.ReadRequest(br)
	if err != nil {
		return
	}
	_ = c.SetReadDeadline(time.Time{})
	if req.Method == http.MethodConnect {
		dest := req.Host
		if _, _, err := net.SplitHostPort(dest); err != nil {
			writeProxyStatus(c, http.StatusBadRequest)
			return
		}
		up, err := t.Connect(dest)
		if err != nil {
			log.Debug("egress proxy", "dest", dest, "err", err)
			writeProxyStatus(c, tunnelStatus(err))
			return
		}
		defer up.Close()
		if _, err := io.WriteString(c, "HTTP/1.1 200 Connection established\r\n\r\n"); err != nil {
			return
		}
		Splice(&bufferedConn{Conn: c, r: br}, up)
		return
	}
	if req.URL == nil || req.URL.Host == "" || !strings.EqualFold(req.URL.Scheme, "http") {
		writeProxyStatus(c, http.StatusBadRequest)
		return
	}
	dest := req.URL.Host
	if _, _, err := net.SplitHostPort(dest); err != nil {
		dest = net.JoinHostPort(strings.Trim(req.URL.Hostname(), "[]"), "80")
	}
	up, err := t.Connect(dest)
	if err != nil {
		log.Debug("egress proxy", "dest", dest, "err", err)
		writeProxyStatus(c, tunnelStatus(err))
		return
	}
	defer up.Close()
	for _, h := range []string{"Proxy-Authorization", "Proxy-Connection", "Proxy-Authenticate"} {
		req.Header.Del(h)
	}
	// Request.Write sends the origin form ("GET /path").
	if err := req.Write(up); err != nil {
		return
	}
	Splice(&bufferedConn{Conn: c, r: br}, up)
}

func tunnelStatus(err error) int {
	var te *TunnelError
	if errors.As(err, &te) && te.StatusCode >= 400 {
		return te.StatusCode
	}
	return http.StatusBadGateway
}

func writeProxyStatus(w io.Writer, code int) {
	_, _ = fmt.Fprintf(w, "HTTP/1.1 %d %s\r\nContent-Length: 0\r\nConnection: close\r\n\r\n", code, http.StatusText(code))
}

// ServeTransparent serves the connections that the guest firewall redirects
// to ln: it finds the original destination, then copies the connection
// through a tunnel. A destination in FakeNet becomes its DNS name, so the
// host (and an egress proxy) sees names, not fake addresses.
func ServeTransparent(ln net.Listener, origDst func(net.Conn) (netip.AddrPort, error), names *FakeDNS, t Tunnel, log *slog.Logger) error {
	for {
		c, err := ln.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return err
			}
			log.Warn("transparent accept", "err", err)
			time.Sleep(100 * time.Millisecond)
			continue
		}
		go func() {
			defer c.Close()
			dst, err := origDst(c)
			if err != nil {
				log.Warn("transparent", "err", err)
				return
			}
			dest, ok := TransparentDestination(dst, names)
			if !ok {
				log.Warn("transparent: unknown fake address", "addr", dst)
				return
			}
			up, err := t.Connect(dest)
			if err != nil {
				log.Debug("transparent", "dest", dest, "err", err)
				return
			}
			defer up.Close()
			Splice(c, up)
		}()
	}
}

// TransparentDestination maps an original destination to the CONNECT
// destination: a fake address becomes its name.
func TransparentDestination(dst netip.AddrPort, names *FakeDNS) (string, bool) {
	addr := dst.Addr().Unmap()
	host := addr.String()
	if names != nil {
		if n, ok := names.Name(addr); ok {
			host = n
		} else if FakeNet.Contains(addr) {
			return "", false
		}
	}
	return net.JoinHostPort(host, strconv.Itoa(int(dst.Port()))), true
}

// ProxyEnv returns the proxy variables for proxy-aware programs. addr is the
// local proxy listener.
func ProxyEnv(addr string) []string {
	url := "http://" + addr
	noProxy := "localhost,127.0.0.1,::1"
	return []string{
		"HTTP_PROXY=" + url, "http_proxy=" + url,
		"HTTPS_PROXY=" + url, "https_proxy=" + url,
		"ALL_PROXY=" + url, "all_proxy=" + url,
		"NO_PROXY=" + noProxy, "no_proxy=" + noProxy,
	}
}
