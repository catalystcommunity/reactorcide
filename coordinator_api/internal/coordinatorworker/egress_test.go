package coordinatorworker

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/egress"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/secrets"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/workerclient/csilapi"
)

const testEgressCA = "-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----\n"

func egressLease(placeholder string) csilapi.Lease {
	return csilapi.Lease{
		LeaseId: "lease-1", JobId: "job_a", TimeoutSeconds: 600,
		Egress: &csilapi.EgressLease{
			Identity:     csilapi.EgressIdentity{OrgId: "org-1", ProjectId: "p-1", Node: "deploy", ActorIdentity: "github:octo", RefClasses: []string{}},
			Destinations: []csilapi.EgressDestination{{Host: "coordinator", Ports: []int64{6080}}},
			Grants: []csilapi.EgressGrant{{Id: "g1", SecretRef: "org/gh:token", Placeholder: placeholder, Value: "ghp-real-secret-value",
				Hosts: []string{"api.github.com"}, Header: "authorization", Format: "Bearer {}"}},
		},
	}
}

func TestEgressPolicyFromLease(t *testing.T) {
	ph, _ := egress.NewPlaceholder()
	now := time.Unix(1000, 0)
	p := egressPolicy(egressLease(ph), now)
	if p.JobID != "job_a" || p.Identity.Node != "deploy" || p.Identity.ActorIdentity != "github:octo" || p.Identity.OrgID != "org-1" {
		t.Fatalf("identity %+v", p.Identity)
	}
	if len(p.Destinations) != 1 || p.Destinations[0].Ports[0] != 6080 {
		t.Fatalf("destinations %+v", p.Destinations)
	}
	g := p.Grants[0]
	if g.Placeholder != ph || g.Value != "ghp-real-secret-value" || g.Format != "Bearer {}" || g.Hosts[0] != "api.github.com" {
		t.Fatalf("grant %+v", g)
	}
	if p.ExpiresAtMs != now.Add(600*time.Second+time.Hour).UnixMilli() {
		t.Fatalf("expiry %d", p.ExpiresAtMs)
	}
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
}

type fakeEgressProxy struct {
	mu         sync.Mutex
	registered map[string]egress.JobPolicy
	deleted    []string
}

func (f *fakeEgressProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer "+strings.Repeat("t", 32) {
		w.WriteHeader(401)
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	id := strings.TrimPrefix(r.URL.Path, "/v1/jobs/")
	switch r.Method {
	case http.MethodPut:
		var p egress.JobPolicy
		json.NewDecoder(r.Body).Decode(&p)
		f.registered[id] = p
		w.Write([]byte(`{"job_id":"` + id + `","grants":1}`))
	case http.MethodDelete:
		f.deleted = append(f.deleted, id)
		w.Write([]byte(`{"job_id":"` + id + `","flows":2,"blocked":1,"secrets_used":["g1"]}`))
	}
}

func TestStartLeaseEgressRegistersAndReleases(t *testing.T) {
	fake := &fakeEgressProxy{registered: map[string]egress.JobPolicy{}}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	s := &EgressSettings{
		ControlURL: srv.URL, ProxyAddr: "reactorcide-egress:3128", ControlToken: strings.Repeat("t", 32),
		JobKey: []byte("0123456789abcdef0123456789abcdef"), CACertPEM: testEgressCA, NetImage: "example/egress:1",
		lookup: func(context.Context, string) ([]string, error) { return []string{"fd00::1", "10.43.7.9"}, nil },
	}
	ph, _ := egress.NewPlaceholder()
	masker := secrets.NewMasker()
	cfg, release, err := startLeaseEgress(context.Background(), s, egressLease(ph), masker)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ProxyIP != "10.43.7.9" || cfg.ProxyPort != 3128 || cfg.Username != "job_a" ||
		cfg.Password != "2c896a41f17c5e4c9702168deb85d829ad3d5a1a7d00ae400638f3d0330548e0" || cfg.NetImage != "example/egress:1" {
		t.Fatalf("egress config %+v", cfg)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	if got := fake.registered["job_a"]; len(got.Grants) != 1 || got.Grants[0].Value != "ghp-real-secret-value" {
		t.Fatalf("the proxy must get the real value: %+v", got)
	}
	if out := masker.MaskString("x ghp-real-secret-value " + cfg.Password); strings.Contains(out, "ghp-real") || strings.Contains(out, cfg.Password) {
		t.Fatalf("the grant value and the credential must be masked: %q", out)
	}
	release()
	if len(fake.deleted) != 1 || fake.deleted[0] != "job_a" {
		t.Fatalf("release must remove the job: %v", fake.deleted)
	}

	if _, _, err := startLeaseEgress(context.Background(), nil, egressLease(ph), masker); err == nil {
		t.Fatal("a lease with egress on a worker without settings must fail")
	}
	s.ControlToken = strings.Repeat("x", 32)
	if _, _, err := startLeaseEgress(context.Background(), s, egressLease(ph), masker); err == nil {
		t.Fatal("a refused registration must fail the lease")
	}
}

func TestLoadEgressSettings(t *testing.T) {
	t.Setenv(EnvEgressControlURL, "")
	if s, err := LoadEgressSettings(); s != nil || err != nil {
		t.Fatal("no control URL: no egress proxy")
	}
	dir := t.TempDir()
	write := func(name, v string) string {
		p := filepath.Join(dir, name)
		os.WriteFile(p, []byte(v), 0o600)
		return p
	}
	t.Setenv(EnvEgressControlURL, "http://reactorcide-egress:8080")
	t.Setenv(EnvEgressProxyAddr, "reactorcide-egress:3128")
	t.Setenv(EnvEgressControlToken, write("token", strings.Repeat("t", 40)+"\n"))
	t.Setenv(EnvEgressJobKey, write("key", strings.Repeat("k", 64)))
	t.Setenv(EnvEgressCAFile, write("ca.crt", testEgressCA))
	t.Setenv(EnvEgressNetImage, "")
	s, err := LoadEgressSettings()
	if err != nil {
		t.Fatal(err)
	}
	if s.ControlToken != strings.Repeat("t", 40) || len(s.JobKey) != 64 || s.NetImage == "" {
		t.Fatalf("settings %+v", s)
	}
	cfg, err := applyEgressCharacteristic(Config{Egress: s})
	if err != nil || len(cfg.Custom) != 1 || cfg.Custom[0].Key != egress.WorkerCharacteristic || cfg.Custom[0].Value != true {
		t.Fatalf("characteristic: %+v %v", cfg.Custom, err)
	}
	t.Setenv(EnvEgressProxyAddr, "")
	if _, err := LoadEgressSettings(); err == nil {
		t.Fatal("the proxy address is required")
	}
	t.Setenv(EnvEgressProxyAddr, "reactorcide-egress:3128")
	t.Setenv(EnvEgressJobKey, write("short", "short"))
	if _, err := LoadEgressSettings(); err == nil {
		t.Fatal("a short job key must be refused")
	}
}
