//go:build windows

// This file is the REAL Windows VMLifecycle, backed by Hyper-V driven through
// PowerShell (New-VHD / New-VM / Start-VM / Remove-VM). It is deliberately PURE
// Go -- it shells out to powershell.exe and uses go-winio for Hyper-V sockets
// rather than Cgo -- so it cross-compiles and compile-verifies from Linux with:
//
//	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build ./...
//
// Because it cross-compiles, the released windows worker gets the vm backend by
// default (no special build tag, unlike the darwin/vz backend). It can only be
// RUN on a Windows 11 Pro host with the Hyper-V feature enabled, by a user in
// the Hyper-V Administrators group (or elevated). See docs/vm-runners-windows.md.
//
// The guest has NO network adapter. The worker reaches the guest daemon
// (cmd/vmguest, a Windows service in the image) over a Hyper-V socket
// (AF_HYPERV). Guest egress comes back over a second Hyper-V socket service to
// EgressForwarder. The guest gets proxy settings for proxy-aware programs; other
// programs fail closed (decision D28; the transparent redirect driver is a TODO).
//
// hcsshim is intentionally NOT used: it targets Windows containers / utility
// VMs, not full guest VMs.

package vmrunner

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/Microsoft/go-winio"
	"github.com/Microsoft/go-winio/pkg/guid"
	"github.com/catalystcommunity/app-utils-go/logging"
	"github.com/google/uuid"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/vmguest"
)

// A base image is a BUNDLE directory holding a single base VHDX, mirroring the
// macOS bundle convention so LocalImageSource / the OCI ImageSource resolve a
// windows and a macOS image the same way. docs/vm-runners-windows.md covers
// producing one. A bare .vhdx path is also accepted for convenience.
const (
	// bundleVHDX is the base disk inside a bundle directory. Each Boot creates
	// a differencing (copy-on-write) child VHDX off it; the base is never
	// written to.
	bundleVHDX = "disk.vhdx"

	// jobVHDX is the per-job differencing child VHDX created in the scratch dir.
	jobVHDX = "job.vhdx"
)

const (
	// defaultVMCPUs / minVMCPUs bound spec.CPUs (0 means "choose a default").
	defaultVMCPUs = 2
	minVMCPUs     = 1

	// defaultVMMemoryBytes / minVMMemoryBytes bound spec.MemoryBytes. 4 GiB
	// default, 1 GiB floor -- a Windows guest is unhappy with less.
	defaultVMMemoryBytes = int64(4) << 30
	minVMMemoryBytes     = int64(1) << 30
)

// windowsVM is one booted guest tracked in-process. A Hyper-V VM name is the
// natural handle, but the scratch dir must also be remembered so Destroy can
// remove the differencing disk, so Boot stores both keyed by the opaque id it
// returns.
type windowsVM struct {
	vmName  string
	scratch *scratchDir
	egress  net.Listener
}

// windowsVMLifecycle is the Hyper-V/PowerShell-backed VMLifecycle. It keeps a
// mutex-guarded map of live guests keyed by the opaque id it hands back from
// Boot. secureBoot and the egress dialer are host-level knobs read once at
// construction.
type windowsVMLifecycle struct {
	mu  sync.Mutex
	vms map[string]*windowsVM
	// scratchRoot is the optional parent directory for per-job differencing
	// disks. An empty value uses the system temporary directory.
	scratchRoot string

	// secureBoot controls whether Gen-2 UEFI Secure Boot stays on. Windows
	// guests want it ON (the default "MicrosoftWindows" template); many Linux
	// guests need it OFF unless their bootloader is signed by the MS UEFI CA.
	// Toggle via REACTORCIDE_VM_HYPERV_SECURE_BOOT=off.
	secureBoot bool

	// egress dials guest egress flows (REACTORCIDE_VM_EGRESS_PROXY).
	egress EgressDialer

	// register adds the Hyper-V socket services to the host registry once.
	registerMu sync.Mutex
	registered bool
}

// newVMLifecycle returns the real windows VMLifecycle. Construction never fails;
// per-boot problems (missing base VHDX, Hyper-V not enabled, PowerShell errors)
// surface from Boot. Host-level configuration is read from the environment here
// rather than threaded through vm_adapter.go, keeping that adapter OS-agnostic:
//   - REACTORCIDE_VM_HYPERV_SECURE_BOOT   ("off"/"false"/"0"/"no" disables it;
//     anything else, including unset, leaves Gen-2 Secure Boot on)
//   - REACTORCIDE_VM_EGRESS_PROXY         (optional HTTP CONNECT proxy for
//     guest egress; unset dials directly)
func newVMLifecycle() (VMLifecycle, error) {
	egress, err := EgressDialerFromEnv()
	if err != nil {
		return nil, err
	}

	secureBoot := true
	switch strings.ToLower(strings.TrimSpace(os.Getenv("REACTORCIDE_VM_HYPERV_SECURE_BOOT"))) {
	case "off", "false", "0", "no":
		secureBoot = false
	}

	w := &windowsVMLifecycle{
		vms:         make(map[string]*windowsVM),
		scratchRoot: strings.TrimSpace(os.Getenv("REACTORCIDE_VM_SCRATCH_DIR")),
		secureBoot:  secureBoot,
		egress:      egress,
	}
	// Remove the VMs and disks that a stopped worker left behind.
	w.sweep(context.Background())
	return w, nil
}

// sweep removes the job VMs and scratch dirs that no live worker owns (see
// sweepScratch). Errors are logged; the next sweep tries again.
func (w *windowsVMLifecycle) sweep(ctx context.Context) {
	removed, errs := sweepScratch(w.scratchRoot, func(dir string) error {
		if name := readScratchVMName(dir); name != "" {
			return w.forceRemoveVM(ctx, name)
		}
		return nil
	})
	for _, dir := range removed {
		logging.Log.WithField("scratch_dir", dir).Info("vmrunner/windows: removed an orphaned job VM and its disk")
	}
	for _, err := range errs {
		logging.Log.WithError(err).Warn("vmrunner/windows: could not remove an orphaned job VM")
	}
}

var _ VMLifecycle = (*windowsVMLifecycle)(nil)

// registerServices adds the Hyper-V socket service IDs to the host registry.
// A failure is retried on the next Boot.
func (w *windowsVMLifecycle) registerServices(ctx context.Context) error {
	w.registerMu.Lock()
	defer w.registerMu.Unlock()
	if w.registered {
		return nil
	}
	if _, err := runPowerShell(ctx, hvsockRegistrationScript(hvsockServices())); err != nil {
		return fmt.Errorf("vmrunner/windows: register Hyper-V socket services (needs administrator rights): %w", err)
	}
	w.registered = true
	return nil
}

// Boot creates a per-job scratch dir, makes a differencing (copy-on-write)
// child VHDX off the base VHDX, creates a Generation-2 Hyper-V VM with no
// network adapter, starts the VM, and then starts the host egress listener for
// the VM. It returns an opaque handle (used by Destroy) and a GuestAddr that dials
// the guest daemon over a Hyper-V socket; VMRunner.SpawnJob separately waits
// for the guest daemon to answer before running the job command.
func (w *windowsVMLifecycle) Boot(ctx context.Context, baseImagePath string, spec BootSpec) (string, GuestAddr, error) {
	return w.boot(ctx, baseImagePath, spec, windowsBootExtras{})
}

// windowsBootExtras are the image build changes to the differencing disk
// before the first boot.
type windowsBootExtras struct {
	// GuestDaemon is a host vmguest.exe that replaces the guest daemon.
	GuestDaemon string
	// PreventDeviceEncryption stops BitLocker device encryption. A job VM
	// has a new vTPM and cannot unlock an encrypted image.
	PreventDeviceEncryption bool
}

// boot is Boot with the image build changes in extras.
func (w *windowsVMLifecycle) boot(ctx context.Context, baseImagePath string, spec BootSpec, extras windowsBootExtras) (string, GuestAddr, error) {
	logger := logging.Log

	baseVHDX, err := resolveBaseVHDX(baseImagePath)
	if err != nil {
		return "", GuestAddr{}, err
	}
	if err := w.registerServices(ctx); err != nil {
		return "", GuestAddr{}, err
	}

	w.sweep(ctx)
	scratch, err := createScratchDir(w.scratchRoot)
	if err != nil {
		return "", GuestAddr{}, fmt.Errorf("vmrunner/windows: %w", err)
	}
	// From here on, any failure must remove the scratch dir.
	fail := func(err error) (string, GuestAddr, error) {
		_ = scratch.remove()
		return "", GuestAddr{}, err
	}
	id := uuid.New().String()
	vmName := "reactorcide-vm-" + id
	diffPath := filepath.Join(scratch.path, jobVHDX)
	// Record the VM name before the VM exists, so that a sweep can remove the
	// VM when this worker stops before Destroy.
	if err := os.WriteFile(filepath.Join(scratch.path, scratchVMNameFile), []byte(vmName+"\n"), 0o600); err != nil {
		return fail(fmt.Errorf("vmrunner/windows: record VM name: %w", err))
	}

	out, err := runPowerShell(ctx, hypervCreateScript(hypervCreateParams{
		BaseVHDX:    baseVHDX,
		DiffPath:    diffPath,
		VMName:      vmName,
		JobID:       spec.JobID,
		MemoryBytes: winMemoryBytes(spec),
		CPUs:        winCPUCount(spec),
		SecureBoot:  w.secureBoot,
		GuestDaemon: extras.GuestDaemon,

		PreventDeviceEncryption: extras.PreventDeviceEncryption,
	}))
	if err != nil {
		// The VM may have been partially created before the script failed; make
		// a best-effort removal so a half-built VM is not left registered.
		_ = w.forceRemoveVM(context.Background(), vmName)
		return fail(fmt.Errorf("vmrunner/windows: create VM: %w", err))
	}
	// From here the VM exists; any failure must also remove it.
	vmID, err := parseVMID(out)
	if err != nil {
		_ = w.forceRemoveVM(context.Background(), vmName)
		return fail(err)
	}

	if _, err := runPowerShell(ctx, hypervStartScript(vmName)); err != nil {
		_ = w.forceRemoveVM(context.Background(), vmName)
		return fail(fmt.Errorf("vmrunner/windows: start VM: %w", err))
	}

	// Hyper-V refuses a bind to a VM ID until the VM partition exists
	// (WSAEADDRNOTAVAIL for a VM that is created but not started), so listen
	// after Start-VM. The guest dials egress only when a job process opens a
	// connection, and jobs start only after the host reaches the guest daemon.
	egress, err := winio.ListenHvsock(&winio.HvsockAddr{VMID: vmID, ServiceID: winio.VsockServiceID(vmguest.EgressPort)})
	if err != nil {
		_ = w.forceRemoveVM(context.Background(), vmName)
		return fail(fmt.Errorf("vmrunner/windows: listen for guest egress: %w", err))
	}
	go func() { _ = (&EgressForwarder{Dialer: w.egress, JobID: spec.JobID}).Serve(egress) }()

	w.mu.Lock()
	w.vms[id] = &windowsVM{vmName: vmName, scratch: scratch, egress: egress}
	w.mu.Unlock()

	logger.WithField("job_id", spec.JobID).WithField("vm_id", vmID.String()).Info("booted Windows Hyper-V guest VM (no network adapter)")

	return id, GuestAddr{Dial: hvsockGuestDialer(vmID)}, nil
}

// vmRecord returns the VM name and the scratch dir path of handle.
func (w *windowsVMLifecycle) vmRecord(handle string) (vmName, scratchPath string, ok bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	rec, ok := w.vms[handle]
	if !ok {
		return "", "", false
	}
	return rec.vmName, rec.scratch.path, true
}

// hvsockGuestDialer connects to the guest daemon job channel of one VM.
func hvsockGuestDialer(vmID guid.GUID) func(ctx context.Context) (net.Conn, error) {
	return func(ctx context.Context) (net.Conn, error) {
		return winio.Dial(ctx, &winio.HvsockAddr{VMID: vmID, ServiceID: winio.VsockServiceID(vmguest.JobPort)})
	}
}

// Destroy force-stops and removes the guest for handle and deletes its scratch
// dir (including the differencing VHDX). An unknown/already-destroyed handle is
// a safe no-op, as VMLifecycle requires.
func (w *windowsVMLifecycle) Destroy(ctx context.Context, handle string) error {
	w.mu.Lock()
	rec, ok := w.vms[handle]
	delete(w.vms, handle)
	w.mu.Unlock()

	if !ok {
		return nil
	}
	if rec.egress != nil {
		_ = rec.egress.Close()
	}

	if err := w.forceRemoveVM(ctx, rec.vmName); err != nil {
		// Keep the scratch dir: it records the VM name. Release the owner lock
		// so that the next sweep removes the VM and the dir.
		if rec.scratch != nil && rec.scratch.lock != nil {
			_ = rec.scratch.lock.Close()
			rec.scratch.lock = nil
		}
		return err
	}
	if err := rec.scratch.remove(); err != nil {
		return fmt.Errorf("vmrunner/windows: remove scratch dir (the next sweep tries again): %w", err)
	}
	return nil
}

// forceRemoveVM turns off (does not gracefully shut down) and removes the named
// VM. It tolerates the VM already being gone (see hypervForceRemoveScript),
// keeping Destroy and the orphan sweep idempotent. The differencing VHDX is deleted separately with the scratch dir.
func (w *windowsVMLifecycle) forceRemoveVM(ctx context.Context, vmName string) error {
	if _, err := runPowerShell(ctx, hypervForceRemoveScript(vmName)); err != nil {
		return fmt.Errorf("vmrunner/windows: remove VM: %w", err)
	}
	return nil
}

// resolveBaseVHDX resolves a base image path to the concrete base VHDX file:
// a bundle directory contributes <dir>/disk.vhdx; a path that is already a file
// (e.g. a bare *.vhdx) is used as-is. LocalImageSource.Resolve has already
// confirmed the path exists.
func resolveBaseVHDX(baseImagePath string) (string, error) {
	info, err := os.Stat(baseImagePath)
	if err != nil {
		return "", fmt.Errorf("vmrunner/windows: base image %q: %w", baseImagePath, err)
	}
	if !info.IsDir() {
		return baseImagePath, nil
	}
	vhdx := filepath.Join(baseImagePath, bundleVHDX)
	if _, err := os.Stat(vhdx); err != nil {
		return "", fmt.Errorf("vmrunner/windows: bundle %q missing %s: %w", baseImagePath, bundleVHDX, err)
	}
	return vhdx, nil
}

func winCPUCount(spec BootSpec) int {
	c := spec.CPUs
	if c <= 0 {
		c = defaultVMCPUs
	}
	if c < minVMCPUs {
		c = minVMCPUs
	}
	return c
}

func winMemoryBytes(spec BootSpec) int64 {
	if spec.MemoryBytes <= 0 {
		return defaultVMMemoryBytes
	}
	if spec.MemoryBytes < minVMMemoryBytes {
		return minVMMemoryBytes
	}
	return spec.MemoryBytes
}

// runPowerShell runs script through powershell.exe non-interactively and returns
// its stdout. On failure it returns an error including trimmed stderr for
// diagnosis. This lifecycle never puts secrets into a script (job inputs flow
// through the separate GuestTransport), so stderr is safe to surface.
func runPowerShell(ctx context.Context, script string) (string, error) {
	cmd := exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-Command", script)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = strings.TrimSpace(stdout.String())
		}
		return stdout.String(), fmt.Errorf("powershell: %w: %s", err, msg)
	}
	return stdout.String(), nil
}
