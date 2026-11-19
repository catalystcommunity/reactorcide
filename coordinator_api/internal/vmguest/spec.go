package vmguest

import (
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"
)

// Listen listens on "vsock:PORT" (macOS guest), "hvsock:PORT" (Hyper-V
// guest), "unix:PATH", or "tcp:HOST:PORT". The last two are for tests.
func Listen(spec string) (net.Listener, error) {
	kind, rest, ok := strings.Cut(spec, ":")
	if !ok {
		return nil, fmt.Errorf("vmguest: listen %q: expected KIND:ADDRESS", spec)
	}
	switch kind {
	case "vsock", "hvsock":
		port, err := strconv.ParseUint(rest, 10, 32)
		if err != nil {
			return nil, fmt.Errorf("vmguest: listen %q: %w", spec, err)
		}
		if kind == "vsock" {
			return vsockListen(uint32(port))
		}
		return hvsockListen(uint32(port))
	case "unix":
		_ = os.Remove(rest)
		return net.Listen("unix", rest)
	case "tcp":
		return net.Listen("tcp", rest)
	}
	return nil, fmt.Errorf("vmguest: listen %q: unknown kind %q", spec, kind)
}

// Dialer returns a dial function for "vsock:CID:PORT", "hvsock:PORT" (the
// Hyper-V parent partition), "unix:PATH", or "tcp:HOST:PORT".
func Dialer(spec string) (func() (net.Conn, error), error) {
	kind, rest, ok := strings.Cut(spec, ":")
	if !ok {
		return nil, fmt.Errorf("vmguest: dial %q: expected KIND:ADDRESS", spec)
	}
	switch kind {
	case "vsock":
		c, p, ok := strings.Cut(rest, ":")
		cid, err1 := strconv.ParseUint(c, 10, 32)
		port, err2 := strconv.ParseUint(p, 10, 32)
		if !ok || err1 != nil || err2 != nil {
			return nil, fmt.Errorf("vmguest: dial %q: expected vsock:CID:PORT", spec)
		}
		return func() (net.Conn, error) { return vsockDial(uint32(cid), uint32(port)) }, nil
	case "hvsock":
		port, err := strconv.ParseUint(rest, 10, 32)
		if err != nil {
			return nil, fmt.Errorf("vmguest: dial %q: expected hvsock:PORT", spec)
		}
		return func() (net.Conn, error) { return hvsockDial(uint32(port)) }, nil
	case "unix":
		return func() (net.Conn, error) { return net.DialTimeout("unix", rest, 20*time.Second) }, nil
	case "tcp":
		return func() (net.Conn, error) { return net.DialTimeout("tcp", rest, 20*time.Second) }, nil
	}
	return nil, fmt.Errorf("vmguest: dial %q: unknown kind %q", spec, kind)
}
