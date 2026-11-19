package vmrunner

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"
)

func TestJobCredentialHeaderVector(t *testing.T) {
	h := JobCredentialHeader([]byte("0123456789abcdef0123456789abcdef"))(EgressFlow{JobID: "job_a", Destination: "api.github.com:443"})
	want := "Basic " + base64.StdEncoding.EncodeToString([]byte("job_a:2c896a41f17c5e4c9702168deb85d829ad3d5a1a7d00ae400638f3d0330548e0"))
	if got := h.Get("Proxy-Authorization"); got != want {
		t.Fatalf("header = %q, want %q", got, want)
	}
	if JobCredentialHeader([]byte("0123456789abcdef0123456789abcdef"))(EgressFlow{}) != nil {
		t.Fatal("a flow without a job gets no credential")
	}
}

func TestEgressDialerFromEnvSetsJobCredential(t *testing.T) {
	key := filepath.Join(t.TempDir(), "job.key")
	if err := os.WriteFile(key, []byte("0123456789abcdef0123456789abcdef\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(EgressProxyEnv, "127.0.0.1:3128")
	t.Setenv(EgressProxyKeyFileEnv, key)
	d, err := EgressDialerFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	p, ok := d.(*ProxyEgressDialer)
	if !ok || p.Header == nil {
		t.Fatalf("dialer %T without the credential hook", d)
	}
	if p.Header(EgressFlow{JobID: "job_a"}).Get("Proxy-Authorization") == "" {
		t.Fatal("no credential")
	}
	if err := EgressProxyConfigured(); err != nil {
		t.Fatal(err)
	}

	t.Setenv(EgressProxyKeyFileEnv, "")
	t.Setenv(workerJobKeyFileEnv, key)
	if err := EgressProxyConfigured(); err != nil {
		t.Fatalf("the worker job key is the fallback: %v", err)
	}
	t.Setenv(workerJobKeyFileEnv, "")
	if EgressProxyConfigured() == nil {
		t.Fatal("no key: egress control must fail closed")
	}
	d, err = EgressDialerFromEnv()
	if err != nil || d.(*ProxyEgressDialer).Header != nil {
		t.Fatal("no key: the proxy dialer sends no credential (as before)")
	}

	short := filepath.Join(t.TempDir(), "short.key")
	os.WriteFile(short, []byte("short"), 0o600)
	t.Setenv(EgressProxyKeyFileEnv, short)
	if _, err := EgressDialerFromEnv(); err == nil {
		t.Fatal("a short key must be refused")
	}
	t.Setenv(EgressProxyEnv, "")
	if EgressProxyConfigured() == nil {
		t.Fatal("no proxy: egress control must fail closed")
	}
}
