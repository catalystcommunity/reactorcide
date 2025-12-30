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
// or constructed programmatically. This is the canonical representation of a job
// that gets converted to JobConfig for execution.
type JobSpec struct {
	// Name is a human-readable name for the job
	Name string `json:"name" yaml:"name"`

	// Command is the full command to execute.
	// Single-line commands are parsed and split on whitespace.
	// Multiline commands are wrapped with "sh -c" by default (see CommandPrefix).
	Command string `json:"command" yaml:"command"`

	// CommandPrefix overrides the default shell wrapper for multiline commands.
	// Default is "sh -c". Examples: "bash -c", "zsh -c", "/bin/ash -c"
	// Only applies to multiline commands that don't already start with a shell invocation.
	CommandPrefix string `json:"command_prefix" yaml:"command_prefix"`

	// Image is the container image to use (defaults to DefaultRunnerImage)
	Image string `json:"image" yaml:"image"`

	// Environment variables to set in the container
	// Values can contain ${secret:path:key} references that get resolved
	Environment map[string]string `json:"environment" yaml:"environment"`

	// Source defines how to prepare the source code
	Source *SourceSpec `json:"source" yaml:"source"`

	// WorkingDir is the working directory inside the container (default: /job)
	WorkingDir string `json:"working_dir" yaml:"working_dir"`

	// Capabilities declares what the job needs from the runtime environment.
	// The runner interprets these based on its environment:
	//   - "docker": Access to build/push container images
	//               DockerRunner: mounts /var/run/docker.sock
	//               KubernetesRunner: uses DinD sidecar or hostPath
	//   - "gpu": Access to GPU resources (future - not yet implemented)
	//            DockerRunner: --gpus all
	//            KubernetesRunner: nvidia.com/gpu resource request
	Capabilities []string `json:"capabilities" yaml:"capabilities"`

	// Timeout in seconds (0 = no timeout)
	TimeoutSeconds int `json:"timeout_seconds" yaml:"timeout_seconds"`

	// Resource limits
	CPULimit    string `json:"cpu_limit" yaml:"cpu_limit"`
	MemoryLimit string `json:"memory_limit" yaml:"memory_limit"`
}

// SourceSpec defines source code preparation
type SourceSpec struct {
	Type string `json:"type" yaml:"type"` // git, copy, none
	URL  string `json:"url" yaml:"url"`
	Ref  string `json:"ref" yaml:"ref"`
	Path string `json:"path" yaml:"path"` // for copy type
}

// LoadJobSpec reads a job specification from a YAML or JSON file
func LoadJobSpec(path string) (*JobSpec, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read job file: %w", err)
	}

	var spec JobSpec

	ext := strings.ToLower(filepath.Ext(path))
	if ext == ".yaml" || ext == ".yml" {
		if err := yaml.Unmarshal(data, &spec); err != nil {
			return nil, fmt.Errorf("failed to parse YAML: %w", err)
		}
	} else {
		if err := json.Unmarshal(data, &spec); err != nil {
			return nil, fmt.Errorf("failed to parse JSON: %w", err)
		}
	}

	// Set defaults
	if spec.Name == "" {
		spec.Name = filepath.Base(path)
	}
	if spec.Image == "" {
		spec.Image = DefaultRunnerImage
	}
	if spec.Command == "" {
		return nil, fmt.Errorf("job file must specify a command")
	}

	return &spec, nil
}

// ToJobConfig converts a JobSpec to a JobConfig for execution
// workspaceDir is the host directory to mount into the container
// jobID is a unique identifier for this job execution
func (s *JobSpec) ToJobConfig(workspaceDir, jobID, queueName string) *JobConfig {
	// Parse command into args, using CommandPrefix for multiline commands
	command := ParseCommandWithPrefix(s.Command, s.CommandPrefix)

	// Build environment
	env := make(map[string]string)
	for k, v := range s.Environment {
		env[k] = v
	}

	// Add job metadata to environment
	env["REACTORCIDE_JOB_ID"] = jobID
	if queueName != "" {
		env["REACTORCIDE_QUEUE"] = queueName
	}

	// Add triggers file path
	env["REACTORCIDE_TRIGGERS_FILE"] = "/job/triggers.json"

	config := &JobConfig{
		Image:          s.Image,
		Command:        command,
		Env:            env,
		WorkspaceDir:   workspaceDir,
		WorkingDir:     s.WorkingDir,
		Capabilities:   s.Capabilities,
		TimeoutSeconds: s.TimeoutSeconds,
		CPULimit:       s.CPULimit,
		MemoryLimit:    s.MemoryLimit,
		JobID:          jobID,
		QueueName:      queueName,
	}

	return config
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
}

// ParseCommandWithPrefix converts a command string to []string for container execution.
// For multiline commands, wraps with the specified prefix (default "sh -c").
// For single-line commands, splits on whitespace respecting basic quoting.
func ParseCommandWithPrefix(cmd, prefix string) []string {
	cmd = strings.TrimSpace(cmd)

	// Check if command is multiline
	if strings.Contains(cmd, "\n") {
		// Check if command already starts with a shell invocation
		cmdLower := strings.ToLower(cmd)
		for _, shellPrefix := range shellPrefixes {
			if strings.HasPrefix(cmdLower, shellPrefix) {
				// Already has shell prefix, parse normally
				// (this handles cases like "sh -c '\n script \n'")
				return parseSimpleCommand(cmd)
			}
		}

		// Wrap with shell prefix
		if prefix == "" {
			prefix = "sh -c"
		}

		// Parse the prefix to get shell and flag
		prefixParts := strings.SplitN(prefix, " ", 2)
		if len(prefixParts) == 2 {
			return []string{prefixParts[0], prefixParts[1], cmd}
		}
		// If prefix doesn't have a flag (unusual), just use sh -c
		return []string{"sh", "-c", cmd}
	}

	// Single-line command: parse normally
	return parseSimpleCommand(cmd)
}

// ParseCommand splits a command string for container execution.
// Uses default "sh -c" prefix for multiline commands.
func ParseCommand(cmd string) []string {
	return ParseCommandWithPrefix(cmd, "")
}

// parseSimpleCommand splits a single-line command on whitespace,
// respecting basic shell quoting rules.
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

// EnvRefPattern matches ${env:VAR_NAME} references in strings
// This allows job YAMLs to reference host environment variables
var EnvRefPattern = regexp.MustCompile(`\$\{env:([^}]+)\}`)

// HasSecretRefs checks if a string contains secret references
func HasSecretRefs(s string) bool {
	return SecretRefPattern.MatchString(s)
}

// HasEnvRefs checks if a string contains environment variable references
func HasEnvRefs(s string) bool {
	return EnvRefPattern.MatchString(s)
}

// ResolveEnvRefs resolves ${env:VAR_NAME} references in a string
// using os.Getenv to get values from the host environment
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

// ResolveSecretRefs resolves ${secret:path:key} references in a string
// using the provided getter function
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

// ResolveSecretsInEnv resolves all secret references in environment variables
// Returns a new map with resolved values and a list of resolved secret values for masking
// Note: ${env:VAR} references should be resolved first using ResolveEnvInMap
func ResolveSecretsInEnv(env map[string]string, getSecret func(path, key string) (string, error)) (map[string]string, []string, error) {
	resolved := make(map[string]string)
	var secretValues []string

	for k, v := range env {
		if HasSecretRefs(v) {
			resolvedValue, err := ResolveSecretRefs(v, getSecret)
			if err != nil {
				return nil, nil, fmt.Errorf("failed to resolve secret in %s: %w", k, err)
			}
			resolved[k] = resolvedValue
			// Track the resolved value for masking
			secretValues = append(secretValues, resolvedValue)
		} else {
			resolved[k] = v
		}
	}

	return resolved, secretValues, nil
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

// MergeJobSpecs merges overlay specs onto a base spec.
// Overlays are applied in order, with later overlays taking precedence.
// Returns the merged spec and any warnings about secret overrides.
func MergeJobSpecs(base *JobSpec, overlays []*JobSpec, overlayFiles []string) (*JobSpec, []SecretOverride) {
	result := &JobSpec{
		Name:           base.Name,
		Command:        base.Command,
		CommandPrefix:  base.CommandPrefix,
		Image:          base.Image,
		WorkingDir:     base.WorkingDir,
		TimeoutSeconds: base.TimeoutSeconds,
		CPULimit:       base.CPULimit,
		MemoryLimit:    base.MemoryLimit,
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

	// Deep copy source
	if base.Source != nil {
		result.Source = &SourceSpec{
			Type: base.Source.Type,
			URL:  base.Source.URL,
			Ref:  base.Source.Ref,
			Path: base.Source.Path,
		}
	}

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
		if overlay.TimeoutSeconds != 0 {
			result.TimeoutSeconds = overlay.TimeoutSeconds
		}
		if overlay.CPULimit != "" {
			result.CPULimit = overlay.CPULimit
		}
		if overlay.MemoryLimit != "" {
			result.MemoryLimit = overlay.MemoryLimit
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

		// Override source if set in overlay
		if overlay.Source != nil {
			result.Source = &SourceSpec{
				Type: overlay.Source.Type,
				URL:  overlay.Source.URL,
				Ref:  overlay.Source.Ref,
				Path: overlay.Source.Path,
			}
		}
	}

	return result, secretOverrides
}

// LoadJobSpecWithOverlays loads a job spec and applies overlay files in order.
// The overlay files are specified from highest to lowest priority (first file wins).
// Returns the merged spec and any warnings about secret overrides.
func LoadJobSpecWithOverlays(jobPath string, overlayPaths []string) (*JobSpec, []SecretOverride, error) {
	// Load base job spec
	base, err := LoadJobSpec(jobPath)
	if err != nil {
		return nil, nil, err
	}

	if len(overlayPaths) == 0 {
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

	// Apply defaults if not set after merge
	if merged.Image == "" {
		merged.Image = DefaultRunnerImage
	}
	if merged.Command == "" {
		return nil, nil, fmt.Errorf("job must specify a command (not set in job file or overlays)")
	}

	return merged, secretOverrides, nil
}
