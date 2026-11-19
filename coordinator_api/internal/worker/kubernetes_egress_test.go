package worker

import (
	"context"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestKubernetesRunnerSpawnsJobWithEgress(t *testing.T) {
	clientset := fake.NewSimpleClientset()
	runner := &KubernetesRunner{clientset: clientset, namespace: "reactorcide", serviceAccount: "default", dindImage: "docker:27-dind"}
	if _, err := runner.SpawnJob(context.Background(), &JobConfig{
		JobID: "egress-job", Image: "reactorcide/runnerbase:test", Command: []string{"sh", "-c", "curl https://api.github.com"},
		Env: map[string]string{"TOKEN": "reactorcide-secret-0123"}, WorkingDir: "/job", Egress: testEgressConfig(),
		VCSAuth: &VCSAuthConfig{ContainerDir: "/job/.auth", GitConfig: "x", Credentials: "y"},
	}); err != nil {
		t.Fatalf("SpawnJob: %v", err)
	}
	jobs, _ := clientset.BatchV1().Jobs("reactorcide").List(context.Background(), metav1.ListOptions{})
	if len(jobs.Items) != 1 {
		t.Fatalf("%d jobs", len(jobs.Items))
	}
	tmpl := jobs.Items[0].Spec.Template
	if tmpl.Labels[EgressPodLabel] != "proxy" {
		t.Fatal("the pod needs the egress label for the NetworkPolicy")
	}
	ic := tmpl.Spec.InitContainers
	if len(ic) < 3 || ic[0].Name != kubernetesEgressNetInit || ic[1].Name != kubernetesEgressSidecar {
		t.Fatalf("egress containers must come first: %v", ic)
	}
	sc := tmpl.Spec.Containers[0].SecurityContext
	if sc == nil || sc.AllowPrivilegeEscalation == nil || *sc.AllowPrivilegeEscalation || sc.Capabilities == nil ||
		len(sc.Capabilities.Drop) != 1 || sc.Capabilities.Drop[0] != "ALL" || sc.SeccompProfile == nil {
		t.Fatalf("the egress job container must drop all capabilities and privilege escalation: %+v", sc)
	}
	if sc.RunAsUser == nil || *sc.RunAsUser != 1001 {
		t.Fatalf("the run-as user stays: %+v", sc.RunAsUser)
	}
}

func TestKubernetesRunnerNoEgressKeepsSecurityContext(t *testing.T) {
	clientset := fake.NewSimpleClientset()
	runner := &KubernetesRunner{clientset: clientset, namespace: "reactorcide", serviceAccount: "default", dindImage: "docker:27-dind"}
	if _, err := runner.SpawnJob(context.Background(), &JobConfig{
		JobID: "plain-job", Image: "reactorcide/runnerbase:test", Command: []string{"true"}, WorkingDir: "/job",
	}); err != nil {
		t.Fatalf("SpawnJob: %v", err)
	}
	jobs, _ := clientset.BatchV1().Jobs("reactorcide").List(context.Background(), metav1.ListOptions{})
	sc := jobs.Items[0].Spec.Template.Spec.Containers[0].SecurityContext
	if sc != nil && (sc.Capabilities != nil || sc.AllowPrivilegeEscalation != nil) {
		t.Fatalf("a job without egress must not change: %+v", sc)
	}
}

func TestKubernetesRunnerRefusesRootEgress(t *testing.T) {
	clientset := fake.NewSimpleClientset()
	runner := &KubernetesRunner{clientset: clientset, namespace: "reactorcide", serviceAccount: "default", dindImage: "docker:27-dind"}
	_, err := runner.SpawnJob(context.Background(), &JobConfig{
		JobID: "egress-root", Image: "x", Command: []string{"true"}, RunAsUser: "root", Egress: testEgressConfig(),
	})
	if err == nil || !strings.Contains(err.Error(), "root") {
		t.Fatalf("run_as root with egress must be refused: %v", err)
	}
}

func TestKubernetesRunnerRefusesPrivilegedEgressBeforeSecrets(t *testing.T) {
	clientset := fake.NewSimpleClientset()
	runner := &KubernetesRunner{clientset: clientset, namespace: "reactorcide", serviceAccount: "default", dindImage: "docker:27-dind"}
	_, err := runner.SpawnJob(context.Background(), &JobConfig{
		JobID: "egress-dind", Image: "x", Command: []string{"true"}, Capabilities: []string{CapabilityDocker}, Egress: testEgressConfig(),
		VCSAuth: &VCSAuthConfig{ContainerDir: "/job/.auth", GitConfig: "x", Credentials: "y"},
	})
	if err == nil {
		t.Fatal("docker capability with egress must be refused")
	}
	secrets, _ := clientset.CoreV1().Secrets("reactorcide").List(context.Background(), metav1.ListOptions{})
	if len(secrets.Items) != 0 {
		t.Fatal("a refused job must not leave a VCS auth Secret")
	}
}
