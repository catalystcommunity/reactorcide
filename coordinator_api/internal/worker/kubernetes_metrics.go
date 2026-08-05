package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/jobtelemetry"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type podMetricsResponse struct {
	Timestamp  time.Time `json:"timestamp"`
	Containers []struct {
		Name  string            `json:"name"`
		Usage map[string]string `json:"usage"`
	} `json:"containers"`
}

type summaryResponse struct {
	Pods []struct {
		PodRef struct {
			Name      string `json:"name"`
			Namespace string `json:"namespace"`
		} `json:"podRef"`
		EphemeralStorage *summaryFS `json:"ephemeral-storage"`
		VolumeStats      []struct {
			Name    string    `json:"name"`
			FsStats summaryFS `json:"fsStats"`
		} `json:"volume"`
		Containers []struct {
			Name   string     `json:"name"`
			Rootfs *summaryFS `json:"rootfs"`
		} `json:"containers"`
	} `json:"pods"`
}

type summaryFS struct {
	AvailableBytes *uint64 `json:"availableBytes"`
	CapacityBytes  *uint64 `json:"capacityBytes"`
	UsedBytes      *uint64 `json:"usedBytes"`
}

func (kr *KubernetesRunner) SampleResources(ctx context.Context, jobName string) (ResourceSnapshot, error) {
	pods, err := kr.clientset.CoreV1().Pods(kr.namespace).List(ctx, metav1.ListOptions{
		LabelSelector: fmt.Sprintf("reactorcide.io/job-name=%s", jobName),
	})
	if err != nil {
		return ResourceSnapshot{}, fmt.Errorf("list job pods for metrics: %w", err)
	}
	if len(pods.Items) == 0 {
		return ResourceSnapshot{}, fmt.Errorf("job pod is not available")
	}
	pod := pods.Items[0]
	snapshot := ResourceSnapshot{ObservedAt: time.Now().UTC()}
	add := func(name, unit, kind string, value int64, labels ...jobtelemetry.Label) {
		id := int64(len(snapshot.Series))
		snapshot.Series = append(snapshot.Series, jobtelemetry.SeriesDefinition{SeriesID: id, Name: name, Unit: unit, Kind: kind, Labels: labels})
		snapshot.Values = append(snapshot.Values, jobtelemetry.Value{SeriesID: id, Value: value})
	}
	addKubernetesResourceSettings(add, &pod)
	restClient := kr.clientset.CoreV1().RESTClient()
	if restClient == nil {
		snapshot.Unavailable = append(snapshot.Unavailable,
			jobtelemetry.Unavailable{MetricPrefix: "cpu.usage", Reason: "metric_api_not_installed"},
			jobtelemetry.Unavailable{MetricPrefix: "memory.usage", Reason: "metric_api_not_installed"},
			jobtelemetry.Unavailable{MetricPrefix: "storage.used", Reason: "permission_denied"},
		)
		return snapshot, nil
	}
	metricData, metricErr := restClient.Get().AbsPath(
		"apis", "metrics.k8s.io", "v1beta1", "namespaces", kr.namespace, "pods", pod.Name,
	).DoRaw(ctx)
	if metricErr == nil {
		var metrics podMetricsResponse
		if json.Unmarshal(metricData, &metrics) == nil {
			if !metrics.Timestamp.IsZero() {
				snapshot.ObservedAt = metrics.Timestamp.UTC()
			}
			var totalCPU, totalMemory int64
			var hasCPU, hasMemory bool
			for _, containerMetric := range metrics.Containers {
				component := kubernetesMetricComponent(containerMetric.Name)
				labels := []jobtelemetry.Label{{Key: "scope", Value: "component"}, {Key: "component", Value: component}}
				if value, parseErr := resource.ParseQuantity(containerMetric.Usage["cpu"]); parseErr == nil {
					milli := value.MilliValue()
					add("cpu.utilization", "millicores", "gauge", milli, labels...)
					totalCPU += milli
					hasCPU = true
				}
				if value, parseErr := resource.ParseQuantity(containerMetric.Usage["memory"]); parseErr == nil {
					bytes := value.Value()
					add("memory.usage", "bytes", "gauge", bytes, labels...)
					totalMemory += bytes
					hasMemory = true
				}
			}
			if hasCPU {
				add("cpu.utilization", "millicores", "gauge", totalCPU, jobtelemetry.Label{Key: "scope", Value: "job"}, jobtelemetry.Label{Key: "cpu", Value: "total"})
			}
			if hasMemory {
				add("memory.usage", "bytes", "gauge", totalMemory, jobtelemetry.Label{Key: "scope", Value: "job"})
			}
		}
	} else {
		snapshot.Unavailable = append(snapshot.Unavailable,
			jobtelemetry.Unavailable{MetricPrefix: "cpu.usage", Reason: "metric_api_not_installed"},
			jobtelemetry.Unavailable{MetricPrefix: "memory.usage", Reason: "metric_api_not_installed"},
		)
	}
	if pod.Spec.NodeName == "" {
		snapshot.Unavailable = append(snapshot.Unavailable, jobtelemetry.Unavailable{MetricPrefix: "storage.used", Reason: "not_applicable"})
		return snapshot, nil
	}
	summaryData, summaryErr := restClient.Get().AbsPath("api", "v1", "nodes", pod.Spec.NodeName, "proxy", "stats", "summary").DoRaw(ctx)
	if summaryErr != nil {
		snapshot.Unavailable = append(snapshot.Unavailable, jobtelemetry.Unavailable{MetricPrefix: "storage.used", Reason: "permission_denied"})
		return snapshot, nil
	}
	var summary summaryResponse
	if json.Unmarshal(summaryData, &summary) != nil {
		snapshot.Unavailable = append(snapshot.Unavailable, jobtelemetry.Unavailable{MetricPrefix: "storage.used", Reason: "runtime_not_supported"})
		return snapshot, nil
	}
	for _, podSummary := range summary.Pods {
		if podSummary.PodRef.Name != pod.Name || podSummary.PodRef.Namespace != kr.namespace {
			continue
		}
		// Kubelet charges this used value to the Pod. The capacity and available
		// values describe the node filesystem, so they are not job metrics.
		addSummaryFS(add, podSummary.EphemeralStorage, kubernetesStorageMetric{
			scope: "job", volume: "total", kind: "ephemeral", includeCapacity: false,
		})
		volumes := kubernetesStorageVolumes(&pod)
		for _, volume := range podSummary.VolumeStats {
			metric, ok := volumes[volume.Name]
			if !ok {
				// Do not report shared PVC usage as job usage. The kubelet value is
				// for the complete claim and can include data from other jobs.
				continue
			}
			fs := volume.FsStats
			addSummaryFS(add, &fs, metric)
		}
		for _, containerSummary := range podSummary.Containers {
			addSummaryFS(add, containerSummary.Rootfs, kubernetesStorageMetric{
				scope: "component", component: kubernetesMetricComponent(containerSummary.Name),
				volume: "rootfs-" + containerSummary.Name, kind: "rootfs", includeCapacity: false,
			})
		}
		break
	}
	return snapshot, nil
}

func kubernetesMetricComponent(name string) string {
	switch name {
	case "job":
		return "main"
	case "buildkitd":
		return "builder"
	case "builder":
		return "builder"
	case "dind":
		return "docker"
	case "docker-daemon":
		return "docker"
	default:
		return name
	}
}

func addKubernetesResourceSettings(add func(string, string, string, int64, ...jobtelemetry.Label), pod *corev1.Pod) {
	var totalCPURequest, totalCPULimit, totalMemoryLimit int64
	containers := append([]corev1.Container{}, pod.Spec.Containers...)
	for _, container := range pod.Spec.InitContainers {
		if container.RestartPolicy != nil && *container.RestartPolicy == corev1.ContainerRestartPolicyAlways {
			containers = append(containers, container)
		}
	}
	for _, container := range containers {
		labels := []jobtelemetry.Label{
			{Key: "scope", Value: "component"},
			{Key: "component", Value: kubernetesMetricComponent(container.Name)},
		}
		if value, ok := container.Resources.Requests[corev1.ResourceCPU]; ok {
			millicores := value.MilliValue()
			add("cpu.request", "millicores", "gauge", millicores, labels...)
			totalCPURequest += millicores
		}
		if value, ok := container.Resources.Limits[corev1.ResourceCPU]; ok {
			millicores := value.MilliValue()
			add("cpu.limit", "millicores", "gauge", millicores, labels...)
			totalCPULimit += millicores
		}
		if value, ok := container.Resources.Limits[corev1.ResourceMemory]; ok {
			bytes := value.Value()
			add("memory.limit", "bytes", "gauge", bytes, labels...)
			totalMemoryLimit += bytes
		}
	}
	jobLabels := []jobtelemetry.Label{{Key: "scope", Value: "job"}}
	if totalCPURequest > 0 {
		add("cpu.request", "millicores", "gauge", totalCPURequest, jobLabels...)
	}
	if totalCPULimit > 0 {
		add("cpu.limit", "millicores", "gauge", totalCPULimit, jobLabels...)
	}
	if totalMemoryLimit > 0 {
		add("memory.limit", "bytes", "gauge", totalMemoryLimit, jobLabels...)
	}
}

type kubernetesStorageMetric struct {
	scope           string
	component       string
	volume          string
	kind            string
	mount           string
	includeCapacity bool
}

func kubernetesStorageVolumes(pod *corev1.Pod) map[string]kubernetesStorageMetric {
	mounts := make(map[string]string)
	containers := append([]corev1.Container{}, pod.Spec.Containers...)
	containers = append(containers, pod.Spec.InitContainers...)
	for _, container := range containers {
		for _, mount := range container.VolumeMounts {
			if _, exists := mounts[mount.Name]; !exists {
				mounts[mount.Name] = mount.MountPath
			}
		}
	}
	result := make(map[string]kubernetesStorageMetric)
	for _, volume := range pod.Spec.Volumes {
		metric := kubernetesStorageMetric{scope: "job", volume: volume.Name, mount: mounts[volume.Name]}
		switch {
		case volume.EmptyDir != nil:
			metric.kind = "ephemeral"
		case volume.Ephemeral != nil:
			metric.kind = "ephemeral-pvc"
			metric.includeCapacity = true
		default:
			// A normal PVC can be shared by concurrent jobs. Its usage is not
			// attributable to this Pod. Ignore all other projected volumes too.
			continue
		}
		result[volume.Name] = metric
	}
	return result
}

func addSummaryFS(add func(string, string, string, int64, ...jobtelemetry.Label), fs *summaryFS, metric kubernetesStorageMetric) {
	if fs == nil {
		return
	}
	labels := []jobtelemetry.Label{{Key: "scope", Value: metric.scope}, {Key: "volume", Value: metric.volume}, {Key: "kind", Value: metric.kind}}
	if metric.component != "" {
		labels = append(labels, jobtelemetry.Label{Key: "component", Value: metric.component})
	}
	if metric.mount != "" {
		labels = append(labels, jobtelemetry.Label{Key: "mount", Value: metric.mount})
	}
	if fs.UsedBytes != nil {
		add("storage.used", "bytes", "gauge", int64(*fs.UsedBytes), labels...)
	}
	if metric.includeCapacity && fs.CapacityBytes != nil {
		add("storage.capacity", "bytes", "gauge", int64(*fs.CapacityBytes), labels...)
	}
	if metric.includeCapacity && fs.AvailableBytes != nil {
		add("storage.available", "bytes", "gauge", int64(*fs.AvailableBytes), labels...)
	}
}
