package netd

import (
	"bufio"
	"encoding/binary"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

// Copied from Magi primitives/execution/envd/vm.go (fake DNS).

// FakeNet holds the addresses that the job DNS gives to names. The
// transparent route changes a fake address back to its name, so that the
// proxy gets the name of each connection, not an address.
var FakeNet = netip.MustParsePrefix("198.18.0.0/15")

// fakeDNS answers A queries with addresses in FakeNet and remembers the
// name of each address. It answers AAAA with no address, so that clients
// use IPv4. The egress proxy resolves the names of connections. Other
// query types go to the proxy (forward).
type fakeDNS struct {
	// forward sends a query of another type (TXT, SRV, ...) to the egress
	// proxy, which answers it by its policy. nil refuses such queries.
	forward func(q []byte) ([]byte, error)
	// search are the search domains of the resolver (in a Pod, the
	// cluster domains). A name below one is the resolver trying a search
	// domain, not a destination: it gets "no such name".
	search []string

	mu     sync.Mutex
	next   netip.Addr
	byName map[string]netip.Addr
	byAddr map[netip.Addr]string
}

func newFakeDNS() *fakeDNS {
	return &fakeDNS{next: FakeNet.Addr().Next(), byName: map[string]netip.Addr{}, byAddr: map[netip.Addr]string{}}
}

func (f *fakeDNS) addr(name string) (netip.Addr, bool) {
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
func (f *fakeDNS) Name(a netip.Addr) (string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	n, ok := f.byAddr[a.Unmap()]
	return n, ok
}

// answer returns the answer to one DNS message.
func (f *fakeDNS) answer(q []byte) ([]byte, error) {
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
	b.StartQuestions()
	b.Question(qq)
	b.StartAnswers()
	name := strings.ToLower(strings.TrimSuffix(qq.Name.String(), "."))
	for _, d := range f.search {
		if strings.HasSuffix(name, "."+d) {
			b = dnsmessage.NewBuilder(nil, dnsmessage.Header{ID: h.ID, Response: true, RecursionAvailable: true, RCode: dnsmessage.RCodeNameError})
			b.StartQuestions()
			b.Question(qq)
			return b.Finish()
		}
	}
	// "localhost" and names below it are this machine (RFC 6761, 6.3):
	// loopback, never a destination through the proxy.
	local := name == "localhost" || strings.HasSuffix(name, ".localhost")
	switch {
	case local && qq.Type == dnsmessage.TypeA:
		b.AResource(dnsmessage.ResourceHeader{Name: qq.Name, Class: dnsmessage.ClassINET, TTL: 600},
			dnsmessage.AResource{A: [4]byte{127, 0, 0, 1}})
		return b.Finish()
	case local && qq.Type == dnsmessage.TypeAAAA:
		b.AAAAResource(dnsmessage.ResourceHeader{Name: qq.Name, Class: dnsmessage.ClassINET, TTL: 600},
			dnsmessage.AAAAResource{AAAA: netip.IPv6Loopback().As16()})
		return b.Finish()
	case local:
		return b.Finish() // no other records
	}
	switch qq.Type {
	case dnsmessage.TypeA:
		if a, ok := f.addr(name); ok {
			b.AResource(dnsmessage.ResourceHeader{Name: qq.Name, Class: dnsmessage.ClassINET, TTL: 600},
				dnsmessage.AResource{A: a.As4()})
		}
	case dnsmessage.TypeAAAA:
		// No IPv6 address: the client uses the A answer.
	default:
		if f.forward != nil {
			if out, err := f.forward(q); err == nil {
				return out, nil
			}
		}
		b = dnsmessage.NewBuilder(nil, dnsmessage.Header{ID: h.ID, Response: true, RCode: dnsmessage.RCodeRefused})
		b.StartQuestions()
		b.Question(qq)
	}
	return b.Finish()
}

// searchDomains returns the search domains in the resolv.conf at path.
func searchDomains(path string) []string {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var out []string
	for _, line := range strings.Split(string(b), "\n") {
		if f := strings.Fields(line); len(f) > 1 && (f[0] == "search" || f[0] == "domain") {
			for _, d := range f[1:] {
				if d = strings.ToLower(strings.Trim(d, ".")); d != "" {
					out = append(out, d)
				}
			}
		}
	}
	return out
}

// serve serves DNS on UDP and TCP at addr.
func (f *fakeDNS) serve(addr string, log *slog.Logger) error {
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
			if out, err := f.answer(buf[:n]); err == nil {
				pc.WriteTo(out, from)
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
				c.SetDeadline(time.Now().Add(10 * time.Second))
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
					out, err := f.answer(q)
					if err != nil {
						return
					}
					c.Write(binary.BigEndian.AppendUint16(nil, uint16(len(out))))
					c.Write(out)
				}
			}()
		}
	}()
	return nil
}
