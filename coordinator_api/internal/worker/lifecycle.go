package worker

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/catalystcommunity/app-utils-go/logging"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

// LifecycleManager handles worker lifecycle events
type LifecycleManager struct {
	store           store.Store
	cleanupTimeout  time.Duration
	shutdownTimeout time.Duration
	activeJobs      map[string]*JobContext
	mu              sync.RWMutex
	shutdownCh      chan struct{}
	cleanupWg       sync.WaitGroup
}

// JobContext tracks the context of an active job
type JobContext struct {
	Job       *models.Job
	StartTime time.Time
	WorkDir   string
	Cancel    context.CancelFunc
}

// NewLifecycleManager creates a new lifecycle manager
func NewLifecycleManager(store store.Store) *LifecycleManager {
	return &LifecycleManager{
		store:           store,
		cleanupTimeout:  30 * time.Second,
		shutdownTimeout: 60 * time.Second,
		activeJobs:      make(map[string]*JobContext),
		shutdownCh:      make(chan struct{}),
	}
}

// RegisterJob registers a job as active
func (lm *LifecycleManager) RegisterJob(job *models.Job, workDir string, cancel context.CancelFunc) {
	lm.mu.Lock()
	defer lm.mu.Unlock()

	lm.activeJobs[job.JobID] = &JobContext{
		Job:       job,
		StartTime: time.Now(),
		WorkDir:   workDir,
		Cancel:    cancel,
	}

	logging.Log.WithField("job_id", job.JobID).
		WithField("active_jobs", len(lm.activeJobs)).
		Info("Job registered with lifecycle manager")
}

// UnregisterJob removes a job from active tracking
func (lm *LifecycleManager) UnregisterJob(jobID string) {
	lm.mu.Lock()
	defer lm.mu.Unlock()

	if jobCtx, exists := lm.activeJobs[jobID]; exists {
		// Cleanup work directory if it exists
		if jobCtx.WorkDir != "" {
			lm.cleanupWorkDir(jobCtx.WorkDir)
		}

		delete(lm.activeJobs, jobID)
		logging.Log.WithField("job_id", jobID).
			WithField("active_jobs", len(lm.activeJobs)).
			Info("Job unregistered from lifecycle manager")
	}
}

// GetActiveJobs returns a list of currently active job IDs
func (lm *LifecycleManager) GetActiveJobs() []string {
	lm.mu.RLock()
	defer lm.mu.RUnlock()

	jobIDs := make([]string, 0, len(lm.activeJobs))
	for jobID := range lm.activeJobs {
		jobIDs = append(jobIDs, jobID)
	}
	return jobIDs
}

// RecoverJobs checks for jobs that were running when the worker stopped
func (lm *LifecycleManager) RecoverJobs(ctx context.Context, workerID string) error {
	logging.Log.Info("Starting job recovery process")

	// Query for jobs that are in "running" state for this worker
	filters := map[string]interface{}{
		"status":    "running",
		"worker_id": workerID,
	}

	stuckJobs, err := lm.store.ListJobs(ctx, filters, 100, 0)
	if err != nil {
		return fmt.Errorf("failed to query for stuck jobs: %w", err)
	}

	if len(stuckJobs) == 0 {
		logging.Log.Info("No stuck jobs found")
		return nil
	}

	logging.Log.WithField("count", len(stuckJobs)).Info("Found stuck jobs to recover")

	for _, job := range stuckJobs {
		if err := lm.recoverJob(ctx, &job); err != nil {
			logging.Log.WithField("job_id", job.JobID).
				WithError(err).
				Error("Failed to recover job")
			// Continue recovering other jobs even if one fails
		}
	}

	return nil
}

// recoverJob handles recovery of a single stuck job
func (lm *LifecycleManager) recoverJob(ctx context.Context, job *models.Job) error {
	logger := logging.Log.WithField("job_id", job.JobID)
	logger.Info("Recovering stuck job")

	// Update job status back to "submitted" so it can be retried
	// You might want different logic here depending on your requirements
	now := time.Now().UTC()
	job.Status = "submitted"
	job.WorkerID = nil
	job.StartedAt = nil
	job.CompletedAt = &now

	// Add a note about the recovery
	if job.Notes == "" {
		job.Notes = "Job recovered after worker restart"
	} else {
		job.Notes += "; Job recovered after worker restart"
	}

	if err := lm.store.UpdateJob(ctx, job); err != nil {
		return fmt.Errorf("failed to update recovered job: %w", err)
	}

	logger.Info("Job recovered and reset to submitted status")
	return nil
}

// GracefulShutdown handles graceful shutdown of the worker
func (lm *LifecycleManager) GracefulShutdown(ctx context.Context) error {
	logging.Log.Info("Initiating graceful shutdown")

	// Signal shutdown to prevent new jobs
	close(lm.shutdownCh)

	// Create shutdown context with timeout
	shutdownCtx, cancel := context.WithTimeout(ctx, lm.shutdownTimeout)
	defer cancel()

	// Cancel all active jobs
	lm.cancelActiveJobs()

	// Wait for active jobs to complete or timeout
	done := make(chan struct{})
	go func() {
		lm.waitForActiveJobs()
		close(done)
	}()

	select {
	case <-done:
		logging.Log.Info("All active jobs completed")
	case <-shutdownCtx.Done():
		logging.Log.Warn("Shutdown timeout reached, forcing termination")
		lm.forceCleanup()
	}

	// Wait for cleanup operations
	cleanupDone := make(chan struct{})
	go func() {
		lm.cleanupWg.Wait()
		close(cleanupDone)
	}()

	select {
	case <-cleanupDone:
		logging.Log.Info("Cleanup completed")
	case <-time.After(lm.cleanupTimeout):
		logging.Log.Warn("Cleanup timeout reached")
	}

	logging.Log.Info("Graceful shutdown completed")
	return nil
}

// cancelActiveJobs sends cancellation signal to all active jobs
func (lm *LifecycleManager) cancelActiveJobs() {
	lm.mu.RLock()
	defer lm.mu.RUnlock()

	for jobID, jobCtx := range lm.activeJobs {
		logging.Log.WithField("job_id", jobID).Info("Cancelling active job")
		if jobCtx.Cancel != nil {
			jobCtx.Cancel()
		}
	}
}

// waitForActiveJobs waits for all active jobs to complete
func (lm *LifecycleManager) waitForActiveJobs() {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		lm.mu.RLock()
		activeCount := len(lm.activeJobs)
		lm.mu.RUnlock()

		if activeCount == 0 {
			break
		}

		logging.Log.WithField("active_jobs", activeCount).
			Info("Waiting for active jobs to complete")

		<-ticker.C
	}
}

// forceCleanup forcibly cleans up remaining jobs
func (lm *LifecycleManager) forceCleanup() {
	lm.mu.Lock()
	defer lm.mu.Unlock()

	for jobID, jobCtx := range lm.activeJobs {
		logging.Log.WithField("job_id", jobID).Warn("Force cleaning up job")

		// Update job status to failed
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		now := time.Now().UTC()
		jobCtx.Job.Status = "failed"
		jobCtx.Job.CompletedAt = &now
		jobCtx.Job.Notes = "Job terminated due to worker shutdown"

		if err := lm.store.UpdateJob(ctx, jobCtx.Job); err != nil {
			logging.Log.WithField("job_id", jobID).
				WithError(err).
				Error("Failed to update job status during force cleanup")
		}

		// Cleanup work directory
		if jobCtx.WorkDir != "" {
			lm.cleanupWorkDir(jobCtx.WorkDir)
		}
	}

	// Clear all active jobs
	lm.activeJobs = make(map[string]*JobContext)
}

// cleanupWorkDir removes a job's work directory
func (lm *LifecycleManager) cleanupWorkDir(workDir string) {
	lm.cleanupWg.Add(1)
	go func() {
		defer lm.cleanupWg.Done()

		if workDir == "" {
			return
		}

		logging.Log.WithField("work_dir", workDir).Debug("Cleaning up work directory")

		if err := os.RemoveAll(workDir); err != nil {
			logging.Log.WithField("work_dir", workDir).
				WithError(err).
				Warn("Failed to cleanup work directory")
		}
	}()
}

// IsShuttingDown checks if the lifecycle manager is shutting down
func (lm *LifecycleManager) IsShuttingDown() bool {
	select {
	case <-lm.shutdownCh:
		return true
	default:
		return false
	}
}

// SetupSignalHandlers sets up OS signal handlers for graceful shutdown
func (lm *LifecycleManager) SetupSignalHandlers(ctx context.Context, cancel context.CancelFunc) {
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		select {
		case sig := <-sigCh:
			logging.Log.WithField("signal", sig).Info("Received shutdown signal")

			// Trigger graceful shutdown
			if err := lm.GracefulShutdown(ctx); err != nil {
				logging.Log.WithError(err).Error("Error during graceful shutdown")
			}

			// Cancel the main context
			cancel()
		case <-ctx.Done():
			// Context cancelled, cleanup already handled
		}
	}()
}

// JobCleanupOnFailure handles cleanup when a job fails
func (lm *LifecycleManager) JobCleanupOnFailure(jobID string, err error) {
	lm.mu.RLock()
	jobCtx, exists := lm.activeJobs[jobID]
	lm.mu.RUnlock()

	if !exists {
		return
	}

	logger := logging.Log.WithField("job_id", jobID).WithError(err)
	logger.Info("Performing cleanup for failed job")

	// Cleanup work directory
	if jobCtx.WorkDir != "" {
		lm.cleanupWorkDir(jobCtx.WorkDir)
	}

	// You could add additional cleanup logic here:
	// - Release resources
	// - Send notifications
	// - Update metrics
	// - Clean up temporary files outside work directory

	logger.Info("Job failure cleanup completed")
}
