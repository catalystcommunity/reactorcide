package vmrunner

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/resources"
)

const (
	defaultConnectTimeout       = 90 * time.Second
	defaultConnectRetryInterval = 2 * time.Second
	defaultDestroyTimeout       = 30 * time.Second
)

// JobConfig is vmrunner's own mirror of the worker.JobConfig fields
// relevant to booting and running a guest job. See interfaces.go's package
// doc for why this package doesn't just import worker.JobConfig directly.
// internal/worker/vm_adapter.go is the single place that converts a
// *worker.JobConfig into one of these.
type JobConfig struct {
	// Image is the base VM image reference, resolved via ImageSource.
	Image string

	// Command is the full command to execute in the guest. There is no
	// container-style entrypoint to clear for VM guests -- this runs
	// directly (see VM_RUNNERS_PLAN.md's "Guest execution note").
	Command []string

	// Env is injected into the guest process. May contain secrets/VCS-auth
	// values (the same fields the container runners read from
	// JobConfig.Env) -- implementations must never log these values.
	Env map[string]string

	// CPURequest/CPULimit/MemoryLimit are Kubernetes-style quantity
	// strings, same grammar as worker.JobConfig (see internal/resources).
	// CPULimit wins over CPURequest when mapping to BootSpec.CPUs.
	CPURequest  string
	CPULimit    string
	MemoryLimit string

	// JobID labels the VM/clone for debugging and log correlation.
	JobID string
}

// vmJob is the in-memory record VMRunner keeps per spawned job, keyed by
// the opaque ID SpawnJob returns.
type vmJob struct {
	handle  string
	addr    GuestAddr
	session GuestSession
}

// VMRunner implements a JobRunner-shaped interface (SpawnJob/StreamLogs/
// WaitForCompletion/Stop/Cleanup -- see internal/worker/vm_adapter.go,
// which wraps a *VMRunner to satisfy worker.JobRunner exactly) by composing
// an ImageSource, a VMLifecycle, and a GuestTransport. It has no
// coordinator/lease/store dependency: every method operates purely on the
// three seams plus an in-memory job table, so it behaves identically under
// run-local and under the coordinator-mediated worker.
type VMRunner struct {
	images    ImageSource
	lifecycle VMLifecycle
	transport GuestTransport
	creds     GuestCreds

	connectTimeout       time.Duration
	connectRetryInterval time.Duration

	mu   sync.Mutex
	jobs map[string]*vmJob
}

// Option configures optional VMRunner behavior.
type Option func(*VMRunner)

// WithConnectTimeout overrides how long SpawnJob waits for the booted
// guest's transport port to accept a TCP connection before giving up and
// destroying the VM. Default: 90s.
func WithConnectTimeout(d time.Duration) Option {
	return func(r *VMRunner) { r.connectTimeout = d }
}

// WithConnectRetryInterval overrides the poll interval used while waiting
// for guest reachability. Default: 2s.
func WithConnectRetryInterval(d time.Duration) Option {
	return func(r *VMRunner) { r.connectRetryInterval = d }
}

// New builds a VMRunner from explicit collaborators. Tests use this
// directly with fakes/loopback doubles; NewDefault is the production
// convenience constructor.
func New(images ImageSource, lifecycle VMLifecycle, transport GuestTransport, creds GuestCreds, opts ...Option) *VMRunner {
	r := &VMRunner{
		images:               images,
		lifecycle:            lifecycle,
		transport:            transport,
		creds:                creds,
		connectTimeout:       defaultConnectTimeout,
		connectRetryInterval: defaultConnectRetryInterval,
		jobs:                 make(map[string]*vmJob),
	}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

// NewDefault builds a VMRunner using this platform's VMLifecycle
// (newVMLifecycle -- build-tag selected: errors on Linux today via
// lifecycle_other.go, real once VM-3/VM-4 land on darwin/windows), the SSH
// GuestTransport, and a local pre-placed-file ImageSource rooted at
// baseImageDir. newVMLifecycle's error is returned unchanged so callers
// like worker.NewJobRunner("vm") can surface a clear "not supported on
// this OS" message rather than a generic failure.
//
// NewDefaultWithImages is the same constructor generalized over ImageSource
// -- use it instead when the caller needs to choose LocalImageSource vs.
// image_oci.go's OCIImageSource (or another implementation) itself, as
// internal/worker/vm_adapter.go's REACTORCIDE_VM_IMAGE_SOURCE selection
// does.
func NewDefault(baseImageDir string, creds GuestCreds, opts ...Option) (*VMRunner, error) {
	return NewDefaultWithImages(NewLocalImageSource(baseImageDir), creds, opts...)
}

// NewDefaultWithImages builds a VMRunner the same way NewDefault does --
// this platform's VMLifecycle plus the SSH GuestTransport -- but takes an
// already-constructed ImageSource rather than always building a
// LocalImageSource, so callers can plug in image_oci.go's OCIImageSource
// (or any other ImageSource) without vmrunner needing to know which. See
// NewDefault's doc comment.
func NewDefaultWithImages(images ImageSource, creds GuestCreds, opts ...Option) (*VMRunner, error) {
	lifecycle, err := newVMLifecycle()
	if err != nil {
		return nil, err
	}
	transport := NewSSHTransport()
	return New(images, lifecycle, transport, creds, opts...), nil
}

// SpawnJob resolves the base image, boots a guest VM sized from config's
// CPU/memory resource fields, waits (bounded) for the guest transport port
// to come up, then starts the job command over the transport. It is
// non-blocking with respect to the job command itself -- SpawnJob returns
// as soon as the command has started running in the guest, matching
// DockerRunner/KubernetesRunner's SpawnJob contract.
func (r *VMRunner) SpawnJob(ctx context.Context, config *JobConfig) (string, error) {
	if err := validateJobConfig(config); err != nil {
		return "", fmt.Errorf("invalid job configuration: %w", err)
	}

	basePath, err := r.images.Resolve(ctx, config.Image)
	if err != nil {
		return "", fmt.Errorf("resolve image: %w", err)
	}

	spec := BootSpec{
		CPUs:        cpuCount(config),
		MemoryBytes: memoryLimitBytes(config),
		JobID:       config.JobID,
		Label:       "reactorcide-job-" + config.JobID,
	}

	handle, addr, err := r.lifecycle.Boot(ctx, basePath, spec)
	if err != nil {
		return "", fmt.Errorf("boot vm: %w", err)
	}

	if err := waitTCPReachable(ctx, addr, r.connectTimeout, r.connectRetryInterval); err != nil {
		r.destroyBestEffort(handle)
		return "", fmt.Errorf("guest transport unreachable: %w", err)
	}

	session, err := r.transport.Start(ctx, addr, r.creds, config.Command, config.Env)
	if err != nil {
		r.destroyBestEffort(handle)
		return "", fmt.Errorf("start guest session: %w", err)
	}

	id := uuid.New().String()
	r.mu.Lock()
	r.jobs[id] = &vmJob{handle: handle, addr: addr, session: session}
	r.mu.Unlock()

	return id, nil
}

// StreamLogs returns the guest session's stdout/stderr as ReadClosers. The
// underlying GuestSession owns the real lifecycle of these streams; Close
// here is a no-op wrapper so callers can use the io.ReadCloser contract
// without prematurely tearing down the session (Cleanup does that).
func (r *VMRunner) StreamLogs(ctx context.Context, jobID string) (stdout io.ReadCloser, stderr io.ReadCloser, err error) {
	job, ok := r.getJob(jobID)
	if !ok {
		return nil, nil, fmt.Errorf("vmrunner: unknown job %q", jobID)
	}
	return io.NopCloser(job.session.Stdout()), io.NopCloser(job.session.Stderr()), nil
}

// WaitForCompletion blocks until the guest command exits, or ctx is
// canceled first.
func (r *VMRunner) WaitForCompletion(ctx context.Context, jobID string) (int, error) {
	job, ok := r.getJob(jobID)
	if !ok {
		return -1, fmt.Errorf("vmrunner: unknown job %q", jobID)
	}

	type result struct {
		code int
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		code, err := job.session.Wait()
		ch <- result{code, err}
	}()

	select {
	case <-ctx.Done():
		return -1, ctx.Err()
	case res := <-ch:
		return res.code, res.err
	}
}

// Stop requests a graceful shutdown: signal TERM to the guest command,
// then destroy the VM outright if it hasn't exited within grace. grace <=
// 0 skips straight to Destroy, matching JobRunner.Stop's "grace == 0 means
// immediate forced termination" contract. Stopping an already-gone job is
// a safe no-op (interfaces.go's JobRunner.Stop doc requires runners to
// tolerate this race between the worker's cancel-poll and natural
// completion).
func (r *VMRunner) Stop(ctx context.Context, jobID string, grace time.Duration) error {
	job, ok := r.getJob(jobID)
	if !ok {
		return nil
	}

	if grace <= 0 {
		return r.lifecycle.Destroy(ctx, job.handle)
	}

	// Best-effort: guest_ssh.go's Signal already falls back to a `kill`
	// exec when the SSH-protocol signal request is ignored, so a failure
	// here just means we fall through to the grace-timeout Destroy below.
	_ = job.session.Signal("TERM")

	exited := make(chan struct{})
	go func() {
		job.session.Wait()
		close(exited)
	}()

	select {
	case <-exited:
		return nil
	case <-time.After(grace):
		return r.lifecycle.Destroy(ctx, job.handle)
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Cleanup closes the guest session and destroys the VM, then drops the job
// record. Idempotent: calling Cleanup on an unknown/already-cleaned-up
// jobID is a safe no-op, matching JobRunner.Cleanup's contract.
func (r *VMRunner) Cleanup(ctx context.Context, jobID string) error {
	r.mu.Lock()
	job, ok := r.jobs[jobID]
	delete(r.jobs, jobID)
	r.mu.Unlock()

	if !ok {
		return nil
	}

	if job.session != nil {
		_ = job.session.Close()
	}

	if err := r.lifecycle.Destroy(ctx, job.handle); err != nil {
		return fmt.Errorf("destroy vm: %w", err)
	}
	return nil
}

func (r *VMRunner) getJob(id string) (*vmJob, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	job, ok := r.jobs[id]
	return job, ok
}

// destroyBestEffort tears down a VM whose boot succeeded but whose
// subsequent setup (reachability wait or transport Start) failed. It uses
// its own bounded context rather than the caller's ctx, which may already
// be canceled/expired by the time this runs.
func (r *VMRunner) destroyBestEffort(handle string) {
	ctx, cancel := context.WithTimeout(context.Background(), defaultDestroyTimeout)
	defer cancel()
	_ = r.lifecycle.Destroy(ctx, handle)
}

func validateJobConfig(config *JobConfig) error {
	if config == nil {
		return errors.New("job configuration is required")
	}
	if config.Image == "" {
		return errors.New("base image reference is required")
	}
	if len(config.Command) == 0 {
		return errors.New("command is required")
	}
	if config.JobID == "" {
		return errors.New("job ID is required")
	}
	return nil
}

// cpuCount maps JobConfig's Kubernetes-style CPU quantity strings to a
// whole vCPU count for BootSpec, rounding up (a VM can't be given 1.5
// vCPUs). CPULimit takes precedence over CPURequest, mirroring
// DockerRunner's resource mapping. An empty/unparseable value yields 0,
// which BootSpec documents as "let the lifecycle choose a default".
func cpuCount(config *JobConfig) int {
	q := config.CPULimit
	if q == "" {
		q = config.CPURequest
	}
	if q == "" {
		return 0
	}
	millicores, err := resources.CPUMillicores(q)
	if err != nil || millicores <= 0 {
		return 0
	}
	cpus := int((millicores + 999) / 1000)
	if cpus < 1 {
		cpus = 1
	}
	return cpus
}

// memoryLimitBytes maps JobConfig.MemoryLimit to bytes for BootSpec. An
// empty/unparseable value yields 0 ("let the lifecycle choose a default"),
// matching DockerRunner's tolerant-skip behavior for bad quantities.
func memoryLimitBytes(config *JobConfig) int64 {
	if config.MemoryLimit == "" {
		return 0
	}
	b, err := resources.MemoryBytes(config.MemoryLimit)
	if err != nil {
		return 0
	}
	return b
}

// waitTCPReachable polls addr with plain TCP dials until one succeeds, ctx
// is canceled, or timeout elapses. This is deliberately transport-agnostic
// (it only needs GuestAddr's Host/Port) rather than routed through
// GuestTransport.Start itself, so a guest that's still booting sshd
// doesn't risk SpawnJob accidentally invoking the job command more than
// once via retried Start calls.
func waitTCPReachable(ctx context.Context, addr GuestAddr, timeout, interval time.Duration) error {
	target := net.JoinHostPort(addr.Host, strconv.Itoa(addr.Port))
	deadline := time.Now().Add(timeout)
	var lastErr error

	for {
		d := net.Dialer{Timeout: interval}
		conn, err := d.DialContext(ctx, "tcp", target)
		if err == nil {
			_ = conn.Close()
			return nil
		}
		lastErr = err

		if ctx.Err() != nil {
			return ctx.Err()
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("guest %s not reachable after %s: %w", target, timeout, lastErr)
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(interval):
		}
	}
}
