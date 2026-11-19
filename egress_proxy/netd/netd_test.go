package netd

import (
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"testing"
)

const testCA = "-----BEGIN CERTIFICATE-----\nMIIBszCCAVmgAwIBAgIUQ2b2\n-----END CERTIFICATE-----\n"

func TestWriteCA(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "egress")
	if err := writeCA(dir, testCA); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, CAFileName)
	b, err := os.ReadFile(p)
	if err != nil || string(b) != testCA {
		t.Fatalf("ca file %q %v", b, err)
	}
	if st, _ := os.Stat(p); st.Mode().Perm() != 0o644 {
		t.Fatalf("mode %v", st.Mode())
	}
	if err := writeCA(dir, "not a certificate"); err == nil {
		t.Fatal("a value that is not PEM must fail")
	}
	if err := writeCA("", testCA); err != nil {
		t.Fatal("no dir writes nothing")
	}
	if err := writeCA(filepath.Join(t.TempDir(), "none"), ""); err != nil {
		t.Fatal(err)
	}
}

func TestParseServe(t *testing.T) {
	f, err := parseServe([]string{"-proxy", "10.43.1.2:3128"})
	if err != nil {
		t.Fatal(err)
	}
	if f.proxy != netip.MustParseAddrPort("10.43.1.2:3128") || f.exclude != f.proxy || f.listen != LocalProxyAddr ||
		len(f.dns) != 1 || f.dns[0] != "127.0.0.1:15053" || f.setup {
		t.Fatalf("defaults %+v", f)
	}
	f, err = parseServe([]string{"-proxy", "10.0.0.1:3128", "-setup", "-exclude", "10.0.0.9:3128", "-listen", "", "-dns", "127.0.0.1:15053, [::1]:15053", "-ca-dir", "/x"})
	if err != nil {
		t.Fatal(err)
	}
	if !f.setup || f.exclude.String() != "10.0.0.9:3128" || f.listen != "" || len(f.dns) != 2 || f.caDir != "/x" {
		t.Fatalf("flags %+v", f)
	}
	for _, bad := range [][]string{{}, {"-proxy", "egress:3128"}, {"-proxy", "10.0.0.1:3128", "-exclude", "x"}, {"-nope"}} {
		if _, err := parseServe(bad); err == nil {
			t.Fatalf("%v must fail", bad)
		}
	}
}

func TestParsePodnet(t *testing.T) {
	f, err := parsePodnet([]string{"-exclude", "[fd00::1]:3128", "-ca-dir", "/etc/reactorcide/egress"})
	if err != nil || f.exclude.String() != "[fd00::1]:3128" || f.caDir != "/etc/reactorcide/egress" {
		t.Fatalf("%+v %v", f, err)
	}
	if _, err := parsePodnet(nil); err == nil {
		t.Fatal("-exclude is required")
	}
}

func TestProxyURL(t *testing.T) {
	t.Setenv(EnvProxyUser, "job_a")
	t.Setenv(EnvProxyPassword, "cred")
	u := proxyURL(netip.MustParseAddrPort("10.0.0.1:3128"))
	if u.String() != "http://job_a:cred@10.0.0.1:3128" {
		t.Fatalf("url %s", u)
	}
	t.Setenv(EnvProxyUser, "")
	if u := proxyURL(netip.MustParseAddrPort("10.0.0.1:3128")); u.User != nil {
		t.Fatal("no user: no credential")
	}
}

func TestMain_Commands(t *testing.T) {
	if Main(nil) != 2 || Main([]string{"nope"}) != 1 || Main([]string{"help"}) != 0 {
		t.Fatal("exit status")
	}
	if Main([]string{"netns"}) != 1 || Main([]string{"podnet"}) != 1 || Main([]string{"serve"}) != 1 {
		t.Fatal("missing arguments must fail")
	}
}

func TestCheckCommand(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	if code := Main([]string{"check", "-addr", addr}); code != 0 {
		t.Fatalf("check of a listening port = %d", code)
	}
	ln.Close()
	if code := Main([]string{"check", "-addr", addr}); code != 1 {
		t.Fatalf("check of a closed port = %d", code)
	}
}
