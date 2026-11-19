package main

import (
	"os/exec"
	"strings"
	"testing"
)

// TestHelmChartEgressProxy guards the egress proxy wiring (WP18,
// docs/egress-proxy.md): off by default, and when on, the proxy, its
// Service, and the worker settings render.
func TestHelmChartEgressProxy(t *testing.T) {
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("helm not installed; skipping chart-render regression test")
	}
	out, err := exec.Command("helm", "template", "rc", "../helm_chart").CombinedOutput()
	if err != nil {
		t.Fatalf("helm template: %v\n%s", err, out)
	}
	if strings.Contains(string(out), "REACTORCIDE_EGRESS") || strings.Contains(string(out), "reactorcide-egress") {
		t.Fatal("the egress proxy must be off by default")
	}

	on := []string{"--set", "egressProxy.enabled=true", "--set", "egressProxy.secretName=egress-keys", "--set", "egressProxy.networkPolicy.enabled=true"}
	proxy := helmTemplateFile(t, "templates/deployment-egress.yaml", on...)
	for _, want := range []string{"kind: Deployment", "kind: Service", "kind: NetworkPolicy", "REACTORCIDE_EGRESS_CONTROL_TOKEN_FILE", "reactorcide.io/egress: proxy", "secretName: egress-keys"} {
		if !strings.Contains(proxy, want) {
			t.Errorf("egress proxy render lacks %q", want)
		}
	}
	worker := helmTemplateFile(t, "templates/deployment-worker.yaml", on...)
	for _, want := range []string{"REACTORCIDE_EGRESS_CONTROL_URL", "REACTORCIDE_EGRESS_PROXY_ADDR", "REACTORCIDE_EGRESS_JOB_KEY_FILE", "REACTORCIDE_EGRESS_CA_FILE"} {
		if !strings.Contains(worker, want) {
			t.Errorf("worker render lacks %q", want)
		}
	}
	if strings.Contains(worker, "ca.key") {
		t.Error("the worker must not mount the CA private key")
	}

	missing, err := exec.Command("helm", "template", "rc", "../helm_chart", "--set", "egressProxy.enabled=true").CombinedOutput()
	if err == nil || !strings.Contains(string(missing), "egressProxy.secretName is required") {
		t.Fatalf("enabled without a Secret must fail: %v %s", err, missing)
	}
}
