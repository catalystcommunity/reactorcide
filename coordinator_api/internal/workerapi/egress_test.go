package workerapi

import (
	"context"
	"strings"
	"testing"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/corndogs"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/egress"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/refclass"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/worker"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/workerapi/csilapi"
)

var egressWorker = []csilapi.CustomCharacteristic{{Key: egress.WorkerCharacteristic, Value: true}}

// seedProxyGrantJob seeds a job whose API_KEY comes from a proxy-delivered
// grant and whose OTHER_KEY comes from an env grant.
func seedProxyGrantJob(t *testing.T, h *testHarness, workerCustom []csilapi.CustomCharacteristic) (*models.Job, string) {
	t.Helper()
	queueUUID := "55555555-5555-5555-5555-555555555555"
	h.store.seedQueue(models.Queue{QueueUUID: queueUUID, Characteristics: mustCharacteristics(t, map[string]any{"os": "linux"})})
	job := &models.Job{
		UserID:     "proxy-user",
		Name:       "deploy",
		JobCommand: "echo hi",
		Status:     "submitted",
		JobEnvVars: models.JSONB{
			"API_KEY":   "${secret:myorg/github:token}",
			"AUTH_LINE": "Bearer ${secret:myorg/github:token} and ${secret:myorg/plain:other}",
			"OTHER_KEY": "${secret:myorg/plain:other}",
		},
	}
	h.store.seedJob(job)
	h.secretsProvider.set("myorg/github", "token", "ghp-real-secret-value")
	h.secretsProvider.set("myorg/plain", "other", "plain-env-secret")
	h.store.seedSecretGrant(models.SecretGrant{
		Name: "github-proxy", UserID: job.UserID,
		SecretPathMatch: models.SecretGrantMatchPrefix, SecretPathPattern: "myorg/github", JobNameMatch: models.SecretGrantMatchAny,
		Delivery: models.SecretDeliveryProxy, ProxyHosts: []string{"api.github.com"}, ProxyHeaderFormat: "Bearer {}",
	})
	h.store.seedSecretGrant(models.SecretGrant{
		Name: "plain-env", UserID: job.UserID,
		SecretPathMatch: models.SecretGrantMatchPrefix, SecretPathPattern: "myorg/plain", JobNameMatch: models.SecretGrantMatchAny,
	})
	if _, err := h.corndogs.SubmitTaskToQueue(context.Background(), queueUUID, &corndogs.TaskPayload{JobID: job.JobID}, 0); err != nil {
		t.Fatal(err)
	}
	token, _ := h.registerWorker(t, "egress-worker", "linux", "amd64", workerCustom)
	return job, token
}

func TestRequestJob_ProxyGrantGivesPlaceholderAndEgressGrant(t *testing.T) {
	t.Setenv(JobAPIURLEnv, "https://ci.example.com/")
	h := newTestHarness()
	_, token := seedProxyGrantJob(t, h, egressWorker)
	resp, err := h.service.RequestJob(ctxWithAuth(token), csilapi.RequestJobRequest{
		WorkerCharacteristics: csilapi.WorkerCharacteristics{Os: "linux", Arch: "amd64", Custom: egressWorker},
	})
	if err != nil || !resp.HasLease {
		t.Fatalf("expected a lease: %v %+v", err, resp)
	}
	lease := resp.Lease
	secrets := map[string]string{}
	for _, e := range lease.Secrets {
		secrets[e.Key] = e.Value
	}
	for _, e := range append(append([]csilapi.EnvVar{}, lease.Env...), lease.Secrets...) {
		if strings.Contains(e.Value, "ghp-real-secret-value") {
			t.Fatalf("the proxy-delivered value is in the job environment (%s)", e.Key)
		}
	}
	ph := secrets["API_KEY"]
	if !egress.IsPlaceholder(ph) {
		t.Fatalf("API_KEY = %q, want a placeholder", ph)
	}
	if secrets["AUTH_LINE"] != "Bearer "+ph+" and plain-env-secret" {
		t.Fatalf("AUTH_LINE = %q", secrets["AUTH_LINE"])
	}
	if secrets["OTHER_KEY"] != "plain-env-secret" {
		t.Fatalf("an env grant must keep env delivery, got %q", secrets["OTHER_KEY"])
	}
	if lease.Egress == nil || len(lease.Egress.Grants) != 1 {
		t.Fatalf("expected one egress grant, got %+v", lease.Egress)
	}
	g := lease.Egress.Grants[0]
	if g.Placeholder != ph || g.Value != "ghp-real-secret-value" || g.Hosts[0] != "api.github.com" ||
		g.Header != "authorization" || g.Format != "Bearer {}" || g.SecretRef != "myorg/github:token" {
		t.Fatalf("egress grant %+v", g)
	}
	if lease.Egress.Identity.OrgId == "" {
		t.Fatalf("identity must carry the org: %+v", lease.Egress.Identity)
	}
	// The job reaches the coordinator (triggers, OIDC tokens) at
	// REACTORCIDE_COORDINATOR_URL; the coordinator permits that host.
	var coordURL string
	for _, e := range append(append([]csilapi.EnvVar{}, lease.Env...), lease.Secrets...) {
		if e.Key == "REACTORCIDE_COORDINATOR_URL" {
			coordURL = e.Value
		}
	}
	if coordURL != "https://ci.example.com/" || len(lease.Egress.Destinations) != 1 ||
		lease.Egress.Destinations[0].Host != "ci.example.com" || lease.Egress.Destinations[0].Ports[0] != 443 {
		t.Fatalf("the coordinator URL of the job (%q) must be a destination: %+v", coordURL, lease.Egress.Destinations)
	}

	// The coordinator masking backstop knows the real values, not the
	// placeholder-bearing strings.
	cached := strings.Join(h.service.secrets.get(lease.LeaseId), "\n")
	if !strings.Contains(cached, "ghp-real-secret-value") || !strings.Contains(cached, "plain-env-secret") {
		t.Fatal("the masking cache must hold each real value")
	}
	if strings.Contains(cached, ph) {
		t.Fatal("the masking cache must not hold placeholders")
	}
}

func TestRequestJob_ProxyGrantRefusedOnWorkerWithoutEgress(t *testing.T) {
	h := newTestHarness()
	job, token := seedProxyGrantJob(t, h, nil)
	resp, err := h.service.RequestJob(ctxWithAuth(token), csilapi.RequestJobRequest{
		WorkerCharacteristics: csilapi.WorkerCharacteristics{Os: "linux", Arch: "amd64"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.HasLease {
		t.Fatal("a job with a proxy grant must not go to a worker without an egress proxy")
	}
	saved, _ := h.store.GetJobByID(context.Background(), job.JobID)
	if saved.Status != "failed" || !strings.Contains(saved.LastError, "egress") {
		t.Fatalf("job %s: %q", saved.Status, saved.LastError)
	}
	assertNoSecretInCorndogsPayloads(t, h, "ghp-real-secret-value")
}

func TestRequestJob_NoEgressWithoutGrantOrSetting(t *testing.T) {
	h := newTestHarness()
	_, token := seedQueueAndJobWithSecretRef(t, h, true)
	resp, err := h.service.RequestJob(ctxWithAuth(token), csilapi.RequestJobRequest{
		WorkerCharacteristics: csilapi.WorkerCharacteristics{Os: "linux", Arch: "amd64"},
	})
	if err != nil || !resp.HasLease {
		t.Fatalf("expected a lease: %v", err)
	}
	if resp.Lease.Egress != nil {
		t.Fatalf("egress control must stay off by default: %+v", resp.Lease.Egress)
	}
}

type egressSettingStore struct {
	*fakeStore
	on bool
}

func (s egressSettingStore) EffectiveEgressControl(context.Context, string, *string, string) (bool, error) {
	return s.on, nil
}
func (s egressSettingStore) SetOrganizationEgressControl(context.Context, string, bool) error {
	return nil
}

// EffectiveEgressControl makes the in-memory store answer the egress
// setting (off), as the Postgres store does; buildLeaseEgress fails closed
// on a store without it.
func (s *fakeStore) EffectiveEgressControl(context.Context, string, *string, string) (bool, error) {
	return false, nil
}

// noEgressSettingStore hides EffectiveEgressControl of the fake store.
type noEgressSettingStore struct{ DataStore }

func TestBuildLeaseEgress_SettingTurnsItOn(t *testing.T) {
	t.Setenv(JobAPIURLEnv, "http://reactorcide-coordinator:6080/api")
	d := &Deps{Store: egressSettingStore{fakeStore: newFakeStore(), on: true}}
	job := &models.Job{JobID: "j1", OrgID: "o1", WorkflowNodeName: "build", ActorIdentity: "github:octo"}
	// A job variable must not choose a permitted destination.
	env := map[string]string{"REACTORCIDE_API_URL": "http://attacker.example.com:9999", "REACTORCIDE_COORDINATOR_URL": "http://attacker.example.com:9998"}
	got, err := d.buildLeaseEgress(context.Background(), job, &resolvedLeaseSecrets{}, csilapi.WorkerCharacteristics{Custom: egressWorker}, env)
	if err != nil || got == nil {
		t.Fatalf("egress = %+v, %v", got, err)
	}
	if len(got.Grants) != 0 || got.Identity.Node != "build" || got.Identity.ActorIdentity != "github:octo" {
		t.Fatalf("lease egress %+v", got)
	}
	if len(got.Destinations) != 1 || got.Destinations[0].Host != "reactorcide-coordinator" || got.Destinations[0].Ports[0] != 6080 {
		t.Fatalf("the coordinator API must be a destination: %+v", got.Destinations)
	}
	if _, err := d.buildLeaseEgress(context.Background(), job, &resolvedLeaseSecrets{}, csilapi.WorkerCharacteristics{}, env); err == nil {
		t.Fatal("egress on a worker without an egress proxy must be refused")
	}
	for _, h := range got.Destinations {
		if h.Host == "attacker.example.com" {
			t.Fatalf("a job variable added a destination: %+v", got.Destinations)
		}
	}
	root := *job
	root.RunAsUser = "root"
	if _, err := d.buildLeaseEgress(context.Background(), &root, &resolvedLeaseSecrets{}, csilapi.WorkerCharacteristics{Custom: egressWorker}, env); err == nil || !strings.Contains(err.Error(), "root") {
		t.Fatalf("run_as root with egress must refuse the claim: %v", err)
	}
	dind := *job
	dind.Capabilities = []string{worker.CapabilityDocker}
	if _, err := d.buildLeaseEgress(context.Background(), &dind, &resolvedLeaseSecrets{}, csilapi.WorkerCharacteristics{Custom: egressWorker}, env); err == nil {
		t.Fatal("the docker capability with egress must refuse the claim")
	}
	closed := &Deps{Store: noEgressSettingStore{DataStore: newFakeStore()}}
	if _, err := closed.buildLeaseEgress(context.Background(), job, &resolvedLeaseSecrets{}, csilapi.WorkerCharacteristics{Custom: egressWorker}, env); err == nil {
		t.Fatal("a store that cannot read the egress setting must refuse the claim (fail closed)")
	}
	t.Setenv(JobAPIURLEnv, "")
	if got, err := d.buildLeaseEgress(context.Background(), job, &resolvedLeaseSecrets{}, csilapi.WorkerCharacteristics{Custom: egressWorker}, env); err != nil || len(got.Destinations) != 0 {
		t.Fatalf("no job API URL: no coordinator destination, also with job variables: %+v %v", got, err)
	}
	off := &Deps{Store: egressSettingStore{fakeStore: newFakeStore(), on: false}}
	if got, err := off.buildLeaseEgress(context.Background(), job, &resolvedLeaseSecrets{}, csilapi.WorkerCharacteristics{}, env); err != nil || got != nil {
		t.Fatalf("egress off: %+v %v", got, err)
	}
}

func TestProxyDelivery_RefusesShortSecret(t *testing.T) {
	fs := newFakeStore()
	fs.seedSecretGrant(models.SecretGrant{
		Name: "short", UserID: "o1", SecretPathMatch: models.SecretGrantMatchExact, SecretPathPattern: "a/b", JobNameMatch: models.SecretGrantMatchAny,
		Delivery: models.SecretDeliveryProxy, ProxyHosts: []string{"x.example.com"},
	})
	p := newProxyDelivery(context.Background(), fs, &models.Job{OrgID: "o1", Name: "j"})
	_, err := p.deliver("a/b", "k", "1234567")
	if err == nil || !strings.Contains(err.Error(), "shorter than 8") || strings.Contains(err.Error(), "1234567") {
		t.Fatalf("a proxy grant value shorter than the scanner minimum must be refused without the value: %v", err)
	}
	if _, err := p.deliver("a/b", "k", "12345678"); err != nil {
		t.Fatalf("8 bytes is enough: %v", err)
	}
}

func TestProxyDelivery_SamePlaceholderForSameRef(t *testing.T) {
	fs := newFakeStore()
	fs.seedSecretGrant(models.SecretGrant{
		UserID: "o1", SecretPathMatch: models.SecretGrantMatchExact, SecretPathPattern: "a/b", JobNameMatch: models.SecretGrantMatchAny,
		Delivery: models.SecretDeliveryProxy, ProxyHosts: []string{"x.example.com"},
	})
	p := newProxyDelivery(context.Background(), fs, &models.Job{OrgID: "o1", Name: "j"})
	a, err := p.deliver("a/b", "k", "value-1234567")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := p.deliver("a/b", "k", "value-1234567")
	c, _ := p.deliver("a/b", "other", "value-other-99")
	if a != b || a == c || len(p.out) != 2 {
		t.Fatalf("placeholders %q %q %q (%d grants)", a, b, c, len(p.out))
	}
	if v, _ := p.deliver("x/y", "k", "env-value"); v != "env-value" {
		t.Fatal("a path without a proxy grant is env")
	}
}

func TestWorkerHasEgress(t *testing.T) {
	cases := []struct {
		v  any
		ok bool
	}{{true, true}, {"true", true}, {false, false}, {"no", false}, {[]any{false, true}, true}}
	for _, c := range cases {
		got := workerHasEgress(csilapi.WorkerCharacteristics{Custom: []csilapi.CustomCharacteristic{{Key: egress.WorkerCharacteristic, Value: c.v}}})
		if got != c.ok {
			t.Errorf("%v: %v", c.v, got)
		}
	}
	if workerHasEgress(csilapi.WorkerCharacteristics{}) {
		t.Fatal("no characteristic is no egress")
	}
}

// The proxy registration carries the ref classes of the job, so that proxy
// policy and flow records can use them.
func TestBuildLeaseEgress_RefClasses(t *testing.T) {
	t.Setenv(JobAPIURLEnv, "")
	d := &Deps{Store: egressSettingStore{fakeStore: newFakeStore(), on: true}}
	job := &models.Job{JobID: "j1", OrgID: "o1", RefClasses: []string{"protected", "default-branch"}}
	got, err := d.buildLeaseEgress(context.Background(), job, &resolvedLeaseSecrets{}, csilapi.WorkerCharacteristics{Custom: egressWorker}, nil)
	if err != nil || got == nil {
		t.Fatalf("egress = %+v, %v", got, err)
	}
	want := refclass.ForJob(job)
	if len(want) != 2 || strings.Join(got.Identity.RefClasses, ",") != strings.Join(want, ",") {
		t.Fatalf("ref classes = %v, want %v", got.Identity.RefClasses, want)
	}
	job.RefClasses = nil
	got, err = d.buildLeaseEgress(context.Background(), job, &resolvedLeaseSecrets{}, csilapi.WorkerCharacteristics{Custom: egressWorker}, nil)
	if err != nil || got.Identity.RefClasses == nil || len(got.Identity.RefClasses) != 0 {
		t.Fatalf("a job without ref classes must send an empty list: %+v, %v", got, err)
	}
}
