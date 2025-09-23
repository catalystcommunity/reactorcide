package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/catalystcommunity/app-utils-go/logging"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/corndogs"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/metrics"
)

// CornDogsWorker represents a job processing worker that uses Corndogs for task management
type CornDogsWorker struct {
	config         *Config
	corndogsClient corndogs.ClientInterface
	processor      JobProcessorInterface
	wg             sync.WaitGroup
	workerPool     chan struct{}
}

// NewCornDogsWorker creates a new worker that uses Corndogs for task management
func NewCornDogsWorker(config *Config, corndogsClient corndogs.ClientInterface) *CornDogsWorker {
	return &CornDogsWorker{
		config:         config,
		corndogsClient: corndogsClient,
		processor:      NewJobProcessor(config.Store, config.DryRun),
		workerPool:     make(chan struct{}, config.Concurrency),
	}
}

// NewCornDogsWorkerWithProcessor creates a new worker with a custom processor (for testing)
func NewCornDogsWorkerWithProcessor(config *Config, corndogsClient corndogs.ClientInterface, processor JobProcessorInterface) *CornDogsWorker {
	return &CornDogsWorker{
		config:         config,
		corndogsClient: corndogsClient,
		processor:      processor,
		workerPool:     make(chan struct{}, config.Concurrency),
	}
}

// Start begins the worker's job processing loop
func (w *CornDogsWorker) Start(ctx context.Context) error {
	logging.Log.WithFields(map[string]interface{}{
		"queue":         w.config.QueueName,
		"concurrency":   w.config.Concurrency,
		"poll_interval": w.config.PollInterval.String(),
		"worker_id":     w.config.WorkerID,
	}).Info("CornDogs Worker starting")

	// Set active workers metric
	metrics.SetWorkersActive(w.config.QueueName, float64(w.config.Concurrency))

	// Start worker goroutines
	for i := 0; i < w.config.Concurrency; i++ {
		w.wg.Add(1)
		go w.worker(ctx, i)
	}

	// Wait for all goroutines to finish
	w.wg.Wait()

	// Clear active workers metric
	metrics.SetWorkersActive(w.config.QueueName, 0)

	logging.Log.WithField("worker_id", w.config.WorkerID).Info("CornDogs Worker stopped")
	return nil
}

// worker continuously polls Corndogs for tasks and processes them
func (w *CornDogsWorker) worker(ctx context.Context, workerID int) {
	defer w.wg.Done()

	logger := logging.Log.WithField("worker_id", workerID)
	logger.Info("Worker started")

	for {
		select {
		case <-ctx.Done():
			logger.Info("Worker stopping due to context cancellation")
			return
		default:
			// Try to get the next task from Corndogs
			w.processNextTask(ctx, workerID)

			// Small delay between polls to avoid hammering Corndogs
			time.Sleep(w.config.PollInterval)
		}
	}
}

// processNextTask gets and processes the next available task from Corndogs
func (w *CornDogsWorker) processNextTask(ctx context.Context, workerID int) {
	logger := logging.Log.WithField("worker_id", workerID)

	// Get next task from Corndogs with worker timeout
	timeout := int64(3600) // 1 hour default timeout for worker execution
	if w.config.PollInterval > 0 {
		// Use a timeout based on poll interval if specified
		timeout = int64(w.config.PollInterval.Seconds() * 10)
	}

	logger.WithField("timeout", timeout).Debug("Polling for next task")
	task, err := w.corndogsClient.GetNextTask(ctx, "submitted", timeout)
	if err != nil {
		// Record poll attempt
		metrics.RecordCornDogsTaskPoll(w.config.QueueName, false)
		// This is normal when no tasks are available
		if err.Error() != "failed to get next task: rpc error: code = NotFound" {
			logger.WithError(err).Debug("No tasks available or error getting next task")
		}
		return
	}
	// Record successful poll
	metrics.RecordCornDogsTaskPoll(w.config.QueueName, true)

	// Parse the task payload
	payload, err := corndogs.ParseTaskPayload(task)
	if err != nil {
		logger.WithError(err).Error("Failed to parse task payload")
		// Update task state to failed
		w.updateTaskFailed(ctx, task.Uuid, task.CurrentState, "Failed to parse payload")
		return
	}

	logger = logger.WithField("job_id", payload.JobID).WithField("task_id", task.Uuid)
	logger.Info("Processing task from Corndogs")

	// Acquire worker slot
	w.workerPool <- struct{}{}
	defer func() { <-w.workerPool }()

	// Update active jobs metric
	workerIDStr := fmt.Sprintf("%s-%d", w.config.WorkerID, workerID)
	metrics.SetWorkerJobsActive(workerIDStr, 1)
	defer metrics.SetWorkerJobsActive(workerIDStr, 0)

	// Get the job from the database
	job, err := w.config.Store.GetJobByID(ctx, payload.JobID)
	if err != nil {
		logger.WithError(err).Error("Failed to get job from database")
		w.updateTaskFailed(ctx, task.Uuid, task.CurrentState, "Job not found in database")
		return
	}

	// Update job status to running
	now := time.Now().UTC()
	job.Status = "running"
	job.StartedAt = &now
	if err := w.config.Store.UpdateJob(ctx, job); err != nil {
		logger.WithError(err).Error("Failed to update job status to running")
		w.updateTaskFailed(ctx, task.Uuid, task.CurrentState, "Failed to update job status")
		return
	}

	// Update Corndogs task state to indicate we're processing
	_, err = w.corndogsClient.UpdateTask(ctx, task.Uuid, task.CurrentState, "processing", nil)
	if err != nil {
		logger.WithError(err).Warn("Failed to update task state to processing")
	}

	// Execute the job using the processor
	startTime := time.Now()
	result := w.processor.ProcessJob(ctx, job)
	duration := time.Since(startTime).Seconds()

	// Record job processing metrics
	status := "completed"
	if result.ExitCode != 0 {
		status = "failed"
	}
	metrics.RecordJobProcessed(w.config.QueueName, status, workerIDStr, duration)

	// Update job status based on result
	completedAt := time.Now().UTC()
	job.CompletedAt = &completedAt
	job.ExitCode = &result.ExitCode

	if result.ExitCode == 0 {
		job.Status = "completed"
		// Complete the task in Corndogs
		_, err = w.corndogsClient.CompleteTask(ctx, task.Uuid, "processing")
		if err != nil {
			logger.WithError(err).Error("Failed to complete task in Corndogs")
		}
	} else {
		job.Status = "failed"
		// Update task state to failed
		w.updateTaskFailed(ctx, task.Uuid, "processing", "Job execution failed")
	}

	// Set object store keys if available
	if result.LogsObjectKey != "" {
		job.LogsObjectKey = result.LogsObjectKey
	}
	if result.ArtifactsObjectKey != "" {
		job.ArtifactsObjectKey = result.ArtifactsObjectKey
	}

	// Update job in database
	if err := w.config.Store.UpdateJob(ctx, job); err != nil {
		logger.WithError(err).Error("Failed to update job result")
	}

	logger.WithField("status", job.Status).WithField("exit_code", result.ExitCode).Info("Task processing completed")
}

// updateTaskFailed updates a task to failed state with an error message
func (w *CornDogsWorker) updateTaskFailed(ctx context.Context, taskID, currentState, errorMsg string) {
	payload := map[string]interface{}{
		"error":     errorMsg,
		"failed_at": time.Now().UTC(),
	}
	payloadBytes, _ := json.Marshal(payload)

	_, err := w.corndogsClient.UpdateTask(ctx, taskID, currentState, "failed", payloadBytes)
	if err != nil {
		logging.Log.WithError(err).WithField("task_id", taskID).Error("Failed to update task to failed state")
	}
}
