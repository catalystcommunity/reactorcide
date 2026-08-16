package cmd

import (
	"context"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/worker"
	"github.com/urfave/cli/v2"
)

type fakeLocalWorkflowExecutor struct {
	mu        sync.Mutex
	active    int
	maxActive int
	runs      []string
	fail      map[string]bool
	outputs   map[string]localWorkflowOutput
}

func (f *fakeLocalWorkflowExecutor) Execute(_ context.Context, node *localWorkflowNode, _ map[string]interface{}) (localWorkflowOutput, error) {
	f.mu.Lock()
	f.active++
	if f.active > f.maxActive {
		f.maxActive = f.active
	}
	f.runs = append(f.runs, node.DisplayName)
	f.mu.Unlock()
	time.Sleep(15 * time.Millisecond)
	f.mu.Lock()
	f.active--
	f.mu.Unlock()
	if f.fail[node.DisplayName] {
		return localWorkflowOutput{}, errors.New("job failed")
	}
	return f.outputs[node.DisplayName], nil
}

func TestWorkflowDefinitionDetection(t *testing.T) {
	dir := t.TempDir()
	workflow := filepath.Join(dir, "workflow.yaml")
	job := filepath.Join(dir, "job.yaml")
	if err := os.WriteFile(workflow, []byte("name: test\non: {events: [push]}\njobs: {test: {image: alpine, command: echo}}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(job, []byte("name: test\nimage: alpine\ncommand: echo\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if value, err := isWorkflowDefinitionFile(workflow); err != nil || !value {
		t.Fatalf("workflow detection = %t, %v", value, err)
	}
	if value, err := isWorkflowDefinitionFile(job); err != nil || value {
		t.Fatalf("job detection = %t, %v", value, err)
	}
}

func TestDirectJobWithoutContextKeepsResolvedBehavior(t *testing.T) {
	dir := t.TempDir()
	job := filepath.Join(dir, "job.yaml")
	if err := os.WriteFile(job, []byte("name: direct\nimage: alpine\ncommand: echo ok\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	want, _, err := worker.LoadJobSpecWithOverlays(job, nil)
	if err != nil {
		t.Fatal(err)
	}
	got, _, err := loadLocalJobSpec(job, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != want.Name || got.Image != want.Image || got.Command != want.Command {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

func TestLocalWorkflowRunsIndependentNodesInParallel(t *testing.T) {
	executor := &fakeLocalWorkflowExecutor{fail: map[string]bool{}, outputs: map[string]localWorkflowOutput{}}
	workflow := localWorkflowSpec{Name: "parallel", Jobs: []localTriggerJob{
		{JobName: "a"}, {JobName: "b"}, {JobName: "after", DependsOn: []string{"a", "b"}},
	}}
	if err := executeLocalWorkflowGraph(context.Background(), workflow, 2, executor); err != nil {
		t.Fatal(err)
	}
	if executor.maxActive != 2 {
		t.Fatalf("max active = %d, want 2", executor.maxActive)
	}
	if len(executor.runs) != 3 || executor.runs[2] != "after" {
		t.Fatalf("run order = %#v", executor.runs)
	}
}

func TestLocalWorkflowUsesProductionConditions(t *testing.T) {
	executor := &fakeLocalWorkflowExecutor{fail: map[string]bool{"build": true}, outputs: map[string]localWorkflowOutput{}}
	workflow := localWorkflowSpec{Name: "conditions", Jobs: []localTriggerJob{
		{JobName: "build"},
		{JobName: "deploy", DependsOn: []string{"build"}, Condition: "all_success"},
		{JobName: "notify", DependsOn: []string{"build"}, Condition: "any_failed"},
	}}
	if err := executeLocalWorkflowGraph(context.Background(), workflow, 2, executor); err == nil {
		t.Fatal("expected failed workflow")
	}
	if len(executor.runs) != 2 || executor.runs[0] != "build" || executor.runs[1] != "notify" {
		t.Fatalf("runs = %#v", executor.runs)
	}
}

func TestLocalWorkflowExpandsForEach(t *testing.T) {
	executor := &fakeLocalWorkflowExecutor{fail: map[string]bool{}, outputs: map[string]localWorkflowOutput{}}
	workflow := localWorkflowSpec{Name: "matrix", Jobs: []localTriggerJob{{JobName: "test", ForEach: []interface{}{"linux", "windows"}, ItemVar: "OS"}}}
	if err := executeLocalWorkflowGraph(context.Background(), workflow, 2, executor); err != nil {
		t.Fatal(err)
	}
	if len(executor.runs) != 2 {
		t.Fatalf("runs = %#v", executor.runs)
	}
	nodes := expandLocalWorkflowNodes(workflow.Jobs)
	if nodes[0].Spec.Env["OS"] != "linux" || nodes[1].Spec.Env["OS"] != "windows" {
		t.Fatalf("item vars = %#v, %#v", nodes[0].Spec.Env, nodes[1].Spec.Env)
	}
}

func TestLocalWorkflowVariableConflictFails(t *testing.T) {
	executor := &fakeLocalWorkflowExecutor{fail: map[string]bool{}, outputs: map[string]localWorkflowOutput{
		"a": {Vars: map[string]interface{}{"version": "one"}},
		"b": {Vars: map[string]interface{}{"version": "two"}},
	}}
	workflow := localWorkflowSpec{Name: "vars", Jobs: []localTriggerJob{{JobName: "a"}, {JobName: "b"}}}
	if err := executeLocalWorkflowGraph(context.Background(), workflow, 2, executor); err == nil {
		t.Fatal("expected variable conflict")
	}
}

func TestLocalWorkflowWritesWorkspaceSummary(t *testing.T) {
	secretValue := "summary-must-not-store-this-value"
	executor := &fakeLocalWorkflowExecutor{fail: map[string]bool{}, outputs: map[string]localWorkflowOutput{"build": {Vars: map[string]interface{}{"version": secretValue}}}}
	path := filepath.Join(t.TempDir(), "reactorcide-workflow-summary.json")
	workflow := localWorkflowSpec{Name: "summary", Jobs: []localTriggerJob{{JobName: "build"}}}
	if err := executeLocalWorkflowGraphWithSummary(context.Background(), workflow, 1, executor, path); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"status": "success"`) || !strings.Contains(string(data), `"version"`) {
		t.Fatalf("summary = %s", data)
	}
	if strings.Contains(string(data), secretValue) {
		t.Fatalf("summary contains a workflow value: %s", data)
	}
}

func TestPrepareSelectedCITreeExcludesOtherWorkflows(t *testing.T) {
	root := t.TempDir()
	workflowDir := filepath.Join(root, ".reactorcide", "workflows")
	jobDir := filepath.Join(root, ".reactorcide", "jobs")
	if err := os.MkdirAll(workflowDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(jobDir, 0o700); err != nil {
		t.Fatal(err)
	}
	selected := filepath.Join(workflowDir, "selected-source.yaml")
	if err := os.WriteFile(selected, []byte("name: selected\njobs: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workflowDir, "other.yaml"), []byte("name: other\njobs: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(jobDir, "build.yaml"), []byte("command: true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	prepared, relative, cleanup, err := prepareSelectedCITree(root, ".reactorcide/workflows/selected-source.yaml")
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if relative != ".reactorcide/workflows/selected.yaml" {
		t.Fatalf("relative = %q", relative)
	}
	if _, err := os.Stat(filepath.Join(prepared, ".reactorcide", "workflows", "other.yaml")); !os.IsNotExist(err) {
		t.Fatalf("other workflow was copied: %v", err)
	}
	if _, err := os.Stat(filepath.Join(prepared, ".reactorcide", "jobs", "build.yaml")); err != nil {
		t.Fatal(err)
	}
}

func TestLocalContextPrecedenceAndWorkflowOverlays(t *testing.T) {
	timeout := 20
	local := localContext{DefaultRunnerImage: "synced", DefaultJobCommand: "synced command", DefaultTimeoutSeconds: 10, CheckoutMode: "isolated", Overrides: localContextOverrides{RunnerImage: "override", TimeoutSeconds: &timeout}}
	node := &localWorkflowNode{DisplayName: "build", Spec: localTriggerJob{JobName: "build", ContainerImage: "job", JobCommand: "job command"}}
	spec := localNodeJobSpec(node, local)
	if spec.Image != "override" || spec.Command != "job command" || spec.TimeoutSeconds != 20 || spec.Checkout.Mode != "isolated" {
		t.Fatalf("context result = %#v", spec)
	}
	overlay := filepath.Join(t.TempDir(), "overlay.yaml")
	if err := os.WriteFile(overlay, []byte("image: cli-overlay\ntimeout_seconds: 30\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := applyWorkflowCLIOverlays(&spec, []string{overlay}, false); err != nil {
		t.Fatal(err)
	}
	if spec.Image != "cli-overlay" || spec.TimeoutSeconds != 30 {
		t.Fatalf("overlay result = %#v", spec)
	}
}

func TestLocalNodeJobSpecPreservesWorkflowExecutionFields(t *testing.T) {
	node := &localWorkflowNode{DisplayName: "windows", Spec: localTriggerJob{
		JobName:         "windows",
		WorkerClass:     "vm",
		DisableRunLocal: true,
		RunLocal:        &worker.RunLocalSpec{AsRunner: true},
		Characteristics: map[string]interface{}{"os": "windows"},
		Resources:       map[string]interface{}{"cpu": map[string]interface{}{"limit": "2"}},
	}}
	spec := localNodeJobSpec(node, localContext{})
	if spec.WorkerClass != "vm" || !spec.DisableRunLocal || spec.RunLocal == nil || !spec.RunLocal.AsRunner {
		t.Fatalf("execution fields were not preserved: %#v", spec)
	}
	if spec.Characteristics["os"] != "windows" || spec.Resources["cpu"] == nil {
		t.Fatalf("routing or resources were not preserved: %#v", spec)
	}
}

func TestLocalContextLimitsRejectCapabilitiesResourcesAndSecrets(t *testing.T) {
	ceiling := 30
	local := localContext{RuntimeCapabilities: []string{"builder"}, ResourceCeilings: map[string]interface{}{"cpu_limit": "1"}, TimeoutCeilingSeconds: &ceiling, DenySecrets: true}
	spec := &worker.JobSpec{TimeoutSeconds: 31}
	if err := validateLocalContextLimits(spec, local); err == nil {
		t.Fatal("timeout ceiling was not enforced")
	}
	spec = &worker.JobSpec{Capabilities: []string{"docker"}}
	if err := validateLocalContextLimits(spec, local); err == nil {
		t.Fatal("capability limit was not enforced")
	}
	spec = &worker.JobSpec{CPULimit: "2"}
	if err := validateLocalContextLimits(spec, local); err == nil {
		t.Fatal("resource ceiling was not enforced")
	}
	spec = &worker.JobSpec{Environment: map[string]string{"TOKEN": "${secret:path:key}"}}
	if err := validateLocalContextLimits(spec, local); err == nil {
		t.Fatal("secret denial was not enforced")
	}
}

func TestLocalContextUserChecksResolvedIdentity(t *testing.T) {
	set := flag.NewFlagSet("test", flag.ContinueOnError)
	set.String("user", "", "")
	set.Bool("as-runner", false, "")
	ctx := cli.NewContext(nil, set, nil)

	if err := set.Set("user", "root"); err != nil {
		t.Fatal(err)
	}
	if err := validateLocalContextUser(ctx, &worker.JobSpec{}, localContext{}); err == nil {
		t.Fatal("resolved root user was allowed")
	}
	if err := set.Set("user", "runner"); err != nil {
		t.Fatal(err)
	}
	if err := validateLocalContextUser(ctx, &worker.JobSpec{}, localContext{}); err != nil {
		t.Fatalf("resolved runner user was rejected: %v", err)
	}
}

func TestOfflineContextStatusReportsAge(t *testing.T) {
	now := time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)
	line := localContextStatusLine("offline", localContext{SyncedAt: now.Add(-2 * time.Hour)}, now)
	if line != "Local context: offline (synchronized 2h0m0s ago)" {
		t.Fatalf("line = %q", line)
	}
}

func TestWorkflowVariablesUseAFileInsteadOfProcessArguments(t *testing.T) {
	dir := t.TempDir()
	varsFile := filepath.Join(dir, "workflow-vars.json")
	secretValue := "value-that-must-not-be-an-argument"
	if err := os.WriteFile(varsFile, []byte(`{"token":"`+secretValue+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	set := flag.NewFlagSet("test", flag.ContinueOnError)
	set.String("backend", "docker", "")
	ctx := cli.NewContext(nil, set, nil)
	args := localWorkflowSubprocessArgs(ctx, dir, varsFile, filepath.Join(dir, "result"))
	if strings.Contains(strings.Join(args, " "), secretValue) {
		t.Fatalf("secret value is present in process arguments: %#v", args)
	}
}
