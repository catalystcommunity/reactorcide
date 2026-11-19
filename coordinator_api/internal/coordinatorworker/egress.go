package coordinatorworker

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/catalystcommunity/app-utils-go/logging"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/characteristics"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/egress"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/secrets"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/worker"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/workerclient/csilapi"
)

// Environment of the worker egress proxy settings (docs/egress-proxy.md).
const (
	EnvEgressControlURL   = "REACTORCIDE_EGRESS_CONTROL_URL"
	EnvEgressProxyAddr    = "REACTORCIDE_EGRESS_PROXY_ADDR"
	EnvEgressControlToken = "REACTORCIDE_EGRESS_CONTROL_TOKEN_FILE"
	EnvEgressJobKey       = "REACTORCIDE_EGRESS_JOB_KEY_FILE"
	EnvEgressCAFile       = "REACTORCIDE_EGRESS_CA_FILE"
	EnvEgressNetImage     = "REACTORCIDE_EGRESS_NET_IMAGE"
	EnvEgressNetwork      = "REACTORCIDE_EGRESS_NETWORK"
)

// EgressSettings connect a worker to its egress proxy. The worker
// registers the policy of each lease with egress over the control API, and
// gives the job a credential that only works while the job is registered.
// ControlToken and JobKey are secrets: they are never logged.
type EgressSettings struct {
	// ControlURL is the proxy control API, for example
	// "http://reactorcide-egress:8080".
	ControlURL string
	// ProxyAddr is the proxy port as the job network reaches it
	// ("host:port"). The worker resolves the host for the redirect.
	ProxyAddr    string
	ControlToken string
	JobKey       []byte
	CACertPEM    string
	NetImage     string
	Network      string

	// lookup resolves ProxyAddr's host. nil uses net.DefaultResolver.
	lookup func(ctx context.Context, host string) ([]string, error)
	// controlHTTP is the control API client. For an https ControlURL it
	// trusts only the proxy CA. nil: the default client (http).
	controlHTTP *http.Client
}

// LoadEgressSettings reads the settings from the environment. It returns
// nil with no error when REACTORCIDE_EGRESS_CONTROL_URL is not set (no
// egress proxy on this worker).
func LoadEgressSettings() (*EgressSettings, error) {
	controlURL := strings.TrimSpace(os.Getenv(EnvEgressControlURL))
	if controlURL == "" {
		return nil, nil
	}
	s := &EgressSettings{
		ControlURL: controlURL,
		ProxyAddr:  strings.TrimSpace(os.Getenv(EnvEgressProxyAddr)),
		NetImage:   strings.TrimSpace(os.Getenv(EnvEgressNetImage)),
		Network:    strings.TrimSpace(os.Getenv(EnvEgressNetwork)),
	}
	if s.NetImage == "" {
		s.NetImage = worker.DefaultEgressNetImage
	}
	need := func(env string) (string, error) {
		v := strings.TrimSpace(os.Getenv(env))
		if v == "" {
			return "", fmt.Errorf("%s is required when %s is set", env, EnvEgressControlURL)
		}
		return v, nil
	}
	tokenFile, err := need(EnvEgressControlToken)
	if err != nil {
		return nil, err
	}
	token, err := egress.ReadSecretFile(tokenFile)
	if err != nil {
		return nil, err
	}
	s.ControlToken = string(token)
	keyFile, err := need(EnvEgressJobKey)
	if err != nil {
		return nil, err
	}
	if s.JobKey, err = egress.ReadSecretFile(keyFile); err != nil {
		return nil, err
	}
	caFile, err := need(EnvEgressCAFile)
	if err != nil {
		return nil, err
	}
	ca, err := os.ReadFile(caFile)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", caFile, err)
	}
	s.CACertPEM = string(ca)
	if s.ProxyAddr == "" {
		return nil, fmt.Errorf("%s is required when %s is set", EnvEgressProxyAddr, EnvEgressControlURL)
	}
	if err := s.validate(); err != nil {
		return nil, err
	}
	if strings.HasPrefix(strings.ToLower(s.ControlURL), "https://") {
		if s.controlHTTP, err = egress.ControlHTTPClient(s.CACertPEM); err != nil {
			return nil, err
		}
	} else {
		logging.Log.WithField("control_url", s.ControlURL).Warn("the egress control API is plain HTTP; it carries secret values, so restrict its port to the workers or use https (REACTORCIDE_EGRESS_CONTROL_TLS_NAME on the proxy)")
	}
	return s, nil
}

func (s *EgressSettings) validate() error {
	host, port, err := net.SplitHostPort(s.ProxyAddr)
	if err != nil || host == "" {
		return fmt.Errorf("%s %q: expected host:port", EnvEgressProxyAddr, s.ProxyAddr)
	}
	if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
		return fmt.Errorf("%s %q: the port is not valid", EnvEgressProxyAddr, s.ProxyAddr)
	}
	if !strings.Contains(s.CACertPEM, "BEGIN CERTIFICATE") {
		return errors.New("the egress CA file has no PEM certificate")
	}
	if len(s.JobKey) < egress.MinKeyBytes || len(s.ControlToken) < egress.MinKeyBytes {
		return errors.New("the egress job key and control token must have at least 32 bytes")
	}
	return nil
}

// resolveProxy returns the proxy IP and port. The job network has a fake
// DNS, so the helper dials the IP.
func (s *EgressSettings) resolveProxy(ctx context.Context) (string, int, error) {
	host, portText, err := net.SplitHostPort(s.ProxyAddr)
	if err != nil {
		return "", 0, err
	}
	port, _ := strconv.Atoi(portText)
	if ip := net.ParseIP(host); ip != nil {
		return ip.String(), port, nil
	}
	lookup := s.lookup
	if lookup == nil {
		lookup = net.DefaultResolver.LookupHost
	}
	addrs, err := lookup(ctx, host)
	if err != nil || len(addrs) == 0 {
		return "", 0, fmt.Errorf("resolve egress proxy %s: %v", host, err)
	}
	for _, a := range addrs {
		if ip := net.ParseIP(a); ip != nil && ip.To4() != nil {
			return ip.String(), port, nil
		}
	}
	return addrs[0], port, nil
}

// applyEgressCharacteristic loads the egress settings (when cfg has none)
// and, when the worker has an egress proxy, adds the characteristic
// egress_proxy=true: the coordinator leases a job with egress control only
// to such a worker.
func applyEgressCharacteristic(cfg Config) (Config, error) {
	if cfg.Egress == nil {
		s, err := LoadEgressSettings()
		if err != nil {
			return cfg, fmt.Errorf("coordinatorworker: egress proxy: %w", err)
		}
		cfg.Egress = s
	}
	if cfg.Egress == nil {
		return cfg, nil
	}
	for _, kv := range cfg.Custom {
		if kv.Key == egress.WorkerCharacteristic {
			return cfg, nil
		}
	}
	cfg.Custom = append(append([]characteristics.KV(nil), cfg.Custom...),
		characteristics.KV{Key: egress.WorkerCharacteristic, Value: true})
	logging.Log.WithField("control_url", cfg.Egress.ControlURL).Info("coordinatorworker: egress proxy configured")
	return cfg, nil
}

// egressPolicy builds the proxy registration of a lease.
func egressPolicy(lease csilapi.Lease, now time.Time) *egress.JobPolicy {
	e := lease.Egress
	id := e.Identity
	p := &egress.JobPolicy{
		JobID: lease.JobId,
		Identity: egress.Identity{
			OrgID: id.OrgId, ProjectID: id.ProjectId, WorkflowID: id.WorkflowId,
			WorkflowSecurityID: id.WorkflowSecurityId, Node: id.Node,
			ActorPrincipalID: id.ActorPrincipalId, ActorIdentity: id.ActorIdentity,
			ExecutionProfile: id.ExecutionProfile, CIOrigin: id.CiOrigin,
			RefClasses: append([]string{}, id.RefClasses...),
		},
		Destinations: []egress.Destination{},
		Grants:       []egress.Grant{},
	}
	for _, d := range e.Destinations {
		dst := egress.Destination{Host: d.Host}
		for _, port := range d.Ports {
			dst.Ports = append(dst.Ports, int(port))
		}
		p.Destinations = append(p.Destinations, dst)
	}
	for _, g := range e.Grants {
		p.Grants = append(p.Grants, egress.Grant{
			ID: g.Id, SecretRef: g.SecretRef, Placeholder: g.Placeholder, Value: g.Value,
			Hosts: append([]string{}, g.Hosts...), Header: g.Header, Format: g.Format,
		})
	}
	// The registration ends by itself after the job timeout and one hour,
	// or after one day without a timeout, when the worker cannot remove it.
	ttl := 24 * time.Hour
	if lease.TimeoutSeconds > 0 {
		ttl = time.Duration(lease.TimeoutSeconds)*time.Second + time.Hour
	}
	p.ExpiresAtMs = now.Add(ttl).UnixMilli()
	return p
}

// startLeaseEgress registers the egress policy of a lease with the proxy
// and returns the runner egress config. The returned release removes the
// registration (the job credential then stops working); call it after the
// job ends. The grant values and the credential go to the masker.
func startLeaseEgress(ctx context.Context, s *EgressSettings, lease csilapi.Lease, masker *secrets.Masker) (*worker.EgressConfig, func(), error) {
	if s == nil {
		return nil, nil, errors.New("the lease needs egress control, and this worker has no egress proxy")
	}
	for _, g := range lease.Egress.Grants {
		masker.RegisterSecret(g.Value)
	}
	ip, port, err := s.resolveProxy(ctx)
	if err != nil {
		return nil, nil, err
	}
	host, _, _ := net.SplitHostPort(s.ProxyAddr)
	cfg := &worker.EgressConfig{
		ProxyHost: host,
		ProxyIP:   ip,
		ProxyPort: port,
		Username:  lease.JobId,
		Password:  egress.Credential(s.JobKey, lease.JobId),
		CACertPEM: s.CACertPEM,
		NetImage:  s.NetImage,
		Network:   s.Network,
	}
	masker.RegisterSecret(cfg.Password)
	client := &egress.Client{BaseURL: s.ControlURL, Token: s.ControlToken, HTTP: s.controlHTTP}
	regCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if err := client.Register(regCtx, egressPolicy(lease, time.Now())); err != nil {
		return nil, nil, fmt.Errorf("register the job with the egress proxy: %w", err)
	}
	release := func() {
		rctx, rcancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer rcancel()
		sum, err := client.Unregister(rctx, lease.JobId)
		logger := logging.Log.WithFields(map[string]interface{}{"lease_id": lease.LeaseId, "job_id": lease.JobId})
		if err != nil {
			logger.WithError(err).Warn("failed to remove the job from the egress proxy; the registration ends at its expiry")
			return
		}
		logger.WithFields(map[string]interface{}{
			"egress_flows":   sum.Flows,
			"egress_blocked": sum.Blocked,
			"grants_used":    strings.Join(sum.SecretsUsed, ","),
		}).Info("egress proxy flows of the job")
	}
	return cfg, release, nil
}
