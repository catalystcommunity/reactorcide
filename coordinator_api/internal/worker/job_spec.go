package worker

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// DefaultRunnerImage is the default container image for job execution
const DefaultRunnerImage = "containers.catalystsquad.com/public/reactorcide/runnerbase:dev"

// JobSpec represents a job definition that can be loaded from YAML/JSON files
// or constructed programmatically. This is the canonical representation of a
// job that gets converted to JobConfig for execution.
type JobSpec struct {
	// Name is a human-readable name for the job
	Name string `json:"name" yaml:"name"`

	// Command is the full command to execute. Every command, single-line or
	// multi-line, goes through the shell prefix (see CommandPrefix), unless
	// it already starts with a shell invocation.
	Command string `json:"command" yaml:"command"`

	// CommandPrefix overrides the default shell wrapper of the command.
	// Default: REACTORCIDE_JOB_SHELLCMD from Environment, else "sh -c", or
	// WindowsShellPrefix for a job with the characteristic os: windows.
	// Examples: "bash -c", "zsh -c", "/bin/ash -c". "none" runs the command
	// without a shell (an image without /bin/sh). Does not apply to a
	// command that already starts with a shell invocation.
	CommandPrefix string `json:"command_prefix" yaml:"command_prefix"`

	// Image is the container image to use (defaults to DefaultRunnerImage)
	Image string `json:"image" yaml:"image"`

	// Environment variables to set in the container Values can contain
	// ${secret:path:key} references that get resolved
	Environment map[string]string `json:"environment" yaml:"environment"`

	// Source defines how to prepare the source code
	Source *SourceSpec `json:"source" yaml:"source"`

	// Checkout selects the runnerlib checkout layout. It is also supported by
	// overlay files. Isolated keeps separate clones. Shared uses one filtered
	// object store for pull-request evaluation.
	Checkout *CheckoutSpec `json:"checkout" yaml:"checkout"`

	// WorkingDir is the working directory inside the container (default: /job)
	WorkingDir string `json:"working_dir" yaml:"working_dir"`

	// CodeDir is where source code is expected inside the job container.
	// run-local mounts local code here; runnerlib checks out remote source
	// here.
	CodeDir string `json:"code_dir" yaml:"code_dir"`

	// JobDir is the directory runnerlib treats as the job working directory.
	// Defaults to CodeDir when empty.
	JobDir string `json:"job_dir" yaml:"job_dir"`

	// Capabilities declares what the job needs from the runtime environment.
	// The runner interprets these based on its environment:
	//   - "docker": Access to build/push container images
	//               DockerRunner: mounts /var/run/docker.sock
	//               KubernetesRunner: uses DinD sidecar or hostPath
	//   - "gpu": Access to GPU resources (future - not yet implemented)
	//            DockerRunner: --gpus all
	//            KubernetesRunner: nvidia.com/gpu resource request
	Capabilities []string `json:"capabilities" yaml:"capabilities"`

	// ImagePullSecrets lists the NAMES of Kubernetes Secrets (type
	// kubernetes.io/dockerconfigjson, in the job namespace) the job pod may
	// use to pull its image. Names only — never credentials. The worker
	// enforces its operator allowlist before Kubernetes Job creation; the
	// Docker/containerd/local runners preserve the field but never read a
	// Kubernetes Secret.
	ImagePullSecrets []string `json:"image_pull_secrets" yaml:"image_pull_secrets"`

	// Timeout in seconds (0 = no timeout)
	TimeoutSeconds int `json:"timeout_seconds" yaml:"timeout_seconds"`

	// Resource limits
	CPULimit    string `json:"cpu_limit" yaml:"cpu_limit"`
	MemoryLimit string `json:"memory_limit" yaml:"memory_limit"`

	// RunAs controls the container user for deployed workers. run-local uses
	// this only as a fallback after run_local and CLI overrides.
	RunAs *RunAsSpec `json:"run_as" yaml:"run_as"`

	// DisableRunLocal marks this job as remote-only. run-local refuses to
	// execute jobs with this set — used for jobs that fundamentally need CI
	// state (PR diff base, push-back-to-remote with a PAT, etc.).
	DisableRunLocal bool `json:"disable_run_local" yaml:"disable_run_local"`

	// RunLocal holds settings that only affect `reactorcide run-local`. The
	// worker ignores this block entirely (ToJobConfig never reads it), so
	// it's the place to pin local-only behavior — e.g. the container uid —
	// without changing how the job runs in CI. Defining it here makes a job
	// behave consistently across local invocations without per-command flags.
	RunLocal *RunLocalSpec `json:"run_local" yaml:"run_local"`

	// Characteristics routes this job to a queue when submitted remotely; raw
	// map, validated by internal/characteristics.ParseJobCharacteristics at
	// submit time. Not consumed by run-local/ToJobConfig -- queue routing
	// only applies to jobs submitted through the coordinator API (see
	// cmd/submit.go).
	Characteristics map[string]interface{} `json:"characteristics" yaml:"characteristics"`

	WorkerClass string `json:"worker_class" yaml:"worker_class"`

	// Resources declares per-job compute resource cpu.request/cpu.limit/
	// memory.limit; raw map, validated by internal/resources.ParseResources
	// at submit time. Not consumed by run-local/ToJobConfig -- see the flat
	// CPULimit/MemoryLimit fields above for that path.
	Resources map[string]interface{} `json:"resources" yaml:"resources"`
}

// RunAsSpec controls the user identity for deployed job containers.
type RunAsSpec struct {
	// User accepts "runner", "root", or numeric "uid[:gid]". "host" is
	// intentionally run-local-only and is not accepted by deployed workers.
	User string `json:"user" yaml:"user"`

	// Administrator applies only to Windows VM jobs. When the guest image
	// has no job account, the in-guest daemon creates it, and this value
	// says whether the new account goes in Administrators. Unset (nil)
	// means true. The daemon does not change an account that the image
	// already has. Container backends ignore this value.
	Administrator *bool `json:"administrator,omitempty" yaml:"administrator,omitempty"`
}

// copyRunAsSpec returns a deep copy of spec, or nil when spec is nil.
func copyRunAsSpec(spec *RunAsSpec) *RunAsSpec {
	if spec == nil {
		return nil
	}
	out := &RunAsSpec{User: spec.User}
	if spec.Administrator != nil {
		v := *spec.Administrator
		out.Administrator = &v
	}
	return out
}

// RunLocalSpec holds run-local-only overrides. The worker never reads this;
// it exists so a job can declare how it should be executed on a laptop.
type RunLocalSpec struct {
	// AsRunner runs the job container as the image's conventional runner uid
	// (1001:1001), matching the worker, instead of the host uid. This gives
	// sudo and HOME parity for jobs that rely on the image's runner user.
	AsRunner bool `json:"as_runner" yaml:"as_runner"`

	// User pins an explicit uid[:gid] for the job container (e.g.
	// "1001:1001"). Takes precedence over AsRunner when both are set. Empty
	// means unset.
	User string `json:"user" yaml:"user"`
}

// SourceSpec defines source code preparation
type SourceSpec struct {
	Type string `json:"type" yaml:"type"` // git, copy, none
	URL  string `json:"url" yaml:"url"`
	Ref  string `json:"ref" yaml:"ref"`
	Path string `json:"path" yaml:"path"` // for copy type
}

type CheckoutSpec struct {
	Mode string `json:"mode" yaml:"mode"`
}

func validateCheckoutSpec(checkout *CheckoutSpec) error {
	if checkout == nil || checkout.Mode == "" {
		return nil
	}
	if checkout.Mode != "isolated" && checkout.Mode != "shared" {
		return fmt.Errorf("checkout.mode must be isolated or shared")
	}
	return nil
}

// normalizeEvalFormat checks if data uses the eval format (nested "job" block
// with triggers/description at top level) and flattens it to the flat JobSpec
// format. Returns the original data unchanged if already in flat format.
func normalizeEvalFormat(data []byte, isYAML bool) []byte {
	var raw map[string]interface{}
	var err error
	if isYAML {
		err = yaml.Unmarshal(data, &raw)
	} else {
		err = json.Unmarshal(data, &raw)
	}
	if err != nil || raw == nil {
		return data
	}

	jobRaw, hasJob := raw["job"]
	if !hasJob {
		return data
	}

	jobMap, ok := jobRaw.(map[string]interface{})
	if !ok {
		return data
	}

	// Pull fields from job block to top level
	for key, val := range jobMap {
		switch key {
		case "timeout":
			// Map eval "timeout" to JobSpec "timeout_seconds"
			raw["timeout_seconds"] = val
		case "priority":
			// Not part of JobSpec, skip
		default:
			raw[key] = val
		}
	}

	// Remove eval-only keys that aren't part of JobSpec
	delete(raw, "job")
	delete(raw, "triggers")
	delete(raw, "description")
	delete(raw, "paths")

	// Re-marshal to pass through normal parsing
	var result []byte
	if isYAML {
		result, err = yaml.Marshal(raw)
	} else {
		result, err = json.Marshal(raw)
	}
	if err != nil {
		return data
	}
	return result
}

// LoadJobSpec reads a job specification from a YAML or JSON file. Supports
// both flat format (image/command at top level) and eval format
// (image/command nested under a "job" block with triggers/description).
func LoadJobSpec(path string) (*JobSpec, error) {
	spec, err := LoadJobSpecPartial(path)
	if err != nil {
		return nil, err
	}
	if err := FinalizeJobSpec(spec); err != nil {
		return nil, err
	}
	return spec, nil
}

// LoadJobSpecPartial reads a job without applying the built-in image or
// command requirements. Local contexts use this form before they apply
// project defaults and command-line overlays.
func LoadJobSpecPartial(path string) (*JobSpec, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read job file: %w", err)
	}

	ext := strings.ToLower(filepath.Ext(path))
	isYAML := ext == ".yaml" || ext == ".yml"

	// Normalize eval format (nested job block) to flat format
	data = normalizeEvalFormat(data, isYAML)

	var spec JobSpec

	if isYAML {
		if err := yaml.Unmarshal(data, &spec); err != nil {
			return nil, fmt.Errorf("failed to parse YAML: %w", err)
		}
	} else {
		if err := json.Unmarshal(data, &spec); err != nil {
			return nil, fmt.Errorf("failed to parse JSON: %w", err)
		}
	}

	if spec.Name == "" {
		spec.Name = filepath.Base(path)
	}
	if err := validateCheckoutSpec(spec.Checkout); err != nil {
		return nil, err
	}
	return &spec, nil
}

// FinalizeJobSpec applies built-in defaults and validates a resolved job.
func FinalizeJobSpec(spec *JobSpec) error {
	if spec.Image == "" {
		spec.Image = DefaultRunnerImage
	}
	if spec.Command == "" {
		return fmt.Errorf("job must specify a command")
	}
	if err := validateCheckoutSpec(spec.Checkout); err != nil {
		return err
	}
	if err := ValidateImagePullSecretNames(spec.ImagePullSecrets); err != nil {
		return err
	}
	return nil
}

const defaultCodeDir = "/job/src"

func defaultJobCodeDir(codeDir string) string {
	if codeDir == "" {
		return defaultCodeDir
	}
	return codeDir
}

func DefaultJobCodeDir(codeDir string) string {
	return defaultJobCodeDir(codeDir)
}

func defaultJobDir(codeDir, jobDir string) string {
	if jobDir != "" {
		return jobDir
	}
	return defaultJobCodeDir(codeDir)
}

func DefaultJobDir(codeDir, jobDir string) string {
	return defaultJobDir(codeDir, jobDir)
}

func containerPathInsideJob(path string) string {
	path = strings.TrimSpace(path)
	if path == "" || path == "/job" {
		return "."
	}
	if strings.HasPrefix(path, "/job/") {
		return strings.TrimPrefix(path, "/job/")
	}
	return strings.TrimPrefix(path, "/")
}

func ContainerPathInsideJob(path string) string {
	return containerPathInsideJob(path)
}

// ShellPrefix returns the shell prefix of the job command (see
// ResolveShellPrefix).
func (s *JobSpec) ShellPrefix() string {
	return ResolveShellPrefix(s.CommandPrefix, s.Environment[ShellPrefixEnvVar], OSFromCharacteristics(s.Characteristics))
}

// ToJobConfig converts a JobSpec to a JobConfig for execution workspaceDir is
// the host directory to mount into the container jobID is a unique identifier
// for this job execution
func (s *JobSpec) ToJobConfig(workspaceDir, jobID, queueName string) *JobConfig {
	// Parse command into args. The prefix is command_prefix, else
	// REACTORCIDE_JOB_SHELLCMD, else the default for the job os.
	command := ParseCommandWithPrefix(s.Command, s.ShellPrefix())

	// Build environment
	env := make(map[string]string)
	for k, v := range s.Environment {
		env[k] = v
	}
	if s.Checkout != nil && s.Checkout.Mode != "" {
		env["REACTORCIDE_CHECKOUT_MODE"] = s.Checkout.Mode
	}

	// Add job metadata to environment
	env["REACTORCIDE_JOB_ID"] = jobID
	if queueName != "" {
		env["REACTORCIDE_QUEUE"] = queueName
	}

	codeDir := defaultJobCodeDir(s.CodeDir)
	jobDir := defaultJobDir(s.CodeDir, s.JobDir)
	workingDir := jobDir
	if s.WorkingDir != "" {
		workingDir = s.WorkingDir
	}
	runAsUser := ""
	var runAsAdministrator *bool
	if s.RunAs != nil {
		runAsUser, _ = NormalizeRunAsUser(s.RunAs.User)
		if s.RunAs.Administrator != nil {
			v := *s.RunAs.Administrator
			runAsAdministrator = &v
		}
	}

	// Add triggers file path
	env["REACTORCIDE_TRIGGERS_FILE"] = "/job/triggers.json"

	// Standard container environment: tell runnerlib it's running inside a
	// container and where source code is mounted. True for both local and
	// production containers.
	env["REACTORCIDE_IN_CONTAINER"] = "true"
	env["REACTORCIDE_CODE_DIR"] = codeDir
	env["REACTORCIDE_JOB_DIR"] = jobDir

	config := &JobConfig{
		Image:            s.Image,
		Command:          command,
		Env:              env,
		WorkspaceDir:     workspaceDir,
		SourceMountPath:  codeDir,
		WorkingDir:       workingDir,
		Capabilities:     s.Capabilities,
		ImagePullSecrets: s.ImagePullSecrets,
		TimeoutSeconds:   s.TimeoutSeconds,
		CPULimit:         s.CPULimit,
		MemoryLimit:      s.MemoryLimit,
		JobID:            jobID,
		QueueName:        queueName,
		RunAsUser:        runAsUser,

		RunAsAdministrator: runAsAdministrator,
	}

	return config
}

// DefaultShellPrefix is the shell prefix of a job that does not set one
// and that does not run on Windows.
const DefaultShellPrefix = "sh -c"

// WindowsShellPrefix is the shell prefix of a Windows job (characteristic
// os: windows) that does not set one. Windows guests have no /bin/sh.
const WindowsShellPrefix = "powershell.exe -NoProfile -NonInteractive -Command"

// DirectCommandPrefix is the command_prefix / REACTORCIDE_JOB_SHELLCMD
// value that runs the command without a shell: the worker splits the
// command into words and runs the first word. Use it for an image that
// has no shell (for example a distroless image).
const DirectCommandPrefix = "none"

// ShellPrefixEnvVar is the job environment variable that sets the shell
// prefix of the job command.
const ShellPrefixEnvVar = "REACTORCIDE_JOB_SHELLCMD"

// DefaultShellPrefixForOS returns the shell prefix of a job with the os
// characteristic osName when the job sets no prefix.
func DefaultShellPrefixForOS(osName string) string {
	if strings.EqualFold(strings.TrimSpace(osName), "windows") {
		return WindowsShellPrefix
	}
	return DefaultShellPrefix
}

// ResolveShellPrefix returns the shell prefix of a job: the job spec's
// command_prefix, else the job environment's REACTORCIDE_JOB_SHELLCMD,
// else the default for the job's os characteristic.
func ResolveShellPrefix(commandPrefix, envPrefix, osName string) string {
	if p := strings.TrimSpace(commandPrefix); p != "" {
		return p
	}
	if p := strings.TrimSpace(envPrefix); p != "" {
		return p
	}
	return DefaultShellPrefixForOS(osName)
}

// OSFromCharacteristics returns the os characteristic of a raw job
// characteristic map, or "" when it has none.
func OSFromCharacteristics(chars map[string]interface{}) string {
	if v, ok := chars["os"].(string); ok {
		return v
	}
	return ""
}

// shellPrefixes are command prefixes that indicate the command is already
// wrapped for shell execution and shouldn't be double-wrapped.
var shellPrefixes = []string{
	"sh -c",
	"bash -c",
	"zsh -c",
	"/bin/sh -c",
	"/bin/bash -c",
	"/bin/zsh -c",
	"/usr/bin/sh -c",
	"/usr/bin/bash -c",
	"/usr/bin/zsh -c",
	"powershell ",
	"powershell.exe ",
	"pwsh ",
	"pwsh.exe ",
	"cmd /c ",
	"cmd.exe /c ",
}

// startsWithShell reports whether cmd already starts with a shell
// invocation (see shellPrefixes).
func startsWithShell(cmd string) bool {
	cmdLower := strings.ToLower(cmd)
	for _, shellPrefix := range shellPrefixes {
		if strings.HasPrefix(cmdLower, shellPrefix) {
			return true
		}
	}
	return false
}

// ParseCommandWithPrefix converts a command string to []string for container
// execution. Every command goes through the shell prefix (default
// DefaultShellPrefix), single-line and multi-line, so shell syntax such as
// `;`, `|`, `&&`, and `$VAR` works in each one. Two exceptions:
//   - A command that already starts with a shell invocation (`sh -c '...'`,
//     `bash -c`, `powershell.exe ...`, see shellPrefixes) is split into
//     words and not wrapped again.
//   - The prefix DirectCommandPrefix ("none") splits the command into words
//     and runs it without a shell, for an image that has no shell.
func ParseCommandWithPrefix(cmd, prefix string) []string {
	cmd = strings.TrimSpace(cmd)
	prefix = strings.TrimSpace(prefix)

	if strings.EqualFold(prefix, DirectCommandPrefix) || startsWithShell(cmd) {
		return parseSimpleCommand(cmd)
	}
	if prefix == "" {
		prefix = DefaultShellPrefix
	}
	return wrapWithShellPrefix(prefix, cmd)
}

// wrapWithShellPrefix splits prefix (REACTORCIDE_JOB_SHELLCMD or a job
// spec's command_prefix) into all of its whitespace-separated words and
// appends cmd as the last argument. A prefix such as "powershell.exe
// -NoProfile -NonInteractive -Command" thus gives each flag as its own
// argument. A prefix with fewer than two words (no flag) falls back to
// "sh -c".
func wrapWithShellPrefix(prefix, cmd string) []string {
	parts := splitShellPrefix(prefix)
	if len(parts) < 2 {
		return []string{"sh", "-c", cmd}
	}
	return append(parts, cmd)
}

// splitShellPrefix splits a shell prefix on whitespace. Single or double
// quotes group a word that has spaces ("C:/Program Files/.../pwsh.exe").
// A backslash is a literal character, NOT an escape, so a Windows path
// such as C:\Windows\System32\...\powershell.exe stays intact.
func splitShellPrefix(prefix string) []string {
	var words []string
	var current strings.Builder
	var quote rune
	inWord := false
	for _, r := range prefix {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
			} else {
				current.WriteRune(r)
			}
		case r == '"' || r == '\'':
			quote = r
			inWord = true
		case r == ' ' || r == '\t' || r == '\n' || r == '\r':
			if inWord {
				words = append(words, current.String())
				current.Reset()
				inWord = false
			}
		default:
			current.WriteRune(r)
			inWord = true
		}
	}
	if inWord {
		words = append(words, current.String())
	}
	return words
}

// ParseCommand converts a command string for container execution with the
// default prefix DefaultShellPrefix (see ParseCommandWithPrefix).
func ParseCommand(cmd string) []string {
	return ParseCommandWithPrefix(cmd, "")
}

// parseSimpleCommand splits a single-line command on whitespace, respecting
// basic shell quoting rules.
func parseSimpleCommand(cmd string) []string {
	var args []string
	var current strings.Builder
	inSingleQuote := false
	inDoubleQuote := false
	escaped := false

	for _, r := range cmd {
		if escaped {
			current.WriteRune(r)
			escaped = false
			continue
		}

		switch r {
		case '\\':
			if inSingleQuote {
				current.WriteRune(r)
			} else {
				escaped = true
			}
		case '\'':
			if inDoubleQuote {
				current.WriteRune(r)
			} else {
				inSingleQuote = !inSingleQuote
			}
		case '"':
			if inSingleQuote {
				current.WriteRune(r)
			} else {
				inDoubleQuote = !inDoubleQuote
			}
		case ' ', '\t':
			if inSingleQuote || inDoubleQuote {
				current.WriteRune(r)
			} else if current.Len() > 0 {
				args = append(args, current.String())
				current.Reset()
			}
		default:
			current.WriteRune(r)
		}
	}

	if current.Len() > 0 {
		args = append(args, current.String())
	}

	return args
}

// SecretRefPattern matches ${secret:path:key} references in strings
var SecretRefPattern = regexp.MustCompile(`\$\{secret:([^:}]+):([^}]+)\}`)

// EnvRefPattern matches ${env:VAR_NAME} references in strings This allows job
// YAMLs to reference host environment variables
var EnvRefPattern = regexp.MustCompile(`\$\{env:([^}]+)\}`)

// HasSecretRefs checks if a string contains secret references
func HasSecretRefs(s string) bool {
	return SecretRefPattern.MatchString(s)
}

// HasEnvRefs checks if a string contains environment variable references
func HasEnvRefs(s string) bool {
	return EnvRefPattern.MatchString(s)
}

// ResolveEnvRefs resolves ${env:VAR_NAME} references in a string using
// os.Getenv to get values from the host environment
func ResolveEnvRefs(value string) string {
	result := value
	matches := EnvRefPattern.FindAllStringSubmatch(value, -1)

	for _, match := range matches {
		if len(match) != 2 {
			continue
		}
		fullMatch := match[0]
		varName := match[1]
		envValue := os.Getenv(varName)
		result = strings.Replace(result, fullMatch, envValue, 1)
	}

	return result
}

// ResolveSecretRefs resolves ${secret:path:key} references in a string using
// the provided getter function
func ResolveSecretRefs(value string, getSecret func(path, key string) (string, error)) (string, error) {
	result := value
	matches := SecretRefPattern.FindAllStringSubmatch(value, -1)

	for _, match := range matches {
		if len(match) != 3 {
			continue
		}
		fullMatch := match[0]
		path := match[1]
		key := match[2]

		secretValue, err := getSecret(path, key)
		if err != nil {
			return "", fmt.Errorf("failed to get secret %s:%s: %w", path, key, err)
		}
		if secretValue == "" {
			return "", fmt.Errorf("secret not found: %s:%s", path, key)
		}

		result = strings.Replace(result, fullMatch, secretValue, 1)
	}

	return result, nil
}

// ResolveEnvInMap resolves ${env:VAR_NAME} references in all values of a map
func ResolveEnvInMap(env map[string]string) map[string]string {
	resolved := make(map[string]string)
	for k, v := range env {
		resolved[k] = ResolveEnvRefs(v)
	}
	return resolved
}

// SecretResolutionResult holds the results of resolving secrets in environment variables
type SecretResolutionResult struct {
	// Resolved contains all environment variables with secrets resolved
	Resolved map[string]string
	// SecretValues contains the actual secret values for masking
	SecretValues []string
	// SecretEnvNames contains the names of env vars that contained secret references
	SecretEnvNames []string
}

// ResolveSecretsInEnv resolves all secret references in environment variables
// Returns a new map with resolved values, a list of resolved secret values
// for masking, and a list of env var names that contained secrets. Note:
// ${env:VAR} references should be resolved first using ResolveEnvInMap
func ResolveSecretsInEnv(env map[string]string, getSecret func(path, key string) (string, error)) (map[string]string, []string, error) {
	result, err := ResolveSecretsInEnvFull(env, getSecret)
	if err != nil {
		return nil, nil, err
	}
	return result.Resolved, result.SecretValues, nil
}

// ResolveSecretsInEnvFull resolves all secret references and returns full result including env var names
func ResolveSecretsInEnvFull(env map[string]string, getSecret func(path, key string) (string, error)) (*SecretResolutionResult, error) {
	result := &SecretResolutionResult{
		Resolved: make(map[string]string),
	}

	for k, v := range env {
		if HasSecretRefs(v) {
			resolvedValue, err := ResolveSecretRefs(v, getSecret)
			if err != nil {
				return nil, fmt.Errorf("failed to resolve secret in %s: %w", k, err)
			}
			result.Resolved[k] = resolvedValue
			// Track the resolved value for masking
			result.SecretValues = append(result.SecretValues, resolvedValue)
			// Track the env var name that contained a secret
			result.SecretEnvNames = append(result.SecretEnvNames, k)
		} else {
			result.Resolved[k] = v
		}
	}

	return result, nil
}

// LoadJobSpecOverlay reads a partial job specification from a YAML/JSON file
// Unlike LoadJobSpec, this doesn't require command or set defaults
func LoadJobSpecOverlay(path string) (*JobSpec, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read overlay file %s: %w", path, err)
	}

	var spec JobSpec

	ext := strings.ToLower(filepath.Ext(path))
	if ext == ".yaml" || ext == ".yml" {
		if err := yaml.Unmarshal(data, &spec); err != nil {
			return nil, fmt.Errorf("failed to parse YAML in %s: %w", path, err)
		}
	} else {
		if err := json.Unmarshal(data, &spec); err != nil {
			return nil, fmt.Errorf("failed to parse JSON in %s: %w", path, err)
		}
	}

	return &spec, nil
}

// SecretOverride represents a case where a secret reference was overridden
type SecretOverride struct {
	Key         string
	OldValue    string // The ${secret:...} reference
	NewValue    string // The plaintext value
	OverlayFile string
}

// MergeJobSpecs merges overlay specs onto a base spec. Overlays are applied
// in order, with later overlays taking precedence. Returns the merged spec
// and any warnings about secret overrides.
func MergeJobSpecs(base *JobSpec, overlays []*JobSpec, overlayFiles []string) (*JobSpec, []SecretOverride) {
	result := &JobSpec{
		Name:            base.Name,
		Command:         base.Command,
		CommandPrefix:   base.CommandPrefix,
		Image:           base.Image,
		WorkingDir:      base.WorkingDir,
		CodeDir:         base.CodeDir,
		JobDir:          base.JobDir,
		TimeoutSeconds:  base.TimeoutSeconds,
		CPULimit:        base.CPULimit,
		MemoryLimit:     base.MemoryLimit,
		WorkerClass:     base.WorkerClass,
		DisableRunLocal: base.DisableRunLocal,
	}

	// Deep copy characteristics/resources (whole-map replace on overlay, like
	// Capabilities below -- these are validated as a unit by
	// internal/characteristics.ParseJobCharacteristics/
	// internal/resources.ParseResources, so a partial per-key merge would
	// risk producing a combination neither the base nor the overlay author
	// intended).
	if len(base.Characteristics) > 0 {
		result.Characteristics = make(map[string]interface{}, len(base.Characteristics))
		for k, v := range base.Characteristics {
			result.Characteristics[k] = v
		}
	}
	if len(base.Resources) > 0 {
		result.Resources = make(map[string]interface{}, len(base.Resources))
		for k, v := range base.Resources {
			result.Resources[k] = v
		}
	}

	// Deep copy environment
	result.Environment = make(map[string]string)
	for k, v := range base.Environment {
		result.Environment[k] = v
	}

	// Deep copy capabilities
	if len(base.Capabilities) > 0 {
		result.Capabilities = make([]string, len(base.Capabilities))
		copy(result.Capabilities, base.Capabilities)
	}

	// Deep copy image pull secret names (whole-list replace on overlay, like
	// Capabilities)
	if len(base.ImagePullSecrets) > 0 {
		result.ImagePullSecrets = make([]string, len(base.ImagePullSecrets))
		copy(result.ImagePullSecrets, base.ImagePullSecrets)
	}

	// Deep copy source
	if base.Source != nil {
		result.Source = &SourceSpec{
			Type: base.Source.Type,
			URL:  base.Source.URL,
			Ref:  base.Source.Ref,
			Path: base.Source.Path,
		}
	}
	if base.Checkout != nil {
		result.Checkout = &CheckoutSpec{Mode: base.Checkout.Mode}
	}

	// Deep copy run-local block
	if base.RunLocal != nil {
		result.RunLocal = &RunLocalSpec{
			AsRunner: base.RunLocal.AsRunner,
			User:     base.RunLocal.User,
		}
	}
	result.RunAs = copyRunAsSpec(base.RunAs)

	var secretOverrides []SecretOverride

	// Apply each overlay in order
	for i, overlay := range overlays {
		overlayFile := ""
		if i < len(overlayFiles) {
			overlayFile = overlayFiles[i]
		}

		// Override scalar fields if set in overlay
		if overlay.Name != "" {
			result.Name = overlay.Name
		}
		if overlay.Command != "" {
			result.Command = overlay.Command
		}
		if overlay.CommandPrefix != "" {
			result.CommandPrefix = overlay.CommandPrefix
		}
		if overlay.Image != "" {
			result.Image = overlay.Image
		}
		if overlay.WorkingDir != "" {
			result.WorkingDir = overlay.WorkingDir
		}
		if overlay.CodeDir != "" {
			result.CodeDir = overlay.CodeDir
		}
		if overlay.JobDir != "" {
			result.JobDir = overlay.JobDir
		}
		if overlay.TimeoutSeconds != 0 {
			result.TimeoutSeconds = overlay.TimeoutSeconds
		}
		if overlay.CPULimit != "" {
			result.CPULimit = overlay.CPULimit
		}
		if overlay.MemoryLimit != "" {
			result.MemoryLimit = overlay.MemoryLimit
		}
		if overlay.WorkerClass != "" {
			result.WorkerClass = overlay.WorkerClass
		}
		if overlay.DisableRunLocal {
			result.DisableRunLocal = true
		}
		if overlay.RunAs != nil {
			// run_as merges field by field: an overlay changes only the
			// fields that it sets.
			merged := &RunAsSpec{User: overlay.RunAs.User}
			if merged.User == "" && result.RunAs != nil {
				merged.User = result.RunAs.User
			}
			if result.RunAs != nil && result.RunAs.Administrator != nil {
				v := *result.RunAs.Administrator
				merged.Administrator = &v
			}
			if overlay.RunAs.Administrator != nil {
				v := *overlay.RunAs.Administrator
				merged.Administrator = &v
			}
			result.RunAs = merged
		}

		// Merge environment (overlay values take precedence)
		for k, v := range overlay.Environment {
			oldValue, exists := result.Environment[k]
			if exists && HasSecretRefs(oldValue) && !HasSecretRefs(v) {
				// Warning: overriding a secret reference with plaintext
				secretOverrides = append(secretOverrides, SecretOverride{
					Key:         k,
					OldValue:    oldValue,
					NewValue:    v,
					OverlayFile: overlayFile,
				})
			}
			result.Environment[k] = v
		}

		// Override capabilities if set in overlay
		if len(overlay.Capabilities) > 0 {
			result.Capabilities = make([]string, len(overlay.Capabilities))
			copy(result.Capabilities, overlay.Capabilities)
		}

		// Override image pull secret names if set in overlay; an omitted
		// field keeps the base list
		if len(overlay.ImagePullSecrets) > 0 {
			result.ImagePullSecrets = make([]string, len(overlay.ImagePullSecrets))
			copy(result.ImagePullSecrets, overlay.ImagePullSecrets)
		}

		// Override characteristics/resources (whole-map replace) if set in
		// overlay -- see the deep-copy comment above for why these aren't
		// merged key-by-key.
		if len(overlay.Characteristics) > 0 {
			result.Characteristics = make(map[string]interface{}, len(overlay.Characteristics))
			for k, v := range overlay.Characteristics {
				result.Characteristics[k] = v
			}
		}
		if len(overlay.Resources) > 0 {
			result.Resources = make(map[string]interface{}, len(overlay.Resources))
			for k, v := range overlay.Resources {
				result.Resources[k] = v
			}
		}

		// Override source if set in overlay
		if overlay.Source != nil {
			result.Source = &SourceSpec{
				Type: overlay.Source.Type,
				URL:  overlay.Source.URL,
				Ref:  overlay.Source.Ref,
				Path: overlay.Source.Path,
			}
		}
		if overlay.Checkout != nil {
			result.Checkout = &CheckoutSpec{Mode: overlay.Checkout.Mode}
		}

		// Override run-local block if set in overlay
		if overlay.RunLocal != nil {
			result.RunLocal = &RunLocalSpec{
				AsRunner: overlay.RunLocal.AsRunner,
				User:     overlay.RunLocal.User,
			}
		}
	}

	return result, secretOverrides
}

// LoadJobSpecWithOverlays loads a job spec and applies overlay files in
// order. The overlay files are specified from highest to lowest priority
// (first file wins). Returns the merged spec and any warnings about secret
// overrides.
func LoadJobSpecWithOverlays(jobPath string, overlayPaths []string) (*JobSpec, []SecretOverride, error) {
	// Load base job spec
	base, err := LoadJobSpecPartial(jobPath)
	if err != nil {
		return nil, nil, err
	}

	if len(overlayPaths) == 0 {
		if err := FinalizeJobSpec(base); err != nil {
			return nil, nil, err
		}
		return base, nil, nil
	}

	// Load overlays (reverse order so first specified has highest priority)
	var overlays []*JobSpec
	var overlayFiles []string
	for i := len(overlayPaths) - 1; i >= 0; i-- {
		overlay, err := LoadJobSpecOverlay(overlayPaths[i])
		if err != nil {
			return nil, nil, err
		}
		overlays = append(overlays, overlay)
		overlayFiles = append(overlayFiles, overlayPaths[i])
	}

	// Merge overlays onto base
	merged, secretOverrides := MergeJobSpecs(base, overlays, overlayFiles)
	if err := validateCheckoutSpec(merged.Checkout); err != nil {
		return nil, nil, err
	}

	if err := FinalizeJobSpec(merged); err != nil {
		return nil, nil, err
	}

	return merged, secretOverrides, nil
}
