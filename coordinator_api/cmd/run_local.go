package cmd

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/secrets"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/worker"
	"github.com/google/uuid"
	"github.com/urfave/cli/v2"
)

// RunLocalCommand executes a job in a container, emulating worker behavior.
// This uses the same JobRunner infrastructure as the worker, ensuring consistent
// execution between local development and production.
var RunLocalCommand = &cli.Command{
	Name:      "run-local",
	Usage:     "Execute a job in a container (emulates worker behavior)",
	ArgsUsage: "<job-file>",
	Flags: []cli.Flag{
		&cli.BoolFlag{
			Name:  "dry-run",
			Usage: "Show what would be executed without running",
		},
		&cli.StringFlag{
			Name:  "job-dir",
			Usage: "Job directory to mount into container (default: ./job)",
			Value: "./job",
		},
		&cli.StringFlag{
			Name:  "backend",
			Usage: "Container runtime backend: docker, containerd, kubernetes",
			Value: "docker",
		},
		&cli.StringSliceFlag{
			Name:    "input",
			Aliases: []string{"i"},
			Usage:   "Overlay YAML files to merge with job spec (first has highest priority)",
		},
		&cli.BoolFlag{
			Name:  "allow-secret-overrides",
			Usage: "Allow overlays to override secret references with plaintext values",
		},
	},
	Action: runLocalAction,
}

func runLocalAction(ctx *cli.Context) error {
	if ctx.NArg() < 1 {
		return fmt.Errorf("usage: reactorcide run-local <job-file>")
	}

	jobFile := ctx.Args().Get(0)
	dryRun := ctx.Bool("dry-run")
	jobDir := ctx.String("job-dir")
	backend := ctx.String("backend")
	inputFiles := ctx.StringSlice("input")
	allowSecretOverrides := ctx.Bool("allow-secret-overrides")

	// Load job specification with overlays
	spec, secretOverrides, err := worker.LoadJobSpecWithOverlays(jobFile, inputFiles)
	if err != nil {
		return err
	}

	// Check for secret overrides
	if len(secretOverrides) > 0 {
		for _, override := range secretOverrides {
			fmt.Fprintf(os.Stderr, "WARNING: %s overrides secret reference in %s with plaintext value\n",
				override.OverlayFile, override.Key)
		}
		if !allowSecretOverrides {
			return fmt.Errorf("secret references were overridden with plaintext values; use --allow-secret-overrides to proceed")
		}
	}

	fmt.Printf("Job: %s\n", spec.Name)
	fmt.Printf("Image: %s\n", spec.Image)
	fmt.Printf("Command: %s\n", spec.Command)

	// Resolve absolute path for job directory
	absJobDir, err := filepath.Abs(jobDir)
	if err != nil {
		return fmt.Errorf("failed to resolve job directory: %w", err)
	}

	// Ensure job directory exists
	if err := os.MkdirAll(absJobDir, 0755); err != nil {
		return fmt.Errorf("failed to create job directory: %w", err)
	}

	// First resolve ${env:VAR_NAME} references from host environment
	spec.Environment = worker.ResolveEnvInMap(spec.Environment)

	// Then resolve ${secret:path:key} references
	resolvedEnv, secretValues, err := resolveJobSecrets(spec.Environment)
	if err != nil {
		return err
	}
	spec.Environment = resolvedEnv

	// Create a masker for secret values
	masker := secrets.NewMasker()
	for _, sv := range secretValues {
		masker.RegisterSecret(sv)
	}
	// Also mask any values that look like secrets based on key names
	for k, v := range spec.Environment {
		if isSensitiveKey(k) {
			masker.RegisterSecret(v)
		}
	}

	// Generate a job ID for this execution
	jobID := uuid.New().String()[:8]

	// Convert spec to JobConfig
	jobConfig := spec.ToJobConfig(absJobDir, jobID, "local")

	if dryRun {
		return performLocalDryRun(spec, jobConfig, masker, absJobDir)
	}

	// Create the job runner
	runner, err := worker.NewJobRunner(backend)
	if err != nil {
		return fmt.Errorf("failed to create job runner: %w", err)
	}

	// Execute the job
	return executeLocalJob(context.Background(), runner, jobConfig, masker)
}

// resolveJobSecrets resolves ${secret:path:key} references in environment variables
func resolveJobSecrets(env map[string]string) (map[string]string, []string, error) {
	// Check if any env vars contain secret references
	hasSecrets := false
	for _, v := range env {
		if worker.HasSecretRefs(v) {
			hasSecrets = true
			break
		}
	}

	if !hasSecrets {
		return env, nil, nil
	}

	// Initialize secrets storage
	storage := secrets.NewStorage()
	if !storage.IsInitialized() {
		return nil, nil, fmt.Errorf("secrets storage not initialized, run 'reactorcide secrets init' first")
	}

	password, err := getPassword("Secrets password: ")
	if err != nil {
		return nil, nil, err
	}

	// Create getter function for secrets
	getSecret := func(path, key string) (string, error) {
		return storage.Get(path, key, password)
	}

	return worker.ResolveSecretsInEnv(env, getSecret)
}

// isSensitiveKey checks if an environment variable key suggests sensitive data
func isSensitiveKey(key string) bool {
	keyUpper := strings.ToUpper(key)
	sensitivePatterns := []string{"TOKEN", "SECRET", "PASSWORD", "KEY", "AUTH", "CREDENTIAL", "API_KEY"}
	for _, pattern := range sensitivePatterns {
		if strings.Contains(keyUpper, pattern) {
			return true
		}
	}
	return false
}

func performLocalDryRun(spec *worker.JobSpec, config *worker.JobConfig, masker *secrets.Masker, jobDir string) error {
	fmt.Println("\n--- DRY RUN MODE ---")
	fmt.Printf("Image: %s\n", spec.Image)
	fmt.Printf("Command: %s\n", spec.Command)
	fmt.Printf("Job directory: %s -> /job\n", jobDir)

	if spec.Source != nil && spec.Source.Type != "" && spec.Source.Type != "none" {
		fmt.Printf("Source: %s from %s (ref: %s)\n",
			spec.Source.Type, spec.Source.URL, spec.Source.Ref)
	}

	fmt.Println("\nEnvironment variables:")
	for k, v := range spec.Environment {
		masked := masker.MaskString(v)
		fmt.Printf("  %s=%s\n", k, masked)
	}

	if len(spec.Capabilities) > 0 {
		fmt.Println("\nCapabilities:")
		for _, cap := range spec.Capabilities {
			fmt.Printf("  - %s\n", cap)
		}
	}

	fmt.Println("\nJobConfig:")
	fmt.Printf("  Image: %s\n", config.Image)
	fmt.Printf("  Command: %v\n", config.Command)
	fmt.Printf("  WorkspaceDir: %s\n", config.WorkspaceDir)
	fmt.Printf("  WorkingDir: %s\n", config.WorkingDir)

	fmt.Println("\n--- END DRY RUN ---")
	return nil
}

func executeLocalJob(ctx context.Context, runner worker.JobRunner, config *worker.JobConfig, masker *secrets.Masker) error {
	fmt.Printf("\nRunning container: %s\n", config.Image)
	fmt.Println("---")

	// Spawn the job container
	containerID, err := runner.SpawnJob(ctx, config)
	if err != nil {
		return fmt.Errorf("failed to spawn container: %w", err)
	}

	// Ensure cleanup
	defer func() {
		if cleanupErr := runner.Cleanup(context.Background(), containerID); cleanupErr != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to cleanup container: %v\n", cleanupErr)
		}
	}()

	// Stream logs
	stdout, stderr, err := runner.StreamLogs(ctx, containerID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to stream logs: %v\n", err)
	}

	// Stream output with masking
	done := make(chan struct{}, 2)

	if stdout != nil {
		go func() {
			defer stdout.Close()
			scanner := bufio.NewScanner(stdout)
			for scanner.Scan() {
				line := masker.MaskString(scanner.Text())
				fmt.Println(line)
			}
			done <- struct{}{}
		}()
	} else {
		done <- struct{}{}
	}

	if stderr != nil {
		go func() {
			defer stderr.Close()
			scanner := bufio.NewScanner(stderr)
			for scanner.Scan() {
				line := masker.MaskString(scanner.Text())
				fmt.Fprintln(os.Stderr, line)
			}
			done <- struct{}{}
		}()
	} else {
		done <- struct{}{}
	}

	// Wait for log streaming to finish
	<-done
	<-done

	// Wait for completion
	exitCode, err := runner.WaitForCompletion(ctx, containerID)
	fmt.Println("---")

	if err != nil {
		return fmt.Errorf("job execution error: %w", err)
	}

	// Check for triggered jobs
	triggersFile := filepath.Join(config.WorkspaceDir, "triggers.json")
	if _, statErr := os.Stat(triggersFile); statErr == nil {
		data, readErr := os.ReadFile(triggersFile)
		if readErr == nil && len(data) > 0 {
			fmt.Printf("\nTriggered jobs written to: %s\n", triggersFile)
		}
	}

	if exitCode != 0 {
		return cli.Exit(fmt.Sprintf("Job failed with exit code %d", exitCode), exitCode)
	}

	fmt.Println("Job completed successfully")
	return nil
}
