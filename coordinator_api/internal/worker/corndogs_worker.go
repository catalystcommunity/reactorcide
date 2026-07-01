package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/catalystcommunity/app-utils-go/logging"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/corndogs"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/metrics"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/pubsub"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/secrets"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/vcs"
)

// getJobLookupAttempts and getJobLookupBackoff bound the retry loop that
// papers over the dual-write race between TriggerProcessor.createAndSubmitJob
// (INSERT job, then enqueue corndogs task) and CornDogsWorker.processNextTask
// (dequeue task, then GetJobByID). On a fast worker the corndogs task can
// become visible to a long-poll before the job INSERT has propagated to a
// fresh DB connection. 5 × 200ms exponential backoff covers ~6.2s, far more
// than any window we have observed. Vars (not consts) so tests can override
// the backoff to keep the suite fast.
var (
	getJobLookupAttempts = 5
	getJobLookupBackoff  = 200 * time.Millisecond
)

// vcsStatusUpdateAttempts and vcsStatusUpdateBackoff bound retry of the
// post-completion VCS commit-status push. Without retry, a single GitHub
// blip silently leaves the PR check stuck on "running" until reactorcide
// posts another status (which may never happen for a one-shot job).
var (
	vcsStatusUpdateAttempts = 4
	vcsStatusUpdateBackoff  = 500 * time.Millisecond
)

// CornDogsWorker represents a job processing worker that uses Corndogs for task management
type CornDogsWorker struct {
	config           *Config
	corndogsClient   corndogs.ClientInterface
	processor        JobProcessorInterface
	triggerProcessor *TriggerProcessor
	statusUpdater    vcs.JobStatusUpdaterInterface
	publisher        *pubsub.Publisher
	wg               sync.WaitGroup
	workerPool       chan struct{}
}

// SetPublisher wires a pubsub.Publisher so job-status transitions and log
// chunk flushes get broadcast to WebSocket subscribers across replicas.
// Safe to call with nil (disables live broadcasts).
func (w *CornDogsWorker) SetPublisher(p *pubsub.Publisher) {
	w.publisher = p
	if jp, ok := w.processor.(*JobProcessor); ok {
		jp.SetPublisher(p)
	}
}

// NewCornDogsWorker creates a new worker that uses Corndogs for task management.
// statusUpdater is optional; if nil, VCS status updates are silently skipped.
func NewCornDogsWorker(config *Config, corndogsClient corndogs.ClientInterface, statusUpdater vcs.JobStatusUpdaterInterface) *CornDogsWorker {
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

	// Create job processor with configuration. Publisher is wired in after
	// construction via SetPublisher, so callers that don't want WS live
	// updates can still use the worker unchanged.
	processor := NewJobProcessorWithConfig(config.Store, runner, config.DryRun, &JobProcessorConfig{
		ObjectStore:        config.ObjectStore,
		LogChunkInterval:   config.LogChunkInterval,
		HeartbeatInterval:  config.HeartbeatInterval,
		HeartbeatTimeout:   config.HeartbeatTimeout,
		SecretsKeyManager:  keyManager,
		SecretsStorageType: secretsStorageType,
	})

	// Create trigger processor for handling eval job output
	triggerProc := NewTriggerProcessor(config.Store, corndogsClient)
	if statusUpdater != nil {
		triggerProc.SetStatusUpdater(statusUpdater)
	}

	return &CornDogsWorker{
		config:           config,
		corndogsClient:   corndogsClient,
		processor:        processor,
		triggerProcessor: triggerProc,
		statusUpdater:    statusUpdater,
		workerPool:       make(chan struct{}, config.Concurrency),
	}
}

// NewCornDogsWorkerWithProcessor creates a new worker with a custom processor (for testing).
// statusUpdater is optional; if nil, VCS status updates are silently skipped.
func NewCornDogsWorkerWithProcessor(config *Config, corndogsClient corndogs.ClientInterface, processor JobProcessorInterface, triggerProcessor *TriggerProcessor, statusUpdater vcs.JobStatusUpdaterInterface) *CornDogsWorker {
	return &CornDogsWorker{
		config:           config,
		corndogsClient:   corndogsClient,
		processor:        processor,
		triggerProcessor: triggerProcessor,
		statusUpdater:    statusUpdater,
		workerPool:       make(chan struct{}, config.Concurrency),
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

	// Get the job from the database, retrying transient ErrNotFound to handle
	// the dual-write race where the corndogs task is visible to this worker
	// before the API's INSERT has propagated to a fresh DB connection.
	job, err := w.getJobWithRetry(ctx, payload.JobID, getJobLookupAttempts, getJobLookupBackoff)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			// Either the API really lost the row, or the race window is wider
			// than our retry budget. Requeue rather than terminal-fail so the
			// task gets another shot from another poll cycle. If the job truly
			// doesn't exist this will loop until corndogs's own retry budget
			// or timeout terminates it — preferable to silently dropping work.
			logger.WithError(err).Warn("Job not visible after retries; requeueing corndogs task")
			w.requeueTask(ctx, task.Uuid, task.CurrentState)
			return
		}
		logger.WithError(err).Error("Failed to get job from database")
		w.updateTaskFailed(ctx, task.Uuid, task.CurrentState, "Failed to load job from database")
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
	w.publisher.PublishJobUpdate(ctx, job.JobID, job.Status, now.Format(time.RFC3339Nano))
	if w.triggerProcessor != nil {
		if workflowErr := w.triggerProcessor.ProcessWorkflowJobStarted(ctx, job); workflowErr != nil {
			logger.WithError(workflowErr).Error("Failed to process workflow job start")
		}
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

	// Ensure workspace cleanup happens after trigger processing
	if result.WorkspaceDir != "" {
		defer os.RemoveAll(result.WorkspaceDir)
	}

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
	w.publisher.PublishJobUpdate(ctx, job.JobID, job.Status, completedAt.Format(time.RFC3339Nano))

	if w.triggerProcessor != nil && result.WorkspaceDir != "" {
		if workflowErr := w.triggerProcessor.ProcessWorkflowCompletion(ctx, result.WorkspaceDir, job); workflowErr != nil {
			logger.WithError(workflowErr).Error("Failed to process workflow completion")
		}
		if job.Status == "completed" {
			if triggerErr := w.triggerProcessor.ProcessTriggers(ctx, result.WorkspaceDir, job); triggerErr != nil {
				logger.WithError(triggerErr).Error("Failed to process triggers")
			}
		}
	}

	// Update VCS commit status with bounded retry. Transient GitHub failures
	// (network blips, rate limits, 5xx) shouldn't drop the terminal status —
	// without retry the PR check sits on "running" until something else
	// triggers a status push. After exhausting retries we still log-and-
	// continue: an unhealthy PAT or repo permission issue is a config bug
	// the operator needs to fix, not something to crash the worker over.
	if w.statusUpdater != nil && (job.WorkflowID == nil || *job.WorkflowID == "") {
		w.updateVCSStatusWithRetry(ctx, job)
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

// getJobWithRetry fetches a job by ID, retrying on store.ErrNotFound with
// exponential backoff. Non-NotFound errors return immediately. Returns the
// last error (always ErrNotFound when retries are exhausted on missing rows).
func (w *CornDogsWorker) getJobWithRetry(ctx context.Context, jobID string, attempts int, backoff time.Duration) (*models.Job, error) {
	var lastErr error
	for i := 0; i < attempts; i++ {
		job, err := w.config.Store.GetJobByID(ctx, jobID)
		if err == nil {
			return job, nil
		}
		if !errors.Is(err, store.ErrNotFound) {
			return nil, err
		}
		lastErr = err
		if i == attempts-1 {
			break
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(backoff):
		}
		backoff *= 2
	}
	return nil, lastErr
}

// requeueTask transitions a corndogs task back to "submitted" so another
// worker can pick it up. Used when a job lookup races the API's INSERT and
// the row truly hasn't appeared yet — terminal-failing in that case would
// drop work that the API thinks is queued.
func (w *CornDogsWorker) requeueTask(ctx context.Context, taskID, currentState string) {
	_, err := w.corndogsClient.UpdateTask(ctx, taskID, currentState, "submitted", nil)
	if err != nil {
		logging.Log.WithError(err).WithField("task_id", taskID).Error("Failed to requeue task")
	}
}

// updateVCSStatusWithRetry pushes the final VCS commit status with bounded
// exponential backoff. All errors are retried — distinguishing transient
// (5xx, network) from permanent (401, 403) at this layer would require
// per-provider error introspection that the status-updater interface
// doesn't expose. Bounded attempts cap the cost of repeatedly retrying a
// genuinely-broken PAT to a few seconds.
func (w *CornDogsWorker) updateVCSStatusWithRetry(ctx context.Context, job *models.Job) {
	backoff := vcsStatusUpdateBackoff
	var lastErr error
	for i := 0; i < vcsStatusUpdateAttempts; i++ {
		err := w.statusUpdater.UpdateJobStatus(ctx, job)
		if err == nil {
			return
		}
		lastErr = err
		if i == vcsStatusUpdateAttempts-1 {
			break
		}
		logging.Log.WithError(err).WithFields(map[string]interface{}{
			"job_id":  job.JobID,
			"attempt": i + 1,
		}).Warn("VCS status update failed; retrying")
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff *= 2
	}
	logging.Log.WithError(lastErr).WithField("job_id", job.JobID).Error("Failed to update VCS commit status after retries")
}
