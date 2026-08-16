package cmd

import (
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/secrets"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/worker"
	"github.com/urfave/cli/v2"
	"gopkg.in/yaml.v3"
)

var localContextNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

type localContext struct {
	Name                    string                `yaml:"name" json:"name"`
	ProjectID               string                `yaml:"project_id" json:"project_id"`
	ProjectName             string                `yaml:"project_name" json:"project_name"`
	Repository              string                `yaml:"repository" json:"repository"`
	CoordinatorURL          string                `yaml:"coordinator_url" json:"coordinator_url"`
	DefaultRunnerImage      string                `yaml:"default_runner_image" json:"default_runner_image"`
	EvalImage               string                `yaml:"eval_image" json:"eval_image"`
	DefaultJobCommand       string                `yaml:"default_job_command,omitempty" json:"default_job_command,omitempty"`
	DefaultTimeoutSeconds   int                   `yaml:"default_timeout_seconds" json:"default_timeout_seconds"`
	CheckoutMode            string                `yaml:"checkout_mode,omitempty" json:"checkout_mode,omitempty"`
	DefaultWorkerClass      string                `yaml:"default_worker_class" json:"default_worker_class"`
	DefaultExecutionProfile string                `yaml:"default_execution_profile" json:"default_execution_profile"`
	CISourceURL             string                `yaml:"ci_source_url,omitempty" json:"ci_source_url,omitempty"`
	CISourceRef             string                `yaml:"ci_source_ref,omitempty" json:"ci_source_ref,omitempty"`
	SecretReferences        map[string]string     `yaml:"secret_references,omitempty" json:"secret_references,omitempty"`
	RuntimeCapabilities     []string              `yaml:"runtime_capabilities,omitempty" json:"runtime_capabilities,omitempty"`
	DenySecrets             bool                  `yaml:"deny_secrets" json:"deny_secrets"`
	SecretPathAllowlist     []string              `yaml:"secret_path_allowlist,omitempty" json:"secret_path_allowlist,omitempty"`
	ResourceCeilings        interface{}           `yaml:"resource_ceilings,omitempty" json:"resource_ceilings,omitempty"`
	TimeoutCeilingSeconds   *int                  `yaml:"timeout_ceiling_seconds,omitempty" json:"timeout_ceiling_seconds,omitempty"`
	MayRunAsRoot            bool                  `yaml:"may_run_as_root" json:"may_run_as_root"`
	WorkerPoolIDs           []string              `yaml:"worker_pool_ids,omitempty" json:"worker_pool_ids,omitempty"`
	AllowedWorkerClasses    []string              `yaml:"allowed_worker_classes,omitempty" json:"allowed_worker_classes,omitempty"`
	CacheNamespace          *string               `yaml:"cache_namespace,omitempty" json:"cache_namespace,omitempty"`
	ArtifactNamespace       *string               `yaml:"artifact_namespace,omitempty" json:"artifact_namespace,omitempty"`
	Overrides               localContextOverrides `yaml:"overrides,omitempty" json:"overrides,omitempty"`
	SyncedAt                time.Time             `yaml:"synced_at" json:"synced_at"`
}

type localContextOverrides struct {
	RunnerImage    string `yaml:"runner_image,omitempty" json:"runner_image,omitempty"`
	EvalImage      string `yaml:"eval_image,omitempty" json:"eval_image,omitempty"`
	JobCommand     string `yaml:"job_command,omitempty" json:"job_command,omitempty"`
	TimeoutSeconds *int   `yaml:"timeout_seconds,omitempty" json:"timeout_seconds,omitempty"`
	CheckoutMode   string `yaml:"checkout_mode,omitempty" json:"checkout_mode,omitempty"`
}

type workflowSecretSelection struct {
	Path    string `json:"path"`
	Key     string `json:"key"`
	JobName string `json:"job_name"`
}

var LocalContextCommand = &cli.Command{
	Name: "local-context", Usage: "Manage synchronized local execution settings",
	Subcommands: []*cli.Command{
		{Name: "sync", Usage: "Synchronize non-secret project settings", Flags: append(apiFlags(),
			&cli.StringFlag{Name: "project", Required: true}, &cli.StringFlag{Name: "name", Required: true},
			&cli.StringFlag{Name: "include-workflow-secrets", Usage: "Workflow file whose referenced secrets are copied to the encrypted local store"},
			&cli.BoolFlag{Name: "replace-secrets", Usage: "Replace local values for selected secret references"}), Action: syncLocalContext},
		{Name: "show", ArgsUsage: "[name]", Flags: []cli.Flag{formatFlag(), &cli.StringFlag{Name: "name"}}, Action: showLocalContext},
		{Name: "remove", ArgsUsage: "[name]", Flags: []cli.Flag{&cli.StringFlag{Name: "name"}}, Action: removeLocalContext},
	},
}

func syncLocalContext(ctx *cli.Context) error {
	name := ctx.String("name")
	if err := validateLocalContextName(name); err != nil {
		return err
	}
	api, err := newAPIClient(ctx)
	if err != nil {
		return err
	}
	var value localContext
	if err := api.doJSON(http.MethodGet, "/api/v1/projects/"+url.PathEscape(ctx.String("project"))+"/local-context", nil, http.StatusOK, &value); err != nil {
		return err
	}
	value.Name = name
	value.CoordinatorURL = api.apiURL
	if value.DefaultRunnerImage == "" {
		value.DefaultRunnerImage = worker.DefaultRunnerImage
	}
	if value.EvalImage == "" {
		value.EvalImage = value.DefaultRunnerImage
	}
	if value.DefaultTimeoutSeconds == 0 {
		value.DefaultTimeoutSeconds = 3600
	}
	if value.SyncedAt.IsZero() {
		value.SyncedAt = time.Now().UTC()
	}
	if existing, loadErr := loadLocalContext(name); loadErr == nil {
		value.Overrides = existing.Overrides
	} else if !os.IsNotExist(loadErr) {
		return loadErr
	}
	if err := writeLocalContext(value); err != nil {
		return err
	}
	fmt.Printf("Synchronized local context %s for project %s.\n", name, value.ProjectID)
	if workflowFile := ctx.String("include-workflow-secrets"); workflowFile != "" {
		return syncSelectedWorkflowSecrets(ctx, workflowFile)
	}
	return nil
}

func showLocalContext(ctx *cli.Context) error {
	name, err := localContextCommandName(ctx, "show")
	if err != nil {
		return err
	}
	value, err := loadLocalContext(name)
	if err != nil {
		return err
	}
	return render(ctx.String("format"), value, func(w *tabwriter.Writer) {
		fmt.Fprintln(w, "NAME\tPROJECT\tRUNNER IMAGE\tCOORDINATOR\tSYNCED")
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", value.Name, value.ProjectID, value.DefaultRunnerImage, value.CoordinatorURL, value.SyncedAt.Format(time.RFC3339))
	})
}

func removeLocalContext(ctx *cli.Context) error {
	name, err := localContextCommandName(ctx, "remove")
	if err != nil {
		return err
	}
	path, err := localContextPath(name)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil {
		return err
	}
	fmt.Printf("Removed local context %s.\n", name)
	return nil
}

func localContextCommandName(ctx *cli.Context, command string) (string, error) {
	name := ctx.String("name")
	if ctx.NArg() > 1 || name != "" && ctx.NArg() != 0 {
		return "", fmt.Errorf("usage: reactorcide local-context %s --name <name>", command)
	}
	if name == "" && ctx.NArg() == 1 {
		name = ctx.Args().First()
	}
	if name == "" {
		return "", fmt.Errorf("usage: reactorcide local-context %s --name <name>", command)
	}
	return name, nil
}

func localContextsDir() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "reactorcide", "contexts"), nil
}

func localContextPath(name string) (string, error) {
	if err := validateLocalContextName(name); err != nil {
		return "", err
	}
	dir, err := localContextsDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, name+".yaml"), nil
}

func validateLocalContextName(name string) error {
	if !localContextNamePattern.MatchString(name) {
		return fmt.Errorf("local context name must use letters, numbers, '.', '_', or '-'")
	}
	return nil
}

func writeLocalContext(value localContext) error {
	path, err := localContextPath(value.Name)
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	data, err := yaml.Marshal(value)
	if err != nil {
		return err
	}
	temp, err := os.CreateTemp(dir, ".context-*.tmp")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	keep := false
	defer func() {
		_ = temp.Close()
		if !keep {
			_ = os.Remove(tempPath)
		}
	}()
	if err := temp.Chmod(0o600); err != nil {
		return err
	}
	if _, err := temp.Write(data); err != nil {
		return err
	}
	if err := temp.Sync(); err != nil {
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := atomicReplace(tempPath, path); err != nil {
		return err
	}
	keep = true
	return nil
}

func loadLocalContext(name string) (localContext, error) {
	path, err := localContextPath(name)
	if err != nil {
		return localContext{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return localContext{}, err
	}
	var value localContext
	if err := yaml.Unmarshal(data, &value); err != nil {
		return localContext{}, err
	}
	return value, nil
}

func syncSelectedWorkflowSecrets(ctx *cli.Context, workflowFile string) error {
	selections, err := workflowSecretSelections(workflowFile)
	if err != nil {
		return err
	}
	refs := uniqueWorkflowSecretRefs(selections)
	if len(refs) == 0 {
		fmt.Println("The selected workflow has no secret references.")
		return nil
	}
	for _, ref := range refs {
		fmt.Printf("Selected secret reference: %s:%s\n", ref.Path, ref.Key)
	}
	remote, err := getProjectWorkflowSecrets(ctx, ctx.String("project"), selections)
	if err != nil {
		return err
	}
	password, err := getPassword("Secrets password: ")
	if err != nil {
		return err
	}
	storage := secrets.NewStorage()
	written, skipped := 0, 0
	for _, ref := range refs {
		exists, err := storage.Has(ref.Path, ref.Key, password)
		if err != nil {
			return err
		}
		if exists && !ctx.Bool("replace-secrets") {
			skipped++
			continue
		}
		value, ok := remote[ref.Path+":"+ref.Key]
		if !ok {
			return fmt.Errorf("API response omitted selected secret reference %s:%s", ref.Path, ref.Key)
		}
		if err := storage.Set(ref.Path, ref.Key, value, password); err != nil {
			return err
		}
		written++
	}
	fmt.Printf("Synchronized %d secret reference(s); kept %d existing local value(s).\n", written, skipped)
	return nil
}

func workflowSecretReferences(workflowFile string) ([]secrets.SecretRef, error) {
	selections, err := workflowSecretSelections(workflowFile)
	if err != nil {
		return nil, err
	}
	return uniqueWorkflowSecretRefs(selections), nil
}

func workflowSecretSelections(workflowFile string) ([]workflowSecretSelection, error) {
	data, err := os.ReadFile(workflowFile)
	if err != nil {
		return nil, err
	}
	var raw struct {
		Jobs map[string]map[string]interface{} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	var workflowFields map[string]interface{}
	if err := yaml.Unmarshal(data, &workflowFields); err != nil {
		return nil, err
	}
	delete(workflowFields, "jobs")
	workflowData, err := yaml.Marshal(workflowFields)
	if err != nil {
		return nil, err
	}
	root := filepath.Dir(filepath.Dir(filepath.Dir(workflowFile)))
	jobsRoot := filepath.Join(root, ".reactorcide", "jobs")
	var selections []workflowSecretSelection
	for jobName, job := range raw.Jobs {
		jobFile, _ := job["job_file"].(string)
		inline, err := yaml.Marshal(job)
		if err != nil {
			return nil, err
		}
		selections = appendWorkflowSecretSelections(selections, inline, jobName)
		if jobFile == "" {
			continue
		}
		jobPath := filepath.Join(jobsRoot, filepath.Clean(jobFile))
		relative, relErr := filepath.Rel(jobsRoot, jobPath)
		if relErr != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return nil, fmt.Errorf("job_file %q is outside .reactorcide/jobs", jobFile)
		}
		jobData, err := os.ReadFile(jobPath)
		if err != nil {
			return nil, err
		}
		selections = appendWorkflowSecretSelections(selections, jobData, jobName)
	}
	for jobName := range raw.Jobs {
		selections = appendWorkflowSecretSelections(selections, workflowData, jobName)
	}
	return dedupeWorkflowSecretSelections(selections), nil
}

func appendWorkflowSecretSelections(result []workflowSecretSelection, content []byte, jobName string) []workflowSecretSelection {
	for _, match := range worker.SecretRefPattern.FindAllStringSubmatch(string(content), -1) {
		result = append(result, workflowSecretSelection{Path: match[1], Key: match[2], JobName: jobName})
	}
	return result
}

func dedupeWorkflowSecretSelections(values []workflowSecretSelection) []workflowSecretSelection {
	unique := map[string]workflowSecretSelection{}
	for _, value := range values {
		unique[value.Path+"\x00"+value.Key+"\x00"+value.JobName] = value
	}
	result := make([]workflowSecretSelection, 0, len(unique))
	for _, value := range unique {
		result = append(result, value)
	}
	sort.Slice(result, func(i, j int) bool {
		left := result[i].Path + "\x00" + result[i].Key + "\x00" + result[i].JobName
		right := result[j].Path + "\x00" + result[j].Key + "\x00" + result[j].JobName
		return left < right
	})
	return result
}

func uniqueWorkflowSecretRefs(selections []workflowSecretSelection) []secrets.SecretRef {
	unique := map[string]secrets.SecretRef{}
	for _, selection := range selections {
		ref := secrets.SecretRef{Path: selection.Path, Key: selection.Key}
		unique[ref.Path+"\x00"+ref.Key] = ref
	}
	refs := make([]secrets.SecretRef, 0, len(unique))
	for _, ref := range unique {
		refs = append(refs, ref)
	}
	sort.Slice(refs, func(i, j int) bool {
		if refs[i].Path == refs[j].Path {
			return refs[i].Key < refs[j].Key
		}
		return refs[i].Path < refs[j].Path
	})
	return refs
}

func getProjectWorkflowSecrets(ctx *cli.Context, projectID string, selections []workflowSecretSelection) (map[string]string, error) {
	client, err := newSecretsAPIClient(ctx)
	if err != nil {
		return nil, err
	}
	refs := make([]secrets.SecretRef, len(selections))
	for i, selection := range selections {
		refs[i] = secrets.SecretRef{Path: selection.Path, Key: selection.Key}
	}
	body := struct {
		Refs       []secrets.SecretRef       `json:"refs"`
		ProjectID  string                    `json:"project_id"`
		Selections []workflowSecretSelection `json:"selections"`
	}{Refs: refs, ProjectID: projectID, Selections: selections}
	var response batchGetAPIResponse
	if err := client.doJSON(http.MethodPost, "/api/v1/secrets/batch/get", body, http.StatusOK, &response); err != nil {
		return nil, err
	}
	return response.Secrets, nil
}
