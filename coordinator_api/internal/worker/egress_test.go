package worker

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/docker/docker/api/types/container"
	corev1 "k8s.io/api/core/v1"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

const testCA = "-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----\n"

func testEgressConfig() *EgressConfig {
	return &EgressConfig{
		ProxyHost: "reactorcide-egress.reactorcide.svc", ProxyIP: "10.43.7.9", ProxyPort: 3128,
		Username: "job-1", Password: "cred-abc", CACertPEM: testCA, NetImage: "example/egress:1",
	}
}

func TestEgressJobEnv(t *testing.T) {
	env := testEgressConfig().JobEnv()
	if env["HTTPS_PROXY"] != "http://job-1:cred-abc@127.0.0.1:3128" || env["http_proxy"] != env["HTTPS_PROXY"] {
		t.Fatalf("proxy env %q", env["HTTPS_PROXY"])
	}
	for _, k := range []string{"SSL_CERT_FILE", "NODE_EXTRA_CA_CERTS", "REQUESTS_CA_BUNDLE", "GIT_SSL_CAINFO"} {
		if env[k] != EgressCAFile {
			t.Errorf("%s = %q", k, env[k])
		}
	}
	if !strings.Contains(env["NO_PROXY"], "127.0.0.1") {
		t.Fatal("loopback must not use the proxy")
	}
	cfg := &JobConfig{Env: map[string]string{"HTTPS_PROXY": "http://elsewhere:1", "KEEP": "1"}, Egress: testEgressConfig()}
	out := WithEgressEnv(cfg)
	if out.Env["HTTPS_PROXY"] == "http://elsewhere:1" || out.Env["KEEP"] != "1" {
		t.Fatal("egress variables replace the job values and keep the others")
	}
	if cfg.Env["HTTPS_PROXY"] != "http://elsewhere:1" {
		t.Fatal("WithEgressEnv must not change its input")
	}
	if WithEgressEnv(&JobConfig{}).Egress != nil {
		t.Fatal("no egress: no change")
	}
}

func TestEgressConfigValidate(t *testing.T) {
	if err := testEgressConfig().Validate(); err != nil {
		t.Fatal(err)
	}
	bad := testEgressConfig()
	bad.ProxyIP = "reactorcide-egress"
	if bad.Validate() == nil {
		t.Fatal("a name is not an IP")
	}
	bad = testEgressConfig()
	bad.CACertPEM = ""
	if bad.Validate() == nil {
		t.Fatal("the CA is required")
	}
	if err := CheckEgressCapabilities([]string{CapabilityDocker}, ""); err == nil {
		t.Fatal("docker (privileged) must be refused with egress")
	}
	if err := CheckEgressCapabilities([]string{CapabilityBuilder}, ""); err == nil {
		t.Fatal("builder must be refused with egress")
	}
	for _, u := range []string{"", "runner", "1001", "1234:1234"} {
		if err := CheckEgressCapabilities([]string{CapabilityGPU}, u); err != nil {
			t.Fatalf("%q: %v", u, err)
		}
	}
	for _, u := range []string{"root", "0", "0:0", "0:1001"} {
		if err := CheckEgressCapabilities(nil, u); err == nil || !strings.Contains(err.Error(), "root") {
			t.Fatalf("run_as %q must be refused with egress: %v", u, err)
		}
	}
}

func TestKubernetesEgressPodSpec(t *testing.T) {
	spec := corev1.PodSpec{
		InitContainers: []corev1.Container{{Name: "prepare-workspace"}},
		Containers: []corev1.Container{{Name: "job", Env: []corev1.EnvVar{
			{Name: "HTTPS_PROXY", Value: "http://job-chosen:1"}, {Name: "KEEP", Value: "x"},
		}}},
	}
	cfg := &JobConfig{JobID: "job-1", Egress: testEgressConfig()}
	if err := prepareKubernetesEgress(&spec, cfg); err != nil {
		t.Fatal(err)
	}
	if len(spec.InitContainers) != 3 || spec.InitContainers[0].Name != kubernetesEgressNetInit ||
		spec.InitContainers[1].Name != kubernetesEgressSidecar || spec.InitContainers[2].Name != "prepare-workspace" {
		t.Fatalf("init order: %+v", spec.InitContainers)
	}
	netInit := spec.InitContainers[0]
	sc := netInit.SecurityContext
	if sc == nil || *sc.RunAsUser != 0 || !slices.Equal(sc.Capabilities.Add, []corev1.Capability{"NET_ADMIN"}) ||
		!slices.Equal(sc.Capabilities.Drop, []corev1.Capability{"ALL"}) || sc.Privileged != nil || *sc.AllowPrivilegeEscalation {
		t.Fatalf("egress-net must have NET_ADMIN only: %+v", sc)
	}
	if strings.Join(netInit.Command, " ") != EgressNetBinary+" podnet -exclude 10.43.7.9:3128 -ca-dir "+EgressCADir {
		t.Fatalf("egress-net command %v", netInit.Command)
	}
	if len(netInit.Env) != 1 || netInit.Env[0].Name != "REACTORCIDE_EGRESS_CA_PEM" {
		t.Fatalf("egress-net env: only the CA, got %+v", netInit.Env)
	}
	side := spec.InitContainers[1]
	if side.RestartPolicy == nil || *side.RestartPolicy != corev1.ContainerRestartPolicyAlways {
		t.Fatal("egress-forward must be a native sidecar")
	}
	if side.SecurityContext.Capabilities.Add != nil || !*side.SecurityContext.RunAsNonRoot {
		t.Fatalf("egress-forward has no capability and is not root: %+v", side.SecurityContext)
	}
	if side.StartupProbe == nil || side.StartupProbe.Exec == nil || side.StartupProbe.Exec.Command[1] != "check" {
		t.Fatal("egress-forward needs the startup probe that holds the job until the proxy port answers")
	}
	for _, e := range side.Env {
		if e.Name == "REACTORCIDE_EGRESS_CA_PEM" {
			t.Fatal("the sidecar does not need the CA")
		}
	}
	job := spec.Containers[0]
	env := map[string]string{}
	count := map[string]int{}
	for _, e := range job.Env {
		env[e.Name] = e.Value
		count[e.Name]++
	}
	if count["HTTPS_PROXY"] != 1 || env["HTTPS_PROXY"] != "http://job-1:cred-abc@127.0.0.1:3128" || env["KEEP"] != "x" {
		t.Fatalf("job env %+v", job.Env)
	}
	if env["SSL_CERT_FILE"] != EgressCAFile {
		t.Fatal("job must trust the proxy CA")
	}
	var mounted bool
	for _, m := range job.VolumeMounts {
		if m.Name == kubernetesEgressVolume && m.MountPath == EgressCADir && m.ReadOnly {
			mounted = true
		}
	}
	if !mounted || job.SecurityContext == nil || job.SecurityContext.Capabilities == nil ||
		len(job.SecurityContext.Capabilities.Add) != 0 || !slices.Equal(job.SecurityContext.Capabilities.Drop, []corev1.Capability{"ALL"}) {
		t.Fatal("job gets the CA read-only and drops all capabilities")
	}

	bad := &JobConfig{JobID: "job-1", Egress: testEgressConfig(), Capabilities: []string{CapabilityDocker}}
	err := prepareKubernetesEgress(&corev1.PodSpec{Containers: []corev1.Container{{}}}, bad)
	if class, _ := ClassifyExecutionError(err, 1); err == nil || class != models.FailureClassJob {
		t.Fatalf("docker capability with egress: %v", err)
	}
	none := corev1.PodSpec{Containers: []corev1.Container{{}}}
	if err := prepareKubernetesEgress(&none, &JobConfig{}); err != nil || len(none.InitContainers) != 0 {
		t.Fatal("no egress: no change")
	}
}

func TestDockerEgressSpecs(t *testing.T) {
	cfg := &JobConfig{JobID: "job-1", Egress: testEgressConfig()}
	hc, hh := dockerEgressHelperSpec(cfg)
	if hc.User != "65532:65532" || !slices.Equal(hh.CapDrop, []string{"ALL"}) || len(hh.CapAdd) != 0 ||
		!slices.Equal(hh.DNS, []string{EgressFakeResolverIP}) || string(hh.NetworkMode) != "bridge" {
		t.Fatalf("helper %+v %+v", hc, hh)
	}
	if strings.Join(append(hc.Entrypoint, hc.Cmd...), " ") != EgressNetBinary+" serve -proxy 10.43.7.9:3128 -exclude 10.43.7.9:3128" {
		t.Fatalf("helper command %v %v", hc.Entrypoint, hc.Cmd)
	}
	if !slices.Contains(hc.Env, "REACTORCIDE_EGRESS_PROXY_PASSWORD=cred-abc") || hc.Labels["reactorcide.component"] != egressComponent {
		t.Fatalf("helper env/labels %v %v", hc.Env, hc.Labels)
	}
	sc, sh := dockerEgressSetupSpec(cfg, EgressHelperName("job-1"))
	if sc.User != "0:0" || !slices.Equal(sh.CapAdd, []string{"NET_ADMIN"}) || !slices.Equal(sh.CapDrop, []string{"ALL"}) || sh.Privileged ||
		string(sh.NetworkMode) != "container:reactorcide-egress-job-1" {
		t.Fatalf("setup %+v %+v", sc, sh)
	}
	if sc.Cmd[0] != "podnet" {
		t.Fatalf("setup command %v", sc.Cmd)
	}
	cfg.Egress.Network = "reactorcide_default"
	if _, hh := dockerEgressHelperSpec(cfg); string(hh.NetworkMode) != "reactorcide_default" {
		t.Fatal("the helper uses the configured network")
	}
}

func TestEgressJobContainerHardening(t *testing.T) {
	host := &container.HostConfig{CapAdd: []string{"NET_RAW"}, SecurityOpt: []string{"label=disable"}}
	hardenDockerEgressJob(host)
	if host.Privileged || len(host.CapAdd) != 0 || !slices.Equal(host.CapDrop, []string{"ALL"}) ||
		!slices.Contains(host.SecurityOpt, "no-new-privileges") || !slices.Contains(host.SecurityOpt, "label=disable") {
		t.Fatalf("docker job host config %+v", host)
	}
	if got := strings.Join(nerdctlEgressJobArgs(), " "); got != "--cap-drop ALL --security-opt no-new-privileges" {
		t.Fatalf("nerdctl job args %q", got)
	}
	c := corev1.Container{SecurityContext: &corev1.SecurityContext{RunAsUser: int64Ptr(1001)}}
	hardenKubernetesEgressJob(&c)
	sc := c.SecurityContext
	if *sc.RunAsUser != 1001 || sc.AllowPrivilegeEscalation == nil || *sc.AllowPrivilegeEscalation || sc.Privileged == nil || *sc.Privileged ||
		sc.Capabilities == nil || !slices.Equal(sc.Capabilities.Drop, []corev1.Capability{"ALL"}) || len(sc.Capabilities.Add) != 0 ||
		sc.SeccompProfile == nil || sc.SeccompProfile.Type != corev1.SeccompProfileTypeRuntimeDefault || !*sc.RunAsNonRoot {
		t.Fatalf("kubernetes job security context %+v", sc)
	}
	if _, err := prepareEgressJobConfig(&JobConfig{WorkspaceDir: t.TempDir(), Egress: testEgressConfig(), RunAsUser: "root"}); err == nil {
		t.Fatal("run_as root with egress must be refused")
	}
}

func TestPrepareEgressJobConfigWritesCA(t *testing.T) {
	ws := t.TempDir()
	cfg := &JobConfig{JobID: "job-1", WorkspaceDir: ws, Env: map[string]string{"A": "1"}, ExtraMounts: []string{"/x:/y"}, Egress: testEgressConfig()}
	out, err := prepareEgressJobConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ca := filepath.Join(ws, ".reactorcide-egress", "ca.crt")
	b, err := os.ReadFile(ca)
	if err != nil || string(b) != testCA {
		t.Fatalf("CA file: %v", err)
	}
	if !slices.Contains(out.ExtraMounts, ca+":"+EgressCAFile+":ro") || !slices.Contains(out.ExtraMounts, "/x:/y") || len(cfg.ExtraMounts) != 1 {
		t.Fatalf("mounts %v", out.ExtraMounts)
	}
	if out.Env["SSL_CERT_FILE"] != EgressCAFile || out.Env["A"] != "1" {
		t.Fatal("egress env")
	}
	if same, err := prepareEgressJobConfig(&JobConfig{}); err != nil || same.Egress != nil {
		t.Fatal("no egress: no change")
	}
	if _, err := prepareEgressJobConfig(&JobConfig{WorkspaceDir: ws, Egress: testEgressConfig(), Capabilities: []string{CapabilityBuilder}}); err == nil {
		t.Fatal("builder with egress must be refused")
	}
}

func TestNerdctlEgressArgs(t *testing.T) {
	cfg := &JobConfig{JobID: "job-1", Egress: testEgressConfig()}
	helper := strings.Join(nerdctlEgressHelperArgs(cfg), " ")
	for _, want := range []string{"run -d", "--name reactorcide-egress-job-1", "--net bridge", "--dns " + EgressFakeResolverIP,
		"--user 65532:65532", "--cap-drop ALL", "-e REACTORCIDE_EGRESS_PROXY_USER=job-1", "example/egress:1 serve -proxy 10.43.7.9:3128"} {
		if !strings.Contains(helper, want) {
			t.Errorf("helper args lack %q: %s", want, helper)
		}
	}
	if strings.Contains(helper, "NET_ADMIN") || strings.Contains(helper, "CA_PEM") {
		t.Errorf("helper must have no capability and no CA env: %s", helper)
	}
	setup := strings.Join(nerdctlEgressSetupArgs(cfg), " ")
	for _, want := range []string{"run --rm", "--net container:reactorcide-egress-job-1", "--user 0:0", "--cap-drop ALL", "--cap-add NET_ADMIN",
		"example/egress:1 podnet -exclude 10.43.7.9:3128"} {
		if !strings.Contains(setup, want) {
			t.Errorf("setup args lack %q: %s", want, setup)
		}
	}
	if strings.Contains(setup, "--privileged") {
		t.Fatal("setup must not be privileged")
	}
}

func TestProxyDeliveryGrant(t *testing.T) {
	project := "p1"
	job := &models.Job{JobID: "j1", Name: "deploy", ProjectID: &project, JobFile: "deploy.yaml"}
	grants := []models.SecretGrant{
		{GrantID: "env", SecretPathMatch: models.SecretGrantMatchPrefix, SecretPathPattern: "org/gh", JobNameMatch: models.SecretGrantMatchAny},
		{GrantID: "proxy", SecretPathMatch: models.SecretGrantMatchPrefix, SecretPathPattern: "org/gh", JobNameMatch: models.SecretGrantMatchAny,
			Delivery: models.SecretDeliveryProxy, ProxyHosts: []string{"api.github.com"}},
		{GrantID: "other", SecretPathMatch: models.SecretGrantMatchPrefix, SecretPathPattern: "org/npm", JobNameMatch: models.SecretGrantMatchAny},
	}
	if g := ProxyDeliveryGrant(grants, job, "org/gh"); g == nil || g.GrantID != "proxy" {
		t.Fatalf("a proxy grant wins over an env grant: %+v", g)
	}
	if g := ProxyDeliveryGrant(grants, job, "org/npm"); g != nil {
		t.Fatal("env grant: env delivery")
	}
	if g := ProxyDeliveryGrant(grants, job, "jobs/j1"); g != nil {
		t.Fatal("a job-scoped secret is env")
	}
	other := &models.Job{JobID: "j2", Name: "deploy", ExecutionProfile: "pr"}
	grants[1].ExecutionProfiles = []string{"standard"}
	if g := ProxyDeliveryGrant(grants, other, "org/gh"); g != nil {
		t.Fatal("a proxy grant for another profile does not match")
	}
}
