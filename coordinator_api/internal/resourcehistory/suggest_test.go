package resourcehistory

import (
	"testing"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/jobtelemetry"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/resources"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

const mi = int64(1024 * 1024)

func measuredRun(status string, cpuP95, memoryMax int64) models.JobRunWithSummary {
	cpuRequest, memoryLimit := int64(1000), 4096*mi
	return models.JobRunWithSummary{
		Job: models.Job{Status: status},
		Summary: &models.JobResourceSummary{
			SummaryVersion: jobtelemetry.SummaryVersion, SampleCount: 30,
			CPUP95Millicores: &cpuP95, MemoryMaxBytes: &memoryMax,
			CPURequestMillicores: &cpuRequest, MemoryLimitBytes: &memoryLimit,
		},
	}
}

func TestSuggestNeedsEnoughRuns(t *testing.T) {
	runs := []models.JobRunWithSummary{
		measuredRun("completed", 500, 512*mi),
		measuredRun("completed", 500, 512*mi),
		{Job: models.Job{Status: "completed"}}, // no summary
		measuredRun("cancelled", 4000, 4096*mi),
	}
	got := Suggest(runs)
	if got.Sufficient || got.Runs != 2 || got.CPURequest != "" || got.MinRuns != SuggestionMinRuns {
		t.Fatalf("suggestion = %+v", got)
	}
}

func TestSuggestUsesP95WithHeadroom(t *testing.T) {
	var runs []models.JobRunWithSummary
	// 20 runs: CPU p95 values 100m..2000m, memory peaks 100Mi..2000Mi.
	for i := int64(20); i >= 1; i-- {
		runs = append(runs, measuredRun("completed", i*100, i*100*mi))
	}
	got := Suggest(runs)
	if !got.Sufficient || got.Runs != 20 {
		t.Fatalf("suggestion = %+v", got)
	}
	// Nearest-rank p95 of 20 values is the 19th: 1900.
	if got.CPUP95Millicores != 1900 || got.MemoryPeakP95Bytes != 1900*mi {
		t.Fatalf("p95 = %d / %d", got.CPUP95Millicores, got.MemoryPeakP95Bytes)
	}
	// 1900 * 1.2 = 2280 -> 2300m. 1900Mi * 1.25 = 2375Mi -> 2432Mi.
	if got.CPURequestMillicores != 2300 || got.CPURequest != "2300m" {
		t.Fatalf("cpu = %d %q", got.CPURequestMillicores, got.CPURequest)
	}
	if got.MemoryLimitBytes != 2432*mi || got.MemoryLimit != "2432Mi" {
		t.Fatalf("memory = %d %q", got.MemoryLimitBytes, got.MemoryLimit)
	}
	if got.CurrentCPURequestMillicores != 1000 || got.CurrentMemoryLimitBytes != 4096*mi {
		t.Fatalf("current = %+v", got)
	}
	// The strings parse with the job resource grammar.
	if v, err := resources.CPUMillicores(got.CPURequest); err != nil || v != got.CPURequestMillicores {
		t.Fatalf("cpu string does not round-trip: %v %v", v, err)
	}
	if v, err := resources.MemoryBytes(got.MemoryLimit); err != nil || v != got.MemoryLimitBytes {
		t.Fatalf("memory string does not round-trip: %v %v", v, err)
	}
}

func TestSuggestMinimumsAndOOM(t *testing.T) {
	var runs []models.JobRunWithSummary
	for i := 0; i < 5; i++ {
		runs = append(runs, measuredRun("completed", 10, 20*mi))
	}
	got := Suggest(runs)
	if got.CPURequest != "100m" || got.MemoryLimit != "128Mi" {
		t.Fatalf("minimums = %q %q", got.CPURequest, got.MemoryLimit)
	}

	oom := measuredRun("failed", 10, 4096*mi)
	class := models.FailureClassOOM
	oom.Job.FailureClass = &class
	got = Suggest(append([]models.JobRunWithSummary{oom}, runs...))
	if got.OOMRuns != 1 || got.MemoryLimit != "6Gi" {
		t.Fatalf("an OOM run must raise memory above its peak: %+v", got)
	}
}

func TestFormatQuantities(t *testing.T) {
	cases := map[string]string{FormatCPU(2000): "2", FormatCPU(750): "750m", FormatMemory(2048 * mi): "2Gi", FormatMemory(1536 * mi): "1536Mi", FormatMemory(1000): "1000"}
	for got, want := range cases {
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	}
	if percentile95(nil) != 0 || percentile95([]int64{7}) != 7 {
		t.Fatal("percentile95 edge cases")
	}
}
