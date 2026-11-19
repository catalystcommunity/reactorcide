//go:build !darwin && !windows

package vmguest

import (
	"errors"
	"net"
)

// Linux builds of the daemon are for tests. They use unix or tcp specs.
var errNoHypervisorSocket = errors.New("vmguest: hypervisor sockets need a macOS or Hyper-V guest; use a unix: or tcp: spec")

func vsockListen(uint32) (net.Listener, error) { return nil, errNoHypervisorSocket }

func vsockDial(uint32, uint32) (net.Conn, error) { return nil, errNoHypervisorSocket }

func hvsockListen(uint32) (net.Listener, error) { return nil, errNoHypervisorSocket }

func hvsockDial(uint32) (net.Conn, error) { return nil, errNoHypervisorSocket }
