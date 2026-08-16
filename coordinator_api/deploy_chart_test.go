package main

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// helmTemplateFile renders a single template from the Helm chart (repo
// helm_chart/, one level up from this module) and returns the YAML. It skips
// the test when helm is not installed (e.g. the no-helm CI test image).
func helmTemplateFile(t *testing.T, templatePath string, extraArgs ...string) string {
	t.Helper()
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("helm not installed; skipping chart-render regression test")
	}
	args := append([]string{"template", "rc", "../helm_chart", "-s", templatePath}, extraArgs...)
	out, err := exec.Command("helm", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("helm template %s failed: %v\n%s", templatePath, err, out)
	}
	return string(out)
}

// TestHelmChartWiresJobAPICredentials guards the deploy-side half of child-job
// submission on the coordinator-mediated k8s path. The coordinator injects
// REACTORCIDE_JOB_API_URL and REACTORCIDE_API_TOKEN into each job container
// (see internal/worker.BuildJobEnv) so eval jobs can submit their build/test/
// release children back to the coordinator API. These were dropped from the
// chart once, which silently stopped all child jobs (e.g. ichoi-release) from
// being created — the eval "succeeded" but spawned nothing. This asserts the
// coordinator's own configmap/deployment carry both, not merely that the
// strings appear somewhere (the web app and worker also reference them).
func TestHelmChartWiresJobAPICredentials(t *testing.T) {
	cm := helmTemplateFile(t, "templates/configmap.yaml")
	if !strings.Contains(cm, "REACTORCIDE_JOB_API_URL") {
		t.Error("coordinator configmap is missing REACTORCIDE_JOB_API_URL — job containers won't know the coordinator API address, so eval jobs can't submit child jobs on k8s")
	}

	appDeploy := helmTemplateFile(t, "templates/deployment-app.yaml")
	if !strings.Contains(appDeploy, "REACTORCIDE_API_TOKEN") {
		t.Error("coordinator deployment is missing REACTORCIDE_API_TOKEN — job containers can't authenticate to submit child jobs on k8s")
	}
}

// TestHelmSecretLookupsAllowEmptyData guards upgrades from a bootstrap
// Secret that exists before the deploy job adds its token. Kubernetes omits
// the data map for that empty Secret. Helm must replace the nil map with an
// empty dictionary before it indexes a key.
func TestHelmSecretLookupsAllowEmptyData(t *testing.T) {
	tests := []struct {
		path          string
		unsafeLookup  string
		guardedLookup string
	}{
		{
			path:          "../helm_chart/templates/secret-api-token.yaml",
			unsafeLookup:  "index $existingSecret.data",
			guardedLookup: "$existingSecret.data | default dict",
		},
		{
			path:          "../helm_chart/templates/secret-worker-enrollment.yaml",
			unsafeLookup:  "index $existing.data",
			guardedLookup: "$existing.data | default dict",
		},
	}

	for _, test := range tests {
		content, err := os.ReadFile(test.path)
		if err != nil {
			t.Fatalf("read %s: %v", test.path, err)
		}
		template := string(content)
		if strings.Contains(template, test.unsafeLookup) {
			t.Errorf("%s indexes a Secret data map before it handles an empty map", test.path)
		}
		if !strings.Contains(template, test.guardedLookup) {
			t.Errorf("%s does not replace an empty Secret data map before key lookup", test.path)
		}
	}
}
