//go:build windows

package vmrunner

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// windowsImageBuildDirPrefix names the locked build dir next to the output.
const windowsImageBuildDirPrefix = ".reactorcide-image-build-"

// buildWindowsImage is the Hyper-V derived image build. It uses the job
// lifecycle (lifecycle_windows.go): the build VM is a differencing disk of
// the base in a locked scratch dir, so a sweep removes the VM and the disk
// when this process stops before its own cleanup.
//
// Steps:
//  1. Boot the base with no network adapter (optionally with a new guest
//     daemon copied into the differencing disk before the first boot).
//  2. Run each provisioning script as the guest account through the guest
//     daemon. Egress goes through the Hyper-V socket tunnel.
//  3. As LocalSystem, turn off BitLocker, empty the job directory, and shut
//     the guest down cleanly (no Sysprep; see windowsCaptureScript).
//  4. Remove the VM, keep the differencing disk, and write a new standalone
//     dynamic VHDX with Convert-VHD (the base is not changed).
//  5. Check the new disk offline, mark the bundle read-only, and move it to
//     the output path.
func buildWindowsImage(ctx context.Context, opts WindowsImageBuildOptions) (retErr error) {
	if err := ValidateWindowsBundle(opts.SourceBundle); err != nil {
		return err
	}
	if opts.OutputBundle == "" {
		return errors.New("vmrunner: output bundle is required")
	}
	if opts.User == "" {
		opts.User = "reactorcide"
	}
	if opts.LogWriter == nil {
		opts.LogWriter = io.Discard
	}
	if opts.ShutdownTimeout <= 0 {
		opts.ShutdownTimeout = 30 * time.Minute
	}
	guestDaemon := ""
	if opts.GuestDaemon != "" {
		abs, err := filepath.Abs(opts.GuestDaemon)
		if err != nil {
			return fmt.Errorf("vmrunner: guest daemon path: %w", err)
		}
		info, err := os.Stat(abs)
		if err != nil {
			return fmt.Errorf("vmrunner: guest daemon: %w", err)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("vmrunner: guest daemon %q is not a file", abs)
		}
		guestDaemon = abs
	}
	output, err := filepath.Abs(opts.OutputBundle)
	if err != nil {
		return fmt.Errorf("vmrunner: output bundle path: %w", err)
	}
	if _, err := os.Stat(output); err == nil {
		return fmt.Errorf("vmrunner: output bundle %q already exists", output)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("vmrunner: inspect output bundle: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(output), 0o755); err != nil {
		return fmt.Errorf("vmrunner: create output parent: %w", err)
	}
	source, err := filepath.Abs(opts.SourceBundle)
	if err != nil {
		return fmt.Errorf("vmrunner: source bundle path: %w", err)
	}

	lifecycle, err := newVMLifecycle()
	if err != nil {
		return err
	}
	w, ok := lifecycle.(*windowsVMLifecycle)
	if !ok {
		return errors.New("vmrunner: the Windows lifecycle is not available")
	}

	// The output dir is built in a locked dir next to it, so that a build
	// that stops without cleanup leaves a dir that the next build removes.
	outputParent := filepath.Dir(output)
	removed, sweepErrs := sweepLockedDirs(outputParent, windowsImageBuildDirPrefix, nil)
	for _, dir := range removed {
		_, _ = fmt.Fprintf(opts.LogWriter, "[image build] removed an incomplete build directory: %s\n", dir)
	}
	for _, err := range sweepErrs {
		_, _ = fmt.Fprintf(opts.LogWriter, "[image build] warning: %v\n", err)
	}
	build, err := createLockedDir(outputParent, windowsImageBuildDirPrefix)
	if err != nil {
		return fmt.Errorf("vmrunner: create image build directory: %w", err)
	}
	tmp := build.path
	keep := false
	defer func() {
		if !keep {
			if err := build.remove(); err != nil {
				retErr = errors.Join(retErr, fmt.Errorf("vmrunner: remove image build directory %s (the next build tries again): %w", tmp, err))
			}
		}
	}()

	logf := func(format string, args ...any) {
		_, _ = fmt.Fprintf(opts.LogWriter, format+"\n", args...)
	}
	logf("[image build] booting %s (no network adapter)", source)
	handle, addr, err := w.boot(ctx, source, BootSpec{CPUs: opts.CPUs, MemoryBytes: opts.MemoryBytes, JobID: "image-build"},
		windowsBootExtras{GuestDaemon: guestDaemon, PreventDeviceEncryption: true})
	if err != nil {
		return err
	}
	// Destroy removes the VM and the scratch dir. It must run with a fresh
	// context: ctx can be canceled (Ctrl-C).
	destroyed := false
	defer func() {
		if destroyed {
			return
		}
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		if err := w.Destroy(cleanupCtx, handle); err != nil {
			retErr = errors.Join(retErr, fmt.Errorf("vmrunner: remove image build VM (the next sweep tries again): %w", err))
		}
	}()
	vmName, scratchPath, ok := w.vmRecord(handle)
	if !ok {
		return errors.New("vmrunner: image build VM record is missing")
	}

	transport := NewVsockTransport()
	if err := waitGuestReachable(ctx, transport, addr, 15*time.Minute, 2*time.Second); err != nil {
		return fmt.Errorf("vmrunner: wait for the image build guest daemon (is the reactorcide-vmguest service in the source image?): %w", err)
	}
	logf("[image build] guest daemon is ready")

	user := GuestCreds{User: opts.User}
	for i, script := range opts.Scripts {
		logf("[provision %d/%d] starting as %s", i+1, len(opts.Scripts), opts.User)
		path := windowsProvisionScriptPath(i)
		code, err := runGuestCommand(ctx, transport, addr, user, GuestCommand{
			Platform: GuestPlatformWindows,
			Files:    []GuestFile{{Path: path, Data: windowsScriptBytes(script), Mode: 0o644}},
			Args:     windowsProvisionArgs(path),
			JobID:    "image-build",
		}, opts.LogWriter)
		if err != nil {
			return fmt.Errorf("vmrunner: provision script %d: %w", i+1, err)
		}
		if code != 0 {
			return fmt.Errorf("vmrunner: provision script %d exited with code %d", i+1, code)
		}
		logf("[provision %d/%d] done", i+1, len(opts.Scripts))
	}

	logf("[image build] preparing the guest for capture and shutting it down")
	if err := captureAndWaitOff(ctx, w, transport, addr, vmName, opts.ShutdownTimeout, opts.LogWriter); err != nil {
		return err
	}
	logf("[image build] guest is off")

	// The VM is off. Remove it but keep its differencing disk.
	if err := w.forceRemoveVM(ctx, vmName); err != nil {
		return err
	}
	diskPath := filepath.Join(tmp, BundleWindowsDisk)
	logf("[image build] writing a standalone VHDX (Convert-VHD)")
	if _, err := runPowerShell(ctx, windowsImageConvertScript(filepath.Join(scratchPath, jobVHDX), diskPath)); err != nil {
		return fmt.Errorf("vmrunner: convert the build disk to a base image: %w", err)
	}
	cleanupCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	destroyErr := w.Destroy(cleanupCtx, handle)
	cancel()
	destroyed = true
	if destroyErr != nil {
		return fmt.Errorf("vmrunner: remove image build VM (the next sweep tries again): %w", destroyErr)
	}
	if _, err := runPowerShell(ctx, windowsImageVerifyScript(diskPath)); err != nil {
		return fmt.Errorf("vmrunner: check the new image: %w", err)
	}

	// Release the build lock and remove its file: the bundle has only
	// disk.vhdx.
	if build.lock != nil {
		_ = build.lock.Close()
		build.lock = nil
	}
	if err := os.Remove(filepath.Join(tmp, scratchLockFile)); err != nil {
		return fmt.Errorf("vmrunner: remove image build lock: %w", err)
	}
	if err := os.Rename(tmp, output); err != nil {
		return fmt.Errorf("vmrunner: publish built image bundle: %w", err)
	}
	keep = true
	if err := os.Chmod(filepath.Join(output, BundleWindowsDisk), 0o444); err != nil {
		return fmt.Errorf("vmrunner: seal image bundle: %w", err)
	}
	if err := os.Chmod(output, 0o555); err != nil {
		return fmt.Errorf("vmrunner: seal image bundle directory: %w", err)
	}
	logf("image build completed: %s", output)
	return nil
}

// captureAndWaitOff runs windowsCaptureScript in the guest and waits until
// the VM is off. The guest daemon session ends when the guest shuts
// down, so a session error after the start is expected. A nonzero exit code
// before the VM is off means that the preparation failed.
func captureAndWaitOff(ctx context.Context, w *windowsVMLifecycle, transport GuestTransport, addr GuestAddr, vmName string, timeout time.Duration, log io.Writer) error {
	session, err := transport.Start(ctx, addr, GuestCreds{}, GuestCommand{
		Platform: GuestPlatformWindows,
		Args:     powerShellEncodedArgs(windowsCaptureScript()),
		JobID:    "image-build",
	})
	if err != nil {
		return fmt.Errorf("vmrunner: start the capture step: %w", err)
	}
	defer session.Close()
	var copies sync.WaitGroup
	copies.Add(2)
	go func() { defer copies.Done(); _, _ = io.Copy(log, session.Stdout()) }()
	go func() { defer copies.Done(); _, _ = io.Copy(log, session.Stderr()) }()
	// waitCopies lets the last guest output reach the log, but does not
	// wait for a connection that the shutdown left open.
	waitCopies := func() {
		flushed := make(chan struct{})
		go func() { copies.Wait(); close(flushed) }()
		select {
		case <-flushed:
		case <-time.After(5 * time.Second):
		}
	}
	type result struct {
		code int
		err  error
	}
	done := make(chan result, 1)
	go func() {
		code, err := session.Wait()
		done <- result{code, err}
	}()

	deadline := time.Now().Add(timeout)
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case r := <-done:
			done = nil
			if r.err == nil && r.code != 0 {
				waitCopies()
				return fmt.Errorf("vmrunner: the capture step failed (exit code %d); see the log above", r.code)
			}
		case <-ticker.C:
			out, err := runPowerShell(ctx, hypervVMStateScript(vmName))
			if err == nil && strings.EqualFold(strings.TrimSpace(out), "Off") {
				return nil
			}
			if time.Now().After(deadline) {
				return fmt.Errorf("vmrunner: the guest did not shut down within %s", timeout)
			}
		}
	}
}
