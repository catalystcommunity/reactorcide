//go:build windows

// Command vmsmoke is a standalone Windows smoke test for the "vm" JobRunner
// backend's Hyper-V lifecycle + guest daemon transport. It boots a guest (no
// network adapter) from a base VHDX bundle directory, runs a PowerShell
// command in it through the guest daemon over a Hyper-V socket, checks guest
// egress through the proxy tunnel, prints the output, then tears the guest
// down. Run it to validate Hyper-V + Hyper-V sockets + the guest daemon end to
// end BEFORE wiring the full worker.
//
// Unlike the macOS smoke target this is PURE Go (it shells out to
// powershell.exe and uses go-winio), so no Cgo, build tag, or code-signing is
// needed:
//
//	cd coordinator_api
//	go build -o vmsmoke.exe ./cmd/vmsmoke
//
// Run it elevated (the first run registers the Hyper-V socket services):
//
//	.\vmsmoke.exe -bundle C:\reactorcide\vm-images\win11-base -user reactorcide
//
// See docs/vm-runners-windows.md for producing the base bundle with the guest
// daemon service in it.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/worker/vmrunner"
)

func main() {
	bundle := flag.String("bundle", "", "path to the base image bundle directory (holding disk.vhdx) or a base .vhdx file (required)")
	user := flag.String("user", "reactorcide", "guest account that runs the command (the guest daemon creates it if it is missing)")
	egressURL := flag.String("egress-url", "https://example.com/", "URL that the guest fetches through the system proxy to check egress (empty skips the check)")
	timeout := flag.Duration("timeout", 10*time.Minute, "overall timeout for the smoke run")
	extra := flag.String("extra", "", "more PowerShell commands that the guest runs after the checks (for hardware checks)")
	createAdmin := flag.Bool("create-admin", true, "when the guest daemon creates the account, put it in Administrators (job run_as.administrator)")
	termCheck := flag.Bool("term-check", false, "check only that TERM stops the process tree of a job: start a job with a child process, stop it, and count the child processes that are left")
	flag.Parse()

	if *bundle == "" {
		fmt.Fprintln(os.Stderr, "vmsmoke: -bundle is required")
		flag.Usage()
		os.Exit(2)
	}

	opts := smokeOptions{egressURL: *egressURL, extra: *extra, createAdmin: *createAdmin, termCheck: *termCheck}
	if err := run(*bundle, vmrunner.GuestCreds{User: *user}, opts, *timeout); err != nil {
		fmt.Fprintf(os.Stderr, "vmsmoke: FAILED: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("vmsmoke: OK")
}

type smokeOptions struct {
	egressURL   string
	extra       string
	createAdmin bool
	termCheck   bool
}

func run(bundle string, creds vmrunner.GuestCreds, opts smokeOptions, timeout time.Duration) error {
	egressURL, extra := opts.egressURL, opts.extra
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	// NewDefault selects the real windows (Hyper-V) VMLifecycle, the guest
	// daemon GuestTransport, and a LocalImageSource. Passing "" as the base dir
	// means the bundle path below is used as an absolute image ref.
	runner, err := vmrunner.NewDefaultWithImages(vmrunner.NewLocalImageSource(""), creds)
	if err != nil {
		return fmt.Errorf("build vm runner: %w", err)
	}

	fmt.Printf("vmsmoke: booting guest from bundle %q (no network adapter; Hyper-V sockets only)...\n", bundle)

	script := "Write-Output hello; Start-Sleep -Seconds 5"
	if egressURL != "" {
		// The guest has no network adapter: curl.exe uses HTTPS_PROXY, the
		// local proxy of the guest daemon, and the Hyper-V socket tunnel.
		script += "; & curl.exe -fsS -o NUL -w 'egress HTTP %{http_code}' '" + strings.ReplaceAll(egressURL, "'", "''") + "'; if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }"
	}
	if extra != "" {
		script += "; " + extra
	}
	if opts.termCheck {
		// A child process that runs until something stops it.
		script = "Write-Output started; Start-Process -FilePath ping.exe -ArgumentList '-t','127.0.0.1' -NoNewWindow; Start-Sleep -Seconds 600"
	}
	createAdmin := opts.createAdmin
	job := &vmrunner.JobConfig{
		Image:    bundle,
		Command:  []string{"powershell.exe", "-NoProfile", "-NonInteractive", "-Command", script},
		Platform: vmrunner.GuestPlatformWindows,
		JobID:    "vmsmoke",

		CreateAdministrator: &createAdmin,
	}

	id, err := runner.SpawnJob(ctx, job)
	if err != nil {
		return fmt.Errorf("spawn job (boot + guest daemon): %w", err)
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

	if opts.termCheck {
		return termCheck(ctx, runner, id)
	}

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

// termCheck stops the job with TERM and checks that no ping.exe child is left
// in the guest.
func termCheck(ctx context.Context, runner *vmrunner.VMRunner, id string) error {
	time.Sleep(10 * time.Second)
	count := []string{"powershell.exe", "-NoProfile", "-NonInteractive", "-Command", "@(Get-Process -Name ping -ErrorAction SilentlyContinue).Count"}
	before, _, err := runner.RunDiagnostic(ctx, id, count)
	if err != nil {
		return fmt.Errorf("count child processes: %w", err)
	}
	if strings.TrimSpace(before) == "0" {
		return fmt.Errorf("the job did not start its child process")
	}
	start := time.Now()
	if err := runner.Stop(ctx, id, 30*time.Second); err != nil {
		return fmt.Errorf("stop the job: %w", err)
	}
	code, err := runner.WaitForCompletion(ctx, id)
	if err != nil {
		return fmt.Errorf("wait for the stopped job: %w", err)
	}
	fmt.Printf("vmsmoke: TERM stopped the job in %s (exit code %d)\n", time.Since(start).Round(time.Millisecond), code)
	after, _, err := runner.RunDiagnostic(ctx, id, count)
	if err != nil {
		return fmt.Errorf("count child processes after TERM: %w", err)
	}
	fmt.Printf("vmsmoke: ping.exe processes before TERM %s, after TERM %s\n", strings.TrimSpace(before), strings.TrimSpace(after))
	if strings.TrimSpace(after) != "0" {
		return fmt.Errorf("TERM left %s child processes", strings.TrimSpace(after))
	}
	fmt.Println("vmsmoke: TERM stopped the process tree")
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
