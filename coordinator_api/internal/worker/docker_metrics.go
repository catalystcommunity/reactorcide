package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/jobtelemetry"
	"github.com/docker/docker/api/types/container"
)

func (dr *DockerRunner) SampleResources(ctx context.Context, jobID string, options ResourceSampleOptions) (ResourceSnapshot, error) {
	stats, err := dr.containerStats(ctx, jobID)
	if err != nil {
		return ResourceSnapshot{}, err
	}
	snapshot := ResourceSnapshot{ObservedAt: stats.Read.UTC()}
	if snapshot.ObservedAt.IsZero() {
		snapshot.ObservedAt = time.Now().UTC()
	}
	main := dockerStatsSeries(stats)
	if options.IncludeStorage {
		if storage, ok := dr.storageSeries(ctx, jobID); ok {
			main = append(main, storage)
		} else {
			snapshot.Unavailable = append(snapshot.Unavailable, jobtelemetry.Unavailable{MetricPrefix: "storage.used", Reason: "runtime_not_supported"})
		}
	}

	// A builder job runs buildkitd in a sidecar container, and the build
	// work happens THERE: the job container only runs buildctl. Without
	// the sidecar the job looks idle while the host is busy.
	dr.sidecarsMu.Lock()
	sidecarID, hasSidecar := dr.sidecars[jobID]
	dr.sidecarsMu.Unlock()
	if !hasSidecar {
		addContainerSeries(&snapshot, main, nil, false, false, "")
		return snapshot, nil
	}
	var sidecar []containerSeries
	sidecarStats, sidecarErr := dr.containerStats(ctx, sidecarID)
	if sidecarErr == nil {
		sidecar = dockerStatsSeries(sidecarStats)
		if options.IncludeStorage {
			if storage, ok := dr.storageSeries(ctx, sidecarID); ok {
				sidecar = append(sidecar, storage)
			}
		}
	}
	addContainerSeries(&snapshot, main, sidecar, true, sidecarErr == nil, "builder")
	return snapshot, nil
}

func (dr *DockerRunner) containerStats(ctx context.Context, containerID string) (container.StatsResponse, error) {
	var stats container.StatsResponse
	response, err := dr.client.ContainerStatsOneShot(ctx, containerID)
	if err != nil {
		return stats, fmt.Errorf("docker stats: %w", err)
	}
	defer response.Body.Close()
	if err := json.NewDecoder(response.Body).Decode(&stats); err != nil {
		return stats, fmt.Errorf("decode docker stats: %w", err)
	}
	return stats, nil
}

func (dr *DockerRunner) storageSeries(ctx context.Context, containerID string) (containerSeries, bool) {
	inspect, _, err := dr.client.ContainerInspectWithRaw(ctx, containerID, true)
	if err != nil || inspect.SizeRw == nil || *inspect.SizeRw < 0 {
		return containerSeries{}, false
	}
	return rootfsStorageSeries(*inspect.SizeRw), true
}

func rootfsStorageSeries(sizeRw int64) containerSeries {
	return containerSeries{name: "storage.used", unit: "bytes", kind: "gauge", value: sizeRw, labels: []jobtelemetry.Label{
		{Key: "volume", Value: "rootfs"},
		{Key: "kind", Value: "rootfs"},
	}}
}

// dockerStatsSeries converts one container's docker stats into series with
// no component label. addContainerSeries decides the labels.
func dockerStatsSeries(stats container.StatsResponse) []containerSeries {
	var out []containerSeries
	add := func(name, unit, kind string, value int64, labels ...jobtelemetry.Label) {
		out = append(out, containerSeries{name: name, unit: unit, kind: kind, value: value, labels: labels})
	}
	add("cpu.usage", "nanoseconds", "counter", int64(stats.CPUStats.CPUUsage.TotalUsage), jobtelemetry.Label{Key: "cpu", Value: "total"})
	// Per-core series (cpu.usage{cpu=cpuN}) are deliberately NOT emitted. They
	// were one series per HOST core -- sixteen extra series per sample on an
	// ordinary machine -- answering a question no view asks. The total above is
	// what "how much CPU is this job using" needs.
	if stats.CPUStats.OnlineCPUs > 0 {
		add("cpu.capacity", "millicores", "gauge", int64(stats.CPUStats.OnlineCPUs)*1000)
	}
	add("memory.usage", "bytes", "gauge", int64(stats.MemoryStats.Usage))
	if stats.MemoryStats.Limit > 0 {
		add("memory.limit", "bytes", "gauge", int64(stats.MemoryStats.Limit))
	}
	if value, ok := firstMemoryStat(stats.MemoryStats.Stats, "rss", "total_rss", "anon"); ok {
		add("memory.rss", "bytes", "gauge", int64(value))
	}
	if value, ok := firstMemoryStat(stats.MemoryStats.Stats, "cache", "total_cache", "file"); ok {
		add("memory.cache", "bytes", "gauge", int64(value))
	}
	if inactive, ok := firstMemoryStat(stats.MemoryStats.Stats, "inactive_file", "total_inactive_file"); ok && inactive <= stats.MemoryStats.Usage {
		add("memory.working_set", "bytes", "gauge", int64(stats.MemoryStats.Usage-inactive))
	}
	if value, ok := firstMemoryStat(stats.MemoryStats.Stats, "swap", "total_swap"); ok {
		add("memory.swap.usage", "bytes", "gauge", int64(value))
	}
	if stats.MemoryStats.Commit > 0 {
		add("memory.committed", "bytes", "gauge", int64(stats.MemoryStats.Commit))
	}
	return out
}

func firstMemoryStat(stats map[string]uint64, keys ...string) (uint64, bool) {
	for _, key := range keys {
		if value, ok := stats[key]; ok {
			return value, true
		}
	}
	return 0, false
}

// containerSeries is one value of one container, before the collector adds
// the component label. Docker and containerd use it; Kubernetes reports
// per-container values with labels already.
type containerSeries struct {
	name, unit, kind string
	value            int64
	labels           []jobtelemetry.Label
}

func (s containerSeries) key() string {
	var b strings.Builder
	b.WriteString(s.name)
	for _, label := range s.labels {
		b.WriteString("\x00" + label.Key + "=" + label.Value)
	}
	return b.String()
}

// rollupTakesMax lists the series whose job roll-up is the larger value, not
// the sum. Both containers share one host: two copies of the host CPU count
// are not twice the CPUs, and a container without a memory limit reports the
// host memory, which a sum would count twice.
var rollupTakesMax = map[string]bool{
	"cpu.capacity": true,
	"memory.limit": true,
}

// addContainerSeries writes one sample of a job's containers into snapshot.
//
// A series with NO component label is the job roll-up and covers every
// container of the job; a series with one belongs to that container.
//
//   - No sidecar: the job container IS the job. Its series go out with no
//     component label, exactly as before sidecars were sampled.
//   - Sidecar sampled: each container's series go out with its component
//     (main, and sidecarComponent), and the roll-up is their sum (max for
//     rollupTakesMax). A roll-up goes out only for a series both containers
//     reported.
//   - Sidecar NOT sampled this time: the main series go out with
//     component=main and NO roll-up. A roll-up without the sidecar would be
//     too low; for the cpu.usage counter it would also go backwards, which
//     reads as a counter reset. The roll-up names are reported as
//     temporarily_unavailable for this sample.
func addContainerSeries(snapshot *ResourceSnapshot, main, sidecar []containerSeries, hasSidecar, sidecarOK bool, sidecarComponent string) {
	add := func(s containerSeries, labels []jobtelemetry.Label) {
		id := int64(len(snapshot.Series))
		snapshot.Series = append(snapshot.Series, jobtelemetry.SeriesDefinition{SeriesID: id, Name: s.name, Unit: s.unit, Kind: s.kind, Labels: labels})
		snapshot.Values = append(snapshot.Values, jobtelemetry.Value{SeriesID: id, Value: s.value})
	}
	withComponent := func(s containerSeries, component string) []jobtelemetry.Label {
		labels := make([]jobtelemetry.Label, 0, len(s.labels)+1)
		labels = append(labels, s.labels...)
		return append(labels, jobtelemetry.Label{Key: "component", Value: component})
	}
	if !hasSidecar {
		for _, s := range main {
			add(s, s.labels)
		}
		return
	}
	for _, s := range main {
		add(s, withComponent(s, "main"))
	}
	if !sidecarOK {
		seen := map[string]bool{}
		for _, s := range main {
			if seen[s.name] {
				continue
			}
			seen[s.name] = true
			snapshot.Unavailable = append(snapshot.Unavailable, jobtelemetry.Unavailable{MetricPrefix: s.name, Reason: "temporarily_unavailable"})
		}
		return
	}
	bySidecarKey := make(map[string]containerSeries, len(sidecar))
	for _, s := range sidecar {
		add(s, withComponent(s, sidecarComponent))
		bySidecarKey[s.key()] = s
	}
	for _, s := range main {
		other, ok := bySidecarKey[s.key()]
		if !ok {
			continue
		}
		rollup := s
		if rollupTakesMax[s.name] {
			rollup.value = max(s.value, other.value)
		} else {
			rollup.value = s.value + other.value
		}
		add(rollup, s.labels)
	}
}
