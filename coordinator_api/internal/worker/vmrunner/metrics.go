package vmrunner

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"

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

const macOSMetricsCommandWithoutStorage = `cpu=$(ps -A -o %cpu= | awk '{s+=$1} END {printf "%.2f", s+0}'); load=$(sysctl -n vm.loadavg | awk '{print $2}'); mt=$(sysctl -n hw.memsize); ncpu=$(sysctl -n hw.ncpu); fp=$(memory_pressure -Q | awk -F': ' '/free percentage/ {gsub(/%/, "", $2); print $2}'); mu=$(awk -v t="$mt" -v f="$fp" 'BEGIN {printf "%.0f", t*(100-f)/100}'); printf '%s\t%s\t%s\t%s\t0\t0\t%s\n' "$cpu" "$load" "$mu" "$mt" "$ncpu"`

const windowsMetricsPreamble = `Add-Type -TypeDefinition 'using System;using System.Runtime.InteropServices;public static class RCPerf{[StructLayout(LayoutKind.Sequential)]public class M{public uint length=64;public uint load;public ulong total;public ulong avail;public ulong totalPage;public ulong availPage;public ulong totalVirt;public ulong availVirt;public ulong availExt;}[DllImport("kernel32.dll",SetLastError=true)]public static extern bool GlobalMemoryStatusEx([In,Out] M status);}'; $ncpu=[uint64]$env:NUMBER_OF_PROCESSORS; $cpu1=(Get-Process | Measure-Object CPU -Sum).Sum; Start-Sleep -Milliseconds 250; $cpu2=(Get-Process | Measure-Object CPU -Sum).Sum; $cpuText=[Math]::Round(($cpu2-$cpu1)*400,2).ToString([Globalization.CultureInfo]::InvariantCulture); $mem=New-Object RCPerf+M; if(-not [RCPerf]::GlobalMemoryStatusEx($mem)){throw 'GlobalMemoryStatusEx failed'}; $mt=[uint64]$mem.total; $mu=$mt-[uint64]$mem.avail; $committed=[uint64]$mem.totalPage-[uint64]$mem.availPage; $swap=[Math]::Max(0,$committed-$mu); `

const windowsMetricsCommand = windowsMetricsPreamble + `$disk=New-Object IO.DriveInfo 'C'; $dt=[uint64]$disk.TotalSize; $du=$dt-[uint64]$disk.AvailableFreeSpace; Write-Output ([string]::Join([char]9,@($cpuText,0,$mu,$mt,$du,$dt,$committed,$swap,$ncpu)))`

const windowsMetricsCommandWithoutStorage = windowsMetricsPreamble + `Write-Output ([string]::Join([char]9,@($cpuText,0,$mu,$mt,0,0,$committed,$swap,$ncpu)))`

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
	sample, err := r.collectResourceSample(ctx, jobID, job, true)
	if err != nil {
		logging.Log.WithError(err).WithField("job_id", jobID).Debug("failed to collect VM metrics sample")
		return
	}
	encoded, err := json.Marshal(sample)
	if err != nil {
		return
	}
	_, _ = fmt.Fprintf(dst, "%s\n", encoded)
}

func (r *VMRunner) collectResourceSample(ctx context.Context, jobID string, job *vmJob, includeStorage bool) (ResourceSample, error) {
	sampleCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	platform := job.platform
	if platform == "" {
		if runtime.GOOS == "windows" {
			platform = GuestPlatformWindows
		} else {
			platform = GuestPlatformPOSIX
		}
	}
	metricsCommand := macOSMetricsCommandWithoutStorage
	if includeStorage {
		metricsCommand = macOSMetricsCommand
	}
	command := []string{"/bin/sh", "-c", metricsCommand}
	if platform == GuestPlatformWindows {
		metricsCommand = windowsMetricsCommandWithoutStorage
		if includeStorage {
			metricsCommand = windowsMetricsCommand
		}
		command = []string{"powershell.exe", "-NoProfile", "-NonInteractive", "-EncodedCommand", encodePowerShellCommand(metricsCommand)}
	}
	session, err := r.transport.Start(sampleCtx, job.addr, job.creds, GuestCommand{Platform: platform, Args: command})
	if err != nil {
		return ResourceSample{}, fmt.Errorf("start guest metrics sample: %w", err)
	}
	stdout, readErr := io.ReadAll(io.LimitReader(session.Stdout(), 16*1024))
	stderr, stderrReadErr := io.ReadAll(io.LimitReader(session.Stderr(), 16*1024))
	_, waitErr := session.Wait()
	_ = session.Close()
	if readErr != nil {
		return ResourceSample{}, fmt.Errorf("read guest metrics sample: %w", readErr)
	}
	if stderrReadErr != nil {
		return ResourceSample{}, fmt.Errorf("read guest metrics error output: %w", stderrReadErr)
	}
	if waitErr != nil {
		return ResourceSample{}, fmt.Errorf("wait for guest metrics sample: %w", waitErr)
	}
	sample, err := parseResourceSample(strings.TrimSpace(string(stdout)), jobID, time.Now().UTC())
	if err != nil {
		return ResourceSample{}, fmt.Errorf("parse guest metrics sample: %w", err)
	}
	if sample.MemoryTotalBytes == 0 {
		return ResourceSample{}, fmt.Errorf("guest metrics reported zero total memory: %s", strings.TrimSpace(string(stderr)))
	}
	if includeStorage && sample.StorageTotalBytes == 0 {
		return ResourceSample{}, fmt.Errorf("guest metrics reported zero total storage: %s", strings.TrimSpace(string(stderr)))
	}
	return sample, nil
}

func encodePowerShellCommand(command string) string {
	units := utf16.Encode([]rune(command))
	bytes := make([]byte, len(units)*2)
	for i, unit := range units {
		bytes[i*2] = byte(unit)
		bytes[i*2+1] = byte(unit >> 8)
	}
	return base64.StdEncoding.EncodeToString(bytes)
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

// SampleResource collects one resource sample directly from a running guest.
func (r *VMRunner) SampleResource(ctx context.Context, jobID string, includeStorage bool) (ResourceSample, error) {
	job, ok := r.getJob(jobID)
	if !ok {
		return ResourceSample{}, fmt.Errorf("vmrunner: unknown job %q", jobID)
	}
	return r.collectResourceSample(ctx, jobID, job, includeStorage)
}
