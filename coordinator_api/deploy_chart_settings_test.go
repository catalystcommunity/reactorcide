package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// newSettingsEnv lists the coordinator variables that WP20 added as chart
// values. The default render must not set any of them, so an upgrade keeps
// the code defaults.
var newSettingsEnv = []string{
	"REACTORCIDE_OIDC_ISSUER_URL",
	"REACTORCIDE_WEBHOOK_RATE_LIMIT_PER_MINUTE",
	"REACTORCIDE_WEBHOOK_RATE_LIMIT_BURST",
	"REACTORCIDE_API_RATE_LIMIT_PER_MINUTE",
	"REACTORCIDE_API_RATE_LIMIT_BURST",
	"REACTORCIDE_OUTBOUND_EVENTS_ENABLED",
	"REACTORCIDE_OUTBOUND_EVENTS_ALLOWED_NETWORKS",
	"REACTORCIDE_OBSERVABILITY_ENABLED",
	"REACTORCIDE_OBSERVABILITY_INTERVAL_SECONDS",
	"REACTORCIDE_OBSERVABILITY_ALERT_INTERVAL_SECONDS",
	"REACTORCIDE_ROLLUP_HOURLY_RETENTION_DAYS",
	"REACTORCIDE_ROLLUP_DAILY_RETENTION_DAYS",
	"REACTORCIDE_ARTIFACTS_ENABLED",
	"REACTORCIDE_ARTIFACT_MAX_FILE_BYTES",
	"REACTORCIDE_ARTIFACT_MAX_JOB_BYTES",
	"REACTORCIDE_ARTIFACT_MAX_FILES",
	"REACTORCIDE_FLAKY_WINDOW_DAYS",
	"REACTORCIDE_PROVENANCE_ENABLED",
	"REACTORCIDE_PROVENANCE_BUILDER_ID",
	"REACTORCIDE_CACHE_SHARED_NAMESPACE",
	"REACTORCIDE_GROUP_VCS_SYNC_INTERVAL",
}

func writeChartValues(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "values.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestHelmChartNewSettingsDefaultToCode guards the upgrade: with default
// values the chart sets none of the new variables, adds no extra env, and
// makes no PrometheusRule.
func TestHelmChartNewSettingsDefaultToCode(t *testing.T) {
	cm := helmTemplateFile(t, "templates/configmap.yaml")
	for _, name := range newSettingsEnv {
		if strings.Contains(cm, name) {
			t.Errorf("default configmap sets %s; it must keep the code default", name)
		}
	}
	if _, err := exec.Command("helm", "template", "rc", "../helm_chart", "-s", "templates/prometheusrule.yaml").CombinedOutput(); err == nil {
		t.Error("the PrometheusRule must not render by default")
	}
}

// TestHelmChartNewSettingsRender checks each first-class value, the number
// format of large values, and that an empty cache namespace still renders
// (it turns the shared builder cache off).
func TestHelmChartNewSettingsRender(t *testing.T) {
	values := writeChartValues(t, `
oidc: {issuerUrl: "https://ci.example.com"}
rateLimits: {webhookPerMinute: 0, webhookBurst: 50, apiPerMinute: 1200, apiBurst: 100}
outboundEvents: {enabled: false, allowedNetworks: "10.0.0.0/8,192.168.1.5"}
observability: {enabled: false, intervalSeconds: 120, alertIntervalSeconds: 600, rollupHourlyRetentionDays: 10, rollupDailyRetentionDays: 100}
artifacts: {enabled: false, maxFileBytes: 268435456, maxJobBytes: 2147483648, maxFiles: 50, flakyWindowDays: 14}
provenance: {enabled: true, builderId: "https://ci.example.com/builder"}
cache: {sharedNamespace: ""}
groupVcsSync: {interval: "30m"}
`)
	cm := helmTemplateFile(t, "templates/configmap.yaml", "-f", values)
	for _, want := range []string{
		`REACTORCIDE_OIDC_ISSUER_URL: "https://ci.example.com"`,
		`REACTORCIDE_WEBHOOK_RATE_LIMIT_PER_MINUTE: "0"`,
		`REACTORCIDE_WEBHOOK_RATE_LIMIT_BURST: "50"`,
		`REACTORCIDE_API_RATE_LIMIT_PER_MINUTE: "1200"`,
		`REACTORCIDE_API_RATE_LIMIT_BURST: "100"`,
		`REACTORCIDE_OUTBOUND_EVENTS_ENABLED: "false"`,
		`REACTORCIDE_OUTBOUND_EVENTS_ALLOWED_NETWORKS: "10.0.0.0/8,192.168.1.5"`,
		`REACTORCIDE_OBSERVABILITY_ENABLED: "false"`,
		`REACTORCIDE_OBSERVABILITY_INTERVAL_SECONDS: "120"`,
		`REACTORCIDE_OBSERVABILITY_ALERT_INTERVAL_SECONDS: "600"`,
		`REACTORCIDE_ROLLUP_HOURLY_RETENTION_DAYS: "10"`,
		`REACTORCIDE_ROLLUP_DAILY_RETENTION_DAYS: "100"`,
		`REACTORCIDE_ARTIFACTS_ENABLED: "false"`,
		`REACTORCIDE_ARTIFACT_MAX_FILE_BYTES: "268435456"`,
		`REACTORCIDE_ARTIFACT_MAX_JOB_BYTES: "2147483648"`,
		`REACTORCIDE_ARTIFACT_MAX_FILES: "50"`,
		`REACTORCIDE_FLAKY_WINDOW_DAYS: "14"`,
		`REACTORCIDE_PROVENANCE_ENABLED: "true"`,
		`REACTORCIDE_PROVENANCE_BUILDER_ID: "https://ci.example.com/builder"`,
		`REACTORCIDE_CACHE_SHARED_NAMESPACE: ""`,
		`REACTORCIDE_GROUP_VCS_SYNC_INTERVAL: "30m"`,
	} {
		if !strings.Contains(cm, want) {
			t.Errorf("configmap lacks %s", want)
		}
	}
	if strings.Contains(cm, "e+") {
		t.Errorf("a number rendered in exponent form:\n%s", cm)
	}

	named := helmTemplateFile(t, "templates/configmap.yaml", "--set", "cache.sharedNamespace=builds")
	if !strings.Contains(named, `REACTORCIDE_CACHE_SHARED_NAMESPACE: "builds"`) {
		t.Error("cache.sharedNamespace=builds does not render")
	}
}

// TestHelmChartExtraEnv checks app.extraEnv and worker.extraEnv with value
// and valueFrom items, and that each list goes only to its own container.
func TestHelmChartExtraEnv(t *testing.T) {
	values := writeChartValues(t, `
app:
  extraEnv:
    - {name: APP_ONLY_SETTING, value: "on"}
    - name: APP_SECRET_SETTING
      valueFrom: {secretKeyRef: {name: app-extra, key: token}}
worker:
  extraEnv:
    - {name: REACTORCIDE_WORKER_REGION, value: us-west-2}
    - name: WORKER_SECRET_SETTING
      valueFrom: {secretKeyRef: {name: worker-extra, key: value}}
`)
	app := helmTemplateFile(t, "templates/deployment-app.yaml", "-f", values)
	worker := helmTemplateFile(t, "templates/deployment-worker.yaml", "-f", values)
	for _, want := range []string{"name: APP_ONLY_SETTING", `value: "on"`, "name: APP_SECRET_SETTING", "name: app-extra"} {
		if !strings.Contains(app, want) {
			t.Errorf("coordinator deployment lacks %q", want)
		}
	}
	for _, want := range []string{"name: REACTORCIDE_WORKER_REGION", "value: us-west-2", "name: WORKER_SECRET_SETTING", "name: worker-extra"} {
		if !strings.Contains(worker, want) {
			t.Errorf("worker deployment lacks %q", want)
		}
	}
	if strings.Contains(app, "REACTORCIDE_WORKER_REGION") || strings.Contains(worker, "APP_ONLY_SETTING") {
		t.Error("extraEnv of one deployment leaked into the other")
	}
	// The items must stay in the env list of the container (indent 12).
	if !strings.Contains(app, "\n            - name: APP_ONLY_SETTING\n") {
		t.Errorf("app.extraEnv is not in the container env list:\n%s", app)
	}
	if !strings.Contains(worker, "\n            - name: REACTORCIDE_WORKER_REGION\n") {
		t.Errorf("worker.extraEnv is not in the container env list:\n%s", worker)
	}
}

// TestHelmChartPrometheusRule checks the optional PrometheusRule and that the
// chart copy of the rules is the same as deployment/prometheus.
func TestHelmChartPrometheusRule(t *testing.T) {
	source, err := os.ReadFile("../deployment/prometheus/reactorcide-ci-owner-alerts.yaml")
	if err != nil {
		t.Fatal(err)
	}
	chartCopy, err := os.ReadFile("../helm_chart/files/reactorcide-ci-owner-alerts.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(source, chartCopy) {
		t.Error("helm_chart/files/reactorcide-ci-owner-alerts.yaml differs from deployment/prometheus/reactorcide-ci-owner-alerts.yaml; copy the file again")
	}

	rule := helmTemplateFile(t, "templates/prometheusrule.yaml",
		"--set", "metrics.prometheusRule.enabled=true",
		"--set", "metrics.prometheusRule.labels.release=kube-prometheus")
	for _, want := range []string{
		"kind: PrometheusRule",
		"release: kube-prometheus",
		"ReactorcideInfraFailureRateHigh",
		"{{ $labels.lane }}",
	} {
		if !strings.Contains(rule, want) {
			t.Errorf("PrometheusRule lacks %q", want)
		}
	}
}

// TestHelmChartCorndogs081 checks the vendored Corndogs chart: server 0.8.1,
// resilience policies in compatibility mode, and the readiness probe on the
// ops port (8080), not on the CSIL-RPC port.
func TestHelmChartCorndogs081(t *testing.T) {
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("helm not installed; skipping chart-render regression test")
	}
	out, err := exec.Command("helm", "template", "rc", "../helm_chart", "--set", "corndogs.enabled=true").CombinedOutput()
	if err != nil {
		t.Fatalf("helm template: %v\n%s", err, out)
	}
	render := string(out)
	for _, want := range []string{
		"corndogs:0.8.1",
		"helm.sh/chart: corndogs-0.6.1",
		"name: CORNDOGS_SUBMISSION_KEY_POLICY\n              value: \"compatibility\"",
		"name: CORNDOGS_TASK_GUARD_POLICY\n              value: \"compatibility\"",
		"containerPort: 8080",
		"port: corndogsops",
	} {
		if !strings.Contains(render, want) {
			t.Errorf("corndogs render lacks %q", want)
		}
	}
}
