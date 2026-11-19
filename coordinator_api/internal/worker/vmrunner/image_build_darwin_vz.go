//go:build darwin && vz

package vmrunner

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	vz "github.com/Code-Hex/vz/v3"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/vmguest"
)

func buildMacImage(ctx context.Context, opts MacImageBuildOptions) error {
	if err := ValidateMacBundle(opts.SourceBundle); err != nil {
		return err
	}
	if opts.OutputBundle == "" {
		return errors.New("vmrunner: output bundle is required")
	}
	if opts.User == "" {
		opts.User = "reactorcide"
	}
	var guestDaemon []byte
	if opts.GuestDaemon != "" {
		data, err := os.ReadFile(opts.GuestDaemon)
		if err != nil {
			return fmt.Errorf("vmrunner: read guest daemon: %w", err)
		}
		guestDaemon = data
	}
	egressDialer, err := EgressDialerFromEnv()
	if err != nil {
		return err
	}
	if opts.LogWriter == nil {
		opts.LogWriter = io.Discard
	}
	if _, err := os.Stat(opts.OutputBundle); err == nil {
		return fmt.Errorf("vmrunner: output bundle %q already exists", opts.OutputBundle)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("vmrunner: inspect output bundle: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(opts.OutputBundle), 0o755); err != nil {
		return fmt.Errorf("vmrunner: create output parent: %w", err)
	}
	tmp, err := os.MkdirTemp(filepath.Dir(opts.OutputBundle), ".reactorcide-image-build-")
	if err != nil {
		return fmt.Errorf("vmrunner: create image build directory: %w", err)
	}
	keep := false
	defer func() {
		if !keep {
			_ = os.Chmod(tmp, 0o700)
			_ = os.RemoveAll(tmp)
		}
	}()

	for _, name := range macBundleFiles {
		if err := cloneFile(filepath.Join(opts.SourceBundle, name), filepath.Join(tmp, name)); err != nil {
			return fmt.Errorf("vmrunner: clone bootstrap %s: %w", name, err)
		}
	}
	for _, name := range []string{BundleDiskImage, BundleAuxImage} {
		if err := os.Chmod(filepath.Join(tmp, name), 0o600); err != nil {
			return fmt.Errorf("vmrunner: make build clone writable: %w", err)
		}
	}

	config, err := buildVMConfig(tmp, filepath.Join(tmp, BundleDiskImage), filepath.Join(tmp, BundleAuxImage), BootSpec{
		CPUs: opts.CPUs, MemoryBytes: opts.MemoryBytes, Label: "reactorcide-image-build",
	})
	if err != nil {
		return err
	}
	vm, err := vz.NewVirtualMachine(config)
	if err != nil {
		return fmt.Errorf("vmrunner: create image build VM: %w", err)
	}
	if err := vm.Start(); err != nil {
		return fmt.Errorf("vmrunner: start image build VM: %w", err)
	}
	defer func() {
		if vm.State() != vz.VirtualMachineStateStopped {
			_ = stopVM(vm)
		}
	}()
	if err := waitForRunning(ctx, vm, bootStateTimeout); err != nil {
		return fmt.Errorf("vmrunner: wait for image build VM: %w", err)
	}
	dev, err := socketDevice(vm)
	if err != nil {
		return err
	}
	egress, err := listenEgress(dev, egressDialer, "image-build")
	if err != nil {
		return err
	}
	defer egress.Close()
	addr := GuestAddr{Dial: vsockGuestDialer(dev)}
	transport := NewVsockTransport()
	if err := waitGuestReachable(ctx, transport, addr, 5*time.Minute, 2*time.Second); err != nil {
		return fmt.Errorf("vmrunner: wait for the image build guest daemon (is vmguest installed in the source image?): %w", err)
	}
	root := GuestCreds{}
	user := GuestCreds{User: opts.User}
	if guestDaemon != nil {
		next := vmguest.MacDaemonPath + ".new"
		code, err := runGuestCommand(ctx, transport, addr, root, GuestCommand{
			Platform: GuestPlatformPOSIX,
			Files:    []GuestFile{{Path: next, Data: guestDaemon, Mode: 0o755}},
			Args:     []string{"/bin/mv", "-f", next, vmguest.MacDaemonPath},
		}, opts.LogWriter)
		if err != nil {
			return fmt.Errorf("vmrunner: install guest daemon: %w", err)
		}
		if code != 0 {
			return fmt.Errorf("vmrunner: install guest daemon exited with code %d", code)
		}
	}
	// /job: images from before this step have no writable /job (the macOS
	// system volume is read-only), so each build makes it.
	code, err := runGuestCommand(ctx, transport, addr, root, GuestCommand{
		Platform: GuestPlatformPOSIX,
		Args:     []string{"/bin/sh", "-c", vmguest.MacJobDirSetupScript},
	}, opts.LogWriter)
	if err != nil {
		return fmt.Errorf("vmrunner: set up /job: %w", err)
	}
	if code != 0 {
		return fmt.Errorf("vmrunner: set up /job exited with code %d", code)
	}
	for i, script := range opts.Scripts {
		if _, err := fmt.Fprintf(opts.LogWriter, "[provision %d/%d] starting\n", i+1, len(opts.Scripts)); err != nil {
			return err
		}
		code, waitErr := runGuestCommand(ctx, transport, addr, user, GuestCommand{
			Platform: GuestPlatformPOSIX,
			Args:     []string{"/bin/zsh", "-c", script},
			Env:      map[string]string{"HOME": "/Users/" + opts.User},
		}, opts.LogWriter)
		if waitErr != nil {
			return fmt.Errorf("vmrunner: provision script %d: %w", i+1, waitErr)
		}
		if code != 0 {
			return fmt.Errorf("vmrunner: provision script %d exited with code %d", i+1, code)
		}
	}

	// The guest daemon runs as root, so the shutdown needs no sudo policy.
	shutdown, err := transport.Start(ctx, addr, root, GuestCommand{
		Platform: GuestPlatformPOSIX,
		Args:     []string{"/bin/sh", "-c", "sync; /sbin/shutdown -h now"},
	})
	if err == nil {
		go func() { _, _ = io.Copy(io.Discard, shutdown.Stdout()) }()
		go func() { _, _ = io.Copy(io.Discard, shutdown.Stderr()) }()
	}
	if vm.CanRequestStop() {
		_, _ = vm.RequestStop()
	}
	waitForStopped(vm, 75*time.Second)
	if shutdown != nil {
		_ = shutdown.Close()
	}
	if vm.State() != vz.VirtualMachineStateStopped && opts.AllowUncleanStop {
		_, _ = fmt.Fprintln(opts.LogWriter, "warning: guest did not shut down cleanly; forcing stop after sync")
		if err := vm.Stop(); err != nil {
			return fmt.Errorf("vmrunner: force-stop image build VM: %w", err)
		}
		waitForStopped(vm, 15*time.Second)
	}
	if vm.State() != vz.VirtualMachineStateStopped {
		return fmt.Errorf("vmrunner: image build VM did not shut down cleanly (state %s)", vm.State())
	}
	for _, name := range macBundleFiles {
		if err := os.Chmod(filepath.Join(tmp, name), 0o444); err != nil {
			return fmt.Errorf("vmrunner: seal image bundle: %w", err)
		}
	}
	if err := os.Chmod(tmp, 0o555); err != nil {
		return fmt.Errorf("vmrunner: seal image bundle directory: %w", err)
	}
	if err := os.Rename(tmp, opts.OutputBundle); err != nil {
		return fmt.Errorf("vmrunner: publish built image bundle: %w", err)
	}
	keep = true
	_, _ = fmt.Fprintln(opts.LogWriter, "image build completed")
	return nil
}
