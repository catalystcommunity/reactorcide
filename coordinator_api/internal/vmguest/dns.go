package vmguest

import (
	"bufio"
	"encoding/binary"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

// FakeNet holds the addresses that the guest DNS gives to names. No real
// DNS query leaves the guest: the egress forwarder (or the egress proxy)
// resolves the name of each flow on the host.
var FakeNet = netip.MustParsePrefix("198.18.0.0/15")

// FakeDNS answers A queries with addresses in FakeNet and remembers the name
// of each address. It answers AAAA with no address, so that clients use
// IPv4. It refuses other query types. Copied from the Magi envd.
type FakeDNS struct {
	// Search are the search domains of the resolver. A name below one is the
	// resolver trying a search domain, not a destination: it gets "no such
	// name".
	Search []string

	mu     sync.Mutex
	next   netip.Addr
	byName map[string]netip.Addr
	byAddr map[netip.Addr]string
}

// NewFakeDNS returns an empty resolver.
func NewFakeDNS() *FakeDNS {
	return &FakeDNS{next: FakeNet.Addr().Next(), byName: map[string]netip.Addr{}, byAddr: map[netip.Addr]string{}}
}

func (f *FakeDNS) addr(name string) (netip.Addr, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if a, ok := f.byName[name]; ok {
		return a, true
	}
	if !FakeNet.Contains(f.next) {
		return netip.Addr{}, false
	}
	a := f.next
	f.next = a.Next()
	f.byName[name], f.byAddr[a] = a, name
	return a, true
}

// Name returns the name of a fake address.
func (f *FakeDNS) Name(a netip.Addr) (string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	n, ok := f.byAddr[a.Unmap()]
	return n, ok
}

// Answer returns the answer to one DNS message.
func (f *FakeDNS) Answer(q []byte) ([]byte, error) {
	var p dnsmessage.Parser
	h, err := p.Start(q)
	if err != nil {
		return nil, err
	}
	qs, err := p.AllQuestions()
	if err != nil {
		return nil, err
	}
	b := dnsmessage.NewBuilder(nil, dnsmessage.Header{ID: h.ID, Response: true, RecursionDesired: h.RecursionDesired,
		RecursionAvailable: true, OpCode: h.OpCode})
	b.EnableCompression()
	if len(qs) != 1 || h.OpCode != 0 {
		b = dnsmessage.NewBuilder(nil, dnsmessage.Header{ID: h.ID, Response: true, RCode: dnsmessage.RCodeNotImplemented})
		return b.Finish()
	}
	qq := qs[0]
	_ = b.StartQuestions()
	_ = b.Question(qq)
	_ = b.StartAnswers()
	name := strings.ToLower(strings.TrimSuffix(qq.Name.String(), "."))
	for _, d := range f.Search {
		if strings.HasSuffix(name, "."+d) {
			b = dnsmessage.NewBuilder(nil, dnsmessage.Header{ID: h.ID, Response: true, RecursionAvailable: true, RCode: dnsmessage.RCodeNameError})
			_ = b.StartQuestions()
			_ = b.Question(qq)
			return b.Finish()
		}
	}
	// "localhost" and names below it are this machine (RFC 6761, 6.3).
	local := name == "localhost" || strings.HasSuffix(name, ".localhost")
	switch {
	case local && qq.Type == dnsmessage.TypeA:
		_ = b.AResource(dnsmessage.ResourceHeader{Name: qq.Name, Class: dnsmessage.ClassINET, TTL: 600},
			dnsmessage.AResource{A: [4]byte{127, 0, 0, 1}})
		return b.Finish()
	case local && qq.Type == dnsmessage.TypeAAAA:
		_ = b.AAAAResource(dnsmessage.ResourceHeader{Name: qq.Name, Class: dnsmessage.ClassINET, TTL: 600},
			dnsmessage.AAAAResource{AAAA: netip.IPv6Loopback().As16()})
		return b.Finish()
	case local:
		return b.Finish()
	}
	switch qq.Type {
	case dnsmessage.TypeA:
		if a, ok := f.addr(name); ok {
			_ = b.AResource(dnsmessage.ResourceHeader{Name: qq.Name, Class: dnsmessage.ClassINET, TTL: 600},
				dnsmessage.AResource{A: a.As4()})
		}
	case dnsmessage.TypeAAAA:
		// No IPv6 address: the client uses the A answer.
	default:
		b = dnsmessage.NewBuilder(nil, dnsmessage.Header{ID: h.ID, Response: true, RCode: dnsmessage.RCodeRefused})
		_ = b.StartQuestions()
		_ = b.Question(qq)
	}
	return b.Finish()
}

// Serve serves DNS on UDP and TCP at addr.
func (f *FakeDNS) Serve(addr string, log *slog.Logger) error {
	pc, err := net.ListenPacket("udp", addr)
	if err != nil {
		return err
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		pc.Close()
		return err
	}
	go func() {
		buf := make([]byte, 1500)
		for {
			n, from, err := pc.ReadFrom(buf)
			if err != nil {
				log.Error("dns", "err", err)
				return
			}
			if out, err := f.Answer(buf[:n]); err == nil {
				_, _ = pc.WriteTo(out, from)
			}
		}
	}()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				_ = c.SetDeadline(time.Now().Add(10 * time.Second))
				r := bufio.NewReader(c)
				for {
					var n uint16
					if binary.Read(r, binary.BigEndian, &n) != nil {
						return
					}
					q := make([]byte, n)
					if _, err := io.ReadFull(r, q); err != nil {
						return
					}
					out, err := f.Answer(q)
					if err != nil {
						return
					}
					_, _ = c.Write(append(binary.BigEndian.AppendUint16(nil, uint16(len(out))), out...))
				}
			}()
		}
	}()
	return nil
}
