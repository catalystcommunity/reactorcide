package worker

import (
	"context"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

// The worker auto-retries an infra.platform failure only when the runtime
// proves that the main container never started. ExecutionTiming gives
// that proof.
func TestKubernetesExecutionTimingMainNotStarted(t *testing.T) {
	labels := map[string]string{"reactorcide.io/job-name": "build"}
	startedAt := metav1.NewTime(time.Now().Add(-time.Minute).Truncate(time.Second))
	cases := []struct {
		name           string
		pods           []*corev1.Pod
		wantStarted    bool
		wantNotStarted bool
	}{
		{"pending pod, main waiting", []*corev1.Pod{jobPod("p", labels, corev1.PodStatus{Phase: corev1.PodPending, ContainerStatuses: []corev1.ContainerStatus{{
			Name: "job", State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "ContainerCreating"}},
		}}})}, false, true},
		{"pending pod, no statuses", []*corev1.Pod{jobPod("p", labels, corev1.PodStatus{Phase: corev1.PodPending})}, false, true},
		{"evicted before start", []*corev1.Pod{jobPod("p", labels, corev1.PodStatus{Phase: corev1.PodFailed, Reason: "Evicted"})}, false, true},
		{"evicted after start", []*corev1.Pod{jobPod("p", labels, corev1.PodStatus{Phase: corev1.PodFailed, Reason: "Evicted", ContainerStatuses: []corev1.ContainerStatus{{
			Name: "job", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: "Error", ExitCode: 137, StartedAt: startedAt}},
		}}})}, true, false},
		{"no pod is no proof", nil, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client := fake.NewSimpleClientset()
			for _, pod := range tc.pods {
				if _, err := client.CoreV1().Pods("test").Create(context.Background(), pod, metav1.CreateOptions{}); err != nil {
					t.Fatal(err)
				}
			}
			runner := &KubernetesRunner{clientset: client, namespace: "test"}
			timing, err := runner.ExecutionTiming(context.Background(), "build")
			if err != nil {
				t.Fatal(err)
			}
			if !timing.StartedAt.IsZero() != tc.wantStarted || timing.MainNotStarted != tc.wantNotStarted {
				t.Fatalf("timing = %+v, want started=%v notStarted=%v", timing, tc.wantStarted, tc.wantNotStarted)
			}
		})
	}
}

func TestKubernetesClassifyExitDeadlineExceededIsTimeout(t *testing.T) {
	client := fake.NewSimpleClientset(&batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: "build", Namespace: "test"},
		Status: batchv1.JobStatus{Conditions: []batchv1.JobCondition{{
			Type: batchv1.JobFailed, Status: corev1.ConditionTrue, Reason: "DeadlineExceeded",
		}}},
	})
	runner := &KubernetesRunner{clientset: client, namespace: "test"}
	class, detail := runner.ClassifyExit(context.Background(), "build", 143)
	if class != models.FailureClassTimeout || detail != "DeadlineExceeded" {
		t.Fatalf("class = %q/%q, want timeout/DeadlineExceeded", class, detail)
	}
	if _, err := runner.WaitForCompletion(context.Background(), "build"); err == nil {
		t.Fatal("WaitForCompletion must report the deadline")
	} else if got, _ := ClassifyExecutionError(err, -1); got != models.FailureClassTimeout {
		t.Fatalf("WaitForCompletion class = %q, want timeout", got)
	}
}

// The Job deadline counts from Job start (scheduling and image pull), so it
// must leave room for the startup timeout: the worker timeout, which counts
// from the main container start, fires first.
func TestKubernetesActiveDeadlineAddsStartupAllowance(t *testing.T) {
	clientset := fake.NewSimpleClientset()
	runner := &KubernetesRunner{clientset: clientset, namespace: "reactorcide", serviceAccount: "default", dindImage: "docker:27-dind", startupTimeout: 10 * time.Minute}
	if _, err := runner.SpawnJob(context.Background(), &JobConfig{
		JobID: "deadline", Image: "alpine", Command: []string{"true"}, Env: map[string]string{}, WorkingDir: "/job", TimeoutSeconds: 1800,
	}); err != nil {
		t.Fatal(err)
	}
	jobs, err := clientset.BatchV1().Jobs("reactorcide").List(context.Background(), metav1.ListOptions{})
	if err != nil || len(jobs.Items) != 1 {
		t.Fatalf("jobs = %v, %v", jobs, err)
	}
	got := jobs.Items[0].Spec.ActiveDeadlineSeconds
	want := int64(1800 + 600 + kubernetesDeadlineAllowanceSeconds)
	if got == nil || *got != want {
		t.Fatalf("ActiveDeadlineSeconds = %v, want %d", got, want)
	}
}
