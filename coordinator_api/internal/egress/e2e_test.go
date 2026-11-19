package egress

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// proxyBinary finds the Rust proxy (egress_proxy/target). The test is
// skipped when it is not built: cargo build --release in egress_proxy/.
func proxyBinary(t *testing.T) string {
	t.Helper()
	if p := os.Getenv("REACTORCIDE_EGRESS_BIN"); p != "" {
		return p
	}
	_, file, _, _ := runtime.Caller(0)
	root := filepath.Join(filepath.Dir(file), "..", "..", "..", "egress_proxy", "target")
	for _, p := range []string{filepath.Join(root, "release", "reactorcide-egress"), filepath.Join(root, "debug", "reactorcide-egress")} {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	t.Skip("egress proxy binary not built (cargo build --release in egress_proxy/), or set REACTORCIDE_EGRESS_BIN")
	return ""
}

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

// TestProxyEndToEnd runs the real proxy and checks the contract of this
// package against it: the job credential, the JSON policy of the control
// API, the placeholder replacement only for an allowed host, the scan of a
// response, and the end of the credential after Unregister.
func TestProxyEndToEnd(t *testing.T) {
	bin := proxyBinary(t)
	dir := t.TempDir()

	// The upstream echoes the Authorization header it gets.
	var seen []string
	upstream := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Header.Get("Authorization"))
		fmt.Fprintf(w, "auth=%s", r.Header.Get("Authorization"))
	}))
	upCAPEM, leaf := upstreamCert(t)
	upstream.TLS = &tls.Config{Certificates: []tls.Certificate{leaf}}
	upstream.StartTLS()
	defer upstream.Close()
	upURL, _ := url.Parse(upstream.URL)
	upPort := upURL.Port()
	upCA := filepath.Join(dir, "upstream.crt")
	os.WriteFile(upCA, upCAPEM, 0o600)

	run := func(args ...string) {
		if out, err := exec.Command(bin, args...).CombinedOutput(); err != nil {
			t.Fatalf("%v: %v: %s", args, err, out)
		}
	}
	run("ca", "--out", dir)
	run("key", "--out", filepath.Join(dir, "job.key"))
	run("control-token", "--out", filepath.Join(dir, "control.token"))
	jobKey, err := ReadSecretFile(filepath.Join(dir, "job.key"))
	if err != nil {
		t.Fatal(err)
	}
	token, err := ReadSecretFile(filepath.Join(dir, "control.token"))
	if err != nil {
		t.Fatal(err)
	}

	apiPort, proxyPort := freePort(t), freePort(t)
	cmd := exec.Command(bin, "serve",
		"--listen", fmt.Sprintf("127.0.0.1:%d", apiPort),
		"--proxy-listen", fmt.Sprintf("127.0.0.1:%d", proxyPort),
		"--control-token", filepath.Join(dir, "control.token"),
		"--ca-cert", filepath.Join(dir, "ca.crt"), "--ca-key", filepath.Join(dir, "ca.key"),
		"--job-key", filepath.Join(dir, "job.key"),
		"--upstream-ca", upCA)
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	control := &Client{BaseURL: fmt.Sprintf("http://127.0.0.1:%d", apiPort), Token: string(token)}
	deadline := time.Now().Add(20 * time.Second)
	for {
		res, err := http.Get(control.BaseURL + "/healthz")
		if err == nil {
			res.Body.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the proxy did not start")
		}
		time.Sleep(100 * time.Millisecond)
	}

	caPEM, _ := os.ReadFile(filepath.Join(dir, "ca.crt"))
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(caPEM)
	client := func(job string) *http.Client {
		pu := &url.URL{Scheme: "http", User: url.UserPassword(job, Credential(jobKey, job)), Host: fmt.Sprintf("127.0.0.1:%d", proxyPort)}
		return &http.Client{Timeout: 20 * time.Second, Transport: &http.Transport{
			Proxy:           http.ProxyURL(pu),
			TLSClientConfig: &tls.Config{RootCAs: pool},
		}}
	}
	get := func(job, auth string) (int, string) {
		req, _ := http.NewRequest(http.MethodGet, upstream.URL+"/x", nil)
		req.Header.Set("Authorization", auth)
		res, err := client(job).Do(req)
		if err != nil {
			return 0, err.Error()
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(b)
	}

	const secret = "ghp-real-secret-value-0123456789"
	dest := []Destination{{Host: "127.0.0.1", Ports: []int{atoi(t, upPort)}}}
	phA, _ := NewPlaceholder()
	phB, _ := NewPlaceholder()
	ctx := context.Background()
	if err := control.Register(ctx, &JobPolicy{JobID: "job-a", Identity: Identity{OrgID: "o"}, Destinations: dest,
		Grants: []Grant{{ID: "g-a", SecretRef: "org/gh:token", Placeholder: phA, Value: secret, Hosts: []string{"127.0.0.1"}, Format: "Bearer {}"}}}); err != nil {
		t.Fatal(err)
	}
	// Job B has the same secret bound to another host.
	if err := control.Register(ctx, &JobPolicy{JobID: "job-b", Destinations: dest,
		Grants: []Grant{{ID: "g-b", Placeholder: phB, Value: secret, Hosts: []string{"api.github.com"}}}}); err != nil {
		t.Fatal(err)
	}

	code, body := get("job-a", phA)
	if code != 200 {
		t.Fatalf("allowed host: %d %s", code, body)
	}
	if len(seen) != 1 || seen[0] != "Bearer "+secret {
		t.Fatalf("upstream saw %q", seen)
	}
	if strings.Contains(body, secret) {
		t.Fatalf("the echo of the secret must be redacted: %q", body)
	}

	code, body = get("job-b", "Bearer "+phB)
	if code != http.StatusForbidden || len(seen) != 1 {
		t.Fatalf("a host that the grant does not permit: %d %q (upstream saw %d requests)", code, body, len(seen))
	}

	// A job cannot use the placeholder of another job.
	if code, _ = get("job-b", phA); code != http.StatusForbidden || len(seen) != 1 {
		t.Fatalf("placeholder of another job: %d", code)
	}

	sum, err := control.Unregister(ctx, "job-a")
	if err != nil || sum.Flows < 1 || len(sum.SecretsUsed) != 1 || sum.SecretsUsed[0] != "g-a" {
		t.Fatalf("summary %+v %v", sum, err)
	}
	if code, _ = get("job-a", phA); code == 200 || len(seen) != 1 {
		t.Fatalf("after Unregister the credential must stop working: %d", code)
	}
	bad := &Client{BaseURL: control.BaseURL, Token: strings.Repeat("x", 64)}
	if err := bad.Register(ctx, &JobPolicy{JobID: "job-c"}); err == nil {
		t.Fatal("a wrong control token must be refused")
	}
}

func atoi(t *testing.T, s string) int {
	var n int
	if _, err := fmt.Sscanf(s, "%d", &n); err != nil {
		t.Fatal(err)
	}
	return n
}

// upstreamCert makes a CA and a leaf certificate for 127.0.0.1 (webpki in
// the proxy refuses a CA certificate as the server certificate).
func upstreamCert(t *testing.T) ([]byte, tls.Certificate) {
	t.Helper()
	caKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	caTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "upstream test CA"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	ca, _ := x509.ParseCertificate(caDER)
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "127.0.0.1"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, KeyUsage: x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca, &key.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}), tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}
