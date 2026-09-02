package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/jobtelemetry"
	"github.com/docker/docker/api/types/container"
)

func (dr *DockerRunner) SampleResources(ctx context.Context, jobID string, options ResourceSampleOptions) (ResourceSnapshot, error) {
	response, err := dr.client.ContainerStatsOneShot(ctx, jobID)
	if err != nil {
		return ResourceSnapshot{}, fmt.Errorf("docker stats: %w", err)
	}
	defer response.Body.Close()
	var stats container.StatsResponse
	if err := json.NewDecoder(response.Body).Decode(&stats); err != nil {
		return ResourceSnapshot{}, fmt.Errorf("decode docker stats: %w", err)
	}
	snapshot := ResourceSnapshot{ObservedAt: stats.Read.UTC()}
	if snapshot.ObservedAt.IsZero() {
		snapshot.ObservedAt = time.Now().UTC()
	}
	add := func(name, unit, kind string, value int64, labels ...jobtelemetry.Label) {
		id := int64(len(snapshot.Series))
		snapshot.Series = append(snapshot.Series, jobtelemetry.SeriesDefinition{
			SeriesID: id, Name: name, Unit: unit, Kind: kind, Labels: labels,
		})
		snapshot.Values = append(snapshot.Values, jobtelemetry.Value{SeriesID: id, Value: value})
	}
	// The job roll-up carries NO component label. Docker runs one container, so
	// the roll-up and "main" are the same series; labelling it component=main
	// made it look like one component among several and put the total next to
	// nothing. A series with a component label belongs to that component; a
	// series without one is the job total. See the scope-label removal note in
	// jobtelemetry/query.go.
	base := []jobtelemetry.Label{}
	add("cpu.usage", "nanoseconds", "counter", int64(stats.CPUStats.CPUUsage.TotalUsage), append(base, jobtelemetry.Label{Key: "cpu", Value: "total"})...)
	// Per-core series (cpu.usage{cpu=cpuN}) are deliberately NOT emitted. They
	// were one series per HOST core -- sixteen extra series per sample on an
	// ordinary machine -- answering a question no view asks. The total above is
	// what "how much CPU is this job using" needs.
	if stats.CPUStats.OnlineCPUs > 0 {
		add("cpu.capacity", "millicores", "gauge", int64(stats.CPUStats.OnlineCPUs)*1000, base...)
	}
	add("memory.usage", "bytes", "gauge", int64(stats.MemoryStats.Usage), base...)
	if stats.MemoryStats.Limit > 0 {
		add("memory.limit", "bytes", "gauge", int64(stats.MemoryStats.Limit), base...)
	}
	if value, ok := firstMemoryStat(stats.MemoryStats.Stats, "rss", "total_rss", "anon"); ok {
		add("memory.rss", "bytes", "gauge", int64(value), base...)
	}
	if value, ok := firstMemoryStat(stats.MemoryStats.Stats, "cache", "total_cache", "file"); ok {
		add("memory.cache", "bytes", "gauge", int64(value), base...)
	}
	if inactive, ok := firstMemoryStat(stats.MemoryStats.Stats, "inactive_file", "total_inactive_file"); ok && inactive <= stats.MemoryStats.Usage {
		add("memory.working_set", "bytes", "gauge", int64(stats.MemoryStats.Usage-inactive), base...)
	}
	if value, ok := firstMemoryStat(stats.MemoryStats.Stats, "swap", "total_swap"); ok {
		add("memory.swap.usage", "bytes", "gauge", int64(value), base...)
	}
	if stats.MemoryStats.Commit > 0 {
		add("memory.committed", "bytes", "gauge", int64(stats.MemoryStats.Commit), base...)
	}
	if options.IncludeStorage {
		inspect, _, inspectErr := dr.client.ContainerInspectWithRaw(ctx, jobID, true)
		if inspectErr == nil && inspect.SizeRw != nil && *inspect.SizeRw >= 0 {
			add("storage.used", "bytes", "gauge", *inspect.SizeRw,
				jobtelemetry.Label{Key: "volume", Value: "rootfs"},
				jobtelemetry.Label{Key: "kind", Value: "rootfs"},
			)
		} else {
			snapshot.Unavailable = append(snapshot.Unavailable, jobtelemetry.Unavailable{MetricPrefix: "storage.used", Reason: "runtime_not_supported"})
		}
	}
	return snapshot, nil
}

func firstMemoryStat(stats map[string]uint64, keys ...string) (uint64, bool) {
	for _, key := range keys {
		if value, ok := stats[key]; ok {
			return value, true
		}
	}
	return 0, false
}
