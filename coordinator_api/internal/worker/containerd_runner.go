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
}

// NewContainerdRunner creates a new nerdctl-based job runner
func NewContainerdRunner() (*ContainerdRunner, error) {
	// Verify nerdctl is available
	cmd := exec.Command(nerdctlBinary, "version")
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("nerdctl not available: %w", err)
	}

	logging.Log.Info("nerdctl runner initialized")

	return &ContainerdRunner{
		containers: make(map[string]*containerProcess),
	}, nil
}

// SpawnJob creates and starts a container using nerdctl
func (cr *ContainerdRunner) SpawnJob(ctx context.Context, config *JobConfig) (string, error) {
	logger := logging.Log.WithField("job_id", config.JobID)

	// Validate configuration
	if err := cr.validateConfig(config); err != nil {
		return "", fmt.Errorf("invalid job configuration: %w", err)
	}

	containerID := fmt.Sprintf("reactorcide-job-%s", config.JobID)

	// Build nerdctl run command
	args := []string{
		"--namespace", containerdNamespace,
		"run",
		"--name", containerID,
		"--rm=false",      // Don't auto-remove so we can get logs and exit code
		"--net", "bridge", // Use bridge networking with CNI
		"--pull", "always", // Always pull to get latest image
	}

	// Determine working directory
	workingDir := "/job"
	if config.WorkingDir != "" {
		workingDir = config.WorkingDir
	}
	args = append(args, "--workdir", workingDir)

	// Mount workspace directory
	args = append(args, "-v", fmt.Sprintf("%s:/job", config.WorkspaceDir))

	// Add environment variables
	for key, value := range config.Env {
		args = append(args, "-e", fmt.Sprintf("%s=%s", key, value))
	}

	// Handle user - run as non-root unless docker capability is requested
	needsRoot := false
	for _, cap := range config.Capabilities {
		if cap == CapabilityDocker {
			needsRoot = true
			break
		}
	}
	if !needsRoot {
		args = append(args, "--user", "1001:1001")
		logger.Info("Running container as non-root user 1001:1001")
	} else {
		logger.Info("Running container as root (docker capability requested)")
	}

	// Handle capabilities
	for _, cap := range config.Capabilities {
		switch cap {
		case CapabilityDocker:
			args = append(args, "--privileged")
			logger.Info("Docker capability enabled: running in privileged mode")
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

	// Override entrypoint to use shell
	args = append(args, "--entrypoint", "/bin/sh")

	// Add image
	args = append(args, config.Image)

	// Add command - join for shell execution
	shellCmd := strings.Join(config.Command, " ")
	args = append(args, "-c", shellCmd)

	logger.WithFields(map[string]interface{}{
		"container_id": containerID,
		"image":        config.Image,
		"command":      shellCmd,
	}).Info("Creating nerdctl container")

	// Create pipes for stdout/stderr
	stdoutReader, stdoutWriter := io.Pipe()
	stderrReader, stderrWriter := io.Pipe()

	// Create and start the nerdctl command
	cmd := exec.CommandContext(ctx, nerdctlBinary, args...)
	cmd.Stdout = stdoutWriter
	cmd.Stderr = stderrWriter

	if err := cmd.Start(); err != nil {
		stdoutWriter.Close()
		stderrWriter.Close()
		stdoutReader.Close()
		stderrReader.Close()
		return "", fmt.Errorf("failed to start nerdctl: %w", err)
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

	// Start a goroutine to wait for process exit and close writers
	go func() {
		err := cmd.Wait()

		// Store exit information
		if err != nil {
			if exitErr, ok := err.(*exec.ExitError); ok {
				proc.exitCode = exitErr.ExitCode()
			} else {
				proc.exitCode = -1
				proc.exitErr = err
			}
		}

		// Close writers to unblock any readers
		stdoutWriter.Close()
		stderrWriter.Close()

		// Signal completion
		close(proc.done)
	}()

	logger.WithField("container_id", containerID).Info("nerdctl container started successfully")
	return containerID, nil
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
