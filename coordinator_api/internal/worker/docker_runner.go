package worker

import (
	"context"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"
	"github.com/sirupsen/logrus"

	"github.com/catalystcommunity/app-utils-go/logging"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/resources"
)

// DockerRunner implements JobRunner using the Docker daemon
type DockerRunner struct {
	client  *client.Client
	builder BuilderConfig

	// sidecars maps job container ID to its builder sidecar container ID so
	// Cleanup can tear down both. Populated when CapabilityBuilder is set.
	sidecars   map[string]string
	sidecarsMu sync.Mutex
}

// NewDockerRunner creates a new Docker-based job runner
// Uses the default Docker socket (unix:///var/run/docker.sock or npipe on Windows)
func NewDockerRunner() (*DockerRunner, error) {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, fmt.Errorf("failed to create Docker client: %w", err)
	}

	dr := &DockerRunner{
		client:   cli,
		builder:  LoadBuilderConfig(),
		sidecars: make(map[string]string),
	}
	dr.sweepLeaked(context.Background())
	return dr, nil
}

// sweepLeaked removes any job/sidecar containers left over from a prior
// worker run. Assumes a single worker per host; multi-worker setups should
// not share a runtime.
func (dr *DockerRunner) sweepLeaked(ctx context.Context) {
	logger := logging.Log
	for _, component := range []string{"builder-sidecar", "job-container"} {
		f := filters.NewArgs()
		f.Add("label", "reactorcide.component="+component)
		list, err := dr.client.ContainerList(ctx, container.ListOptions{All: true, Filters: f})
		if err != nil {
			logger.WithError(err).Warn("Failed to list leaked containers")
			continue
		}
		for _, c := range list {
			if err := dr.client.ContainerRemove(ctx, c.ID, container.RemoveOptions{Force: true}); err != nil {
				logger.WithError(err).WithField("container_id", c.ID).Warn("Failed to sweep leaked container")
				continue
			}
			logger.WithFields(map[string]interface{}{
				"container_id": c.ID,
				"component":    component,
			}).Info("Swept leaked container from prior worker run")
		}
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

	// If the job requested the builder capability, spawn the buildkitd sidecar
	// first so the job can attach to its netns. Sidecar cleanup is handled on
	// any subsequent failure, and via Cleanup() on success.
	wantsBuilder := HasCapability(config.Capabilities, CapabilityBuilder)
	var sidecarID, sidecarName string
	if wantsBuilder {
		sid, sname, err := dr.startBuilderSidecar(ctx, config)
		if err != nil {
			return "", fmt.Errorf("failed to start builder sidecar: %w", err)
		}
		sidecarID = sid
		sidecarName = sname
	}

	// Prepare container configuration
	// WorkingDir uses container's default if not specified
	env := dr.envMapToSlice(config.Env)
	if wantsBuilder {
		env = append(env, fmt.Sprintf("%s=tcp://localhost:%d", BuilderHostEnv, BuilderSidecarPort))
	}
	containerConfig := &container.Config{
		Image:        config.Image,
		Cmd:          config.Command,
		Env:          env,
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

	user, err := DefaultRunAsUser(config.RunAsUser)
	if err != nil {
		return "", fmt.Errorf("invalid run-as user: %w", err)
	}
	containerConfig.User = user
	logger.WithField("user", user).Info("Running container with configured user")

	// Always clear the entrypoint so Command runs directly.
	// Users specify the full command in JobConfig.Command.
	containerConfig.Entrypoint = []string{}

	// Prepare host configuration (mounts, resource limits)
	binds := []string{
		fmt.Sprintf("%s:/job", config.WorkspaceDir),
	}
	if config.SourceDir != "" {
		sourceMountPath := config.SourceMountPath
		if sourceMountPath == "" {
			sourceMountPath = defaultCodeDir
		}
		binds = append(binds, fmt.Sprintf("%s:%s", config.SourceDir, sourceMountPath))
	}
	binds = append(binds, config.ExtraMounts...)

	// Handle capabilities
	privileged := false
	for _, cap := range config.Capabilities {
		switch cap {
		case CapabilityDocker:
			// Container builds with buildkit need privileged mode for mount operations
			privileged = true
			logger.Info("Docker capability enabled: running in privileged mode for buildkit")
		case CapabilityBuilder:
			// Handled above: sidecar spawn + NetworkMode/env injection. No
			// privilege change on the job container itself.
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

	// When the builder capability is active, attach the job to the sidecar's
	// netns so BUILDKIT_HOST=tcp://localhost:1234 reaches buildkitd. This
	// mirrors k8s pod netns sharing, giving job YAMLs a single wire to code
	// against across runners.
	if wantsBuilder {
		hostConfig.NetworkMode = container.NetworkMode("container:" + sidecarName)
	}

	// Add resource limits/requests if specified. CPU/memory values are
	// Kubernetes-style quantity strings (see internal/resources and
	// JobConfig's CPURequest/CPULimit/MemoryLimit doc comments).
	hostConfig.NanoCPUs, hostConfig.CPUShares, hostConfig.Memory = dockerResourceLimits(config, logger)

	// Create the container
	containerName := fmt.Sprintf("reactorcide-job-%s", config.JobID)
	logger.WithFields(map[string]interface{}{
		"container_name": containerName,
		"image":          config.Image,
		"command":        config.Command,
	}).Info("Creating Docker container")

	resp, err := dr.client.ContainerCreate(ctx, containerConfig, hostConfig, nil, nil, containerName)
	if err != nil {
		if sidecarID != "" {
			dr.client.ContainerRemove(ctx, sidecarID, container.RemoveOptions{Force: true})
		}
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
		if sidecarID != "" {
			dr.client.ContainerRemove(ctx, sidecarID, container.RemoveOptions{Force: true})
		}
		return "", fmt.Errorf("failed to start container: %w", err)
	}

	if sidecarID != "" {
		dr.sidecarsMu.Lock()
		dr.sidecars[resp.ID] = sidecarID
		dr.sidecarsMu.Unlock()
	}

	logger.WithField("container_id", resp.ID).Info("Docker container started successfully")
	return resp.ID, nil
}

// startBuilderSidecar spawns a buildkitd container bound to a TCP port on the
// container's internal interface. The returned name is what NetworkMode uses
// to attach the job container to the sidecar's netns.
func (dr *DockerRunner) startBuilderSidecar(ctx context.Context, config *JobConfig) (id, name string, err error) {
	logger := logging.Log.WithField("job_id", config.JobID)

	image := dr.builder.Image
	if image == "" {
		image = DefaultBuilderImage
	}

	logger.WithField("image", image).Info("Ensuring builder sidecar image")
	if err := dr.ensureImage(ctx, image); err != nil {
		return "", "", fmt.Errorf("pull buildkit image: %w", err)
	}

	name = BuilderSidecarName(config.JobID)
	priv := true

	// buildkitd --addr tcp://0.0.0.0:1234 listens on all interfaces within its
	// own netns; the job container joins that netns and reaches it via
	// localhost. We pass `--oci-worker=true` explicitly so the image's default
	// worker choice is deterministic across buildkit versions.
	cmd := []string{
		"--addr", fmt.Sprintf("tcp://0.0.0.0:%d", BuilderSidecarPort),
		"--oci-worker=true",
	}
	// When operator supplied a buildkitd.toml, point buildkitd at it.
	if dr.builder.ConfigPath != "" {
		cmd = append(cmd, "--config", "/etc/buildkit/buildkitd.toml")
	}

	var binds []string
	if dr.builder.ConfigPath != "" {
		binds = append(binds, fmt.Sprintf("%s:/etc/buildkit/buildkitd.toml:ro", dr.builder.ConfigPath))
	}
	if dr.builder.RegistryAuthPath != "" {
		binds = append(binds, fmt.Sprintf("%s:/root/.docker/config.json:ro", dr.builder.RegistryAuthPath))
	}

	// Named-volume cache: docker interprets "<volname>:<path>" as a named
	// volume mount when volname has no leading slash, which is exactly what
	// operators pass via REACTORCIDE_BUILDER_CACHE_VOLUME.
	if dr.builder.CacheVolume != "" {
		binds = append(binds, fmt.Sprintf("%s:/var/lib/buildkit", dr.builder.CacheVolume))
	}

	sidecarCfg := &container.Config{
		Image: image,
		Cmd:   cmd,
		Labels: map[string]string{
			"reactorcide.job_id":    config.JobID,
			"reactorcide.component": "builder-sidecar",
		},
	}
	sidecarHost := &container.HostConfig{
		Binds:      binds,
		Privileged: priv,
		AutoRemove: false,
	}

	resp, err := dr.client.ContainerCreate(ctx, sidecarCfg, sidecarHost, nil, nil, name)
	if err != nil {
		return "", "", fmt.Errorf("create sidecar: %w", err)
	}
	if err := dr.client.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		dr.client.ContainerRemove(ctx, resp.ID, container.RemoveOptions{Force: true})
		return "", "", fmt.Errorf("start sidecar: %w", err)
	}
	logger.WithFields(map[string]interface{}{
		"sidecar_id":   resp.ID,
		"sidecar_name": name,
	}).Info("Builder sidecar started")
	return resp.ID, name, nil
}

// StreamLogs streams stdout and stderr from the container
func (dr *DockerRunner) StreamLogs(ctx context.Context, containerID string) (stdout io.ReadCloser, stderr io.ReadCloser, err error) {
	logger := logging.Log.WithField("container_id", containerID)

	logOptions := container.LogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Follow:     true,
		Timestamps: false,
	}

	jobLogs, err := dr.client.ContainerLogs(ctx, containerID, logOptions)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get container logs: %w", err)
	}

	// If this job has a builder sidecar, open its log stream too so we can
	// surface buildkitd output to the user, tagged and line-merged into the
	// same pipes — which means the caller's secret masker applies to it just
	// like job output.
	dr.sidecarsMu.Lock()
	sidecarID := dr.sidecars[containerID]
	dr.sidecarsMu.Unlock()

	var sidecarLogs io.ReadCloser
	if sidecarID != "" {
		sidecarLogs, err = dr.client.ContainerLogs(ctx, sidecarID, logOptions)
		if err != nil {
			// Don't fail the whole stream — just warn and continue without
			// sidecar output. The user still gets job logs.
			logger.WithError(err).WithField("sidecar_id", sidecarID).Warn("Failed to open builder sidecar logs")
			sidecarLogs = nil
		}
	}

	stdoutReader, stdoutWriter := io.Pipe()
	stderrReader, stderrWriter := io.Pipe()

	// A mutex per pipe keeps line writes from different sources interleaved
	// cleanly — each tagger still has its own buffer, so partial lines from
	// one source don't stomp on the other.
	var stdoutMu, stderrMu sync.Mutex

	jobStdout := newTaggingWriter(stdoutWriter, &stdoutMu, "")
	jobStderr := newTaggingWriter(stderrWriter, &stderrMu, "")

	// Job goroutine. When the job container exits its log stream EOFs, which
	// unblocks this and lets the pipes close so the caller can stop scanning.
	jobDone := make(chan struct{})
	go func() {
		defer close(jobDone)
		defer jobLogs.Close()
		if _, err := stdcopy.StdCopy(jobStdout, jobStderr, jobLogs); err != nil && err != io.EOF {
			logger.WithError(err).Error("Error demultiplexing container logs")
		}
		if tw, ok := jobStdout.(*taggingWriter); ok {
			tw.Flush()
		}
		if tw, ok := jobStderr.(*taggingWriter); ok {
			tw.Flush()
		}
	}()

	// Sidecar goroutine (if any). The buildkitd daemon doesn't EOF on its own
	// — we need to close its log stream externally (below) to unblock this.
	// Until then we forward whatever it writes, tagged with [builder].
	if sidecarLogs != nil {
		sidecarStdout := newTaggingWriter(stdoutWriter, &stdoutMu, BuilderLogPrefix)
		sidecarStderr := newTaggingWriter(stderrWriter, &stderrMu, BuilderLogPrefix)
		go func() {
			defer sidecarLogs.Close()
			if _, err := stdcopy.StdCopy(sidecarStdout, sidecarStderr, sidecarLogs); err != nil && err != io.EOF && err != io.ErrClosedPipe {
				logger.WithError(err).WithField("sidecar_id", sidecarID).Warn("Error demultiplexing builder sidecar logs")
			}
			if tw, ok := sidecarStdout.(*taggingWriter); ok {
				tw.Flush()
			}
			if tw, ok := sidecarStderr.(*taggingWriter); ok {
				tw.Flush()
			}
		}()
	}

	// When the job stream ends, close the output pipes so the caller's
	// scanners see EOF, and close the sidecar log stream so its goroutine
	// can unblock. Any trailing sidecar lines after job exit are best-effort
	// — we prioritize caller progress over flushing buildkitd shutdown noise.
	go func() {
		<-jobDone
		stdoutWriter.Close()
		stderrWriter.Close()
		if sidecarLogs != nil {
			sidecarLogs.Close()
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

// Stop requests a graceful shutdown of the job container: the Docker daemon
// sends SIGTERM to PID 1 and waits up to `grace` before sending SIGKILL.
// grace == 0 skips the wait and kills immediately (Docker's ContainerStop
// with Timeout=0 still sends SIGTERM first, but does not wait for it to take
// effect before following up with SIGKILL).
//
// Any builder sidecar is left running; the caller's subsequent Cleanup()
// call tears down both the job container and its sidecar together.
func (dr *DockerRunner) Stop(ctx context.Context, containerID string, grace time.Duration) error {
	logger := logging.Log.WithField("container_id", containerID).WithField("grace", grace)
	logger.Info("Stopping Docker container")

	timeoutSeconds := int(grace.Seconds())
	err := dr.client.ContainerStop(ctx, containerID, container.StopOptions{Timeout: &timeoutSeconds})
	if err != nil {
		if client.IsErrNotFound(err) {
			// Container already gone (naturally exited + reaped, or already
			// cleaned up) — the caller's WaitForCompletion/Cleanup path
			// handles that race, so this is not an error for Stop.
			logger.Debug("Container not found during Stop, treating as already stopped")
			return nil
		}
		return fmt.Errorf("failed to stop container: %w", err)
	}
	return nil
}

// Cleanup removes the container and any builder sidecar launched for it.
func (dr *DockerRunner) Cleanup(ctx context.Context, containerID string) error {
	logger := logging.Log.WithField("container_id", containerID)

	logger.Info("Cleaning up Docker container")

	removeOptions := container.RemoveOptions{
		RemoveVolumes: true,
		Force:         true,
	}

	// Remove the job container first. The sidecar holds the netns the job
	// attached to, so stopping the job before the sidecar avoids a window
	// where the job briefly loses network before its own exit path runs.
	jobErr := dr.client.ContainerRemove(ctx, containerID, removeOptions)

	dr.sidecarsMu.Lock()
	sidecarID, hadSidecar := dr.sidecars[containerID]
	delete(dr.sidecars, containerID)
	dr.sidecarsMu.Unlock()

	if hadSidecar {
		if err := dr.client.ContainerRemove(ctx, sidecarID, removeOptions); err != nil {
			logger.WithError(err).WithField("sidecar_id", sidecarID).Warn("Failed to remove builder sidecar")
		} else {
			logger.WithField("sidecar_id", sidecarID).Info("Builder sidecar cleaned up")
		}
	}

	if jobErr != nil {
		return fmt.Errorf("failed to remove container: %w", jobErr)
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

// dockerResourceLimits translates JobConfig's Kubernetes-style quantity
// resource fields into Docker's HostConfig resource knobs:
//   - NanoCPUs (nanoseconds of CPU time per second of wall time; 1 full CPU
//     == 1e9) from CPULimit.
//   - CPUShares (Docker's relative scheduling weight, default 1024 per full
//     CPU, documented minimum 2) from CPURequest. Docker has no native CPU
//     reservation distinct from --cpus, so the request is applied
//     advisorily rather than as a hard reservation.
//   - Memory (bytes) from MemoryLimit. Memory is limit-only -- there is no
//     memory request/reservation knob applied here, mirroring JobConfig.
//
// An empty field leaves the corresponding return value at zero (Docker's
// "unset" default); an invalid quantity is logged and skipped the same way
// rather than failing the spawn, matching the pre-existing behavior for
// these fields.
func dockerResourceLimits(config *JobConfig, logger *logrus.Entry) (nanoCPUs int64, cpuShares int64, memoryBytes int64) {
	if config.CPULimit != "" {
		millicores, err := resources.CPUMillicores(config.CPULimit)
		if err != nil {
			logger.WithError(err).Warn("Failed to parse CPU limit, ignoring")
		} else {
			// nanocpus = millicores * 1e6 (1 full CPU == 1e9 nanocpus).
			nanoCPUs = millicores * 1e6
		}
	}

	if config.CPURequest != "" {
		millicores, err := resources.CPUMillicores(config.CPURequest)
		if err != nil {
			logger.WithError(err).Warn("Failed to parse CPU request, ignoring")
		} else {
			shares := millicores * 1024 / 1000
			if shares < 2 {
				shares = 2
			}
			cpuShares = shares
		}
	}

	if config.MemoryLimit != "" {
		memBytes, err := resources.MemoryBytes(config.MemoryLimit)
		if err != nil {
			logger.WithError(err).Warn("Failed to parse memory limit, ignoring")
		} else {
			memoryBytes = memBytes
		}
	}

	return nanoCPUs, cpuShares, memoryBytes
}

// Ensure DockerRunner implements JobRunner interface
var _ JobRunner = (*DockerRunner)(nil)
