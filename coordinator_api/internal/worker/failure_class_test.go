package worker

import (
	"context"
	"errors"
	"fmt"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

func TestClassifyExecutionError(t *testing.T) {
	cases := []struct {
		name       string
		err        error
		exitCode   int
		wantClass  string
		wantDetail string
	}{
		{"success", nil, 0, "", ""},
		{"non-zero exit is the job", nil, 2, models.FailureClassJob, ""},
		{"exit 137 without a runner reason is the job", nil, 137, models.FailureClassJob, ""},
		{"image pull", NewFailureError(models.FailureClassImagePull, "image pull", errors.New("manifest unknown")), -1, models.FailureClassImagePull, "image pull"},
		{"wrapped failure error", fmt.Errorf("spawn: %w", NewFailureError(models.FailureClassScheduling, "x", errors.New("no node"))), -1, models.FailureClassScheduling, "x"},
		{"pod image pull", &PodStartupError{Reason: "ImagePullBackOff"}, -1, models.FailureClassImagePull, "ImagePullBackOff"},
		{"pod unschedulable", fmt.Errorf("wait: %w", &PodStartupError{Reason: "ResourceRequestTooLarge"}), -1, models.FailureClassScheduling, "ResourceRequestTooLarge"},
		{"pod startup timeout", &PodStartupError{Reason: "StartupTimeout"}, -1, models.FailureClassScheduling, "StartupTimeout"},
		{"pod config error", &PodStartupError{Reason: "CreateContainerConfigError"}, -1, models.FailureClassPlatform, "CreateContainerConfigError"},
		{"plain runtime error", errors.New("docker daemon gone"), -1, models.FailureClassPlatform, ""},
		{"invalid class falls back", NewFailureError("bogus", "", errors.New("x")), -1, models.FailureClassPlatform, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			class, detail := ClassifyExecutionError(tc.err, tc.exitCode)
			if class != tc.wantClass || detail != tc.wantDetail {
				t.Fatalf("ClassifyExecutionError = (%q, %q), want (%q, %q)", class, detail, tc.wantClass, tc.wantDetail)
			}
		})
	}
	if NewFailureError(models.FailureClassJob, "", nil) != nil {
		t.Fatal("NewFailureError(nil) must be nil")
	}
}

type classifyingRunner struct {
	JobRunner
	class, detail string
}

func (r classifyingRunner) ClassifyExit(context.Context, string, int) (string, string) {
	return r.class, r.detail
}

// plainRunner is a JobRunner without the ExitClassifier capability.
type plainRunner struct{ JobRunner }

func TestClassifyExitUsesRunnerCapability(t *testing.T) {
	if class, _ := ClassifyExit(context.Background(), classifyingRunner{class: models.FailureClassOOM, detail: "OOMKilled"}, "j", 0); class != "" {
		t.Fatalf("exit 0 has no class, got %q", class)
	}
	if class, detail := ClassifyExit(context.Background(), classifyingRunner{class: models.FailureClassOOM, detail: "OOMKilled"}, "j", 137); class != models.FailureClassOOM || detail != "OOMKilled" {
		t.Fatalf("got (%q, %q)", class, detail)
	}
	if class, _ := ClassifyExit(context.Background(), classifyingRunner{}, "j", 1); class != models.FailureClassJob {
		t.Fatalf("an unknown runner reason is the job, got %q", class)
	}
	if class, _ := ClassifyExit(context.Background(), plainRunner{}, "j", 1); class != models.FailureClassJob {
		t.Fatalf("a runner without the capability gives job, got %q", class)
	}
}

func TestContainerExitClass(t *testing.T) {
	if class, detail := containerExitClass(true); class != models.FailureClassOOM || detail != "OOMKilled" {
		t.Fatalf("OOM kill = (%q, %q)", class, detail)
	}
	if class, _ := containerExitClass(false); class != "" {
		t.Fatalf("no OOM kill must leave the class to the caller, got %q", class)
	}
}

func TestParseNerdctlOOMKilled(t *testing.T) {
	oom, err := parseNerdctlOOMKilled([]byte(`{"Status":"exited","OOMKilled":true,"ExitCode":137}` + "\n"))
	if err != nil || !oom {
		t.Fatalf("got %v %v", oom, err)
	}
	oom, err = parseNerdctlOOMKilled([]byte(`{"Status":"exited","ExitCode":1}`))
	if err != nil || oom {
		t.Fatalf("a missing field reads as false, got %v %v", oom, err)
	}
	if _, err := parseNerdctlOOMKilled([]byte("not json")); err == nil {
		t.Fatal("bad output must be an error")
	}
	if got := lastLines("a\nb\nc\nd\n", 2); got != "c\nd" {
		t.Fatalf("lastLines = %q", got)
	}
}

func jobPod(name string, labels map[string]string, status corev1.PodStatus) *corev1.Pod {
	return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "test", Labels: labels}, Status: status}
}

func TestKubernetesClassifyExit(t *testing.T) {
	labels := map[string]string{"reactorcide.io/job-name": "build"}
	terminated := func(reason string, code int32) corev1.PodStatus {
		return corev1.PodStatus{Phase: corev1.PodFailed, ContainerStatuses: []corev1.ContainerStatus{{
			Name: "job", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: reason, ExitCode: code}},
		}}}
	}
	cases := []struct {
		name      string
		pods      []*corev1.Pod
		wantClass string
	}{
		{"oom killed", []*corev1.Pod{jobPod("p", labels, terminated("OOMKilled", 137))}, models.FailureClassOOM},
		{"ordinary error exit", []*corev1.Pod{jobPod("p", labels, terminated("Error", 1))}, ""},
		{"evicted", []*corev1.Pod{jobPod("p", labels, corev1.PodStatus{Phase: corev1.PodFailed, Reason: "Evicted"})}, models.FailureClassPlatform},
		{"no pod left", nil, models.FailureClassPlatform},
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
			class, _ := runner.ClassifyExit(context.Background(), "build", 1)
			if class != tc.wantClass {
				t.Fatalf("class = %q, want %q", class, tc.wantClass)
			}
		})
	}
}

var (
	_ ExitClassifier = (*DockerRunner)(nil)
	_ ExitClassifier = (*ContainerdRunner)(nil)
	_ ExitClassifier = (*KubernetesRunner)(nil)
)
