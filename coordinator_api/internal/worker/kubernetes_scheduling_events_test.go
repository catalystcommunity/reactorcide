package worker

import (
	"context"
	"fmt"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

const affinityFailure = "0/1 nodes are available: 1 node(s) didn't match Pod's node affinity/selector. preemption: 0/1 nodes are available: 1 Preemption is not helpful for scheduling."

func podSchedulingEvent(pod *corev1.Pod, source, reason, message string, at time.Time) *corev1.Event {
	return &corev1.Event{
		ObjectMeta:     metav1.ObjectMeta{Name: reason, Namespace: pod.Namespace, CreationTimestamp: metav1.NewTime(at)},
		InvolvedObject: corev1.ObjectReference{Kind: "Pod", Namespace: pod.Namespace, Name: pod.Name, UID: pod.UID},
		Source:         corev1.EventSource{Component: source}, Reason: reason, Message: message, LastTimestamp: metav1.NewTime(at),
	}
}

func TestKubernetesSchedulingEventDecisions(t *testing.T) {
	for _, tc := range []struct {
		name, mode, schedulerMessage, refusal, want string
		alter                                       func(*corev1.Pod, []*corev1.Event) []*corev1.Event
		unreadable                                  bool
	}{
		{name: "fixed cluster placement block", mode: "disabled", want: "SchedulingBlocked"},
		{name: "autoscaler refuses placement", refusal: "pod didn't trigger scale-up: 1 node(s) didn't match Pod's node affinity/selector", want: "SchedulingBlocked"},
		{name: "unknown autoscaler waits"},
		{name: "max size with placement failure", refusal: "pod didn't trigger scale-up: 1 max node group size reached", want: "SchedulingBlocked"},
		{name: "max size with CPU contention waits", refusal: "pod didn't trigger scale-up: 1 max node group size reached", schedulerMessage: "0/1 nodes are available: 1 Insufficient cpu"},
		{name: "mixed contention waits", mode: "disabled", schedulerMessage: "0/2 nodes are available: 1 Insufficient memory, 1 node(s) didn't match Pod's node affinity/selector."},
		{name: "temporary autoscaler backoff waits", refusal: "pod didn't trigger scale-up: 1 in backoff after failed scale-up, 1 node(s) didn't match node selector"},
		{name: "volume binding waits", mode: "disabled", schedulerMessage: "0/1 nodes are available: 1 pod has unbound immediate PersistentVolumeClaims."},
		{name: "readiness taint waits", mode: "disabled", schedulerMessage: "0/1 nodes are available: 1 node(s) had untolerated taint {node.kubernetes.io/not-ready: }."},
		{name: "dedicated taint blocks", mode: "disabled", schedulerMessage: "0/1 nodes are available: 1 node(s) had untolerated taint {dedicated: system}.", want: "SchedulingBlocked"},
		{name: "volume affinity blocks", mode: "disabled", schedulerMessage: "0/1 nodes are available: 1 node(s) had volume node affinity conflict.", want: "SchedulingBlocked"},
		{name: "stale event waits", mode: "disabled", alter: func(p *corev1.Pod, e []*corev1.Event) []*corev1.Event {
			e[0].CreationTimestamp = metav1.NewTime(time.Now().Add(-10 * time.Minute))
			e[0].LastTimestamp = e[0].CreationTimestamp
			return e
		}},
		{name: "wrong pod UID waits", mode: "disabled", alter: func(p *corev1.Pod, e []*corev1.Event) []*corev1.Event { e[0].InvolvedObject.UID = "old-pod"; return e }},
		{name: "wrong scheduler waits", mode: "disabled", alter: func(p *corev1.Pod, e []*corev1.Event) []*corev1.Event {
			e[0].Source.Component = "unrelated-controller"
			return e
		}},
		{name: "changed diagnosis waits", mode: "disabled", alter: func(p *corev1.Pod, e []*corev1.Event) []*corev1.Event {
			p.Status.Conditions[0].Message = "0/1 nodes are available: 1 Insufficient cpu"
			return e
		}},
		{name: "startup grace waits", mode: "disabled", alter: func(p *corev1.Pod, e []*corev1.Event) []*corev1.Event { p.CreationTimestamp = metav1.Now(); return e }},
		{name: "events forbidden waits", mode: "disabled", unreadable: true},
		{name: "scale up overrides refusal", refusal: "pod didn't trigger scale-up: 1 max node group size reached", alter: func(p *corev1.Pod, e []*corev1.Event) []*corev1.Event {
			return append(e, podSchedulingEvent(p, "cluster-autoscaler", "TriggeredScaleUp", "new node requested", time.Now().Add(-10*time.Second)))
		}},
		{name: "scale up can take more than two minutes", refusal: "pod didn't trigger scale-up: 1 max node group size reached", alter: func(p *corev1.Pod, e []*corev1.Event) []*corev1.Event {
			p.CreationTimestamp = metav1.NewTime(time.Now().Add(-10 * time.Minute))
			return append(e, podSchedulingEvent(p, "cluster-autoscaler", "TriggeredScaleUp", "new node requested", time.Now().Add(-5*time.Minute)))
		}},
		{name: "refusal before diagnosis waits", refusal: "pod didn't trigger scale-up: 1 max node group size reached", alter: func(p *corev1.Pod, e []*corev1.Event) []*corev1.Event {
			e[1].CreationTimestamp = metav1.NewTime(time.Now().Add(-40 * time.Second))
			e[1].LastTimestamp = e[1].CreationTimestamp
			return e
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pod := schedulingPod()
			pod.UID = "pod-uid"
			pod.CreationTimestamp = metav1.NewTime(time.Now().Add(-time.Minute))
			message := tc.schedulerMessage
			if message == "" {
				message = affinityFailure
			}
			pod.Status.Conditions[0].Message = message
			events := []*corev1.Event{podSchedulingEvent(pod, "default-scheduler", "FailedScheduling", message, time.Now().Add(-20*time.Second))}
			if tc.refusal != "" {
				events = append(events, podSchedulingEvent(pod, "cluster-autoscaler", "NotTriggerScaleUp", tc.refusal, time.Now().Add(-10*time.Second)))
			}
			if tc.alter != nil {
				events = tc.alter(pod, events)
			}
			objects := []runtime.Object{schedulingNode("8", "16Gi")}
			for _, e := range events {
				objects = append(objects, e)
			}
			client := fake.NewSimpleClientset(objects...)
			if tc.unreadable {
				client.PrependReactor("list", "events", func(ktesting.Action) (bool, runtime.Object, error) { return true, nil, fmt.Errorf("forbidden") })
			}
			runner := &KubernetesRunner{clientset: client, namespace: pod.Namespace, autoscalingMode: tc.mode}
			reason, message := runner.checkPodStartupFailure(context.Background(), pod)
			if reason != tc.want {
				t.Fatalf("got %q: %s; want %q", reason, message, tc.want)
			}
		})
	}
}

func TestKubernetesExecutionTimingUsesMainContainer(t *testing.T) {
	started, finished := time.Now().Add(-20*time.Second), time.Now().Add(-5*time.Second)
	pod := schedulingPod()
	pod.Status.Phase = corev1.PodRunning
	pod.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "sidecar", State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: metav1.NewTime(started.Add(-time.Minute))}}}}
	client := fake.NewSimpleClientset(pod)
	runner := &KubernetesRunner{clientset: client, namespace: pod.Namespace}
	got, err := runner.ExecutionTiming(context.Background(), "build")
	if err != nil || !got.StartedAt.IsZero() {
		t.Fatalf("sidecar counted as job: %+v %v", got, err)
	}
	pod.Status.ContainerStatuses = append(pod.Status.ContainerStatuses, corev1.ContainerStatus{Name: "job", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{StartedAt: metav1.NewTime(started), FinishedAt: metav1.NewTime(finished)}}})
	if _, err := client.CoreV1().Pods(pod.Namespace).Update(context.Background(), pod, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	got, err = runner.ExecutionTiming(context.Background(), "build")
	if err != nil || !got.StartedAt.Equal(started) || !got.FinishedAt.Equal(finished) {
		t.Fatalf("got %+v %v", got, err)
	}
}
