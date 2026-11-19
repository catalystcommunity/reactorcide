package worker

import (
	"context"
	"fmt"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/admission"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	podresource "k8s.io/component-helpers/resource"
)

// CapacitySnapshot is the resource view of a runner backend for resource
// admission (see internal/admission).
type CapacitySnapshot struct {
	// Capacity is the total CPU and memory that jobs can use.
	Capacity admission.Resources
	// Used is the resources that running Reactorcide jobs request now.
	Used admission.Resources
	// CountedJobIDs are the Reactorcide job IDs whose requests are in Used.
	// The worker adds its own leases that are not in this set yet (a lease
	// whose Pod is not created yet), so that it does not count a job twice
	// or miss one.
	CountedJobIDs map[string]bool
}

// CapacityReporter is a JobRunner that can measure its own capacity. The
// Kubernetes runner implements it. Other backends use the host size or a
// configured budget (see internal/coordinatorworker).
type CapacityReporter interface {
	ResourceCapacity(ctx context.Context) (CapacitySnapshot, error)
}

// TopologyDetector is a JobRunner that can detect the standard region and
// zone characteristics of the place where its jobs run.
type TopologyDetector interface {
	DetectTopology(ctx context.Context) (map[string]string, error)
}

// Standard Kubernetes topology labels.
const (
	topologyRegionLabel = "topology.kubernetes.io/region"
	topologyZoneLabel   = "topology.kubernetes.io/zone"
)

// capacityListTimeout bounds each list call of a capacity measurement.
const capacityListTimeout = 10 * time.Second

// eligibleCapacityNode reports whether a node counts toward capacity: it
// matches the capacity node selector, it is Ready, it is not cordoned, and
// it has no NoSchedule or NoExecute taint. A job Pod has no tolerations, so a
// tainted node cannot run it.
func (kr *KubernetesRunner) eligibleCapacityNode(node *corev1.Node) bool {
	if kr.capacityNodeSelector != nil && !kr.capacityNodeSelector.Empty() && !kr.capacityNodeSelector.Matches(labels.Set(node.Labels)) {
		return false
	}
	if node.Spec.Unschedulable {
		return false
	}
	for _, taint := range node.Spec.Taints {
		if taint.Effect == corev1.TaintEffectNoSchedule || taint.Effect == corev1.TaintEffectNoExecute {
			return false
		}
	}
	for _, condition := range node.Status.Conditions {
		if condition.Type == corev1.NodeReady {
			return condition.Status == corev1.ConditionTrue
		}
	}
	return false
}

func (kr *KubernetesRunner) listCapacityNodes(ctx context.Context) ([]corev1.Node, error) {
	listCtx, cancel := context.WithTimeout(ctx, capacityListTimeout)
	defer cancel()
	options := metav1.ListOptions{}
	if kr.capacityNodeSelector != nil && !kr.capacityNodeSelector.Empty() {
		options.LabelSelector = kr.capacityNodeSelector.String()
	}
	nodes, err := kr.clientset.CoreV1().Nodes().List(listCtx, options)
	if err != nil {
		return nil, fmt.Errorf("list nodes: %w", err)
	}
	out := make([]corev1.Node, 0, len(nodes.Items))
	for i := range nodes.Items {
		if kr.eligibleCapacityNode(&nodes.Items[i]) {
			out = append(out, nodes.Items[i])
		}
	}
	return out, nil
}

// ResourceCapacity returns the sum of allocatable CPU and memory of the
// eligible nodes, and the requests of the Reactorcide job Pods in the job
// namespace that are not finished.
//
// Capacity is the current node total. A node autoscaler adds nodes only when
// Pods wait, so with node autoscaling set a configured budget on the worker
// (REACTORCIDE_WORKER_ADMISSION_CPU and REACTORCIDE_WORKER_ADMISSION_MEMORY)
// that is the largest size of the node pool. The worker then ignores this
// capacity value and uses only Used.
//
// Used counts every Reactorcide job Pod in the namespace, also the Pods of
// other worker replicas, so many replicas in one cluster share one budget.
// It does not count other workloads on the same nodes; give jobs a dedicated
// node pool (and REACTORCIDE_K8S_CAPACITY_NODE_SELECTOR) or a budget.
func (kr *KubernetesRunner) ResourceCapacity(ctx context.Context) (CapacitySnapshot, error) {
	nodes, err := kr.listCapacityNodes(ctx)
	if err != nil {
		return CapacitySnapshot{}, err
	}
	var snapshot CapacitySnapshot
	for i := range nodes {
		allocatable := nodes[i].Status.Allocatable
		if cpu, ok := allocatable[corev1.ResourceCPU]; ok {
			snapshot.Capacity.CPUMillicores += cpu.MilliValue()
		}
		if memory, ok := allocatable[corev1.ResourceMemory]; ok {
			snapshot.Capacity.MemoryBytes += memory.Value()
		}
	}

	listCtx, cancel := context.WithTimeout(ctx, capacityListTimeout)
	defer cancel()
	pods, err := kr.clientset.CoreV1().Pods(kr.namespace).List(listCtx, metav1.ListOptions{LabelSelector: "reactorcide.io/job-id"})
	if err != nil {
		return CapacitySnapshot{}, fmt.Errorf("list job pods: %w", err)
	}
	snapshot.CountedJobIDs = make(map[string]bool, len(pods.Items))
	for i := range pods.Items {
		pod := &pods.Items[i]
		if pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed || pod.DeletionTimestamp != nil {
			continue
		}
		snapshot.Used = snapshot.Used.Add(podAdmissionNeed(pod))
		if jobID := pod.Labels["reactorcide.io/job-id"]; jobID != "" {
			snapshot.CountedJobIDs[jobID] = true
		}
	}
	return snapshot, nil
}

// podAdmissionNeed returns the CPU and memory requests of a Pod as the
// scheduler counts them: containers, init containers, native sidecars,
// pod-level requests, and overhead.
func podAdmissionNeed(pod *corev1.Pod) admission.Resources {
	requests := podresource.PodRequests(pod, podresource.PodResourcesOptions{})
	cpu, memory := requests[corev1.ResourceCPU], requests[corev1.ResourceMemory]
	return admission.Resources{CPUMillicores: cpu.MilliValue(), MemoryBytes: memory.Value()}
}

// DetectTopology returns the standard "region" and "zone" characteristics
// from the topology.kubernetes.io labels of the eligible nodes. A value is
// returned only when every eligible node has the label with the same value.
// When the nodes span many zones, the worker advertises no zone: a job that
// requires one zone could otherwise land on a node in another zone, because
// the runner does not pin a job Pod to a zone.
func (kr *KubernetesRunner) DetectTopology(ctx context.Context) (map[string]string, error) {
	nodes, err := kr.listCapacityNodes(ctx)
	if err != nil {
		return nil, err
	}
	return uniformTopology(nodes), nil
}

func uniformTopology(nodes []corev1.Node) map[string]string {
	out := map[string]string{}
	if len(nodes) == 0 {
		return out
	}
	for key, label := range map[string]string{"region": topologyRegionLabel, "zone": topologyZoneLabel} {
		value := nodes[0].Labels[label]
		uniform := value != ""
		for i := 1; uniform && i < len(nodes); i++ {
			uniform = nodes[i].Labels[label] == value
		}
		if uniform {
			out[key] = value
		}
	}
	return out
}
