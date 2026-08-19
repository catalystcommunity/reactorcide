package worker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// TestKubernetesRunnerHomeMatchesHOME guards that the k8s runner's writable
// home emptyDir is mounted at /home/runner -- the same path lease.go/run-local
// set HOME to (and the AGENTS.md convention). The runner previously used
// /home/reactorcide, so once lease.go set HOME=/home/runner the home was
// unwritable and every job died at
// "mkdir: cannot create directory '/home/runner': Permission denied".
func TestKubernetesRunnerHomeMatchesHOME(t *testing.T) {
	clientset := fake.NewSimpleClientset()
	runner := &KubernetesRunner{
		clientset:      clientset,
		namespace:      "reactorcide",
		serviceAccount: "default",
		dindImage:      "docker:27-dind",
	}
	if _, err := runner.SpawnJob(context.Background(), &JobConfig{
		JobID:      "home-job",
		Image:      "reactorcide/runnerbase:test",
		Command:    []string{"sh", "-c", "echo ok"},
		Env:        map[string]string{"HOME": "/home/runner"},
		WorkingDir: "/job",
	}); err != nil {
		t.Fatalf("SpawnJob failed: %v", err)
	}
	jobs, err := clientset.BatchV1().Jobs("reactorcide").List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatalf("listing jobs failed: %v", err)
	}
	podSpec := jobs.Items[0].Spec.Template.Spec
	if len(podSpec.Containers) != 1 {
		t.Fatalf("non-workflow job should not have a workflow output reader, got %d containers", len(podSpec.Containers))
	}

	foundHomeMount := false
	for _, m := range podSpec.Containers[0].VolumeMounts {
		if m.MountPath == "/home/runner" {
			foundHomeMount = true
		}
		if m.MountPath == "/home/reactorcide" {
			t.Errorf("job container mounts its writable home at /home/reactorcide; must be /home/runner to match HOME")
		}
	}
	if !foundHomeMount {
		t.Fatalf("job container has no volume mounted at /home/runner; HOME=/home/runner would be unwritable")
	}

	if len(podSpec.InitContainers) == 0 {
		t.Fatal("expected a prepare init container")
	}
	if prep := strings.Join(podSpec.InitContainers[0].Command, " "); !strings.Contains(prep, "/home/runner") {
		t.Errorf("prepare init container does not make /home/runner writable: %q", prep)
	}
}

func TestKubernetesRunnerAddsWorkflowOutputReader(t *testing.T) {
	clientset := fake.NewSimpleClientset()
	runner := &KubernetesRunner{
		clientset:      clientset,
		namespace:      "reactorcide",
		serviceAccount: "default",
		dindImage:      "docker:27-dind",
	}
	if _, err := runner.SpawnJob(context.Background(), &JobConfig{
		JobID:      "output-job",
		Image:      "reactorcide/runnerbase:test",
		Command:    []string{"sh", "-c", "echo ok"},
		Env:        map[string]string{"RC_WF_OUTPUT_FILE": "/job/workflow-output.json"},
		WorkingDir: "/job",
	}); err != nil {
		t.Fatalf("SpawnJob failed: %v", err)
	}

	jobs, err := clientset.BatchV1().Jobs("reactorcide").List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatalf("listing jobs failed: %v", err)
	}
	podSpec := jobs.Items[0].Spec.Template.Spec
	if len(podSpec.Containers) != 2 {
		t.Fatalf("expected job and workflow output reader containers, got %d", len(podSpec.Containers))
	}
	reader := podSpec.Containers[1]
	if reader.Name != kubernetesWorkflowOutputReader {
		t.Fatalf("expected %q container, got %q", kubernetesWorkflowOutputReader, reader.Name)
	}
	if reader.SecurityContext == nil || reader.SecurityContext.RunAsUser == nil || *reader.SecurityContext.RunAsUser != 1001 {
		t.Fatalf("workflow output reader should use job uid 1001, got %v", reader.SecurityContext)
	}
	if len(reader.VolumeMounts) != 1 || reader.VolumeMounts[0].Name != "job" || reader.VolumeMounts[0].MountPath != "/job" {
		t.Fatalf("workflow output reader must mount the private job volume: %#v", reader.VolumeMounts)
	}
}

func TestKubernetesRunnerWaitCapturesWorkflowOutput(t *testing.T) {
	const output = `{"vars":{"release_id":"release-123"}}`
	clientset := fake.NewSimpleClientset(&corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "output-pod",
			Namespace: "reactorcide",
			Labels:    map[string]string{"reactorcide.io/job-name": "output-job"},
		},
		Spec: corev1.PodSpec{Containers: []corev1.Container{
			{Name: "job"},
			{Name: kubernetesWorkflowOutputReader},
		}},
		Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{
			{Name: "job", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 0}}},
			{Name: kubernetesWorkflowOutputReader, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}},
		}},
	})
	runner := &KubernetesRunner{
		clientset: clientset,
		namespace: "reactorcide",
		execInPod: func(_ context.Context, podName, containerName string, command []string, stdout, _ io.Writer) error {
			if podName != "output-pod" || containerName != kubernetesWorkflowOutputReader {
				t.Fatalf("unexpected exec target %s/%s", podName, containerName)
			}
			if !strings.Contains(strings.Join(command, " "), "workflow-output.json") {
				t.Fatalf("exec command does not read workflow output: %v", command)
			}
			_, err := io.WriteString(stdout, output)
			return err
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	exitCode, err := runner.WaitForCompletion(ctx, "output-job")
	if err != nil {
		t.Fatalf("WaitForCompletion failed: %v", err)
	}
	if exitCode != 0 {
		t.Fatalf("expected exit code 0, got %d", exitCode)
	}
	got, ok := runner.TakeWorkflowOutput("output-job")
	if !ok || got != output {
		t.Fatalf("captured output = %q, %v; want %q, true", got, ok, output)
	}
	if _, ok := runner.TakeWorkflowOutput("output-job"); ok {
		t.Fatal("TakeWorkflowOutput must remove captured output")
	}
}

func TestKubernetesRunnerRejectsOversizeWorkflowOutput(t *testing.T) {
	runner := &KubernetesRunner{
		execInPod: func(_ context.Context, _, _ string, _ []string, stdout, _ io.Writer) error {
			_, err := io.WriteString(stdout, strings.Repeat("x", maxKubernetesWorkflowOutput+1))
			return err
		},
	}
	_, err := runner.readWorkflowOutputFromPod(context.Background(), "output-pod")
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("expected an output size error, got %v", err)
	}
}

func TestMainContainerStartedIgnoresSupportSidecar(t *testing.T) {
	pod := &corev1.Pod{Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{
		{Name: kubernetesWorkflowOutputReader, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}},
		{Name: "job", State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "ContainerCreating"}}},
	}}}
	if mainContainerStarted(pod) {
		t.Fatal("support sidecar must not make the main job container ready for log streaming")
	}
	pod.Status.ContainerStatuses[1].State = corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}
	if !mainContainerStarted(pod) {
		t.Fatal("running job container should be ready for log streaming")
	}
}

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

// TestKubernetesRunnerImagePullSecrets_CombinesGlobalAndApprovedJobLevel
// guards the pod-level imagePullSecrets composition: the operator's global
// list first, then the job's approved names, deduplicated in stable order.
func TestKubernetesRunnerImagePullSecrets_CombinesGlobalAndApprovedJobLevel(t *testing.T) {
	clientset := fake.NewSimpleClientset()
	runner := &KubernetesRunner{
		clientset:                  clientset,
		namespace:                  "reactorcide",
		serviceAccount:             "default",
		dindImage:                  "docker:27-dind",
		imagePullSecrets:           []string{"global-cred", "shared-cred"},
		allowedJobImagePullSecrets: []string{"regcred"},
	}
	if _, err := runner.SpawnJob(context.Background(), &JobConfig{
		JobID:            "pull-secret-job",
		Image:            "containers.example.com/private/tool:1",
		Command:          []string{"sh", "-c", "echo ok"},
		ImagePullSecrets: []string{"regcred", "shared-cred"},
	}); err != nil {
		t.Fatalf("SpawnJob failed: %v", err)
	}
	jobs, err := clientset.BatchV1().Jobs("reactorcide").List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatalf("listing jobs failed: %v", err)
	}
	podSpec := jobs.Items[0].Spec.Template.Spec
	var names []string
	for _, ref := range podSpec.ImagePullSecrets {
		names = append(names, ref.Name)
	}
	want := []string{"global-cred", "shared-cred", "regcred"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Fatalf("expected pod imagePullSecrets %v, got %v", want, names)
	}
}

// TestKubernetesRunnerImagePullSecrets_RejectsUnapprovedBeforeJobCreation
// guards the worker-side allowlist: an unapproved name fails before any
// Kubernetes Job exists, and the secure default (no allowlist) rejects all
// job-level requests.
func TestKubernetesRunnerImagePullSecrets_RejectsUnapprovedBeforeJobCreation(t *testing.T) {
	clientset := fake.NewSimpleClientset()
	runner := &KubernetesRunner{
		clientset:                  clientset,
		namespace:                  "reactorcide",
		serviceAccount:             "default",
		dindImage:                  "docker:27-dind",
		imagePullSecrets:           []string{"global-cred"},
		allowedJobImagePullSecrets: []string{"regcred"},
	}
	_, err := runner.SpawnJob(context.Background(), &JobConfig{
		JobID:            "unapproved-job",
		Image:            "containers.example.com/private/tool:1",
		Command:          []string{"sh", "-c", "echo ok"},
		ImagePullSecrets: []string{"sneaky-cred"},
	})
	if err == nil || !strings.Contains(err.Error(), "sneaky-cred") {
		t.Fatalf("expected allowlist rejection naming sneaky-cred, got %v", err)
	}
	jobs, listErr := clientset.BatchV1().Jobs("reactorcide").List(context.Background(), metav1.ListOptions{})
	if listErr != nil {
		t.Fatalf("listing jobs failed: %v", listErr)
	}
	if len(jobs.Items) != 0 {
		t.Fatalf("expected no Kubernetes Job to be created, found %d", len(jobs.Items))
	}

	// Secure default: with no allowlist configured at all, any request fails.
	runner.imagePullSecrets = nil
	runner.allowedJobImagePullSecrets = nil
	if _, err := runner.SpawnJob(context.Background(), &JobConfig{
		JobID:            "secure-default-job",
		Image:            "containers.example.com/private/tool:1",
		Command:          []string{"sh", "-c", "echo ok"},
		ImagePullSecrets: []string{"regcred"},
	}); err == nil {
		t.Fatal("expected rejection under the secure default (empty allowlist)")
	}
}

// TestKubernetesRunnerImagePullSecrets_InvalidNamesRejected guards worker
// re-validation of names independent of coordinator validation.
func TestKubernetesRunnerImagePullSecrets_InvalidNamesRejected(t *testing.T) {
	runner := &KubernetesRunner{
		clientset:                  fake.NewSimpleClientset(),
		namespace:                  "reactorcide",
		serviceAccount:             "default",
		dindImage:                  "docker:27-dind",
		allowedJobImagePullSecrets: []string{"regcred"},
	}
	for _, bad := range [][]string{{""}, {"Bad_Name"}, {"regcred", "regcred"}} {
		if _, err := runner.SpawnJob(context.Background(), &JobConfig{
			JobID:            "invalid-name-job",
			Image:            "img:1",
			Command:          []string{"true"},
			ImagePullSecrets: bad,
		}); err == nil {
			t.Fatalf("expected rejection for names %v", bad)
		}
	}
}
