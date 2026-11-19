// Package netd sends all egress of a job through the Reactorcide egress
// proxy. It is a copy of the network parts of Magi envd (podnet, netns, the
// transparent route, and the fake DNS).
//
// Commands:
//
//	podnet -exclude IP:PORT [-ca-dir DIR]
//	    Installs the firewall of the job network namespace and exits. It
//	    runs in a Kubernetes init container with only CAP_NET_ADMIN.
//	serve -proxy IP:PORT [-listen ADDR] [-dns ADDR] [-setup] [-exclude IP:PORT] [-ca-dir DIR]
//	    Serves the local proxy port, the transparent port, and the fake DNS
//	    until SIGTERM. With -setup it first does what podnet does (the
//	    Docker and containerd helper container, which holds the network
//	    namespace of the job).
//	netns -- PROGRAM [ARGS]
//	    Sets up a new network namespace with no real interface, then runs
//	    PROGRAM (for runners with user namespaces).
package netd

import (
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	// LocalProxyAddr is the proxy address for proxy-aware tools
	// (HTTP_PROXY and HTTPS_PROXY of the job).
	LocalProxyAddr = "127.0.0.1:3128"
	// TransparentPort is the port that the firewall sends each other TCP
	// connection to.
	TransparentPort = 15001
	// DNSPort is the port that the firewall sends DNS (port 53) to.
	DNSPort = 15053

	// EnvProxyUser and EnvProxyPassword are the proxy credential of the job.
	EnvProxyUser     = "REACTORCIDE_EGRESS_PROXY_USER"
	EnvProxyPassword = "REACTORCIDE_EGRESS_PROXY_PASSWORD"
	// EnvCAPEM is the CA certificate of the proxy (PEM).
	EnvCAPEM = "REACTORCIDE_EGRESS_CA_PEM"
	// CAFileName is the name of the CA file in -ca-dir.
	CAFileName = "ca.crt"
)

const usage = `usage:
  reactorcide-egress-net podnet -exclude IP:PORT [-ca-dir DIR]
  reactorcide-egress-net serve -proxy IP:PORT [-listen 127.0.0.1:3128] [-dns 127.0.0.1:15053] [-setup] [-exclude IP:PORT] [-ca-dir DIR]
  reactorcide-egress-net netns -- PROGRAM [ARGS]
  reactorcide-egress-net check [-addr 127.0.0.1:3128]`

// Main runs a command and returns the exit status.
func Main(args []string) int {
	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, usage)
		return 2
	}
	var err error
	switch args[0] {
	case "podnet":
		err = podnetCommand(args[1:])
	case "serve":
		err = serveCommand(args[1:], log, nil)
	case "netns":
		err = netnsExec(args[1:])
	case "check":
		err = checkCommand(args[1:])
	case "-h", "--help", "help":
		fmt.Println(usage)
		return 0
	default:
		err = fmt.Errorf("unknown command %q\n%s", args[0], usage)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "reactorcide-egress-net %s: %v\n", args[0], err)
		return 1
	}
	return 0
}

type podnetFlags struct {
	exclude netip.AddrPort
	caDir   string
}

func parsePodnet(args []string) (podnetFlags, error) {
	fs := flag.NewFlagSet("podnet", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	exclude := fs.String("exclude", "", "IP:PORT of the egress proxy")
	caDir := fs.String("ca-dir", "", "write $"+EnvCAPEM+" to DIR/"+CAFileName)
	if err := fs.Parse(args); err != nil {
		return podnetFlags{}, err
	}
	px, err := netip.ParseAddrPort(*exclude)
	if err != nil {
		return podnetFlags{}, fmt.Errorf("-exclude: %w", err)
	}
	return podnetFlags{exclude: px, caDir: *caDir}, nil
}

func podnetCommand(args []string) error {
	f, err := parsePodnet(args)
	if err != nil {
		return err
	}
	if err := writeCA(f.caDir, os.Getenv(EnvCAPEM)); err != nil {
		return err
	}
	return installRules(f.exclude)
}

type serveFlags struct {
	proxy   netip.AddrPort
	listen  string
	dns     []string
	setup   bool
	exclude netip.AddrPort
	caDir   string
}

func parseServe(args []string) (serveFlags, error) {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	proxy := fs.String("proxy", "", "IP:PORT of the egress proxy")
	listen := fs.String("listen", LocalProxyAddr, "the local proxy port; empty turns it off")
	dns := fs.String("dns", "127.0.0.1:"+strconv.Itoa(DNSPort), "the fake DNS addresses, separated by commas")
	setup := fs.Bool("setup", false, "install the firewall first (as podnet)")
	exclude := fs.String("exclude", "", "with -setup: IP:PORT that the firewall does not redirect (default: -proxy)")
	caDir := fs.String("ca-dir", "", "write $"+EnvCAPEM+" to DIR/"+CAFileName)
	if err := fs.Parse(args); err != nil {
		return serveFlags{}, err
	}
	px, err := netip.ParseAddrPort(*proxy)
	if err != nil {
		return serveFlags{}, fmt.Errorf("-proxy: %w", err)
	}
	out := serveFlags{proxy: px, listen: *listen, setup: *setup, exclude: px, caDir: *caDir}
	if *exclude != "" {
		if out.exclude, err = netip.ParseAddrPort(*exclude); err != nil {
			return serveFlags{}, fmt.Errorf("-exclude: %w", err)
		}
	}
	for _, a := range strings.Split(*dns, ",") {
		if a = strings.TrimSpace(a); a != "" {
			out.dns = append(out.dns, a)
		}
	}
	return out, nil
}

// proxyURL is the proxy with the credential of the job from the
// environment.
func proxyURL(proxy netip.AddrPort) *url.URL {
	u := &url.URL{Scheme: "http", Host: proxy.String()}
	if user := os.Getenv(EnvProxyUser); user != "" {
		u.User = url.UserPassword(user, os.Getenv(EnvProxyPassword))
	}
	return u
}

// serveCommand serves until SIGTERM or SIGINT, or until stop (tests) is
// closed.
func serveCommand(args []string, log *slog.Logger, stop <-chan struct{}) error {
	f, err := parseServe(args)
	if err != nil {
		return err
	}
	if err := writeCA(f.caDir, os.Getenv(EnvCAPEM)); err != nil {
		return err
	}
	if f.setup {
		if err := installRules(f.exclude); err != nil {
			return err
		}
	}
	dial := tcpDialer(f.proxy.String())
	if f.listen != "" {
		ln, err := startForward(f.listen, dial, log)
		if err != nil {
			return err
		}
		defer ln.Close()
	}
	if err := startTransparent(dial, proxyURL(f.proxy), f.dns, "/etc/resolv.conf", log); err != nil {
		return err
	}
	log.Info("egress network ready", "proxy", f.proxy.String(), "listen", f.listen, "dns", f.dns)
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, os.Interrupt)
	defer signal.Stop(sig)
	select {
	case <-sig:
	case <-stop:
	}
	log.Info("egress network stopped")
	return nil
}

// startTransparent serves the transparent port and the fake DNS at each
// address in dns. The connections go through the proxy.
func startTransparent(dial func() (net.Conn, error), pu *url.URL, dns []string, resolvConf string, log *slog.Logger) error {
	names := newFakeDNS()
	names.search = searchDomains(resolvConf)
	names.forward = func(q []byte) ([]byte, error) { return dnsThroughProxy(dial, pu, q) }
	for _, a := range dns {
		if err := names.serve(a, log); err != nil {
			return err
		}
	}
	for _, a := range []string{"127.0.0.1", "[::1]"} {
		ln, err := net.Listen("tcp", a+":"+strconv.Itoa(TransparentPort))
		if err != nil && a == "[::1]" {
			// A Pod or a container can have no IPv6.
			log.Warn("transparent: no IPv6 loopback", "err", err)
			continue
		}
		if err != nil {
			return err
		}
		go transparent(ln, origDst, names, dial, pu, log)
	}
	return nil
}

// writeCA writes the CA certificate (PEM) to dir/ca.crt with mode 0644. An
// empty dir or an empty certificate writes nothing.
func writeCA(dir, caPEM string) error {
	if dir == "" || strings.TrimSpace(caPEM) == "" {
		return nil
	}
	b, _ := pem.Decode([]byte(caPEM))
	if b == nil || b.Type != "CERTIFICATE" {
		return errors.New("$" + EnvCAPEM + " is not a PEM certificate")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	path := filepath.Join(dir, CAFileName)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(caPEM), 0o644); err != nil {
		return err
	}
	if err := os.Chmod(tmp, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// checkCommand connects to a TCP address and exits 0 when it can: the
// startup probe of the Kubernetes sidecar (the local proxy port binds
// loopback only, so a TCP probe of the kubelet cannot reach it).
func checkCommand(args []string) error {
	fs := flag.NewFlagSet("check", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	addr := fs.String("addr", LocalProxyAddr, "TCP address to connect to")
	if err := fs.Parse(args); err != nil {
		return err
	}
	c, err := net.DialTimeout("tcp", *addr, 2*time.Second)
	if err != nil {
		return err
	}
	return c.Close()
}
