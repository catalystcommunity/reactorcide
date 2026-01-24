package worker

import (
	"fmt"
	"strings"

	"github.com/catalystcommunity/app-utils-go/logging"
)

// RunnerBackend represents the container runtime backend to use
type RunnerBackend string

const (
	// BackendDocker uses the Docker daemon
	BackendDocker RunnerBackend = "docker"

	// BackendContainerd uses containerd via nerdctl
	BackendContainerd RunnerBackend = "containerd"

	// BackendKubernetes uses Kubernetes Jobs
	BackendKubernetes RunnerBackend = "kubernetes"

	// BackendAuto automatically detects the best backend
	BackendAuto RunnerBackend = "auto"
)

// NewJobRunner creates a new JobRunner based on the specified backend
// Supported backends: "docker", "containerd", "kubernetes", "auto"
// "auto" will detect if running in Kubernetes and use that, otherwise Docker
func NewJobRunner(backend string) (JobRunner, error) {
	// Normalize backend string (lowercase, trim whitespace)
	backend = strings.ToLower(strings.TrimSpace(backend))

	// Handle auto-detection
	if backend == "" || backend == string(BackendAuto) {
		return NewJobRunnerAuto()
	}

	switch RunnerBackend(backend) {
	case BackendDocker:
		return NewDockerRunner()

	case BackendContainerd:
		return NewContainerdRunner()

	case BackendKubernetes:
		return NewKubernetesRunner()

	default:
		return nil, fmt.Errorf("unsupported job runner backend: %s (supported: docker, containerd, kubernetes, auto)", backend)
	}
}

// NewJobRunnerAuto automatically detects the best runner backend
// It checks if running in Kubernetes first, then falls back to Docker
func NewJobRunnerAuto() (JobRunner, error) {
	logger := logging.Log

	// Check if running in Kubernetes
	if IsKubernetesEnvironment() {
		logger.Info("Detected Kubernetes environment, using Kubernetes Jobs runner")
		runner, err := NewKubernetesRunner()
		if err != nil {
			logger.WithError(err).Warn("Failed to create Kubernetes runner, falling back to Docker")
		} else {
			return runner, nil
		}
	}

	// Fall back to Docker
	logger.Info("Using Docker runner")
	return NewDockerRunner()
}

// GetSupportedBackends returns a list of all supported runner backends
func GetSupportedBackends() []RunnerBackend {
	return []RunnerBackend{
		BackendAuto,
		BackendDocker,
		BackendContainerd,
		BackendKubernetes,
	}
}

// IsBackendSupported checks if a backend is supported (though may not be fully implemented)
func IsBackendSupported(backend string) bool {
	backend = strings.ToLower(strings.TrimSpace(backend))
	for _, supported := range GetSupportedBackends() {
		if string(supported) == backend {
			return true
		}
	}
	return false
}

// IsBackendImplemented checks if a backend is fully implemented (not just stubbed)
func IsBackendImplemented(backend string) bool {
	backend = strings.ToLower(strings.TrimSpace(backend))
	// Docker, Containerd, and Kubernetes are fully implemented
	return backend == string(BackendDocker) ||
		backend == string(BackendContainerd) ||
		backend == string(BackendKubernetes) ||
		backend == string(BackendAuto)
}
