package uiapi

import (
	"context"
	"time"

	"github.com/catalystcommunity/app-utils-go/logging"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/backlog"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/resourcehistory"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/uiapi/csilapi"
)

// addQueueBacklogDetail adds the database backlog (oldest wait, CPU and
// memory sums) to each queue summary in place, and returns the backlog by
// characteristic. A store without backlog support, or a failed read, leaves
// the summaries as they are: backlog is a display convenience.
func (s *UiService) addQueueBacklogDetail(ctx context.Context, queues []csilapi.QueueSummary) []csilapi.CharacteristicBacklog {
	source := backlog.SourceFrom(s.deps.Store)
	if source == nil {
		return nil
	}
	snapshot, err := backlog.Load(ctx, source, nil, time.Now())
	if err != nil {
		logging.Log.WithError(err).Warn("Failed to read queue backlog for list-queues")
		return nil
	}
	byUUID := make(map[string]backlog.QueueBacklog, len(snapshot.Queues))
	for _, q := range snapshot.Queues {
		byUUID[q.QueueUUID] = q
	}
	for i := range queues {
		q, ok := byUUID[queues[i].QueueUuid]
		if !ok {
			continue
		}
		age, cpu, memory := q.OldestAgeSeconds, q.CPURequestMillicores, q.MemoryLimitBytes
		queues[i].BacklogOldestAgeSeconds = &age
		queues[i].BacklogCpuRequestMillicores = &cpu
		queues[i].BacklogMemoryLimitBytes = &memory
	}
	out := make([]csilapi.CharacteristicBacklog, 0, len(snapshot.ByCharacteristic))
	for _, c := range snapshot.ByCharacteristic {
		out = append(out, csilapi.CharacteristicBacklog{
			Key: c.Key, Value: c.Value, Queues: int64(c.Queues), Count: c.Count,
			OldestAgeSeconds: c.OldestAgeSeconds, CpuRequestMillicores: c.CPURequestMillicores, MemoryLimitBytes: c.MemoryLimitBytes,
		})
	}
	return out
}

func workerAdmissionToCsil(a *models.WorkerAdmission) *csilapi.WorkerAdmissionSummary {
	if a == nil {
		return nil
	}
	return &csilapi.WorkerAdmissionSummary{
		Mode:                  a.Mode,
		CapacityCpuMillicores: a.CapacityCPUMillicores,
		CapacityMemoryBytes:   a.CapacityMemoryBytes,
		HeadroomCpuMillicores: a.HeadroomCPUMillicores,
		HeadroomMemoryBytes:   a.HeadroomMemoryBytes,
		ReportedAt:            a.ReportedAt.UTC().Format(time.RFC3339),
	}
}

func resourceSuggestionToCsil(s resourcehistory.Suggestion) *csilapi.ResourceSuggestion {
	out := &csilapi.ResourceSuggestion{
		Runs:                        int64(s.Runs),
		MinRuns:                     int64(s.MinRuns),
		Sufficient:                  s.Sufficient,
		CpuP95Millicores:            s.CPUP95Millicores,
		MemoryPeakP95Bytes:          s.MemoryPeakP95Bytes,
		OomRuns:                     int64(s.OOMRuns),
		CurrentCpuRequestMillicores: s.CurrentCPURequestMillicores,
		CurrentMemoryLimitBytes:     s.CurrentMemoryLimitBytes,
	}
	if s.Sufficient {
		cpu, cpuValue, memory, memoryValue := s.CPURequest, s.CPURequestMillicores, s.MemoryLimit, s.MemoryLimitBytes
		out.CpuRequest, out.CpuRequestMillicores, out.MemoryLimit, out.MemoryLimitBytes = &cpu, &cpuValue, &memory, &memoryValue
	}
	return out
}
