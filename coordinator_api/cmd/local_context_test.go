package cmd

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/urfave/cli/v2"
)

func TestLocalContextRoundTripUsesProtectedFile(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("REACTORCIDE_API_TOKEN", "raw-api-token")
	t.Setenv("TEST_REMOTE_SECRET_VALUE", "raw-secret-value")
	want := localContext{Name: "dev", ProjectID: "project-1", CoordinatorURL: "https://coordinator.example", DefaultRunnerImage: "runner:dev", SyncedAt: time.Now().UTC().Truncate(time.Second)}
	if err := writeLocalContext(want); err != nil {
		t.Fatal(err)
	}
	want.DefaultRunnerImage = "runner:updated"
	if err := writeLocalContext(want); err != nil {
		t.Fatal(err)
	}
	got, err := loadLocalContext("dev")
	if err != nil {
		t.Fatal(err)
	}
	if got.ProjectID != want.ProjectID || got.CoordinatorURL != want.CoordinatorURL || got.DefaultRunnerImage != "runner:updated" {
		t.Fatalf("context = %#v", got)
	}
	path, err := localContextPath("dev")
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o, want 600", info.Mode().Perm())
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) == "" {
		t.Fatal("context file is empty")
	}
	if strings.Contains(string(data), "raw-api-token") || strings.Contains(string(data), "raw-secret-value") {
		t.Fatalf("context file contains a credential: %s", data)
	}
}

func TestWorkflowSecretReferencesUseOnlySelectedWorkflowJobs(t *testing.T) {
	root := t.TempDir()
	workflows := filepath.Join(root, ".reactorcide", "workflows")
	jobs := filepath.Join(root, ".reactorcide", "jobs")
	if err := os.MkdirAll(workflows, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(jobs, 0o700); err != nil {
		t.Fatal(err)
	}
	workflow := filepath.Join(workflows, "selected.yaml")
	if err := os.WriteFile(workflow, []byte("jobs:\n  build:\n    job_file: build.yaml\n  inline:\n    environment:\n      TOKEN: ${secret:selected/inline:token}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(jobs, "build.yaml"), []byte("environment:\n  PASSWORD: ${secret:selected/build:password}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(jobs, "unrelated.yaml"), []byte("environment:\n  TOKEN: ${secret:unrelated:path}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	refs, err := workflowSecretReferences(workflow)
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 2 {
		t.Fatalf("refs = %#v", refs)
	}
	if refs[0].Path != "selected/build" || refs[1].Path != "selected/inline" {
		t.Fatalf("refs = %#v", refs)
	}
	selections, err := workflowSecretSelections(workflow)
	if err != nil {
		t.Fatal(err)
	}
	if len(selections) != 2 || selections[0].JobName != "build" || selections[1].JobName != "inline" {
		t.Fatalf("selections = %#v", selections)
	}
}

func TestWorkflowSecretSelectionsIncludeTopLevelVariables(t *testing.T) {
	root := t.TempDir()
	workflows := filepath.Join(root, ".reactorcide", "workflows")
	if err := os.MkdirAll(workflows, 0o700); err != nil {
		t.Fatal(err)
	}
	workflow := filepath.Join(workflows, "selected.yaml")
	content := "vars:\n  TOKEN: ${secret:selected/workflow:token}\njobs:\n  build:\n    command: make build\n"
	if err := os.WriteFile(workflow, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	selections, err := workflowSecretSelections(workflow)
	if err != nil {
		t.Fatal(err)
	}
	if len(selections) != 1 || selections[0].JobName != "build" || selections[0].Path != "selected/workflow" {
		t.Fatalf("selections = %#v", selections)
	}
}

func TestLocalContextRejectsUnsafeName(t *testing.T) {
	if _, err := localContextPath("../outside"); err == nil {
		t.Fatal("expected unsafe name error")
	}
}

func TestLocalContextCommandsAcceptNameFlag(t *testing.T) {
	set := flag.NewFlagSet("test", flag.ContinueOnError)
	set.String("name", "", "")
	if err := set.Set("name", "dev"); err != nil {
		t.Fatal(err)
	}
	name, err := localContextCommandName(cli.NewContext(nil, set, nil), "show")
	if err != nil || name != "dev" {
		t.Fatalf("name = %q, error = %v", name, err)
	}
}

func TestLocalContextSyncPreservesOverrides(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	timeout := 15
	want := localContext{Name: "dev", ProjectID: "old", Overrides: localContextOverrides{RunnerImage: "local", TimeoutSeconds: &timeout}}
	if err := writeLocalContext(want); err != nil {
		t.Fatal(err)
	}
	loaded, err := loadLocalContext("dev")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Overrides.RunnerImage != "local" || loaded.Overrides.TimeoutSeconds == nil || *loaded.Overrides.TimeoutSeconds != 15 {
		t.Fatalf("overrides = %#v", loaded.Overrides)
	}
}
