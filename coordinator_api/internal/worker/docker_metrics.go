package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/jobtelemetry"
	"github.com/docker/docker/api/types/container"
)

func (dr *DockerRunner) SampleResources(ctx context.Context, jobID string) (ResourceSnapshot, error) {
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
	base := []jobtelemetry.Label{{Key: "scope", Value: "job"}, {Key: "component", Value: "main"}}
	add("cpu.usage", "nanoseconds", "counter", int64(stats.CPUStats.CPUUsage.TotalUsage), append(base, jobtelemetry.Label{Key: "cpu", Value: "total"})...)
	for index, usage := range stats.CPUStats.CPUUsage.PercpuUsage {
		labels := append([]jobtelemetry.Label{}, base...)
		labels = append(labels, jobtelemetry.Label{Key: "cpu", Value: fmt.Sprintf("cpu%d", index)})
		add("cpu.usage", "nanoseconds", "counter", int64(usage), labels...)
	}
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
	inspect, _, inspectErr := dr.client.ContainerInspectWithRaw(ctx, jobID, true)
	if inspectErr == nil && inspect.SizeRw != nil && *inspect.SizeRw >= 0 {
		add("storage.used", "bytes", "gauge", *inspect.SizeRw,
			jobtelemetry.Label{Key: "scope", Value: "job"},
			jobtelemetry.Label{Key: "volume", Value: "rootfs"},
			jobtelemetry.Label{Key: "kind", Value: "rootfs"},
		)
	} else {
		snapshot.Unavailable = append(snapshot.Unavailable, jobtelemetry.Unavailable{MetricPrefix: "storage.used", Reason: "runtime_not_supported"})
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
