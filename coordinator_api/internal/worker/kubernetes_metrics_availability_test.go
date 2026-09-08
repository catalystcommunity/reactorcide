package worker

import (
	"context"
	"errors"
	"testing"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/jobtelemetry"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
)

// fakeMetricsSource scripts the three reads SampleResources makes. Each call
// pops the next scripted answer so a test can say "404 first, then a sample".
type fakeMetricsSource struct {
	podMetrics     []fakeRead
	nodeSummary    []fakeRead
	registered     bool
	registeredErr  error
	discoveryCalls int
}

type fakeRead struct {
	body []byte
	err  error
}

func (f *fakeMetricsSource) next(queue *[]fakeRead) ([]byte, error) {
	if len(*queue) == 0 {
		return nil, errors.New("fakeMetricsSource: no scripted answer")
	}
	read := (*queue)[0]
	if len(*queue) > 1 {
		*queue = (*queue)[1:]
	}
	return read.body, read.err
}

func (f *fakeMetricsSource) PodMetrics(context.Context, string, string) ([]byte, error) {
	return f.next(&f.podMetrics)
}

func (f *fakeMetricsSource) NodeSummary(context.Context, string) ([]byte, error) {
	return f.next(&f.nodeSummary)
}

func (f *fakeMetricsSource) MetricsAPIRegistered(context.Context) (bool, error) {
	f.discoveryCalls++
	return f.registered, f.registeredErr
}

const (
	podMetricsBody  = `{"timestamp":"2026-09-08T00:00:00Z","containers":[{"name":"job","usage":{"cpu":"250m","memory":"64Mi"}}]}`
	nodeSummaryBody = `{"pods":[{"podRef":{"name":"job-pod","namespace":"ci"},"ephemeral-storage":{"usedBytes":4096}}]}`
)

func newMetricsTestRunner(t *testing.T, source *fakeMetricsSource) *KubernetesRunner {
	t.Helper()
	clientset := fake.NewSimpleClientset(&corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: "job-pod", Namespace: "ci",
			Labels: map[string]string{"reactorcide.io/job-name": "job-1"},
		},
		Spec: corev1.PodSpec{NodeName: "node-a", Containers: []corev1.Container{{Name: "job"}}},
	})
	return &KubernetesRunner{clientset: clientset, namespace: "ci", metricsSource: source, workflowOutputs: map[string]string{}}
}

func reasonsByPrefix(items []jobtelemetry.Unavailable) map[string]string {
	out := map[string]string{}
	for _, item := range items {
		out[item.MetricPrefix] = item.Reason
	}
	return out
}

func hasSeries(snapshot ResourceSnapshot, name string) bool {
	for _, series := range snapshot.Series {
		if series.Name == name {
			return true
		}
	}
	return false
}

var podMetricsGVR = schema.GroupResource{Group: "metrics.k8s.io", Resource: "pods"}

// Case 1: the metrics API is not registered. A 404 on the PodMetrics object
// plus discovery saying the group is absent is the only path to
// metric_api_not_installed.
func TestKubernetesSampleResources_MetricsAPINotRegistered(t *testing.T) {
	source := &fakeMetricsSource{
		podMetrics:  []fakeRead{{err: apierrors.NewNotFound(podMetricsGVR, "job-pod")}},
		nodeSummary: []fakeRead{{body: []byte(nodeSummaryBody)}},
		registered:  false,
	}
	runner := newMetricsTestRunner(t, source)
	snapshot, err := runner.SampleResources(context.Background(), "job-1", ResourceSampleOptions{IncludeStorage: true})
	if err != nil {
		t.Fatal(err)
	}
	reasons := reasonsByPrefix(snapshot.Unavailable)
	if reasons["cpu.utilization"] != "metric_api_not_installed" || reasons["memory.usage"] != "metric_api_not_installed" {
		t.Fatalf("reasons = %v, want metric_api_not_installed for cpu.utilization and memory.usage", reasons)
	}
	if _, ok := reasons["cpu.usage"]; ok {
		t.Fatal("the collector must name the series it emits (cpu.utilization), not cpu.usage")
	}
	if _, ok := reasons["storage.used"]; ok {
		t.Fatalf("storage must be unaffected by the metrics API being absent, got %v", reasons)
	}
	if !hasSeries(snapshot, "storage.used") {
		t.Fatal("storage.used should still be collected from the node summary")
	}
}

// Case 2: the worker is forbidden from reading pod metrics.
func TestKubernetesSampleResources_PodMetricsForbidden(t *testing.T) {
	source := &fakeMetricsSource{
		podMetrics: []fakeRead{{err: apierrors.NewForbidden(podMetricsGVR, "job-pod", errors.New("rbac"))}},
		registered: true,
	}
	runner := newMetricsTestRunner(t, source)
	snapshot, err := runner.SampleResources(context.Background(), "job-1", ResourceSampleOptions{})
	if err != nil {
		t.Fatal(err)
	}
	reasons := reasonsByPrefix(snapshot.Unavailable)
	if reasons["cpu.utilization"] != "permission_denied" || reasons["memory.usage"] != "permission_denied" {
		t.Fatalf("reasons = %v, want permission_denied", reasons)
	}
	if source.discoveryCalls != 0 {
		t.Fatal("a Forbidden answer needs no discovery: the API answered")
	}
}

// Case 3: pod metrics are temporarily unavailable during startup, then
// succeed. The 404 with a registered API is temporarily_unavailable, and
// the positive discovery answer is cached so later samples do not re-ask.
func TestKubernetesSampleResources_PodMetricsNotReadyThenAvailable(t *testing.T) {
	source := &fakeMetricsSource{
		podMetrics: []fakeRead{
			{err: apierrors.NewNotFound(podMetricsGVR, "job-pod")},
			{err: apierrors.NewNotFound(podMetricsGVR, "job-pod")},
			{body: []byte(podMetricsBody)},
		},
		registered: true,
	}
	runner := newMetricsTestRunner(t, source)

	first, err := runner.SampleResources(context.Background(), "job-1", ResourceSampleOptions{})
	if err != nil {
		t.Fatal(err)
	}
	reasons := reasonsByPrefix(first.Unavailable)
	if reasons["cpu.utilization"] != "temporarily_unavailable" || reasons["memory.usage"] != "temporarily_unavailable" {
		t.Fatalf("first sample reasons = %v, want temporarily_unavailable", reasons)
	}
	if hasSeries(first, "cpu.utilization") {
		t.Fatal("no cpu series expected before metrics-server has a sample")
	}

	second, err := runner.SampleResources(context.Background(), "job-1", ResourceSampleOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if reasonsByPrefix(second.Unavailable)["cpu.utilization"] != "temporarily_unavailable" {
		t.Fatalf("second sample reasons = %v", second.Unavailable)
	}
	if source.discoveryCalls != 1 {
		t.Fatalf("discovery calls = %d, want 1: a positive answer is cached", source.discoveryCalls)
	}

	third, err := runner.SampleResources(context.Background(), "job-1", ResourceSampleOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(third.Unavailable) != 0 {
		t.Fatalf("a successful sample must report nothing unavailable, got %v", third.Unavailable)
	}
	if !hasSeries(third, "cpu.utilization") || !hasSeries(third, "memory.usage") {
		t.Fatalf("expected cpu.utilization and memory.usage series, got %+v", third.Series)
	}
}

// A 503 or a timeout from metrics.k8s.io is neither "not installed" nor
// "forbidden", and discovery is not consulted for it.
func TestKubernetesSampleResources_PodMetricsTransientErrors(t *testing.T) {
	for name, readErr := range map[string]error{
		"service unavailable": apierrors.NewServiceUnavailable("metrics-server is restarting"),
		"timeout":             apierrors.NewTimeoutError("deadline", 1),
		"context deadline":    context.DeadlineExceeded,
		"internal":            apierrors.NewInternalError(errors.New("boom")),
	} {
		t.Run(name, func(t *testing.T) {
			source := &fakeMetricsSource{podMetrics: []fakeRead{{err: readErr}}, registered: false}
			runner := newMetricsTestRunner(t, source)
			snapshot, err := runner.SampleResources(context.Background(), "job-1", ResourceSampleOptions{})
			if err != nil {
				t.Fatal(err)
			}
			reasons := reasonsByPrefix(snapshot.Unavailable)
			if reasons["cpu.utilization"] != "temporarily_unavailable" {
				t.Fatalf("reasons = %v, want temporarily_unavailable", reasons)
			}
			if source.discoveryCalls != 0 {
				t.Fatal("discovery must only be asked about a NotFound")
			}
		})
	}
}

// A NotFound while discovery itself fails proves nothing, so it is reported
// as temporary and re-checked next time rather than declared uninstalled.
func TestKubernetesSampleResources_NotFoundWithFailedDiscoveryIsTemporary(t *testing.T) {
	source := &fakeMetricsSource{
		podMetrics:    []fakeRead{{err: apierrors.NewNotFound(podMetricsGVR, "job-pod")}},
		registeredErr: errors.New("discovery timed out"),
	}
	runner := newMetricsTestRunner(t, source)
	snapshot, err := runner.SampleResources(context.Background(), "job-1", ResourceSampleOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if reasonsByPrefix(snapshot.Unavailable)["cpu.utilization"] != "temporarily_unavailable" {
		t.Fatalf("reasons = %v", snapshot.Unavailable)
	}
	runner.SampleResources(context.Background(), "job-1", ResourceSampleOptions{}) //nolint:errcheck
	if source.discoveryCalls != 2 {
		t.Fatalf("discovery calls = %d, want 2: a failed answer is not cached", source.discoveryCalls)
	}
}

// Case 4: the node summary request is forbidden.
func TestKubernetesSampleResources_NodeSummaryForbidden(t *testing.T) {
	source := &fakeMetricsSource{
		podMetrics:  []fakeRead{{body: []byte(podMetricsBody)}},
		nodeSummary: []fakeRead{{err: apierrors.NewForbidden(schema.GroupResource{Resource: "nodes/proxy"}, "node-a", errors.New("rbac"))}},
	}
	runner := newMetricsTestRunner(t, source)
	snapshot, err := runner.SampleResources(context.Background(), "job-1", ResourceSampleOptions{IncludeStorage: true})
	if err != nil {
		t.Fatal(err)
	}
	reasons := reasonsByPrefix(snapshot.Unavailable)
	if reasons["storage.used"] != "permission_denied" {
		t.Fatalf("reasons = %v, want permission_denied for storage.used", reasons)
	}
	if _, ok := reasons["cpu.utilization"]; ok {
		t.Fatal("cpu must be unaffected by a storage failure")
	}
}

// Case 5: the node summary fails temporarily, then succeeds.
func TestKubernetesSampleResources_NodeSummaryTransientThenAvailable(t *testing.T) {
	source := &fakeMetricsSource{
		podMetrics: []fakeRead{{body: []byte(podMetricsBody)}},
		nodeSummary: []fakeRead{
			{err: apierrors.NewServiceUnavailable("kubelet busy")},
			{body: []byte(`{"pods":[]}`)}, // summary answered but does not list the pod yet
			{body: []byte(nodeSummaryBody)},
		},
	}
	runner := newMetricsTestRunner(t, source)
	for i, want := range []string{"temporarily_unavailable", "temporarily_unavailable", ""} {
		snapshot, err := runner.SampleResources(context.Background(), "job-1", ResourceSampleOptions{IncludeStorage: true})
		if err != nil {
			t.Fatal(err)
		}
		got := reasonsByPrefix(snapshot.Unavailable)["storage.used"]
		if got != want {
			t.Fatalf("sample %d storage reason = %q, want %q (unavailable=%v)", i, got, want, snapshot.Unavailable)
		}
	}
}

// Without a REST client nothing can be read; that is a runtime limitation,
// not a missing API and not a permission problem.
func TestKubernetesSampleResources_NoRESTClientIsRuntimeNotSupported(t *testing.T) {
	runner := newMetricsTestRunner(t, nil)
	runner.metricsSource = nil // fall back to the fake clientset, whose REST client is a typed nil
	snapshot, err := runner.SampleResources(context.Background(), "job-1", ResourceSampleOptions{IncludeStorage: true})
	if err != nil {
		t.Fatal(err)
	}
	reasons := reasonsByPrefix(snapshot.Unavailable)
	for _, prefix := range []string{"cpu.utilization", "memory.usage", "storage.used"} {
		if reasons[prefix] != "runtime_not_supported" {
			t.Fatalf("%s reason = %q, want runtime_not_supported (all: %v)", prefix, reasons[prefix], reasons)
		}
	}
}
