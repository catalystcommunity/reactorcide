package worker

import (
	"context"
	"io"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

// JobProcessorInterface defines the interface for job processing
type JobProcessorInterface interface {
	ProcessJob(ctx context.Context, job *models.Job) *JobResult
	ProcessJobWithContext(ctx context.Context, job *models.Job, execCtx *JobExecutionContext) *JobResult
}

// Ensure JobProcessor implements JobProcessorInterface
var _ JobProcessorInterface = (*JobProcessor)(nil)

// JobRunner defines the interface for container runtime backends
// This abstraction allows the worker to spawn job containers using different runtimes
// (Docker, containerd, Kubernetes) without changing the core worker logic.
type JobRunner interface {
	// SpawnJob creates and starts a job container with the specified configuration
	// Returns a unique job ID/handle and any error encountered
	SpawnJob(ctx context.Context, config *JobConfig) (string, error)

	// StreamLogs streams stdout/stderr from a running job container
	// Returns separate readers for stdout and stderr
	StreamLogs(ctx context.Context, jobID string) (stdout io.ReadCloser, stderr io.ReadCloser, err error)

	// WaitForCompletion blocks until the job container exits
	// Returns the exit code and any error encountered
	WaitForCompletion(ctx context.Context, jobID string) (int, error)

	// Cleanup removes the job container and associated resources
	// Should be called after the job completes (success or failure)
	Cleanup(ctx context.Context, jobID string) error
}

// Capability constants for job requirements
const (
	// CapabilityDocker provides access to build/push container images.
	// DockerRunner: mounts /var/run/docker.sock
	// KubernetesRunner: uses DinD sidecar or hostPath mount
	CapabilityDocker = "docker"

	// CapabilityGPU provides access to GPU resources.
	// NOT YET IMPLEMENTED - placeholder for future development.
	// DockerRunner: would use --gpus all flag
	// KubernetesRunner: would add nvidia.com/gpu resource request
	CapabilityGPU = "gpu"
)

// JobConfig contains all the configuration needed to spawn a job container.
// The container's entrypoint is always cleared - the Command field contains
// the full command to execute. Users can run their own commands or invoke
// runnerlib for source preparation and lifecycle hooks.
type JobConfig struct {
	// Container image to use (e.g., "reactorcide/runner:latest")
	Image string

	// Command to execute in the container. The container's entrypoint is
	// cleared, so this is the full command (e.g., ["sh", "-c", "make build"])
	Command []string

	// Environment variables to inject into the container
	Env map[string]string

	// WorkspaceDir is the host directory to mount into the container at /job
	WorkspaceDir string

	// WorkingDir is the working directory inside the container (default: /job)
	WorkingDir string

	// Capabilities declares what the job needs from the runtime environment.
	// Each runner interprets these appropriately for its environment.
	// See CapabilityDocker, CapabilityGPU constants.
	Capabilities []string

	// Timeout for the job execution (0 = no timeout)
	TimeoutSeconds int

	// Resource limits
	CPULimit    string // e.g., "1.0" for 1 CPU
	MemoryLimit string // e.g., "512Mi" or "1Gi"

	// Job metadata (for labeling/tagging)
	JobID     string
	QueueName string
}
