package worker

import (
	"context"
	"encoding/json"
	"fmt"
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

func (cr *ContainerdRunner) SampleResources(ctx context.Context, jobID string) (ResourceSnapshot, error) {
	output, err := exec.CommandContext(ctx, nerdctlBinary,
		"--namespace", containerdNamespace,
		"stats", "--no-stream", "--format", "{{json .}}", jobID,
	).Output()
	if err != nil {
		return ResourceSnapshot{}, fmt.Errorf("nerdctl stats: %w", err)
	}
	var row nerdctlStatsRow
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(output))), &row); err != nil {
		return ResourceSnapshot{}, fmt.Errorf("decode nerdctl stats: %w", err)
	}
	snapshot := ResourceSnapshot{ObservedAt: time.Now().UTC()}
	base := []jobtelemetry.Label{{Key: "scope", Value: "job"}, {Key: "component", Value: "main"}}
	add := func(name, unit, kind string, value int64, labels ...jobtelemetry.Label) {
		id := int64(len(snapshot.Series))
		snapshot.Series = append(snapshot.Series, jobtelemetry.SeriesDefinition{SeriesID: id, Name: name, Unit: unit, Kind: kind, Labels: labels})
		snapshot.Values = append(snapshot.Values, jobtelemetry.Value{SeriesID: id, Value: value})
	}
	cpuText := row.CPUPercentage
	if cpuText == "" {
		cpuText = row.CPUPercentageAlt
	}
	if percent, parseErr := strconv.ParseFloat(strings.TrimSpace(strings.TrimSuffix(cpuText, "%")), 64); parseErr == nil {
		add("cpu.utilization", "millicores", "gauge", int64(percent*10), base...)
	} else {
		snapshot.Unavailable = append(snapshot.Unavailable, jobtelemetry.Unavailable{MetricPrefix: "cpu.usage", Reason: "runtime_not_supported"})
	}
	memoryText := row.MemoryUsage
	if memoryText == "" {
		memoryText = row.MemoryUsageAlt
	}
	parts := strings.Split(memoryText, "/")
	if len(parts) > 0 {
		if used, parseErr := resources.MemoryBytes(strings.TrimSpace(parts[0])); parseErr == nil {
			add("memory.usage", "bytes", "gauge", used, base...)
		}
	}
	if len(parts) > 1 {
		if limit, parseErr := resources.MemoryBytes(strings.TrimSpace(parts[1])); parseErr == nil {
			add("memory.limit", "bytes", "gauge", limit, base...)
		}
	}
	var inspectRows []struct {
		SizeRw *int64 `json:"SizeRw"`
	}
	inspectOutput, inspectErr := exec.CommandContext(ctx, nerdctlBinary,
		"--namespace", containerdNamespace, "inspect", "--size", jobID,
	).Output()
	if inspectErr == nil && json.Unmarshal(inspectOutput, &inspectRows) == nil && len(inspectRows) > 0 && inspectRows[0].SizeRw != nil {
		add("storage.used", "bytes", "gauge", *inspectRows[0].SizeRw,
			jobtelemetry.Label{Key: "scope", Value: "job"},
			jobtelemetry.Label{Key: "volume", Value: "rootfs"},
			jobtelemetry.Label{Key: "kind", Value: "rootfs"},
		)
	} else {
		snapshot.Unavailable = append(snapshot.Unavailable, jobtelemetry.Unavailable{MetricPrefix: "storage.used", Reason: "runtime_not_supported"})
	}
	return snapshot, nil
}
