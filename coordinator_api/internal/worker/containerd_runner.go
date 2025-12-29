package worker

import (
	"context"
	"fmt"
	"io"
)

// ContainerdRunner implements JobRunner using containerd/nerdctl
// This is a stub implementation - full implementation coming in future phase
type ContainerdRunner struct {
	// TODO: Add containerd client when implementing
}

// NewContainerdRunner creates a new containerd-based job runner
// Currently returns a stub implementation
func NewContainerdRunner() (*ContainerdRunner, error) {
	return &ContainerdRunner{}, nil
}

// SpawnJob creates and starts a containerd container for the job
func (cr *ContainerdRunner) SpawnJob(ctx context.Context, config *JobConfig) (string, error) {
	return "", fmt.Errorf("ContainerdRunner not yet implemented - use DockerRunner instead")
}

// StreamLogs streams stdout and stderr from the container
func (cr *ContainerdRunner) StreamLogs(ctx context.Context, containerID string) (stdout io.ReadCloser, stderr io.ReadCloser, err error) {
	return nil, nil, fmt.Errorf("ContainerdRunner not yet implemented - use DockerRunner instead")
}

// WaitForCompletion waits for the container to exit and returns the exit code
func (cr *ContainerdRunner) WaitForCompletion(ctx context.Context, containerID string) (int, error) {
	return -1, fmt.Errorf("ContainerdRunner not yet implemented - use DockerRunner instead")
}

// Cleanup removes the container and associated resources
func (cr *ContainerdRunner) Cleanup(ctx context.Context, containerID string) error {
	return fmt.Errorf("ContainerdRunner not yet implemented - use DockerRunner instead")
}

// Ensure ContainerdRunner implements JobRunner interface
var _ JobRunner = (*ContainerdRunner)(nil)
