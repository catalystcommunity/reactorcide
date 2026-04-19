package worker

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"

	"github.com/catalystcommunity/app-utils-go/logging"
)

const (
	// containerdNamespace is the namespace used for reactorcide containers
	containerdNamespace = "reactorcide"
	// nerdctlBinary is the path to the nerdctl binary
	nerdctlBinary = "nerdctl"
)

// containerProcess holds information about a running container
type containerProcess struct {
	containerID  string
	cmd          *exec.Cmd
	stdoutReader *io.PipeReader
	stdoutWriter *io.PipeWriter
	stderrReader *io.PipeReader
	stderrWriter *io.PipeWriter
	done         chan struct{}
	exitCode     int
	exitErr      error
}

// ContainerdRunner implements JobRunner using nerdctl CLI
// This approach lets nerdctl handle networking (CNI) automatically
type ContainerdRunner struct {
	// containers stores running container processes by container ID
	containers map[string]*containerProcess
	mu         sync.RWMutex

	builder BuilderConfig

	// sidecars maps a job container ID (the nerdctl container name we use)
	// to its builder sidecar container name, so Cleanup can remove both.
	sidecars   map[string]string
	sidecarsMu sync.Mutex
}

// NewContainerdRunner creates a new nerdctl-based job runner
func NewContainerdRunner() (*ContainerdRunner, error) {
	// Verify nerdctl is available
	cmd := exec.Command(nerdctlBinary, "version")
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("nerdctl not available: %w", err)
	}

	logging.Log.Info("nerdctl runner initialized")

	cr := &ContainerdRunner{
		containers: make(map[string]*containerProcess),
		builder:    LoadBuilderConfig(),
		sidecars:   make(map[string]string),
	}
	cr.sweepLeaked(context.Background())
	return cr, nil
}

// sweepLeaked removes any job/sidecar containers left behind by a previous
// worker run. Same assumptions as DockerRunner.sweepLeaked.
func (cr *ContainerdRunner) sweepLeaked(ctx context.Context) {
	logger := logging.Log
	for _, component := range []string{"builder-sidecar", "job-container"} {
		out, err := exec.CommandContext(ctx, nerdctlBinary,
			"--namespace", containerdNamespace,
			"ps", "-a", "-q",
			"--filter", "label=reactorcide.component="+component,
		).Output()
		if err != nil {
			logger.WithError(err).Warn("Failed to list leaked containers for sweep")
			continue
		}
		for _, id := range strings.Fields(string(out)) {
			rmOut, rmErr := exec.CommandContext(ctx, nerdctlBinary,
				"--namespace", containerdNamespace, "rm", "-f", id,
			).CombinedOutput()
			if rmErr != nil {
				if !strings.Contains(string(rmOut), "not found") && !strings.Contains(string(rmOut), "No such container") {
					logger.WithError(rmErr).WithField("container_id", id).Warn("Failed to sweep leaked container")
					continue
				}
			}
			logger.WithFields(map[string]interface{}{
				"container_id": id,
				"component":    component,
			}).Info("Swept leaked container from prior worker run")
		}
	}
}

// SpawnJob creates and starts a container using nerdctl
func (cr *ContainerdRunner) SpawnJob(ctx context.Context, config *JobConfig) (string, error) {
	logger := logging.Log.WithField("job_id", config.JobID)

	// Validate configuration
	if err := cr.validateConfig(config); err != nil {
		return "", fmt.Errorf("invalid job configuration: %w", err)
	}

	containerID := fmt.Sprintf("reactorcide-job-%s", config.JobID)

	// If the job requested the builder capability, spawn the buildkitd
	// sidecar first and have the job container share its netns.
	wantsBuilder := HasCapability(config.Capabilities, CapabilityBuilder)
	var sidecarName string
	if wantsBuilder {
		name, err := cr.startBuilderSidecar(ctx, config)
		if err != nil {
			return "", fmt.Errorf("failed to start builder sidecar: %w", err)
		}
		sidecarName = name
	}

	// Build nerdctl run command
	args := []string{
		"--namespace", containerdNamespace,
		"run",
		"--name", containerID,
		"--rm=false",       // Don't auto-remove so we can get logs and exit code
		"--pull", "always", // Always pull to get latest image
	}
	if wantsBuilder {
		// Share the sidecar's netns so BUILDKIT_HOST=tcp://localhost:1234 works
		// without any per-job network plumbing — mirrors k8s pod semantics.
		args = append(args, "--net", "container:"+sidecarName)
	} else {
		args = append(args, "--net", "bridge")
	}

	// Set working directory only if specified, otherwise use container's default
	if config.WorkingDir != "" {
		args = append(args, "--workdir", config.WorkingDir)
	}

	// Mount workspace directory
	args = append(args, "-v", fmt.Sprintf("%s:/job", config.WorkspaceDir))
	if config.SourceDir != "" {
		args = append(args, "-v", fmt.Sprintf("%s:/job/src", config.SourceDir))
	}
	for _, m := range config.ExtraMounts {
		args = append(args, "-v", m)
	}

	// Add environment variables
	for key, value := range config.Env {
		args = append(args, "-e", fmt.Sprintf("%s=%s", key, value))
	}
	if wantsBuilder {
		args = append(args, "-e", fmt.Sprintf("%s=tcp://localhost:%d", BuilderHostEnv, BuilderSidecarPort))
	}

	// Default non-root; docker and builder both imply root uid inside the
	// container (see DockerRunner for rationale).
	needsRoot := HasCapability(config.Capabilities, CapabilityDocker) ||
		HasCapability(config.Capabilities, CapabilityBuilder)
	if !needsRoot {
		user := "1001:1001"
		if config.RunAsUser != "" {
			user = config.RunAsUser
		}
		args = append(args, "--user", user)
		logger.WithField("user", user).Info("Running container as non-root user")
	} else {
		logger.Info("Running container as root (build/docker capability requested)")
	}

	// Handle capabilities
	for _, cap := range config.Capabilities {
		switch cap {
		case CapabilityDocker:
			args = append(args, "--privileged")
			logger.Info("Docker capability enabled: running in privileged mode")
		case CapabilityBuilder:
			// Handled upstream: sidecar spawn + shared netns. Job container
			// itself stays unprivileged.
		case CapabilityGPU:
			// TODO: GPU support requires NVIDIA container runtime integration
			logger.Warn("GPU capability requested but not yet implemented")
		default:
			logger.WithField("capability", cap).Warn("Unknown capability requested, ignoring")
		}
	}

	// Add resource limits if specified
	if config.MemoryLimit != "" {
		args = append(args, "--memory", config.MemoryLimit)
	}

	// Add labels
	args = append(args, "--label", fmt.Sprintf("reactorcide.job_id=%s", config.JobID))
	args = append(args, "--label", fmt.Sprintf("reactorcide.queue=%s", config.QueueName))
	args = append(args, "--label", "reactorcide.component=job-container")

	// Clear entrypoint so command is executed directly
	// This allows job commands like "runnerlib run ..." to work with any base image
	args = append(args, "--entrypoint", "")

	// Add image
	args = append(args, config.Image)

	// Add command arguments directly (already parsed by job_processor)
	args = append(args, config.Command...)

	logger.WithFields(map[string]interface{}{
		"container_id": containerID,
		"image":        config.Image,
		"command":      config.Command,
	}).Info("Creating nerdctl container")

	// Create pipes returned to the caller; both job and sidecar output flow
	// through line-tagging writers so writes are atomic per line and sidecar
	// lines are clearly prefixed for the operator — and both streams pass
	// through the caller's secret masker identically.
	stdoutReader, stdoutWriter := io.Pipe()
	stderrReader, stderrWriter := io.Pipe()

	var stdoutMu, stderrMu sync.Mutex
	jobStdoutTag := &taggingWriter{dest: stdoutWriter, mu: &stdoutMu}
	jobStderrTag := &taggingWriter{dest: stderrWriter, mu: &stderrMu}

	cmd := exec.CommandContext(ctx, nerdctlBinary, args...)
	cmd.Stdout = jobStdoutTag
	cmd.Stderr = jobStderrTag

	if err := cmd.Start(); err != nil {
		stdoutWriter.Close()
		stderrWriter.Close()
		stdoutReader.Close()
		stderrReader.Close()
		if sidecarName != "" {
			cr.removeSidecar(context.Background(), sidecarName)
		}
		return "", fmt.Errorf("failed to start nerdctl: %w", err)
	}

	// Optional sidecar log pump. `nerdctl logs -f` keeps running as long as
	// the sidecar container lives; we'll kill it explicitly when the job
	// exits so output pipes can close.
	var sidecarLogsCmd *exec.Cmd
	var sidecarStdoutTag, sidecarStderrTag *taggingWriter
	if sidecarName != "" {
		sidecarStdoutTag = &taggingWriter{dest: stdoutWriter, mu: &stdoutMu, prefix: []byte(BuilderLogPrefix)}
		sidecarStderrTag = &taggingWriter{dest: stderrWriter, mu: &stderrMu, prefix: []byte(BuilderLogPrefix)}
		sidecarLogsCmd = exec.Command(nerdctlBinary, "--namespace", containerdNamespace, "logs", "-f", sidecarName)
		sidecarLogsCmd.Stdout = sidecarStdoutTag
		sidecarLogsCmd.Stderr = sidecarStderrTag
		if err := sidecarLogsCmd.Start(); err != nil {
			logger.WithError(err).Warn("Failed to start builder sidecar log pump; continuing without sidecar logs")
			sidecarLogsCmd = nil
		}
	}

	// Store the container process
	proc := &containerProcess{
		containerID:  containerID,
		cmd:          cmd,
		stdoutReader: stdoutReader,
		stdoutWriter: stdoutWriter,
		stderrReader: stderrReader,
		stderrWriter: stderrWriter,
		done:         make(chan struct{}),
	}

	cr.mu.Lock()
	cr.containers[containerID] = proc
	cr.mu.Unlock()

	if sidecarName != "" {
		cr.sidecarsMu.Lock()
		cr.sidecars[containerID] = sidecarName
		cr.sidecarsMu.Unlock()
	}

	// Wait for job exit, flush taggers, tear down sidecar log pump, close pipes.
	go func() {
		err := cmd.Wait()

		if err != nil {
			if exitErr, ok := err.(*exec.ExitError); ok {
				proc.exitCode = exitErr.ExitCode()
			} else {
				proc.exitCode = -1
				proc.exitErr = err
			}
		}

		// Flush any partial trailing lines from the job before tearing down.
		jobStdoutTag.Flush()
		jobStderrTag.Flush()

		// Stop the sidecar log pump so its reader unblocks.
		if sidecarLogsCmd != nil && sidecarLogsCmd.Process != nil {
			_ = sidecarLogsCmd.Process.Kill()
			_ = sidecarLogsCmd.Wait()
		}
		if sidecarStdoutTag != nil {
			sidecarStdoutTag.Flush()
		}
		if sidecarStderrTag != nil {
			sidecarStderrTag.Flush()
		}

		stdoutWriter.Close()
		stderrWriter.Close()

		close(proc.done)
	}()

	logger.WithField("container_id", containerID).Info("nerdctl container started successfully")
	return containerID, nil
}

// startBuilderSidecar launches a buildkitd sidecar container detached. The
// returned name is what --net=container:<name> attaches the job container to.
func (cr *ContainerdRunner) startBuilderSidecar(ctx context.Context, config *JobConfig) (string, error) {
	logger := logging.Log.WithField("job_id", config.JobID)

	image := cr.builder.Image
	if image == "" {
		image = DefaultBuilderImage
	}
	name := BuilderSidecarName(config.JobID)

	args := []string{
		"--namespace", containerdNamespace,
		"run", "-d",
		"--name", name,
		"--rm=false",
		"--privileged",
		"--net", "bridge",
		"--pull", "always",
		"--label", "reactorcide.job_id=" + config.JobID,
		"--label", "reactorcide.component=builder-sidecar",
	}
	if cr.builder.ConfigPath != "" {
		args = append(args, "-v", fmt.Sprintf("%s:/etc/buildkit/buildkitd.toml:ro", cr.builder.ConfigPath))
	}
	if cr.builder.RegistryAuthPath != "" {
		args = append(args, "-v", fmt.Sprintf("%s:/root/.docker/config.json:ro", cr.builder.RegistryAuthPath))
	}
	if cr.builder.CacheVolume != "" {
		args = append(args, "-v", fmt.Sprintf("%s:/var/lib/buildkit", cr.builder.CacheVolume))
	}
	args = append(args, image,
		"--addr", fmt.Sprintf("tcp://0.0.0.0:%d", BuilderSidecarPort),
		"--oci-worker=true",
	)
	if cr.builder.ConfigPath != "" {
		args = append(args, "--config", "/etc/buildkit/buildkitd.toml")
	}

	out, err := exec.CommandContext(ctx, nerdctlBinary, args...).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("nerdctl run buildkit sidecar: %w (%s)", err, strings.TrimSpace(string(out)))
	}

	logger.WithField("sidecar_name", name).Info("Builder sidecar started")
	return name, nil
}

// removeSidecar best-effort cleanup: stops and removes a sidecar by name.
func (cr *ContainerdRunner) removeSidecar(ctx context.Context, name string) {
	logger := logging.Log.WithField("sidecar_name", name)
	cmd := exec.CommandContext(ctx, nerdctlBinary, "--namespace", containerdNamespace, "rm", "-f", name)
	if out, err := cmd.CombinedOutput(); err != nil {
		if !strings.Contains(string(out), "not found") && !strings.Contains(string(out), "No such container") {
			logger.WithError(err).WithField("output", string(out)).Warn("Failed to remove builder sidecar")
			return
		}
	}
	logger.Info("Builder sidecar cleaned up")
}

// StreamLogs returns stdout and stderr readers for a container
func (cr *ContainerdRunner) StreamLogs(ctx context.Context, containerID string) (stdout io.ReadCloser, stderr io.ReadCloser, err error) {
	logger := logging.Log.WithField("container_id", containerID)

	cr.mu.RLock()
	proc, ok := cr.containers[containerID]
	cr.mu.RUnlock()

	if !ok {
		return nil, nil, fmt.Errorf("no container found for %s", containerID)
	}

	logger.Info("Returning log streams for nerdctl container")
	return proc.stdoutReader, proc.stderrReader, nil
}

// WaitForCompletion waits for the container to exit and returns the exit code
func (cr *ContainerdRunner) WaitForCompletion(ctx context.Context, containerID string) (int, error) {
	logger := logging.Log.WithField("container_id", containerID)

	cr.mu.RLock()
	proc, ok := cr.containers[containerID]
	cr.mu.RUnlock()

	if !ok {
		return -1, fmt.Errorf("no container found for %s", containerID)
	}

	// Wait for the process to exit via the done channel
	select {
	case <-proc.done:
		// Process has exited, check for errors
		if proc.exitErr != nil {
			return -1, fmt.Errorf("failed to wait for container: %w", proc.exitErr)
		}
		logger.WithField("exit_code", proc.exitCode).Info("Container exited")
		return proc.exitCode, nil
	case <-ctx.Done():
		return -1, ctx.Err()
	}
}

// Cleanup removes the container and associated resources
func (cr *ContainerdRunner) Cleanup(ctx context.Context, containerID string) error {
	logger := logging.Log.WithField("container_id", containerID)

	logger.Info("Cleaning up nerdctl container")

	// Remove from our tracking
	cr.mu.Lock()
	proc, ok := cr.containers[containerID]
	if ok {
		// Close any remaining pipes
		proc.stdoutWriter.Close()
		proc.stderrWriter.Close()
		proc.stdoutReader.Close()
		proc.stderrReader.Close()
		delete(cr.containers, containerID)
	}
	cr.mu.Unlock()

	// Kill the container if still running and remove it
	// Use nerdctl rm -f to force remove (will stop if running)
	cmd := exec.CommandContext(ctx, nerdctlBinary, "--namespace", containerdNamespace, "rm", "-f", containerID)
	output, err := cmd.CombinedOutput()
	if err != nil {
		// Ignore "not found" errors
		if !strings.Contains(string(output), "not found") && !strings.Contains(string(output), "No such container") {
			logger.WithError(err).WithField("output", string(output)).Warn("Failed to remove container")
		}
	}

	// Tear down the builder sidecar, if any.
	cr.sidecarsMu.Lock()
	sidecarName, hadSidecar := cr.sidecars[containerID]
	delete(cr.sidecars, containerID)
	cr.sidecarsMu.Unlock()
	if hadSidecar {
		cr.removeSidecar(ctx, sidecarName)
	}

	logger.Info("nerdctl container cleaned up successfully")
	return nil
}

// validateConfig validates the job configuration
func (cr *ContainerdRunner) validateConfig(config *JobConfig) error {
	if config.Image == "" {
		return fmt.Errorf("container image is required")
	}
	if len(config.Command) == 0 {
		return fmt.Errorf("command is required")
	}
	if config.WorkspaceDir == "" {
		return fmt.Errorf("workspace directory is required")
	}
	if config.JobID == "" {
		return fmt.Errorf("job ID is required")
	}
	return nil
}

// pullImage pulls an image using nerdctl if not present locally
func (cr *ContainerdRunner) pullImage(ctx context.Context, imageName string) error {
	logger := logging.Log.WithField("image", imageName)

	// Check if image exists
	checkCmd := exec.CommandContext(ctx, nerdctlBinary, "--namespace", containerdNamespace, "image", "inspect", imageName)
	if err := checkCmd.Run(); err == nil {
		logger.Debug("Image already exists locally")
		return nil
	}

	logger.Info("Pulling container image")

	cmd := exec.CommandContext(ctx, nerdctlBinary, "--namespace", containerdNamespace, "pull", imageName)

	// Stream pull output to logs
	stdout, _ := cmd.StdoutPipe()
	stderr, _ := cmd.StderrPipe()

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start image pull: %w", err)
	}

	// Log pull progress
	go func() {
		scanner := bufio.NewScanner(io.MultiReader(stdout, stderr))
		for scanner.Scan() {
			logger.Debug(scanner.Text())
		}
	}()

	if err := cmd.Wait(); err != nil {
		return fmt.Errorf("failed to pull image: %w", err)
	}

	logger.Info("Image pulled successfully")
	return nil
}

// Ensure ContainerdRunner implements JobRunner interface
var _ JobRunner = (*ContainerdRunner)(nil)
