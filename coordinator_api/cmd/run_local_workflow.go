package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/resources"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/secrets"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/worker"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/workflowengine"
	"github.com/google/uuid"
	"github.com/urfave/cli/v2"
	"gopkg.in/yaml.v3"
)

type localTriggerEnvelope struct {
	Workflows []localWorkflowSpec `json:"workflows"`
	Workflow  *localWorkflowSpec  `json:"workflow"`
	Jobs      []localTriggerJob   `json:"jobs"`
}

type localWorkflowSpec struct {
	ID   string                 `json:"id"`
	Name string                 `json:"name"`
	Vars map[string]interface{} `json:"vars"`
	Jobs []localTriggerJob      `json:"jobs"`
}

type localTriggerJob struct {
	JobName         string                 `json:"job_name"`
	DependsOn       []string               `json:"depends_on"`
	Condition       string                 `json:"condition"`
	Env             map[string]string      `json:"env"`
	ContainerImage  string                 `json:"container_image"`
	JobCommand      string                 `json:"job_command"`
	CodeDir         string                 `json:"code_dir"`
	JobDir          string                 `json:"job_dir"`
	WorkingDir      string                 `json:"working_dir"`
	RunAsUser       string                 `json:"run_as_user"`
	Timeout         *int                   `json:"timeout"`
	Capabilities    []string               `json:"capabilities"`
	ForEach         []interface{}          `json:"for_each"`
	ItemVar         string                 `json:"item_var"`
	Characteristics map[string]interface{} `json:"characteristics"`
	Resources       map[string]interface{} `json:"resources"`
	WorkerClass     string                 `json:"worker_class"`
	DisableRunLocal bool                   `json:"disable_run_local"`
	RunLocal        *worker.RunLocalSpec   `json:"run_local"`
}

type localWorkflowNode struct {
	ID          string
	Spec        localTriggerJob
	Name        string
	DisplayName string
	Status      string
	DependsOn   []string
	Condition   string
	Reason      string
}

type localWorkflowSummary struct {
	Name        string                     `json:"name"`
	Status      string                     `json:"status"`
	CompletedAt time.Time                  `json:"completed_at"`
	Variables   []string                   `json:"variables,omitempty"`
	Nodes       []localWorkflowSummaryNode `json:"nodes"`
}

type localWorkflowSummaryNode struct {
	Name        string `json:"name"`
	DisplayName string `json:"display_name"`
	Status      string `json:"status"`
	Reason      string `json:"reason,omitempty"`
}

type localWorkflowOutput struct {
	Vars    map[string]interface{} `json:"vars"`
	Outputs map[string]interface{} `json:"outputs"`
}

type localWorkflowResult struct {
	index  int
	output localWorkflowOutput
	err    error
}

type localWorkflowExecutor interface {
	Execute(context.Context, *localWorkflowNode, map[string]interface{}) (localWorkflowOutput, error)
}

type localWorkflowEngineAdapter struct {
	nodes    *[]localWorkflowNode
	vars     map[string]interface{}
	executor localWorkflowExecutor
	results  chan<- localWorkflowResult
	running  *int
}

func (a *localWorkflowEngineAdapter) Nodes(context.Context) ([]workflowengine.Node, error) {
	return localWorkflowStates(*a.nodes), nil
}

func (a *localWorkflowEngineAdapter) ApplyDecision(_ context.Context, decision workflowengine.Decision) error {
	i := localWorkflowNodeIndex(*a.nodes, decision.NodeID)
	if i < 0 {
		return fmt.Errorf("workflow node %q was not found", decision.NodeID)
	}
	node := &(*a.nodes)[i]
	switch decision.Action {
	case workflowengine.ActionWait:
		node.Status, node.Reason = "waiting", decision.Reason
	case workflowengine.ActionSkip:
		node.Status, node.Reason = "skipped", decision.Reason
		fmt.Printf("[%s] skipped: %s\n", node.DisplayName, decision.Reason)
	}
	return nil
}

func (a *localWorkflowEngineAdapter) Start(ctx context.Context, decision workflowengine.Decision) error {
	i := localWorkflowNodeIndex(*a.nodes, decision.NodeID)
	if i < 0 {
		return fmt.Errorf("workflow node %q was not found", decision.NodeID)
	}
	node := &(*a.nodes)[i]
	node.Status, node.Reason = "running", decision.Reason
	fmt.Printf("[%s] running\n", node.DisplayName)
	snapshot := cloneWorkflowValues(a.vars)
	(*a.running)++
	go func(index int, current *localWorkflowNode) {
		output, err := a.executor.Execute(ctx, current, snapshot)
		a.results <- localWorkflowResult{index: index, output: output, err: err}
	}(i, node)
	return nil
}

type localJobFields struct {
	Image    bool
	Command  bool
	Timeout  bool
	Checkout bool
}

func isWorkflowDefinitionFile(path string) (bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return false, fmt.Errorf("read run-local input: %w", err)
	}
	var raw map[string]interface{}
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return false, fmt.Errorf("parse run-local input: %w", err)
	}
	_, hasJobs := raw["jobs"]
	_, hasJob := raw["job"]
	_, hasCommand := raw["command"]
	return hasJobs && !hasJob && !hasCommand, nil
}

func localJobExplicitFields(path string) localJobFields {
	data, err := os.ReadFile(path)
	if err != nil {
		return localJobFields{}
	}
	var raw map[string]interface{}
	if yaml.Unmarshal(data, &raw) != nil {
		return localJobFields{}
	}
	var fields localJobFields
	_, fields.Image = raw["image"]
	_, fields.Command = raw["command"]
	_, fields.Timeout = raw["timeout_seconds"]
	_, fields.Checkout = raw["checkout"]
	if nested, ok := raw["job"].(map[string]interface{}); ok {
		if _, ok := nested["image"]; ok {
			fields.Image = true
		}
		if _, ok := nested["command"]; ok {
			fields.Command = true
		}
		if _, ok := nested["timeout"]; ok {
			fields.Timeout = true
		}
		if _, ok := nested["checkout"]; ok {
			fields.Checkout = true
		}
	}
	return fields
}

func loadLocalJobSpec(path string, overlayPaths []string, local *localContext) (*worker.JobSpec, []worker.SecretOverride, error) {
	base, err := worker.LoadJobSpecPartial(path)
	if err != nil {
		return nil, nil, err
	}
	if local != nil {
		applyLocalContextToJob(base, *local, localJobExplicitFields(path))
	}
	merged, secretOverrides, err := mergeLocalJobOverlays(base, overlayPaths)
	if err != nil {
		return nil, nil, err
	}
	if err := worker.FinalizeJobSpec(merged); err != nil {
		return nil, nil, err
	}
	return merged, secretOverrides, nil
}

func applyLocalContextToJob(spec *worker.JobSpec, local localContext, fields localJobFields) {
	if !fields.Image && local.DefaultRunnerImage != "" {
		spec.Image = local.DefaultRunnerImage
	}
	if !fields.Command && local.DefaultJobCommand != "" {
		spec.Command = local.DefaultJobCommand
	}
	if !fields.Timeout && local.DefaultTimeoutSeconds > 0 {
		spec.TimeoutSeconds = local.DefaultTimeoutSeconds
	}
	if !fields.Checkout && local.CheckoutMode != "" {
		spec.Checkout = &worker.CheckoutSpec{Mode: local.CheckoutMode}
	}
	if local.Overrides.RunnerImage != "" {
		spec.Image = local.Overrides.RunnerImage
	}
	if local.Overrides.JobCommand != "" {
		spec.Command = local.Overrides.JobCommand
	}
	if local.Overrides.TimeoutSeconds != nil {
		spec.TimeoutSeconds = *local.Overrides.TimeoutSeconds
	}
	if local.Overrides.CheckoutMode != "" {
		spec.Checkout = &worker.CheckoutSpec{Mode: local.Overrides.CheckoutMode}
	}
}

func mergeLocalJobOverlays(base *worker.JobSpec, overlayPaths []string) (*worker.JobSpec, []worker.SecretOverride, error) {
	if len(overlayPaths) == 0 {
		return base, nil, nil
	}
	overlays := make([]*worker.JobSpec, 0, len(overlayPaths))
	files := make([]string, 0, len(overlayPaths))
	for i := len(overlayPaths) - 1; i >= 0; i-- {
		overlay, err := worker.LoadJobSpecOverlay(overlayPaths[i])
		if err != nil {
			return nil, nil, err
		}
		overlays = append(overlays, overlay)
		files = append(files, overlayPaths[i])
	}
	merged, overrides := worker.MergeJobSpecs(base, overlays, files)
	return merged, overrides, nil
}

func applyWorkflowCLIOverlays(spec *worker.JobSpec, overlayPaths []string, allowSecretOverrides bool) error {
	merged, overrides, err := mergeLocalJobOverlays(spec, overlayPaths)
	if err != nil {
		return err
	}
	if err := checkSecretOverrides(overrides, allowSecretOverrides); err != nil {
		return err
	}
	if err := worker.FinalizeJobSpec(merged); err != nil {
		return err
	}
	*spec = *merged
	return nil
}

func validateLocalContextLimits(spec *worker.JobSpec, local localContext) error {
	for _, value := range spec.Environment {
		for _, match := range worker.SecretRefPattern.FindAllStringSubmatch(value, -1) {
			if local.DenySecrets {
				return fmt.Errorf("secrets are not allowed by the local context execution profile")
			}
			if len(local.SecretPathAllowlist) > 0 && !worker.SecretPathAllowed(local.SecretPathAllowlist, match[1]) {
				return fmt.Errorf("secret path %q is not allowed by the local context execution profile", match[1])
			}
		}
	}
	if local.TimeoutCeilingSeconds != nil && spec.TimeoutSeconds > *local.TimeoutCeilingSeconds {
		return fmt.Errorf("job timeout exceeds local context execution profile ceiling")
	}
	if local.RuntimeCapabilities != nil {
		allowed := make(map[string]bool, len(local.RuntimeCapabilities))
		for _, capability := range local.RuntimeCapabilities {
			allowed[capability] = true
		}
		for _, capability := range spec.Capabilities {
			if !allowed[capability] {
				return fmt.Errorf("capability %q is not allowed by the local context", capability)
			}
		}
	}
	ceilings := map[string]interface{}{}
	if local.ResourceCeilings != nil {
		data, _ := json.Marshal(local.ResourceCeilings)
		_ = json.Unmarshal(data, &ceilings)
	}
	requested := map[string]string{"cpu_limit": spec.CPULimit, "memory_limit": spec.MemoryLimit}
	if len(spec.Resources) > 0 {
		cpuRequest, cpuLimit, memoryLimit, err := resources.ParseResources(spec.Resources)
		if err != nil {
			return err
		}
		requested["cpu_request"], requested["cpu_limit"], requested["memory_limit"] = cpuRequest, cpuLimit, memoryLimit
	}
	for key, ceiling := range ceilings {
		value := requested[key]
		if value == "" {
			continue
		}
		var actual, limit int64
		var err error
		if strings.HasPrefix(key, "cpu_") {
			actual, err = resources.ParseCPU(value)
			if err == nil {
				limit, err = resources.ParseCPU(fmt.Sprint(ceiling))
			}
		} else if key == "memory_limit" {
			actual, err = resources.ParseMemory(value)
			if err == nil {
				limit, err = resources.ParseMemory(fmt.Sprint(ceiling))
			}
		}
		if err != nil {
			return fmt.Errorf("invalid local context %s ceiling: %w", key, err)
		}
		if actual > limit {
			return fmt.Errorf("job %s exceeds local context execution profile ceiling", key)
		}
	}
	return nil
}

func validateLocalContextUser(ctx *cli.Context, spec *worker.JobSpec, local localContext) error {
	if local.MayRunAsRoot {
		return nil
	}
	uid, _, _, err := resolveRunAsUser(ctx, spec)
	if err != nil {
		return err
	}
	if uid == 0 {
		return fmt.Errorf("root execution is not allowed by the local context")
	}
	return nil
}

func runLocalWorkflow(ctx *cli.Context, workflowFile string) error {
	if ctx.Int("max-parallel") < 1 {
		return fmt.Errorf("--max-parallel must be at least 1")
	}
	ciRoot, relativeWorkflow, err := localWorkflowRoot(ctx, workflowFile)
	if err != nil {
		return err
	}
	sourceRoot, err := resolveLocalDirectory(ctx.String("source-dir"))
	if err != nil {
		return fmt.Errorf("resolve source directory: %w", err)
	}
	if info, statErr := os.Stat(sourceRoot); statErr != nil {
		return fmt.Errorf("source directory does not exist: %s", sourceRoot)
	} else if !info.IsDir() {
		return fmt.Errorf("source directory is not a directory: %s", sourceRoot)
	}
	var local localContext
	if contextName := ctx.String("context"); contextName != "" {
		local, err = loadLocalContext(contextName)
		if err != nil {
			return err
		}
		evalImage := local.EvalImage
		if local.Overrides.EvalImage != "" {
			evalImage = local.Overrides.EvalImage
		}
		if !ctx.IsSet("eval-image") && evalImage != "" {
			if err := ctx.Set("eval-image", evalImage); err != nil {
				return err
			}
		}
		fmt.Println(localContextStatusLine(contextName, local, time.Now()))
	}
	codeSource := resolveCodeSource(ctx)
	if codeSource != nil {
		uid, gid := hostRunAsUser()
		var cleanup func()
		sourceRoot, cleanup, err = cloneCodeSource(codeSource, uid, gid)
		if err != nil {
			return err
		}
		defer cleanup()
	}

	selectedRoot, selectedWorkflow, cleanupCI, err := prepareSelectedCITree(ciRoot, relativeWorkflow)
	if err != nil {
		return err
	}
	defer cleanupCI()
	fmt.Printf("Workflow: %s\n", relativeWorkflow)
	envelope, err := evaluateLocalWorkflow(ctx, selectedRoot, sourceRoot, selectedWorkflow, codeSource)
	if err != nil {
		return err
	}
	if len(envelope.Workflows) == 0 && envelope.Workflow != nil {
		legacy := *envelope.Workflow
		legacy.Jobs = envelope.Jobs
		envelope.Workflows = []localWorkflowSpec{legacy}
	}
	if len(envelope.Workflows) != 1 {
		return fmt.Errorf("local evaluation produced %d workflows; expected one", len(envelope.Workflows))
	}

	executor := &subprocessWorkflowExecutor{ctx: ctx, sourceRoot: sourceRoot, ciRoot: ciRoot, localContext: local, hasLocalContext: ctx.String("context") != ""}
	summaryPath := filepath.Join(ciRoot, "reactorcide-workflow-summary.json")
	return executeLocalWorkflowGraphWithSummary(ctx.Context, envelope.Workflows[0], ctx.Int("max-parallel"), executor, summaryPath)
}

func localContextStatusLine(name string, local localContext, now time.Time) string {
	age := now.Sub(local.SyncedAt).Round(time.Second)
	if age < 0 {
		age = 0
	}
	return fmt.Sprintf("Local context: %s (synchronized %s ago)", name, age)
}

func prepareSelectedCITree(ciRoot, workflowFile string) (string, string, func(), error) {
	root, err := os.MkdirTemp("", "reactorcide-selected-ci-")
	if err != nil {
		return "", "", nil, err
	}
	cleanup := func() { _ = os.RemoveAll(root) }
	selected := filepath.Join(".reactorcide", "workflows", "selected.yaml")
	if err := copyLocalCIPath(filepath.Join(ciRoot, workflowFile), filepath.Join(root, selected), ciRoot); err != nil {
		cleanup()
		return "", "", nil, err
	}
	for _, relative := range []string{filepath.Join(".reactorcide", "jobs"), filepath.Join(".reactorcide", "policies"), filepath.Join(".reactorcide", "policy.yaml")} {
		source := filepath.Join(ciRoot, relative)
		if _, err := os.Stat(source); os.IsNotExist(err) {
			continue
		} else if err != nil {
			cleanup()
			return "", "", nil, err
		}
		if err := copyLocalCIPath(source, filepath.Join(root, relative), ciRoot); err != nil {
			cleanup()
			return "", "", nil, err
		}
	}
	return root, filepath.ToSlash(selected), cleanup, nil
}

func copyLocalCIPath(source, destination, allowedRoot string) error {
	resolved, err := filepath.EvalSymlinks(source)
	if err != nil {
		return err
	}
	root, err := filepath.Abs(allowedRoot)
	if err != nil {
		return err
	}
	resolved, err = filepath.Abs(resolved)
	if err != nil {
		return err
	}
	relative, err := filepath.Rel(root, resolved)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return fmt.Errorf("CI path %q resolves outside the local CI tree", source)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return err
	}
	if info.IsDir() {
		if err := os.MkdirAll(destination, 0o700); err != nil {
			return err
		}
		entries, err := os.ReadDir(resolved)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if err := copyLocalCIPath(filepath.Join(resolved, entry.Name()), filepath.Join(destination, entry.Name()), root); err != nil {
				return err
			}
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		return err
	}
	in, err := os.Open(resolved)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(destination, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, info.Mode().Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

func localWorkflowRoot(ctx *cli.Context, workflowFile string) (string, string, error) {
	absWorkflow, err := filepath.Abs(workflowFile)
	if err != nil {
		return "", "", err
	}
	root, err := resolveLocalDirectory(ctx.String("ci-dir"))
	if err != nil {
		return "", "", err
	}
	if info, statErr := os.Stat(root); statErr != nil {
		return "", "", fmt.Errorf("CI directory does not exist: %s", root)
	} else if !info.IsDir() {
		return "", "", fmt.Errorf("CI directory is not a directory: %s", root)
	}
	relative, err := filepath.Rel(root, absWorkflow)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", "", fmt.Errorf("workflow file must be inside --ci-dir")
	}
	return root, filepath.ToSlash(relative), nil
}

func evaluateLocalWorkflow(ctx *cli.Context, ciRoot, sourceRoot, workflowFile string, codeSource *resolvedCodeSource) (localTriggerEnvelope, error) {
	workspace, err := os.MkdirTemp("/tmp", "reactorcide-local-eval-")
	if err != nil {
		return localTriggerEnvelope{}, err
	}
	defer os.RemoveAll(workspace)
	uid, gid := hostRunAsUser()
	if err := makeWritableFor(workspace, uid, gid); err != nil {
		return localTriggerEnvelope{}, err
	}
	args := localWorkflowEvalArgs(ctx, workflowFile, codeSource)
	quoted := make([]string, len(args))
	for i, arg := range args {
		quoted[i] = shellQuote(arg)
	}
	spec := &worker.JobSpec{
		Name:        "evaluate local workflow",
		Image:       ctx.String("eval-image"),
		Command:     strings.Join(quoted, " ") + "\n",
		Environment: map[string]string{"REACTORCIDE_WORKER_MODE": "local", "HOME": "/tmp"},
	}
	config := spec.ToJobConfig(workspace, "eval-"+uuid.NewString()[:8], "local")
	config.SourceDir = sourceRoot
	config.SourceMountPath = "/job/src"
	config.RunAsUser = fmt.Sprintf("%d:%d", uid, gid)
	config.ExtraMounts = append(config.ExtraMounts, fmt.Sprintf("%s:/job/ci:ro", ciRoot))
	runner, err := worker.NewJobRunner(ctx.String("backend"))
	if err != nil {
		return localTriggerEnvelope{}, err
	}
	if err := executeLocalJob(context.Background(), runner, config, secrets.NewMasker()); err != nil {
		return localTriggerEnvelope{}, fmt.Errorf("workflow evaluation failed: %w", err)
	}
	data, err := os.ReadFile(filepath.Join(workspace, "triggers.json"))
	if err != nil {
		return localTriggerEnvelope{}, fmt.Errorf("workflow evaluation did not produce triggers: %w", err)
	}
	var envelope localTriggerEnvelope
	if err := json.Unmarshal(data, &envelope); err != nil {
		return localTriggerEnvelope{}, fmt.Errorf("parse workflow triggers: %w", err)
	}
	return envelope, nil
}

func localWorkflowEvalArgs(ctx *cli.Context, workflowFile string, codeSource *resolvedCodeSource) []string {
	args := []string{
		"runnerlib", "eval",
		"--ci-source-dir", "/job/ci",
		"--source-dir", "/job/src",
		"--event-type", ctx.String("event"),
		"--workflow-file", filepath.ToSlash(filepath.Join("/job/ci", workflowFile)),
		"--triggers-file", "/job/triggers.json",
	}
	if codeSource != nil {
		args = append(args, "--source-url", codeSource.URL)
		if codeSource.Ref != "" {
			args = append(args, "--source-ref", codeSource.Ref)
		}
	}
	for _, path := range ctx.StringSlice("changed-file") {
		args = append(args, "--changed-file", path)
	}
	return args
}

func executeLocalWorkflowGraph(ctx context.Context, workflow localWorkflowSpec, maxParallel int, executor localWorkflowExecutor) error {
	return executeLocalWorkflowGraphWithSummary(ctx, workflow, maxParallel, executor, "")
}

func executeLocalWorkflowGraphWithSummary(ctx context.Context, workflow localWorkflowSpec, maxParallel int, executor localWorkflowExecutor, summaryPath string) error {
	nodes := expandLocalWorkflowNodes(workflow.Jobs)
	vars := cloneWorkflowValues(workflow.Vars)
	results := make(chan localWorkflowResult, maxParallel)
	running := 0
	adapter := &localWorkflowEngineAdapter{nodes: &nodes, vars: vars, executor: executor, results: results, running: &running}
	engine := workflowengine.Engine{Store: adapter, Executor: adapter}

	for {
		decisions, err := engine.Advance(ctx, maxParallel-running)
		if err != nil {
			return err
		}
		progress := false
		for _, decision := range decisions {
			switch decision.Action {
			case workflowengine.ActionSkip:
				progress = true
			case workflowengine.ActionStart:
				progress = true
			}
		}

		status := workflowengine.ComputeStatus(localWorkflowStates(nodes))
		if workflowengineStatusTerminal(status) && running == 0 {
			printLocalWorkflowSummary(workflow.Name, status, nodes)
			if err := writeLocalWorkflowSummary(summaryPath, workflow.Name, status, vars, nodes); err != nil {
				return err
			}
			if status != "success" && status != "skipped" {
				return cli.Exit("workflow failed", 1)
			}
			return nil
		}
		if running == 0 {
			if !progress {
				_ = writeLocalWorkflowSummary(summaryPath, workflow.Name, "failed", vars, nodes)
				return fmt.Errorf("workflow cannot make progress; check dependency names and cycles")
			}
			continue
		}

		completed := <-results
		running--
		node := &nodes[completed.index]
		if completed.err != nil {
			node.Status, node.Reason = "failed", completed.err.Error()
			fmt.Printf("[%s] failed\n", node.DisplayName)
			continue
		}
		if err := mergeLocalWorkflowOutput(vars, node.Name, completed.output); err != nil {
			node.Status, node.Reason = "failed", err.Error()
			fmt.Printf("[%s] failed: %s\n", node.DisplayName, err)
			continue
		}
		node.Status, node.Reason = "completed", "job finished with status completed"
		fmt.Printf("[%s] completed\n", node.DisplayName)
	}
}

func expandLocalWorkflowNodes(specs []localTriggerJob) []localWorkflowNode {
	expansionSpecs := make([]workflowengine.ExpansionSpec, len(specs))
	for i := range specs {
		expansionSpecs[i] = workflowengine.ExpansionSpec{Name: specs[i].JobName, ForEach: specs[i].ForEach, ItemVar: specs[i].ItemVar, Payload: specs[i]}
	}
	expansions := workflowengine.Expand(expansionSpecs)
	nodes := make([]localWorkflowNode, 0, len(expansions))
	for _, expansion := range expansions {
		spec := expansion.Payload.(localTriggerJob)
		condition := spec.Condition
		if condition == "" {
			condition = "all_success"
		}
		if expansion.ItemIndex != nil {
			copySpec := spec
			copySpec.Env = cloneStringMapLocal(spec.Env)
			copySpec.Env[expansion.ItemVar] = stringifyLocalWorkflowValue(expansion.ItemValue)
			spec = copySpec
		}
		nodes = append(nodes, localWorkflowNode{ID: expansion.DisplayName, Spec: spec, Name: expansion.Name, DisplayName: expansion.DisplayName, Status: "pending", DependsOn: spec.DependsOn, Condition: condition})
	}
	return nodes
}

type subprocessWorkflowExecutor struct {
	ctx             *cli.Context
	sourceRoot      string
	ciRoot          string
	localContext    localContext
	hasLocalContext bool
}

func (e *subprocessWorkflowExecutor) Execute(ctx context.Context, node *localWorkflowNode, vars map[string]interface{}) (localWorkflowOutput, error) {
	tempDir, err := os.MkdirTemp("/tmp", "reactorcide-local-node-")
	if err != nil {
		return localWorkflowOutput{}, err
	}
	defer os.RemoveAll(tempDir)
	varsFile := filepath.Join(tempDir, "workflow-vars.json")
	varsData, err := json.Marshal(vars)
	if err != nil {
		return localWorkflowOutput{}, err
	}
	if err := os.WriteFile(varsFile, varsData, 0o600); err != nil {
		return localWorkflowOutput{}, err
	}
	resultDir := filepath.Join(tempDir, "result")
	if err := os.Mkdir(resultDir, 0o700); err != nil {
		return localWorkflowOutput{}, err
	}
	spec := localNodeJobSpec(node, e.localContext)
	if err := applyWorkflowCLIOverlays(&spec, e.ctx.StringSlice("input"), e.ctx.Bool("allow-secret-overrides")); err != nil {
		return localWorkflowOutput{}, err
	}
	if e.hasLocalContext {
		if err := validateLocalContextLimits(&spec, e.localContext); err != nil {
			return localWorkflowOutput{}, err
		}
		if err := validateLocalContextUser(e.ctx, &spec, e.localContext); err != nil {
			return localWorkflowOutput{}, err
		}
	}
	jobData, err := yaml.Marshal(spec)
	if err != nil {
		return localWorkflowOutput{}, err
	}
	jobFile := filepath.Join(tempDir, "job.yaml")
	if err := os.WriteFile(jobFile, jobData, 0o600); err != nil {
		return localWorkflowOutput{}, err
	}

	args := localWorkflowSubprocessArgs(e.ctx, e.sourceRoot, e.ciRoot, varsFile, resultDir)
	if e.ctx.Bool("dry-run") {
		args = append(args, "--dry-run")
	}
	if e.ctx.Bool("as-runner") {
		args = append(args, "--as-runner")
	}
	if user := e.ctx.String("user"); user != "" {
		args = append(args, "--user", user)
	}
	args = append(args, jobFile)
	executable, err := os.Executable()
	if err != nil {
		return localWorkflowOutput{}, err
	}
	command := exec.CommandContext(ctx, executable, args...)
	command.Stdout, command.Stderr = os.Stdout, os.Stderr
	if err := command.Run(); err != nil {
		return localWorkflowOutput{}, err
	}
	data, err := os.ReadFile(filepath.Join(resultDir, "workflow-output.json"))
	if os.IsNotExist(err) {
		return localWorkflowOutput{}, nil
	}
	if err != nil {
		return localWorkflowOutput{}, err
	}
	var output localWorkflowOutput
	if err := json.Unmarshal(data, &output); err != nil {
		return localWorkflowOutput{}, fmt.Errorf("parse workflow output: %w", err)
	}
	return output, nil
}

func localWorkflowSubprocessArgs(ctx *cli.Context, sourceRoot, ciRoot, varsFile, resultDir string) []string {
	return []string{"run-local", "--source-dir", sourceRoot, "--ci-dir", ciRoot, "--backend", ctx.String("backend"), "--workflow-vars-file", varsFile, "--result-dir", resultDir}
}

func localNodeJobSpec(node *localWorkflowNode, local localContext) worker.JobSpec {
	image := node.Spec.ContainerImage
	if image == "" {
		image = local.DefaultRunnerImage
	}
	if image == "" {
		image = worker.DefaultRunnerImage
	}
	if local.Overrides.RunnerImage != "" {
		image = local.Overrides.RunnerImage
	}
	env := cloneStringMapLocal(node.Spec.Env)
	env["REACTORCIDE_WORKER_MODE"] = "local"
	env["RC_WF_NODE_NAME"] = node.DisplayName
	command := node.Spec.JobCommand
	if command == "" {
		command = local.DefaultJobCommand
	}
	if local.Overrides.JobCommand != "" {
		command = local.Overrides.JobCommand
	}
	spec := worker.JobSpec{Name: node.DisplayName, Image: image, Command: command, Environment: env, CodeDir: node.Spec.CodeDir, JobDir: node.Spec.JobDir, WorkingDir: node.Spec.WorkingDir, Capabilities: node.Spec.Capabilities, Characteristics: node.Spec.Characteristics, Resources: node.Spec.Resources, WorkerClass: node.Spec.WorkerClass, DisableRunLocal: node.Spec.DisableRunLocal, RunLocal: node.Spec.RunLocal}
	if node.Spec.Timeout != nil {
		spec.TimeoutSeconds = *node.Spec.Timeout
	} else {
		spec.TimeoutSeconds = local.DefaultTimeoutSeconds
	}
	if local.Overrides.TimeoutSeconds != nil {
		spec.TimeoutSeconds = *local.Overrides.TimeoutSeconds
	}
	checkout := local.CheckoutMode
	if local.Overrides.CheckoutMode != "" {
		checkout = local.Overrides.CheckoutMode
	}
	if checkout != "" {
		spec.Checkout = &worker.CheckoutSpec{Mode: checkout}
	}
	if node.Spec.RunAsUser != "" {
		spec.RunAs = &worker.RunAsSpec{User: node.Spec.RunAsUser}
	}
	return spec
}

func mergeLocalWorkflowOutput(vars map[string]interface{}, nodeName string, output localWorkflowOutput) error {
	for _, value := range workflowengine.OutputValues(nodeName, output.Vars, output.Outputs) {
		if _, err := workflowengine.MergeValue(vars, value.Key, value.Value); err != nil {
			return err
		}
	}
	return nil
}

func cloneWorkflowValues(values map[string]interface{}) map[string]interface{} {
	out := map[string]interface{}{}
	for key, value := range values {
		out[key] = value
	}
	return out
}
func cloneStringMapLocal(values map[string]string) map[string]string {
	out := map[string]string{}
	for key, value := range values {
		out[key] = value
	}
	return out
}
func stringifyLocalWorkflowValue(value interface{}) string {
	if text, ok := value.(string); ok {
		return text
	}
	data, err := json.Marshal(value)
	if err == nil {
		return string(data)
	}
	return fmt.Sprint(value)
}
func localWorkflowState(node localWorkflowNode) workflowengine.Node {
	return workflowengine.Node{ID: node.ID, Name: node.Name, DisplayName: node.DisplayName, Status: node.Status, DependsOn: node.DependsOn, Condition: node.Condition}
}
func localWorkflowStates(nodes []localWorkflowNode) []workflowengine.Node {
	out := make([]workflowengine.Node, len(nodes))
	for i := range nodes {
		out[i] = localWorkflowState(nodes[i])
	}
	return out
}
func workflowengineStatusTerminal(status string) bool {
	return status == "success" || status == "failed" || status == "skipped" || status == "cancelled"
}
func localWorkflowNodeIndex(nodes []localWorkflowNode, id string) int {
	for i := range nodes {
		if nodes[i].ID == id {
			return i
		}
	}
	return -1
}
func shellQuote(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'" }

func printLocalWorkflowSummary(name, status string, nodes []localWorkflowNode) {
	if name == "" {
		name = "Local workflow"
	}
	fmt.Printf("\nWorkflow summary: %s (%s)\n", name, status)
	for _, node := range nodes {
		fmt.Printf("  %-24s %s\n", node.DisplayName, node.Status)
	}
}

func writeLocalWorkflowSummary(path, name, status string, vars map[string]interface{}, nodes []localWorkflowNode) error {
	if path == "" {
		return nil
	}
	variableNames := make([]string, 0, len(vars))
	for key := range vars {
		variableNames = append(variableNames, key)
	}
	sort.Strings(variableNames)
	summary := localWorkflowSummary{Name: name, Status: status, CompletedAt: time.Now().UTC(), Variables: variableNames}
	for _, node := range nodes {
		summary.Nodes = append(summary.Nodes, localWorkflowSummaryNode{Name: node.Name, DisplayName: node.DisplayName, Status: node.Status, Reason: node.Reason})
	}
	data, err := json.MarshalIndent(summary, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	temp, err := os.CreateTemp(filepath.Dir(path), ".workflow-summary-*.tmp")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(0o600); err != nil {
		_ = temp.Close()
		return err
	}
	if _, err := temp.Write(data); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return atomicReplace(tempPath, path)
}
