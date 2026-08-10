//go:build darwin && vz

// Command vmsmoke is a standalone Apple Silicon macOS smoke test for the "vm"
// JobRunner backend's Virtualization.framework lifecycle + SSH transport. It
// boots a guest from a base bundle directory, runs `echo hello` in it over the
// existing vmrunner SSH transport, prints the output, then tears the guest
// down. Run it to validate vz + NAT networking + guest SSH end to end BEFORE
// wiring the full worker.
//
// Build + sign (it needs the virtualization entitlement, same as the worker):
//
//	cd coordinator_api
//	CGO_ENABLED=1 go build -tags vz -o vmsmoke ./cmd/vmsmoke
//	codesign --entitlements deployment/macos/vz.entitlements -s - ./vmsmoke
//
// Run (the private key path is read from a file; its contents never go on the
// command line or into logs):
//
//	./vmsmoke -bundle /path/to/base-bundle -user reactorcide -key ~/.ssh/reactorcide_vm
//
// See docs/vm-runners-macos.md for producing the base bundle and baking in the
// worker's SSH public key.
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
	user := flag.String("user", "reactorcide", "guest SSH user")
	keyFile := flag.String("key", "", "path to the worker's SSH private key file (PEM)")
	password := flag.String("password", "", "guest SSH password (used only if -key is empty; discouraged)")
	timeout := flag.Duration("timeout", 5*time.Minute, "overall timeout for the smoke run")
	flag.Parse()

	if *bundle == "" {
		fmt.Fprintln(os.Stderr, "vmsmoke: -bundle is required")
		flag.Usage()
		os.Exit(2)
	}

	creds := vmrunner.GuestCreds{User: *user, Password: *password}
	if *keyFile != "" {
		key, err := os.ReadFile(*keyFile)
		if err != nil {
			fmt.Fprintf(os.Stderr, "vmsmoke: read key file: %v\n", err)
			os.Exit(1)
		}
		creds.PrivateKeyPEM = key
	}
	if len(creds.PrivateKeyPEM) == 0 && creds.Password == "" {
		fmt.Fprintln(os.Stderr, "vmsmoke: need -key or -password to authenticate to the guest")
		os.Exit(2)
	}

	if err := run(*bundle, creds, *timeout); err != nil {
		fmt.Fprintf(os.Stderr, "vmsmoke: FAILED: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("vmsmoke: OK")
}

func run(bundle string, creds vmrunner.GuestCreds, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	// NewDefault selects the real darwin VMLifecycle (this is a `-tags vz`
	// build), the SSH GuestTransport, and a LocalImageSource. Passing "" as the
	// base dir means the bundle path below is used as an absolute image ref.
	runner, err := vmrunner.NewDefaultWithImages(vmrunner.NewLocalImageSource(""), creds)
	if err != nil {
		return fmt.Errorf("build vm runner: %w", err)
	}

	fmt.Printf("vmsmoke: booting guest from bundle %q (the guest IP is logged as guest_ip once DHCP assigns it)...\n", bundle)

	job := &vmrunner.JobConfig{
		Image:   bundle,
		Command: []string{"echo", "hello"},
		JobID:   "vmsmoke",
	}

	id, err := runner.SpawnJob(ctx, job)
	if err != nil {
		return fmt.Errorf("spawn job (boot + ssh): %w", err)
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
