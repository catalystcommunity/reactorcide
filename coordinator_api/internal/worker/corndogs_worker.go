package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/catalystcommunity/app-utils-go/logging"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/corndogs"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/metrics"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/secrets"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
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
	// Default container runtime to auto-detect if not specified
	if config.ContainerRuntime == "" {
		config.ContainerRuntime = "auto"
	}

	// Set default log chunk interval if not specified
	if config.LogChunkInterval == 0 {
		config.LogChunkInterval = 3 * time.Second
	}

	// Set default heartbeat interval if not specified
	if config.HeartbeatInterval == 0 {
		config.HeartbeatInterval = 30 * time.Second
	}

	// Set default heartbeat timeout if not specified
	if config.HeartbeatTimeout == 0 {
		config.HeartbeatTimeout = 10 * time.Minute
	}

	// Create job runner
	runner, err := NewJobRunner(config.ContainerRuntime)
	if err != nil {
		logging.Log.WithError(err).WithField("runtime", config.ContainerRuntime).
			Fatal("Failed to create job runner")
	}

	// Determine secrets storage type from environment
	secretsStorageType := os.Getenv("REACTORCIDE_SECRETS_STORAGE_TYPE")
	if secretsStorageType == "" {
		secretsStorageType = "database" // Default to database
	}

	// Load master keys for database-backed secrets
	var keyManager *secrets.MasterKeyManager
	if secretsStorageType == "database" {
		db := store.GetDB()
		if db != nil {
			var err error
			keyManager, err = secrets.LoadOrCreateMasterKeys(db)
			if err != nil {
				logging.Log.WithError(err).Warn("Failed to load master keys - secrets resolution will be unavailable")
			} else {
				logging.Log.Info("Master keys loaded for secrets resolution")
			}
		} else {
			logging.Log.Warn("Database not available - secrets resolution will be unavailable")
		}
	}

	// Create job processor with configuration
	processor := NewJobProcessorWithConfig(config.Store, runner, config.DryRun, &JobProcessorConfig{
		ObjectStore:        config.ObjectStore,
		LogChunkInterval:   config.LogChunkInterval,
		HeartbeatInterval:  config.HeartbeatInterval,
		HeartbeatTimeout:   config.HeartbeatTimeout,
		SecretsKeyManager:  keyManager,
		SecretsStorageType: secretsStorageType,
	})

	return &CornDogsWorker{
		config:         config,
		corndogsClient: corndogsClient,
		processor:      processor,
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

	// Check if task is nil (no tasks available)
	if task == nil {
		logger.Debug("No tasks available in queue")
		metrics.RecordCornDogsTaskPoll(w.config.QueueName, false)
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

	// Create heartbeat function that extends the Corndogs task timeout
	heartbeatFunc := func(hbCtx context.Context) error {
		timeoutExtension := int64(w.config.HeartbeatTimeout.Seconds())
		_, err := w.corndogsClient.SendHeartbeat(hbCtx, task.Uuid, "processing", timeoutExtension)
		return err
	}

	// Execute the job using the processor with heartbeat support
	execCtx := &JobExecutionContext{
		HeartbeatFunc: heartbeatFunc,
	}

	startTime := time.Now()
	result := w.processor.ProcessJobWithContext(ctx, job, execCtx)
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
