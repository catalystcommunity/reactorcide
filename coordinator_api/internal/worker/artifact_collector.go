package worker

import "context"

// ArtifactCollector is an optional runner capability for backends whose job
// workspace is not mounted on the worker host (WP15,
// docs/artifacts-and-tests.md). The coordinator worker calls
// CollectArtifacts after WaitForCompletion and before Cleanup. It copies the
// regular files under /job/artifacts of the job into destDir and reads at
// most maxBytes bytes. It never copies a symbolic link or a device.
type ArtifactCollector interface {
	CollectArtifacts(ctx context.Context, jobID, destDir string, maxBytes int64) error
}

// NoHostWorkspace marks a runner that cannot give a job the files of the
// worker workspace and cannot return the job artifacts (the VM runner). The
// coordinator worker then gives the job no artifacts directory and no
// quarantined tests file.
type NoHostWorkspace interface {
	NoHostWorkspace() bool
}

// NoHostWorkspace reports that the VM guest does not see the worker
// workspace. The VM runner copies named result files only.
func (a *vmRunnerAdapter) NoHostWorkspace() bool { return true }
