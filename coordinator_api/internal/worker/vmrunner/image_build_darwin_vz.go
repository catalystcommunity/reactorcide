//go:build darwin && vz

package vmrunner

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	vz "github.com/Code-Hex/vz/v3"
)

func buildMacImage(ctx context.Context, opts MacImageBuildOptions) error {
	if err := ValidateMacBundle(opts.SourceBundle); err != nil {
		return err
	}
	if opts.OutputBundle == "" {
		return errors.New("vmrunner: output bundle is required")
	}
	if opts.Creds.User == "" {
		opts.Creds.User = "reactorcide"
	}
	if len(opts.Creds.PrivateKeyPEM) == 0 && opts.Creds.Password == "" {
		return errors.New("vmrunner: image build needs an SSH private key or password for the bootstrap image")
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

	config, mac, err := buildVMConfig(tmp, filepath.Join(tmp, BundleDiskImage), filepath.Join(tmp, BundleAuxImage), BootSpec{
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
	ip, err := resolveGuestIP(ctx, mac, leaseResolveTimeout, leaseResolvePoll)
	if err != nil {
		return fmt.Errorf("vmrunner: resolve image build VM address: %w", err)
	}
	addr := GuestAddr{Host: ip, Port: 22}
	if err := waitTCPReachable(ctx, addr, 2*time.Minute, 2*time.Second); err != nil {
		return fmt.Errorf("vmrunner: wait for image build SSH: %w", err)
	}
	transport := NewSSHTransport()
	for i, script := range opts.Scripts {
		if _, err := fmt.Fprintf(opts.LogWriter, "[provision %d/%d] starting\n", i+1, len(opts.Scripts)); err != nil {
			return err
		}
		session, err := transport.Start(ctx, addr, opts.Creds, []string{"/bin/zsh", "-c", script}, map[string]string{"HOME": "/Users/" + opts.Creds.User})
		if err != nil {
			return fmt.Errorf("vmrunner: start provision script %d: %w", i+1, err)
		}
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); _, _ = io.Copy(opts.LogWriter, session.Stdout()) }()
		go func() { defer wg.Done(); _, _ = io.Copy(opts.LogWriter, session.Stderr()) }()
		code, waitErr := session.Wait()
		wg.Wait()
		_ = session.Close()
		if waitErr != nil {
			return fmt.Errorf("vmrunner: provision script %d: %w", i+1, waitErr)
		}
		if code != 0 {
			return fmt.Errorf("vmrunner: provision script %d exited with code %d", i+1, code)
		}
	}

	shutdownEnv := map[string]string{}
	if opts.Creds.Password != "" {
		shutdownEnv["REACTORCIDE_SHUTDOWN_PASSWORD"] = opts.Creds.Password
	}
	shutdownScript := `sync; if sudo -n /sbin/shutdown -h now >/dev/null 2>&1; then exit 0; fi; if [ -n "${REACTORCIDE_SHUTDOWN_PASSWORD:-}" ]; then printf '%s\n' "$REACTORCIDE_SHUTDOWN_PASSWORD" | sudo -S -p '' /sbin/shutdown -h now; else exit 1; fi`
	shutdown, err := transport.Start(ctx, addr, opts.Creds,
		[]string{"/bin/zsh", "-c", shutdownScript}, shutdownEnv)
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
