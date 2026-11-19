package resourcehistory

import (
	"fmt"
	"math"
	"sort"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

// Right-sizing rules. The suggestion is a starting value for an operator or a
// job owner. Reactorcide never changes a job's resources by itself.
const (
	// SuggestionMinRuns is the number of measured runs a suggestion needs.
	SuggestionMinRuns = 5
	// SuggestionDefaultRuns is how many of the newest runs a suggestion reads.
	SuggestionDefaultRuns = 50

	cpuHeadroomFactor    = 1.2
	memoryHeadroomFactor = 1.25
	cpuStepMillicores    = 50
	minCPUMillicores     = 100
	memoryStepBytes      = 64 * 1024 * 1024
	minMemoryBytes       = 128 * 1024 * 1024
	// oomMemoryFactor raises the memory suggestion above the peak of a run
	// that ran out of memory: the measured peak of such a run is only the
	// old limit, not the real need.
	oomMemoryFactor = 1.5
)

// Suggestion is a right-sizing suggestion for one job (all runs with the same
// organization, project, and name).
type Suggestion struct {
	// Runs is the number of measured runs the suggestion uses.
	Runs int `json:"runs"`
	// MinRuns is the number of measured runs a suggestion needs.
	MinRuns int `json:"min_runs"`
	// Sufficient is false when Runs < MinRuns. The suggested values are then
	// zero.
	Sufficient bool `json:"sufficient"`

	// CPUP95Millicores is the 95th percentile of the per-run CPU p95.
	CPUP95Millicores int64 `json:"cpu_p95_millicores"`
	// MemoryPeakP95Bytes is the 95th percentile of the per-run memory peak.
	MemoryPeakP95Bytes int64 `json:"memory_peak_p95_bytes"`
	// OOMRuns is the number of used runs that ran out of memory.
	OOMRuns int `json:"oom_runs"`

	// CPURequestMillicores is CPUP95Millicores plus 20 percent, rounded up
	// to 50m, at least 100m.
	CPURequestMillicores int64  `json:"cpu_request_millicores"`
	CPURequest           string `json:"cpu_request"`
	// MemoryLimitBytes is MemoryPeakP95Bytes plus 25 percent, rounded up to
	// 64Mi, at least 128Mi. With an OOM run it is at least 1.5 times the
	// largest peak of an OOM run.
	MemoryLimitBytes int64  `json:"memory_limit_bytes"`
	MemoryLimit      string `json:"memory_limit"`

	// CurrentCPURequestMillicores and CurrentMemoryLimitBytes are the values
	// of the newest used run, for comparison.
	CurrentCPURequestMillicores int64 `json:"current_cpu_request_millicores"`
	CurrentMemoryLimitBytes     int64 `json:"current_memory_limit_bytes"`
}

// Suggest computes a right-sizing suggestion from finished runs, newest
// first (the order of store.ListJobRunsWithSummaries). It uses runs with a
// current summary and CPU or memory samples. Cancelled runs are left out:
// they can stop before the heavy part of the job.
func Suggest(runs []models.JobRunWithSummary) Suggestion {
	out := Suggestion{MinRuns: SuggestionMinRuns}
	var cpuValues, memoryValues []int64
	var oomPeak int64
	currentSet := false
	for _, run := range runs {
		summary := run.Summary
		if !Current(summary) || summary.SampleCount == 0 || run.Job.Status == "cancelled" {
			continue
		}
		used := false
		if summary.CPUP95Millicores != nil {
			cpuValues = append(cpuValues, *summary.CPUP95Millicores)
			used = true
		}
		if summary.MemoryMaxBytes != nil {
			memoryValues = append(memoryValues, *summary.MemoryMaxBytes)
			used = true
			if run.Job.FailureClass != nil && *run.Job.FailureClass == models.FailureClassOOM {
				out.OOMRuns++
				oomPeak = max(oomPeak, *summary.MemoryMaxBytes)
			}
		}
		if !used {
			continue
		}
		out.Runs++
		if !currentSet {
			currentSet = true
			if summary.CPURequestMillicores != nil {
				out.CurrentCPURequestMillicores = *summary.CPURequestMillicores
			}
			if summary.MemoryLimitBytes != nil {
				out.CurrentMemoryLimitBytes = *summary.MemoryLimitBytes
			}
		}
	}
	out.CPUP95Millicores = percentile95(cpuValues)
	out.MemoryPeakP95Bytes = percentile95(memoryValues)
	out.Sufficient = out.Runs >= SuggestionMinRuns && len(cpuValues) > 0 && len(memoryValues) > 0
	if !out.Sufficient {
		return out
	}
	out.CPURequestMillicores = roundUp(int64(math.Ceil(float64(out.CPUP95Millicores)*cpuHeadroomFactor)), cpuStepMillicores, minCPUMillicores)
	memory := int64(math.Ceil(float64(out.MemoryPeakP95Bytes) * memoryHeadroomFactor))
	if out.OOMRuns > 0 {
		memory = max(memory, int64(math.Ceil(float64(oomPeak)*oomMemoryFactor)))
	}
	out.MemoryLimitBytes = roundUp(memory, memoryStepBytes, minMemoryBytes)
	out.CPURequest = FormatCPU(out.CPURequestMillicores)
	out.MemoryLimit = FormatMemory(out.MemoryLimitBytes)
	return out
}

// percentile95 is the nearest-rank 95th percentile. It returns 0 for no
// values.
func percentile95(values []int64) int64 {
	if len(values) == 0 {
		return 0
	}
	sorted := append([]int64(nil), values...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	rank := int(math.Ceil(0.95 * float64(len(sorted))))
	return sorted[max(rank, 1)-1]
}

func roundUp(value, step, minimum int64) int64 {
	if value < minimum {
		return minimum
	}
	if remainder := value % step; remainder != 0 {
		value += step - remainder
	}
	return value
}

// FormatCPU renders millicores as a quantity string that internal/resources
// accepts: whole cores as "2", others as "750m".
func FormatCPU(millicores int64) string {
	if millicores%1000 == 0 {
		return fmt.Sprintf("%d", millicores/1000)
	}
	return fmt.Sprintf("%dm", millicores)
}

// FormatMemory renders bytes as a binary quantity string: "2Gi" when whole
// gibibytes, else "1536Mi" (or bytes when not whole mebibytes).
func FormatMemory(bytes int64) string {
	const mi = 1024 * 1024
	switch {
	case bytes%(1024*mi) == 0:
		return fmt.Sprintf("%dGi", bytes/(1024*mi))
	case bytes%mi == 0:
		return fmt.Sprintf("%dMi", bytes/mi)
	default:
		return fmt.Sprintf("%d", bytes)
	}
}
