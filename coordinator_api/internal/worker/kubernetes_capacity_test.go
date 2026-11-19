package worker

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/admission"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
)

func capacityNode(name, cpu, memory string, nodeLabels map[string]string, ready bool) *corev1.Node {
	status := corev1.ConditionTrue
	if !ready {
		status = corev1.ConditionFalse
	}
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: name, Labels: nodeLabels},
		Status: corev1.NodeStatus{
			Allocatable: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse(cpu),
				corev1.ResourceMemory: resource.MustParse(memory),
			},
			Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: status}},
		},
	}
}

func capacityJobPod(name, namespace, jobID, cpu, memory string, phase corev1.PodPhase) *corev1.Pod {
	podLabels := map[string]string{}
	if jobID != "" {
		podLabels["reactorcide.io/job-id"] = jobID
	}
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace, Labels: podLabels},
		Spec: corev1.PodSpec{Containers: []corev1.Container{{
			Name: "job",
			Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse(cpu),
				corev1.ResourceMemory: resource.MustParse(memory),
			}},
		}}},
		Status: corev1.PodStatus{Phase: phase},
	}
}

func TestKubernetesResourceCapacity(t *testing.T) {
	const gi = int64(1024 * 1024 * 1024)
	cordoned := capacityNode("cordoned", "8", "32Gi", nil, true)
	cordoned.Spec.Unschedulable = true
	tainted := capacityNode("tainted", "8", "32Gi", nil, true)
	tainted.Spec.Taints = []corev1.Taint{{Key: "dedicated", Value: "db", Effect: corev1.TaintEffectNoSchedule}}
	objects := []runtime.Object{
		capacityNode("a", "4", "16Gi", nil, true),
		capacityNode("b", "2", "8Gi", nil, true),
		capacityNode("not-ready", "8", "32Gi", nil, false),
		cordoned,
		tainted,
		capacityJobPod("job-1", "jobs", "j1", "1", "4Gi", corev1.PodRunning),
		capacityJobPod("job-2", "jobs", "j2", "500m", "1Gi", corev1.PodPending),
		capacityJobPod("job-done", "jobs", "j3", "4", "8Gi", corev1.PodSucceeded),
		capacityJobPod("other-app", "jobs", "", "2", "2Gi", corev1.PodRunning),
		capacityJobPod("other-namespace", "elsewhere", "j4", "2", "2Gi", corev1.PodRunning),
	}
	runner := &KubernetesRunner{clientset: fake.NewSimpleClientset(objects...), namespace: "jobs"}
	snapshot, err := runner.ResourceCapacity(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if want := (admission.Resources{CPUMillicores: 6000, MemoryBytes: 24 * gi}); snapshot.Capacity != want {
		t.Fatalf("capacity = %+v, want %+v", snapshot.Capacity, want)
	}
	if want := (admission.Resources{CPUMillicores: 1500, MemoryBytes: 5 * gi}); snapshot.Used != want {
		t.Fatalf("used = %+v, want %+v", snapshot.Used, want)
	}
	if !snapshot.CountedJobIDs["j1"] || !snapshot.CountedJobIDs["j2"] || snapshot.CountedJobIDs["j3"] || len(snapshot.CountedJobIDs) != 2 {
		t.Fatalf("counted = %v", snapshot.CountedJobIDs)
	}
}

func TestKubernetesResourceCapacityNodeSelector(t *testing.T) {
	selector, err := labels.Parse("pool=ci")
	if err != nil {
		t.Fatal(err)
	}
	runner := &KubernetesRunner{
		clientset: fake.NewSimpleClientset(
			capacityNode("ci", "4", "8Gi", map[string]string{"pool": "ci"}, true),
			capacityNode("web", "16", "64Gi", map[string]string{"pool": "web"}, true),
		),
		namespace:            "jobs",
		capacityNodeSelector: selector,
	}
	snapshot, err := runner.ResourceCapacity(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Capacity.CPUMillicores != 4000 {
		t.Fatalf("capacity = %+v", snapshot.Capacity)
	}
}

func TestPodAdmissionNeedIncludesPodLevelResources(t *testing.T) {
	pod := capacityJobPod("p", "jobs", "j", "250m", "256Mi", corev1.PodPending)
	pod.Spec.Resources = &corev1.ResourceRequirements{Requests: corev1.ResourceList{
		corev1.ResourceCPU:    resource.MustParse("2"),
		corev1.ResourceMemory: resource.MustParse("4Gi"),
	}}
	need := podAdmissionNeed(pod)
	if need.CPUMillicores != 2000 || need.MemoryBytes != 4*1024*1024*1024 {
		t.Fatalf("need = %+v", need)
	}
}

func TestKubernetesDetectTopology(t *testing.T) {
	region := map[string]string{topologyRegionLabel: "us-east-1", topologyZoneLabel: "us-east-1a"}
	otherZone := map[string]string{topologyRegionLabel: "us-east-1", topologyZoneLabel: "us-east-1b"}
	runner := &KubernetesRunner{clientset: fake.NewSimpleClientset(
		capacityNode("a", "1", "1Gi", region, true),
		capacityNode("b", "1", "1Gi", otherZone, true),
		capacityNode("down", "1", "1Gi", map[string]string{topologyRegionLabel: "eu-west-1"}, false),
	)}
	got, err := runner.DetectTopology(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got["region"] != "us-east-1" {
		t.Fatalf("region = %q", got["region"])
	}
	if _, ok := got["zone"]; ok {
		t.Fatalf("zone must be absent when nodes span many zones: %v", got)
	}

	single := &KubernetesRunner{clientset: fake.NewSimpleClientset(capacityNode("a", "1", "1Gi", region, true))}
	got, err = single.DetectTopology(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got["region"] != "us-east-1" || got["zone"] != "us-east-1a" {
		t.Fatalf("topology = %v", got)
	}

	empty := &KubernetesRunner{clientset: fake.NewSimpleClientset()}
	got, err = empty.DetectTopology(context.Background())
	if err != nil || len(got) != 0 {
		t.Fatalf("topology = %v, %v", got, err)
	}
}

const testKubeconfig = `apiVersion: v1
kind: Config
clusters:
- name: remote
  cluster:
    server: https://remote.example.invalid:6443
- name: other
  cluster:
    server: https://other.example.invalid:6443
contexts:
- name: remote
  context:
    cluster: remote
    user: ci
    namespace: ci-jobs
- name: other
  context:
    cluster: other
    user: ci
current-context: remote
users:
- name: ci
  user:
    token: not-a-real-token
`

func TestLoadKubernetesClusterConfigFromKubeconfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kubeconfig")
	if err := os.WriteFile(path, []byte(testKubeconfig), 0o600); err != nil {
		t.Fatal(err)
	}
	cluster, err := loadKubernetesClusterConfig(path, "")
	if err != nil {
		t.Fatal(err)
	}
	if cluster.inCluster || cluster.rest.Host != "https://remote.example.invalid:6443" || cluster.namespace != "ci-jobs" {
		t.Fatalf("cluster = host %q namespace %q inCluster %v", cluster.rest.Host, cluster.namespace, cluster.inCluster)
	}

	cluster, err = loadKubernetesClusterConfig(path, "other")
	if err != nil {
		t.Fatal(err)
	}
	if cluster.rest.Host != "https://other.example.invalid:6443" || cluster.namespace != "default" {
		t.Fatalf("cluster = host %q namespace %q", cluster.rest.Host, cluster.namespace)
	}

	if _, err := loadKubernetesClusterConfig(path, "missing"); err == nil {
		t.Fatal("an unknown context must fail")
	}
	_, err = loadKubernetesClusterConfig(filepath.Join(t.TempDir(), "absent"), "")
	if err == nil {
		t.Fatal("a missing kubeconfig must fail")
	}
	if strings.Contains(err.Error(), "not-a-real-token") {
		t.Fatal("error leaks a credential")
	}
	if _, err := loadKubernetesClusterConfig("", "remote"); err == nil {
		t.Fatal("a context without a kubeconfig must fail")
	}
}

func TestNewKubernetesRunnerUsesKubeconfigNamespace(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kubeconfig")
	if err := os.WriteFile(path, []byte(testKubeconfig), 0o600); err != nil {
		t.Fatal(err)
	}
	runner, err := NewKubernetesRunnerWithConfig(KubernetesRunnerConfig{Kubeconfig: path, CapacityNodeSelector: "pool=ci"})
	if err != nil {
		t.Fatal(err)
	}
	if runner.namespace != "ci-jobs" {
		t.Fatalf("namespace = %q", runner.namespace)
	}
	if runner.restConfig.Host != "https://remote.example.invalid:6443" {
		t.Fatalf("host = %q", runner.restConfig.Host)
	}
	if runner.capacityNodeSelector.String() != "pool=ci" {
		t.Fatalf("selector = %q", runner.capacityNodeSelector.String())
	}
	// The metrics source uses the same clientset, so kubelet stats proxy
	// requests go to the kubeconfig cluster.
	client, err := (restMetricsSource{clientset: runner.clientset}).restClient()
	if err != nil {
		t.Fatal(err)
	}
	url := client.Get().AbsPath("api", "v1", "nodes", "n1", "proxy", "stats", "summary").URL()
	if url.Host != "remote.example.invalid:6443" {
		t.Fatalf("kubelet proxy URL host = %q", url.Host)
	}

	if _, err := NewKubernetesRunnerWithConfig(KubernetesRunnerConfig{Kubeconfig: path, CapacityNodeSelector: "=bad"}); err == nil {
		t.Fatal("an invalid node selector must fail")
	}
}
