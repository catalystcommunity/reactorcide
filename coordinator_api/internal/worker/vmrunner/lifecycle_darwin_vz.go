//go:build darwin && vz

// This file is the REAL macOS VMLifecycle, backed by Apple's
// Virtualization.framework through github.com/Code-Hex/vz/v3. It only compiles
// with `CGO_ENABLED=1 go build -tags vz` on Apple Silicon macOS and the
// resulting binary MUST be code-signed with the
// com.apple.security.virtualization entitlement (see
// scripts/build-macos-worker.sh and docs/vm-runners-macos.md). Every other
// build of the tree selects a stub: darwin without the tag ->
// lifecycle_darwin_novz.go, linux -> lifecycle_other.go, windows ->
// lifecycle_windows.go.
//
// IMPORTANT: this file has NOT been compiled on Linux (no Cgo/vz there). It is
// written against the confirmed Code-Hex/vz/v3 v3.7.1 API surface and is
// verified by the maintainer building+running it on a MacBook. Iterate here on
// what the hardware build reports.
//
// The guest has NO network device (decision D29). The worker reaches the guest
// daemon (cmd/vmguest, a launchd daemon in the image) through a
// VirtioSocketDevice: Connect(vmguest.JobPort) is the job channel, and a host
// Listen(vmguest.EgressPort) receives each guest TCP flow as an HTTP CONNECT
// tunnel for EgressForwarder.

package vmrunner

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/catalystcommunity/app-utils-go/logging"
	"github.com/google/uuid"
	"golang.org/x/sys/unix"

	vz "github.com/Code-Hex/vz/v3"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/vmguest"
)

const vmScratchDirEnv = "REACTORCIDE_VM_SCRATCH_DIR"

const (
	// defaultVMCPUs / minVMCPUs bound spec.CPUs (0 means "choose a default").
	defaultVMCPUs = 2
	minVMCPUs     = 1

	// defaultVMMemoryBytes / minVMMemoryBytes bound spec.MemoryBytes. macOS
	// guests are unhappy with very little RAM; 4 GiB default, 2 GiB floor.
	defaultVMMemoryBytes = uint64(4) << 30
	minVMMemoryBytes     = uint64(2) << 30

	// bootStateTimeout bounds how long Boot waits for the guest to reach the
	// Running state after Start returns.
	bootStateTimeout = 60 * time.Second

	// vsockConnectTimeout bounds one VirtioSocketDevice.Connect. Apple
	// documents that Connect "does nothing" when the guest does not listen,
	// so the completion handler can be late or absent.
	vsockConnectTimeout = 10 * time.Second

	// stopSettleTimeout bounds how long Destroy waits for a requested stop to
	// take effect before removing scratch storage anyway.
	stopSettleTimeout = 15 * time.Second
)

// darwinVM is one booted guest tracked in-process. vz.VirtualMachine objects
// are not serializable handles -- they are live Cgo objects tied to this
// process -- so Boot stores them in a map and returns an opaque string id as
// the VMLifecycle handle.
type darwinVM struct {
	vm      *vz.VirtualMachine
	scratch *scratchDir
	egress  net.Listener
}

// darwinVMLifecycle is the Virtualization.framework-backed VMLifecycle. It
// keeps live VM objects in an in-process, mutex-guarded map keyed by the
// opaque id it hands back from Boot.
type darwinVMLifecycle struct {
	mu     sync.Mutex
	vms    map[string]*darwinVM
	egress EgressDialer
}

// newVMLifecycle returns the real darwin VMLifecycle. It fails only for a bad
// REACTORCIDE_VM_EGRESS_PROXY; per-boot problems (missing bundle files, vz
// errors) surface from Boot.
func newVMLifecycle() (VMLifecycle, error) {
	egress, err := EgressDialerFromEnv()
	if err != nil {
		return nil, err
	}
	d := &darwinVMLifecycle{vms: make(map[string]*darwinVM), egress: egress}
	// Remove the disk clones that a stopped worker left behind. A VM stops
	// with the worker process that started it.
	d.sweep()
	return d, nil
}

// sweep removes the scratch dirs that no live worker owns (see
// sweepScratch). Errors are logged; the next sweep tries again.
func (d *darwinVMLifecycle) sweep() {
	removed, errs := sweepScratch(os.Getenv(vmScratchDirEnv), nil)
	for _, dir := range removed {
		logging.Log.WithField("scratch_dir", dir).Info("vmrunner/darwin: removed an orphaned job disk clone")
	}
	for _, err := range errs {
		logging.Log.WithError(err).Warn("vmrunner/darwin: could not remove an orphaned job disk clone")
	}
}

var _ VMLifecycle = (*darwinVMLifecycle)(nil)

// Boot clones the base bundle's disk + aux images copy-on-write into a per-job
// scratch dir, builds a Virtualization.framework VM from the clone plus the
// base hardware model/machine identifier and a virtio socket device (no network
// device), starts it, waits for Running, and starts the host egress listener.
func (d *darwinVMLifecycle) Boot(ctx context.Context, baseImagePath string, spec BootSpec) (string, GuestAddr, error) {
	logger := logging.Log

	if err := ValidateMacBundle(baseImagePath); err != nil {
		return "", GuestAddr{}, err
	}

	d.sweep()
	scratch, err := createScratchDir(os.Getenv(vmScratchDirEnv))
	if err != nil {
		return "", GuestAddr{}, fmt.Errorf("vmrunner/darwin: %w", err)
	}
	jobDir := scratch.path
	// From here on, any failure must remove the scratch dir.
	fail := func(err error) (string, GuestAddr, error) {
		_ = scratch.remove()
		return "", GuestAddr{}, err
	}

	clonedDisk := filepath.Join(jobDir, BundleDiskImage)
	clonedAux := filepath.Join(jobDir, BundleAuxImage)
	if err := cloneFile(filepath.Join(baseImagePath, BundleDiskImage), clonedDisk); err != nil {
		return fail(fmt.Errorf("vmrunner/darwin: clone disk image: %w", err))
	}
	if err := cloneFile(filepath.Join(baseImagePath, BundleAuxImage), clonedAux); err != nil {
		return fail(fmt.Errorf("vmrunner/darwin: clone aux image: %w", err))
	}
	// clonefile preserves the base file mode. Production base bundles are
	// read-only, but the guest must write to both per-job clones.
	if err := os.Chmod(clonedDisk, 0o600); err != nil {
		return fail(fmt.Errorf("vmrunner/darwin: make cloned disk writable: %w", err))
	}
	if err := os.Chmod(clonedAux, 0o600); err != nil {
		return fail(fmt.Errorf("vmrunner/darwin: make cloned aux storage writable: %w", err))
	}

	config, err := buildVMConfig(baseImagePath, clonedDisk, clonedAux, spec)
	if err != nil {
		return fail(err)
	}

	vm, err := vz.NewVirtualMachine(config)
	if err != nil {
		return fail(fmt.Errorf("vmrunner/darwin: new virtual machine: %w", err))
	}

	// vz dispatches VM control operations onto its own serial queue, so
	// Start may be called from this goroutine; it blocks until the start
	// completion handler fires. Start is valid again from the Stopped or
	// Error state, so a start that hit the concurrent VM limit (a stopped
	// VM's slot is released asynchronously) is tried again on the same VM
	// (vm_start_retry.go).
	startErr := startWithLimitRetry(ctx, func() error { return vm.Start() }, vzStartLimitExceeded, defaultStartRetryPolicy,
		func(attempt int, wait time.Duration, err error) {
			logger.WithError(err).WithFields(map[string]interface{}{"job_id": spec.JobID, "attempt": attempt, "wait": wait.String()}).
				Warn("vmrunner/darwin: the Virtualization.framework VM limit is full; trying the start again")
			// A stopped VM that is not released yet (for example from a
			// failed Boot) still holds its slot.
			releaseStoppedVMs()
		})
	if startErr != nil {
		return fail(fmt.Errorf("vmrunner/darwin: start vm: %w", startErr))
	}

	if err := waitForRunning(ctx, vm, bootStateTimeout); err != nil {
		_ = stopVM(vm)
		return fail(fmt.Errorf("vmrunner/darwin: waiting for guest to run: %w", err))
	}

	dev, err := socketDevice(vm)
	if err != nil {
		_ = stopVM(vm)
		return fail(err)
	}
	egress, err := listenEgress(dev, d.egress, spec.JobID)
	if err != nil {
		_ = stopVM(vm)
		return fail(err)
	}

	id := uuid.New().String()
	d.mu.Lock()
	d.vms[id] = &darwinVM{vm: vm, scratch: scratch, egress: egress}
	d.mu.Unlock()

	logger.WithField("job_id", spec.JobID).Info("booted macOS guest VM (no network device)")

	return id, GuestAddr{Dial: vsockGuestDialer(dev)}, nil
}

// vzStartLimitExceeded reports whether a vm.Start error is
// VZErrorDomain Code=6 (VZErrorVirtualMachineLimitExceeded). Code-Hex/vz
// returns a *vz.NSError for a start completion error.
func vzStartLimitExceeded(err error) bool {
	var nsErr *vz.NSError
	if errors.As(err, &nsErr) {
		return isVZLimitExceeded(nsErr.Domain, nsErr.Code)
	}
	return err != nil && isVZLimitExceededText(err.Error())
}

// socketDevice returns the virtio socket device of a running VM.
func socketDevice(vm *vz.VirtualMachine) (*vz.VirtioSocketDevice, error) {
	devices := vm.SocketDevices()
	if len(devices) == 0 {
		return nil, errors.New("vmrunner/darwin: the VM has no virtio socket device")
	}
	return devices[0], nil
}

// listenEgress starts the host egress forwarder for one VM.
func listenEgress(dev *vz.VirtioSocketDevice, dialer EgressDialer, jobID string) (net.Listener, error) {
	inner, err := dev.Listen(vmguest.EgressPort)
	if err != nil {
		return nil, fmt.Errorf("vmrunner/darwin: listen for guest egress: %w", err)
	}
	ln := newClosableListener(inner)
	go func() { _ = (&EgressForwarder{Dialer: dialer, JobID: jobID}).Serve(ln) }()
	return ln, nil
}

// vsockGuestDialer connects to the guest daemon job channel.
func vsockGuestDialer(dev *vz.VirtioSocketDevice) func(ctx context.Context) (net.Conn, error) {
	return func(ctx context.Context) (net.Conn, error) {
		type result struct {
			conn *vz.VirtioSocketConnection
			err  error
		}
		ch := make(chan result, 1)
		go func() {
			c, err := dev.Connect(vmguest.JobPort)
			ch <- result{c, err}
		}()
		abandon := func() {
			go func() {
				if r := <-ch; r.conn != nil {
					_ = r.conn.Close()
				}
			}()
		}
		timer := time.NewTimer(vsockConnectTimeout)
		defer timer.Stop()
		select {
		case r := <-ch:
			if r.err != nil {
				return nil, r.err
			}
			if r.conn == nil {
				return nil, errors.New("vmrunner/darwin: vsock connect returned no connection")
			}
			return r.conn, nil
		case <-ctx.Done():
			abandon()
			return nil, ctx.Err()
		case <-timer.C:
			abandon()
			return nil, fmt.Errorf("vmrunner/darwin: vsock connect to port %d timed out", vmguest.JobPort)
		}
	}
}

// closableListener makes Accept return net.ErrClosed after Close. The vz
// VirtioSocketListener does not end a blocked Accept when it is closed, so
// one goroutine for each VM stays blocked in the inner Accept after Destroy
// (a limit of Code-Hex/vz v3.7.1).
type closableListener struct {
	inner  net.Listener
	conns  chan net.Conn
	closed chan struct{}
	once   sync.Once
}

func newClosableListener(inner net.Listener) *closableListener {
	l := &closableListener{inner: inner, conns: make(chan net.Conn), closed: make(chan struct{})}
	go func() {
		for {
			c, err := inner.Accept()
			if err != nil || c == nil {
				select {
				case <-l.closed:
					return
				default:
				}
				time.Sleep(50 * time.Millisecond)
				continue
			}
			select {
			case l.conns <- c:
			case <-l.closed:
				_ = c.Close()
				return
			}
		}
	}()
	return l
}

func (l *closableListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.conns:
		return c, nil
	case <-l.closed:
		return nil, net.ErrClosed
	}
}

func (l *closableListener) Close() error {
	l.once.Do(func() {
		close(l.closed)
		_ = l.inner.Close()
	})
	return nil
}

func (l *closableListener) Addr() net.Addr { return l.inner.Addr() }

// Destroy requests the guest stop, waits briefly for it to settle, removes the
// per-job scratch dir, and drops the record. Unknown/already-destroyed handles
// are a safe no-op, as VMLifecycle requires.
func (d *darwinVMLifecycle) Destroy(ctx context.Context, handle string) error {
	d.mu.Lock()
	rec, ok := d.vms[handle]
	delete(d.vms, handle)
	d.mu.Unlock()

	if !ok {
		return nil
	}
	if rec.egress != nil {
		_ = rec.egress.Close()
	}

	// Best-effort stop; tolerate an already-stopped VM.
	_ = stopVM(rec.vm)
	waitForStopped(rec.vm, stopSettleTimeout)
	// Drop the last reference and release the VM now, so its slot is free
	// for the next Boot (see releaseStoppedVMs).
	rec.vm = nil
	releaseStoppedVMs()

	if err := rec.scratch.remove(); err != nil {
		return fmt.Errorf("vmrunner/darwin: remove scratch dir (the next sweep tries again): %w", err)
	}
	return nil
}

// buildVMConfig assembles a VirtualMachineConfiguration: macOS boot loader,
// platform config from the CLONED aux storage plus the base
// hardwaremodel/machineidentifier, the cloned disk attached read-write, and a
// virtio socket device. It adds no network device: the guest reaches the
// network only through the egress tunnel on the socket device.
func buildVMConfig(baseImagePath, clonedDisk, clonedAux string, spec BootSpec) (*vz.VirtualMachineConfiguration, error) {
	aux, err := vz.NewMacAuxiliaryStorage(clonedAux)
	if err != nil {
		return nil, fmt.Errorf("open cloned aux storage: %w", err)
	}
	hwModel, err := vz.NewMacHardwareModelWithDataPath(filepath.Join(baseImagePath, BundleHardwareModel))
	if err != nil {
		return nil, fmt.Errorf("load hardware model: %w", err)
	}
	machineID, err := vz.NewMacMachineIdentifierWithDataPath(filepath.Join(baseImagePath, BundleMachineIdentifier))
	if err != nil {
		return nil, fmt.Errorf("load machine identifier: %w", err)
	}

	platform, err := vz.NewMacPlatformConfiguration(
		vz.WithMacAuxiliaryStorage(aux),
		vz.WithMacHardwareModel(hwModel),
		vz.WithMacMachineIdentifier(machineID),
	)
	if err != nil {
		return nil, fmt.Errorf("build platform config: %w", err)
	}

	bootLoader, err := vz.NewMacOSBootLoader()
	if err != nil {
		return nil, fmt.Errorf("build boot loader: %w", err)
	}

	config, err := vz.NewVirtualMachineConfiguration(bootLoader, cpuCountForSpec(spec), memoryBytesForSpec(spec))
	if err != nil {
		return nil, fmt.Errorf("build vm config: %w", err)
	}
	config.SetPlatformVirtualMachineConfiguration(platform)

	// Storage: the cloned disk, read-write.
	diskAttach, err := vz.NewDiskImageStorageDeviceAttachment(clonedDisk, false)
	if err != nil {
		return nil, fmt.Errorf("attach cloned disk: %w", err)
	}
	blockDev, err := vz.NewVirtioBlockDeviceConfiguration(diskAttach)
	if err != nil {
		return nil, fmt.Errorf("build block device: %w", err)
	}
	config.SetStorageDevicesVirtualMachineConfiguration([]vz.StorageDeviceConfiguration{blockDev})

	// The job channel and the egress tunnel. No network device.
	socketDev, err := vz.NewVirtioSocketDeviceConfiguration()
	if err != nil {
		return nil, fmt.Errorf("build socket device: %w", err)
	}
	config.SetSocketDevicesVirtualMachineConfiguration([]vz.SocketDeviceConfiguration{socketDev})

	// A macOS guest needs a graphics device to finish its boot, also with no
	// window: without one it stops before it mounts the Data volume, and
	// launchd never starts the guest daemon. The input and entropy devices
	// match a standard macOS VM. No device here gives the guest a network.
	if err := addMacDesktopDevices(config); err != nil {
		return nil, err
	}

	if _, err := config.Validate(); err != nil {
		return nil, fmt.Errorf("validate vm config: %w", err)
	}

	return config, nil
}

func cpuCountForSpec(spec BootSpec) uint {
	c := spec.CPUs
	if c <= 0 {
		c = defaultVMCPUs
	}
	if c < minVMCPUs {
		c = minVMCPUs
	}
	return uint(c)
}

func memoryBytesForSpec(spec BootSpec) uint64 {
	if spec.MemoryBytes <= 0 {
		return defaultVMMemoryBytes
	}
	m := uint64(spec.MemoryBytes)
	if m < minVMMemoryBytes {
		return minVMMemoryBytes
	}
	return m
}

// cloneFile copy-on-write clones src to dst using APFS clonefile(2), falling
// back to a plain byte copy (logged) if the clone fails -- e.g. src and dst on
// different filesystems, or a non-APFS volume.
func cloneFile(src, dst string) error {
	if err := unix.Clonefile(src, dst, 0); err == nil {
		return nil
	} else {
		logging.Log.WithField("src", src).WithField("dst", dst).WithError(err).
			Warn("APFS clonefile failed; falling back to a full copy (slower, no CoW)")
	}
	return copyFile(src, dst)
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	info, err := in.Stat()
	if err != nil {
		return err
	}

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, info.Mode().Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

// waitForRunning blocks until the VM reports Running, an error state, ctx is
// canceled, or timeout elapses. It combines a snapshot of the current state
// (Start may already have transitioned to Running before we subscribe) with
// the state-change channel.
func waitForRunning(ctx context.Context, vm *vz.VirtualMachine, timeout time.Duration) error {
	if vm.State() == vz.VirtualMachineStateRunning {
		return nil
	}

	stateCh := vm.StateChangedNotify()
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			return fmt.Errorf("timed out after %s (last state: %s)", timeout, vm.State())
		case state := <-stateCh:
			switch state {
			case vz.VirtualMachineStateRunning:
				return nil
			case vz.VirtualMachineStateError:
				return fmt.Errorf("guest entered error state")
			case vz.VirtualMachineStateStopped:
				return fmt.Errorf("guest stopped before reaching running state")
			}
		}
	}
}

// waitForStopped blocks (bounded) until the VM reports Stopped, so Destroy
// doesn't yank scratch storage out from under a still-running guest. Best
// effort: on timeout it returns and Destroy removes storage anyway.
func waitForStopped(vm *vz.VirtualMachine, timeout time.Duration) {
	if vm.State() == vz.VirtualMachineStateStopped {
		return
	}
	stateCh := vm.StateChangedNotify()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		select {
		case <-timer.C:
			return
		case state := <-stateCh:
			if state == vz.VirtualMachineStateStopped || state == vz.VirtualMachineStateError {
				return
			}
		}
	}
}

// releaseStoppedVMs runs a garbage collection so that the finalizers of
// unreachable vz.VirtualMachine values run. Code-Hex/vz v3.7.1 releases the
// VZVirtualMachine object only in a finalizer (there is no exported
// release). Virtualization.framework keeps the VM XPC process and its slot
// of the concurrent VM limit until that release, also after the VM stopped.
// With an idle heap, the next Go GC can come only after the 2-minute forced
// GC period, so a Boot right after a Destroy failed with VZErrorDomain
// Code=6 for up to 2 minutes (hardware run 2026-11-18). The finalizer runs
// asynchronously after the GC; startWithLimitRetry covers that short delay.
func releaseStoppedVMs() {
	runtime.GC()
}

// stopVM asks the guest to stop as gracefully as the current state allows,
// tolerating a VM that is already stopped or refuses the request. It prefers
// RequestStop (guest-initiated shutdown) and falls back to the forceful Stop.
func stopVM(vm *vz.VirtualMachine) error {
	if vm.CanRequestStop() {
		if _, err := vm.RequestStop(); err == nil {
			return nil
		}
	}
	if vm.CanStop() {
		return vm.Stop()
	}
	return nil
}

// addMacDesktopDevices adds a headless display, a USB keyboard and pointer,
// and an entropy device.
func addMacDesktopDevices(config *vz.VirtualMachineConfiguration) error {
	graphics, err := vz.NewMacGraphicsDeviceConfiguration()
	if err != nil {
		return fmt.Errorf("build graphics device: %w", err)
	}
	display, err := vz.NewMacGraphicsDisplayConfiguration(1920, 1080, 80)
	if err != nil {
		return fmt.Errorf("build display: %w", err)
	}
	graphics.SetDisplays(display)
	config.SetGraphicsDevicesVirtualMachineConfiguration([]vz.GraphicsDeviceConfiguration{graphics})

	keyboard, err := vz.NewUSBKeyboardConfiguration()
	if err != nil {
		return fmt.Errorf("build keyboard: %w", err)
	}
	config.SetKeyboardsVirtualMachineConfiguration([]vz.KeyboardConfiguration{keyboard})
	pointer, err := vz.NewUSBScreenCoordinatePointingDeviceConfiguration()
	if err != nil {
		return fmt.Errorf("build pointing device: %w", err)
	}
	config.SetPointingDevicesVirtualMachineConfiguration([]vz.PointingDeviceConfiguration{pointer})

	entropy, err := vz.NewVirtioEntropyDeviceConfiguration()
	if err != nil {
		return fmt.Errorf("build entropy device: %w", err)
	}
	config.SetEntropyDevicesVirtualMachineConfiguration([]*vz.VirtioEntropyDeviceConfiguration{entropy})
	return nil
}
