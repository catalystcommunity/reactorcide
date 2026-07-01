package worker

import (
	"context"
	"errors"
	"fmt"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestPodStartupError(t *testing.T) {
	tests := []struct {
		name           string
		reason         string
		message        string
		expectedString string
	}{
		{
			name:           "ImagePullBackOff with message",
			reason:         "ImagePullBackOff",
			message:        "Back-off pulling image \"invalid:image\"",
			expectedString: "pod failed to start: ImagePullBackOff - Back-off pulling image \"invalid:image\"",
		},
		{
			name:           "ErrImagePull without message",
			reason:         "ErrImagePull",
			message:        "",
			expectedString: "pod failed to start: ErrImagePull",
		},
		{
			name:           "CreateContainerConfigError with message",
			reason:         "CreateContainerConfigError",
			message:        "container config invalid",
			expectedString: "pod failed to start: CreateContainerConfigError - container config invalid",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := &PodStartupError{
				Reason:  tt.reason,
				Message: tt.message,
			}

			if err.Error() != tt.expectedString {
				t.Errorf("expected error string %q, got %q", tt.expectedString, err.Error())
			}
		})
	}
}

func TestKubernetesRunnerPrepareWorkspaceRunsAsRootForNonRootJobs(t *testing.T) {
	clientset := fake.NewSimpleClientset()
	runner := &KubernetesRunner{
		clientset:      clientset,
		namespace:      "reactorcide",
		serviceAccount: "default",
		dindImage:      "docker:27-dind",
	}

	_, err := runner.SpawnJob(context.Background(), &JobConfig{
		JobID:      "test-job",
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
	if len(jobs.Items) != 1 {
		t.Fatalf("expected 1 Kubernetes Job, got %d", len(jobs.Items))
	}

	podSpec := jobs.Items[0].Spec.Template.Spec
	if podSpec.SecurityContext == nil || podSpec.SecurityContext.RunAsNonRoot == nil || !*podSpec.SecurityContext.RunAsNonRoot {
		t.Fatalf("expected pod to default to runAsNonRoot=true")
	}
	if len(podSpec.InitContainers) != 1 {
		t.Fatalf("expected 1 init container, got %d", len(podSpec.InitContainers))
	}

	prepare := podSpec.InitContainers[0]
	if prepare.Name != "prepare-workspace" {
		t.Fatalf("expected prepare-workspace init container, got %q", prepare.Name)
	}
	if prepare.SecurityContext == nil {
		t.Fatalf("prepare-workspace should have an explicit security context")
	}
	if prepare.SecurityContext.RunAsUser == nil || *prepare.SecurityContext.RunAsUser != 0 {
		t.Fatalf("prepare-workspace should run as uid 0, got %v", prepare.SecurityContext.RunAsUser)
	}
	if prepare.SecurityContext.RunAsNonRoot == nil || *prepare.SecurityContext.RunAsNonRoot {
		t.Fatalf("prepare-workspace should override pod runAsNonRoot=false")
	}

	job := podSpec.Containers[0]
	if job.SecurityContext == nil || job.SecurityContext.RunAsUser == nil || *job.SecurityContext.RunAsUser != 1001 {
		t.Fatalf("job container should run as uid 1001, got %v", job.SecurityContext)
	}
}

func TestIsPodStartupError(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected bool
	}{
		{
			name:     "PodStartupError returns true",
			err:      &PodStartupError{Reason: "ImagePullBackOff", Message: "test"},
			expected: true,
		},
		{
			name:     "wrapped PodStartupError returns true",
			err:      fmt.Errorf("failed to get pod for job: %w", &PodStartupError{Reason: "ErrImagePull", Message: "test"}),
			expected: true,
		},
		{
			name:     "double wrapped PodStartupError returns true",
			err:      fmt.Errorf("outer: %w", fmt.Errorf("inner: %w", &PodStartupError{Reason: "CrashLoopBackOff", Message: "test"})),
			expected: true,
		},
		{
			name:     "regular error returns false",
			err:      errors.New("some error"),
			expected: false,
		},
		{
			name:     "nil error returns false",
			err:      nil,
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := IsPodStartupError(tt.err)
			if result != tt.expected {
				t.Errorf("expected %v, got %v", tt.expected, result)
			}
		})
	}
}
