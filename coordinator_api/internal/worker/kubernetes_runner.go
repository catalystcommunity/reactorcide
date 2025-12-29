package worker

import (
	"context"
	"fmt"
	"io"
)

// KubernetesRunner implements JobRunner using Kubernetes Jobs
// This is a stub implementation - full implementation coming in future phase
//
// Capability handling (when implemented):
//   - CapabilityDocker: Will use one of:
//     * Docker-in-Docker (DinD) sidecar container with shared emptyDir volume
//     * hostPath mount to node's Docker socket (less secure, simpler)
//     * Kaniko executor for rootless builds (most secure)
//   - CapabilityGPU: Will add to pod spec:
//     * resources.limits["nvidia.com/gpu"] = "1"
//     * Requires nvidia-device-plugin DaemonSet on cluster
type KubernetesRunner struct {
	// TODO: Add Kubernetes client when implementing
	// kubernetes.Clientset
	// namespace string
	// serviceAccount string
}

// NewKubernetesRunner creates a new Kubernetes-based job runner
// Currently returns a stub implementation
func NewKubernetesRunner() (*KubernetesRunner, error) {
	return &KubernetesRunner{}, nil
}

// SpawnJob creates and starts a Kubernetes Job resource
func (kr *KubernetesRunner) SpawnJob(ctx context.Context, config *JobConfig) (string, error) {
	return "", fmt.Errorf("KubernetesRunner not yet implemented - use DockerRunner instead")
}

// StreamLogs streams stdout and stderr from the job pod
func (kr *KubernetesRunner) StreamLogs(ctx context.Context, jobID string) (stdout io.ReadCloser, stderr io.ReadCloser, err error) {
	return nil, nil, fmt.Errorf("KubernetesRunner not yet implemented - use DockerRunner instead")
}

// WaitForCompletion waits for the Kubernetes Job to complete and returns the exit code
func (kr *KubernetesRunner) WaitForCompletion(ctx context.Context, jobID string) (int, error) {
	return -1, fmt.Errorf("KubernetesRunner not yet implemented - use DockerRunner instead")
}

// Cleanup removes the Kubernetes Job resource
func (kr *KubernetesRunner) Cleanup(ctx context.Context, jobID string) error {
	return fmt.Errorf("KubernetesRunner not yet implemented - use DockerRunner instead")
}

// Ensure KubernetesRunner implements JobRunner interface
var _ JobRunner = (*KubernetesRunner)(nil)
