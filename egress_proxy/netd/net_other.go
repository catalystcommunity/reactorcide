//go:build !linux

package netd

import (
	"errors"
	"net"
	"net/netip"
)

var errLinux = errors.New("the egress network setup runs on Linux only")

func installRules(netip.AddrPort) error { return errLinux }

func netnsExec([]string) error { return errLinux }

func origDst(net.Conn) (netip.AddrPort, error) { return netip.AddrPort{}, errLinux }
