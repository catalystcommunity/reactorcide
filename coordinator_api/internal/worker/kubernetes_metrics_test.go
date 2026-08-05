package worker

import (
	"testing"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/jobtelemetry"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

type capturedMetric struct {
	name   string
	value  int64
	labels map[string]string
}

func metricCapture(dst *[]capturedMetric) func(string, string, string, int64, ...jobtelemetry.Label) {
	return func(name, _, _ string, value int64, labels ...jobtelemetry.Label) {
		labelMap := make(map[string]string, len(labels))
		for _, label := range labels {
			labelMap[label.Key] = label.Value
		}
		*dst = append(*dst, capturedMetric{name: name, value: value, labels: labelMap})
	}
}

func findCapturedMetric(metrics []capturedMetric, name, scope, component string) (capturedMetric, bool) {
	for _, metric := range metrics {
		if metric.name == name && metric.labels["scope"] == scope && metric.labels["component"] == component {
			return metric, true
		}
	}
	return capturedMetric{}, false
}

func TestAddKubernetesResourceSettings(t *testing.T) {
	always := corev1.ContainerRestartPolicyAlways
	pod := &corev1.Pod{Spec: corev1.PodSpec{
		Containers: []corev1.Container{{
			Name: "job",
			Resources: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("500m")},
				Limits: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("2"),
					corev1.ResourceMemory: resource.MustParse("4Gi"),
				},
			},
		}},
		InitContainers: []corev1.Container{
			{
				Name: "docker-daemon", RestartPolicy: &always,
				Resources: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("100m")},
					Limits: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("250m"),
						corev1.ResourceMemory: resource.MustParse("256Mi"),
					},
				},
			},
			{
				Name: "prepare-workspace",
				Resources: corev1.ResourceRequirements{Limits: corev1.ResourceList{
					corev1.ResourceCPU: resource.MustParse("8"),
				}},
			},
		},
	}}

	var metrics []capturedMetric
	addKubernetesResourceSettings(metricCapture(&metrics), pod)

	checks := []struct {
		name, scope, component string
		want                   int64
	}{
		{"cpu.request", "component", "main", 500},
		{"cpu.limit", "component", "main", 2000},
		{"memory.limit", "component", "main", 4 * 1024 * 1024 * 1024},
		{"cpu.request", "component", "docker", 100},
		{"cpu.limit", "component", "docker", 250},
		{"memory.limit", "component", "docker", 256 * 1024 * 1024},
		{"cpu.request", "job", "", 600},
		{"cpu.limit", "job", "", 2250},
		{"memory.limit", "job", "", 4352 * 1024 * 1024},
	}
	for _, check := range checks {
		got, ok := findCapturedMetric(metrics, check.name, check.scope, check.component)
		if !ok || got.value != check.want {
			t.Errorf("%s scope=%s component=%s = %d, present=%v; want %d", check.name, check.scope, check.component, got.value, ok, check.want)
		}
	}
	if _, ok := findCapturedMetric(metrics, "cpu.limit", "component", "prepare-workspace"); ok {
		t.Error("one-time init container must not contribute to running job limits")
	}
	if _, ok := findCapturedMetric(metrics, "memory.request", "job", ""); ok {
		t.Error("memory.request must not be emitted")
	}
}

func TestKubernetesStorageMetricsDoNotUseNodeCapacity(t *testing.T) {
	used, capacity, available := uint64(12), uint64(1000), uint64(988)
	fs := &summaryFS{UsedBytes: &used, CapacityBytes: &capacity, AvailableBytes: &available}
	var metrics []capturedMetric
	addSummaryFS(metricCapture(&metrics), fs, kubernetesStorageMetric{
		scope: "job", volume: "total", kind: "ephemeral",
	})
	if len(metrics) != 1 || metrics[0].name != "storage.used" || metrics[0].value != 12 {
		t.Fatalf("Pod ephemeral metrics = %+v; want only job storage.used", metrics)
	}

	metrics = nil
	addSummaryFS(metricCapture(&metrics), fs, kubernetesStorageMetric{
		scope: "job", volume: "scratch", kind: "ephemeral-pvc", includeCapacity: true,
	})
	if len(metrics) != 3 {
		t.Fatalf("dedicated ephemeral PVC metric count = %d; want used, capacity, and available", len(metrics))
	}
}

func TestKubernetesStorageVolumesExcludeSharedPVC(t *testing.T) {
	pod := &corev1.Pod{Spec: corev1.PodSpec{
		Containers: []corev1.Container{{VolumeMounts: []corev1.VolumeMount{
			{Name: "workspace", MountPath: "/workspace"},
			{Name: "scratch", MountPath: "/scratch"},
			{Name: "shared", MountPath: "/cache"},
		}}},
		Volumes: []corev1.Volume{
			{Name: "workspace", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
			{Name: "scratch", VolumeSource: corev1.VolumeSource{Ephemeral: &corev1.EphemeralVolumeSource{}}},
			{Name: "shared", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "cache"}}},
			{Name: "config", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{}}},
		},
	}}

	metrics := kubernetesStorageVolumes(pod)
	if len(metrics) != 2 {
		t.Fatalf("storage volume policies = %+v; want only job-owned volumes", metrics)
	}
	if got := metrics["workspace"]; got.kind != "ephemeral" || got.includeCapacity || got.mount != "/workspace" {
		t.Errorf("workspace policy = %+v", got)
	}
	if got := metrics["scratch"]; got.kind != "ephemeral-pvc" || !got.includeCapacity || got.mount != "/scratch" {
		t.Errorf("scratch policy = %+v", got)
	}
	if _, ok := metrics["shared"]; ok {
		t.Error("shared PVC usage must not be attributed to one job")
	}
}
