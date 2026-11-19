package netd

import (
	"net/netip"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/net/dns/dnsmessage"
)

func query(t *testing.T, f *fakeDNS, name string, typ dnsmessage.Type) dnsmessage.Message {
	t.Helper()
	b := dnsmessage.NewBuilder(nil, dnsmessage.Header{ID: 7, RecursionDesired: true})
	b.StartQuestions()
	b.Question(dnsmessage.Question{Name: dnsmessage.MustNewName(name), Type: typ, Class: dnsmessage.ClassINET})
	q, _ := b.Finish()
	out, err := f.answer(q)
	if err != nil {
		t.Fatal(err)
	}
	var m dnsmessage.Message
	if err := m.Unpack(out); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestFakeDNS(t *testing.T) {
	f := newFakeDNS()
	m := query(t, f, "Example.org.", dnsmessage.TypeA)
	if m.Header.ID != 7 || len(m.Answers) != 1 {
		t.Fatalf("A answer: %+v", m)
	}
	a := m.Answers[0].Body.(*dnsmessage.AResource).A
	if a != [4]byte{198, 18, 0, 1} {
		t.Fatalf("address %v", a)
	}
	if m := query(t, f, "example.org.", dnsmessage.TypeA); m.Answers[0].Body.(*dnsmessage.AResource).A != a {
		t.Fatal("a name must keep its address")
	}
	if m := query(t, f, "other.example.", dnsmessage.TypeA); m.Answers[0].Body.(*dnsmessage.AResource).A == a {
		t.Fatal("two names got one address")
	}
	if n, ok := f.Name(netip.AddrFrom4(a)); !ok || n != "example.org" {
		t.Fatalf("name of %v: %q", a, n)
	}
	if m := query(t, f, "example.org.", dnsmessage.TypeAAAA); m.RCode != dnsmessage.RCodeSuccess || len(m.Answers) != 0 {
		t.Fatalf("AAAA: %+v", m)
	}
	// No forward: other types are refused.
	if m := query(t, f, "_linkkeys.example.org.", dnsmessage.TypeTXT); m.RCode != dnsmessage.RCodeRefused {
		t.Fatalf("TXT: %+v", m)
	}
}

func TestFakeDNSLocalhost(t *testing.T) {
	f := newFakeDNS()
	for _, n := range []string{"localhost.", "api.localhost."} {
		m := query(t, f, n, dnsmessage.TypeA)
		if len(m.Answers) != 1 || m.Answers[0].Body.(*dnsmessage.AResource).A != [4]byte{127, 0, 0, 1} {
			t.Fatalf("%s A: %+v", n, m)
		}
		m = query(t, f, n, dnsmessage.TypeAAAA)
		if len(m.Answers) != 1 || m.Answers[0].Body.(*dnsmessage.AAAAResource).AAAA != netip.IPv6Loopback().As16() {
			t.Fatalf("%s AAAA: %+v", n, m)
		}
		if m := query(t, f, n, dnsmessage.TypeTXT); m.RCode != dnsmessage.RCodeSuccess || len(m.Answers) != 0 {
			t.Fatalf("%s TXT: %+v", n, m)
		}
	}
}

// The Magi trap: a Pod resolver has search domains and ndots:5, so the
// first query is the name below a cluster domain. It must get NXDOMAIN, or
// the wrong name goes to the proxy.
func TestFakeDNSSearchDomains(t *testing.T) {
	dir := t.TempDir()
	rc := filepath.Join(dir, "resolv.conf")
	os.WriteFile(rc, []byte("search reactorcide.svc.cluster.local svc.cluster.local cluster.local.\nnameserver 10.43.0.10\noptions ndots:5\n"), 0o644)
	f := newFakeDNS()
	f.search = searchDomains(rc)
	if len(f.search) != 3 || f.search[2] != "cluster.local" {
		t.Fatalf("search domains %v", f.search)
	}
	if m := query(t, f, "pypi.org.reactorcide.svc.cluster.local.", dnsmessage.TypeA); m.RCode != dnsmessage.RCodeNameError || len(m.Answers) != 0 {
		t.Fatalf("search domain: %+v", m)
	}
	if m := query(t, f, "pypi.org.", dnsmessage.TypeA); len(m.Answers) != 1 {
		t.Fatalf("after the search domains: %+v", m)
	}
	if got := searchDomains(filepath.Join(dir, "missing")); got != nil {
		t.Fatalf("missing file: %v", got)
	}
}
