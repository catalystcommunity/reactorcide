//go:build windows

// Command vmsmoke is a standalone Windows smoke test for the "vm" JobRunner
// backend's Hyper-V lifecycle + SSH transport. It boots a guest from a base
// VHDX bundle directory, runs `cmd /c echo hello` in it over the existing
// vmrunner SSH transport, prints the output, then tears the guest down. Run it
// to validate Hyper-V + the virtual switch + guest SSH end to end BEFORE wiring
// the full worker.
//
// Unlike the macOS smoke target this is PURE Go (it shells out to
// powershell.exe), so no Cgo, build tag, or code-signing is needed:
//
//	cd coordinator_api
//	go build -o vmsmoke.exe ./cmd/vmsmoke
//
// Run it elevated (or as a user in the Hyper-V Administrators group):
//
//	.\vmsmoke.exe -bundle C:\reactorcide\vm-images\win11-base -user reactorcide
//
// See docs/vm-runners-windows.md for producing the base bundle and the switch
// note. The lifecycle injects new client and host keys into each VM clone.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/worker/vmrunner"
)

func main() {
	bundle := flag.String("bundle", "", "path to the base image bundle directory (holding disk.vhdx) or a base .vhdx file (required)")
	user := flag.String("user", "reactorcide", "guest SSH user")
	timeout := flag.Duration("timeout", 10*time.Minute, "overall timeout for the smoke run")
	flag.Parse()

	if *bundle == "" {
		fmt.Fprintln(os.Stderr, "vmsmoke: -bundle is required")
		flag.Usage()
		os.Exit(2)
	}

	if err := run(*bundle, vmrunner.GuestCreds{User: *user}, *timeout); err != nil {
		fmt.Fprintf(os.Stderr, "vmsmoke: FAILED: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("vmsmoke: OK")
}

func run(bundle string, creds vmrunner.GuestCreds, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	// NewDefault selects the real windows (Hyper-V) VMLifecycle, the SSH
	// GuestTransport, and a LocalImageSource. Passing "" as the base dir means
	// the bundle path below is used as an absolute image ref. The Hyper-V switch
	// comes from REACTORCIDE_VM_HYPERV_SWITCH (default "Default Switch").
	runner, err := vmrunner.NewDefaultWithImages(vmrunner.NewLocalImageSource(""), creds)
	if err != nil {
		return fmt.Errorf("build vm runner: %w", err)
	}

	fmt.Printf("vmsmoke: booting guest from bundle %q (the guest IP is logged as guest_ip once DHCP assigns it)...\n", bundle)

	job := &vmrunner.JobConfig{
		Image:    bundle,
		Command:  []string{"powershell.exe", "-NoProfile", "-NonInteractive", "-Command", "Write-Output hello; Start-Sleep -Seconds 5"},
		Platform: vmrunner.GuestPlatformWindows,
		JobID:    "vmsmoke",
	}

	id, err := runner.SpawnJob(ctx, job)
	if err != nil {
		return fmt.Errorf("spawn job (boot + ssh): %w", err)
	}
	// Always attempt cleanup (Destroy) even if the rest fails.
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
		defer cancel()
		if err := runner.Cleanup(cleanupCtx, id); err != nil {
			fmt.Fprintf(os.Stderr, "vmsmoke: cleanup warning: %v\n", err)
		}
	}()

	stdout, stderr, err := runner.StreamLogs(ctx, id)
	if err != nil {
		return fmt.Errorf("stream logs: %w", err)
	}
	go func() { _, _ = io.Copy(prefixWriter{os.Stdout, "[guest stdout] "}, stdout) }()
	go func() { _, _ = io.Copy(prefixWriter{os.Stderr, "[guest stderr] "}, stderr) }()

	code, err := runner.WaitForCompletion(ctx, id)
	if err != nil {
		return fmt.Errorf("wait for completion: %w", err)
	}
	// Give the stream copiers a beat to flush.
	time.Sleep(200 * time.Millisecond)

	if code != 0 {
		return fmt.Errorf("guest command exited with code %d", code)
	}
	fmt.Println("vmsmoke: guest command exited 0")
	sample, err := runner.SampleResource(ctx, id, true)
	if err != nil {
		return fmt.Errorf("collect VM resource metrics: %w", err)
	}
	if sample.CPUCount == 0 || sample.MemoryTotalBytes == 0 || sample.StorageTotalBytes == 0 {
		return fmt.Errorf("VM resource metrics are incomplete: cpu_count=%d memory_total_bytes=%d storage_total_bytes=%d",
			sample.CPUCount, sample.MemoryTotalBytes, sample.StorageTotalBytes)
	}
	fmt.Printf("vmsmoke: metrics cpu_count=%d memory_total_bytes=%d storage_total_bytes=%d\n",
		sample.CPUCount, sample.MemoryTotalBytes, sample.StorageTotalBytes)
	return nil
}

// prefixWriter tags each line-ish chunk from the guest so smoke output is
// readable. It intentionally does no buffering/parsing -- this is a smoke tool.
type prefixWriter struct {
	w      io.Writer
	prefix string
}

func (p prefixWriter) Write(b []byte) (int, error) {
	if _, err := io.WriteString(p.w, p.prefix); err != nil {
		return 0, err
	}
	return p.w.Write(b)
}
