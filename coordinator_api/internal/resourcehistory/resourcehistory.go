// Package resourcehistory computes and stores the per-run resource summaries
// that the job resource history reads. The worker API calls Refresh when a
// job finishes; the UI API calls it for runs that finished before summaries
// existed, or whose summary has an old algorithm version.
package resourcehistory

import (
	"context"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/jobtelemetry"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/objects"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

// Current reports whether a stored summary is from the current algorithm.
func Current(summary *models.JobResourceSummary) bool {
	return summary != nil && summary.SummaryVersion >= jobtelemetry.SummaryVersion
}

// Refresh computes the summary of one job from its stored telemetry and
// upserts it. A job with no telemetry gets a row with sample_count 0, so it
// is not computed again on each read.
func Refresh(ctx context.Context, objectStore objects.ObjectStore, summaries store.ResourceSummaryStore, jobID string) (*models.JobResourceSummary, error) {
	computed, err := jobtelemetry.SummarizeJob(ctx, objectStore, jobID)
	if err != nil {
		return nil, err
	}
	row := ToModel(computed, time.Now().UTC())
	if err := summaries.UpsertJobResourceSummary(ctx, row); err != nil {
		return nil, err
	}
	return row, nil
}

// ToModel converts a computed summary to its stored row.
func ToModel(summary jobtelemetry.JobSummary, computedAt time.Time) *models.JobResourceSummary {
	row := &models.JobResourceSummary{
		JobID:                summary.JobID,
		SummaryVersion:       jobtelemetry.SummaryVersion,
		ComputedAt:           computedAt,
		SampleCount:          summary.SampleCount,
		CPURequestMillicores: summary.CPURequestMillicores,
		CPULimitMillicores:   summary.CPULimitMillicores,
		MemoryLimitBytes:     summary.MemoryLimitBytes,
		StorageMaxBytes:      summary.StorageMaxBytes,
	}
	if cpu := summary.CPU; cpu != nil {
		avg, p95, peak, peakAt := cpu.Avg, cpu.P95, cpu.Max, cpu.PeakAt
		row.CPUAvgMillicores, row.CPUP95Millicores, row.CPUMaxMillicores, row.CPUPeakAt = &avg, &p95, &peak, &peakAt
	}
	if memory := summary.Memory; memory != nil {
		avg, p95, peak, peakAt := memory.Avg, memory.P95, memory.Max, memory.PeakAt
		row.MemoryAvgBytes, row.MemoryP95Bytes, row.MemoryMaxBytes, row.MemoryPeakAt = &avg, &p95, &peak, &peakAt
	}
	return row
}
