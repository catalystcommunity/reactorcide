package netd

import (
	"bytes"
	"net"
	"net/netip"
	"testing"

	"github.com/google/nftables/binaryutil"
	"github.com/google/nftables/expr"
	"golang.org/x/sys/unix"
)

func redirPort(r []expr.Any) (uint16, bool) {
	if len(r) < 2 {
		return 0, false
	}
	im, ok := r[len(r)-2].(*expr.Immediate)
	if _, isRedir := r[len(r)-1].(*expr.Redir); !ok || !isRedir {
		return 0, false
	}
	return binaryutil.BigEndian.Uint16(im.Data), true
}

func isReturn(r []expr.Any) bool {
	v, ok := r[len(r)-1].(*expr.Verdict)
	return ok && v.Kind == expr.VerdictReturn
}

func hasCmp(r []expr.Any, data []byte) bool {
	for _, e := range r {
		if c, ok := e.(*expr.Cmp); ok && bytes.Equal(c.Data, data) {
			return true
		}
	}
	return false
}

func TestPodnetRules(t *testing.T) {
	for _, tc := range []struct {
		exclude string
		fam     byte
		off     uint32
	}{{"10.43.7.9:3128", unix.NFPROTO_IPV4, 16}, {"[fd00::7]:3128", unix.NFPROTO_IPV6, 24}} {
		px := netip.MustParseAddrPort(tc.exclude)
		rules := podnetRules(px)
		if len(rules) != 5 {
			t.Fatalf("%d rules", len(rules))
		}
		lo := make([]byte, 16)
		copy(lo, "lo")
		if !isReturn(rules[0]) || !hasCmp(rules[0], lo) {
			t.Fatal("rule 0: loopback returns")
		}
		r := rules[1]
		if !isReturn(r) || !hasCmp(r, []byte{tc.fam}) || !hasCmp(r, px.Addr().AsSlice()) ||
			!hasCmp(r, binaryutil.BigEndian.PutUint16(3128)) || !hasCmp(r, []byte{unix.IPPROTO_TCP}) {
			t.Fatalf("rule 1: the proxy returns (%s)", tc.exclude)
		}
		if p, ok := r[2].(*expr.Payload); !ok || p.Offset != tc.off || p.Base != expr.PayloadBaseNetworkHeader {
			t.Fatalf("rule 1: destination address offset %+v", r[2])
		}
		if p, ok := redirPort(rules[2]); !ok || p != DNSPort || !hasCmp(rules[2], []byte{unix.IPPROTO_UDP}) || !hasCmp(rules[2], binaryutil.BigEndian.PutUint16(53)) {
			t.Fatal("rule 2: UDP DNS to the fake DNS")
		}
		if p, ok := redirPort(rules[3]); !ok || p != DNSPort || !hasCmp(rules[3], []byte{unix.IPPROTO_TCP}) || !hasCmp(rules[3], binaryutil.BigEndian.PutUint16(53)) {
			t.Fatal("rule 3: TCP DNS to the fake DNS")
		}
		if p, ok := redirPort(rules[4]); !ok || p != TransparentPort || !hasCmp(rules[4], []byte{unix.IPPROTO_TCP}) {
			t.Fatal("rule 4: all other TCP to the transparent port")
		}
	}
}

func TestGuardRules(t *testing.T) {
	px := netip.MustParseAddrPort("10.43.7.9:3128")
	rules := guardRules(px)
	if len(rules) != 7 {
		t.Fatalf("%d rules", len(rules))
	}
	verdict := func(r []expr.Any) expr.VerdictKind {
		v, _ := r[len(r)-1].(*expr.Verdict)
		if v == nil {
			return -100
		}
		return v.Kind
	}
	lo := make([]byte, 16)
	copy(lo, "lo")
	if verdict(rules[0]) != expr.VerdictAccept || !hasCmp(rules[0], lo) {
		t.Fatal("rule 0: loopback (redirected traffic) is accepted")
	}
	if verdict(rules[1]) != expr.VerdictAccept || !hasCmp(rules[1], []byte{127, 0, 0, 0}) {
		t.Fatal("rule 1: IPv4 loopback destinations (redirected packets) are accepted")
	}
	if verdict(rules[2]) != expr.VerdictAccept || !hasCmp(rules[2], net.IPv6loopback) {
		t.Fatal("rule 2: the IPv6 loopback destination is accepted")
	}
	if _, ok := rules[3][0].(*expr.Ct); !ok || verdict(rules[3]) != expr.VerdictAccept || !hasCtDirection(rules[3], 0) {
		t.Fatal("rule 3: established connections are accepted only in the original direction")
	}
	if verdict(rules[4]) != expr.VerdictAccept || !hasCmp(rules[4], []byte{unix.IPPROTO_ICMPV6}) {
		t.Fatal("rule 4: IPv6 neighbor discovery is accepted")
	}
	r := rules[5]
	if verdict(r) != expr.VerdictAccept || !hasCmp(r, px.Addr().AsSlice()) || !hasCmp(r, binaryutil.BigEndian.PutUint16(3128)) ||
		!hasCmp(r, []byte{unix.IPPROTO_TCP}) {
		t.Fatal("rule 4: TCP to the proxy is accepted")
	}
	if len(rules[6]) != 1 || verdict(rules[6]) != expr.VerdictDrop {
		t.Fatal("rule 6: all other packets are dropped")
	}
}

func hasCtDirection(r []expr.Any, dir byte) bool {
	for i, e := range r {
		if c, ok := e.(*expr.Ct); ok && c.Key == expr.CtKeyDIRECTION && i+1 < len(r) {
			if cmp, ok := r[i+1].(*expr.Cmp); ok && bytes.Equal(cmp.Data, []byte{dir}) {
				return true
			}
		}
	}
	return false
}

func TestInputRules(t *testing.T) {
	rules := inputRules()
	if len(rules) != 4 {
		t.Fatalf("%d rules", len(rules))
	}
	lo := make([]byte, 16)
	copy(lo, "lo")
	if m, ok := rules[0][0].(*expr.Meta); !ok || m.Key != expr.MetaKeyIIFNAME || !hasCmp(rules[0], lo) {
		t.Fatal("rule 0: input from loopback is accepted")
	}
	if !hasCtDirection(rules[1], 1) {
		t.Fatal("rule 1: only replies of connections that the job started are accepted")
	}
	if v, ok := rules[3][0].(*expr.Verdict); !ok || len(rules[3]) != 1 || v.Kind != expr.VerdictDrop {
		t.Fatal("rule 3: all other input (new inbound connections) is dropped")
	}
}
