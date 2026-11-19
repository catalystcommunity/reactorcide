package coordinatorworker

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"

	"github.com/catalystcommunity/app-utils-go/logging"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/admission"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/characteristics"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/worker"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/workerclient/csilapi"
)

// admissionRequester is the optional client method a resources-mode worker
// uses. *workerclient.Client implements it.
type admissionRequester interface {
	RequestJobWithAdmission(ctx context.Context, characteristics csilapi.WorkerCharacteristics, report *csilapi.AdmissionReport) (csilapi.RequestJobResponse, error)
}

// requestJob sends RequestJob, with the admission report when there is one.
func requestJob(ctx context.Context, c client, wc csilapi.WorkerCharacteristics, report *csilapi.AdmissionReport) (csilapi.RequestJobResponse, error) {
	if report != nil {
		if requester, ok := c.(admissionRequester); ok {
			return requester.RequestJobWithAdmission(ctx, wc, report)
		}
	}
	return c.RequestJob(ctx, wc)
}

// leaseReservations holds the resources of the leases this worker runs. The
// run loop adds a lease before it sends the next RequestJob, so the next
// headroom report already includes it, also before its Pod or container
// exists.
type leaseReservations struct {
	mu      sync.Mutex
	byLease map[string]reservation
}

type reservation struct {
	jobID string
	need  admission.Resources
}

func newLeaseReservations() *leaseReservations {
	return &leaseReservations{byLease: map[string]reservation{}}
}

func (r *leaseReservations) add(leaseID, jobID string, need admission.Resources) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.byLease[leaseID] = reservation{jobID: jobID, need: need}
}

func (r *leaseReservations) remove(leaseID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.byLease, leaseID)
}

// uncounted returns the sum of the reservations whose job is not in counted.
func (r *leaseReservations) uncounted(counted map[string]bool) admission.Resources {
	r.mu.Lock()
	defer r.mu.Unlock()
	var total admission.Resources
	for _, res := range r.byLease {
		if counted[res.jobID] {
			continue
		}
		total = total.Add(res.need)
	}
	return total
}

// leaseNeed returns the admission need of a lease: its CPU request and memory
// limit.
func leaseNeed(lease csilapi.Lease) admission.Resources {
	need, err := admission.JobNeed(lease.Resources.CpuRequest, lease.Resources.MemoryLimit)
	if err != nil {
		// The coordinator validates resources at submit. Count an invalid
		// value as the default, never as zero.
		need, _ = admission.JobNeed("", "")
	}
	return need
}

// hostCapacityFunc returns the CPU and memory of this host. Tests replace it.
var hostCapacityFunc = hostCapacity

// hostCapacity reads the number of CPUs and the total memory of the host.
// Memory is read from /proc/meminfo, so it is known only on Linux; other
// systems need a configured budget.
func hostCapacity() (admission.Resources, error) {
	capacity := admission.Resources{CPUMillicores: int64(runtime.NumCPU()) * 1000}
	file, err := os.Open("/proc/meminfo")
	if err != nil {
		return capacity, fmt.Errorf("host memory is unknown (set REACTORCIDE_WORKER_ADMISSION_MEMORY): %w", err)
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) >= 2 && fields[0] == "MemTotal:" {
			kib, parseErr := strconv.ParseInt(fields[1], 10, 64)
			if parseErr != nil {
				break
			}
			capacity.MemoryBytes = kib * 1024
			return capacity, nil
		}
	}
	return capacity, errors.New("host memory is unknown (set REACTORCIDE_WORKER_ADMISSION_MEMORY)")
}

// measureAdmission returns the capacity and headroom of this worker.
//
//   - A runner that is a worker.CapacityReporter (Kubernetes) reports its
//     capacity and the requests of the job Pods it can see. Other runners use
//     the host size and count only this worker's own leases.
//   - A configured budget (cfg.AdmissionBudget) replaces the measured
//     capacity, one resource at a time. Use it with node autoscaling, on a
//     shared host, or when the host size is unknown.
//   - Used is the runner's count plus every lease of this worker that the
//     runner did not count yet.
func measureAdmission(ctx context.Context, cfg Config, runner worker.JobRunner, reservations *leaseReservations) (admission.Report, error) {
	var snapshot worker.CapacitySnapshot
	budget := cfg.AdmissionBudget
	if reporter, ok := runner.(worker.CapacityReporter); ok {
		measured, err := reporter.ResourceCapacity(ctx)
		if err != nil {
			return admission.Report{}, err
		}
		snapshot = measured
	} else if budget.CPUMillicores <= 0 || budget.MemoryBytes <= 0 {
		host, err := hostCapacityFunc()
		// The CPU count is always known; only the memory size can be unknown.
		if err != nil && budget.MemoryBytes <= 0 {
			return admission.Report{}, err
		}
		snapshot.Capacity = host
	}
	if budget.CPUMillicores > 0 {
		snapshot.Capacity.CPUMillicores = budget.CPUMillicores
	}
	if budget.MemoryBytes > 0 {
		snapshot.Capacity.MemoryBytes = budget.MemoryBytes
	}
	used := snapshot.Used.Add(reservations.uncounted(snapshot.CountedJobIDs))
	return admission.Report{Capacity: snapshot.Capacity, Headroom: snapshot.Capacity.Sub(used)}, nil
}

func admissionReportWire(report admission.Report) *csilapi.AdmissionReport {
	return &csilapi.AdmissionReport{
		Mode:                  admission.ModeResources,
		CapacityCpuMillicores: report.Capacity.CPUMillicores,
		CapacityMemoryBytes:   report.Capacity.MemoryBytes,
		HeadroomCpuMillicores: report.Headroom.CPUMillicores,
		HeadroomMemoryBytes:   report.Headroom.MemoryBytes,
	}
}

// applyDetectedTopology adds the region and zone that the runner detects to
// the worker's custom characteristics. A key that the operator set already
// is never replaced. A detection failure only logs: the characteristics are
// optional.
func applyDetectedTopology(ctx context.Context, cfg Config, runner worker.JobRunner) Config {
	if !cfg.DetectTopology {
		return cfg
	}
	detector, ok := runner.(worker.TopologyDetector)
	if !ok {
		return cfg
	}
	detected, err := detector.DetectTopology(ctx)
	if err != nil {
		logging.Log.WithError(err).Warn("coordinatorworker: could not detect region and zone; continuing without them")
		return cfg
	}
	present := make(map[string]bool, len(cfg.Custom))
	for _, kv := range cfg.Custom {
		present[kv.Key] = true
	}
	custom := append([]characteristics.KV(nil), cfg.Custom...)
	for _, key := range []string{"region", "zone"} {
		value, found := detected[key]
		if !found || value == "" || present[key] {
			continue
		}
		custom = append(custom, characteristics.KV{Key: key, Value: value})
		logging.Log.WithFields(map[string]any{"key": key, "value": value}).Info("coordinatorworker: detected topology characteristic")
	}
	cfg.Custom = custom
	return cfg
}
