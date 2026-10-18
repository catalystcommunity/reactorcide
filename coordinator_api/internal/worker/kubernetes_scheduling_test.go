package worker

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

func schedulingResources(cpu, memory string) corev1.ResourceList {
	return corev1.ResourceList{corev1.ResourceCPU: resource.MustParse(cpu), corev1.ResourceMemory: resource.MustParse(memory)}
}

func schedulingPod() *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: "build-pod", Namespace: "test", CreationTimestamp: metav1.Now(),
			Labels: map[string]string{"reactorcide.io/job-name": "build"},
		},
		Spec: corev1.PodSpec{Containers: []corev1.Container{{
			Name: "job", Resources: corev1.ResourceRequirements{Requests: schedulingResources("4", "8Gi")},
		}}},
		Status: corev1.PodStatus{
			Phase: corev1.PodPending,
			Conditions: []corev1.PodCondition{{Type: corev1.PodScheduled, Status: corev1.ConditionFalse,
				Reason: "Unschedulable", Message: "0/1 nodes are available: 1 Insufficient cpu, 1 Insufficient memory"}},
		},
	}
}

func schedulingNode(cpu, memory string) *corev1.Node {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "node", Labels: map[string]string{"pool": "build"}},
		Status:     corev1.NodeStatus{Allocatable: schedulingResources(cpu, memory)},
	}
}

func TestKubernetesSchedulingCapacity(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*corev1.Pod, *corev1.Node)
		fail   bool
	}{
		{name: "busy node can hold request"},
		{name: "exact fit", change: func(p *corev1.Pod, n *corev1.Node) { n.Status.Allocatable = schedulingResources("4", "8Gi") }},
		{name: "cpu too large", fail: true, change: func(p *corev1.Pod, n *corev1.Node) { n.Status.Allocatable = schedulingResources("3", "16Gi") }},
		{name: "memory too large", fail: true, change: func(p *corev1.Pod, n *corev1.Node) { n.Status.Allocatable = schedulingResources("8", "4Gi") }},
		{name: "limits do not replace admitted requests", change: func(p *corev1.Pod, n *corev1.Node) {
			p.Spec.Containers[0].Resources.Limits = schedulingResources("128", "1Ti")
		}},
		{name: "pod request replaces container total", change: func(p *corev1.Pod, n *corev1.Node) {
			p.Spec.Containers[0].Resources.Requests = schedulingResources("20", "32Gi")
			p.Spec.Resources = &corev1.ResourceRequirements{Requests: schedulingResources("8", "16Gi")}
		}},
		{name: "pod memory too large", fail: true, change: func(p *corev1.Pod, n *corev1.Node) {
			p.Spec.Resources = &corev1.ResourceRequirements{Requests: schedulingResources("4", "32Gi")}
		}},
		{name: "regular sidecar total", fail: true, change: func(p *corev1.Pod, n *corev1.Node) {
			p.Spec.Containers = append(p.Spec.Containers, corev1.Container{Name: "sidecar", Resources: corev1.ResourceRequirements{Requests: schedulingResources("5", "1Gi")}})
		}},
		{name: "init maximum not sum", change: func(p *corev1.Pod, n *corev1.Node) {
			p.Spec.InitContainers = []corev1.Container{{Name: "prepare", Resources: corev1.ResourceRequirements{Requests: schedulingResources("8", "16Gi")}}}
		}},
		{name: "native sidecar adds to later init", fail: true, change: func(p *corev1.Pod, n *corev1.Node) {
			always := corev1.ContainerRestartPolicyAlways
			p.Spec.InitContainers = []corev1.Container{
				{Name: "builder", RestartPolicy: &always, Resources: corev1.ResourceRequirements{Requests: schedulingResources("2", "1Gi")}},
				{Name: "prepare", Resources: corev1.ResourceRequirements{Requests: schedulingResources("7", "1Gi")}},
			}
		}},
		{name: "native sidecar adds to job", fail: true, change: func(p *corev1.Pod, n *corev1.Node) {
			always := corev1.ContainerRestartPolicyAlways
			p.Spec.InitContainers = []corev1.Container{{Name: "builder", RestartPolicy: &always, Resources: corev1.ResourceRequirements{Requests: schedulingResources("5", "1Gi")}}}
		}},
		{name: "pod overhead", fail: true, change: func(p *corev1.Pod, n *corev1.Node) { p.Spec.Overhead = schedulingResources("5", "1Gi") }},
		{name: "no matching selector waits", change: func(p *corev1.Pod, n *corev1.Node) { p.Spec.NodeSelector = map[string]string{"pool": "other"} }},
		{name: "missing node capacity waits", change: func(p *corev1.Pod, n *corev1.Node) { n.Status.Allocatable = nil }},
		{name: "readiness cordon and taint can recover", change: func(p *corev1.Pod, n *corev1.Node) {
			n.Spec.Unschedulable = true
			n.Spec.Taints = []corev1.Taint{{Key: "node.kubernetes.io/not-ready", Effect: corev1.TaintEffectNoSchedule}}
		}},
		{name: "other scheduling constraint waits", change: func(p *corev1.Pod, n *corev1.Node) {
			p.Status.Conditions[0].Message = "node(s) had untolerated taint"
		}},
		{name: "stale scheduling condition after start", change: func(p *corev1.Pod, n *corev1.Node) {
			n.Status.Allocatable = schedulingResources("1", "1Gi")
			p.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "job", State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}}}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pod, node := schedulingPod(), schedulingNode("8", "16Gi")
			if tc.change != nil {
				tc.change(pod, node)
			}
			runner := &KubernetesRunner{autoscalingMode: "disabled", clientset: fake.NewSimpleClientset(node), namespace: "test"}
			reason, message := runner.checkPodStartupFailure(context.Background(), pod)
			if tc.fail {
				if reason != "ResourceRequestTooLarge" || !strings.Contains(message, "allocatable") {
					t.Fatalf("got %q: %s", reason, message)
				}
			} else if reason != "" {
				t.Fatalf("unexpected failure %s: %s", reason, message)
			}
		})
	}
}

func TestKubernetesSchedulingNeedsOneNodeForBothResources(t *testing.T) {
	pod := schedulingPod()
	cpuNode, memNode := schedulingNode("8", "4Gi"), schedulingNode("2", "16Gi")
	memNode.Name = "memory-node"
	client := fake.NewSimpleClientset(cpuNode, memNode)
	runner := &KubernetesRunner{autoscalingMode: "disabled", clientset: client}
	if reason, _ := runner.checkPodStartupFailure(context.Background(), pod); reason != "ResourceRequestTooLarge" {
		t.Fatalf("got %q", reason)
	}
	fit := schedulingNode("8", "16Gi")
	fit.Name = "fit"
	if _, err := client.CoreV1().Nodes().Create(context.Background(), fit, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if reason, _ := runner.checkPodStartupFailure(context.Background(), pod); reason != "" {
		t.Fatalf("fit node rejected: %s", reason)
	}
	// The only node with enough resources is outside the required affinity.
	pod.Spec.Affinity = &corev1.Affinity{NodeAffinity: &corev1.NodeAffinity{RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{
		NodeSelectorTerms: []corev1.NodeSelectorTerm{{MatchFields: []corev1.NodeSelectorRequirement{{Key: "metadata.name", Operator: corev1.NodeSelectorOpNotIn, Values: []string{"fit"}}}}},
	}}}
	if reason, _ := runner.checkPodStartupFailure(context.Background(), pod); reason != "ResourceRequestTooLarge" {
		t.Fatalf("affinity ignored: %q", reason)
	}
}

func TestKubernetesSchedulingUnavailableNodesWait(t *testing.T) {
	for _, failure := range []error{nil, apierrors.NewForbidden(schema.GroupResource{Resource: "nodes"}, "", fmt.Errorf("denied")), fmt.Errorf("API unavailable")} {
		client := fake.NewSimpleClientset()
		if failure != nil {
			client.PrependReactor("list", "nodes", func(ktesting.Action) (bool, runtime.Object, error) { return true, nil, failure })
		}
		runner := &KubernetesRunner{autoscalingMode: "disabled", clientset: client}
		if reason, message := runner.checkPodStartupFailure(context.Background(), schedulingPod()); reason != "" {
			t.Fatalf("%v: %s: %s", failure, reason, message)
		}
	}
}

func TestKubernetesSchedulingRecoversInBothWaitPaths(t *testing.T) {
	for _, completion := range []bool{false, true} {
		t.Run(fmt.Sprintf("completion=%t", completion), func(t *testing.T) {
			pod := schedulingPod()
			client := fake.NewSimpleClientset(schedulingNode("8", "16Gi"))
			polls := 0
			client.PrependReactor("list", "pods", func(ktesting.Action) (bool, runtime.Object, error) {
				polls++
				current := pod.DeepCopy()
				if polls > 1 {
					current.Status.Phase = corev1.PodRunning
					state := corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}
					if completion {
						state = corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 0}}
					}
					current.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "job", State: state}}
				}
				return true, &corev1.PodList{Items: []corev1.Pod{*current}}, nil
			})
			runner := &KubernetesRunner{autoscalingMode: "disabled", clientset: client, namespace: "test"}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if completion {
				code, err := runner.WaitForCompletion(ctx, "build")
				if err != nil || code != 0 {
					t.Fatalf("code=%d err=%v", code, err)
				}
			} else {
				name, err := runner.waitForPod(ctx, "build")
				if err != nil || name != pod.Name {
					t.Fatalf("name=%q err=%v", name, err)
				}
			}
			if polls < 2 {
				t.Fatal("did not wait for capacity")
			}
		})
	}
}

func TestKubernetesSchedulingTimeoutAndStatus(t *testing.T) {
	pod := schedulingPod()
	job := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: "build", Namespace: "test", CreationTimestamp: metav1.Now()}}
	client := fake.NewSimpleClientset(pod, job, schedulingNode("8", "16Gi"))
	runner := &KubernetesRunner{autoscalingMode: "disabled", clientset: client, namespace: "test", startupTimeout: time.Minute}
	status, reason, err := runner.GetJobStatus(context.Background(), "build")
	if err != nil || status != "pending" || reason != "" {
		t.Fatalf("%s %s %v", status, reason, err)
	}
	pod.CreationTimestamp = metav1.NewTime(time.Now().Add(-2 * time.Minute))
	if _, err := client.CoreV1().Pods("test").Update(context.Background(), pod, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	status, reason, err = runner.GetJobStatus(context.Background(), "build")
	if err != nil || status != "failed" || !strings.Contains(reason, "StartupTimeout") || !strings.Contains(reason, "Insufficient cpu") {
		t.Fatalf("%s %s %v", status, reason, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := runner.waitForPod(ctx, "build"); !IsPodStartupError(err) || !strings.Contains(err.Error(), "StartupTimeout") {
		t.Fatalf("waitForPod: %v", err)
	}
	if _, err := runner.WaitForCompletion(ctx, "build"); !IsPodStartupError(err) || !strings.Contains(err.Error(), "Insufficient memory") {
		t.Fatalf("WaitForCompletion: %v", err)
	}
	// Starting the main container ends the startup deadline.
	pod.Status.Phase = corev1.PodRunning
	pod.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "job", State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}}}
	if _, err := client.CoreV1().Pods("test").Update(context.Background(), pod, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	status, reason, err = runner.GetJobStatus(ctx, "build")
	if err != nil || status != "running" {
		t.Fatalf("%s %s %v", status, reason, err)
	}
}

func TestKubernetesStartupTimeoutWithoutPod(t *testing.T) {
	job := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: "build", Namespace: "test", CreationTimestamp: metav1.NewTime(time.Now().Add(-time.Hour))}}
	runner := &KubernetesRunner{autoscalingMode: "disabled", clientset: fake.NewSimpleClientset(job), namespace: "test"}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := runner.waitForPod(ctx, "build"); !IsPodStartupError(err) {
		t.Fatalf("waitForPod: %v", err)
	}
	if _, err := runner.WaitForCompletion(ctx, "build"); !IsPodStartupError(err) {
		t.Fatalf("got %v", err)
	}
	status, reason, err := runner.GetJobStatus(ctx, "build")
	if err != nil || status != "failed" || !strings.Contains(reason, "StartupTimeout") {
		t.Fatalf("%s %s %v", status, reason, err)
	}
}

func TestKubernetesSchedulingOversizedRequestFailsInAllPaths(t *testing.T) {
	pod := schedulingPod()
	job := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: "build", Namespace: "test", CreationTimestamp: metav1.Now()}}
	runner := &KubernetesRunner{autoscalingMode: "disabled", clientset: fake.NewSimpleClientset(pod, job, schedulingNode("2", "4Gi")), namespace: "test"}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := runner.waitForPod(ctx, "build"); !IsPodStartupError(err) || !strings.Contains(err.Error(), "ResourceRequestTooLarge") {
		t.Fatalf("waitForPod: %v", err)
	}
	if _, err := runner.WaitForCompletion(ctx, "build"); !IsPodStartupError(err) || !strings.Contains(err.Error(), "ResourceRequestTooLarge") {
		t.Fatalf("WaitForCompletion: %v", err)
	}
	status, reason, err := runner.GetJobStatus(ctx, "build")
	if err != nil || status != "failed" || !strings.Contains(reason, "ResourceRequestTooLarge") {
		t.Fatalf("%s %s %v", status, reason, err)
	}
}

func TestKubernetesStartupWaitHonorsJobDeadline(t *testing.T) {
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: "build", Namespace: "test", CreationTimestamp: metav1.Now()},
		Status:     batchv1.JobStatus{Conditions: []batchv1.JobCondition{{Type: batchv1.JobFailed, Status: corev1.ConditionTrue, Reason: "DeadlineExceeded", Message: "job deadline expired"}}},
	}
	runner := &KubernetesRunner{autoscalingMode: "disabled", clientset: fake.NewSimpleClientset(job), namespace: "test"}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := runner.waitForPod(ctx, "build"); !IsPodStartupError(err) || !strings.Contains(err.Error(), "DeadlineExceeded") {
		t.Fatalf("got %v", err)
	}
}

func TestKubernetesStartupContainerErrorsStillFail(t *testing.T) {
	for _, init := range []bool{false, true} {
		pod := schedulingPod()
		status := []corev1.ContainerStatus{{Name: "job", State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "ImagePullBackOff", Message: "missing image"}}}}
		if init {
			pod.Status.InitContainerStatuses = status
		} else {
			pod.Status.ContainerStatuses = status
		}
		runner := &KubernetesRunner{autoscalingMode: "disabled", clientset: fake.NewSimpleClientset()}
		if reason, _ := runner.checkPodStartupFailure(context.Background(), pod); reason != "ImagePullBackOff" {
			t.Fatalf("got %q", reason)
		}
	}
}

func TestKubernetesStartupTimeoutConfiguration(t *testing.T) {
	for _, value := range []string{"0", "-1m", "invalid"} {
		t.Setenv("REACTORCIDE_K8S_JOB_STARTUP_TIMEOUT", value)
		if _, err := NewKubernetesRunner(); err == nil || !strings.Contains(err.Error(), "must be a positive duration") {
			t.Fatalf("%q: %v", value, err)
		}
	}
	if _, err := NewKubernetesRunnerWithConfig(KubernetesRunnerConfig{StartupTimeout: -time.Minute}); err == nil || !strings.Contains(err.Error(), "must not be negative") {
		t.Fatalf("got %v", err)
	}
}
