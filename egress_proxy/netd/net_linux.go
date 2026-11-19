package netd

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"unsafe"

	"github.com/google/nftables"
	"github.com/google/nftables/binaryutil"
	"github.com/google/nftables/expr"
	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

// Copied from Magi primitives/execution/envd/vm_linux.go.

// TableName is the nftables table (family inet) of the redirect.
const TableName = "reactorcide"

// DummyInterface is the interface of a namespace made by netns.
const DummyInterface = "rcegress0"

// podnetRules returns the rules of the nat output chain: a connection to
// loopback or to the proxy (exclude) returns; DNS (UDP and TCP port 53)
// goes to DNSPort; each other TCP connection goes to TransparentPort.
// Other traffic is not changed.
func podnetRules(exclude netip.AddrPort) [][]expr.Any {
	lon := make([]byte, 16)
	copy(lon, "lo")
	tcp := []expr.Any{&expr.Meta{Key: expr.MetaKeyL4PROTO, Register: 1}, &expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte{unix.IPPROTO_TCP}}}
	dport := func(p uint16) []expr.Any {
		return []expr.Any{&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseTransportHeader, Offset: 2, Len: 2},
			&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: binaryutil.BigEndian.PutUint16(p)}}
	}
	redir := func(p uint16) []expr.Any {
		return []expr.Any{&expr.Immediate{Register: 1, Data: binaryutil.BigEndian.PutUint16(p)},
			&expr.Redir{RegisterProtoMin: 1, Flags: unix.NF_NAT_RANGE_PROTO_SPECIFIED}}
	}
	cat := func(parts ...[]expr.Any) []expr.Any {
		var out []expr.Any
		for _, p := range parts {
			out = append(out, p...)
		}
		return out
	}
	addr := exclude.Addr().Unmap()
	fam, off, ip := byte(unix.NFPROTO_IPV4), uint32(16), addr.AsSlice()
	if addr.Is6() {
		fam, off = unix.NFPROTO_IPV6, 24
	}
	ret := []expr.Any{&expr.Verdict{Kind: expr.VerdictReturn}}
	return [][]expr.Any{
		{&expr.Meta{Key: expr.MetaKeyOIFNAME, Register: 1}, &expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: lon}, &expr.Verdict{Kind: expr.VerdictReturn}},
		cat([]expr.Any{&expr.Meta{Key: expr.MetaKeyNFPROTO, Register: 1}, &expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte{fam}},
			&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseNetworkHeader, Offset: off, Len: uint32(len(ip))},
			&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: ip}}, tcp, dport(exclude.Port()), ret),
		cat([]expr.Any{&expr.Meta{Key: expr.MetaKeyL4PROTO, Register: 1}, &expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte{unix.IPPROTO_UDP}}},
			dport(53), redir(DNSPort)),
		cat(tcp, dport(53), redir(DNSPort)),
		cat(tcp, redir(TransparentPort)),
	}
}

// guardRules returns the rules of the filter output chain, which runs
// after the redirect: a packet to loopback (each redirected connection and
// DNS query, matched by interface and by address), a TCP packet to the proxy, or a packet of an established
// connection is accepted. All other packets are dropped: UDP other than
// DNS (for example QUIC), ICMP, and any connection that the redirect did
// not catch. Without this chain, UDP could leave the job directly
// (Reactorcide addition; Magi relies on a NetworkPolicy for this).
func guardRules(exclude netip.AddrPort) [][]expr.Any {
	lon := make([]byte, 16)
	copy(lon, "lo")
	addr := exclude.Addr().Unmap()
	fam, off, ip := byte(unix.NFPROTO_IPV4), uint32(16), addr.AsSlice()
	if addr.Is6() {
		fam, off = unix.NFPROTO_IPV6, 24
	}
	accept := &expr.Verdict{Kind: expr.VerdictAccept}
	// A redirected packet still has the output interface of its first
	// route in the hook (not lo), so loopback is also matched by the
	// destination address: 127.0.0.0/8 and ::1.
	v4 := []byte{unix.NFPROTO_IPV4}
	v6 := []byte{unix.NFPROTO_IPV6}
	return [][]expr.Any{
		{&expr.Meta{Key: expr.MetaKeyOIFNAME, Register: 1}, &expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: lon}, accept},
		{&expr.Meta{Key: expr.MetaKeyNFPROTO, Register: 1}, &expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: v4},
			&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseNetworkHeader, Offset: 16, Len: 4},
			&expr.Bitwise{SourceRegister: 1, DestRegister: 1, Len: 4, Mask: []byte{255, 0, 0, 0}, Xor: []byte{0, 0, 0, 0}},
			&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte{127, 0, 0, 0}}, accept},
		{&expr.Meta{Key: expr.MetaKeyNFPROTO, Register: 1}, &expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: v6},
			&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseNetworkHeader, Offset: 24, Len: 16},
			&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: net.IPv6loopback}, accept},
		// Only packets in the original direction of a connection that the
		// job started. A reply of an inbound connection (ct direction
		// reply) is not accepted, so a host that connects to the job
		// cannot get data back past the proxy (the input chain also drops
		// new inbound connections).
		append(ctEstablished(), &expr.Ct{Register: 1, Key: expr.CtKeyDIRECTION},
			&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte{0}}, accept),
		ndAccept(),
		{&expr.Meta{Key: expr.MetaKeyNFPROTO, Register: 1}, &expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte{fam}},
			&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseNetworkHeader, Offset: off, Len: uint32(len(ip))},
			&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: ip},
			&expr.Meta{Key: expr.MetaKeyL4PROTO, Register: 1}, &expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte{unix.IPPROTO_TCP}},
			&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseTransportHeader, Offset: 2, Len: 2},
			&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: binaryutil.BigEndian.PutUint16(exclude.Port())}, accept},
		{&expr.Verdict{Kind: expr.VerdictDrop}},
	}
}

// ctEstablished matches a packet of an established or related connection.
func ctEstablished() []expr.Any {
	return []expr.Any{&expr.Ct{Register: 1, Key: expr.CtKeySTATE},
		&expr.Bitwise{SourceRegister: 1, DestRegister: 1, Len: 4,
			Mask: binaryutil.NativeEndian.PutUint32(expr.CtStateBitESTABLISHED | expr.CtStateBitRELATED),
			Xor:  binaryutil.NativeEndian.PutUint32(0)},
		&expr.Cmp{Op: expr.CmpOpNeq, Register: 1, Data: []byte{0, 0, 0, 0}}}
}

// ndAccept accepts IPv6 neighbor discovery (ICMPv6 types 133 to 137), which
// the kernel needs to reach the proxy on an IPv6 network. A job without
// CAP_NET_RAW cannot write these messages itself.
func ndAccept() []expr.Any {
	return []expr.Any{&expr.Meta{Key: expr.MetaKeyL4PROTO, Register: 1},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte{unix.IPPROTO_ICMPV6}},
		&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseTransportHeader, Offset: 0, Len: 1},
		&expr.Range{Op: expr.CmpOpEq, Register: 1, FromData: []byte{133}, ToData: []byte{137}},
		&expr.Verdict{Kind: expr.VerdictAccept}}
}

// inputRules returns the rules of the filter input chain: loopback (the
// local proxy port, the transparent route, the fake DNS), packets of a
// connection that the job started, and IPv6 neighbor discovery are
// accepted. All other packets are dropped, so that no host can open a
// connection to the job. Without this chain, a host that reaches the job
// address (another pod, another container on the Docker network) connects
// in, and the job writes data back on that connection: the reply passes
// the output guard as an established connection.
func inputRules() [][]expr.Any {
	lon := make([]byte, 16)
	copy(lon, "lo")
	accept := &expr.Verdict{Kind: expr.VerdictAccept}
	return [][]expr.Any{
		{&expr.Meta{Key: expr.MetaKeyIIFNAME, Register: 1}, &expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: lon}, accept},
		// A reply to a connection that the job started has ct direction reply.
		append(ctEstablished(), &expr.Ct{Register: 1, Key: expr.CtKeyDIRECTION},
			&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte{1}}, accept),
		ndAccept(),
		{&expr.Verdict{Kind: expr.VerdictDrop}},
	}
}

// installRules installs the redirect and the guard in the network
// namespace of the process. It needs CAP_NET_ADMIN.
func installRules(exclude netip.AddrPort) error {
	c := &nftables.Conn{}
	t := c.AddTable(&nftables.Table{Family: nftables.TableFamilyINet, Name: TableName})
	ch := c.AddChain(&nftables.Chain{Name: "out", Table: t, Type: nftables.ChainTypeNAT,
		Hooknum: nftables.ChainHookOutput, Priority: nftables.ChainPriorityNATDest})
	for _, r := range podnetRules(exclude) {
		c.AddRule(&nftables.Rule{Table: t, Chain: ch, Exprs: r})
	}
	guard := c.AddChain(&nftables.Chain{Name: "guard", Table: t, Type: nftables.ChainTypeFilter,
		Hooknum: nftables.ChainHookOutput, Priority: nftables.ChainPriorityFilter})
	for _, r := range guardRules(exclude) {
		c.AddRule(&nftables.Rule{Table: t, Chain: guard, Exprs: r})
	}
	in := c.AddChain(&nftables.Chain{Name: "in", Table: t, Type: nftables.ChainTypeFilter,
		Hooknum: nftables.ChainHookInput, Priority: nftables.ChainPriorityFilter})
	for _, r := range inputRules() {
		c.AddRule(&nftables.Rule{Table: t, Chain: in, Exprs: r})
	}
	if err := c.Flush(); err != nil {
		return fmt.Errorf("firewall: %w", err)
	}
	return nil
}

// namespaceNetwork brings up loopback and a dummy interface with the
// default routes, so that a connection to any address reaches the
// firewall. The firewall sends each TCP connection that does not go to
// loopback to TransparentPort. Other traffic goes to the dummy interface,
// which drops it.
func namespaceNetwork() error {
	lo, err := netlink.LinkByName("lo")
	if err != nil {
		return err
	}
	if err := netlink.LinkSetUp(lo); err != nil {
		return err
	}
	d := &netlink.Dummy{LinkAttrs: netlink.LinkAttrs{Name: DummyInterface}}
	if err := netlink.LinkAdd(d); err != nil {
		return err
	}
	if err := netlink.LinkSetUp(d); err != nil {
		return err
	}
	for _, a := range []string{"10.255.0.2/24", "fd72:6365:6772::2/64"} {
		addr, _ := netlink.ParseAddr(a)
		addr.Flags = unix.IFA_F_NODAD
		if err := netlink.AddrAdd(d, addr); err != nil {
			return fmt.Errorf("address %s: %w", a, err)
		}
	}
	for _, def := range []string{"0.0.0.0/0", "::/0"} {
		_, dst, _ := net.ParseCIDR(def)
		if err := netlink.RouteAdd(&netlink.Route{LinkIndex: d.Attrs().Index, Dst: dst}); err != nil {
			return fmt.Errorf("route %s: %w", def, err)
		}
	}
	c := &nftables.Conn{}
	t := c.AddTable(&nftables.Table{Family: nftables.TableFamilyINet, Name: TableName})
	ch := c.AddChain(&nftables.Chain{Name: "out", Table: t, Type: nftables.ChainTypeNAT,
		Hooknum: nftables.ChainHookOutput, Priority: nftables.ChainPriorityNATDest})
	lon := make([]byte, 16)
	copy(lon, "lo")
	c.AddRule(&nftables.Rule{Table: t, Chain: ch, Exprs: []expr.Any{
		&expr.Meta{Key: expr.MetaKeyOIFNAME, Register: 1},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: lon},
		&expr.Verdict{Kind: expr.VerdictReturn},
	}})
	c.AddRule(&nftables.Rule{Table: t, Chain: ch, Exprs: []expr.Any{
		&expr.Meta{Key: expr.MetaKeyL4PROTO, Register: 1},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte{unix.IPPROTO_TCP}},
		&expr.Immediate{Register: 1, Data: binaryutil.BigEndian.PutUint16(TransparentPort)},
		&expr.Redir{RegisterProtoMin: 1, Flags: unix.NF_NAT_RANGE_PROTO_SPECIFIED},
	}})
	if err := c.Flush(); err != nil {
		return fmt.Errorf("firewall: %w", err)
	}
	return nil
}

// netnsExec sets up the network of a new network namespace
// (namespaceNetwork), and then runs args[1:] (args[0] is "--"). It must
// run as root of a new user namespace that owns the network namespace.
// Commands can then bind port 53, so that the fake DNS can answer at
// 127.0.0.53.
func netnsExec(args []string) error {
	if len(args) < 2 || args[0] != "--" {
		return errors.New("usage: reactorcide-egress-net netns -- PROGRAM [ARGS]")
	}
	if err := namespaceNetwork(); err != nil {
		return err
	}
	if err := os.WriteFile("/proc/sys/net/ipv4/ip_unprivileged_port_start", []byte("0"), 0); err != nil {
		return err
	}
	return unix.Exec(args[1], args[1:], os.Environ())
}

// origDst returns the destination of a redirected connection before the
// redirect (SO_ORIGINAL_DST).
func origDst(c net.Conn) (netip.AddrPort, error) {
	tc, ok := c.(*net.TCPConn)
	if !ok {
		return netip.AddrPort{}, errors.New("not a TCP connection")
	}
	rc, err := tc.SyscallConn()
	if err != nil {
		return netip.AddrPort{}, err
	}
	v6 := tc.LocalAddr().(*net.TCPAddr).IP.To4() == nil
	var out netip.AddrPort
	var serr error
	err = rc.Control(func(fd uintptr) {
		if v6 {
			var mi *unix.IPv6MTUInfo
			mi, serr = unix.GetsockoptIPv6MTUInfo(int(fd), unix.IPPROTO_IPV6, ip6tSoOriginalDst)
			if serr == nil {
				port := binary.BigEndian.Uint16((*[2]byte)(unsafe.Pointer(&mi.Addr.Port))[:])
				out = netip.AddrPortFrom(netip.AddrFrom16(mi.Addr.Addr), port)
			}
			return
		}
		var mr *unix.IPv6Mreq
		mr, serr = unix.GetsockoptIPv6Mreq(int(fd), unix.IPPROTO_IP, unix.SO_ORIGINAL_DST)
		if serr == nil {
			// The value is a sockaddr_in: family, port, address.
			b := mr.Multiaddr
			out = netip.AddrPortFrom(netip.AddrFrom4([4]byte{b[4], b[5], b[6], b[7]}), binary.BigEndian.Uint16(b[2:4]))
		}
	})
	if err == nil {
		err = serr
	}
	return out, err
}

// ip6tSoOriginalDst is IP6T_SO_ORIGINAL_DST (linux/netfilter_ipv6/ip6_tables.h).
const ip6tSoOriginalDst = 80
