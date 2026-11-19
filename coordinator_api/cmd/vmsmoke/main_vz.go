//go:build darwin && vz

// Command vmsmoke is a standalone Apple Silicon macOS smoke test for the "vm"
// JobRunner backend's Virtualization.framework lifecycle + guest daemon
// transport. It boots a guest (no network device) from a base bundle
// directory, runs `echo hello` in it through the guest daemon over vsock,
// checks guest egress through the vsock tunnel, prints the output, then tears
// the guest down. Run it to validate vz + the virtio socket device + the guest
// daemon end to end BEFORE wiring the full worker.
//
// Build + sign (it needs the virtualization entitlement, same as the worker):
//
//	cd coordinator_api
//	CGO_ENABLED=1 go build -tags vz -o vmsmoke ./cmd/vmsmoke
//	codesign --entitlements ../deployment/macos/vz.entitlements -s - ./vmsmoke
//
// Run:
//
//	./vmsmoke -bundle /path/to/base-bundle -user reactorcide
//
// See docs/vm-runners-macos.md for producing the base bundle and installing
// the guest daemon (cmd/vmguest) in it.
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
	bundle := flag.String("bundle", "", "path to the base image bundle directory (required)")
	user := flag.String("user", "reactorcide", "guest account that runs the command")
	egressURL := flag.String("egress-url", "https://example.com/", "URL that the guest fetches with curl to check egress (empty skips the check)")
	timeout := flag.Duration("timeout", 5*time.Minute, "overall timeout for the smoke run")
	extra := flag.String("extra", "", "more shell commands that the guest runs after the checks (for hardware checks)")
	flag.Parse()

	if *bundle == "" {
		fmt.Fprintln(os.Stderr, "vmsmoke: -bundle is required")
		flag.Usage()
		os.Exit(2)
	}

	if err := run(*bundle, vmrunner.GuestCreds{User: *user}, *egressURL, *extra, *timeout); err != nil {
		fmt.Fprintf(os.Stderr, "vmsmoke: FAILED: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("vmsmoke: OK")
}

func run(bundle string, creds vmrunner.GuestCreds, egressURL, extra string, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	// NewDefault selects the real darwin VMLifecycle (this is a `-tags vz`
	// build), the guest daemon GuestTransport, and a LocalImageSource. Passing "" as the
	// base dir means the bundle path below is used as an absolute image ref.
	runner, err := vmrunner.NewDefaultWithImages(vmrunner.NewLocalImageSource(""), creds)
	if err != nil {
		return fmt.Errorf("build vm runner: %w", err)
	}

	fmt.Printf("vmsmoke: booting guest from bundle %q (no network device; vsock only)...\n", bundle)

	script := "echo hello"
	if egressURL != "" {
		// The guest has no network device, so this reaches the network only
		// through the transparent route and the vsock egress tunnel.
		script += "; curl -fsS -o /dev/null -w 'egress HTTP %{http_code}\\n' " + shellQuote(egressURL)
	}
	if extra != "" {
		script += "; " + extra
	}
	job := &vmrunner.JobConfig{
		Image:   bundle,
		Command: []string{"/bin/sh", "-c", script},
		JobID:   "vmsmoke",
	}

	id, err := runner.SpawnJob(ctx, job)
	if err != nil {
		return fmt.Errorf("spawn job (boot + guest daemon): %w", err)
	}
	// Always attempt cleanup (Destroy) even if the rest fails.
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
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

func shellQuote(s string) string {
	out := "'"
	for _, r := range s {
		if r == '\'' {
			out += `'\''`
			continue
		}
		out += string(r)
	}
	return out + "'"
}
