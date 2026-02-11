package worker

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"

	"github.com/catalystcommunity/app-utils-go/logging"
)

// DockerRunner implements JobRunner using the Docker daemon
type DockerRunner struct {
	client *client.Client
}

// NewDockerRunner creates a new Docker-based job runner
// Uses the default Docker socket (unix:///var/run/docker.sock or npipe on Windows)
func NewDockerRunner() (*DockerRunner, error) {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, fmt.Errorf("failed to create Docker client: %w", err)
	}

	return &DockerRunner{
		client: cli,
	}, nil
}

// NewDockerRunnerWithClient creates a DockerRunner with a custom Docker client
// Useful for testing or custom configurations
func NewDockerRunnerWithClient(cli *client.Client) *DockerRunner {
	return &DockerRunner{
		client: cli,
	}
}

// SpawnJob creates and starts a Docker container for the job
func (dr *DockerRunner) SpawnJob(ctx context.Context, config *JobConfig) (string, error) {
	logger := logging.Log.WithField("job_id", config.JobID)

	// Validate configuration
	if err := dr.validateConfig(config); err != nil {
		return "", fmt.Errorf("invalid job configuration: %w", err)
	}

	// Pull the image if it doesn't exist locally
	logger.WithField("image", config.Image).Info("Ensuring Docker image is available")
	if err := dr.ensureImage(ctx, config.Image); err != nil {
		return "", fmt.Errorf("failed to ensure image: %w", err)
	}

	// Prepare container configuration
	// WorkingDir uses container's default if not specified
	containerConfig := &container.Config{
		Image:        config.Image,
		Cmd:          config.Command,
		Env:          dr.envMapToSlice(config.Env),
		WorkingDir:   config.WorkingDir,
		AttachStdout: true,
		AttachStderr: true,
		Tty:          false,
		Labels: map[string]string{
			"reactorcide.job_id":    config.JobID,
			"reactorcide.queue":     config.QueueName,
			"reactorcide.component": "job-container",
		},
	}

	// Run as non-root user 1001:1001 for security, unless the job
	// requires the docker capability (container builds need root)
	needsRoot := false
	for _, cap := range config.Capabilities {
		if cap == CapabilityDocker {
			needsRoot = true
			break
		}
	}
	if !needsRoot {
		containerConfig.User = "1001:1001"
		logger.Info("Running container as non-root user 1001:1001")
	} else {
		logger.Info("Running container as root (docker capability requested)")
	}

	// Always clear the entrypoint so Command runs directly.
	// Users specify the full command in JobConfig.Command.
	containerConfig.Entrypoint = []string{}

	// Prepare host configuration (mounts, resource limits)
	binds := []string{
		fmt.Sprintf("%s:/job", config.WorkspaceDir),
	}
	if config.SourceDir != "" {
		binds = append(binds, fmt.Sprintf("%s:/job/src", config.SourceDir))
	}

	// Handle capabilities
	privileged := false
	for _, cap := range config.Capabilities {
		switch cap {
		case CapabilityDocker:
			// Container builds with buildkit need privileged mode for mount operations
			privileged = true
			logger.Info("Docker capability enabled: running in privileged mode for buildkit")
		case CapabilityGPU:
			// TODO: GPU support not yet implemented for DockerRunner
			// Would need to use container.DeviceRequest with "nvidia" driver
			// and potentially the --gpus flag equivalent in the API
			logger.Warn("GPU capability requested but not yet implemented for DockerRunner")
		default:
			logger.WithField("capability", cap).Warn("Unknown capability requested, ignoring")
		}
	}

	hostConfig := &container.HostConfig{
		Binds:      binds,
		Privileged: privileged,
		AutoRemove: false, // We'll remove it explicitly in Cleanup
	}

	// Add resource limits if specified
	if config.CPULimit != "" {
		// Convert CPU limit (e.g., "1.0" -> 1000000000 nanoseconds)
		// Docker uses NanoCPUs (1 CPU = 1e9 nanoseconds)
		var cpuNanos int64
		if _, err := fmt.Sscanf(config.CPULimit, "%f", &cpuNanos); err == nil {
			hostConfig.NanoCPUs = int64(cpuNanos * 1e9)
		}
	}

	if config.MemoryLimit != "" {
		// Parse memory limit (e.g., "512Mi" or "1Gi")
		memBytes, err := parseMemoryString(config.MemoryLimit)
		if err != nil {
			logger.WithError(err).Warn("Failed to parse memory limit, ignoring")
		} else {
			hostConfig.Memory = memBytes
		}
	}

	// Create the container
	containerName := fmt.Sprintf("reactorcide-job-%s", config.JobID)
	logger.WithFields(map[string]interface{}{
		"container_name": containerName,
		"image":          config.Image,
		"command":        config.Command,
	}).Info("Creating Docker container")

	resp, err := dr.client.ContainerCreate(ctx, containerConfig, hostConfig, nil, nil, containerName)
	if err != nil {
		return "", fmt.Errorf("failed to create container: %w", err)
	}

	// Log any warnings from container creation
	if len(resp.Warnings) > 0 {
		logger.WithField("warnings", resp.Warnings).Warn("Container creation warnings")
	}

	// Start the container
	logger.WithField("container_id", resp.ID).Info("Starting Docker container")
	if err := dr.client.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		// Clean up the container if start fails
		dr.client.ContainerRemove(ctx, resp.ID, container.RemoveOptions{Force: true})
		return "", fmt.Errorf("failed to start container: %w", err)
	}

	logger.WithField("container_id", resp.ID).Info("Docker container started successfully")
	return resp.ID, nil
}

// StreamLogs streams stdout and stderr from the container
func (dr *DockerRunner) StreamLogs(ctx context.Context, containerID string) (stdout io.ReadCloser, stderr io.ReadCloser, err error) {
	logger := logging.Log.WithField("container_id", containerID)

	// Get logs stream from Docker
	logOptions := container.LogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Follow:     true,
		Timestamps: false,
	}

	logs, err := dr.client.ContainerLogs(ctx, containerID, logOptions)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get container logs: %w", err)
	}

	// Docker multiplexes stdout and stderr into a single stream with headers
	// We need to demultiplex them using stdcopy
	stdoutReader, stdoutWriter := io.Pipe()
	stderrReader, stderrWriter := io.Pipe()

	// Start a goroutine to demultiplex the log stream
	go func() {
		defer logs.Close()
		defer stdoutWriter.Close()
		defer stderrWriter.Close()

		_, err := stdcopy.StdCopy(stdoutWriter, stderrWriter, logs)
		if err != nil && err != io.EOF {
			logger.WithError(err).Error("Error demultiplexing container logs")
		}
	}()

	return stdoutReader, stderrReader, nil
}

// WaitForCompletion waits for the container to exit and returns the exit code
func (dr *DockerRunner) WaitForCompletion(ctx context.Context, containerID string) (int, error) {
	logger := logging.Log.WithField("container_id", containerID)

	// Wait for the container to exit
	statusCh, errCh := dr.client.ContainerWait(ctx, containerID, container.WaitConditionNotRunning)

	select {
	case err := <-errCh:
		if err != nil {
			return -1, fmt.Errorf("error waiting for container: %w", err)
		}
	case status := <-statusCh:
		logger.WithField("exit_code", status.StatusCode).Info("Container exited")
		return int(status.StatusCode), nil
	}

	return -1, fmt.Errorf("unexpected error waiting for container")
}

// Cleanup removes the container and associated resources
func (dr *DockerRunner) Cleanup(ctx context.Context, containerID string) error {
	logger := logging.Log.WithField("container_id", containerID)

	logger.Info("Cleaning up Docker container")

	// Remove the container (force remove even if still running)
	removeOptions := container.RemoveOptions{
		RemoveVolumes: true,
		Force:         true,
	}

	if err := dr.client.ContainerRemove(ctx, containerID, removeOptions); err != nil {
		return fmt.Errorf("failed to remove container: %w", err)
	}

	logger.Info("Docker container cleaned up successfully")
	return nil
}

// validateConfig validates the job configuration
func (dr *DockerRunner) validateConfig(config *JobConfig) error {
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

// ensureImage pulls the image if it doesn't exist locally
func (dr *DockerRunner) ensureImage(ctx context.Context, imageName string) error {
	logger := logging.Log.WithField("image", imageName)

	// Check if image exists locally
	_, _, err := dr.client.ImageInspectWithRaw(ctx, imageName)
	if err == nil {
		// Image exists locally
		logger.Debug("Image found locally")
		return nil
	}

	// Image doesn't exist, pull it
	logger.Info("Pulling Docker image")
	pullResp, err := dr.client.ImagePull(ctx, imageName, image.PullOptions{})
	if err != nil {
		return fmt.Errorf("failed to pull image: %w", err)
	}
	defer pullResp.Close()

	// Read the pull response to ensure it completes
	// (Docker SDK requires reading the response body)
	_, err = io.Copy(io.Discard, pullResp)
	if err != nil {
		return fmt.Errorf("error reading pull response: %w", err)
	}

	logger.Info("Image pulled successfully")
	return nil
}

// envMapToSlice converts an environment variable map to a slice of "KEY=VALUE" strings
func (dr *DockerRunner) envMapToSlice(envMap map[string]string) []string {
	if envMap == nil {
		return nil
	}

	envSlice := make([]string, 0, len(envMap))
	for key, value := range envMap {
		envSlice = append(envSlice, fmt.Sprintf("%s=%s", key, value))
	}
	return envSlice
}

// parseMemoryString parses memory strings like "512Mi", "1Gi", "1024M", "1G"
func parseMemoryString(memStr string) (int64, error) {
	memStr = strings.TrimSpace(memStr)
	if memStr == "" {
		return 0, fmt.Errorf("empty memory string")
	}

	// Common suffixes and their multipliers
	suffixes := map[string]int64{
		"Ki": 1024,
		"Mi": 1024 * 1024,
		"Gi": 1024 * 1024 * 1024,
		"Ti": 1024 * 1024 * 1024 * 1024,
		"K":  1000,
		"M":  1000 * 1000,
		"G":  1000 * 1000 * 1000,
		"T":  1000 * 1000 * 1000 * 1000,
	}

	// Try to match suffix
	for suffix, multiplier := range suffixes {
		if strings.HasSuffix(memStr, suffix) {
			numStr := strings.TrimSuffix(memStr, suffix)
			var num int64
			_, err := fmt.Sscanf(numStr, "%d", &num)
			if err != nil {
				return 0, fmt.Errorf("invalid number in memory string: %w", err)
			}
			return num * multiplier, nil
		}
	}

	// No suffix, assume bytes
	var num int64
	_, err := fmt.Sscanf(memStr, "%d", &num)
	if err != nil {
		return 0, fmt.Errorf("invalid memory string format: %w", err)
	}
	return num, nil
}

// Ensure DockerRunner implements JobRunner interface
var _ JobRunner = (*DockerRunner)(nil)
