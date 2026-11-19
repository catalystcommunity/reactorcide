package worker

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/egress"
)

// TestDockerEgressEndToEnd runs a real Docker job with egress control: the
// helper and setup containers, the nftables redirect in the job namespace,
// the local proxy port, the transparent route, the CA trust, and the
// placeholder replacement by the real proxy. It needs Docker and the
// egress image:
//
//	docker build -t reactorcide/egress:dev egress_proxy   (DOCKER_BUILDKIT=0 without buildx)
//	REACTORCIDE_EGRESS_DOCKER_E2E=1 REACTORCIDE_EGRESS_IMAGE=reactorcide/egress:dev \
//	  go test ./internal/worker/ -run TestDockerEgressEndToEnd -v
//
// REACTORCIDE_EGRESS_E2E_JOB_IMAGE is any local Linux image (default
// postgres:17); the test mounts a static client program into it.
func TestDockerEgressEndToEnd(t *testing.T) {
	if os.Getenv("REACTORCIDE_EGRESS_DOCKER_E2E") != "1" {
		t.Skip("set REACTORCIDE_EGRESS_DOCKER_E2E=1 (needs Docker and the egress image)")
	}
	image := envOr("REACTORCIDE_EGRESS_IMAGE", "reactorcide/egress:dev")
	jobImage := envOr("REACTORCIDE_EGRESS_E2E_JOB_IMAGE", "postgres:17")
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	docker := func(args ...string) string {
		t.Helper()
		out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
		if err != nil {
			t.Fatalf("docker %v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	suffix := strconv.FormatInt(time.Now().UnixNano(), 36)
	network := "rc-egress-e2e-" + suffix
	docker("network", "create", network)
	t.Cleanup(func() { _ = exec.Command("docker", "network", "rm", network).Run() })
	gateway := docker("network", "inspect", "-f", "{{(index .IPAM.Config 0).Gateway}}", network)

	// Upstream on the host, reachable from the network at its gateway.
	var mu sync.Mutex
	var seen []string
	upCA, upCert := e2eCert(t, gateway)
	ln, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	upPort := ln.Addr().(*net.TCPAddr).Port
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Header.Get("Authorization"))
		mu.Unlock()
		fmt.Fprint(w, "upstream-ok")
	}), TLSConfig: &tls.Config{Certificates: []tls.Certificate{upCert}}}
	go srv.ServeTLS(ln, "", "")
	defer srv.Close()

	// Proxy keys and the proxy container.
	keys := t.TempDir()
	os.Chmod(keys, 0o777)
	user := fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid())
	docker("run", "--rm", "--user", user, "-v", keys+":/keys", image, "ca", "--out", "/keys")
	jobKey := make([]byte, 32)
	rand.Read(jobKey)
	os.WriteFile(filepath.Join(keys, "job.key"), []byte(hex.EncodeToString(jobKey)), 0o600)
	token := make([]byte, 32)
	rand.Read(token)
	os.WriteFile(filepath.Join(keys, "control.token"), []byte(hex.EncodeToString(token)), 0o600)
	os.WriteFile(filepath.Join(keys, "upstream.crt"), upCA, 0o644)
	proxyName := "rc-egress-e2e-proxy-" + suffix
	docker("run", "-d", "--name", proxyName, "--network", network, "--user", user, "-v", keys+":/keys:ro",
		"--add-host", "upstream.test:"+gateway,
		"-p", "127.0.0.1::8080", image, "serve",
		"--ca-cert", "/keys/ca.crt", "--ca-key", "/keys/ca.key", "--job-key", "/keys/job.key",
		"--control-token", "/keys/control.token", "--upstream-ca", "/keys/upstream.crt")
	t.Cleanup(func() {
		if t.Failed() {
			out, _ := exec.Command("docker", "logs", proxyName).CombinedOutput()
			t.Logf("proxy logs:\n%s", out)
		}
		_ = exec.Command("docker", "rm", "-f", proxyName).Run()
	})
	proxyIP := docker("inspect", "-f", "{{(index .NetworkSettings.Networks \""+network+"\").IPAddress}}", proxyName)
	controlAddr := docker("port", proxyName, "8080/tcp")
	control := &egress.Client{BaseURL: "http://" + strings.Split(controlAddr, "\n")[0], Token: hex.EncodeToString(token)}
	for i := 0; ; i++ {
		if res, err := http.Get(control.BaseURL + "/healthz"); err == nil {
			res.Body.Close()
			break
		}
		if i > 100 {
			t.Fatal("the proxy did not start")
		}
		time.Sleep(100 * time.Millisecond)
	}

	jobID := "e2e-" + suffix
	ph, _ := egress.NewPlaceholder()
	const secret = "real-secret-value-0123456789"
	if err := control.Register(ctx, &egress.JobPolicy{JobID: jobID,
		Destinations: []egress.Destination{{Host: gateway, Ports: []int{upPort}}, {Host: "upstream.test", Ports: []int{upPort}}},
		Grants:       []egress.Grant{{ID: "g1", Placeholder: ph, Value: secret, Hosts: []string{gateway, "upstream.test"}, Format: "Bearer {}"}},
	}); err != nil {
		t.Fatal(err)
	}

	client := buildE2EClient(t)
	ws := t.TempDir()
	os.Chmod(ws, 0o777)
	caPEM, _ := os.ReadFile(filepath.Join(keys, "ca.crt"))
	dr, err := NewDockerRunner()
	if err != nil {
		t.Fatal(err)
	}
	cfg := &JobConfig{
		JobID: jobID, Image: jobImage, Command: []string{"/e2e-client"}, WorkspaceDir: ws,
		Env: map[string]string{"TOKEN": ph, "UPSTREAM": fmt.Sprintf("https://%s:%d/", gateway, upPort),
			"UPSTREAM_NAME": fmt.Sprintf("https://upstream.test:%d/", upPort), "HOME": "/tmp"},
		ExtraMounts: []string{client + ":/e2e-client:ro"},
		Egress: &EgressConfig{ProxyHost: proxyIP, ProxyIP: proxyIP, ProxyPort: 3128, Username: jobID,
			Password: egress.Credential([]byte(hex.EncodeToString(jobKey)), jobID), CACertPEM: string(caPEM), NetImage: image, Network: network},
	}
	id, err := dr.SpawnJob(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	// Dial into the job namespace from the host while the job serves data.
	helperIP := docker("inspect", "-f", "{{(index .NetworkSettings.Networks \""+network+"\").IPAddress}}", EgressHelperName(jobID))
	inbound := make(chan string, 1)
	go func() {
		var got []byte
		deadline := time.Now().Add(12 * time.Second)
		for time.Now().Before(deadline) && len(got) == 0 {
			c, err := net.DialTimeout("tcp", net.JoinHostPort(helperIP, "9100"), time.Second)
			if err != nil {
				time.Sleep(200 * time.Millisecond)
				continue
			}
			c.SetDeadline(time.Now().Add(time.Second))
			got, _ = io.ReadAll(c)
			c.Close()
		}
		inbound <- string(got)
	}()
	stdout, _, _ := dr.StreamLogs(ctx, id)
	code, err := dr.WaitForCompletion(ctx, id)
	var out []byte
	if stdout != nil {
		out, _ = io.ReadAll(stdout)
	}
	if err := dr.Cleanup(context.Background(), id); err != nil {
		t.Errorf("cleanup: %v", err)
	}
	if err != nil || code != 0 {
		t.Fatalf("job exit %d %v:\n%s", code, err, out)
	}
	t.Logf("job output:\n%s", out)
	if got := <-inbound; got != "" {
		t.Errorf("an inbound connection carried data out of the job: %q", got)
	}
	for _, want := range []string{"proxy-env: 200 upstream-ok", "transparent: 200 upstream-ok", "transparent-name: 200 upstream-ok", "other-header: 403", "udp: blocked",
		"packet: blocked", "status: CapEff: 0000000000000000", "status: CapBnd: 0000000000000000", "status: NoNewPrivs: 1", "inbound: listening"} {
		if !strings.Contains(string(out), want) {
			t.Errorf("job output lacks %q", want)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 3 || seen[0] != "Bearer "+secret || seen[1] != "Bearer "+secret || seen[2] != "Bearer "+secret {
		t.Errorf("upstream saw %q", seen)
	}
	if exec.Command("docker", "inspect", EgressHelperName(jobID)).Run() == nil {
		t.Error("Cleanup must remove the helper container")
	}
}

func envOr(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

func e2eCert(t *testing.T, ip string) ([]byte, tls.Certificate) {
	caKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	caT := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "e2e CA"}, NotBefore: time.Now().Add(-time.Hour),
		NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	caDER, err := x509.CreateCertificate(rand.Reader, caT, caT, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	ca, _ := x509.ParseCertificate(caDER)
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	lt := &x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: ip}, NotBefore: time.Now().Add(-time.Hour),
		NotAfter: time.Now().Add(time.Hour), IPAddresses: []net.IP{net.ParseIP(ip)}, DNSNames: []string{"upstream.test"}, KeyUsage: x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, lt, ca, &key.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}), tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

// buildE2EClient builds a static client: it calls the upstream through
// HTTPS_PROXY, then directly (the transparent route), then with the
// placeholder in another header, and tries UDP out.
func buildE2EClient(t *testing.T) string {
	dir := t.TempDir()
	src := `package main

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"syscall"
	"time"
)

func get(c *http.Client, header, value string) string {
	return getURL(c, os.Getenv("UPSTREAM"), header, value)
}

func getURL(c *http.Client, url, header, value string) string {
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set(header, value)
	res, err := c.Do(req)
	if err != nil {
		return "error " + err.Error()
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return fmt.Sprintf("%d %s", res.StatusCode, b)
}

func main() {
	tok := os.Getenv("TOKEN")
	viaEnv := &http.Client{Timeout: 20 * time.Second}
	fmt.Println("proxy-env:", get(viaEnv, "Authorization", tok))
	direct := &http.Client{Timeout: 20 * time.Second, Transport: &http.Transport{Proxy: nil}}
	fmt.Println("transparent:", get(direct, "Authorization", tok))
	// By name: the fake DNS gives an address in 198.18.0.0/15, and the
	// transparent route sends CONNECT upstream.test:port.
	fmt.Println("transparent-name:", getURL(direct, os.Getenv("UPSTREAM_NAME"), "Authorization", tok))
	fmt.Println("other-header:", get(viaEnv, "X-Token", tok))
	c, err := net.DialTimeout("udp", "1.1.1.1:443", 2*time.Second)
	if err == nil {
		c.SetDeadline(time.Now().Add(2 * time.Second))
		_, err = c.Write([]byte("x"))
	}
	if err != nil {
		fmt.Println("udp: blocked", err)
	} else {
		fmt.Println("udp: sent (not blocked)")
	}
	// No capability, no new privileges: AF_PACKET (raw frames skip
	// nftables) needs CAP_NET_RAW.
	st, _ := os.ReadFile("/proc/self/status")
	for _, l := range strings.Split(string(st), "\n") {
		if strings.HasPrefix(l, "CapBnd:") || strings.HasPrefix(l, "CapEff:") || strings.HasPrefix(l, "NoNewPrivs:") {
			fmt.Println("status:", strings.Join(strings.Fields(l), " "))
		}
	}
	if fd, err := syscall.Socket(syscall.AF_PACKET, syscall.SOCK_RAW, 0x0300); err != nil {
		fmt.Println("packet: blocked", err)
	} else {
		syscall.Close(fd)
		fmt.Println("packet: opened (not blocked)")
	}
	// An inbound connection must not carry data out: serve data for a
	// while; the test dials in from outside.
	if l, err := net.Listen("tcp", "0.0.0.0:9100"); err == nil {
		fmt.Println("inbound: listening")
		go func() {
			for {
				c, err := l.Accept()
				if err != nil {
					return
				}
				c.Write([]byte("exfil-via-inbound-connection"))
				c.Close()
			}
		}()
		time.Sleep(8 * time.Second)
	}
}
`
	os.WriteFile(filepath.Join(dir, "main.go"), []byte(src), 0o644)
	os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module e2eclient\n\ngo 1.22\n"), 0o644)
	out := filepath.Join(dir, "e2e-client")
	cmd := exec.Command("go", "build", "-o", out, ".")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOFLAGS=-mod=mod")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build client: %v %s", err, b)
	}
	os.Chmod(out, 0o755)
	return out
}
