package worker

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/catalystcommunity/app-utils-go/logging"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

// Config holds the configuration for the worker
type Config struct {
	QueueName    string
	PollInterval time.Duration
	Concurrency  int
	DryRun       bool
	Store        store.Store
	WorkerID     string // Unique identifier for this worker instance
}

// Worker represents a job processing worker
type Worker struct {
	config *Config

	// Job processing
	jobChan   chan *models.Job
	processor *JobProcessor

	// Concurrency control
	wg         sync.WaitGroup
	workerPool chan struct{}

	// Lifecycle management
	lifecycle *LifecycleManager

	// Resource monitoring
	monitor *ResourceMonitor
}

// New creates a new worker instance
func New(config *Config) *Worker {
	// Generate worker ID if not provided
	if config.WorkerID == "" {
		config.WorkerID = fmt.Sprintf("worker-%d", time.Now().Unix())
	}

	// Create resource monitor
	monitor, err := NewResourceMonitor(config.WorkerID, config.Concurrency)
	if err != nil {
		logging.Log.WithError(err).Warn("Failed to create resource monitor, continuing without monitoring")
		monitor = nil
	}

	return &Worker{
		config:     config,
		jobChan:    make(chan *models.Job, config.Concurrency*2), // Buffered channel
		processor:  NewJobProcessor(config.Store, config.DryRun),
		workerPool: make(chan struct{}, config.Concurrency),
		lifecycle:  NewLifecycleManager(config.Store),
		monitor:    monitor,
	}
}

// Start begins the worker's job processing loop
func (w *Worker) Start(ctx context.Context) error {
	logging.Log.WithField("worker_id", w.config.WorkerID).Info("Worker starting...")

	// Set up signal handlers for graceful shutdown
	workerCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	w.lifecycle.SetupSignalHandlers(workerCtx, cancel)

	// Start resource monitoring if available
	if w.monitor != nil {
		w.monitor.Start(workerCtx)
		defer w.monitor.Stop()

		// Log metrics periodically
		go w.logMetricsPeriodically(workerCtx)
	}

	// Recover any stuck jobs from previous runs
	if err := w.lifecycle.RecoverJobs(workerCtx, w.config.WorkerID); err != nil {
		logging.Log.WithError(err).Warn("Failed to recover stuck jobs")
		// Continue anyway - don't fail startup due to recovery issues
	}

	// Start job processing goroutines
	for i := 0; i < w.config.Concurrency; i++ {
		w.wg.Add(1)
		go w.jobWorker(workerCtx, i)
	}

	// Start job polling goroutine
	w.wg.Add(1)
	go w.jobPoller(workerCtx)

	// Wait for all goroutines to finish
	w.wg.Wait()

	// Perform final cleanup
	if err := w.lifecycle.GracefulShutdown(workerCtx); err != nil {
		logging.Log.WithError(err).Error("Error during final cleanup")
	}

	logging.Log.WithField("worker_id", w.config.WorkerID).Info("Worker stopped")
	return nil
}

// jobPoller continuously polls for new jobs and sends them to the job channel
func (w *Worker) jobPoller(ctx context.Context) {
	defer w.wg.Done()

	ticker := time.NewTicker(w.config.PollInterval)
	defer ticker.Stop()

	logging.Log.Infof("Job poller started with interval %v", w.config.PollInterval)

	for {
		select {
		case <-ctx.Done():
			logging.Log.Info("Job poller stopping due to context cancellation")
			close(w.jobChan)
			return
		case <-ticker.C:
			w.pollForJobs(ctx)
		}
	}
}

// pollForJobs queries the database for pending jobs
func (w *Worker) pollForJobs(ctx context.Context) {
	// Don't poll for new jobs if we're shutting down
	if w.lifecycle.IsShuttingDown() {
		logging.Log.Debug("Skipping job poll due to shutdown")
		return
	}

	// Query for jobs in submitted or queued status for this queue
	filters := map[string]interface{}{
		"status":     "submitted", // Start with submitted jobs
		"queue_name": w.config.QueueName,
	}

	jobs, err := w.config.Store.ListJobs(ctx, filters, w.config.Concurrency*2, 0)
	if err != nil {
		logging.Log.WithError(err).Error("Failed to query for jobs")
		return
	}

	if len(jobs) > 0 {
		logging.Log.Infof("Found %d pending jobs", len(jobs))
	}

	for _, job := range jobs {
		select {
		case w.jobChan <- &job:
			// Job sent to processing channel
		case <-ctx.Done():
			return
		default:
			// Channel is full, skip this job for now
			logging.Log.Warnf("Job channel full, skipping job %s", job.JobID)
			break
		}
	}
}

// jobWorker processes jobs from the job channel
func (w *Worker) jobWorker(ctx context.Context, workerID int) {
	defer w.wg.Done()

	logging.Log.Infof("Job worker %d started", workerID)

	for {
		select {
		case <-ctx.Done():
			logging.Log.Infof("Job worker %d stopping due to context cancellation", workerID)
			return
		case job, ok := <-w.jobChan:
			if !ok {
				logging.Log.Infof("Job worker %d stopping - job channel closed", workerID)
				return
			}

			// Acquire worker slot
			w.workerPool <- struct{}{}

			// Process the job
			w.processJob(ctx, job, workerID)

			// Release worker slot
			<-w.workerPool
		}
	}
}

// processJob handles the execution of a single job
func (w *Worker) processJob(ctx context.Context, job *models.Job, workerID int) {
	logger := logging.Log.WithField("job_id", job.JobID).WithField("worker_id", workerID)
	logger.Info("Processing job")

	// Try to claim the job (update status from submitted to running)
	if !w.claimJob(ctx, job) {
		logger.Warn("Failed to claim job - may have been picked up by another worker")
		return
	}

	// Track job start in metrics
	if w.monitor != nil {
		w.monitor.RecordJobStart(job.JobID)
	}

	// Create a cancellable context for this job
	jobCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Register job with lifecycle manager
	// Note: We don't have workDir yet, will be set by processor
	w.lifecycle.RegisterJob(job, "", cancel)
	defer w.lifecycle.UnregisterJob(job.JobID)

	// Execute the job using the processor
	result := w.processor.ProcessJob(jobCtx, job)

	// Track job completion in metrics
	if w.monitor != nil {
		w.monitor.RecordJobComplete(job.JobID, result.ExitCode == 0)
	}

	// If job failed, perform cleanup
	if result.ExitCode != 0 {
		w.lifecycle.JobCleanupOnFailure(job.JobID, fmt.Errorf("job failed with exit code %d", result.ExitCode))
	}

	// Update job status based on result
	if err := w.updateJobResult(ctx, job, result); err != nil {
		logger.WithError(err).Error("Failed to update job result")
	}

	logger.WithField("status", job.Status).
		WithField("exit_code", result.ExitCode).
		WithField("retry_count", result.RetryCount).
		Info("Job processing completed")
}

// claimJob attempts to claim a job by updating its status to running
func (w *Worker) claimJob(ctx context.Context, job *models.Job) bool {
	// First, get the current job from database to ensure it's still claimable
	currentJob, err := w.config.Store.GetJobByID(ctx, job.JobID)
	if err != nil {
		logging.Log.WithError(err).WithField("job_id", job.JobID).Error("Failed to get current job status")
		return false
	}

	// Only claim if job is still in submitted status
	if currentJob.Status != "submitted" {
		logging.Log.WithField("job_id", job.JobID).WithField("current_status", currentJob.Status).
			Warn("Job status changed, cannot claim")
		return false
	}

	// Update job status to running with timestamp and worker ID
	now := time.Now().UTC()
	currentJob.Status = "running"
	currentJob.StartedAt = &now
	currentJob.WorkerID = &w.config.WorkerID

	if err := w.config.Store.UpdateJob(ctx, currentJob); err != nil {
		logging.Log.WithError(err).WithField("job_id", job.JobID).Error("Failed to claim job")
		return false
	}

	// Update the local job object
	job.Status = currentJob.Status
	job.StartedAt = currentJob.StartedAt
	job.WorkerID = currentJob.WorkerID

	return true
}

// updateJobResult updates the job with the execution result
func (w *Worker) updateJobResult(ctx context.Context, job *models.Job, result *JobResult) error {
	now := time.Now().UTC()
	job.CompletedAt = &now
	job.ExitCode = &result.ExitCode

	// Set status based on exit code
	if result.ExitCode == 0 {
		job.Status = "completed"
	} else {
		job.Status = "failed"
	}

	// Update retry count and error information
	job.RetryCount = result.RetryCount
	if result.Error != "" {
		job.LastError = result.Error
	}

	// Set object store keys if available
	if result.LogsObjectKey != "" {
		job.LogsObjectKey = result.LogsObjectKey
	}
	if result.ArtifactsObjectKey != "" {
		job.ArtifactsObjectKey = result.ArtifactsObjectKey
	}

	return w.config.Store.UpdateJob(ctx, job)
}

// logMetricsPeriodically logs resource metrics at regular intervals
func (w *Worker) logMetricsPeriodically(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Minute) // Log every 5 minutes
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if w.monitor != nil {
				w.monitor.LogMetricsSummary()
			}
		}
	}
}
