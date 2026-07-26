package worker

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// TestKubernetesRunnerResourceRequirements verifies SpawnJob sets the job
// container's resources.requests.cpu, resources.limits.cpu, and
// resources.limits.memory straight from JobConfig's Kubernetes-style
// quantity strings, and -- critically -- sets NO memory request (memory is
// limit-only in reactorcide; see WORKERS_PLAN.md "Resources").
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

	container := jobs.Items[0].Spec.Template.Spec.Containers[0]
	res := container.Resources

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
// our parsed byte count (resource.NewQuantity) rather than parsing the
// string with the k8s quantity parser. This is the whole point of replacing
// k8s.io/apimachinery/.../resource string parsing with internal/resources
// (see WORKERS_PLAN.md "Resources").
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

	res := jobs.Items[0].Spec.Template.Spec.Containers[0].Resources
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
	res := jobs.Items[0].Spec.Template.Spec.Containers[0].Resources
	if res.Requests != nil {
		t.Errorf("expected nil Requests, got %v", res.Requests)
	}
	if res.Limits != nil {
		t.Errorf("expected nil Limits, got %v", res.Limits)
	}
}
