package worker

import (
	"context"
	"regexp"
	"strconv"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
)

const schedulingEventMaxAge = 2 * time.Minute
const schedulingDecisionGrace = 30 * time.Second

type schedulingEvidence struct {
	scaleUp   bool
	noScaleUp bool
	blocked   bool
	message   string
}

// Events are evidence for this Pod UID, never for a previous Pod with the same
// name. An absent event, an unknown reason, or a failed read proves nothing.
func (kr *KubernetesRunner) schedulingEvidence(ctx context.Context, pod *corev1.Pod) schedulingEvidence {
	result := schedulingEvidence{noScaleUp: kr.autoscalingMode == "disabled"}
	if pod.UID == "" {
		return result
	}
	readCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	events, err := kr.clientset.CoreV1().Events(kr.namespace).List(readCtx, metav1.ListOptions{
		FieldSelector: fields.OneTermEqualSelector("involvedObject.uid", string(pod.UID)).String(),
	})
	if err != nil {
		return result
	}
	now := time.Now()
	var failed, refused *corev1.Event
	var failedAt, refusedAt time.Time
	scheduler := pod.Spec.SchedulerName
	if scheduler == "" {
		scheduler = "default-scheduler"
	}
	for i := range events.Items {
		event := &events.Items[i]
		if event.InvolvedObject.UID != pod.UID || event.InvolvedObject.Kind != "Pod" || event.InvolvedObject.Name != pod.Name || event.InvolvedObject.Namespace != pod.Namespace {
			continue
		}
		at := schedulerEventTime(event)
		if at.IsZero() || at.Before(pod.CreationTimestamp.Time) || at.After(now.Add(5*time.Second)) {
			continue
		}
		source := event.ReportingController
		if source == "" {
			source = event.Source.Component
		}
		if source == "cluster-autoscaler" && event.Reason == "TriggeredScaleUp" {
			result.scaleUp = true
		}
		if now.Sub(at) > schedulingEventMaxAge {
			continue
		}
		if source == "cluster-autoscaler" {
			if event.Reason == "NotTriggerScaleUp" && (refused == nil || at.After(refusedAt)) {
				refused, refusedAt = event, at
			}
		}
		if source == scheduler && event.Reason == "FailedScheduling" && (failed == nil || at.After(failedAt)) {
			failed, failedAt = event, at
		}
	}
	// A recent positive scale-up decision takes precedence over a refusal.
	if result.scaleUp {
		return result
	}
	if refused != nil && definiteScaleUpRefusal(refused.Message) && now.Sub(pod.CreationTimestamp.Time) >= schedulingDecisionGrace {
		result.noScaleUp = true
	}
	if !result.noScaleUp || failed == nil || now.Sub(pod.CreationTimestamp.Time) < schedulingDecisionGrace {
		return result
	}
	// The current condition must agree with the event. This rejects a stale
	// failure event after the scheduler changes its diagnosis.
	current := ""
	for _, condition := range pod.Status.Conditions {
		if condition.Type == corev1.PodScheduled && condition.Status == corev1.ConditionFalse && condition.Reason == "Unschedulable" {
			current = condition.Message
		}
	}
	if current != failed.Message || !onlyPlacementBlockers(failed.Message) {
		return result
	}
	if kr.autoscalingMode != "disabled" && (refused == nil || refusedAt.Before(failedAt)) {
		return result
	}
	result.blocked = true
	result.message = "scheduler cannot place the pod: " + failed.Message
	if refused != nil {
		result.message += "; autoscaler: " + refused.Message
	} else {
		result.message += "; node autoscaling is disabled by the worker configuration"
	}
	return result
}

func schedulerEventTime(event *corev1.Event) time.Time {
	at := event.CreationTimestamp.Time
	for _, candidate := range []time.Time{event.EventTime.Time, event.LastTimestamp.Time} {
		if candidate.After(at) {
			at = candidate
		}
	}
	if event.Series != nil && event.Series.LastObservedTime.After(at) {
		at = event.Series.LastObservedTime.Time
	}
	return at
}

var schedulingSummary = regexp.MustCompile(`^0/([0-9]+) nodes are available: (.+)`)
var countedSchedulingReason = regexp.MustCompile(`^([0-9]+) (.+)$`)

// Require a complete diagnosis: every node must have a known placement
// blocker. In particular, mixed CPU/memory contention must keep waiting.
func onlyPlacementBlockers(message string) bool {
	parts := schedulingSummary.FindStringSubmatch(message)
	if parts == nil {
		return false
	}
	total, _ := strconv.Atoi(parts[1])
	reasons := strings.SplitN(parts[2], ". preemption:", 2)[0]
	count := 0
	for _, part := range strings.Split(strings.TrimSuffix(reasons, "."), ", ") {
		reason := countedSchedulingReason.FindStringSubmatch(part)
		if reason == nil || !placementBlocker(reason[2]) {
			return false
		}
		n, _ := strconv.Atoi(reason[1])
		count += n
	}
	return total > 0 && count >= total
}

func placementBlocker(reason string) bool {
	lower := strings.ToLower(reason)
	switch lower {
	case "node(s) didn't match pod's node affinity/selector", "node(s) didn't match node selector", "node(s) didn't match node affinity", "node(s) had volume node affinity conflict":
		return true
	}
	// Kubernetes pressure and readiness taints can clear without intervention.
	return strings.HasPrefix(lower, "node(s) had untolerated taint {") && !strings.Contains(lower, "node.kubernetes.io/")
}

func definiteScaleUpRefusal(message string) bool {
	lower := strings.ToLower(message)
	// The event reason also covers temporary failures and backoff.
	for _, transient := range []string{"backoff", "back-off", "in progress", "upcoming", "unready", "not ready", "timeout", "quota", "node.kubernetes.io/"} {
		if strings.Contains(lower, transient) {
			return false
		}
	}
	if lower == "pod didn't trigger scale-up (it wouldn't fit if a new node is added)" {
		return true
	}
	_, reasons, ok := strings.Cut(lower, ": ")
	if !ok || !strings.HasPrefix(lower, "pod didn't trigger scale-up") {
		return false
	}
	for _, part := range strings.Split(reasons, ", ") {
		counted := countedSchedulingReason.FindStringSubmatch(part)
		if counted == nil {
			return false
		}
		reason := strings.TrimSuffix(counted[2], ".")
		if !placementBlocker(reason) && reason != "max node group size reached" && reason != "insufficient cpu" && reason != "insufficient memory" {
			return false
		}
	}
	return true
}

// ExecutionTiming uses the main container, not Pod phase or sidecar startup.
func (kr *KubernetesRunner) ExecutionTiming(ctx context.Context, jobName string) (ExecutionTiming, error) {
	pods, err := kr.clientset.CoreV1().Pods(kr.namespace).List(ctx, metav1.ListOptions{LabelSelector: "reactorcide.io/job-name=" + jobName})
	if err != nil {
		return ExecutionTiming{}, err
	}
	for _, pod := range pods.Items {
		for _, status := range pod.Status.ContainerStatuses {
			if status.Name != "job" {
				continue
			}
			if status.State.Terminated != nil {
				return ExecutionTiming{StartedAt: status.State.Terminated.StartedAt.Time, FinishedAt: status.State.Terminated.FinishedAt.Time}, nil
			}
			if status.State.Running != nil {
				return ExecutionTiming{StartedAt: status.State.Running.StartedAt.Time}, nil
			}
		}
	}
	return ExecutionTiming{}, nil
}
