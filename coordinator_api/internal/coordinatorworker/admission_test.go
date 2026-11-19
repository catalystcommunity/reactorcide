package coordinatorworker

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/admission"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/characteristics"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/worker"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/workerclient/csilapi"
	"github.com/stretchr/testify/require"
)

const testGi = int64(1024 * 1024 * 1024)

// admissionClient is a fakeClient that also records admission reports.
type admissionClient struct {
	*fakeClient
	mu      sync.Mutex
	reports []csilapi.AdmissionReport
	respond func(report *csilapi.AdmissionReport) csilapi.RequestJobResponse
}

func (a *admissionClient) RequestJobWithAdmission(ctx context.Context, wc csilapi.WorkerCharacteristics, report *csilapi.AdmissionReport) (csilapi.RequestJobResponse, error) {
	a.mu.Lock()
	a.reports = append(a.reports, *report)
	a.mu.Unlock()
	return a.respond(report), nil
}

func (a *admissionClient) snapshotReports() []csilapi.AdmissionReport {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]csilapi.AdmissionReport(nil), a.reports...)
}

// capacityRunner is a fakeRunner that reports Kubernetes-style capacity.
type capacityRunner struct {
	*fakeRunner
	snapshot worker.CapacitySnapshot
	err      error
	topology map[string]string
}

func (c *capacityRunner) ResourceCapacity(context.Context) (worker.CapacitySnapshot, error) {
	return c.snapshot, c.err
}

func (c *capacityRunner) DetectTopology(context.Context) (map[string]string, error) {
	return c.topology, nil
}

func TestMeasureAdmissionHostBudgetAndReservations(t *testing.T) {
	previous := hostCapacityFunc
	t.Cleanup(func() { hostCapacityFunc = previous })
	hostCapacityFunc = func() (admission.Resources, error) {
		return admission.Resources{CPUMillicores: 8000, MemoryBytes: 16 * testGi}, nil
	}

	reservations := newLeaseReservations()
	reservations.add("l1", "j1", admission.Resources{CPUMillicores: 2000, MemoryBytes: 4 * testGi})
	report, err := measureAdmission(context.Background(), Config{}, &fakeRunner{}, reservations)
	require.NoError(t, err)
	require.Equal(t, admission.Resources{CPUMillicores: 8000, MemoryBytes: 16 * testGi}, report.Capacity)
	require.Equal(t, admission.Resources{CPUMillicores: 6000, MemoryBytes: 12 * testGi}, report.Headroom)

	// A budget replaces the measured value one resource at a time.
	cfg := Config{AdmissionBudget: admission.Resources{MemoryBytes: 8 * testGi}}
	report, err = measureAdmission(context.Background(), cfg, &fakeRunner{}, reservations)
	require.NoError(t, err)
	require.Equal(t, admission.Resources{CPUMillicores: 8000, MemoryBytes: 8 * testGi}, report.Capacity)

	reservations.remove("l1")
	report, err = measureAdmission(context.Background(), cfg, &fakeRunner{}, reservations)
	require.NoError(t, err)
	require.Equal(t, report.Capacity, report.Headroom)
}

func TestMeasureAdmissionUnknownHostMemoryNeedsBudget(t *testing.T) {
	previous := hostCapacityFunc
	t.Cleanup(func() { hostCapacityFunc = previous })
	hostCapacityFunc = func() (admission.Resources, error) {
		return admission.Resources{CPUMillicores: 4000}, errors.New("host memory is unknown")
	}
	_, err := measureAdmission(context.Background(), Config{}, &fakeRunner{}, newLeaseReservations())
	require.Error(t, err)

	cfg := Config{AdmissionBudget: admission.Resources{MemoryBytes: 2 * testGi}}
	report, err := measureAdmission(context.Background(), cfg, &fakeRunner{}, newLeaseReservations())
	require.NoError(t, err)
	require.Equal(t, admission.Resources{CPUMillicores: 4000, MemoryBytes: 2 * testGi}, report.Capacity)
}

func TestMeasureAdmissionKubernetesDoesNotCountALeaseTwice(t *testing.T) {
	runner := &capacityRunner{fakeRunner: &fakeRunner{}, snapshot: worker.CapacitySnapshot{
		Capacity:      admission.Resources{CPUMillicores: 10000, MemoryBytes: 40 * testGi},
		Used:          admission.Resources{CPUMillicores: 3000, MemoryBytes: 6 * testGi},
		CountedJobIDs: map[string]bool{"has-pod": true},
	}}
	reservations := newLeaseReservations()
	reservations.add("l1", "has-pod", admission.Resources{CPUMillicores: 1000, MemoryBytes: 2 * testGi})
	reservations.add("l2", "no-pod-yet", admission.Resources{CPUMillicores: 500, MemoryBytes: testGi})

	report, err := measureAdmission(context.Background(), Config{}, runner, reservations)
	require.NoError(t, err)
	require.Equal(t, admission.Resources{CPUMillicores: 6500, MemoryBytes: 33 * testGi}, report.Headroom)

	// With a budget (node autoscaling), the budget is the capacity and the
	// Pods still count as used.
	cfg := Config{AdmissionBudget: admission.Resources{CPUMillicores: 64000, MemoryBytes: 256 * testGi}}
	report, err = measureAdmission(context.Background(), cfg, runner, reservations)
	require.NoError(t, err)
	require.Equal(t, admission.Resources{CPUMillicores: 60500, MemoryBytes: 249 * testGi}, report.Headroom)

	runner.err = errors.New("forbidden: list pods")
	_, err = measureAdmission(context.Background(), cfg, runner, reservations)
	require.Error(t, err)
}

func TestLeaseNeedUsesDefaultsForInvalidValues(t *testing.T) {
	need := leaseNeed(csilapi.Lease{Resources: csilapi.Resources{CpuRequest: "250m", MemoryLimit: "1Gi"}})
	require.Equal(t, admission.Resources{CPUMillicores: 250, MemoryBytes: testGi}, need)
	need = leaseNeed(csilapi.Lease{Resources: csilapi.Resources{CpuRequest: "bogus", MemoryLimit: "1Gi"}})
	require.Equal(t, admission.Resources{CPUMillicores: 1000, MemoryBytes: 4 * testGi}, need)
}

func TestApplyDetectedTopologyKeepsOperatorValues(t *testing.T) {
	runner := &capacityRunner{fakeRunner: &fakeRunner{}, topology: map[string]string{"region": "us-east-1", "zone": "us-east-1a"}}
	cfg := Config{DetectTopology: true, Custom: []characteristics.KV{{Key: "zone", Value: "operator-zone"}}}
	got := applyDetectedTopology(context.Background(), cfg, runner)
	require.Equal(t, []characteristics.KV{{Key: "zone", Value: "operator-zone"}, {Key: "region", Value: "us-east-1"}}, got.Custom)
	require.Len(t, cfg.Custom, 1, "the input config must not change")

	cfg.DetectTopology = false
	require.Len(t, applyDetectedTopology(context.Background(), cfg, runner).Custom, 1)
}

// TestRunLoopResourcesModeReportsHeadroom runs the loop in resources mode: the
// first RequestJob reports the full budget, the worker takes a lease, and the
// next RequestJob reports the headroom less that lease.
func TestRunLoopResourcesModeReportsHeadroom(t *testing.T) {
	previous := hostCapacityFunc
	t.Cleanup(func() { hostCapacityFunc = previous })
	hostCapacityFunc = func() (admission.Resources, error) {
		return admission.Resources{CPUMillicores: 4000, MemoryBytes: 8 * testGi}, nil
	}

	block := make(chan struct{})
	runner := &fakeRunner{WaitBlock: block}
	lease := csilapi.Lease{
		LeaseId: "lease-1", JobId: "job-1", Image: "alpine", Command: []string{"true"},
		Resources:      csilapi.Resources{CpuRequest: "1500m", CpuLimit: "2", MemoryLimit: "2Gi"},
		TimeoutSeconds: 30, CancelGraceSeconds: 1,
	}
	client := &admissionClient{fakeClient: &fakeClient{}}
	var handed sync.Once
	client.respond = func(*csilapi.AdmissionReport) csilapi.RequestJobResponse {
		response := csilapi.RequestJobResponse{}
		handed.Do(func() {
			l := lease
			response = csilapi.RequestJobResponse{HasLease: true, Lease: &l}
		})
		return response
	}

	cfg := testConfig()
	cfg.Concurrency = 4
	cfg.AdmissionMode = admission.ModeResources
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- runLoop(ctx, cfg, client, runnerFactoryFor(runner)) }()

	require.Eventually(t, func() bool { return len(client.snapshotReports()) >= 2 }, 8*time.Second, 20*time.Millisecond)
	reports := client.snapshotReports()
	require.Equal(t, admission.ModeResources, reports[0].Mode)
	require.Equal(t, int64(4000), reports[0].CapacityCpuMillicores)
	require.Equal(t, int64(4000), reports[0].HeadroomCpuMillicores)
	require.Equal(t, int64(2500), reports[1].HeadroomCpuMillicores)
	require.Equal(t, 6*testGi, reports[1].HeadroomMemoryBytes)
	require.Zero(t, client.snapshotRequestJobCalls(), "resources mode must not use the plain RequestJob")

	close(block)
	cancel()
	require.ErrorIs(t, <-done, context.Canceled)
}

func TestRunLoopResourcesModeFailsWithoutCapacity(t *testing.T) {
	previous := hostCapacityFunc
	t.Cleanup(func() { hostCapacityFunc = previous })
	hostCapacityFunc = func() (admission.Resources, error) {
		return admission.Resources{CPUMillicores: 1000}, errors.New("host memory is unknown")
	}
	cfg := testConfig()
	cfg.AdmissionMode = admission.ModeResources
	err := runLoop(context.Background(), cfg, &fakeClient{}, runnerFactoryFor(&fakeRunner{}))
	require.ErrorContains(t, err, "resources admission")

	cfg.AdmissionMode = "fair-share"
	require.ErrorContains(t, runLoop(context.Background(), cfg, &fakeClient{}, runnerFactoryFor(&fakeRunner{})), "admission mode")
}
