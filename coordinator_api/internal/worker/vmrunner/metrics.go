package vmrunner

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/catalystcommunity/app-utils-go/logging"
)

// ResourceSample is one point in a VM job's local metrics stream.
type ResourceSample struct {
	Timestamp            time.Time `json:"timestamp"`
	JobID                string    `json:"job_id"`
	CPUPercent           float64   `json:"cpu_percent"`
	CPUCount             uint64    `json:"cpu_count,omitempty"`
	Load1                float64   `json:"load_1"`
	MemoryUsedBytes      uint64    `json:"memory_used_bytes"`
	MemoryTotalBytes     uint64    `json:"memory_total_bytes"`
	StorageUsedBytes     uint64    `json:"storage_used_bytes"`
	StorageTotalBytes    uint64    `json:"storage_total_bytes"`
	MemoryCommittedBytes uint64    `json:"memory_committed_bytes,omitempty"`
	SwapUsedBytes        uint64    `json:"swap_used_bytes,omitempty"`
}

const macOSMetricsCommand = `cpu=$(ps -A -o %cpu= | awk '{s+=$1} END {printf "%.2f", s+0}'); load=$(sysctl -n vm.loadavg | awk '{print $2}'); mt=$(sysctl -n hw.memsize); ncpu=$(sysctl -n hw.ncpu); fp=$(memory_pressure -Q | awk -F': ' '/free percentage/ {gsub(/%/, "", $2); print $2}'); mu=$(awk -v t="$mt" -v f="$fp" 'BEGIN {printf "%.0f", t*(100-f)/100}'); disk=$(df -k / | awk 'NR==2 {printf "%.0f %.0f", $2*1024, $3*1024}'); set -- $disk; printf '%s\t%s\t%s\t%s\t%s\t%s\t%s\n' "$cpu" "$load" "$mu" "$mt" "$2" "$1" "$ncpu"`

const windowsMetricsCommand = `$cpu=(Get-Counter '\Processor(_Total)\% Processor Time').CounterSamples.CookedValue; $os=Get-CimInstance Win32_OperatingSystem; $disk=Get-CimInstance Win32_LogicalDisk -Filter "DeviceID='C:'"; $mt=[uint64]$os.TotalVisibleMemorySize*1024; $mu=$mt-([uint64]$os.FreePhysicalMemory*1024); $committed=([uint64]$os.TotalVirtualMemorySize-[uint64]$os.FreeVirtualMemory)*1024; $swap=[Math]::Max(0,$committed-$mu); $du=[uint64]$disk.Size-[uint64]$disk.FreeSpace; Write-Output ([string]::Join([char]9,@(("{0:F2}" -f $cpu),0,$mu,$mt,$du,[uint64]$disk.Size,$committed,$swap,[uint64]$env:NUMBER_OF_PROCESSORS)))`

func (r *VMRunner) startMetrics(jobID string, job *vmJob) {
	if err := os.MkdirAll(r.metricsDir, 0o700); err != nil {
		logging.Log.WithError(err).WithField("job_id", jobID).Warn("failed to create VM metrics directory")
		return
	}
	path := filepath.Join(r.metricsDir, safeMetricName(jobID)+".jsonl")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		logging.Log.WithError(err).WithField("job_id", jobID).Warn("failed to open VM metrics file")
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	job.metricsMu.Lock()
	job.metricsCancel = cancel
	job.metricsDone = make(chan struct{})
	job.metricsPath = path
	done := job.metricsDone
	job.metricsMu.Unlock()
	go func() {
		defer close(done)
		defer file.Close()
		r.sampleMetrics(ctx, jobID, job, file)
		ticker := time.NewTicker(r.metricsInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				r.sampleMetrics(ctx, jobID, job, file)
			}
		}
	}()
}

func (r *VMRunner) stopMetrics(job *vmJob) {
	job.metricsMu.Lock()
	cancel := job.metricsCancel
	done := job.metricsDone
	job.metricsCancel = nil
	job.metricsDone = nil
	job.metricsMu.Unlock()
	if cancel == nil {
		return
	}
	cancel()
	if done != nil {
		<-done
	}
}

func (r *VMRunner) sampleMetrics(ctx context.Context, jobID string, job *vmJob, dst io.Writer) {
	sampleCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	command := []string{"/bin/sh", "-c", macOSMetricsCommand}
	if runtime.GOOS == "windows" {
		command = []string{"powershell.exe", "-NoProfile", "-NonInteractive", "-Command", windowsMetricsCommand}
	}
	session, err := r.transport.Start(sampleCtx, job.addr, r.creds, command, nil)
	if err != nil {
		logging.Log.WithError(err).WithField("job_id", jobID).Debug("failed to start VM metrics sample")
		return
	}
	stdout, readErr := io.ReadAll(io.LimitReader(session.Stdout(), 16*1024))
	_, _ = io.Copy(io.Discard, io.LimitReader(session.Stderr(), 16*1024))
	_, waitErr := session.Wait()
	_ = session.Close()
	if readErr != nil || waitErr != nil {
		return
	}
	sample, err := parseResourceSample(strings.TrimSpace(string(stdout)), jobID, time.Now().UTC())
	if err != nil {
		return
	}
	encoded, err := json.Marshal(sample)
	if err != nil {
		return
	}
	_, _ = fmt.Fprintf(dst, "%s\n", encoded)
}

func parseResourceSample(line, jobID string, timestamp time.Time) (ResourceSample, error) {
	fields := strings.Split(line, "\t")
	if len(fields) != 6 && len(fields) != 7 && len(fields) != 8 && len(fields) != 9 {
		return ResourceSample{}, fmt.Errorf("expected 6 to 9 resource fields, got %d", len(fields))
	}
	cpu, err1 := strconv.ParseFloat(fields[0], 64)
	load, err2 := strconv.ParseFloat(fields[1], 64)
	memUsed, err3 := strconv.ParseUint(fields[2], 10, 64)
	memTotal, err4 := strconv.ParseUint(fields[3], 10, 64)
	diskUsed, err5 := strconv.ParseUint(fields[4], 10, 64)
	diskTotal, err6 := strconv.ParseUint(fields[5], 10, 64)
	if err := errorsJoin(err1, err2, err3, err4, err5, err6); err != nil {
		return ResourceSample{}, err
	}
	sample := ResourceSample{
		Timestamp: timestamp, JobID: jobID, CPUPercent: cpu, Load1: load,
		MemoryUsedBytes: memUsed, MemoryTotalBytes: memTotal,
		StorageUsedBytes: diskUsed, StorageTotalBytes: diskTotal,
	}
	if len(fields) == 8 || len(fields) == 9 {
		committed, err7 := strconv.ParseUint(fields[6], 10, 64)
		swap, err8 := strconv.ParseUint(fields[7], 10, 64)
		if err := errorsJoin(err7, err8); err != nil {
			return ResourceSample{}, err
		}
		sample.MemoryCommittedBytes = committed
		sample.SwapUsedBytes = swap
	}
	if len(fields) == 7 || len(fields) == 9 {
		cpuCount, countErr := strconv.ParseUint(fields[len(fields)-1], 10, 64)
		if countErr != nil {
			return ResourceSample{}, countErr
		}
		sample.CPUCount = cpuCount
	}
	return sample, nil
}

func errorsJoin(errs ...error) error {
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

// LatestResourceSample reads the most recent complete local JSONL sample.
// The adapter uses this during the transition to coordinator telemetry.
func (r *VMRunner) LatestResourceSample(jobID string) (ResourceSample, bool, error) {
	path, ok := r.MetricsPath(jobID)
	if !ok {
		return ResourceSample{}, false, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ResourceSample{}, false, err
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) == 0 || lines[len(lines)-1] == "" {
		return ResourceSample{}, false, nil
	}
	var sample ResourceSample
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &sample); err != nil {
		return ResourceSample{}, false, err
	}
	return sample, true, nil
}
