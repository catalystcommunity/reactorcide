//go:build windows

package vmguest

import (
	"context"
	"errors"
	"net"
	"time"

	"github.com/Microsoft/go-winio"
)

// hvsockListen listens for the host on a Hyper-V socket. The service ID is
// the vsock template GUID for port (go-winio VsockServiceID); the host
// registers the same GUID under GuestCommunicationServices.
func hvsockListen(port uint32) (net.Listener, error) {
	return winio.ListenHvsock(&winio.HvsockAddr{VMID: winio.HvsockGUIDWildcard(), ServiceID: winio.VsockServiceID(port)})
}

// hvsockDial connects to a host service through the parent partition.
func hvsockDial(port uint32) (net.Conn, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	return winio.Dial(ctx, &winio.HvsockAddr{VMID: winio.HvsockGUIDParent(), ServiceID: winio.VsockServiceID(port)})
}

func vsockListen(uint32) (net.Listener, error) { return nil, errVsock }

func vsockDial(uint32, uint32) (net.Conn, error) { return nil, errVsock }

var errVsock = errors.New("vmguest: vsock needs a macOS guest; use hvsock on Hyper-V")
