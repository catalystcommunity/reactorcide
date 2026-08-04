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

package vmrunner

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/catalystcommunity/app-utils-go/logging"
	"github.com/google/uuid"
	"golang.org/x/sys/unix"

	vz "github.com/Code-Hex/vz/v3"
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

	// leaseResolveTimeout / leaseResolvePoll bound the wait for the guest's
	// DHCP lease (its IP) to appear in /var/db/dhcpd_leases. DHCP typically
	// completes a few seconds after Running.
	leaseResolveTimeout = 60 * time.Second
	leaseResolvePoll    = 1 * time.Second

	// stopSettleTimeout bounds how long Destroy waits for a requested stop to
	// take effect before removing scratch storage anyway.
	stopSettleTimeout = 15 * time.Second
)

// darwinVM is one booted guest tracked in-process. vz.VirtualMachine objects
// are not serializable handles -- they are live Cgo objects tied to this
// process -- so Boot stores them in a map and returns an opaque string id as
// the VMLifecycle handle.
type darwinVM struct {
	vm         *vz.VirtualMachine
	scratchDir string
	mac        string
}

// darwinVMLifecycle is the Virtualization.framework-backed VMLifecycle. It
// keeps live VM objects in an in-process, mutex-guarded map keyed by the
// opaque id it hands back from Boot.
type darwinVMLifecycle struct {
	mu  sync.Mutex
	vms map[string]*darwinVM
}

// newVMLifecycle returns the real darwin VMLifecycle. Construction never fails;
// per-boot problems (missing bundle files, vz errors) surface from Boot.
func newVMLifecycle() (VMLifecycle, error) {
	return &darwinVMLifecycle{vms: make(map[string]*darwinVM)}, nil
}

var _ VMLifecycle = (*darwinVMLifecycle)(nil)

// Boot clones the base bundle's disk + aux images copy-on-write into a per-job
// scratch dir, builds a Virtualization.framework VM from the clone plus the
// base hardware model/machine identifier, starts it, waits for Running, then
// resolves the guest IP by matching its NAT MAC in /var/db/dhcpd_leases.
func (d *darwinVMLifecycle) Boot(ctx context.Context, baseImagePath string, spec BootSpec) (string, GuestAddr, error) {
	logger := logging.Log

	if err := validateBundle(baseImagePath); err != nil {
		return "", GuestAddr{}, err
	}

	scratchRoot := os.Getenv(vmScratchDirEnv)
	if scratchRoot != "" {
		if err := os.MkdirAll(scratchRoot, 0o700); err != nil {
			return "", GuestAddr{}, fmt.Errorf("vmrunner/darwin: create scratch root: %w", err)
		}
	}
	scratchDir, err := os.MkdirTemp(scratchRoot, "reactorcide-vm-")
	if err != nil {
		return "", GuestAddr{}, fmt.Errorf("vmrunner/darwin: create scratch dir: %w", err)
	}
	// From here on, any failure must remove scratchDir.
	fail := func(err error) (string, GuestAddr, error) {
		_ = os.RemoveAll(scratchDir)
		return "", GuestAddr{}, err
	}

	clonedDisk := filepath.Join(scratchDir, BundleDiskImage)
	clonedAux := filepath.Join(scratchDir, BundleAuxImage)
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

	config, mac, err := buildVMConfig(baseImagePath, clonedDisk, clonedAux, spec)
	if err != nil {
		return fail(err)
	}

	vm, err := vz.NewVirtualMachine(config)
	if err != nil {
		return fail(fmt.Errorf("vmrunner/darwin: new virtual machine: %w", err))
	}

	// vz dispatches VM control operations onto its own serial queue, so
	// Start may be called from this goroutine; it blocks until the start
	// completion handler fires.
	if err := vm.Start(); err != nil {
		return fail(fmt.Errorf("vmrunner/darwin: start vm: %w", err))
	}

	if err := waitForRunning(ctx, vm, bootStateTimeout); err != nil {
		_ = stopVM(vm)
		return fail(fmt.Errorf("vmrunner/darwin: waiting for guest to run: %w", err))
	}

	ip, err := resolveGuestIP(ctx, mac, leaseResolveTimeout, leaseResolvePoll)
	if err != nil {
		_ = stopVM(vm)
		return fail(fmt.Errorf("vmrunner/darwin: resolve guest IP: %w", err))
	}

	id := uuid.New().String()
	d.mu.Lock()
	d.vms[id] = &darwinVM{vm: vm, scratchDir: scratchDir, mac: mac}
	d.mu.Unlock()

	// NB: never log mac->key material or guest creds; the guest IP + job label
	// are safe operational detail.
	logger.WithField("job_id", spec.JobID).WithField("guest_ip", ip).Info("booted macOS guest VM")

	return id, GuestAddr{Host: ip, Port: 22}, nil
}

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

	// Best-effort stop; tolerate an already-stopped VM.
	_ = stopVM(rec.vm)
	waitForStopped(rec.vm, stopSettleTimeout)

	if rec.scratchDir != "" {
		if err := os.RemoveAll(rec.scratchDir); err != nil {
			return fmt.Errorf("vmrunner/darwin: remove scratch dir: %w", err)
		}
	}
	return nil
}

// validateBundle checks baseImagePath is a directory containing the four
// required bundle artifacts.
func validateBundle(baseImagePath string) error {
	return ValidateMacBundle(baseImagePath)
}

// buildVMConfig assembles a VirtualMachineConfiguration: macOS boot loader,
// platform config from the CLONED aux storage plus the base
// hardwaremodel/machineidentifier, the cloned disk attached read-write, and a
// NAT network with a fresh random locally-administered MAC (returned so Boot
// can match it in the DHCP leases file).
func buildVMConfig(baseImagePath, clonedDisk, clonedAux string, spec BootSpec) (*vz.VirtualMachineConfiguration, string, error) {
	aux, err := vz.NewMacAuxiliaryStorage(clonedAux)
	if err != nil {
		return nil, "", fmt.Errorf("open cloned aux storage: %w", err)
	}
	hwModel, err := vz.NewMacHardwareModelWithDataPath(filepath.Join(baseImagePath, BundleHardwareModel))
	if err != nil {
		return nil, "", fmt.Errorf("load hardware model: %w", err)
	}
	machineID, err := vz.NewMacMachineIdentifierWithDataPath(filepath.Join(baseImagePath, BundleMachineIdentifier))
	if err != nil {
		return nil, "", fmt.Errorf("load machine identifier: %w", err)
	}

	platform, err := vz.NewMacPlatformConfiguration(
		vz.WithMacAuxiliaryStorage(aux),
		vz.WithMacHardwareModel(hwModel),
		vz.WithMacMachineIdentifier(machineID),
	)
	if err != nil {
		return nil, "", fmt.Errorf("build platform config: %w", err)
	}

	bootLoader, err := vz.NewMacOSBootLoader()
	if err != nil {
		return nil, "", fmt.Errorf("build boot loader: %w", err)
	}

	config, err := vz.NewVirtualMachineConfiguration(bootLoader, cpuCountForSpec(spec), memoryBytesForSpec(spec))
	if err != nil {
		return nil, "", fmt.Errorf("build vm config: %w", err)
	}
	config.SetPlatformVirtualMachineConfiguration(platform)

	// Storage: the cloned disk, read-write.
	diskAttach, err := vz.NewDiskImageStorageDeviceAttachment(clonedDisk, false)
	if err != nil {
		return nil, "", fmt.Errorf("attach cloned disk: %w", err)
	}
	blockDev, err := vz.NewVirtioBlockDeviceConfiguration(diskAttach)
	if err != nil {
		return nil, "", fmt.Errorf("build block device: %w", err)
	}
	config.SetStorageDevicesVirtualMachineConfiguration([]vz.StorageDeviceConfiguration{blockDev})

	// Network: NAT with a known random MAC so we can find the guest's IP.
	natAttach, err := vz.NewNATNetworkDeviceAttachment()
	if err != nil {
		return nil, "", fmt.Errorf("build NAT attachment: %w", err)
	}
	netDev, err := vz.NewVirtioNetworkDeviceConfiguration(natAttach)
	if err != nil {
		return nil, "", fmt.Errorf("build network device: %w", err)
	}
	mac, err := vz.NewRandomLocallyAdministeredMACAddress()
	if err != nil {
		return nil, "", fmt.Errorf("generate MAC: %w", err)
	}
	netDev.SetMACAddress(mac)
	config.SetNetworkDevicesVirtualMachineConfiguration([]*vz.VirtioNetworkDeviceConfiguration{netDev})

	if _, err := config.Validate(); err != nil {
		return nil, "", fmt.Errorf("validate vm config: %w", err)
	}

	return config, mac.String(), nil
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

// resolveGuestIP polls the macOS DHCP leases file until the guest's MAC shows
// up with an IP, ctx is canceled, or timeout elapses.
func resolveGuestIP(ctx context.Context, mac string, timeout, poll time.Duration) (string, error) {
	deadline := time.Now().Add(timeout)
	for {
		if ip, ok := parseDHCPLeases(defaultDHCPLeasesPath, mac); ok {
			return ip, nil
		}
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		if time.Now().After(deadline) {
			return "", fmt.Errorf("guest MAC not found in %s after %s", defaultDHCPLeasesPath, timeout)
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(poll):
		}
	}
}
