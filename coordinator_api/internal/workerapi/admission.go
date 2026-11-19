package workerapi

import (
	"context"
	"os"
	"strings"
	"time"

	"github.com/catalystcommunity/app-utils-go/logging"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/admission"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/workerapi/csilapi"
)

// defaultAdmissionReserveAfter is how long a queue head that fits a worker's
// capacity but not its headroom waits before the worker reserves room for it
// (see admission.SelectQueues).
const defaultAdmissionReserveAfter = 10 * time.Minute

// admissionReportInterval limits how often the coordinator stores the
// headroom of one worker. The values are for display; claims always use the
// report of the current RequestJob call.
const admissionReportInterval = 30 * time.Second

// admissionReserveAfter is read once from REACTORCIDE_ADMISSION_RESERVE_AFTER
// (a Go duration; "0" turns the anti-starvation reservation off). Tests set
// it directly.
var admissionReserveAfter = loadAdmissionReserveAfter()

func loadAdmissionReserveAfter() time.Duration {
	raw := strings.TrimSpace(os.Getenv("REACTORCIDE_ADMISSION_RESERVE_AFTER"))
	if raw == "" {
		return defaultAdmissionReserveAfter
	}
	value, err := time.ParseDuration(raw)
	if err != nil || value < 0 {
		logging.Log.WithField("value", raw).Warn("REACTORCIDE_ADMISSION_RESERVE_AFTER is not a valid duration; using the default")
		return defaultAdmissionReserveAfter
	}
	return value
}

// admissionReport converts the wire report of a resources-mode worker. It
// returns nil for a worker in the default concurrency mode.
func admissionReport(wire *csilapi.AdmissionReport) *admission.Report {
	if wire == nil || wire.Mode != admission.ModeResources {
		return nil
	}
	return &admission.Report{
		Capacity: admission.Resources{CPUMillicores: wire.CapacityCpuMillicores, MemoryBytes: wire.CapacityMemoryBytes},
		Headroom: admission.Resources{CPUMillicores: wire.HeadroomCpuMillicores, MemoryBytes: wire.HeadroomMemoryBytes},
	}
}

// admitQueues narrows the satisfied queues of a resources-mode worker to
// those whose head job fits its headroom (admission.SelectQueues). A store
// without admission support, or a failed read, leaves the queues unchanged;
// the claim-time fit check (jobFits) still applies.
func (s *WorkerService) admitQueues(ctx context.Context, wkr *models.Worker, report *admission.Report, queues []string) []string {
	admissionStore, ok := s.deps.Store.(store.AdmissionStore)
	if !ok {
		return queues
	}
	rows, err := admissionStore.ListWaitingQueueHeads(ctx, queues)
	if err != nil {
		logging.Log.WithError(err).WithField("worker_id", wkr.WorkerID).Warn("Failed to read queue heads for resource admission; using the claim-time fit check only")
		return queues
	}
	heads := make([]admission.QueueHead, 0, len(rows))
	for _, row := range rows {
		need, err := admission.JobNeed(row.ResourceCPURequest, row.ResourceMemoryLimit)
		if err != nil {
			// An unparseable head cannot be sized. Leave its queue to the
			// claim-time check.
			continue
		}
		heads = append(heads, admission.QueueHead{
			QueueUUID: row.QueueName, JobID: row.JobID, Priority: row.Priority, CreatedAt: row.CreatedAt, Need: need,
		})
	}
	selection := admission.SelectQueues(queues, heads, *report, time.Now().UTC(), admissionReserveAfter)
	if selection.Reserved != nil {
		logging.Log.WithFields(map[string]any{
			"worker_id": wkr.WorkerID,
			"job_id":    selection.Reserved.JobID,
			"queue":     selection.Reserved.QueueUUID,
		}).Debug("Worker reserves room for a starving queue head")
	}
	return selection.Queues
}

// jobFits is the claim-time fit check. Corndogs can return a task that is
// not the head the coordinator read (a race, or a requeued task), so the
// claimed job is checked again. An unparseable job is admitted: it already
// passed submit validation, and a refusal here would only loop it.
func jobFits(job *models.Job, report *admission.Report) bool {
	need, err := admission.JobNeed(job.ResourceCPURequest, job.ResourceMemoryLimit)
	if err != nil {
		return true
	}
	return report.Headroom.Fits(need)
}

// recordAdmission stores the admission report of a worker for display, at
// most once per admissionReportInterval unless the mode or capacity changes.
// A failure only logs.
func (s *WorkerService) recordAdmission(ctx context.Context, wkr *models.Worker, report *admission.Report) {
	admissionStore, ok := s.deps.Store.(store.AdmissionStore)
	if !ok {
		return
	}
	now := time.Now().UTC()
	previous := wkr.Admission
	if report == nil {
		if previous == nil {
			return
		}
		if err := admissionStore.UpdateWorkerAdmission(ctx, wkr.WorkerID, nil); err != nil {
			logging.Log.WithError(err).WithField("worker_id", wkr.WorkerID).Warn("Failed to clear worker admission report")
		}
		return
	}
	if previous != nil && previous.Mode == admission.ModeResources &&
		previous.CapacityCPUMillicores == report.Capacity.CPUMillicores &&
		previous.CapacityMemoryBytes == report.Capacity.MemoryBytes &&
		now.Sub(previous.ReportedAt) < admissionReportInterval {
		return
	}
	row := &models.WorkerAdmission{
		Mode:                  admission.ModeResources,
		CapacityCPUMillicores: report.Capacity.CPUMillicores,
		CapacityMemoryBytes:   report.Capacity.MemoryBytes,
		HeadroomCPUMillicores: report.Headroom.CPUMillicores,
		HeadroomMemoryBytes:   report.Headroom.MemoryBytes,
		ReportedAt:            now,
	}
	if err := admissionStore.UpdateWorkerAdmission(ctx, wkr.WorkerID, row); err != nil {
		logging.Log.WithError(err).WithField("worker_id", wkr.WorkerID).Warn("Failed to store worker admission report")
	}
}
