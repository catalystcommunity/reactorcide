package vmrunner

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestCredentialsForImageUsesBundledWindowsHostKey(t *testing.T) {
	bundle := t.TempDir()
	hostKey := []byte("ssh-ed25519 bundled-key\n")
	if err := os.WriteFile(filepath.Join(bundle, BundleWindowsHostKey), hostKey, 0o600); err != nil {
		t.Fatal(err)
	}
	runner := New(nil, nil, nil, GuestCreds{User: "reactorcide"})
	creds, err := runner.credentialsForImage(bundle)
	if err != nil {
		t.Fatal(err)
	}
	if string(creds.HostPublicKey) != string(hostKey) {
		t.Fatalf("HostPublicKey = %q", creds.HostPublicKey)
	}
}

func TestCredentialsForImageRejectsConfiguredHostKeyMismatch(t *testing.T) {
	bundle := t.TempDir()
	if err := os.WriteFile(filepath.Join(bundle, BundleWindowsHostKey), []byte("ssh-ed25519 bundled"), 0o600); err != nil {
		t.Fatal(err)
	}
	runner := New(nil, nil, nil, GuestCreds{HostPublicKey: []byte("ssh-ed25519 configured")})
	if _, err := runner.credentialsForImage(bundle); err == nil {
		t.Fatal("expected host key mismatch")
	}
}

// fakeImageSource records Resolve calls and returns a configured
// path/error, standing in for a real ImageSource (local or, eventually,
// OCI) in orchestration tests that only care about VMRunner's call
// sequencing.
type fakeImageSource struct {
	mu       sync.Mutex
	resolved []string
	path     string
	err      error
}

func (f *fakeImageSource) Resolve(ctx context.Context, imageRef string) (string, error) {
	f.mu.Lock()
	f.resolved = append(f.resolved, imageRef)
	f.mu.Unlock()
	if f.err != nil {
		return "", f.err
	}
	return f.path, nil
}

// fakeLifecycle records Boot/Destroy calls and returns configured
// results, so orchestration tests can assert VMRunner drives
// ImageSource->VMLifecycle->GuestTransport in the right order and cleans
// up on failure without needing a real platform VMLifecycle.
type fakeLifecycle struct {
	mu sync.Mutex

	bootBaseImagePaths []string
	bootSpecs          []BootSpec
	bootHandle         string
	bootAddr           GuestAddr
	bootErr            error

	destroyedHandles []string
	destroyErr       error
}

func (f *fakeLifecycle) Boot(ctx context.Context, baseImagePath string, spec BootSpec) (string, GuestAddr, error) {
	f.mu.Lock()
	f.bootBaseImagePaths = append(f.bootBaseImagePaths, baseImagePath)
	f.bootSpecs = append(f.bootSpecs, spec)
	f.mu.Unlock()
	if f.bootErr != nil {
		return "", GuestAddr{}, f.bootErr
	}
	return f.bootHandle, f.bootAddr, nil
}

func (f *fakeLifecycle) Destroy(ctx context.Context, handle string) error {
	f.mu.Lock()
	f.destroyedHandles = append(f.destroyedHandles, handle)
	f.mu.Unlock()
	return f.destroyErr
}

func (f *fakeLifecycle) destroyCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.destroyedHandles)
}

// fakeSession is a controllable GuestSession: Wait blocks on waitCh (so
// tests can simulate a still-running job), Signal is recorded and can be
// configured to simulate the guest process actually exiting in response.
type fakeSession struct {
	mu sync.Mutex

	stdout string
	stderr string

	exitCode int
	exitErr  error
	waitCh   chan struct{}
	waitOnce sync.Once

	signals []string
	closed  bool

	// signalExits, when true, makes Signal close waitCh itself (simulating
	// a guest process that actually terminates in response to the
	// signal) instead of leaving Wait blocked until grace elapses.
	signalExits bool
}

func newFakeSession(stdout, stderr string, exitCode int) *fakeSession {
	s := &fakeSession{stdout: stdout, stderr: stderr, exitCode: exitCode, waitCh: make(chan struct{})}
	close(s.waitCh) // "already exited" by default -- most tests want this.
	return s
}

func newBlockedFakeSession() *fakeSession {
	return &fakeSession{waitCh: make(chan struct{})}
}

func (s *fakeSession) Stdout() io.Reader { return strings.NewReader(s.stdout) }
func (s *fakeSession) Stderr() io.Reader { return strings.NewReader(s.stderr) }

func (s *fakeSession) Wait() (int, error) {
	<-s.waitCh
	return s.exitCode, s.exitErr
}

func (s *fakeSession) Signal(sig string) error {
	s.mu.Lock()
	s.signals = append(s.signals, sig)
	shouldExit := s.signalExits
	s.mu.Unlock()
	if shouldExit {
		s.waitOnce.Do(func() { close(s.waitCh) })
	}
	return nil
}

func (s *fakeSession) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	return nil
}

func (s *fakeSession) signalCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.signals)
}

func (s *fakeSession) isClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

var _ GuestSession = (*fakeSession)(nil)

// fakeTransport hands back pre-configured fakeSessions (or an error) and
// records every Start call's cmd/env so tests can assert what VMRunner
// passed through.
type fakeTransport struct {
	mu sync.Mutex

	starts []fakeStartCall
	next   GuestSession
	err    error
}

type fakeStartCall struct {
	addr    GuestAddr
	command GuestCommand
}

func (f *fakeTransport) Start(ctx context.Context, addr GuestAddr, creds GuestCreds, command GuestCommand) (GuestSession, error) {
	f.mu.Lock()
	f.starts = append(f.starts, fakeStartCall{addr: addr, command: command})
	f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	return f.next, nil
}

func (f *fakeTransport) startCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.starts)
}

var _ GuestTransport = (*fakeTransport)(nil)

func baseTestConfig() *JobConfig {
	return &JobConfig{
		Image:    "test-base.img",
		Command:  []string{"sh", "-c", "echo hi"},
		Env:      map[string]string{"FOO": "bar"},
		Platform: GuestPlatformPOSIX,
		JobID:    "job-1",
	}
}

// newTestRunner wires a VMRunner from fakes with a fast connect-retry loop
// so waitTCPReachable-driven tests run quickly. Most tests point
// lifecycle.bootAddr at a real (currently listening) loopback port via
// listenLoopback so the reachability wait succeeds on its first attempt.
func newTestRunner(images ImageSource, lifecycle VMLifecycle, transport GuestTransport) *VMRunner {
	return New(images, lifecycle, transport, GuestCreds{User: "runner"},
		WithConnectTimeout(2*time.Second),
		WithConnectRetryInterval(20*time.Millisecond))
}

// listenLoopback binds a real loopback TCP listener and returns the
// GuestAddr for it (a stand-in "guest" endpoint) plus a cleanup func. This
// lets SpawnJob's waitTCPReachable poll succeed immediately without a real
// VM or SSH handshake, keeping these tests focused on VMRunner's
// orchestration rather than the transport (guest_ssh_test.go covers that).
func listenLoopback(t *testing.T) (GuestAddr, func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("skipping: could not create a loopback listener: %v", err)
	}
	addr := ln.Addr().(*net.TCPAddr)
	return GuestAddr{Host: "127.0.0.1", Port: addr.Port}, func() { _ = ln.Close() }
}

func TestVMRunner_SpawnJob_StreamLogs_WaitForCompletion(t *testing.T) {
	guestAddr, cleanup := listenLoopback(t)
	defer cleanup()

	images := &fakeImageSource{path: "/base/test-base.img"}
	lifecycle := &fakeLifecycle{bootHandle: "vm-handle-1", bootAddr: guestAddr}
	session := newFakeSession("stdout-data", "stderr-data", 3)
	transport := &fakeTransport{next: session}

	runner := newTestRunner(images, lifecycle, transport)

	id, err := runner.SpawnJob(context.Background(), baseTestConfig())
	if err != nil {
		t.Fatalf("SpawnJob: %v", err)
	}
	if id == "" {
		t.Fatal("SpawnJob returned empty id")
	}

	if got := images.resolved; len(got) != 1 || got[0] != "test-base.img" {
		t.Fatalf("images.resolved = %v, want [test-base.img]", got)
	}
	if len(lifecycle.bootBaseImagePaths) != 1 || lifecycle.bootBaseImagePaths[0] != "/base/test-base.img" {
		t.Fatalf("lifecycle.Boot base path = %v, want [/base/test-base.img]", lifecycle.bootBaseImagePaths)
	}
	if transport.startCount() != 1 {
		t.Fatalf("transport.Start called %d times, want 1", transport.startCount())
	}
	if got := transport.starts[0].command.Env["FOO"]; got != "bar" {
		t.Fatalf("Start env FOO = %q, want %q", got, "bar")
	}

	stdout, stderr, err := runner.StreamLogs(context.Background(), id)
	if err != nil {
		t.Fatalf("StreamLogs: %v", err)
	}
	defer stdout.Close()
	defer stderr.Close()

	stdoutBytes, err := io.ReadAll(stdout)
	if err != nil {
		t.Fatalf("read stdout: %v", err)
	}
	if string(stdoutBytes) != "stdout-data" {
		t.Fatalf("stdout = %q, want %q", stdoutBytes, "stdout-data")
	}
	stderrBytes, err := io.ReadAll(stderr)
	if err != nil {
		t.Fatalf("read stderr: %v", err)
	}
	if string(stderrBytes) != "stderr-data" {
		t.Fatalf("stderr = %q, want %q", stderrBytes, "stderr-data")
	}

	code, err := runner.WaitForCompletion(context.Background(), id)
	if err != nil {
		t.Fatalf("WaitForCompletion: %v", err)
	}
	if code != 3 {
		t.Fatalf("exit code = %d, want 3", code)
	}
}

func TestVMRunner_SpawnJob_BootFailure_ReturnsError(t *testing.T) {
	images := &fakeImageSource{path: "/base/test-base.img"}
	lifecycle := &fakeLifecycle{bootErr: errors.New("boom")}
	transport := &fakeTransport{}
	runner := newTestRunner(images, lifecycle, transport)

	_, err := runner.SpawnJob(context.Background(), baseTestConfig())
	if err == nil {
		t.Fatal("expected error from failed Boot")
	}
	if transport.startCount() != 0 {
		t.Fatalf("transport.Start should not be called when Boot fails, got %d calls", transport.startCount())
	}
}

func TestVMRunner_SpawnJob_UnreachableGuest_DestroysVM(t *testing.T) {
	images := &fakeImageSource{path: "/base/test-base.img"}
	// Port 1 on loopback: nothing listens there, so waitTCPReachable
	// reliably fails without depending on the platform's connection-
	// refused timing beyond our own bounded retry/timeout.
	lifecycle := &fakeLifecycle{bootHandle: "vm-handle-2", bootAddr: GuestAddr{Host: "127.0.0.1", Port: 1}}
	transport := &fakeTransport{}
	runner := New(images, lifecycle, transport, GuestCreds{},
		WithConnectTimeout(150*time.Millisecond),
		WithConnectRetryInterval(20*time.Millisecond))

	_, err := runner.SpawnJob(context.Background(), baseTestConfig())
	if err == nil {
		t.Fatal("expected error when guest never becomes reachable")
	}
	if transport.startCount() != 0 {
		t.Fatalf("transport.Start should not be called when guest is unreachable, got %d calls", transport.startCount())
	}
	if lifecycle.destroyCount() != 1 {
		t.Fatalf("Destroy called %d times, want 1 (cleanup after unreachable guest)", lifecycle.destroyCount())
	}
}

func TestVMRunner_SpawnJob_TransportStartFailure_DestroysVM(t *testing.T) {
	guestAddr, cleanup := listenLoopback(t)
	defer cleanup()

	images := &fakeImageSource{path: "/base/test-base.img"}
	lifecycle := &fakeLifecycle{bootHandle: "vm-handle-3", bootAddr: guestAddr}
	transport := &fakeTransport{err: errors.New("ssh auth failed")}
	runner := newTestRunner(images, lifecycle, transport)

	_, err := runner.SpawnJob(context.Background(), baseTestConfig())
	if err == nil {
		t.Fatal("expected error when transport.Start fails")
	}
	if lifecycle.destroyCount() != 1 {
		t.Fatalf("Destroy called %d times, want 1 (cleanup after Start failure)", lifecycle.destroyCount())
	}
}

func TestVMRunner_Stop_SignalsThenDestroysAfterGrace(t *testing.T) {
	guestAddr, cleanup := listenLoopback(t)
	defer cleanup()

	images := &fakeImageSource{path: "/base/test-base.img"}
	lifecycle := &fakeLifecycle{bootHandle: "vm-handle-4", bootAddr: guestAddr}
	session := newBlockedFakeSession() // never exits on its own
	transport := &fakeTransport{next: session}
	runner := newTestRunner(images, lifecycle, transport)

	id, err := runner.SpawnJob(context.Background(), baseTestConfig())
	if err != nil {
		t.Fatalf("SpawnJob: %v", err)
	}

	start := time.Now()
	if err := runner.Stop(context.Background(), id, 100*time.Millisecond); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	elapsed := time.Since(start)

	if session.signalCount() != 1 {
		t.Fatalf("session.Signal called %d times, want 1", session.signalCount())
	}
	if lifecycle.destroyCount() != 1 {
		t.Fatalf("Destroy called %d times, want 1 (grace elapsed without exit)", lifecycle.destroyCount())
	}
	if elapsed < 100*time.Millisecond {
		t.Fatalf("Stop returned after %s, want it to wait out the grace period", elapsed)
	}
}

func TestVMRunner_Stop_ExitsWithinGrace_NoDestroy(t *testing.T) {
	guestAddr, cleanup := listenLoopback(t)
	defer cleanup()

	images := &fakeImageSource{path: "/base/test-base.img"}
	lifecycle := &fakeLifecycle{bootHandle: "vm-handle-5", bootAddr: guestAddr}
	session := newBlockedFakeSession()
	session.signalExits = true // simulate the guest process exiting on TERM
	transport := &fakeTransport{next: session}
	runner := newTestRunner(images, lifecycle, transport)

	id, err := runner.SpawnJob(context.Background(), baseTestConfig())
	if err != nil {
		t.Fatalf("SpawnJob: %v", err)
	}

	if err := runner.Stop(context.Background(), id, 5*time.Second); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if lifecycle.destroyCount() != 0 {
		t.Fatalf("Destroy called %d times, want 0 (process exited within grace)", lifecycle.destroyCount())
	}
}

func TestVMRunner_Stop_ZeroGrace_DestroysImmediatelyWithoutSignal(t *testing.T) {
	guestAddr, cleanup := listenLoopback(t)
	defer cleanup()

	images := &fakeImageSource{path: "/base/test-base.img"}
	lifecycle := &fakeLifecycle{bootHandle: "vm-handle-6", bootAddr: guestAddr}
	session := newBlockedFakeSession()
	transport := &fakeTransport{next: session}
	runner := newTestRunner(images, lifecycle, transport)

	id, err := runner.SpawnJob(context.Background(), baseTestConfig())
	if err != nil {
		t.Fatalf("SpawnJob: %v", err)
	}

	if err := runner.Stop(context.Background(), id, 0); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if lifecycle.destroyCount() != 1 {
		t.Fatalf("Destroy called %d times, want 1", lifecycle.destroyCount())
	}
	if session.signalCount() != 0 {
		t.Fatalf("Signal called %d times, want 0 (grace==0 skips straight to Destroy)", session.signalCount())
	}
}

func TestVMRunner_Stop_UnknownJob_IsSafeNoOp(t *testing.T) {
	lifecycle := &fakeLifecycle{}
	runner := newTestRunner(&fakeImageSource{}, lifecycle, &fakeTransport{})

	if err := runner.Stop(context.Background(), "does-not-exist", time.Second); err != nil {
		t.Fatalf("Stop on unknown job: %v", err)
	}
	if lifecycle.destroyCount() != 0 {
		t.Fatalf("Destroy called %d times, want 0", lifecycle.destroyCount())
	}
}

func TestVMRunner_Cleanup_DestroysAndIsIdempotent(t *testing.T) {
	guestAddr, cleanup := listenLoopback(t)
	defer cleanup()

	images := &fakeImageSource{path: "/base/test-base.img"}
	lifecycle := &fakeLifecycle{bootHandle: "vm-handle-7", bootAddr: guestAddr}
	session := newFakeSession("", "", 0)
	transport := &fakeTransport{next: session}
	runner := newTestRunner(images, lifecycle, transport)

	id, err := runner.SpawnJob(context.Background(), baseTestConfig())
	if err != nil {
		t.Fatalf("SpawnJob: %v", err)
	}

	if err := runner.Cleanup(context.Background(), id); err != nil {
		t.Fatalf("Cleanup: %v", err)
	}
	if !session.isClosed() {
		t.Fatal("expected session.Close to have been called")
	}
	if lifecycle.destroyCount() != 1 {
		t.Fatalf("Destroy called %d times, want 1", lifecycle.destroyCount())
	}

	// Idempotent: calling again must not error or double-Destroy.
	if err := runner.Cleanup(context.Background(), id); err != nil {
		t.Fatalf("second Cleanup: %v", err)
	}
	if lifecycle.destroyCount() != 1 {
		t.Fatalf("Destroy called %d times after second Cleanup, want still 1", lifecycle.destroyCount())
	}

	// StreamLogs/Stop on the now-cleaned-up id must not panic; StreamLogs
	// reports "unknown job", Stop is a safe no-op per its documented
	// not-found tolerance.
	if _, _, err := runner.StreamLogs(context.Background(), id); err == nil {
		t.Fatal("expected StreamLogs on cleaned-up job to error")
	}
	if err := runner.Stop(context.Background(), id, time.Second); err != nil {
		t.Fatalf("Stop on cleaned-up job: %v", err)
	}
}

func TestVMRunner_SpawnJob_InvalidConfig(t *testing.T) {
	runner := newTestRunner(&fakeImageSource{}, &fakeLifecycle{}, &fakeTransport{})

	cases := []struct {
		name   string
		config *JobConfig
	}{
		{"nil config", nil},
		{"missing image", &JobConfig{Command: []string{"true"}, JobID: "j"}},
		{"missing command", &JobConfig{Image: "img", JobID: "j"}},
		{"missing job id", &JobConfig{Image: "img", Command: []string{"true"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := runner.SpawnJob(context.Background(), tc.config); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestValidateJobConfigRejectsUnsafeWindowsTreeDestination(t *testing.T) {
	err := validateJobConfig(&JobConfig{
		Image:    "image",
		Command:  []string{"cmd.exe", "/c", "exit", "0"},
		Platform: GuestPlatformWindows,
		Trees: []GuestTree{{
			SourcePath:  t.TempDir(),
			Destination: `C:/reactorcide/job/"; Write-Output bad`,
		}},
		JobID: "job",
	})
	if err == nil {
		t.Fatal("expected an unsafe destination error")
	}
}

func TestVMRunner_CPUAndMemoryMapping(t *testing.T) {
	guestAddr, cleanup := listenLoopback(t)
	defer cleanup()

	images := &fakeImageSource{path: "/base/test-base.img"}
	lifecycle := &fakeLifecycle{bootHandle: "vm-handle-8", bootAddr: guestAddr}
	transport := &fakeTransport{next: newFakeSession("", "", 0)}
	runner := newTestRunner(images, lifecycle, transport)

	config := baseTestConfig()
	config.CPULimit = "1500m"
	config.MemoryLimit = "512Mi"

	if _, err := runner.SpawnJob(context.Background(), config); err != nil {
		t.Fatalf("SpawnJob: %v", err)
	}

	if len(lifecycle.bootSpecs) != 1 {
		t.Fatalf("expected exactly one Boot call, got %d", len(lifecycle.bootSpecs))
	}
	spec := lifecycle.bootSpecs[0]
	if spec.CPUs != 2 { // 1500m rounds up to 2 vCPUs
		t.Fatalf("BootSpec.CPUs = %d, want 2", spec.CPUs)
	}
	if spec.MemoryBytes != 512*1024*1024 {
		t.Fatalf("BootSpec.MemoryBytes = %d, want %d", spec.MemoryBytes, 512*1024*1024)
	}
	if spec.JobID != "job-1" {
		t.Fatalf("BootSpec.JobID = %q, want %q", spec.JobID, "job-1")
	}
}
