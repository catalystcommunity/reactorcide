package vmguest

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"runtime"
	"time"
)

// DaemonConfig configures the guest daemon.
type DaemonConfig struct {
	// Listen is the job channel listen spec (see Listen).
	Listen string
	// Egress is the dial spec of the host egress forwarder (see Dialer).
	Egress string
	// ProxyListen is the local HTTP proxy address for proxy-aware programs.
	// Empty disables it.
	ProxyListen string
	// Transparent sets the macOS transparent route (routes to lo0, a pf
	// redirect to TransparentListen, and the fake DNS at DNSListen).
	Transparent       bool
	TransparentListen string
	DNSListen         string
	// SystemProxy sets the Windows WinHTTP and WinINET proxy settings to
	// ProxyListen.
	SystemProxy bool
}

// DefaultDaemonConfig returns the configuration for this guest OS.
func DefaultDaemonConfig() DaemonConfig {
	cfg := DaemonConfig{ProxyListen: "127.0.0.1:3128"}
	switch runtime.GOOS {
	case "darwin":
		cfg.Listen = fmt.Sprintf("vsock:%d", JobPort)
		cfg.Egress = fmt.Sprintf("vsock:%d:%d", HostCID, EgressPort)
		cfg.Transparent = true
		cfg.TransparentListen = "127.0.0.1:15001"
		cfg.DNSListen = "127.0.0.1:53"
	case "windows":
		cfg.Listen = fmt.Sprintf("hvsock:%d", JobPort)
		cfg.Egress = fmt.Sprintf("hvsock:%d", EgressPort)
		cfg.SystemProxy = true
	}
	return cfg
}

// RunDaemon starts the egress parts and serves the job channel until ctx
// ends.
func RunDaemon(ctx context.Context, cfg DaemonConfig, log *slog.Logger) error {
	if cfg.Listen == "" {
		return fmt.Errorf("vmguest: no job channel listen spec for %s", runtime.GOOS)
	}
	var tunnel Tunnel
	if cfg.Egress != "" {
		dial, err := Dialer(cfg.Egress)
		if err != nil {
			return err
		}
		tunnel = Tunnel{Dial: dial}
	}
	var extraEnv []string
	if cfg.ProxyListen != "" {
		ln, err := net.Listen("tcp", cfg.ProxyListen)
		if err != nil {
			return fmt.Errorf("vmguest: egress proxy listener: %w", err)
		}
		defer ln.Close()
		go func() { _ = ServeProxy(ln, tunnel, log) }()
	}
	transparent := false
	if cfg.Transparent {
		if err := startTransparent(cfg, tunnel, log); err != nil {
			log.Warn("transparent route is not available; commands get proxy variables", "err", err)
		} else {
			transparent = true
		}
	}
	if !transparent && cfg.ProxyListen != "" {
		extraEnv = ProxyEnv(cfg.ProxyListen)
	}
	if cfg.SystemProxy && cfg.ProxyListen != "" {
		if err := applySystemProxy(cfg.ProxyListen); err != nil {
			log.Warn("system proxy settings", "err", err)
		}
	}

	// The socket device can come up after the daemon at boot: retry.
	var ln net.Listener
	deadline := time.Now().Add(2 * time.Minute)
	for {
		var err error
		ln, err = Listen(cfg.Listen)
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("vmguest: job channel listener: %w", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
	log.Info("guest daemon ready", "listen", cfg.Listen, "egress", cfg.Egress, "transparent", transparent, "version", ProtocolVersion)
	srv := &Server{ExtraEnv: extraEnv, Log: log}
	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(ln) }()
	select {
	case <-ctx.Done():
		ln.Close()
		return nil
	case err := <-errCh:
		return err
	}
}
