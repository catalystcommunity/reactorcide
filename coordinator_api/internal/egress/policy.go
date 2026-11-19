package egress

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Identity holds the facts about a job that the proxy policy can use and
// that the flow records show. RefClasses are the ref classes of the job
// (docs/ref-classes.md); empty when the job has none.
type Identity struct {
	OrgID              string   `json:"org_id,omitempty"`
	ProjectID          string   `json:"project_id,omitempty"`
	WorkflowID         string   `json:"workflow_id,omitempty"`
	WorkflowSecurityID string   `json:"workflow_security_id,omitempty"`
	Node               string   `json:"node,omitempty"`
	ActorPrincipalID   string   `json:"actor_principal_id,omitempty"`
	ActorIdentity      string   `json:"actor_identity,omitempty"`
	ExecutionProfile   string   `json:"execution_profile,omitempty"`
	CIOrigin           string   `json:"ci_origin,omitempty"`
	RefClasses         []string `json:"ref_classes,omitempty"`
}

// Destination is a host pattern and its ports. Empty Ports means 80 and
// 443.
type Destination struct {
	Host  string `json:"host"`
	Ports []int  `json:"ports,omitempty"`
}

// Grant binds a placeholder to a real secret value and to the hosts and the
// header where the proxy may put the value.
type Grant struct {
	ID          string   `json:"id"`
	SecretRef   string   `json:"secret_ref"`
	Placeholder string   `json:"placeholder"`
	Value       string   `json:"value"`
	Hosts       []string `json:"hosts"`
	Header      string   `json:"header,omitempty"`
	Format      string   `json:"format,omitempty"`
}

// JobPolicy is what the worker registers with the proxy for one job
// (PUT /v1/jobs/{job_id}). It holds real secret values: never log it.
type JobPolicy struct {
	JobID        string        `json:"job_id"`
	Identity     Identity      `json:"identity"`
	Destinations []Destination `json:"destinations"`
	Grants       []Grant       `json:"grants"`
	ExpiresAtMs  int64         `json:"expires_at_ms,omitempty"`
}

// Validate checks the policy with the same rules as the proxy.
func (p *JobPolicy) Validate() error {
	if strings.TrimSpace(p.JobID) == "" {
		return errors.New("egress policy: job_id is required")
	}
	seen := map[string]bool{}
	for _, g := range p.Grants {
		if !IsPlaceholder(g.Placeholder) {
			return fmt.Errorf("egress grant %s: the placeholder is not valid", g.ID)
		}
		if seen[g.Placeholder] {
			return fmt.Errorf("egress grant %s: the placeholder is used two times", g.ID)
		}
		seen[g.Placeholder] = true
		if g.Value == "" {
			return fmt.Errorf("egress grant %s: the secret value is empty", g.ID)
		}
		if len(g.Value) < MinSecretBytes {
			return fmt.Errorf("egress grant %s: the secret value is shorter than %d bytes, which the proxy scanner cannot find", g.ID, MinSecretBytes)
		}
		if len(g.Hosts) == 0 {
			return fmt.Errorf("egress grant %s: at least one host is required", g.ID)
		}
		for _, h := range g.Hosts {
			if err := ValidateHostPattern(h); err != nil {
				return fmt.Errorf("egress grant %s: %w", g.ID, err)
			}
		}
		if err := ValidateHeader(g.Header); err != nil {
			return fmt.Errorf("egress grant %s: %w", g.ID, err)
		}
		if err := ValidateHeaderFormat(g.Format); err != nil {
			return fmt.Errorf("egress grant %s: %w", g.ID, err)
		}
	}
	return nil
}

// Summary is the answer of the proxy when the worker removes a job.
type Summary struct {
	JobID       string   `json:"job_id"`
	Flows       int      `json:"flows"`
	Blocked     int      `json:"blocked"`
	SecretsUsed []string `json:"secrets_used"`
}

// Client calls the proxy control API. The control token authenticates the
// worker to the proxy; it is never logged.
type Client struct {
	BaseURL string
	Token   string
	HTTP    *http.Client
}

// ControlHTTPClient returns the HTTP client of the control API. For an
// https control URL it trusts only the proxy CA (caPEM): the proxy serves
// the control API with a certificate from its CA when
// REACTORCIDE_EGRESS_CONTROL_TLS_NAME is set. The control API carries the
// secret values of the grants.
func ControlHTTPClient(caPEM string) (*http.Client, error) {
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM([]byte(caPEM)) {
		return nil, errors.New("egress control TLS: the CA file has no certificate")
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.Proxy = nil
	tr.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	return &http.Client{Timeout: 15 * time.Second, Transport: tr}, nil
}

func (c *Client) http() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 15 * time.Second}
}

func (c *Client) do(ctx context.Context, method, path string, body any, out any) error {
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.BaseURL, "/")+path, r)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := c.http().Do(req)
	if err != nil {
		return fmt.Errorf("egress proxy %s %s: %w", method, path, err)
	}
	defer res.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode/100 != 2 {
		var e struct {
			Message string `json:"message"`
		}
		msg := strings.TrimSpace(string(data))
		if json.Unmarshal(data, &e) == nil && e.Message != "" {
			msg = e.Message
		}
		return &StatusError{Code: res.StatusCode, Message: msg}
	}
	if out != nil && len(data) > 0 {
		return json.Unmarshal(data, out)
	}
	return nil
}

// StatusError is a refusal of the control API.
type StatusError struct {
	Code    int
	Message string
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("egress proxy: %d %s", e.Code, e.Message)
}

// Register sends the policy of a job to the proxy. It replaces an earlier
// registration of the same job.
func (c *Client) Register(ctx context.Context, p *JobPolicy) error {
	if err := p.Validate(); err != nil {
		return err
	}
	return c.do(ctx, http.MethodPut, "/v1/jobs/"+url.PathEscape(p.JobID), p, nil)
}

// Unregister removes a job from the proxy. Its credential then stops
// working. The answer counts the flows of the job.
func (c *Client) Unregister(ctx context.Context, jobID string) (*Summary, error) {
	var s Summary
	if err := c.do(ctx, http.MethodDelete, "/v1/jobs/"+url.PathEscape(jobID), nil, &s); err != nil {
		return nil, err
	}
	return &s, nil
}
