// Package vmrunner implements the "vm" JobRunner backend: ephemeral, per-job
// guest VMs used to run native macOS/Windows jobs on host workers.
//
// VMRunner composes three seams so the platform-specific VM lifecycle code
// (Virtualization.framework on darwin, Hyper-V on windows) stays isolated
// behind build tags while everything else -- orchestration, the SSH guest
// channel, and image resolution -- is cross-platform and testable on Linux:
//
//   - ImageSource resolves an image reference to a local base image file.
//     The package provides local and OCI-backed implementations.
//   - VMLifecycle clones the base image and boots/destroys the guest. It is
//     build-tag selected via newVMLifecycle() (lifecycle_other.go /
//     lifecycle_darwin.go / lifecycle_windows.go) and is the only piece of
//     this package that differs per host OS.
//   - GuestTransport reaches into the running guest to start a command and
//     stream its output back. guest_ssh.go ships the SSH implementation;
//     a vsock-based transport could implement the same interface later
//     without touching VMRunner.
//
// This package intentionally does NOT import internal/worker (or anything
// coordinator/lease/store-shaped): internal/worker/runner_factory.go needs to
// import vmrunner to construct the "vm" backend, so vmrunner importing worker
// back would be a cycle. JobConfig here is vmrunner's own mirror of
// worker.JobConfig's relevant fields; internal/worker/vm_adapter.go
// translates between the two at the one call site that needs both types.
// Keeping vmrunner free of that dependency also keeps the promise that
// VMRunner is a pure JobRunner-shaped type usable identically from run-local
// and the coordinator-mediated worker.
package vmrunner

import (
	"context"
	"io"
	"io/fs"
)

// ImageSource resolves an image reference to a local, read-only base image
// path that VMLifecycle.Boot clones from. Implementations pull/cache by
// content (a registry digest, or file identity for a local path) so repeated
// Resolve calls for the same ref are cheap.
type ImageSource interface {
	// Resolve pulls/caches the base image referenced by imageRef and returns
	// its local path. imageRef is whatever JobConfig.Image the job specifies
	// -- for LocalImageSource that's a file path (absolute, or relative to a
	// configured base dir); OCIImageSource treats it as an OCI
	// artifact ref.
	Resolve(ctx context.Context, imageRef string) (baseImagePath string, err error)
}

// BootSpec carries what a VMLifecycle needs to boot a guest for one job.
// CPUs/MemoryBytes are mapped from JobConfig's CPU/memory resource fields
// (see cpuCount/memoryLimitBytes in vm_runner.go); a zero value lets the
// lifecycle apply its own default.
type BootSpec struct {
	// CPUs is the number of virtual CPUs to give the guest. 0 means "let the
	// lifecycle choose a default".
	CPUs int

	// MemoryBytes is the guest memory ceiling in bytes. 0 means "let the
	// lifecycle choose a default".
	MemoryBytes int64

	// JobID labels the clone/VM for debugging, leak sweeps, and log
	// correlation -- the VM-equivalent of DockerRunner's "reactorcide.job_id"
	// container label.
	JobID string

	// Label is a human-readable name for the clone/VM (e.g.
	// "reactorcide-job-<id>"), mirroring DockerRunner's container name.
	Label string
}

// GuestAddr is how the host reaches a booted guest's transport endpoint. Both
// fields are required for any transport dialed over TCP (SSH today); a future
// vsock-based transport could repurpose Host as a CID.
type GuestAddr struct {
	Host string
	Port int
}

// VMLifecycle clones a base image and boots/destroys the resulting guest VM.
// It is the one seam that is genuinely platform-specific (Apple
// Virtualization.framework / Hyper-V) and is therefore build-tag selected via
// newVMLifecycle() rather than constructed directly by callers.
type VMLifecycle interface {
	// Boot copy-on-write clones baseImagePath and boots the guest described
	// by spec, blocking until the returned handle is valid (the guest need
	// not be reachable yet -- VMRunner.SpawnJob separately polls guest
	// reachability before starting the job command). handle is an opaque,
	// implementation-defined identifier Destroy uses to tear the guest back
	// down.
	Boot(ctx context.Context, baseImagePath string, spec BootSpec) (handle string, guest GuestAddr, err error)

	// Destroy force-kills the guest identified by handle and removes its
	// clone/scratch storage. Must tolerate being called on an
	// already-destroyed or unknown handle (idempotent, safe no-op) since
	// VMRunner may call it from more than one path (a failed boot's cleanup,
	// Stop's grace timeout, and Cleanup can race in principle).
	Destroy(ctx context.Context, handle string) error
}

// GuestCreds carries whatever a GuestTransport needs to authenticate to the
// guest. The SSH implementation (guest_ssh.go) uses User plus one of
// Password/PrivateKeyPEM; a different transport can add or ignore fields.
type GuestCreds struct {
	// User is the guest OS account the transport authenticates as.
	User string

	// Password authenticates via SSH password auth when set.
	Password string

	// PrivateKeyPEM authenticates via SSH public-key auth when set (PEM
	// encoded, unencrypted). Takes precedence over Password when both are
	// set.
	PrivateKeyPEM []byte

	// HostPublicKey is an authorized_keys-format public key for the guest SSH
	// server. When it is empty, the transport accepts the key from the private
	// host-to-guest network.
	HostPublicKey []byte
}

// GuestPlatform selects the command bootstrap syntax used inside a guest.
type GuestPlatform string

const (
	GuestPlatformPOSIX   GuestPlatform = "posix"
	GuestPlatformWindows GuestPlatform = "windows"
)

// GuestFile is a file that the transport creates before it starts the job.
// Data can contain credentials and must never be logged.
type GuestFile struct {
	Path string
	Data []byte
	Mode fs.FileMode
}

// GuestTree is a host directory that the transport copies into the guest.
// The transport must stream the directory and must not load the full tree into
// memory. SourcePath can contain credentials and must never be logged.
type GuestTree struct {
	SourcePath  string
	Destination string
}

// GuestCommand contains all process inputs that must cross the guest
// isolation boundary.
type GuestCommand struct {
	Platform   GuestPlatform
	Args       []string
	Env        map[string]string
	WorkingDir string
	Files      []GuestFile
	Trees      []GuestTree
}

// GuestTransport starts a command inside an already-booted guest and streams
// its output back.
type GuestTransport interface {
	// Start creates command files and starts the process. Environment values
	// and file data can contain secrets. Implementations must not log them.
	Start(ctx context.Context, addr GuestAddr, creds GuestCreds, command GuestCommand) (GuestSession, error)
}

// GuestSession represents one running guest command. Wait, Stdout, and Stderr
// are all safe to use concurrently (e.g. StreamLogs's readers next to a
// separate WaitForCompletion caller), and Wait is safe to call more than once
// (implementations must memoize the result) since both VMRunner.Stop's grace
// check and WaitForCompletion may call it.
type GuestSession interface {
	// Stdout returns the guest command's stdout stream. Valid to read from
	// until Close.
	Stdout() io.Reader

	// Stderr returns the guest command's stderr stream. Valid to read from
	// until Close.
	Stderr() io.Reader

	// Wait blocks until the guest command exits and returns its exit code.
	// Safe to call from multiple goroutines/multiple times -- all callers
	// observe the same result once the command has actually exited.
	Wait() (exitCode int, err error)

	// Signal requests the guest command terminate via sig (a POSIX signal
	// name without the "SIG" prefix, e.g. "TERM", "KILL"). Best-effort: see
	// guest_ssh.go's doc comment for why the SSH implementation also falls
	// back to an explicit `kill` rather than relying solely on the SSH
	// protocol's signal request.
	Signal(sig string) error

	// Close releases transport-level resources (the SSH session/client
	// connection). It does not affect the guest VM itself -- callers still
	// call VMLifecycle.Destroy separately. Safe to call more than once.
	Close() error
}
