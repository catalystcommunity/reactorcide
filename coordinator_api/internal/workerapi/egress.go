package workerapi

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/egress"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/refclass"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/worker"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/workerapi/csilapi"
)

// EgressWorkerCharacteristic is the custom worker characteristic of a worker
// with an egress proxy. The coordinator leases a job with egress control
// only to such a worker (an older worker would ignore Lease.egress).
// Operators can also route egress jobs with a queue that requires it.
const EgressWorkerCharacteristic = egress.WorkerCharacteristic

// proxyDelivery issues placeholders for the secrets of proxy-delivered
// grants while a job's secret references are resolved (WP18,
// docs/egress-proxy.md). The real values go only to Lease.egress.grants.
type proxyDelivery struct {
	ctx    context.Context
	store  worker.SecretGrantStore
	job    *models.Job
	loaded bool
	grants []models.SecretGrant
	err    error

	// issued maps grant ID + secret ref to its egress grant, so the same
	// reference used two times gets one placeholder.
	issued map[string]int
	out    []csilapi.EgressGrant
	// envValues are the values that went into the environment, for the
	// masking cache.
	envValues []string
}

func newProxyDelivery(ctx context.Context, s worker.SecretGrantStore, job *models.Job) *proxyDelivery {
	return &proxyDelivery{ctx: ctx, store: s, job: job, issued: map[string]int{}}
}

func (p *proxyDelivery) grantFor(path string) (*models.SecretGrant, error) {
	if p.store == nil || p.job == nil {
		return nil, nil
	}
	if !p.loaded {
		orgID := p.job.OrgID
		if orgID == "" {
			orgID = p.job.UserID
		}
		p.grants, p.err = p.store.ListSecretGrantsForJob(p.ctx, orgID, p.job.ProjectID, p.job.Name)
		p.loaded = true
	}
	if p.err != nil {
		return nil, p.err
	}
	return worker.ProxyDeliveryGrant(p.grants, p.job, path), nil
}

// deliver returns what goes into the job environment for one resolved
// secret: the value itself (env delivery), or a placeholder (proxy
// delivery).
func (p *proxyDelivery) deliver(path, key, value string) (string, error) {
	g, err := p.grantFor(path)
	if err != nil {
		return "", err
	}
	if g == nil {
		p.envValues = append(p.envValues, value)
		return value, nil
	}
	ref := path + ":" + key
	id := g.GrantID + "|" + ref
	if i, ok := p.issued[id]; ok {
		return p.out[i].Placeholder, nil
	}
	if len(g.ProxyHosts) == 0 {
		return "", fmt.Errorf("secret grant %q delivers through the egress proxy but has no proxy_hosts", g.Name)
	}
	if len(value) < egress.MinSecretBytes {
		return "", fmt.Errorf("secret grant %q delivers %s through the egress proxy, but the value is shorter than %d bytes: the proxy scanner cannot find so short a value", g.Name, ref, egress.MinSecretBytes)
	}
	ph, err := egress.NewPlaceholder()
	if err != nil {
		return "", err
	}
	header := strings.ToLower(strings.TrimSpace(g.ProxyHeader))
	if header == "" {
		header = egress.DefaultHeader
	}
	p.issued[id] = len(p.out)
	p.out = append(p.out, csilapi.EgressGrant{
		Id:          g.GrantID,
		SecretRef:   ref,
		Placeholder: ph,
		Value:       value,
		Hosts:       append([]string{}, g.ProxyHosts...),
		Header:      header,
		Format:      g.ProxyHeaderFormat,
	})
	return ph, nil
}

// maskValues returns the values for the masking cache: each real value
// (env and proxy), and each resolved environment value that holds no
// placeholder.
func (p *proxyDelivery) maskValues(resolved []string) []string {
	out := make([]string, 0, len(resolved)+len(p.envValues)+len(p.out))
	for _, v := range resolved {
		if !egress.FindPlaceholders.MatchString(v) {
			out = append(out, v)
		}
	}
	out = append(out, p.envValues...)
	for _, g := range p.out {
		out = append(out, g.Value)
	}
	return out
}

// egressSettingReader reads the egress control setting of a job
// (store.EgressControlStore).
type egressSettingReader interface {
	EffectiveEgressControl(ctx context.Context, orgID string, projectID *string, profile string) (bool, error)
}

var _ egressSettingReader = store.EgressControlStore(nil)

// workerHasEgress reports whether the worker has the egress_proxy
// characteristic with the value true.
func workerHasEgress(chars csilapi.WorkerCharacteristics) bool {
	for _, kv := range chars.Custom {
		if kv.Key != EgressWorkerCharacteristic {
			continue
		}
		return customValueIsTrue(kv.Value)
	}
	return false
}

// buildLeaseEgress decides whether a job runs with egress control and
// builds its Lease.egress. Egress control is on when the organization, the
// project, or the execution profile turns it on, or when the job has a
// proxy-delivered secret (a placeholder works only through the proxy).
// It returns nil when egress control is off. A job that needs egress on a
// worker without an egress proxy is an error: the claim is refused and the
// job fails, so that its secrets and its egress policy are never bypassed.
func (d *Deps) buildLeaseEgress(ctx context.Context, job *models.Job, resolved *resolvedLeaseSecrets, chars csilapi.WorkerCharacteristics, _ map[string]string) (*csilapi.EgressLease, error) {
	on := len(resolved.ProxyGrants) > 0
	if !on {
		// Fail closed: a store that cannot answer the egress setting must
		// not turn egress control off for a job that has it on.
		s, ok := d.Store.(egressSettingReader)
		if !ok {
			return nil, fmt.Errorf("the store cannot read the egress control settings")
		}
		v, err := s.EffectiveEgressControl(ctx, job.OwnershipOrgID(), job.ProjectID, job.ExecutionProfile)
		if err != nil {
			return nil, err
		}
		on = v
	}
	if !on {
		return nil, nil
	}
	if !workerHasEgress(chars) {
		return nil, fmt.Errorf("the job needs egress control, and this worker has no egress proxy (characteristic %s=true)", EgressWorkerCharacteristic)
	}
	// The worker refuses these too; refusing the claim here gives the
	// clear reason before any secret goes to a worker.
	if err := worker.CheckEgressCapabilities(job.Capabilities, job.RunAsUser); err != nil {
		return nil, err
	}
	lease := &csilapi.EgressLease{
		Identity:     d.egressIdentity(ctx, job),
		Destinations: coordinatorDestinations(os.Getenv(JobAPIURLEnv)),
		Grants:       append([]csilapi.EgressGrant{}, resolved.ProxyGrants...),
	}
	return lease, nil
}

func (d *Deps) egressIdentity(ctx context.Context, job *models.Job) csilapi.EgressIdentity {
	id := csilapi.EgressIdentity{
		OrgId:            job.OwnershipOrgID(),
		Node:             job.WorkflowNodeName,
		ActorIdentity:    job.ActorIdentity,
		ExecutionProfile: job.ExecutionProfile,
		CiOrigin:         job.CIOrigin,
		// The ref classes of the job (docs/ref-classes.md), so that proxy
		// policy and flow records can use them, as the secret grants of the
		// lease do (worker.grantRefClassesSatisfied).
		RefClasses: refclass.ForJob(job),
	}
	if job.ProjectID != nil {
		id.ProjectId = *job.ProjectID
	}
	if job.ActorPrincipalID != nil {
		id.ActorPrincipalId = *job.ActorPrincipalID
	}
	if job.WorkflowID != nil && *job.WorkflowID != "" {
		id.WorkflowId = *job.WorkflowID
		if s, ok := d.Store.(interface {
			GetWorkflowInstance(context.Context, string) (*models.WorkflowInstance, error)
		}); ok {
			if wf, err := s.GetWorkflowInstance(ctx, *job.WorkflowID); err == nil && wf != nil {
				id.WorkflowSecurityId = wf.WorkflowSecurityID
			}
		}
	}
	return id
}

// JobAPIURLEnv is the coordinator setting of the API URL that jobs use. It
// is the value of REACTORCIDE_COORDINATOR_URL in the job environment
// (worker.BuildJobEnv).
const JobAPIURLEnv = "REACTORCIDE_JOB_API_URL"

// coordinatorDestinations permits the coordinator API of the job (the job
// token reports results, creates triggers, and gets OIDC tokens there), so
// that a job with egress control can still reach it. raw comes from the
// coordinator configuration (JobAPIURLEnv), never from the job environment:
// a job variable must not add a destination.
func coordinatorDestinations(raw string) []csilapi.EgressDestination {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return []csilapi.EgressDestination{}
	}
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" {
		return []csilapi.EgressDestination{}
	}
	port := u.Port()
	if port == "" {
		if u.Scheme == "http" {
			port = "80"
		} else {
			port = "443"
		}
	}
	n, err := strconv.Atoi(port)
	if err != nil {
		return []csilapi.EgressDestination{}
	}
	host := u.Hostname()
	if ip := net.ParseIP(host); ip != nil {
		host = ip.String()
	}
	return []csilapi.EgressDestination{{Host: strings.ToLower(host), Ports: []int64{int64(n)}}}
}

func customValueIsTrue(v any) bool {
	switch x := v.(type) {
	case bool:
		return x
	case string:
		return strings.EqualFold(strings.TrimSpace(x), "true")
	case []any:
		for _, e := range x {
			if customValueIsTrue(e) {
				return true
			}
		}
	}
	return false
}
