//go:build darwin

package vmguest

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"syscall"
	"unsafe"
)

// vsock in a macOS guest of Virtualization.framework (sys/vsock.h). Copied
// from the Magi envd.
const afVSOCK = 40

type sockaddrVM struct {
	Len       uint8
	Family    uint8
	Reserved1 uint16
	Port      uint32
	CID       uint32
}

// vmcidAny is VMADDR_CID_ANY.
const vmcidAny = 0xffffffff

func vsockSocket() (int, error) {
	return syscall.Socket(afVSOCK, syscall.SOCK_STREAM, 0)
}

func vsockAddr(cid, port uint32) (unsafe.Pointer, uintptr) {
	sa := &sockaddrVM{Len: uint8(unsafe.Sizeof(sockaddrVM{})), Family: afVSOCK, Port: port, CID: cid}
	return unsafe.Pointer(sa), unsafe.Sizeof(*sa)
}

func vsockListen(port uint32) (net.Listener, error) {
	fd, err := vsockSocket()
	if err != nil {
		return nil, fmt.Errorf("vsock socket: %w", err)
	}
	p, n := vsockAddr(vmcidAny, port)
	if _, _, e := syscall.Syscall(syscall.SYS_BIND, uintptr(fd), uintptr(p), n); e != 0 {
		syscall.Close(fd)
		return nil, fmt.Errorf("vsock bind %d: %w", port, e)
	}
	if err := syscall.Listen(fd, 128); err != nil {
		syscall.Close(fd)
		return nil, err
	}
	return &vsockListener{fd: fd, port: port}, nil
}

func vsockDial(cid, port uint32) (net.Conn, error) {
	fd, err := vsockSocket()
	if err != nil {
		return nil, fmt.Errorf("vsock socket: %w", err)
	}
	p, n := vsockAddr(cid, port)
	if _, _, e := syscall.Syscall(syscall.SYS_CONNECT, uintptr(fd), uintptr(p), n); e != 0 {
		syscall.Close(fd)
		return nil, fmt.Errorf("vsock connect %d:%d: %w", cid, port, e)
	}
	return vsockConn(fd, port)
}

// vsockConn returns a net.Conn for a vsock socket. Go cannot poll a vsock
// socket on macOS, so the socket stays blocking, and two goroutines copy
// between it and one end of an in-memory pipe, which has deadlines.
func vsockConn(fd int, port uint32) (net.Conn, error) {
	f := os.NewFile(uintptr(fd), "vsock")
	mine, theirs := net.Pipe()
	go func() {
		_, _ = io.Copy(theirs, f)
		theirs.Close()
	}()
	go func() {
		_, _ = io.Copy(f, theirs)
		_ = syscall.Shutdown(fd, syscall.SHUT_WR)
		f.Close()
	}()
	return &pipeConn{Conn: mine, addr: vsockNetAddr(port)}, nil
}

type pipeConn struct {
	net.Conn
	addr net.Addr
}

func (c *pipeConn) LocalAddr() net.Addr  { return c.addr }
func (c *pipeConn) RemoteAddr() net.Addr { return c.addr }

type vsockListener struct {
	fd   int
	port uint32
}

func (l *vsockListener) Accept() (net.Conn, error) {
	for {
		// syscall.Accept does not know AF_VSOCK addresses: it would close
		// the connection. Ask for no address.
		nfd, _, e := syscall.Syscall(syscall.SYS_ACCEPT, uintptr(l.fd), 0, 0)
		if e == syscall.EINTR {
			continue
		}
		if e == syscall.EBADF || e == syscall.EINVAL {
			return nil, net.ErrClosed
		}
		if e != 0 {
			return nil, e
		}
		// Only the host may use the job channel. A peer address that the
		// kernel cannot report does not stop the connection.
		if cid, err := peerCID(int(nfd)); err == nil && cid != HostCID {
			syscall.Close(int(nfd))
			continue
		}
		return vsockConn(int(nfd), l.port)
	}
}

func peerCID(fd int) (uint32, error) {
	var sa sockaddrVM
	n := uint32(unsafe.Sizeof(sa))
	_, _, e := syscall.Syscall(syscall.SYS_GETPEERNAME, uintptr(fd), uintptr(unsafe.Pointer(&sa)), uintptr(unsafe.Pointer(&n)))
	if e != 0 {
		return 0, e
	}
	if sa.Family != afVSOCK {
		return 0, errors.New("not a vsock peer")
	}
	return sa.CID, nil
}

func (l *vsockListener) Close() error   { return syscall.Close(l.fd) }
func (l *vsockListener) Addr() net.Addr { return vsockNetAddr(l.port) }

type vsockNetAddr uint32

func (a vsockNetAddr) Network() string { return "vsock" }
func (a vsockNetAddr) String() string  { return fmt.Sprintf("vsock:%d", uint32(a)) }

func hvsockListen(uint32) (net.Listener, error) { return nil, errHvsock }

func hvsockDial(uint32) (net.Conn, error) { return nil, errHvsock }

var errHvsock = errors.New("vmguest: hvsock needs a Hyper-V guest")
