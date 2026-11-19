package vmrunner

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/egress"
)

// EgressProxyKeyFileEnv names the job key file of the egress proxy (WP18,
// docs/egress-proxy.md). With it, ProxyEgressDialer sends the proxy
// credential of the job that owns each flow (Proxy-Authorization: Basic
// JOB:CREDENTIAL). The guest never sees the credential. When it is not set,
// the worker job key (REACTORCIDE_EGRESS_JOB_KEY_FILE) is used.
const EgressProxyKeyFileEnv = "REACTORCIDE_VM_EGRESS_PROXY_KEY_FILE"

// workerJobKeyFileEnv is the job key file of the worker egress settings.
const workerJobKeyFileEnv = "REACTORCIDE_EGRESS_JOB_KEY_FILE"

func egressKeyFile() string {
	if p := strings.TrimSpace(os.Getenv(EgressProxyKeyFileEnv)); p != "" {
		return p
	}
	return strings.TrimSpace(os.Getenv(workerJobKeyFileEnv))
}

// JobCredentialHeader returns the CONNECT header hook that authenticates
// each flow as its job.
func JobCredentialHeader(key []byte) func(EgressFlow) http.Header {
	return func(flow EgressFlow) http.Header {
		if flow.JobID == "" {
			return nil
		}
		raw := flow.JobID + ":" + egress.Credential(key, flow.JobID)
		return http.Header{"Proxy-Authorization": {"Basic " + base64.StdEncoding.EncodeToString([]byte(raw))}}
	}
}

// proxyHeaderFromEnv returns the header hook from the key file, or nil when
// no key file is set.
func proxyHeaderFromEnv() (func(EgressFlow) http.Header, error) {
	path := egressKeyFile()
	if path == "" {
		return nil, nil
	}
	key, err := egress.ReadSecretFile(path)
	if err != nil {
		return nil, fmt.Errorf("egress proxy key: %w", err)
	}
	return JobCredentialHeader(key), nil
}

// EgressProxyConfigured reports whether guest egress goes to the egress
// proxy with job credentials. A job with egress control must not run on a
// VM backend without it.
func EgressProxyConfigured() error {
	if strings.TrimSpace(os.Getenv(EgressProxyEnv)) == "" {
		return errors.New("egress control needs " + EgressProxyEnv + " on a VM worker")
	}
	if egressKeyFile() == "" {
		return errors.New("egress control needs " + EgressProxyKeyFileEnv + " or " + workerJobKeyFileEnv + " on a VM worker")
	}
	return nil
}
