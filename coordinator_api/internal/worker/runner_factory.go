package worker

import (
	"fmt"
	"strings"
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
)

// NewJobRunner creates a new JobRunner based on the specified backend
// Supported backends: "docker", "containerd", "kubernetes"
func NewJobRunner(backend string) (JobRunner, error) {
	// Normalize backend string (lowercase, trim whitespace)
	backend = strings.ToLower(strings.TrimSpace(backend))

	switch RunnerBackend(backend) {
	case BackendDocker:
		return NewDockerRunner()

	case BackendContainerd:
		return NewContainerdRunner()

	case BackendKubernetes:
		return NewKubernetesRunner()

	default:
		return nil, fmt.Errorf("unsupported job runner backend: %s (supported: docker, containerd, kubernetes)", backend)
	}
}

// GetSupportedBackends returns a list of all supported runner backends
func GetSupportedBackends() []RunnerBackend {
	return []RunnerBackend{
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
	// Currently only Docker is fully implemented
	return backend == string(BackendDocker)
}
