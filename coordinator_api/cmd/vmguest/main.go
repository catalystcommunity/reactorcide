// Command vmguest is the Reactorcide guest daemon for the "vm" job backend.
// It runs inside a macOS or Windows job VM that has no network card:
//
//   - It serves the job channel on a hypervisor socket (vsock on macOS,
//     hvsock on Hyper-V): the worker sends files, trees, and a command, and
//     the daemon streams the output and the exit status back.
//   - It sends each guest TCP flow as an HTTP CONNECT tunnel to the host
//     egress forwarder. macOS guests get a transparent route (routes to lo0,
//     a pf redirect, a fake DNS). Windows guests get proxy settings for
//     proxy-aware programs only (a transparent redirect driver is a TODO).
//
// It is a separate small binary, not a subcommand of reactorcide: it runs as
// root (launchd) or LocalSystem (a Windows service) inside an untrusted job VM,
// so it contains only the job channel and the tunnel. It cross-compiles with
// CGO_ENABLED=0:
//
//	GOOS=darwin GOARCH=arm64 CGO_ENABLED=0 go build -o vmguest ./cmd/vmguest
//	GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -o vmguest.exe ./cmd/vmguest
//
// Usage:
//
//	vmguest serve [flags]   run the daemon (launchd or the service manager runs this)
//	vmguest install [-no-start]  install the daemon in this guest (root / elevated)
//	vmguest uninstall       remove the daemon from this guest
//	vmguest version         print the protocol version
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/vmguest"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "serve":
		err = serve(os.Args[2:])
	case "install":
		fs := flag.NewFlagSet("install", flag.ExitOnError)
		noStart := fs.Bool("no-start", false, "install only; the daemon starts at the next boot")
		_ = fs.Parse(os.Args[2:])
		err = vmguest.Install(!*noStart)
	case "uninstall":
		err = vmguest.Uninstall()
	case "version":
		fmt.Printf("vmguest protocol %d\n", vmguest.ProtocolVersion)
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "vmguest:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: vmguest serve|install|uninstall|version")
}

func serve(args []string) error {
	cfg := vmguest.DefaultDaemonConfig()
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	fs.StringVar(&cfg.Listen, "listen", cfg.Listen, "job channel listen spec: vsock:PORT, hvsock:PORT, unix:PATH, or tcp:HOST:PORT")
	fs.StringVar(&cfg.Egress, "egress", cfg.Egress, "host egress forwarder: vsock:CID:PORT, hvsock:PORT, unix:PATH, or tcp:HOST:PORT")
	fs.StringVar(&cfg.ProxyListen, "proxy-listen", cfg.ProxyListen, "local HTTP proxy for proxy-aware programs (empty disables it)")
	fs.BoolVar(&cfg.Transparent, "transparent", cfg.Transparent, "macOS: route all TCP through the tunnel (lo0 routes, pf redirect, fake DNS)")
	fs.StringVar(&cfg.TransparentListen, "transparent-listen", cfg.TransparentListen, "macOS: the pf redirect target")
	fs.StringVar(&cfg.DNSListen, "dns-listen", cfg.DNSListen, "macOS: the fake DNS address")
	fs.BoolVar(&cfg.SystemProxy, "system-proxy", cfg.SystemProxy, "Windows: set WinHTTP and WinINET proxy settings to the local proxy")
	logFile := fs.String("log", defaultLogFile(), "log file (empty: standard error)")
	_ = fs.Parse(args)

	var out io.Writer = os.Stderr
	if *logFile != "" {
		_ = os.MkdirAll(filepath.Dir(*logFile), 0o750)
		f, err := os.OpenFile(*logFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o640)
		if err != nil {
			return err
		}
		defer f.Close()
		out = f
	}
	log := slog.New(slog.NewTextHandler(out, nil))

	run := func(stop <-chan struct{}) error {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		go func() {
			<-stop
			cancel()
		}()
		return vmguest.RunDaemon(ctx, cfg, log)
	}
	if isService, err := vmguest.RunService(run); isService || err != nil {
		return err
	}
	stop := make(chan struct{})
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sig
		close(stop)
	}()
	return run(stop)
}
