package worker

import (
	"context"
	"testing"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/jobtelemetry"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// snapshotValue finds a series by name and component ("" = job roll-up).
func snapshotValue(snapshot ResourceSnapshot, name, component string) (int64, bool) {
	for _, series := range snapshot.Series {
		if series.Name != name || jobtelemetry.SeriesComponent(series.Labels) != component {
			continue
		}
		for _, value := range snapshot.Values {
			if value.SeriesID == series.SeriesID {
				return value.Value, true
			}
		}
	}
	return 0, false
}

// A builder pod as the kubelet summary reports it: pod-level cpu/memory (the
// pod cgroup, so it holds the sidecar too) and per-container values.
const builderSummaryBody = `{"pods":[{
  "podRef":{"name":"job-pod","namespace":"ci"},
  "cpu":{"time":"2026-11-14T10:00:00Z","usageNanoCores":1750000000},
  "memory":{"workingSetBytes":3000},
  "ephemeral-storage":{"usedBytes":4096},
  "containers":[
    {"name":"job","cpu":{"usageNanoCores":250000000},"memory":{"workingSetBytes":1000}},
    {"name":"builder","cpu":{"usageNanoCores":1400000000},"memory":{"workingSetBytes":1900}}
  ]}]}`

// metrics-server serves no PodMetrics object for builder pods. The sampler
// must then read the kubelet summary, and the roll-up is the POD value
// (1750m), not the container sum (1650m), so nothing the pod cgroup charges
// is lost.
func TestKubernetesSampleResources_BuilderPodFallsBackToSummary(t *testing.T) {
	source := &fakeMetricsSource{
		podMetrics:  []fakeRead{{err: apierrors.NewNotFound(podMetricsGVR, "job-pod")}},
		nodeSummary: []fakeRead{{body: []byte(builderSummaryBody)}},
		registered:  true,
	}
	runner := newMetricsTestRunner(t, source)
	snapshot, err := runner.SampleResources(context.Background(), "job-1", ResourceSampleOptions{IncludeStorage: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Unavailable) != 0 {
		t.Fatalf("the summary answered, so nothing is unavailable; got %v", snapshot.Unavailable)
	}
	checks := []struct {
		name, component string
		want            int64
	}{
		{"cpu.utilization", "", 1750},
		{"cpu.utilization", "main", 250},
		{"cpu.utilization", "builder", 1400},
		{"memory.usage", "", 3000},
		{"memory.usage", "main", 1000},
		{"memory.usage", "builder", 1900},
		{"storage.used", "", 4096},
	}
	for _, check := range checks {
		got, ok := snapshotValue(snapshot, check.name, check.component)
		if !ok || got != check.want {
			t.Errorf("%s component=%q = %d (present=%v), want %d", check.name, check.component, got, ok, check.want)
		}
	}
	if want := time.Date(2026, 11, 14, 10, 0, 0, 0, time.UTC); !snapshot.ObservedAt.Equal(want) {
		t.Errorf("ObservedAt = %v, want the summary cpu time %v", snapshot.ObservedAt, want)
	}
}

// Without pod-level values the roll-up is the container sum.
func TestKubernetesSampleResources_SummaryFallbackSumsContainersWithoutPodValues(t *testing.T) {
	body := `{"pods":[{"podRef":{"name":"job-pod","namespace":"ci"},"containers":[
	  {"name":"job","cpu":{"usageNanoCores":250000000},"memory":{"workingSetBytes":1000}},
	  {"name":"buildkitd","cpu":{"usageNanoCores":500000000},"memory":{"workingSetBytes":2000}}]}]}`
	source := &fakeMetricsSource{
		podMetrics:  []fakeRead{{body: []byte(`{"containers":[]}`)}},
		nodeSummary: []fakeRead{{body: []byte(body)}},
	}
	runner := newMetricsTestRunner(t, source)
	snapshot, err := runner.SampleResources(context.Background(), "job-1", ResourceSampleOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := snapshotValue(snapshot, "cpu.utilization", ""); got != 750 {
		t.Errorf("roll-up cpu = %d, want 750", got)
	}
	if got, _ := snapshotValue(snapshot, "memory.usage", ""); got != 3000 {
		t.Errorf("roll-up memory = %d, want 3000", got)
	}
	if _, ok := snapshotValue(snapshot, "cpu.utilization", "builder"); !ok {
		t.Error("buildkitd must report as component=builder")
	}
}

// A summary without usage for the pod keeps the metrics-server reason.
func TestKubernetesSampleResources_SummaryWithoutUsageKeepsReason(t *testing.T) {
	source := &fakeMetricsSource{
		podMetrics:  []fakeRead{{err: apierrors.NewNotFound(podMetricsGVR, "job-pod")}},
		nodeSummary: []fakeRead{{body: []byte(nodeSummaryBody)}},
		registered:  true,
	}
	runner := newMetricsTestRunner(t, source)
	snapshot, err := runner.SampleResources(context.Background(), "job-1", ResourceSampleOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if reasonsByPrefix(snapshot.Unavailable)["cpu.utilization"] != "temporarily_unavailable" {
		t.Fatalf("reasons = %v", snapshot.Unavailable)
	}
}

// An unscheduled pod has no node: no summary read, the reason stays.
func TestKubernetesSampleResources_UnscheduledPodSkipsSummary(t *testing.T) {
	source := &fakeMetricsSource{
		podMetrics:  []fakeRead{{err: apierrors.NewNotFound(podMetricsGVR, "job-pod")}},
		nodeSummary: []fakeRead{{body: []byte(builderSummaryBody)}},
		registered:  true,
	}
	countingSource := &countingSummarySource{fakeMetricsSource: source}
	clientset := fake.NewSimpleClientset(&corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "job-pod", Namespace: "ci", Labels: map[string]string{"reactorcide.io/job-name": "job-1"}},
		Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "job"}}},
	})
	runner := &KubernetesRunner{clientset: clientset, namespace: "ci", metricsSource: countingSource, workflowOutputs: map[string]string{}}
	snapshot, err := runner.SampleResources(context.Background(), "job-1", ResourceSampleOptions{IncludeStorage: true})
	if err != nil {
		t.Fatal(err)
	}
	reasons := reasonsByPrefix(snapshot.Unavailable)
	if reasons["cpu.utilization"] != "temporarily_unavailable" || reasons["storage.used"] != "temporarily_unavailable" {
		t.Fatalf("reasons = %v", reasons)
	}
	if countingSource.summaryCalls != 0 {
		t.Fatalf("summary calls = %d, want 0", countingSource.summaryCalls)
	}
}

type countingSummarySource struct {
	*fakeMetricsSource
	summaryCalls int
}

func (c *countingSummarySource) NodeSummary(ctx context.Context, node string) ([]byte, error) {
	c.summaryCalls++
	return c.fakeMetricsSource.NodeSummary(ctx, node)
}

// The CPU fallback and the storage path in one sample, and the next sample
// inside the TTL, share ONE node summary read. After the TTL it is read again.
func TestKubernetesSampleResources_SummaryCacheIsShared(t *testing.T) {
	source := &countingSummarySource{fakeMetricsSource: &fakeMetricsSource{
		podMetrics:  []fakeRead{{err: apierrors.NewNotFound(podMetricsGVR, "job-pod")}},
		nodeSummary: []fakeRead{{body: []byte(builderSummaryBody)}},
		registered:  true,
	}}
	runner := newMetricsTestRunner(t, nil)
	runner.metricsSource = source
	now := time.Date(2026, 11, 14, 10, 0, 0, 0, time.UTC)
	runner.summaryCache.now = func() time.Time { return now }

	for i := 0; i < 2; i++ {
		if _, err := runner.SampleResources(context.Background(), "job-1", ResourceSampleOptions{IncludeStorage: true}); err != nil {
			t.Fatal(err)
		}
		now = now.Add(2 * time.Second)
	}
	if source.summaryCalls != 1 {
		t.Fatalf("summary calls = %d, want 1 within the TTL", source.summaryCalls)
	}
	now = now.Add(summaryCacheTTL)
	if _, err := runner.SampleResources(context.Background(), "job-1", ResourceSampleOptions{}); err != nil {
		t.Fatal(err)
	}
	if source.summaryCalls != 2 {
		t.Fatalf("summary calls = %d, want 2 after the TTL", source.summaryCalls)
	}
}

func series(name, kind string, value int64, labels ...jobtelemetry.Label) containerSeries {
	return containerSeries{name: name, unit: "u", kind: kind, value: value, labels: labels}
}

// Without a sidecar the output is exactly the pre-sidecar output: the job
// container's series with no component label.
func TestAddContainerSeriesNoSidecarUnchanged(t *testing.T) {
	var snapshot ResourceSnapshot
	main := []containerSeries{
		series("cpu.usage", "counter", 100, jobtelemetry.Label{Key: "cpu", Value: "total"}),
		series("memory.usage", "gauge", 10),
	}
	addContainerSeries(&snapshot, main, nil, false, false, "")
	if len(snapshot.Series) != 2 || len(snapshot.Unavailable) != 0 {
		t.Fatalf("series = %+v unavailable = %v", snapshot.Series, snapshot.Unavailable)
	}
	for _, s := range snapshot.Series {
		if jobtelemetry.SeriesComponent(s.Labels) != "" {
			t.Errorf("%s carries a component label: %v", s.Name, s.Labels)
		}
	}
	if len(snapshot.Series[0].Labels) != 1 || snapshot.Series[0].Labels[0].Key != "cpu" {
		t.Errorf("cpu.usage labels = %v, want only cpu=total", snapshot.Series[0].Labels)
	}
}

func TestAddContainerSeriesSumsSidecar(t *testing.T) {
	var snapshot ResourceSnapshot
	main := []containerSeries{
		series("cpu.usage", "counter", 100, jobtelemetry.Label{Key: "cpu", Value: "total"}),
		series("cpu.capacity", "gauge", 8000),
		series("memory.usage", "gauge", 10),
		series("memory.limit", "gauge", 4000),
		series("memory.swap.usage", "gauge", 1),
	}
	sidecar := []containerSeries{
		series("cpu.usage", "counter", 900, jobtelemetry.Label{Key: "cpu", Value: "total"}),
		series("cpu.capacity", "gauge", 8000),
		series("memory.usage", "gauge", 90),
		series("memory.limit", "gauge", 64000),
	}
	addContainerSeries(&snapshot, main, sidecar, true, true, "builder")
	checks := []struct {
		name, component string
		want            int64
	}{
		{"cpu.usage", "", 1000},
		{"cpu.usage", "main", 100},
		{"cpu.usage", "builder", 900},
		{"cpu.capacity", "", 8000},
		{"memory.usage", "", 100},
		{"memory.limit", "", 64000},
	}
	for _, check := range checks {
		got, ok := snapshotValue(snapshot, check.name, check.component)
		if !ok || got != check.want {
			t.Errorf("%s component=%q = %d (present=%v), want %d", check.name, check.component, got, ok, check.want)
		}
	}
	if _, ok := snapshotValue(snapshot, "memory.swap.usage", ""); ok {
		t.Error("a series only one container reported must not get a roll-up")
	}
	if _, ok := snapshotValue(snapshot, "memory.swap.usage", "main"); !ok {
		t.Error("the main series must still go out")
	}
	for _, s := range snapshot.Series {
		if s.Name == "cpu.usage" && jobtelemetry.SeriesComponent(s.Labels) == "" && (len(s.Labels) != 1 || s.Labels[0].Key != "cpu") {
			t.Errorf("roll-up cpu.usage labels = %v", s.Labels)
		}
	}
}

// A failed sidecar sample must not produce a roll-up: the cpu.usage counter
// would drop by the sidecar's share and read as a counter reset.
func TestAddContainerSeriesSidecarFailureOmitsRollup(t *testing.T) {
	var snapshot ResourceSnapshot
	main := []containerSeries{
		series("cpu.usage", "counter", 100, jobtelemetry.Label{Key: "cpu", Value: "total"}),
		series("memory.usage", "gauge", 10),
	}
	addContainerSeries(&snapshot, main, nil, true, false, "builder")
	for _, s := range snapshot.Series {
		if jobtelemetry.SeriesComponent(s.Labels) != "main" {
			t.Errorf("only component=main series expected, got %s %v", s.Name, s.Labels)
		}
	}
	reasons := reasonsByPrefix(snapshot.Unavailable)
	if reasons["cpu.usage"] != "temporarily_unavailable" || reasons["memory.usage"] != "temporarily_unavailable" {
		t.Fatalf("reasons = %v", reasons)
	}
}

func TestNerdctlStatsSeries(t *testing.T) {
	got, cpuOK := nerdctlStatsSeries(nerdctlStatsRow{CPUPercentage: "150.5%", MemoryUsage: "100MiB / 2GiB"})
	if !cpuOK || len(got) != 3 || got[0].name != "cpu.utilization" || got[0].value != 1505 {
		t.Fatalf("series = %+v cpuOK=%v", got, cpuOK)
	}
	if _, cpuOK := nerdctlStatsSeries(nerdctlStatsRow{CPUPercentage: "--"}); cpuOK {
		t.Fatal("an unparsable CPU value must report cpuOK=false")
	}
}

func TestNerdctlBytes(t *testing.T) {
	for text, want := range map[string]int64{
		"100MiB": 100 << 20, " 2GiB": 2 << 30, "512B": 512, "1.5kB": 1500, "3MB": 3_000_000, "64Mi": 64 << 20, "0B": 0,
	} {
		got, err := nerdctlBytes(text)
		if err != nil || got != want {
			t.Errorf("nerdctlBytes(%q) = %d, %v; want %d", text, got, err, want)
		}
	}
}
