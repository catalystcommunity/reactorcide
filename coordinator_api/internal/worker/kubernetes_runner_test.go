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

func TestKubernetesRunnerBuilderSidecarOverridesPodNonRootPolicy(t *testing.T) {
	clientset := fake.NewSimpleClientset()
	runner := &KubernetesRunner{
		clientset:      clientset,
		namespace:      "reactorcide",
		serviceAccount: "default",
		dindImage:      "docker:27-dind",
	}

	_, err := runner.SpawnJob(context.Background(), &JobConfig{
		JobID:        "test-job",
		Image:        "reactorcide/runnerbase:test",
		Command:      []string{"sh", "-c", "buildctl debug info"},
		Env:          map[string]string{},
		WorkingDir:   "/job",
		Capabilities: []string{CapabilityBuilder},
		RunAsUser:    "runner",
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

	var builder *int
	for i := range podSpec.InitContainers {
		if podSpec.InitContainers[i].Name == "builder" {
			builder = &i
			break
		}
	}
	if builder == nil {
		t.Fatalf("expected builder sidecar init container")
	}

	builderSecurity := podSpec.InitContainers[*builder].SecurityContext
	if builderSecurity == nil {
		t.Fatalf("builder sidecar should have an explicit security context")
	}
	if builderSecurity.Privileged == nil || !*builderSecurity.Privileged {
		t.Fatalf("builder sidecar should be privileged")
	}
	if builderSecurity.RunAsUser == nil || *builderSecurity.RunAsUser != 0 {
		t.Fatalf("builder sidecar should run as uid 0, got %v", builderSecurity.RunAsUser)
	}
	if builderSecurity.RunAsNonRoot == nil || *builderSecurity.RunAsNonRoot {
		t.Fatalf("builder sidecar should override pod runAsNonRoot=false")
	}

	job := podSpec.Containers[0]
	if job.SecurityContext == nil || job.SecurityContext.RunAsUser == nil || *job.SecurityContext.RunAsUser != 1001 {
		t.Fatalf("job container should still run as uid 1001, got %v", job.SecurityContext)
	}
}

func TestKubernetesRunnerDinDSidecarOverridesPodNonRootPolicy(t *testing.T) {
	clientset := fake.NewSimpleClientset()
	runner := &KubernetesRunner{
		clientset:      clientset,
		namespace:      "reactorcide",
		serviceAccount: "default",
		dindImage:      "docker:27-dind",
	}

	_, err := runner.SpawnJob(context.Background(), &JobConfig{
		JobID:        "test-job",
		Image:        "reactorcide/runnerbase:test",
		Command:      []string{"sh", "-c", "docker info"},
		Env:          map[string]string{},
		WorkingDir:   "/job",
		Capabilities: []string{CapabilityDocker},
		RunAsUser:    "runner",
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

	var dind *int
	for i := range podSpec.InitContainers {
		if podSpec.InitContainers[i].Name == "docker-daemon" {
			dind = &i
			break
		}
	}
	if dind == nil {
		t.Fatalf("expected docker-daemon sidecar init container")
	}

	// The DinD sidecar image runs as root, so it must override the pod-level
	// runAsNonRoot policy or the kubelet rejects it with CreateContainerConfigError.
	dindSecurity := podSpec.InitContainers[*dind].SecurityContext
	if dindSecurity == nil {
		t.Fatalf("docker-daemon sidecar should have an explicit security context")
	}
	if dindSecurity.Privileged == nil || !*dindSecurity.Privileged {
		t.Fatalf("docker-daemon sidecar should be privileged")
	}
	if dindSecurity.RunAsUser == nil || *dindSecurity.RunAsUser != 0 {
		t.Fatalf("docker-daemon sidecar should run as uid 0, got %v", dindSecurity.RunAsUser)
	}
	if dindSecurity.RunAsNonRoot == nil || *dindSecurity.RunAsNonRoot {
		t.Fatalf("docker-daemon sidecar should override pod runAsNonRoot=false")
	}
}

func TestKubernetesRunnerMountsVCSAuthAsWritableRuntimeFiles(t *testing.T) {
	clientset := fake.NewSimpleClientset()
	runner := &KubernetesRunner{
		clientset:      clientset,
		namespace:      "reactorcide",
		serviceAccount: "default",
		dindImage:      "docker:27-dind",
	}

	jobName, err := runner.SpawnJob(context.Background(), &JobConfig{
		JobID:      "test-job",
		Image:      "reactorcide/runnerbase:test",
		Command:    []string{"sh", "-c", "echo ok"},
		Env:        map[string]string{"GIT_CONFIG_GLOBAL": "/job/.reactorcide/vcs-auth/gitconfig"},
		WorkingDir: "/job",
		VCSAuth: &VCSAuthConfig{
			ContainerDir: "/job/.reactorcide/vcs-auth",
			GitConfig:    "[credential]\n\thelper = store --file /job/.reactorcide/vcs-auth/credentials\n",
			Credentials:  "https://x-access-token:test-token-123@github.com/example/repo.git\n",
		},
	})
	if err != nil {
		t.Fatalf("SpawnJob failed: %v", err)
	}

	secret, err := clientset.CoreV1().Secrets("reactorcide").Get(context.Background(), jobName+"-vcs-auth", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("expected VCS auth secret: %v", err)
	}
	if string(secret.Data["gitconfig"]) == "" || string(secret.Data["credentials"]) == "" {
		t.Fatalf("secret missing git auth data")
	}

	jobs, err := clientset.BatchV1().Jobs("reactorcide").List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatalf("listing jobs failed: %v", err)
	}
	podSpec := jobs.Items[0].Spec.Template.Spec
	if len(podSpec.InitContainers) != 2 {
		t.Fatalf("expected prepare and copy auth init containers, got %d", len(podSpec.InitContainers))
	}
	if podSpec.InitContainers[1].Name != "copy-vcs-auth" {
		t.Fatalf("expected copy-vcs-auth init container, got %q", podSpec.InitContainers[1].Name)
	}
	foundAuthMount := false
	for _, mount := range podSpec.Containers[0].VolumeMounts {
		if mount.Name == "vcs-auth" && mount.MountPath == "/job/.reactorcide/vcs-auth" && !mount.ReadOnly {
			foundAuthMount = true
		}
	}
	if !foundAuthMount {
		t.Fatalf("expected writable vcs-auth emptyDir mount on job container")
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
