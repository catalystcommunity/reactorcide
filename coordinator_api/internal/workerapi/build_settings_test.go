package workerapi

import (
	"context"
	"strings"
	"testing"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/cachens"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/workerapi/csilapi"
)

// buildSettingsStore adds profiles, environment defaults, an image policy,
// and job links to the in-memory store.
type buildSettingsStore struct {
	*fakeStore
	profiles map[string]*models.ExecutionProfile
	org      map[string]string
	class    map[string]string
	project  map[string]string
	policy   *models.ImagePolicy
	links    map[string][]models.JobLink
}

func newBuildSettingsStore(base *fakeStore) *buildSettingsStore {
	return &buildSettingsStore{fakeStore: base, profiles: map[string]*models.ExecutionProfile{}, links: map[string][]models.JobLink{}}
}

func (s *buildSettingsStore) GetExecutionProfile(_ context.Context, _ string, name string) (*models.ExecutionProfile, error) {
	if p, ok := s.profiles[name]; ok {
		return p, nil
	}
	return nil, store.ErrNotFound
}
func (s *buildSettingsStore) ListEnvDefaults(context.Context, string, string, string) ([]models.EnvDefault, error) {
	return nil, nil
}
func (s *buildSettingsStore) SetEnvDefault(context.Context, string, string, string, string, string) (*models.EnvDefault, error) {
	return nil, nil
}
func (s *buildSettingsStore) DeleteEnvDefault(context.Context, string, string, string, string) error {
	return nil
}
func (s *buildSettingsStore) EnvDefaultLayers(context.Context, string, string, *string) (map[string]string, map[string]string, map[string]string, error) {
	return s.org, s.class, s.project, nil
}
func (s *buildSettingsStore) GetImagePolicy(context.Context, string) (*models.ImagePolicy, error) {
	if s.policy == nil {
		return &models.ImagePolicy{}, nil
	}
	return s.policy, nil
}
func (s *buildSettingsStore) SetImagePolicy(context.Context, *models.ImagePolicy) error { return nil }
func (s *buildSettingsStore) ReplaceJobLinks(_ context.Context, jobID string, links []models.JobLink) error {
	s.links[jobID] = links
	return nil
}
func (s *buildSettingsStore) ListJobLinks(_ context.Context, jobID string) ([]models.JobLink, error) {
	return s.links[jobID], nil
}
func (s *buildSettingsStore) ListJobLinksForJobs(context.Context, []string) (map[string][]models.JobLink, error) {
	return s.links, nil
}

func leaseEnvMap(lease *csilapi.Lease) map[string]string {
	env := map[string]string{}
	for _, kv := range lease.Env {
		env[kv.Key] = kv.Value
	}
	return env
}

func requestLease(t *testing.T, h *testHarness, token string) csilapi.RequestJobResponse {
	t.Helper()
	resp, err := h.service.RequestJob(ctxWithAuth(token), csilapi.RequestJobRequest{
		WorkerCharacteristics: csilapi.WorkerCharacteristics{Os: "linux", Arch: "amd64"},
	})
	if err != nil {
		t.Fatalf("RequestJob: %v", err)
	}
	return resp
}

// The default profile keeps the shared cache volume with writes, as before
// namespaces, also with a store that cannot read profiles.
func TestRequestJob_DefaultProfileCacheLease(t *testing.T) {
	h := newTestHarness()
	_, token := seedQueueAndJobWithSecretRef(t, h, true)
	resp := requestLease(t, h, token)
	if !resp.HasLease || resp.Lease.Cache == nil {
		t.Fatalf("expected a lease with a cache decision: %+v", resp)
	}
	c := resp.Lease.Cache
	if c.Namespace != "standard" || c.VolumeKey != "" || !c.Writable || c.ArtifactNamespace != "standard" {
		t.Fatalf("cache %+v", *c)
	}
	env := leaseEnvMap(resp.Lease)
	if env[cachens.EnvNamespace] != "standard" || env[cachens.EnvWritable] != "true" || env[cachens.EnvArtifactNamespace] != "standard" {
		t.Fatalf("env %v", env)
	}
}

func TestRequestJob_UntrustedProfileAndEnvDefaults(t *testing.T) {
	h := newTestHarness()
	settings := newBuildSettingsStore(h.store)
	settings.profiles["pr-untrusted"] = &models.ExecutionProfile{Name: "pr-untrusted", TrustedCacheWrites: false}
	settings.org = map[string]string{"A": "org", "B": "org", "C": "org", "REACTORCIDE_SHA": "forged"}
	settings.class = map[string]string{"B": "class", "C": "class"}
	settings.project = map[string]string{"C": "project", "PLAIN": "project"}
	h.deps.Store = settings

	job, token := seedQueueAndJobWithSecretRef(t, h, true)
	job.ExecutionProfile = "pr-untrusted"
	job.RefClasses = []string{"pr", "fork"}
	job.JobEnvVars[cachens.EnvWritable] = "true"
	job.JobEnvVars[cachens.EnvNamespace] = "standard"
	resp := requestLease(t, h, token)
	if !resp.HasLease {
		t.Fatal("expected a lease")
	}
	c := resp.Lease.Cache
	if c.Namespace != "pr-untrusted" || c.Writable || c.VolumeKey == "" {
		t.Fatalf("cache %+v", *c)
	}
	env := leaseEnvMap(resp.Lease)
	if env[cachens.EnvWritable] != "false" || env[cachens.EnvNamespace] != "pr-untrusted" {
		t.Fatalf("the job must not choose its cache: %v", env)
	}
	if env["A"] != "org" || env["B"] != "class" || env["C"] != "project" || env["PLAIN"] != "not-a-secret" {
		t.Fatalf("precedence job > project > worker class > org: %v", env)
	}
	if _, ok := env["REACTORCIDE_SHA"]; ok {
		t.Fatalf("a reserved default must not reach the job: %v", env)
	}
}

func TestRequestJob_ImagePolicyRefusesClaim(t *testing.T) {
	h := newTestHarness()
	settings := newBuildSettingsStore(h.store)
	settings.policy = &models.ImagePolicy{Enabled: true, AllowedPrefixes: []string{"ghcr.io/acme/"}}
	h.deps.Store = settings
	job, token := seedQueueAndJobWithSecretRef(t, h, true)
	image := "docker.io/evil/tool:1"
	job.ContainerImage = &image
	if resp := requestLease(t, h, token); resp.HasLease {
		t.Fatal("the image policy must refuse the claim")
	}
	saved, _ := h.store.GetJobByID(context.Background(), job.JobID)
	if saved.Status != "failed" || !strings.Contains(saved.LastError, "image policy denied") || !strings.Contains(saved.LastError, "allowed registry prefix") {
		t.Fatalf("job %s: %q", saved.Status, saved.LastError)
	}
}

func TestRecordJobLinks(t *testing.T) {
	settings := newBuildSettingsStore(newFakeStore())
	d := &Deps{Store: settings}
	output := `{"links": [{"name": "Results", "url": "https://builds.example/1"}, {"name": "Bad", "url": "ftp://x"}]}`
	d.recordJobLinks(context.Background(), &models.Job{JobID: "j1"}, &output)
	if links := settings.links["j1"]; len(links) != 1 || links[0].URL != "https://builds.example/1" {
		t.Fatalf("links %+v", links)
	}
	empty := `{"vars": {}}`
	d.recordJobLinks(context.Background(), &models.Job{JobID: "j2"}, &empty)
	if _, ok := settings.links["j2"]; ok {
		t.Fatal("a document with no links must not write")
	}
}
