package worker

import (
	"context"
	"testing"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

// TestKubernetesRunnerResourceRequirements verifies SpawnJob sets the pod's
// resources.requests.cpu, resources.limits.cpu, and resources.limits.memory
// straight from JobConfig's Kubernetes-style quantity strings, and --
// critically -- sets NO memory request. The fake clientset keeps
// spec.resources, so the auto probe selects pod-level resources.
func TestKubernetesRunnerResourceRequirements(t *testing.T) {
	clientset := fake.NewSimpleClientset()
	runner := &KubernetesRunner{
		clientset:      clientset,
		namespace:      "reactorcide",
		serviceAccount: "default",
		dindImage:      "docker:27-dind",
	}

	_, err := runner.SpawnJob(context.Background(), &JobConfig{
		JobID:       "test-job-resources",
		Image:       "reactorcide/runnerbase:test",
		Command:     []string{"sh", "-c", "echo ok"},
		Env:         map[string]string{},
		WorkingDir:  "/job",
		CPURequest:  "500m",
		CPULimit:    "2",
		MemoryLimit: "4Gi",
	})
	if err != nil {
		t.Fatalf("SpawnJob failed: %v", err)
	}

	jobs, err := clientset.BatchV1().Jobs("reactorcide").List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatalf("listing jobs failed: %v", err)
	}
	if len(jobs.Items) != 1 {
		t.Fatalf("expected 1 Kubernetes Job, got %d", len(jobs.Items))
	}

	podSpec := jobs.Items[0].Spec.Template.Spec
	if podSpec.Resources == nil {
		t.Fatal("expected pod-level spec.resources to be set")
	}
	res := *podSpec.Resources
	if c := podSpec.Containers[0].Resources; c.Limits != nil || c.Requests != nil {
		t.Errorf("job container must carry no resources with pod-level resources, got %+v", c)
	}

	wantCPURequest := resource.MustParse("500m")
	gotCPURequest, ok := res.Requests[corev1.ResourceCPU]
	if !ok {
		t.Fatal("expected resources.requests.cpu to be set")
	}
	if gotCPURequest.Cmp(wantCPURequest) != 0 {
		t.Errorf("resources.requests.cpu = %s, want %s", gotCPURequest.String(), wantCPURequest.String())
	}

	wantCPULimit := resource.MustParse("2")
	gotCPULimit, ok := res.Limits[corev1.ResourceCPU]
	if !ok {
		t.Fatal("expected resources.limits.cpu to be set")
	}
	if gotCPULimit.Cmp(wantCPULimit) != 0 {
		t.Errorf("resources.limits.cpu = %s, want %s", gotCPULimit.String(), wantCPULimit.String())
	}

	wantMemLimit := resource.MustParse("4Gi")
	gotMemLimit, ok := res.Limits[corev1.ResourceMemory]
	if !ok {
		t.Fatal("expected resources.limits.memory to be set")
	}
	if gotMemLimit.Cmp(wantMemLimit) != 0 {
		t.Errorf("resources.limits.memory = %s, want %s", gotMemLimit.String(), wantMemLimit.String())
	}

	if _, ok := res.Requests[corev1.ResourceMemory]; ok {
		t.Error("expected NO resources.requests.memory to be set -- memory is limit-only")
	}
}

// TestKubernetesRunnerResourceRequirementsGBSuffix verifies the Kubernetes
// runner accepts our own memory grammar's decimal "GB"/"G" suffixes -- which
// resource.ParseQuantity rejects -- by building the resource.Quantity from
// our parsed byte count (resource.NewQuantity) rather than parsing the string
// with the k8s quantity parser. This is the whole point of replacing
// k8s.io/apimachinery/.../resource string parsing with internal/resources.
func TestKubernetesRunnerResourceRequirementsGBSuffix(t *testing.T) {
	clientset := fake.NewSimpleClientset()
	runner := &KubernetesRunner{
		clientset:      clientset,
		namespace:      "reactorcide",
		serviceAccount: "default",
		dindImage:      "docker:27-dind",
	}

	_, err := runner.SpawnJob(context.Background(), &JobConfig{
		JobID:       "test-job-resources-gb",
		Image:       "reactorcide/runnerbase:test",
		Command:     []string{"sh", "-c", "echo ok"},
		Env:         map[string]string{},
		WorkingDir:  "/job",
		MemoryLimit: "4GB",
	})
	if err != nil {
		t.Fatalf("SpawnJob failed: %v", err)
	}

	jobs, err := clientset.BatchV1().Jobs("reactorcide").List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatalf("listing jobs failed: %v", err)
	}
	if len(jobs.Items) != 1 {
		t.Fatalf("expected 1 Kubernetes Job, got %d", len(jobs.Items))
	}

	res := jobs.Items[0].Spec.Template.Spec.Resources
	if res == nil {
		t.Fatal("expected pod-level spec.resources to be set")
	}
	gotMemLimit, ok := res.Limits[corev1.ResourceMemory]
	if !ok {
		t.Fatal("expected resources.limits.memory to be set")
	}
	wantMemLimit := *resource.NewQuantity(4*1000*1000*1000, resource.BinarySI)
	if gotMemLimit.Cmp(wantMemLimit) != 0 {
		t.Errorf("resources.limits.memory = %s, want %s", gotMemLimit.String(), wantMemLimit.String())
	}
	if gotMemLimit.Value() != 4*1000*1000*1000 {
		t.Errorf("resources.limits.memory bytes = %d, want %d", gotMemLimit.Value(), 4*1000*1000*1000)
	}
}

// TestKubernetesRunnerResourceRequirementsEmpty verifies that when JobConfig
// carries no resource fields at all, SpawnJob leaves Resources.Requests and
// Resources.Limits unset (nil) rather than empty-but-present maps.
func TestKubernetesRunnerResourceRequirementsEmpty(t *testing.T) {
	clientset := fake.NewSimpleClientset()
	runner := &KubernetesRunner{
		clientset:      clientset,
		namespace:      "reactorcide",
		serviceAccount: "default",
		dindImage:      "docker:27-dind",
	}

	_, err := runner.SpawnJob(context.Background(), &JobConfig{
		JobID:      "test-job-no-resources",
		Image:      "reactorcide/runnerbase:test",
		Command:    []string{"sh", "-c", "echo ok"},
		Env:        map[string]string{},
		WorkingDir: "/job",
	})
	if err != nil {
		t.Fatalf("SpawnJob failed: %v", err)
	}

	jobs, err := clientset.BatchV1().Jobs("reactorcide").List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatalf("listing jobs failed: %v", err)
	}
	if jobs.Items[0].Spec.Template.Spec.Resources != nil {
		t.Errorf("expected nil pod-level resources, got %v", jobs.Items[0].Spec.Template.Spec.Resources)
	}
	res := jobs.Items[0].Spec.Template.Spec.Containers[0].Resources
	if res.Requests != nil {
		t.Errorf("expected nil Requests, got %v", res.Requests)
	}
	if res.Limits != nil {
		t.Errorf("expected nil Limits, got %v", res.Limits)
	}
}

func spawnResourceJob(t *testing.T, runner *KubernetesRunner, clientset *fake.Clientset, config *JobConfig) corev1.PodSpec {
	t.Helper()
	if _, err := runner.SpawnJob(context.Background(), config); err != nil {
		t.Fatalf("SpawnJob failed: %v", err)
	}
	jobs, err := clientset.BatchV1().Jobs("reactorcide").List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatalf("listing jobs failed: %v", err)
	}
	if len(jobs.Items) != 1 {
		t.Fatalf("expected exactly the job's Kubernetes Job (probe removed), got %d", len(jobs.Items))
	}
	return jobs.Items[0].Spec.Template.Spec
}

// TestKubernetesRunnerSidecarsSharePodResources: the buildkit and DinD
// sidecars carry no resources of their own; they share the pod budget with
// the job container. The GPU limit stays on the job container, because
// pod-level resources accept only cpu, memory, and hugepages.
func TestKubernetesRunnerSidecarsSharePodResources(t *testing.T) {
	clientset := fake.NewSimpleClientset()
	runner := &KubernetesRunner{clientset: clientset, namespace: "reactorcide", serviceAccount: "default", dindImage: "docker:27-dind"}

	podSpec := spawnResourceJob(t, runner, clientset, &JobConfig{
		JobID: "test-job-sidecars", Image: "reactorcide/runnerbase:test", Command: []string{"true"},
		Env: map[string]string{}, WorkingDir: "/job", CPULimit: "4", MemoryLimit: "8Gi",
		Capabilities: []string{CapabilityDocker, CapabilityGPU},
	})

	if podSpec.Resources == nil || podSpec.Resources.Limits.Memory().Cmp(resource.MustParse("8Gi")) != 0 {
		t.Fatalf("pod-level memory limit = %v, want 8Gi", podSpec.Resources)
	}
	var sawSidecar bool
	for _, ic := range podSpec.InitContainers {
		if ic.RestartPolicy != nil && *ic.RestartPolicy == corev1.ContainerRestartPolicyAlways {
			sawSidecar = true
			if ic.Resources.Limits != nil || ic.Resources.Requests != nil {
				t.Errorf("sidecar %s must share the pod budget, got own resources %+v", ic.Name, ic.Resources)
			}
		}
	}
	if !sawSidecar {
		t.Fatal("expected a DinD sidecar")
	}
	job := podSpec.Containers[0].Resources
	if _, ok := job.Limits["nvidia.com/gpu"]; !ok {
		t.Error("GPU limit must stay on the job container")
	}
	if _, ok := job.Limits[corev1.ResourceMemory]; ok {
		t.Error("memory must not be on the job container with pod-level resources")
	}
}

// TestKubernetesRunnerFallsBackWhenPodResourcesDropped: an API server with
// the PodLevelResources feature gate off drops spec.resources without an
// error. The probe sees that, and the runner puts the limits on the job
// container instead of leaving the pod unlimited.
func TestKubernetesRunnerFallsBackWhenPodResourcesDropped(t *testing.T) {
	clientset := fake.NewSimpleClientset()
	clientset.PrependReactor("create", "jobs", func(action k8stesting.Action) (bool, runtime.Object, error) {
		job := action.(k8stesting.CreateAction).GetObject().(*batchv1.Job)
		job.Spec.Template.Spec.Resources = nil // feature gate off
		return false, nil, nil
	})
	runner := &KubernetesRunner{clientset: clientset, namespace: "reactorcide", serviceAccount: "default", dindImage: "docker:27-dind"}

	podSpec := spawnResourceJob(t, runner, clientset, &JobConfig{
		JobID: "test-job-fallback", Image: "reactorcide/runnerbase:test", Command: []string{"true"},
		Env: map[string]string{}, WorkingDir: "/job", MemoryLimit: "2Gi",
	})

	got, ok := podSpec.Containers[0].Resources.Limits[corev1.ResourceMemory]
	if !ok || got.Cmp(resource.MustParse("2Gi")) != 0 {
		t.Fatalf("fallback must put the memory limit on the job container, got %+v", podSpec.Containers[0].Resources)
	}
}

func TestKubernetesRunnerContainerResourceScope(t *testing.T) {
	clientset := fake.NewSimpleClientset()
	runner := &KubernetesRunner{clientset: clientset, namespace: "reactorcide", serviceAccount: "default",
		dindImage: "docker:27-dind", resourceScope: ResourceScopeContainer}

	podSpec := spawnResourceJob(t, runner, clientset, &JobConfig{
		JobID: "test-job-container-scope", Image: "reactorcide/runnerbase:test", Command: []string{"true"},
		Env: map[string]string{}, WorkingDir: "/job", MemoryLimit: "2Gi",
	})

	if podSpec.Resources != nil {
		t.Errorf("container scope must not set pod-level resources, got %v", podSpec.Resources)
	}
	if _, ok := podSpec.Containers[0].Resources.Limits[corev1.ResourceMemory]; !ok {
		t.Error("container scope must put the memory limit on the job container")
	}
}
