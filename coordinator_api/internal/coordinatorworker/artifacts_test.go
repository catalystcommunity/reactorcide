package coordinatorworker

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/secrets"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/testresults"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/worker"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/workerclient"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/workerclient/csilapi"
)

// uploadingClient is a fakeClient that records artifact chunks.
type uploadingClient struct {
	*fakeClient
	mu     sync.Mutex
	chunks []csilapi.UploadArtifactRequest
	refuse map[string]string
}

func (u *uploadingClient) UploadArtifact(_ context.Context, req csilapi.UploadArtifactRequest) (csilapi.UploadArtifactResponse, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if code := u.refuse[req.Path]; code != "" {
		return csilapi.UploadArtifactResponse{}, &workerclient.ServiceCallError{Code: code, Message: "refused"}
	}
	u.chunks = append(u.chunks, req)
	return csilapi.UploadArtifactResponse{Ok: true}, nil
}

func (u *uploadingClient) files() map[string][]byte {
	out := map[string][]byte{}
	for _, c := range u.chunks {
		out[c.Path] = append(out[c.Path], c.Data...)
	}
	return out
}

func TestPrepareLeaseArtifactsWritesDirAndQuarantineFile(t *testing.T) {
	workspace := t.TempDir()
	lease := csilapi.Lease{LeaseId: "l1", JobId: "j1", Artifacts: &csilapi.ArtifactLease{MaxFileBytes: 100, MaxJobBytes: 100, MaxFiles: 10, ChunkBytes: 4},
		QuarantinedTests: []csilapi.QuarantinedTest{{TestId: "abc", Suite: "unit", Classname: "pkg", Name: "flaky", ExpiresAt: "2026-12-01T00:00:00Z"}}}
	config := &worker.JobConfig{Env: map[string]string{}}
	if err := prepareLeaseArtifacts(lease, &fakeRunner{}, workspace, config, os.Getuid(), os.Getgid()); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(filepath.Join(workspace, "artifacts")); err != nil || !info.IsDir() {
		t.Fatalf("artifacts dir: %v", err)
	}
	if config.Env[ArtifactsDirEnv] != "/job/artifacts" || config.Env[QuarantinedTestsFileEnv] != "/job/quarantined-tests.json" {
		t.Fatalf("env %v", config.Env)
	}
	data, err := os.ReadFile(filepath.Join(workspace, "quarantined-tests.json"))
	if err != nil {
		t.Fatal(err)
	}
	var file testresults.QuarantineFile
	if err := json.Unmarshal(data, &file); err != nil || file.Version != 1 || len(file.Tests) != 1 || file.Tests[0].Name != "flaky" {
		t.Fatalf("file %s %v", data, err)
	}
	if !bytes.Equal(config.JobFiles["quarantined-tests.json"], data) {
		t.Fatal("the job files carry the same content for Kubernetes")
	}

	// An older coordinator: nothing.
	old := &worker.JobConfig{Env: map[string]string{}}
	if err := prepareLeaseArtifacts(csilapi.Lease{}, &fakeRunner{}, t.TempDir(), old, 0, 0); err != nil || len(old.Env) != 0 {
		t.Fatalf("old lease: %v %v", err, old.Env)
	}
}

func TestUploadLeaseArtifactsSkipsLinksAndMasksReports(t *testing.T) {
	workspace := t.TempDir()
	dir := filepath.Join(workspace, "artifacts")
	if err := os.MkdirAll(filepath.Join(dir, "reports"), 0o755); err != nil {
		t.Fatal(err)
	}
	secretFile := filepath.Join(workspace, "host-secret")
	if err := os.WriteFile(secretFile, []byte("host data"), 0o600); err != nil {
		t.Fatal(err)
	}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.WriteFile(filepath.Join(dir, "app.bin"), []byte("0123456789"), 0o644))
	must(os.WriteFile(filepath.Join(dir, "reports", "junit-unit.xml"), []byte(`<testsuite><testcase name="t"><failure message="token hunter2"/></testcase></testsuite>`), 0o644))
	must(os.WriteFile(filepath.Join(dir, "declared.xml"), []byte("<testsuite>hunter2</testsuite>"), 0o644))
	must(os.WriteFile(filepath.Join(dir, "too-big.bin"), bytes.Repeat([]byte("x"), 200), 0o644))
	must(os.Symlink(secretFile, filepath.Join(dir, "link.txt")))
	must(os.Symlink(workspace, filepath.Join(dir, "linkdir")))
	must(os.WriteFile(filepath.Join(dir, "empty.txt"), nil, 0o644))

	client := &uploadingClient{fakeClient: &fakeClient{}}
	masker := secrets.NewMasker()
	masker.RegisterSecret("hunter2")
	lease := csilapi.Lease{LeaseId: "l1", JobId: "j1", Artifacts: &csilapi.ArtifactLease{MaxFileBytes: 100, MaxJobBytes: 1000, MaxFiles: 10, ChunkBytes: 4}}
	uploadLeaseArtifacts(client, &fakeRunner{}, "r1", lease, workspace, `{"test_reports":["declared.xml"]}`, masker)

	files := client.files()
	if string(files["app.bin"]) != "0123456789" {
		t.Fatalf("app.bin %q", files["app.bin"])
	}
	for _, name := range []string{"link.txt", "linkdir/host-secret", "too-big.bin"} {
		if _, ok := files[name]; ok {
			t.Fatalf("%s must not be uploaded", name)
		}
	}
	for _, name := range []string{"reports/junit-unit.xml", "declared.xml"} {
		if strings.Contains(string(files[name]), "hunter2") || !strings.Contains(string(files[name]), "testsuite") {
			t.Fatalf("%s must be uploaded masked: %q", name, files[name])
		}
	}
	if _, ok := files["empty.txt"]; !ok {
		t.Fatal("an empty file is one final chunk")
	}
	finals, kinds := map[string]int{}, map[string]string{}
	var offsets []int64
	for _, c := range client.chunks {
		if c.Final {
			finals[c.Path]++
		}
		kinds[c.Path] = *c.Kind
		if c.Path == "app.bin" {
			offsets = append(offsets, c.Offset)
		}
		if len(c.Data) > 4 {
			t.Fatalf("chunk over ChunkBytes: %d", len(c.Data))
		}
	}
	if finals["app.bin"] != 1 || kinds["declared.xml"] != "test_report" || kinds["app.bin"] != "file" {
		t.Fatalf("finals %v kinds %v", finals, kinds)
	}
	if len(offsets) != 3 || offsets[1] != 4 || offsets[2] != 8 {
		t.Fatalf("offsets %v", offsets)
	}
}

func TestUploadLeaseArtifactsRefusesLinkedArtifactsDir(t *testing.T) {
	workspace := t.TempDir()
	target := t.TempDir()
	if err := os.WriteFile(filepath.Join(target, "x.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(workspace, "artifacts")); err != nil {
		t.Fatal(err)
	}
	client := &uploadingClient{fakeClient: &fakeClient{}}
	lease := csilapi.Lease{LeaseId: "l1", Artifacts: &csilapi.ArtifactLease{MaxFileBytes: 10, MaxJobBytes: 10, MaxFiles: 1, ChunkBytes: 4}}
	uploadLeaseArtifacts(client, &fakeRunner{}, "r", lease, workspace, "", secrets.NewMasker())
	if len(client.chunks) != 0 {
		t.Fatal("a linked artifacts directory must not be read")
	}
}

func TestUploadStopsOnFinalAnswer(t *testing.T) {
	workspace := t.TempDir()
	dir := filepath.Join(workspace, "artifacts")
	_ = os.MkdirAll(dir, 0o755)
	_ = os.WriteFile(filepath.Join(dir, "a.bin"), []byte("abcdefgh"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "b.bin"), []byte("b"), 0o644)
	client := &uploadingClient{fakeClient: &fakeClient{}, refuse: map[string]string{"a.bin": "invalid_argument"}}
	lease := csilapi.Lease{LeaseId: "l1", Artifacts: &csilapi.ArtifactLease{MaxFileBytes: 10, MaxJobBytes: 100, MaxFiles: 10, ChunkBytes: 4}}
	uploadLeaseArtifacts(client, &fakeRunner{}, "r", lease, workspace, "", secrets.NewMasker())
	files := client.files()
	if _, ok := files["a.bin"]; ok || string(files["b.bin"]) != "b" {
		t.Fatalf("a refused file stops, the next file uploads: %v", files)
	}
}
