package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/jobtelemetry"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/resources"
)

type nerdctlStatsRow struct {
	CPUPercentage    string `json:"CPUPerc"`
	CPUPercentageAlt string `json:"CPUPercentage"`
	MemoryUsage      string `json:"MemUsage"`
	MemoryUsageAlt   string `json:"MemoryUsage"`
}

func (cr *ContainerdRunner) SampleResources(ctx context.Context, jobID string, options ResourceSampleOptions) (ResourceSnapshot, error) {
	row, err := nerdctlStats(ctx, jobID)
	if err != nil {
		return ResourceSnapshot{}, err
	}
	snapshot := ResourceSnapshot{ObservedAt: time.Now().UTC()}
	main, cpuOK := nerdctlStatsSeries(row)
	if !cpuOK {
		snapshot.Unavailable = append(snapshot.Unavailable, jobtelemetry.Unavailable{MetricPrefix: "cpu.utilization", Reason: "runtime_not_supported"})
	}
	if options.IncludeStorage {
		if storage, ok := nerdctlStorageSeries(ctx, jobID); ok {
			main = append(main, storage)
		} else {
			snapshot.Unavailable = append(snapshot.Unavailable, jobtelemetry.Unavailable{MetricPrefix: "storage.used", Reason: "runtime_not_supported"})
		}
	}

	// The builder sidecar does the build work; see docker_metrics.go.
	cr.sidecarsMu.Lock()
	sidecarName, hasSidecar := cr.sidecars[jobID]
	cr.sidecarsMu.Unlock()
	if !hasSidecar {
		addContainerSeries(&snapshot, main, nil, false, false, "")
		return snapshot, nil
	}
	var sidecar []containerSeries
	sidecarRow, sidecarErr := nerdctlStats(ctx, sidecarName)
	if sidecarErr == nil {
		sidecar, _ = nerdctlStatsSeries(sidecarRow)
		if options.IncludeStorage {
			if storage, ok := nerdctlStorageSeries(ctx, sidecarName); ok {
				sidecar = append(sidecar, storage)
			}
		}
	}
	addContainerSeries(&snapshot, main, sidecar, true, sidecarErr == nil, "builder")
	return snapshot, nil
}

func nerdctlStats(ctx context.Context, containerName string) (nerdctlStatsRow, error) {
	var row nerdctlStatsRow
	output, err := exec.CommandContext(ctx, nerdctlBinary,
		"--namespace", containerdNamespace,
		"stats", "--no-stream", "--format", "{{json .}}", containerName,
	).Output()
	if err != nil {
		return row, fmt.Errorf("nerdctl stats: %w", err)
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(output))), &row); err != nil {
		return row, fmt.Errorf("decode nerdctl stats: %w", err)
	}
	return row, nil
}

// nerdctlStatsSeries converts one stats row into series with no component
// label. The bool is false when the CPU value did not parse.
func nerdctlStatsSeries(row nerdctlStatsRow) ([]containerSeries, bool) {
	var out []containerSeries
	cpuOK := false
	cpuText := row.CPUPercentage
	if cpuText == "" {
		cpuText = row.CPUPercentageAlt
	}
	if percent, parseErr := strconv.ParseFloat(strings.TrimSpace(strings.TrimSuffix(cpuText, "%")), 64); parseErr == nil {
		out = append(out, containerSeries{name: "cpu.utilization", unit: "millicores", kind: "gauge", value: int64(percent * 10)})
		cpuOK = true
	}
	memoryText := row.MemoryUsage
	if memoryText == "" {
		memoryText = row.MemoryUsageAlt
	}
	parts := strings.Split(memoryText, "/")
	if len(parts) > 0 {
		if used, parseErr := nerdctlBytes(parts[0]); parseErr == nil {
			out = append(out, containerSeries{name: "memory.usage", unit: "bytes", kind: "gauge", value: used})
		}
	}
	if len(parts) > 1 {
		if limit, parseErr := nerdctlBytes(parts[1]); parseErr == nil {
			out = append(out, containerSeries{name: "memory.limit", unit: "bytes", kind: "gauge", value: limit})
		}
	}
	return out, cpuOK
}

func nerdctlStorageSeries(ctx context.Context, containerName string) (containerSeries, bool) {
	var inspectRows []struct {
		SizeRw *int64 `json:"SizeRw"`
	}
	inspectOutput, inspectErr := exec.CommandContext(ctx, nerdctlBinary,
		"--namespace", containerdNamespace, "inspect", "--size", containerName,
	).Output()
	if inspectErr != nil || json.Unmarshal(inspectOutput, &inspectRows) != nil || len(inspectRows) == 0 || inspectRows[0].SizeRw == nil {
		return containerSeries{}, false
	}
	return rootfsStorageSeries(*inspectRows[0].SizeRw), true
}

// nerdctlBytes parses a nerdctl stats size such as "100MiB", "1.5GB" or
// "512B". nerdctl prints docker-style units, which resources.MemoryBytes
// (Kubernetes quantities: "Mi", "G") does not accept, so those are read
// here; anything else falls through to resources.MemoryBytes.
func nerdctlBytes(text string) (int64, error) {
	text = strings.TrimSpace(text)
	units := []struct {
		suffix     string
		multiplier float64
	}{
		{"KiB", 1 << 10}, {"MiB", 1 << 20}, {"GiB", 1 << 30}, {"TiB", 1 << 40},
		{"kB", 1e3}, {"KB", 1e3}, {"MB", 1e6}, {"GB", 1e9}, {"TB", 1e12},
		{"B", 1},
	}
	for _, unit := range units {
		if number, ok := strings.CutSuffix(text, unit.suffix); ok {
			value, err := strconv.ParseFloat(strings.TrimSpace(number), 64)
			if err != nil || value < 0 {
				return 0, fmt.Errorf("nerdctl size %q is not valid", text)
			}
			return int64(math.Round(value * unit.multiplier)), nil
		}
	}
	return resources.MemoryBytes(text)
}
