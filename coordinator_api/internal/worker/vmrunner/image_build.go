package vmrunner

import (
	"context"
	"io"
	"time"
)

// MacImageBuildOptions configures creation of a derived macOS image from an
// existing bootstrap bundle.
// The source image must contain the guest daemon (cmd/vmguest) as a launchd
// daemon. The build VM has no network device: provisioning egress goes
// through the egress tunnel and EgressForwarder, like a job.
type MacImageBuildOptions struct {
	SourceBundle string
	OutputBundle string
	CPUs         int
	MemoryBytes  int64
	// User is the guest account that runs the provisioning scripts.
	User string
	// GuestDaemon is an optional host path of a darwin/arm64 vmguest binary.
	// The build installs it in the new image before the scripts run, so a
	// derived image can update the guest daemon of its source image.
	GuestDaemon      string
	Scripts          []string
	LogWriter        io.Writer
	AllowUncleanStop bool
}

// BuildMacImage clones a bootstrap image, provisions it in a VM, shuts it
// down, and seals the result as a new bundle.
func BuildMacImage(ctx context.Context, opts MacImageBuildOptions) error {
	return buildMacImage(ctx, opts)
}

// WindowsImageBuildOptions configures creation of a derived Windows image
// from an existing base bundle (disk.vhdx with the guest daemon service).
// The build VM has no network adapter: provisioning egress goes through the
// Hyper-V socket egress tunnel and EgressForwarder, like a job.
type WindowsImageBuildOptions struct {
	SourceBundle string
	OutputBundle string
	CPUs         int
	MemoryBytes  int64
	// User is the guest account that runs the provisioning scripts. The
	// scripts get its elevated token when it is an administrator.
	User string
	// GuestDaemon is an optional host path of a windows/amd64 vmguest.exe.
	// The build copies it into the image before the first boot, so the
	// scripts already run with the new guest daemon.
	GuestDaemon string
	// Scripts are PowerShell scripts that run in order.
	Scripts   []string
	LogWriter io.Writer
	// ShutdownTimeout limits the BitLocker decryption and the guest shutdown.
	// Zero uses 30 minutes.
	ShutdownTimeout time.Duration
}

// BuildWindowsImage boots a differencing disk of a base image with no
// network adapter, runs the provisioning scripts through the guest daemon,
// shuts the guest down cleanly, and writes the result as a new
// standalone base VHDX bundle. It removes the build VM and its disks on
// success and on failure.
func BuildWindowsImage(ctx context.Context, opts WindowsImageBuildOptions) error {
	return buildWindowsImage(ctx, opts)
}
