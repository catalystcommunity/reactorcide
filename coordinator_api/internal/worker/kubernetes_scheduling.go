package worker

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	podresource "k8s.io/component-helpers/resource"
	"k8s.io/component-helpers/scheduling/corev1/nodeaffinity"
)

const defaultKubernetesStartupTimeout = 30 * time.Minute

func (kr *KubernetesRunner) podStartupTimeout() time.Duration {
	if kr.startupTimeout > 0 {
		return kr.startupTimeout
	}
	return defaultKubernetesStartupTimeout
}

func (kr *KubernetesRunner) startupExpired(created metav1.Time) bool {
	return !created.IsZero() && time.Since(created.Time) >= kr.podStartupTimeout()
}

func (kr *KubernetesRunner) startupTimeoutMessage(pod *corev1.Pod) string {
	message := fmt.Sprintf("main container did not start within %s", kr.podStartupTimeout())
	if pod != nil {
		for _, condition := range pod.Status.Conditions {
			if condition.Type == corev1.PodScheduled && condition.Status == corev1.ConditionFalse {
				return message + ": " + condition.Reason + ": " + condition.Message
			}
		}
	}
	return message
}

// Scheduling conditions describe current availability, not permanent failures.
// Reject only CPU or memory requests that exceed every matching node's total
// allocatable resources. Never subtract the requests of other running pods.
func (kr *KubernetesRunner) checkPodSchedulingFailure(ctx context.Context, pod *corev1.Pod) (string, string) {
	unschedulable := false
	for _, condition := range pod.Status.Conditions {
		if condition.Type == corev1.PodScheduled && condition.Status == corev1.ConditionFalse && condition.Reason == "Unschedulable" {
			unschedulable = true
			break
		}
	}
	if !unschedulable {
		return "", ""
	}
	evidence := kr.schedulingEvidence(ctx, pod)
	if evidence.scaleUp || !evidence.noScaleUp {
		return "", ""
	}
	if evidence.blocked {
		return "SchedulingBlocked", evidence.message
	}

	// Read the admitted Pod. Kubernetes has already applied request defaults
	// and any injected sidecars. Its helper includes init containers, native
	// sidecars, pod-level requests, and runtime overhead.
	requests := podresource.PodRequests(pod, podresource.PodResourcesOptions{})
	cpu, memory := requests[corev1.ResourceCPU], requests[corev1.ResourceMemory]
	if cpu.IsZero() && memory.IsZero() {
		return "", ""
	}
	nodeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	nodes, err := kr.clientset.CoreV1().Nodes().List(nodeCtx, metav1.ListOptions{})
	if err != nil || len(nodes.Items) == 0 {
		// Missing RBAC, an API outage, or an empty cluster cannot prove that
		// this request is too large. Keep waiting under the startup deadline.
		return "", ""
	}
	affinity := nodeaffinity.GetRequiredNodeAffinity(pod)
	matched := 0
	for i := range nodes.Items {
		node := &nodes.Items[i]
		if pod.Spec.NodeName != "" && pod.Spec.NodeName != node.Name {
			continue
		}
		matches, err := affinity.Match(node)
		if err != nil {
			return "", ""
		}
		if !matches {
			continue
		}
		matched++
		tooSmall := false
		for _, name := range []corev1.ResourceName{corev1.ResourceCPU, corev1.ResourceMemory} {
			requested := requests[name]
			allocatable, known := node.Status.Allocatable[name]
			if known && requested.Cmp(allocatable) > 0 {
				tooSmall = true
			}
		}
		// Readiness, cordons, taints, and other workloads can change. A node
		// with enough capacity is sufficient reason to wait. Unknown capacity
		// also prevents an early failure.
		if !tooSmall {
			return "", ""
		}
	}
	if matched == 0 {
		return "", ""
	}
	return "ResourceRequestTooLarge", fmt.Sprintf("pod requests cpu=%s memory=%s; no current node matching its node selector and required node affinity has enough allocatable CPU and memory", cpu.String(), memory.String())
}
