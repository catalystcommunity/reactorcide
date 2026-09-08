package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/jobtelemetry"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
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

// Unavailable prefixes the Kubernetes collector reports. They name the series
// the collector would have emitted: cpu.utilization, not cpu.usage. The
// query layer treats the two as one family for telemetry stored before this
// was corrected.
const (
	kubernetesCPUPrefix     = "cpu.utilization"
	kubernetesMemoryPrefix  = "memory.usage"
	kubernetesStoragePrefix = "storage.used"
)

// metricsAPIGroupVersion is the resource-metrics API served by metrics-server.
const metricsAPIGroupVersion = "metrics.k8s.io/v1beta1"

// errNoRESTClient is what the REST-backed source returns when the clientset
// has no REST client at all (the fake clientset in tests).
var errNoRESTClient = errors.New("kubernetes rest client is not available")

// kubernetesMetricsSource is the narrow read surface SampleResources uses,
// so tests can hand it Kubernetes status errors without a live API server.
// The production implementation is restMetricsSource.
type kubernetesMetricsSource interface {
	// PodMetrics reads the metrics.k8s.io PodMetrics object for one pod.
	PodMetrics(ctx context.Context, namespace, pod string) ([]byte, error)
	// NodeSummary reads the kubelet stats summary through the node proxy.
	NodeSummary(ctx context.Context, node string) ([]byte, error)
	// MetricsAPIRegistered reports whether metrics.k8s.io/v1beta1 is served
	// by this API server, using discovery. A 404 on one PodMetrics object
	// cannot tell "no metrics-server" from "no sample for this pod yet";
	// discovery can.
	MetricsAPIRegistered(ctx context.Context) (bool, error)
}

type restMetricsSource struct {
	clientset kubernetes.Interface
}

func (s restMetricsSource) restClient() (rest.Interface, error) {
	client := s.clientset.CoreV1().RESTClient()
	// The fake clientset returns a typed nil *rest.RESTClient, which is a
	// non-nil interface value. Check the concrete pointer.
	if typed, ok := client.(*rest.RESTClient); client == nil || (ok && typed == nil) {
		return nil, errNoRESTClient
	}
	return client, nil
}

func (s restMetricsSource) PodMetrics(ctx context.Context, namespace, pod string) ([]byte, error) {
	client, err := s.restClient()
	if err != nil {
		return nil, err
	}
	return client.Get().AbsPath("apis", "metrics.k8s.io", "v1beta1", "namespaces", namespace, "pods", pod).DoRaw(ctx)
}

func (s restMetricsSource) NodeSummary(ctx context.Context, node string) ([]byte, error) {
	client, err := s.restClient()
	if err != nil {
		return nil, err
	}
	return client.Get().AbsPath("api", "v1", "nodes", node, "proxy", "stats", "summary").DoRaw(ctx)
}

func (s restMetricsSource) MetricsAPIRegistered(ctx context.Context) (bool, error) {
	_, err := s.clientset.Discovery().ServerResourcesForGroupVersion(metricsAPIGroupVersion)
	if err == nil {
		return true, nil
	}
	if apierrors.IsNotFound(err) {
		return false, nil
	}
	return false, err
}

// metricsSourceFor returns the injected source or the REST-backed default.
func (kr *KubernetesRunner) metricsSourceFor() kubernetesMetricsSource {
	if kr.metricsSource != nil {
		return kr.metricsSource
	}
	return restMetricsSource{clientset: kr.clientset}
}

// classifyKubernetesError maps a failed read to a safe availability reason.
//
// Only an authorization failure is permission_denied. Everything else that
// is not a proven "API group absent" is temporarily_unavailable: a 404 for a
// pod metrics-server has not sampled yet, a 503 while metrics-server
// restarts, a timeout, a network error. Before this, every metrics.k8s.io
// error became metric_api_not_installed and every node summary error became
// permission_denied, so a healthy cluster showed both warnings whenever the
// first sample after pod start raced metrics-server.
func classifyKubernetesError(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, errNoRESTClient):
		return "runtime_not_supported"
	case apierrors.IsForbidden(err) || apierrors.IsUnauthorized(err):
		return "permission_denied"
	default:
		return "temporarily_unavailable"
	}
}

// podMetricsReason decides the reason for a failed PodMetrics read. A
// NotFound is ambiguous, so it asks discovery whether the API group exists.
// A positive answer is cached for the life of the runner: an API group does
// not disappear between samples, and the discovery call is not free.
func (kr *KubernetesRunner) podMetricsReason(ctx context.Context, source kubernetesMetricsSource, err error) string {
	reason := classifyKubernetesError(err)
	if reason != "temporarily_unavailable" || !apierrors.IsNotFound(err) {
		return reason
	}
	kr.metricsAPIMu.Lock()
	known := kr.metricsAPIRegistered
	kr.metricsAPIMu.Unlock()
	if known {
		return "temporarily_unavailable"
	}
	registered, discoveryErr := source.MetricsAPIRegistered(ctx)
	if discoveryErr != nil {
		// Discovery itself failed, so nothing is proven either way. Say
		// "not yet" rather than "not installed": the next sample re-checks.
		return "temporarily_unavailable"
	}
	if !registered {
		return "metric_api_not_installed"
	}
	kr.metricsAPIMu.Lock()
	kr.metricsAPIRegistered = true
	kr.metricsAPIMu.Unlock()
	return "temporarily_unavailable"
}

func (kr *KubernetesRunner) SampleResources(ctx context.Context, jobName string, options ResourceSampleOptions) (ResourceSnapshot, error) {
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
	source := kr.metricsSourceFor()

	metricData, metricErr := source.PodMetrics(ctx, kr.namespace, pod.Name)
	if metricErr != nil {
		reason := kr.podMetricsReason(ctx, source, metricErr)
		snapshot.Unavailable = append(snapshot.Unavailable,
			jobtelemetry.Unavailable{MetricPrefix: kubernetesCPUPrefix, Reason: reason},
			jobtelemetry.Unavailable{MetricPrefix: kubernetesMemoryPrefix, Reason: reason},
		)
	} else {
		var metrics podMetricsResponse
		if json.Unmarshal(metricData, &metrics) != nil {
			snapshot.Unavailable = append(snapshot.Unavailable,
				jobtelemetry.Unavailable{MetricPrefix: kubernetesCPUPrefix, Reason: "runtime_not_supported"},
				jobtelemetry.Unavailable{MetricPrefix: kubernetesMemoryPrefix, Reason: "runtime_not_supported"},
			)
		} else {
			if !metrics.Timestamp.IsZero() {
				snapshot.ObservedAt = metrics.Timestamp.UTC()
			}
			var totalCPU, totalMemory int64
			var hasCPU, hasMemory bool
			for _, containerMetric := range metrics.Containers {
				component := kubernetesMetricComponent(containerMetric.Name)
				// Presence of the component label IS the "this is one container,
				// not the job total" signal. The old scope label carried no
				// information this does not.
				labels := []jobtelemetry.Label{{Key: "component", Value: component}}
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
				add("cpu.utilization", "millicores", "gauge", totalCPU, jobtelemetry.Label{Key: "cpu", Value: "total"})
			}
			if hasMemory {
				add("memory.usage", "bytes", "gauge", totalMemory)
			}
			// metrics-server answered with an object that has no containers
			// yet: the pod is known but not sampled. That is a "not yet".
			if !hasCPU && !hasMemory {
				snapshot.Unavailable = append(snapshot.Unavailable,
					jobtelemetry.Unavailable{MetricPrefix: kubernetesCPUPrefix, Reason: "temporarily_unavailable"},
					jobtelemetry.Unavailable{MetricPrefix: kubernetesMemoryPrefix, Reason: "temporarily_unavailable"},
				)
			}
		}
	}
	if !options.IncludeStorage {
		return snapshot, nil
	}
	if pod.Spec.NodeName == "" {
		// Not scheduled yet. There is no node to ask; the next storage
		// sample will find one.
		snapshot.Unavailable = append(snapshot.Unavailable, jobtelemetry.Unavailable{MetricPrefix: kubernetesStoragePrefix, Reason: "temporarily_unavailable"})
		return snapshot, nil
	}
	summaryData, summaryErr := source.NodeSummary(ctx, pod.Spec.NodeName)
	if summaryErr != nil {
		snapshot.Unavailable = append(snapshot.Unavailable, jobtelemetry.Unavailable{MetricPrefix: kubernetesStoragePrefix, Reason: classifyKubernetesError(summaryErr)})
		return snapshot, nil
	}
	var summary summaryResponse
	if json.Unmarshal(summaryData, &summary) != nil {
		snapshot.Unavailable = append(snapshot.Unavailable, jobtelemetry.Unavailable{MetricPrefix: kubernetesStoragePrefix, Reason: "runtime_not_supported"})
		return snapshot, nil
	}
	found := false
	for _, podSummary := range summary.Pods {
		if podSummary.PodRef.Name != pod.Name || podSummary.PodRef.Namespace != kr.namespace {
			continue
		}
		found = true
		// Kubelet charges this used value to the Pod. The capacity and available
		// values describe the node filesystem, so they are not job metrics.
		addSummaryFS(add, podSummary.EphemeralStorage, kubernetesStorageMetric{
			volume: "total", kind: "ephemeral", includeCapacity: false,
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
				component: kubernetesMetricComponent(containerSummary.Name),
				volume:    "rootfs-" + containerSummary.Name, kind: "rootfs", includeCapacity: false,
			})
		}
		break
	}
	if !found {
		// The kubelet summary lags pod creation by one stats interval.
		snapshot.Unavailable = append(snapshot.Unavailable, jobtelemetry.Unavailable{MetricPrefix: kubernetesStoragePrefix, Reason: "temporarily_unavailable"})
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
	jobLabels := []jobtelemetry.Label{}
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
		metric := kubernetesStorageMetric{volume: volume.Name, mount: mounts[volume.Name]}
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
	labels := []jobtelemetry.Label{{Key: "volume", Value: metric.volume}, {Key: "kind", Value: metric.kind}}
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
