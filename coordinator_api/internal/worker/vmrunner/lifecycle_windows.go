//go:build windows

// This file is the REAL Windows VMLifecycle, backed by Hyper-V driven through
// PowerShell (New-VHD / New-VM / Start-VM / Get-VMNetworkAdapter / Remove-VM).
// It is deliberately PURE Go -- it shells out to powershell.exe rather than
// linking a native library -- so it needs no Cgo and cross-compiles and
// compile-verifies from Linux with:
//
//	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build ./...
//
// Because it cross-compiles, the released windows worker gets the vm backend by
// default (no special build tag, unlike the darwin/vz backend). It can only be
// RUN on a Windows 11 Pro host with the Hyper-V feature enabled, by a user in
// the Hyper-V Administrators group (or elevated). See docs/vm-runners-windows.md.
//
// hcsshim is intentionally NOT used: it targets Windows containers / utility
// VMs, not full guest VMs. PowerShell's Hyper-V module is the pragmatic path to
// a booted Windows (or Linux) guest with its own OpenSSH server, which the
// shared SSH GuestTransport then reaches exactly like the macOS backend.

package vmrunner

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/catalystcommunity/app-utils-go/logging"
	"github.com/google/uuid"
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

// defaultHyperVSwitch is the virtual switch new guests attach to when
// REACTORCIDE_VM_HYPERV_SWITCH is unset. On Windows 10/11 the built-in
// "Default Switch" provides NAT with DHCP, which is what the guest-IP discovery
// below relies on.
const defaultHyperVSwitch = "Default Switch"

const (
	// defaultVMCPUs / minVMCPUs bound spec.CPUs (0 means "choose a default").
	defaultVMCPUs = 2
	minVMCPUs     = 1

	// defaultVMMemoryBytes / minVMMemoryBytes bound spec.MemoryBytes. 4 GiB
	// default, 1 GiB floor -- a Windows guest is unhappy with less.
	defaultVMMemoryBytes = int64(4) << 30
	minVMMemoryBytes     = int64(1) << 30

	// ipResolveTimeout / ipResolvePoll bound the wait for the guest to report
	// an IPv4 address via Get-VMNetworkAdapter. This needs the guest's Hyper-V
	// integration (Data Exchange) service running and DHCP to complete, so it
	// can take a while after Start-VM returns -- and is the single most likely
	// first-hardware snag (see VM_RUNNERS_PLAN.md).
	ipResolveTimeout = 120 * time.Second
	ipResolvePoll    = 2 * time.Second
)

// windowsVM is one booted guest tracked in-process. A Hyper-V VM name is the
// natural handle, but scratchDir must also be remembered so Destroy can remove
// the differencing disk, so Boot stores both keyed by the opaque id it returns.
type windowsVM struct {
	vmName     string
	scratchDir string
}

// windowsVMLifecycle is the Hyper-V/PowerShell-backed VMLifecycle. It keeps a
// mutex-guarded map of live guests keyed by the opaque id it hands back from
// Boot. switchName / secureBoot are host-level knobs read once at construction.
type windowsVMLifecycle struct {
	mu  sync.Mutex
	vms map[string]*windowsVM

	// switchName is the Hyper-V virtual switch guests attach to
	// (REACTORCIDE_VM_HYPERV_SWITCH, default "Default Switch").
	switchName string

	// secureBoot controls whether Gen-2 UEFI Secure Boot stays on. Windows
	// guests want it ON (the default "MicrosoftWindows" template); many Linux
	// guests need it OFF unless their bootloader is signed by the MS UEFI CA.
	// Toggle via REACTORCIDE_VM_HYPERV_SECURE_BOOT=off.
	secureBoot bool
}

// newVMLifecycle returns the real windows VMLifecycle. Construction never fails;
// per-boot problems (missing base VHDX, Hyper-V not enabled, PowerShell errors)
// surface from Boot. Host-level configuration is read from the environment here
// rather than threaded through vm_adapter.go, keeping that adapter OS-agnostic:
//   - REACTORCIDE_VM_HYPERV_SWITCH        (default "Default Switch")
//   - REACTORCIDE_VM_HYPERV_SECURE_BOOT   ("off"/"false"/"0"/"no" disables it;
//     anything else, including unset, leaves Gen-2 Secure Boot on)
func newVMLifecycle() (VMLifecycle, error) {
	switchName := strings.TrimSpace(os.Getenv("REACTORCIDE_VM_HYPERV_SWITCH"))
	if switchName == "" {
		switchName = defaultHyperVSwitch
	}

	secureBoot := true
	switch strings.ToLower(strings.TrimSpace(os.Getenv("REACTORCIDE_VM_HYPERV_SECURE_BOOT"))) {
	case "off", "false", "0", "no":
		secureBoot = false
	}

	return &windowsVMLifecycle{
		vms:        make(map[string]*windowsVM),
		switchName: switchName,
		secureBoot: secureBoot,
	}, nil
}

var _ VMLifecycle = (*windowsVMLifecycle)(nil)

// Boot creates a per-job scratch dir, makes a differencing (copy-on-write)
// child VHDX off the base VHDX, creates + configures + starts a Generation-2
// Hyper-V VM from it, then polls Get-VMNetworkAdapter until the guest reports an
// IPv4 address. It returns an opaque handle (used by Destroy) and the guest's
// SSH address; VMRunner.SpawnJob separately waits for guest SSH to accept the
// connection before running the job command.
func (w *windowsVMLifecycle) Boot(ctx context.Context, baseImagePath string, spec BootSpec) (string, GuestAddr, error) {
	logger := logging.Log

	baseVHDX, err := resolveBaseVHDX(baseImagePath)
	if err != nil {
		return "", GuestAddr{}, err
	}

	scratchDir, err := os.MkdirTemp("", "reactorcide-vm-")
	if err != nil {
		return "", GuestAddr{}, fmt.Errorf("vmrunner/windows: create scratch dir: %w", err)
	}
	// From here on, any failure must remove scratchDir.
	fail := func(err error) (string, GuestAddr, error) {
		_ = os.RemoveAll(scratchDir)
		return "", GuestAddr{}, err
	}

	id := uuid.New().String()
	vmName := "reactorcide-vm-" + id
	diffPath := filepath.Join(scratchDir, jobVHDX)

	if _, err := runPowerShell(ctx, w.createScript(baseVHDX, diffPath, vmName, spec)); err != nil {
		// The VM may have been partially created before the script failed; make
		// a best-effort removal so a half-built VM is not left registered.
		_ = w.forceRemoveVM(context.Background(), vmName)
		return fail(fmt.Errorf("vmrunner/windows: create+start VM: %w", err))
	}
	// From here the VM exists; any failure must also remove it.

	ip, err := w.resolveGuestIPv4(ctx, vmName, ipResolveTimeout, ipResolvePoll)
	if err != nil {
		_ = w.forceRemoveVM(context.Background(), vmName)
		return fail(fmt.Errorf("vmrunner/windows: resolve guest IP: %w", err))
	}

	w.mu.Lock()
	w.vms[id] = &windowsVM{vmName: vmName, scratchDir: scratchDir}
	w.mu.Unlock()

	// NB: never log guest SSH credentials; the guest IP + job label are safe
	// operational detail.
	logger.WithField("job_id", spec.JobID).WithField("guest_ip", ip).Info("booted Windows Hyper-V guest VM")

	return id, GuestAddr{Host: ip, Port: 22}, nil
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

	rmErr := w.forceRemoveVM(ctx, rec.vmName)

	var scratchErr error
	if rec.scratchDir != "" {
		scratchErr = os.RemoveAll(rec.scratchDir)
	}

	if rmErr != nil {
		return rmErr
	}
	if scratchErr != nil {
		return fmt.Errorf("vmrunner/windows: remove scratch dir: %w", scratchErr)
	}
	return nil
}

// createScript builds the PowerShell that creates the differencing disk and the
// Generation-2 VM, sizes it, optionally disables Secure Boot, and starts it.
// $ErrorActionPreference = 'Stop' makes any cmdlet failure abort the whole
// script with a non-zero exit so runPowerShell surfaces it.
func (w *windowsVMLifecycle) createScript(baseVHDX, diffPath, vmName string, spec BootSpec) string {
	var b strings.Builder
	b.WriteString("$ErrorActionPreference = 'Stop'\n")
	fmt.Fprintf(&b, "New-VHD -ParentPath %s -Path %s -Differencing | Out-Null\n",
		psQuote(baseVHDX), psQuote(diffPath))
	fmt.Fprintf(&b, "New-VM -Name %s -Generation 2 -MemoryStartupBytes %d -VHDPath %s -SwitchName %s | Out-Null\n",
		psQuote(vmName), winMemoryBytes(spec), psQuote(diffPath), psQuote(w.switchName))
	fmt.Fprintf(&b, "Set-VMProcessor -VMName %s -Count %d\n", psQuote(vmName), winCPUCount(spec))
	if !w.secureBoot {
		fmt.Fprintf(&b, "Set-VMFirmware -VMName %s -EnableSecureBoot Off\n", psQuote(vmName))
	}
	fmt.Fprintf(&b, "Start-VM -Name %s\n", psQuote(vmName))
	return b.String()
}

// forceRemoveVM turns off (does not gracefully shut down) and removes the named
// VM. It tolerates the VM already being gone: Get-VM with SilentlyContinue
// yields an empty pipeline (a no-op) rather than an error, keeping Destroy
// idempotent. The differencing VHDX is deleted separately with the scratch dir.
func (w *windowsVMLifecycle) forceRemoveVM(ctx context.Context, vmName string) error {
	script := "$ErrorActionPreference = 'Stop'\n" +
		"Get-VM -Name " + psQuote(vmName) + " -ErrorAction SilentlyContinue | ForEach-Object {\n" +
		"  Stop-VM -VM $_ -Force -TurnOff -ErrorAction SilentlyContinue\n" +
		"  Remove-VM -VM $_ -Force\n" +
		"}\n"
	if _, err := runPowerShell(ctx, script); err != nil {
		return fmt.Errorf("vmrunner/windows: remove VM: %w", err)
	}
	return nil
}

// resolveGuestIPv4 polls Get-VMNetworkAdapter until the guest reports a usable
// IPv4 address, ctx is canceled, or timeout elapses. Transient PowerShell
// errors (the adapter/integration service not yet ready) are treated as
// "keep polling" until the deadline.
func (w *windowsVMLifecycle) resolveGuestIPv4(ctx context.Context, vmName string, timeout, poll time.Duration) (string, error) {
	script := "Get-VMNetworkAdapter -VMName " + psQuote(vmName) +
		" | Select-Object -ExpandProperty IPAddresses | ConvertTo-Json -Compress"

	deadline := time.Now().Add(timeout)
	var lastErr error
	for {
		out, err := runPowerShell(ctx, script)
		if err != nil {
			lastErr = err
		} else if ip, ok := parseVMIPv4(out); ok {
			return ip, nil
		}

		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		if time.Now().After(deadline) {
			if lastErr != nil {
				return "", fmt.Errorf("guest reported no IPv4 within %s (last query error: %w)", timeout, lastErr)
			}
			return "", fmt.Errorf("guest reported no IPv4 within %s (is Hyper-V Data Exchange running in the guest?)", timeout)
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(poll):
		}
	}
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
// diagnosis. This lifecycle never puts secrets (guest SSH creds flow through the
// separate GuestTransport) into a script, so stderr is safe to surface.
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
