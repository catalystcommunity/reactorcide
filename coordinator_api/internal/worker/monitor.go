package worker

import (
	"context"
	"runtime"
	"sync"
	"time"

	"github.com/catalystcommunity/app-utils-go/logging"
	"github.com/shirou/gopsutil/v3/cpu"
	"github.com/shirou/gopsutil/v3/mem"
	"github.com/shirou/gopsutil/v3/process"
)

// ResourceMetrics holds current resource usage metrics
type ResourceMetrics struct {
	Timestamp time.Time `json:"timestamp"`

	// CPU metrics
	CPUPercent float64 `json:"cpu_percent"`
	CPUCores   int     `json:"cpu_cores"`
	GoRoutines int     `json:"go_routines"`

	// Memory metrics
	MemoryUsedMB  uint64  `json:"memory_used_mb"`
	MemoryTotalMB uint64  `json:"memory_total_mb"`
	MemoryPercent float64 `json:"memory_percent"`
	HeapAllocMB   uint64  `json:"heap_alloc_mb"`
	HeapSysMB     uint64  `json:"heap_sys_mb"`

	// Job metrics
	ActiveJobs     int   `json:"active_jobs"`
	MaxConcurrency int   `json:"max_concurrency"`
	JobsProcessed  int64 `json:"jobs_processed"`
	JobsFailed     int64 `json:"jobs_failed"`

	// Worker metrics
	WorkerID    string        `json:"worker_id"`
	Uptime      time.Duration `json:"uptime"`
	LastJobTime *time.Time    `json:"last_job_time"`
}

// ResourceMonitor monitors system resources and worker metrics
type ResourceMonitor struct {
	workerID       string
	startTime      time.Time
	interval       time.Duration
	maxConcurrency int

	// Metrics tracking
	metrics       ResourceMetrics
	mu            sync.RWMutex
	jobsProcessed int64
	jobsFailed    int64
	lastJobTime   *time.Time

	// Thresholds for alerts
	cpuThreshold    float64
	memoryThreshold float64

	// Process tracking
	process *process.Process
	stopCh  chan struct{}
	wg      sync.WaitGroup
}

// NewResourceMonitor creates a new resource monitor
func NewResourceMonitor(workerID string, maxConcurrency int) (*ResourceMonitor, error) {
	proc, err := process.NewProcess(int32(runtime.GOMAXPROCS(0)))
	if err != nil {
		// Try to get current process instead
		proc, err = process.NewProcess(int32(0)) // 0 means current process
		if err != nil {
			logging.Log.WithError(err).Warn("Failed to get process handle for monitoring")
			// Continue without process-specific monitoring
			proc = nil
		}
	}

	return &ResourceMonitor{
		workerID:        workerID,
		startTime:       time.Now(),
		interval:        30 * time.Second, // Monitor every 30 seconds
		maxConcurrency:  maxConcurrency,
		cpuThreshold:    80.0, // Alert if CPU > 80%
		memoryThreshold: 90.0, // Alert if memory > 90%
		process:         proc,
		stopCh:          make(chan struct{}),
	}, nil
}

// Start begins monitoring resources
func (rm *ResourceMonitor) Start(ctx context.Context) {
	rm.wg.Add(1)
	go rm.monitorLoop(ctx)
}

// Stop stops the resource monitor
func (rm *ResourceMonitor) Stop() {
	close(rm.stopCh)
	rm.wg.Wait()
}

// monitorLoop continuously monitors resources
func (rm *ResourceMonitor) monitorLoop(ctx context.Context) {
	defer rm.wg.Done()

	ticker := time.NewTicker(rm.interval)
	defer ticker.Stop()

	// Initial collection
	rm.collectMetrics()

	for {
		select {
		case <-ctx.Done():
			logging.Log.Info("Resource monitor stopping due to context cancellation")
			return
		case <-rm.stopCh:
			logging.Log.Info("Resource monitor stopping")
			return
		case <-ticker.C:
			rm.collectMetrics()
			rm.checkThresholds()
		}
	}
}

// collectMetrics gathers current resource metrics
func (rm *ResourceMonitor) collectMetrics() {
	rm.mu.Lock()
	defer rm.mu.Unlock()

	metrics := ResourceMetrics{
		Timestamp:      time.Now(),
		WorkerID:       rm.workerID,
		Uptime:         time.Since(rm.startTime),
		MaxConcurrency: rm.maxConcurrency,
		JobsProcessed:  rm.jobsProcessed,
		JobsFailed:     rm.jobsFailed,
		LastJobTime:    rm.lastJobTime,
		CPUCores:       runtime.NumCPU(),
		GoRoutines:     runtime.NumGoroutine(),
	}

	// Collect CPU metrics
	if cpuPercent, err := cpu.Percent(1*time.Second, false); err == nil && len(cpuPercent) > 0 {
		metrics.CPUPercent = cpuPercent[0]
	}

	// Collect memory metrics
	if vmStat, err := mem.VirtualMemory(); err == nil {
		metrics.MemoryUsedMB = vmStat.Used / 1024 / 1024
		metrics.MemoryTotalMB = vmStat.Total / 1024 / 1024
		metrics.MemoryPercent = vmStat.UsedPercent
	}

	// Collect Go runtime metrics
	var memStats runtime.MemStats
	runtime.ReadMemStats(&memStats)
	metrics.HeapAllocMB = memStats.HeapAlloc / 1024 / 1024
	metrics.HeapSysMB = memStats.HeapSys / 1024 / 1024

	rm.metrics = metrics

	// Log metrics at debug level
	logging.Log.WithField("metrics", metrics).Debug("Resource metrics collected")
}

// checkThresholds checks if any resource thresholds are exceeded
func (rm *ResourceMonitor) checkThresholds() {
	rm.mu.RLock()
	metrics := rm.metrics
	rm.mu.RUnlock()

	// Check CPU threshold
	if metrics.CPUPercent > rm.cpuThreshold {
		logging.Log.WithField("cpu_percent", metrics.CPUPercent).
			WithField("threshold", rm.cpuThreshold).
			Warn("CPU usage exceeds threshold")
	}

	// Check memory threshold
	if metrics.MemoryPercent > rm.memoryThreshold {
		logging.Log.WithField("memory_percent", metrics.MemoryPercent).
			WithField("threshold", rm.memoryThreshold).
			Warn("Memory usage exceeds threshold")
	}
}

// GetMetrics returns the current resource metrics
func (rm *ResourceMonitor) GetMetrics() ResourceMetrics {
	rm.mu.RLock()
	defer rm.mu.RUnlock()
	return rm.metrics
}

// RecordJobStart records that a job has started
func (rm *ResourceMonitor) RecordJobStart(jobID string) {
	rm.mu.Lock()
	defer rm.mu.Unlock()

	now := time.Now()
	rm.lastJobTime = &now
	rm.metrics.ActiveJobs++

	logging.Log.WithField("job_id", jobID).
		WithField("active_jobs", rm.metrics.ActiveJobs).
		Debug("Job started, updating metrics")
}

// RecordJobComplete records that a job has completed
func (rm *ResourceMonitor) RecordJobComplete(jobID string, success bool) {
	rm.mu.Lock()
	defer rm.mu.Unlock()

	rm.metrics.ActiveJobs--
	if rm.metrics.ActiveJobs < 0 {
		rm.metrics.ActiveJobs = 0
	}

	if success {
		rm.jobsProcessed++
	} else {
		rm.jobsFailed++
	}

	logging.Log.WithField("job_id", jobID).
		WithField("active_jobs", rm.metrics.ActiveJobs).
		WithField("success", success).
		Debug("Job completed, updating metrics")
}

// SetThresholds sets the resource monitoring thresholds
func (rm *ResourceMonitor) SetThresholds(cpu, memory float64) {
	rm.mu.Lock()
	defer rm.mu.Unlock()

	rm.cpuThreshold = cpu
	rm.memoryThreshold = memory

	logging.Log.WithField("cpu_threshold", cpu).
		WithField("memory_threshold", memory).
		Info("Resource thresholds updated")
}

// LogMetricsSummary logs a summary of current metrics
func (rm *ResourceMonitor) LogMetricsSummary() {
	metrics := rm.GetMetrics()

	logging.Log.WithFields(map[string]interface{}{
		"worker_id":      metrics.WorkerID,
		"uptime":         metrics.Uptime.String(),
		"cpu_percent":    metrics.CPUPercent,
		"memory_percent": metrics.MemoryPercent,
		"memory_used_mb": metrics.MemoryUsedMB,
		"heap_alloc_mb":  metrics.HeapAllocMB,
		"active_jobs":    metrics.ActiveJobs,
		"jobs_processed": metrics.JobsProcessed,
		"jobs_failed":    metrics.JobsFailed,
		"go_routines":    metrics.GoRoutines,
	}).Info("Worker resource metrics summary")
}

// IsHealthy checks if the worker is operating within healthy parameters
func (rm *ResourceMonitor) IsHealthy() bool {
	metrics := rm.GetMetrics()

	// Check if CPU is within limits
	if metrics.CPUPercent > rm.cpuThreshold {
		return false
	}

	// Check if memory is within limits
	if metrics.MemoryPercent > rm.memoryThreshold {
		return false
	}

	// Check if we have excessive goroutines (potential leak)
	if metrics.GoRoutines > 1000 {
		logging.Log.WithField("go_routines", metrics.GoRoutines).
			Warn("Excessive number of goroutines detected")
		return false
	}

	return true
}
