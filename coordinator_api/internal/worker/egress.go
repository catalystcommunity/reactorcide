package worker

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
)

// EgressConfig is the egress proxy setup of one job (docs/egress-proxy.md).
// The worker fills it from its egress settings and the lease; the runner
// adds the network plumbing:
//
//   - Kubernetes: an init container with NET_ADMIN only (reactorcide-egress-net
//     podnet) installs the redirect, and a native sidecar (serve) holds the
//     transparent route, the fake DNS, and the local proxy port.
//   - Docker and containerd: a helper container (serve) owns the network
//     namespace; a short setup container with NET_ADMIN only (podnet) installs
//     the redirect in it; the job container joins that namespace.
//   - VM: the host forwarder sends each guest flow to the proxy with the job
//     credential (vmrunner.EgressProxyKeyFileEnv).
//
// Password is the per-job proxy credential. It is a secret of the job: the
// worker masks it.
type EgressConfig struct {
	// ProxyHost and ProxyPort are the proxy address that the job network
	// reaches. ProxyIP is ProxyHost resolved: the redirect excludes it, and
	// the helper dials it (the job DNS is fake, so the helper cannot
	// resolve names).
	ProxyHost string
	ProxyIP   string
	ProxyPort int

	// Username is the job ID; Password is egress.Credential of it.
	Username string
	Password string

	// CACertPEM is the proxy CA certificate. The job trusts it.
	CACertPEM string

	// NetImage is the image with /usr/local/bin/reactorcide-egress-net.
	NetImage string

	// Network is the Docker or nerdctl network of the helper container,
	// from which the proxy is reachable. Empty means "bridge".
	Network string
}

// Paths and ports of the egress plumbing. They match egress_proxy/netd.
const (
	EgressLocalProxyAddr = "127.0.0.1:3128"
	EgressCADir          = "/etc/reactorcide/egress"
	EgressCAFile         = EgressCADir + "/ca.crt"
	EgressNetBinary      = "/usr/local/bin/reactorcide-egress-net"
	// EgressFakeResolverIP is the DNS server of a Docker or containerd job
	// with egress. Any address that is not loopback works: the redirect
	// sends port 53 to the fake DNS of the helper. 192.0.2.53 is in
	// TEST-NET-1 (RFC 5737), so it never reaches a real server.
	EgressFakeResolverIP = "192.0.2.53"
	// DefaultEgressNetImage is the image when the worker sets none.
	DefaultEgressNetImage = "containers.catalystsquad.com/public/reactorcide/egress:dev"
)

// Validate checks that the config is complete.
func (c *EgressConfig) Validate() error {
	if c == nil {
		return errors.New("egress: no config")
	}
	if c.ProxyPort < 1 || c.ProxyPort > 65535 {
		return fmt.Errorf("egress: proxy port %d is not valid", c.ProxyPort)
	}
	if _, err := netip.ParseAddr(c.ProxyIP); err != nil {
		return fmt.Errorf("egress: proxy IP %q is not valid", c.ProxyIP)
	}
	if c.Username == "" || c.Password == "" {
		return errors.New("egress: the job credential is missing")
	}
	if !strings.Contains(c.CACertPEM, "BEGIN CERTIFICATE") {
		return errors.New("egress: the proxy CA certificate is missing")
	}
	if c.NetImage == "" {
		return errors.New("egress: the network helper image is missing")
	}
	return nil
}

// ProxyAddr is the proxy IP and port, as the helper dials it.
func (c *EgressConfig) ProxyAddr() string {
	return net.JoinHostPort(c.ProxyIP, strconv.Itoa(c.ProxyPort))
}

// LocalProxyURL is the proxy URL that the job uses: the local port of the
// helper, with the job credential.
func (c *EgressConfig) LocalProxyURL() string {
	u := url.URL{Scheme: "http", User: url.UserPassword(c.Username, c.Password), Host: EgressLocalProxyAddr}
	return u.String()
}

// JobEnv returns the environment of a job container with egress: the proxy
// variables for tools that read them (the redirect catches the others),
// and the CA variables of common tools. These values replace the same
// names in the job environment.
func (c *EgressConfig) JobEnv() map[string]string {
	proxy := c.LocalProxyURL()
	noProxy := "localhost,127.0.0.1,::1"
	env := map[string]string{
		"HTTP_PROXY":  proxy,
		"HTTPS_PROXY": proxy,
		"http_proxy":  proxy,
		"https_proxy": proxy,
		"NO_PROXY":    noProxy,
		"no_proxy":    noProxy,

		"REACTORCIDE_EGRESS":         "proxy",
		"REACTORCIDE_EGRESS_CA_FILE": EgressCAFile,
		"SSL_CERT_FILE":              EgressCAFile,
		"NODE_EXTRA_CA_CERTS":        EgressCAFile,
		"REQUESTS_CA_BUNDLE":         EgressCAFile,
		"CURL_CA_BUNDLE":             EgressCAFile,
		"GIT_SSL_CAINFO":             EgressCAFile,
		"PIP_CERT":                   EgressCAFile,
		"CARGO_HTTP_CAINFO":          EgressCAFile,
		"AWS_CA_BUNDLE":              EgressCAFile,
		"DENO_CERT":                  EgressCAFile,
		"BUNDLE_SSL_CA_CERT":         EgressCAFile,
	}
	return env
}

// HelperEnv is the environment of the helper (serve) and the setup
// container (podnet): the credential that the transparent route sends, and
// the CA that podnet writes.
func (c *EgressConfig) HelperEnv() map[string]string {
	return map[string]string{
		"REACTORCIDE_EGRESS_PROXY_USER":     c.Username,
		"REACTORCIDE_EGRESS_PROXY_PASSWORD": c.Password,
		"REACTORCIDE_EGRESS_CA_PEM":         c.CACertPEM,
	}
}

// ServeArgs is the command of the helper: the local proxy port, the
// transparent route, and the fake DNS.
func (c *EgressConfig) ServeArgs() []string {
	return []string{EgressNetBinary, "serve", "-proxy", c.ProxyAddr(), "-exclude", c.ProxyAddr()}
}

// PodnetArgs is the command of the setup container: the redirect, and the
// CA file in caDir ("" writes no file).
func (c *EgressConfig) PodnetArgs(caDir string) []string {
	args := []string{EgressNetBinary, "podnet", "-exclude", c.ProxyAddr()}
	if caDir != "" {
		args = append(args, "-ca-dir", caDir)
	}
	return args
}

// CheckEgressCapabilities refuses the capabilities that egress control
// does not support yet. A privileged job (docker) could remove the
// redirect. The builder sidecar shares the network of the job but does not
// trust the proxy CA, so its pulls would fail.
//
// It also refuses a job that runs as root (uid 0). Root in the job
// container, or a capability such as CAP_NET_RAW, can send packets that
// nftables never sees (an AF_PACKET socket), so they would not go through
// the proxy. The runners drop all capabilities of an egress job container
// and set no-new-privileges (sudo does not work), so a non-root job
// cannot get them back.
func CheckEgressCapabilities(capabilities []string, runAsUser string) error {
	for _, c := range capabilities {
		switch c {
		case CapabilityDocker:
			return errors.New("egress control does not permit the docker capability: a privileged job can remove the redirect")
		case CapabilityBuilder:
			return errors.New("egress control does not support the builder capability yet")
		}
	}
	user, err := DefaultRunAsUser(runAsUser)
	if err != nil {
		return err
	}
	if uid, _, _ := strings.Cut(user, ":"); uid == "0" {
		return errors.New("egress control does not permit run_as root: root can send raw packets past the egress proxy")
	}
	return nil
}

// WithEgressEnv returns a copy of config whose environment has the egress
// variables (they replace the same names). Without egress it returns
// config.
func WithEgressEnv(config *JobConfig) *JobConfig {
	if config == nil || config.Egress == nil {
		return config
	}
	out := *config
	out.Env = make(map[string]string, len(config.Env)+16)
	for k, v := range config.Env {
		out.Env[k] = v
	}
	for k, v := range config.Egress.JobEnv() {
		out.Env[k] = v
	}
	return &out
}
