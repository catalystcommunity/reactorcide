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
	pb "github.com/catalystcommunity/reactorcide/coordinator_api/internal/corndogs/v1alpha1"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/metrics"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/pubsub"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/secrets"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/vcs"
	"github.com/sirupsen/logrus"
)

// guardedJobStore is the narrow store capability the claim-path cancel
// check, terminal-write guard, and cancelling-job reaper all need: a
// race-safe status transition scoped to an expected set of prior statuses
// (see internal/store/postgres_store.PostgresDbStore.UpdateJobStatusGuarded).
// Reached via type assertion since it's not part of store.Store — the same
// narrow-interface pattern internal/jobcontrol uses for its own
// identically-shaped guardedJobStore; the two packages don't share the type
// to avoid a worker<->jobcontrol import cycle.
type guardedJobStore interface {
	UpdateJobStatusGuarded(ctx context.Context, jobID string, fromStatuses []string, apply func(*models.Job)) (*models.Job, bool, error)
}

// staleCancellingLister is the narrow store capability the cancelling-job
// reaper needs (Finding 2b: a job stuck "cancelling" forever because the
// worker driving its cancel crashed/restarted before finalizing it).
type staleCancellingLister interface {
	ListStaleCancellingJobs(ctx context.Context, olderThan time.Time) ([]models.Job, error)
}

// cancellingReapInterval is how often CornDogsWorker's reaper scans for
// orphaned "cancelling" jobs: once immediately on Start, then on this
// ticker.
const cancellingReapInterval = 60 * time.Second

// cancellingReapSafetyMargin pads cancellingReapStaleAfter beyond the
// longest legitimate in-flight cancel window, so the reaper only ever
// catches genuinely orphaned jobs (no live worker driving their cancel),
// never one that's still legitimately within its grace period.
const cancellingReapSafetyMargin = 2 * time.Minute

// cancellingReapStaleAfter returns how old a "cancelling" job's updated_at
// must be before the reaper treats it as orphaned: the graceful-cancel
// grace period (worst case before a live worker's pollForCancel would have
// force-killed and finalized it) plus the cancel-poll cadence
// (HeartbeatInterval — how long a live worker can go between observing
// "cancelling" and acting on it) plus a fixed safety margin. Derived from
// the worker's own config rather than hardcoded, since both inputs are
// themselves configurable.
func cancellingReapStaleAfter(cfg *Config) time.Duration {
	grace := cfg.CancelGrace
	if grace <= 0 {
		grace = DefaultCancelGrace
	}
	heartbeat := cfg.HeartbeatInterval
	if heartbeat <= 0 {
		heartbeat = 30 * time.Second
	}
	return grace + heartbeat + cancellingReapSafetyMargin
}

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

	// Set default cancel grace period if not specified
	if config.CancelGrace == 0 {
		config.CancelGrace = 60 * time.Second
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
		CancelGrace:        config.CancelGrace,
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

	// Start the cancelling-job reaper (Finding 2b): reaps jobs orphaned in
	// "cancelling" by a worker that crashed/restarted before finalizing
	// them. Runs once immediately, then on cancellingReapInterval, until ctx
	// is cancelled — unlike active job execution, there's no reason to let
	// this survive shutdown.
	w.wg.Add(1)
	go w.runCancellingReaper(ctx)

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
			select {
			case <-ctx.Done():
				logger.Info("Worker stopping due to context cancellation")
				return
			case <-time.After(w.config.PollInterval):
			}
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

	// Once a task is claimed, worker shutdown should stop intake but let the
	// active job finish. Kubernetes and compose deployments provide the outer
	// grace-period bound; do not pass the intake cancellation into the job.
	jobCtx := context.WithoutCancel(ctx)

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
	job, err := w.getJobWithRetry(jobCtx, payload.JobID, getJobLookupAttempts, getJobLookupBackoff)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			// Either the API really lost the row, or the race window is wider
			// than our retry budget. Requeue rather than terminal-fail so the
			// task gets another shot from another poll cycle. If the job truly
			// doesn't exist this will loop until corndogs's own retry budget
			// or timeout terminates it — preferable to silently dropping work.
			logger.WithError(err).Warn("Job not visible after retries; requeueing corndogs task")
			w.requeueTask(jobCtx, task.Uuid, task.CurrentState)
			return
		}
		logger.WithError(err).Error("Failed to get job from database")
		w.updateTaskFailed(jobCtx, task.Uuid, task.CurrentState, "Failed to load job from database")
		return
	}

	// Claim-path race closer (Finding 1c): a cancel/kill request can flip
	// the job to "cancelling" after its Corndogs task was already dequeued
	// by this worker — jobcontrol.transitionJob's own attempt to cancel the
	// task pre-claim loses that race and deliberately leaves the job
	// "cancelling" for whoever claims it. If that's what we just loaded,
	// there's no execution to run: finalize straight to "cancelled" and
	// skip ProcessJobWithContext entirely, rather than starting the
	// container only to have pollForCancel catch it moments later.
	if job.IsCancelling() {
		w.finalizeClaimedCancellingJob(jobCtx, job, task, logger)
		return
	}

	// Update job status to running. Guarded so a cancel that races in
	// between the IsCancelling() check above and this write — a narrow but
	// real window, since both are separate store round trips — can't be
	// silently clobbered back to "running" (see Finding 1c/1d).
	now := time.Now().UTC()
	running, matched := w.finalizeJobGuarded(jobCtx, job, []string{"submitted", "queued"}, func(j *models.Job) {
		j.Status = "running"
		j.StartedAt = &now
	}, logger)
	if !matched {
		// Raced: the job was cancelled between our IsCancelling() check and
		// this write. Don't start executing a job that's already been asked
		// to stop — finalize it the same way the earlier check would have.
		current, getErr := w.config.Store.GetJobByID(jobCtx, job.JobID)
		if getErr == nil && current.IsCancelling() {
			w.finalizeClaimedCancellingJob(jobCtx, current, task, logger)
			return
		}
		logger.Warn("Failed to update job status to running (unexpected concurrent status change)")
		w.updateTaskFailed(jobCtx, task.Uuid, task.CurrentState, "Failed to update job status")
		return
	}
	job = running
	w.publisher.PublishJobUpdate(jobCtx, job.JobID, job.Status, now.Format(time.RFC3339Nano))
	if w.triggerProcessor != nil {
		if workflowErr := w.triggerProcessor.ProcessWorkflowJobStarted(jobCtx, job); workflowErr != nil {
			logger.WithError(workflowErr).Error("Failed to process workflow job start")
		}
	}

	// Update Corndogs task state to indicate we're processing
	_, err = w.corndogsClient.UpdateTask(jobCtx, task.Uuid, task.CurrentState, "processing", nil)
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
	result := w.processor.ProcessJobWithContext(jobCtx, job, execCtx)
	duration := time.Since(startTime).Seconds()

	// Ensure workspace cleanup happens after trigger processing
	if result.WorkspaceDir != "" {
		defer os.RemoveAll(result.WorkspaceDir)
	}

	// Record job processing metrics. A runner-initiated stop (cancel/kill)
	// is neither a normal completion nor a failure of the job's own logic,
	// so it gets its own metrics/status bucket rather than being derived
	// from ExitCode (which reflects a SIGTERM/SIGKILL termination in that
	// case, not the job command's outcome).
	status := "completed"
	switch {
	case result.Cancelled:
		status = "cancelled"
	case result.ExitCode != 0:
		status = "failed"
	}
	metrics.RecordJobProcessed(w.config.QueueName, status, workerIDStr, duration)

	// Update job status based on result
	completedAt := time.Now().UTC()
	job.CompletedAt = &completedAt
	job.ExitCode = &result.ExitCode

	switch {
	case result.Cancelled:
		// The cancel-poll (job_processor.go pollForCancel) observed
		// job.Status == "cancelling" mid-execution and stopped/killed the
		// container itself. Land on the terminal "cancelled" status
		// regardless of the container's raw exit code — see JobResult.Cancelled.
		job.Status = "cancelled"
		if result.Killed {
			job.LastError = "killed by admin"
		} else {
			job.LastError = "cancelled"
		}

		// The Corndogs task was mid-"processing" — cancel it (rather than
		// complete/fail) so Corndogs' own bookkeeping matches what actually
		// happened. Best-effort: the job's terminal DB status is already
		// authoritative regardless of this call's outcome.
		if _, err := w.corndogsClient.CancelTask(jobCtx, task.Uuid, "processing"); err != nil {
			logger.WithError(err).Warn("Failed to cancel task in Corndogs after job cancellation")
		}
	case result.ExitCode == 0:
		job.Status = "completed"

		// Complete the task in Corndogs
		_, err = w.corndogsClient.CompleteTask(jobCtx, task.Uuid, "processing")
		if err != nil {
			logger.WithError(err).Error("Failed to complete task in Corndogs")
		}
	default:
		job.Status = "failed"
		// Update task state to failed
		w.updateTaskFailed(jobCtx, task.Uuid, "processing", "Job execution failed")
	}

	// Set object store keys if available
	if result.LogsObjectKey != "" {
		job.LogsObjectKey = result.LogsObjectKey
	}
	if result.ArtifactsObjectKey != "" {
		job.ArtifactsObjectKey = result.ArtifactsObjectKey
	}

	// Update job in database. Guarded (Finding 1d) so this terminal write
	// can't blindly clobber a status a concurrent cancel/kill or the
	// cancelling-job reaper already landed — e.g. the classic TOCTOU: a
	// CancelJob request reads the job mid-execution, races job_processor.go's
	// own pollForCancel, and both try to write a terminal status.
	finalized, matched := w.finalizeJobGuarded(jobCtx, job, []string{"running", "cancelling"}, func(j *models.Job) {
		j.Status = job.Status
		j.LastError = job.LastError
		j.CompletedAt = job.CompletedAt
		j.ExitCode = job.ExitCode
		if job.LogsObjectKey != "" {
			j.LogsObjectKey = job.LogsObjectKey
		}
		if job.ArtifactsObjectKey != "" {
			j.ArtifactsObjectKey = job.ArtifactsObjectKey
		}
	}, logger)
	if !matched {
		// The row was no longer "running"/"cancelling" by the time we tried
		// to land the terminal status — most likely the cancelling-job
		// reaper (or another worker, after a crash/restart) already
		// finalized it. Don't clobber whatever terminal status is already
		// there; reload it so the trigger/VCS-status logic below reflects
		// reality instead of the status this execution computed but
		// couldn't persist.
		logger.Warn("Job already finalized by another writer; not overwriting terminal status")
		if current, getErr := w.config.Store.GetJobByID(jobCtx, job.JobID); getErr == nil {
			job = current
		}
	} else if finalized != nil {
		job = finalized
	}
	w.publisher.PublishJobUpdate(jobCtx, job.JobID, job.Status, completedAt.Format(time.RFC3339Nano))

	if w.triggerProcessor != nil && result.WorkspaceDir != "" {
		workflowOK := true
		if workflowErr := w.triggerProcessor.ProcessWorkflowCompletion(jobCtx, result.WorkspaceDir, job); workflowErr != nil {
			logger.WithError(workflowErr).Error("Failed to process workflow completion")
			workflowOK = false
		}
		if workflowOK && job.Status == "completed" {
			if triggerErr := w.triggerProcessor.ProcessTriggers(jobCtx, result.WorkspaceDir, job); triggerErr != nil {
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
		w.updateVCSStatusWithRetry(jobCtx, job)
	}

	logger.WithField("status", job.Status).WithField("exit_code", result.ExitCode).Info("Task processing completed")
}

// finalizeJobGuarded performs a race-safe status write for job (JobID is
// authoritative; apply mutates whichever copy of the row actually gets
// persisted). It prefers a guarded (race-safe) store transition when the
// configured store supports it (see guardedJobStore), and falls back to
// mutating job in place and issuing a blind store.UpdateJob — this worker's
// pre-existing behavior — when it doesn't (e.g. the package's own MockStore
// in tests, or any store.Store implementation that hasn't been updated).
//
// Returns the resulting job and whether the write actually landed. false
// means the row's status was no longer in fromStatuses by the time the
// guarded update ran (someone else already moved it on) — callers must not
// treat their locally computed job state as authoritative in that case.
// The fallback (non-guarded) path always reports matched=true, matching
// its pre-existing best-effort semantics; a store error either way returns
// (nil, false).
func (w *CornDogsWorker) finalizeJobGuarded(ctx context.Context, job *models.Job, fromStatuses []string, apply func(*models.Job), logger *logrus.Entry) (*models.Job, bool) {
	if gs, ok := w.config.Store.(guardedJobStore); ok {
		updated, matched, err := gs.UpdateJobStatusGuarded(ctx, job.JobID, fromStatuses, apply)
		if err != nil {
			logger.WithError(err).Error("Guarded job status update failed")
			return nil, false
		}
		return updated, matched
	}

	apply(job)
	if err := w.config.Store.UpdateJob(ctx, job); err != nil {
		logger.WithError(err).Error("Failed to update job result")
		return nil, false
	}
	return job, true
}

// finalizeClaimedCancellingJob closes the claim-time cancel race (Finding
// 1c/1d): a job can be "cancelling" by the time this worker has claimed its
// Corndogs task, either because internal/jobcontrol.transitionJob lost its
// own pre-claim CancelTask race, or because a cancel/kill request landed in
// the narrow window between processNextTask's IsCancelling() check and its
// running-transition write. Either way there's no execution to run:
// finalize straight to "cancelled" (guarded, from "cancelling") and cancel
// the Corndogs task, without ever calling ProcessJobWithContext.
func (w *CornDogsWorker) finalizeClaimedCancellingJob(ctx context.Context, job *models.Job, task *pb.Task, logger *logrus.Entry) {
	lastError := "cancelled"
	if job.IsKillRequested() {
		lastError = "killed by admin"
	}
	now := time.Now().UTC()
	finalized, matched := w.finalizeJobGuarded(ctx, job, []string{"cancelling"}, func(j *models.Job) {
		j.Status = "cancelled"
		j.LastError = lastError
		j.CompletedAt = &now
	}, logger)
	if !matched {
		logger.Warn("Job no longer cancelling by the time the claiming worker tried to finalize it; leaving as-is")
		return
	}

	if _, err := w.corndogsClient.CancelTask(ctx, task.Uuid, task.CurrentState); err != nil {
		logger.WithError(err).Warn("Failed to cancel corndogs task for a job cancelled before worker claim")
	}

	status := "cancelled"
	if finalized != nil {
		status = finalized.Status
	}
	w.publisher.PublishJobUpdate(ctx, job.JobID, status, now.Format(time.RFC3339Nano))
	logger.Info("Job was already cancelling when claimed; finalized without executing")
}

// runCancellingReaper drives reapStaleCancellingJobs on
// cancellingReapInterval until ctx is cancelled, running once immediately
// on entry (Finding 2b).
func (w *CornDogsWorker) runCancellingReaper(ctx context.Context) {
	defer w.wg.Done()

	w.reapStaleCancellingJobs(ctx)

	ticker := time.NewTicker(cancellingReapInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.reapStaleCancellingJobs(ctx)
		}
	}
}

// reapStaleCancellingJobs finalizes jobs orphaned in "cancelling": no
// worker is actively driving their cancel to completion (e.g. the worker
// that was executing them crashed or restarted before finalizing), so
// without this they would stay "cancelling" forever. A no-op if the
// configured store doesn't support staleCancellingLister.
func (w *CornDogsWorker) reapStaleCancellingJobs(ctx context.Context) {
	lister, ok := w.config.Store.(staleCancellingLister)
	if !ok {
		return
	}

	threshold := time.Now().Add(-cancellingReapStaleAfter(w.config))
	stale, err := lister.ListStaleCancellingJobs(ctx, threshold)
	if err != nil {
		logging.Log.WithError(err).Warn("Failed to list stale cancelling jobs for reaper")
		return
	}

	for i := range stale {
		job := &stale[i]
		logger := logging.Log.WithField("job_id", job.JobID)
		now := time.Now().UTC()
		finalized, matched := w.finalizeJobGuarded(ctx, job, []string{"cancelling"}, func(j *models.Job) {
			j.Status = "cancelled"
			j.LastError = "cancelled: no active worker (reaped)"
			j.CompletedAt = &now
		}, logger)
		if !matched {
			// Finalized by someone else (e.g. the executing worker, after
			// all) between the list query and this write — nothing to do.
			continue
		}

		if w.corndogsClient != nil && job.CorndogsTaskID != nil && *job.CorndogsTaskID != "" {
			if _, err := w.corndogsClient.CancelTask(ctx, *job.CorndogsTaskID, "processing"); err != nil {
				logger.WithError(err).Debug("Best-effort corndogs cancel failed while reaping stale cancelling job")
			}
		}

		status := "cancelled"
		if finalized != nil {
			status = finalized.Status
		}
		w.publisher.PublishJobUpdate(ctx, job.JobID, status, now.Format(time.RFC3339Nano))
		logger.Warn("Reaped orphaned cancelling job with no active worker")
	}
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
