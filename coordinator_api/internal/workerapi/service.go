package workerapi

import (
	"context"
	"errors"
	"reflect"
	"time"

	"github.com/catalystcommunity/app-utils-go/logging"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/audit"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/characteristics"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/corndogs"
	pb "github.com/catalystcommunity/reactorcide/coordinator_api/internal/corndogs/v1alpha1"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/pubsub"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/uiapi"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/worker"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/workerapi/csilapi"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/workerauth"
)

// requestJobPollTimeoutSeconds bounds how long a single RequestJob call's
// corndogs.GetNextTaskGroup long-poll waits for a task before returning
// has_lease=false, so a worker's poll loop gets a bounded round trip
// (roughly WorkerSessionTTL/HeartbeatInterval-scale, not indefinite).
const requestJobPollTimeoutSeconds = 30

// heartbeatTaskExtensionSeconds is how far Heartbeat extends a running
// lease's corndogs task timeout on each call, mirroring
// internal/worker/corndogs_worker.go's own JobProcessorConfig.HeartbeatTimeout
// default (10 minutes) -- generous relative to HeartbeatInterval (30s) so a
// worker that misses a handful of heartbeats doesn't lose its claim.
const heartbeatTaskExtensionSeconds = int64(10 * time.Minute / time.Second)

// getJobLookupAttempts/getJobLookupBackoff bound the retry loop papering
// over the same dual-write race internal/worker/corndogs_worker.go's
// getJobWithRetry closes: the corndogs task can become visible to
// GetNextTaskGroup before the job INSERT has propagated to a fresh DB
// connection.
var (
	getJobLookupAttempts = 5
	getJobLookupBackoff  = 200 * time.Millisecond
)

// WorkerService implements csilapi.ReactorcideWorker against a Deps bag.
// Constructed once at startup (see handlers/router.go's buildWorkerAPIDeps)
// and mounted on the shared /csil/v1/rpc dispatcher via
// uiapi.NewHandlerWithWorker.
type WorkerService struct {
	deps    *Deps
	secrets *leaseSecretCache
}

type poolQueueStore interface {
	ListQueuesForPool(ctx context.Context, poolID string) ([]models.Queue, error)
	UpdateWorkerCharacteristics(ctx context.Context, workerID, osName, arch string, chars characteristics.Characteristics) error
}

type jobTokenStore interface {
	MintJobToken(ctx context.Context, job *models.Job) (string, error)
	RevokeJobTokens(ctx context.Context, jobID string) error
}

var _ csilapi.ReactorcideWorker = (*WorkerService)(nil)

// NewWorkerService constructs a WorkerService backed by deps.
func NewWorkerService(deps *Deps) *WorkerService {
	return &WorkerService{deps: deps, secrets: newLeaseSecretCache()}
}

// --- Register ---------------------------------------------------------

// Register validates the presented enrollment token, upserts the workers
// row by worker_key, and mints a fresh process-lifetime worker session.
// Never reactivates a disabled worker's status, and never resets an
// existing worker's status at all (UpsertWorkerByKey only overwrites status
// when explicitly given a non-empty one) -- Register must not undo an
// admin's quarantine/disable decision.
func (s *WorkerService) Register(ctx context.Context, req csilapi.RegisterRequest) (csilapi.RegisterResponse, error) {
	pool, err := s.deps.Enrollment.ValidateEnrollmentToken(ctx, req.EnrollmentToken)
	if err != nil {
		return csilapi.RegisterResponse{}, uiapi.NewServiceError("unauthorized", "enrollment token rejected")
	}

	info := req.WorkerInfo
	if info.WorkerKey == "" {
		return csilapi.RegisterResponse{}, uiapi.NewServiceError("invalid_argument", "worker_info.worker_key is required")
	}

	chars, err := characteristics.ParseWorkerCharacteristics(info.Os, info.Arch, toCharacteristicKVs(info.Custom))
	if err != nil {
		return csilapi.RegisterResponse{}, uiapi.NewServiceError("invalid_argument", err.Error())
	}

	toUpsert := &models.Worker{
		PoolID:          pool.PoolID,
		WorkerKey:       info.WorkerKey,
		Hostname:        derefOrEmpty(info.Hostname),
		OS:              info.Os,
		Arch:            info.Arch,
		Characteristics: chars,
		WorkerVersion:   derefOrEmpty(info.WorkerVersion),
	}
	saved, err := s.deps.Store.UpsertWorkerByKey(ctx, toUpsert)
	if err != nil {
		logging.Log.WithError(err).WithField("worker_key", info.WorkerKey).Error("Failed to upsert worker on register")
		return csilapi.RegisterResponse{}, uiapi.NewServiceError("internal", "failed to register worker")
	}
	if saved.Status == models.WorkerStatusDisabled {
		return csilapi.RegisterResponse{}, uiapi.NewServiceError("forbidden", "worker is disabled")
	}
	orgID := ""
	if pool.OrgID != nil {
		orgID = *pool.OrgID
	}
	audit.Record(ctx, s.deps.Store, orgID, "worker.enroll", "worker", saved.WorkerID, models.JSONB{
		"pool_id": pool.PoolID,
	})

	token, err := s.deps.Sessions.Mint(ctx, saved.WorkerID)
	if err != nil {
		logging.Log.WithError(err).WithField("worker_id", saved.WorkerID).Error("Failed to mint worker session")
		return csilapi.RegisterResponse{}, uiapi.NewServiceError("internal", "failed to mint worker session")
	}

	return csilapi.RegisterResponse{
		WorkerSession:     token,
		WorkerId:          saved.WorkerID,
		HeartbeatInterval: int64(workerauth.HeartbeatInterval / time.Second),
	}, nil
}

// --- RequestJob ---------------------------------------------------------

// RequestJob resolves the caller's worker session, computes which queues
// the worker's characteristics satisfy, claims at most one task across that
// queue group via corndogs.GetNextTaskGroup, transitions the underlying job
// to running, resolves its secrets coordinator-side (see secrets.go), and
// returns a Lease. See rejectClaim/abandonClaim for the denial and
// store-failure paths.
func (s *WorkerService) RequestJob(ctx context.Context, req csilapi.RequestJobRequest) (csilapi.RequestJobResponse, error) {
	wkr, _, err := s.resolveSession(ctx)
	if err != nil {
		return csilapi.RequestJobResponse{}, err
	}
	// A quarantined/disabled worker is offered no further work: this is what
	// makes quarantine/drain actually stop new job offers, rather than only
	// affecting admin-surface display. Returned as an ordinary
	// has_lease=false rather than a ServiceError -- a non-active worker is
	// not a caller error, it's just "no work right now", exactly like every
	// other no-match path in this method (no satisfying queue, empty task
	// group, claim-race loss), so the worker's poll loop doesn't need
	// special-case error handling for it.
	if wkr.Status != models.WorkerStatusActive {
		return csilapi.RequestJobResponse{HasLease: false}, nil
	}

	wc := req.WorkerCharacteristics
	workerChars, err := characteristics.ParseWorkerCharacteristics(wc.Os, wc.Arch, toCharacteristicKVs(wc.Custom))
	if err != nil {
		return csilapi.RequestJobResponse{}, uiapi.NewServiceError("invalid_argument", err.Error())
	}
	if !reflect.DeepEqual(workerChars, wkr.Characteristics) || wc.Os != wkr.OS || wc.Arch != wkr.Arch {
		if scopedStore, ok := s.deps.Store.(poolQueueStore); ok {
			if err := scopedStore.UpdateWorkerCharacteristics(ctx, wkr.WorkerID, wc.Os, wc.Arch, workerChars); err != nil {
				return csilapi.RequestJobResponse{}, uiapi.NewServiceError("internal", "failed to record worker characteristic drift")
			}
			logging.Log.WithFields(map[string]any{"worker_id": wkr.WorkerID, "pool_id": wkr.PoolID}).Info("Worker characteristics changed; enrollment record updated")
			orgID := ""
			if pool, poolErr := s.deps.Store.GetWorkerPoolByID(ctx, wkr.PoolID); poolErr == nil && pool.OrgID != nil {
				orgID = *pool.OrgID
			}
			audit.Record(ctx, s.deps.Store, orgID, "worker.characteristics_update", "worker", wkr.WorkerID, models.JSONB{
				"pool_id": wkr.PoolID,
				"os":      wc.Os,
				"arch":    wc.Arch,
			})
		}
	}

	var queues []models.Queue
	if scopedStore, ok := s.deps.Store.(poolQueueStore); ok {
		queues, err = scopedStore.ListQueuesForPool(ctx, wkr.PoolID)
	} else {
		queues, err = s.deps.Store.ListQueues(ctx, 0, 0)
	}
	if err != nil {
		return csilapi.RequestJobResponse{}, uiapi.NewServiceError("internal", "failed to list queues")
	}
	queueUUIDs := satisfyingQueueUUIDs(workerChars, queues)
	if len(queueUUIDs) == 0 {
		return csilapi.RequestJobResponse{HasLease: false}, nil
	}

	if s.deps.CorndogsClient == nil {
		return csilapi.RequestJobResponse{}, uiapi.NewServiceError("internal", "corndogs is not configured")
	}
	task, err := s.deps.CorndogsClient.GetNextTaskGroup(ctx, queueUUIDs, "submitted", requestJobPollTimeoutSeconds)
	if err != nil || task == nil {
		return csilapi.RequestJobResponse{HasLease: false}, nil
	}

	payload, err := corndogs.ParseTaskPayload(task)
	if err != nil {
		logging.Log.WithError(err).WithField("task_id", task.Uuid).Error("Failed to parse claimed task payload")
		s.updateTaskFailed(ctx, task, "failed to parse task payload")
		return csilapi.RequestJobResponse{HasLease: false}, nil
	}

	job, err := s.getJobWithRetry(ctx, payload.JobID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			// Same dual-write race corndogs_worker.go's getJobWithRetry
			// guards against; requeue rather than terminal-fail so another
			// RequestJob call (this worker or another) gets it next.
			logging.Log.WithError(err).WithField("job_id", payload.JobID).Warn("Job not visible after retries; requeueing corndogs task")
			s.requeueTask(ctx, task)
		} else {
			logging.Log.WithError(err).WithField("job_id", payload.JobID).Error("Failed to load claimed job")
			s.updateTaskFailed(ctx, task, "failed to load job from database")
		}
		return csilapi.RequestJobResponse{HasLease: false}, nil
	}
	var claimedQueue *models.Queue
	for i := range queues {
		if queues[i].QueueUUID == task.Queue {
			claimedQueue = &queues[i]
			break
		}
	}
	// Persisted jobs have all three authority fields. Some in-memory legacy
	// test stores omit them; those stores cannot model cross-tenant claims.
	completeAuthority := job.QueueName != "" && job.OwnershipOrgID() != "" && job.WorkerClass != ""
	if completeAuthority && (claimedQueue == nil || job.QueueName != task.Queue || job.OwnershipOrgID() != claimedQueue.OrgID || job.WorkerClass != claimedQueue.WorkerClass) {
		logging.Log.WithFields(map[string]any{"job_id": job.JobID, "task_id": task.Uuid}).Error("Rejected task whose job authority does not match its queue")
		s.updateTaskFailed(ctx, task, "job organization or worker class does not match queue")
		return csilapi.RequestJobResponse{HasLease: false}, nil
	}

	// A task that is not the job's current task is a leftover: the lost-job
	// reconciler (reaper.go) submitted a replacement task and the old one came
	// back from a corndogs timeout. Only the current task may drive the job.
	if job.CorndogsTaskID != nil && *job.CorndogsTaskID != "" && *job.CorndogsTaskID != task.Uuid {
		logging.Log.WithFields(map[string]any{"job_id": job.JobID, "task_id": task.Uuid, "current_task_id": *job.CorndogsTaskID}).Warn("Discarding superseded corndogs task")
		if _, err := s.deps.CorndogsClient.CancelTask(ctx, task.Uuid, task.CurrentState); err != nil {
			logging.Log.WithError(err).WithField("task_id", task.Uuid).Warn("Failed to cancel superseded corndogs task")
		}
		return csilapi.RequestJobResponse{HasLease: false}, nil
	}

	if job.IsCancelling() {
		s.finalizeClaimedCancellingJob(ctx, job, task)
		return csilapi.RequestJobResponse{HasLease: false}, nil
	}

	// Resolve everything that can deny the claim BEFORE any write. A denial
	// or a store failure here leaves the job at submitted, so nothing needs
	// to be undone (see rejectClaim).
	env := worker.BuildJobEnv(job)
	if job.WorkflowID != nil && *job.WorkflowID != "" {
		if workflowVars, ok := s.deps.Store.(interface {
			GetWorkflowVars(context.Context, string) (map[string]models.JSONB, error)
		}); ok {
			if values, varsErr := workflowVars.GetWorkflowVars(ctx, *job.WorkflowID); varsErr == nil {
				if encoded, marshalErr := worker.EncodeWorkflowVars(values); marshalErr == nil {
					env["RC_WF_VARS_JSON"] = string(encoded)
				}
			}
		}
	}
	// A remote lease must never inherit the coordinator's static API token.
	// The claim-specific job token below is the only API credential for it.
	delete(env, "REACTORCIDE_API_TOKEN")
	resolved, err := s.deps.resolveJobSecrets(ctx, job, env)
	if err != nil {
		s.rejectClaim(ctx, job, task, "secret resolution", err)
		return csilapi.RequestJobResponse{HasLease: false}, nil
	}

	// Resolve a VCS checkout credential for the job's source repo, if any is
	// configured -- coordinator-side, over this same authenticated claim
	// (see vcs_auth.go). A resolution failure rejects the claim exactly like
	// a denied job secret; "no credential configured" (public repo) is not
	// an error and simply leaves vcsAuth nil.
	vcsAuth, err := s.deps.resolveVCSAuth(ctx, job)
	if err != nil {
		s.rejectClaim(ctx, job, task, "VCS checkout credential", err)
		return csilapi.RequestJobResponse{HasLease: false}, nil
	}

	// The running transition, the job token, and the lease commit together
	// or not at all. A partial claim (job at running with no lease) is a job
	// no worker executes and no claim can take again.
	now := time.Now().UTC()
	workerID := wkr.WorkerID
	queueUUID := task.Queue
	var (
		running  *models.Job
		matched  bool
		lease    *models.WorkerLease
		jobToken string
		step     string
	)
	err = s.inTransaction(ctx, func(txCtx context.Context) error {
		var txErr error
		step = "job status"
		running, matched, txErr = s.deps.Store.UpdateJobStatusGuarded(txCtx, job.JobID, []string{"submitted", "queued"}, func(j *models.Job) {
			j.Status = "running"
			j.StartedAt = &now
			j.WorkerID = &workerID
		})
		if txErr != nil || !matched {
			return txErr
		}
		if tokenStore, ok := s.deps.Store.(jobTokenStore); ok {
			step = "job token"
			if jobToken, txErr = tokenStore.MintJobToken(txCtx, running); txErr != nil {
				return txErr
			}
		}
		step = "worker lease"
		lease, txErr = s.deps.Store.CreateWorkerLease(txCtx, workerID, job.JobID, &queueUUID)
		return txErr
	})
	if err != nil {
		s.abandonClaim(ctx, job, task, step, err)
		return csilapi.RequestJobResponse{HasLease: false}, nil
	}
	if !matched {
		// Raced with a cancel: reload and, if cancelling, finalize it the
		// same way the earlier IsCancelling() check would have.
		if current, getErr := s.deps.Store.GetJobByID(ctx, job.JobID); getErr == nil && current.IsCancelling() {
			s.finalizeClaimedCancellingJob(ctx, current, task)
		} else {
			s.updateTaskFailed(ctx, task, "job status changed concurrently")
		}
		return csilapi.RequestJobResponse{HasLease: false}, nil
	}
	job = running
	s.publishJobUpdate(ctx, job, now)

	// Roll the job's workflow node to "running" (best-effort): keeps the
	// workflow instance and its VCS check in sync while the job executes. Does
	// nothing for non-workflow jobs.
	if s.deps.WorkflowFinalizer != nil && job.WorkflowID != nil && *job.WorkflowID != "" {
		if err := s.deps.WorkflowFinalizer.ProcessWorkflowJobStarted(ctx, job); err != nil {
			logging.Log.WithError(err).WithFields(map[string]interface{}{
				"job_id":      job.JobID,
				"workflow_id": *job.WorkflowID,
			}).Warn("Failed to mark workflow node running")
		}
	}

	if _, err := s.deps.CorndogsClient.UpdateTask(ctx, task.Uuid, task.CurrentState, "processing", nil); err != nil {
		logging.Log.WithError(err).WithField("task_id", task.Uuid).Warn("Failed to update task state to processing")
	}

	if jobToken != "" {
		if resolved.Secrets == nil {
			resolved.Secrets = map[string]string{}
		}
		resolved.Secrets["REACTORCIDE_API_TOKEN"] = jobToken
		resolved.SecretValues = append(resolved.SecretValues, jobToken)
	}
	secretValues := resolved.SecretValues
	if vcsAuth != nil {
		secretValues = append(append([]string{}, secretValues...), vcsAuth.Token)
	}
	if len(secretValues) > 0 {
		s.secrets.put(lease.LeaseID, secretValues)
	}

	return csilapi.RequestJobResponse{
		HasLease: true,
		Lease:    buildLease(lease.LeaseID, job, resolved, vcsAuth),
	}, nil
}

// inTransaction runs fn in one database transaction when the store supports
// it (PostgresDbStore.InTransaction). Other stores run fn directly;
// abandonClaim then undoes a partial claim by hand.
func (s *WorkerService) inTransaction(ctx context.Context, fn func(context.Context) error) error {
	if txStore, ok := s.deps.Store.(interface {
		InTransaction(context.Context, func(context.Context) error) error
	}); ok {
		return txStore.InTransaction(ctx, fn)
	}
	return fn(ctx)
}

// abandonClaim handles a store failure while the claim commits. The job must
// go back to submitted and the corndogs task must stay claimable: a store
// error is never a reason to fail the job. With a transactional store the
// failed transaction already left the job at submitted and the revert below
// matches nothing. Without one, the revert undoes the partial claim.
func (s *WorkerService) abandonClaim(ctx context.Context, job *models.Job, task *pb.Task, step string, cause error) {
	logging.Log.WithError(cause).WithFields(map[string]interface{}{
		"job_id":            job.JobID,
		"task_id":           task.Uuid,
		"step":              step,
		"store_unavailable": store.IsUnavailable(cause),
	}).Warn("Claim failed to commit; returning job to the queue")

	if _, _, err := s.deps.Store.UpdateJobStatusGuarded(ctx, job.JobID, []string{"running"}, func(j *models.Job) {
		j.Status = "submitted"
		j.StartedAt = nil
		j.WorkerID = nil
	}); err != nil {
		// The job row state is unknown. Leave the corndogs task alone: its
		// claim timeout returns it to the queue, and the lost-job reconciler
		// repairs a job left at running with no lease.
		logging.Log.WithError(err).WithField("job_id", job.JobID).Warn("Could not confirm job is back at submitted; leaving task to its claim timeout")
		return
	}
	if tokenStore, ok := s.deps.Store.(jobTokenStore); ok {
		if err := tokenStore.RevokeJobTokens(ctx, job.JobID); err != nil {
			logging.Log.WithError(err).WithField("job_id", job.JobID).Warn("Failed to revoke job token of abandoned claim")
		}
	}
	s.requeueTask(ctx, task)
}

// finalizeClaimedCancellingJob closes the claim-time cancel race. A job can
// already be "cancelling" by the time its corndogs task is claimed here
// (jobcontrol.transitionJob lost its own pre-claim CancelTask race, or a
// cancel landed in the narrow window around the running-transition write
// above). There's no execution to hand out: finalize straight to
// "cancelled" and cancel the corndogs task, without ever returning a lease.
func (s *WorkerService) finalizeClaimedCancellingJob(ctx context.Context, job *models.Job, task *pb.Task) {
	lastError := "cancelled"
	if job.IsKillRequested() {
		lastError = "killed by admin"
	}
	now := time.Now().UTC()
	finalized, matched, err := s.deps.Store.UpdateJobStatusGuarded(ctx, job.JobID, []string{"cancelling"}, func(j *models.Job) {
		j.Status = "cancelled"
		j.LastError = lastError
		j.CompletedAt = &now
	})
	if err != nil || !matched {
		return
	}
	if _, err := s.deps.CorndogsClient.CancelTask(ctx, task.Uuid, task.CurrentState); err != nil {
		logging.Log.WithError(err).WithField("job_id", job.JobID).Warn("Failed to cancel corndogs task for a job cancelled before claim")
	}
	s.publishJobUpdate(ctx, finalized, now)
	s.advanceWorkflowAfterCoordinatorFinalization(ctx, finalized)
}

// rejectClaim handles a failed secret or VCS credential resolution. The job
// is still at submitted: no write has happened yet.
//
// A store failure is not a denial. The job stays at submitted and the task
// goes back to the queue for the next claim.
//
// A real denial fails the job (submitted->failed) and only then fails the
// corndogs task. If the job row cannot be written, the task goes back to the
// queue: a finished task with an unfinished job is a job nothing can drive
// again. No worker_leases row is created and no lease is returned, so a
// secret the job is not authorized for never reaches a worker.
func (s *WorkerService) rejectClaim(ctx context.Context, job *models.Job, task *pb.Task, step string, cause error) {
	fields := map[string]interface{}{"job_id": job.JobID, "task_id": task.Uuid, "step": step}
	if store.IsUnavailable(cause) {
		logging.Log.WithError(cause).WithFields(fields).Warn("Claim deferred: store unavailable; requeueing task")
		s.requeueTask(ctx, task)
		return
	}
	logging.Log.WithError(cause).WithFields(fields).Warn("Claim denied; failing job without leasing")
	now := time.Now().UTC()
	finalized, matched, err := s.deps.Store.UpdateJobStatusGuarded(ctx, job.JobID, []string{"submitted", "queued"}, func(j *models.Job) {
		j.Status = "failed"
		j.LastError = step + " denied: " + cause.Error()
		j.CompletedAt = &now
	})
	if err != nil {
		logging.Log.WithError(err).WithFields(fields).Warn("Could not record claim denial; requeueing task")
		s.requeueTask(ctx, task)
		return
	}
	if !matched {
		if current, getErr := s.deps.Store.GetJobByID(ctx, job.JobID); getErr == nil && current.IsCancelling() {
			s.finalizeClaimedCancellingJob(ctx, current, task)
		} else {
			s.updateTaskFailed(ctx, task, "job status changed concurrently")
		}
		return
	}
	s.updateTaskFailed(ctx, task, step+" denied")
	s.publishJobUpdate(ctx, finalized, now)
	s.advanceWorkflowAfterCoordinatorFinalization(ctx, finalized)
}

// advanceWorkflowAfterCoordinatorFinalization processes a terminal workflow
// job when the coordinator does not return a lease. No worker can report a
// result for these jobs, so the coordinator must process the completion here.
func (s *WorkerService) advanceWorkflowAfterCoordinatorFinalization(ctx context.Context, job *models.Job) {
	if s.deps.WorkflowFinalizer == nil || job.WorkflowID == nil || *job.WorkflowID == "" {
		return
	}
	if err := s.deps.WorkflowFinalizer.ProcessWorkflowCompletion(ctx, "", job); err != nil {
		logging.Log.WithError(err).WithFields(map[string]interface{}{
			"job_id":      job.JobID,
			"workflow_id": *job.WorkflowID,
		}).Warn("Failed to advance workflow after coordinator finalized job")
	}
}

// --- Heartbeat ---------------------------------------------------------

// Heartbeat resolves the caller's session, extends the corndogs task
// timeout for every lease the worker reports as still running, and computes
// cancel/kill directives for any of those leases whose job has since moved
// to "cancelling".
func (s *WorkerService) Heartbeat(ctx context.Context, req csilapi.HeartbeatRequest) (csilapi.HeartbeatResponse, error) {
	wkr, _, err := s.resolveSession(ctx)
	if err != nil {
		return csilapi.HeartbeatResponse{}, err
	}
	_ = s.deps.Store.TouchWorkerLastSeen(ctx, wkr.WorkerID)

	// draining mirrors RequestJob's status guard: a quarantined/disabled
	// worker is already offered no new work, but a worker sitting in its
	// RequestJob long-poll or mid-lease needs a signal on its existing
	// Heartbeat cadence to learn it should finish up and shut down rather
	// than keep polling forever. Derived from the worker's current status on
	// every call, not a separate persisted state -- drain and quarantine are
	// the same status, distinguished only by admin intent, not by data model.
	draining := wkr.Status != models.WorkerStatusActive

	var directives []csilapi.Directive
	for _, rl := range req.RunningLeases {
		lease, err := s.deps.Store.GetWorkerLeaseByID(ctx, rl.LeaseId)
		if err != nil || lease.WorkerID != wkr.WorkerID {
			continue
		}
		if !lease.IsActive() {
			continue
		}
		_ = s.deps.Store.TouchWorkerLeaseHeartbeat(ctx, lease.LeaseID)
		job, err := s.deps.Store.GetJobByID(ctx, lease.JobID)
		if err != nil {
			continue
		}

		if job.CorndogsTaskID != nil && *job.CorndogsTaskID != "" && s.deps.CorndogsClient != nil {
			if _, err := s.deps.CorndogsClient.SendHeartbeat(ctx, *job.CorndogsTaskID, "processing", heartbeatTaskExtensionSeconds); err != nil {
				logging.Log.WithError(err).WithField("job_id", job.JobID).Debug("Failed to extend corndogs task on heartbeat")
			}
		}

		if job.IsCancelling() {
			action := "cancel"
			if job.IsKillRequested() {
				action = "kill"
			}
			directives = append(directives, csilapi.Directive{LeaseId: rl.LeaseId, Action: action})
		}
	}

	return csilapi.HeartbeatResponse{Directives: directives, Draining: draining}, nil
}

// --- resolveSession -------------------------------------------------------

// resolveSession resolves the CSIL-RPC envelope's auth field into a worker.
// An absent, unknown, expired, or revoked session (store.ErrNotFound) is a
// ServiceError "unauthorized" that says nothing more specific. Any other
// failure is the store failing to answer, not an authorization decision: it
// is a retryable ServiceError "unavailable" (see errUnavailable), so the
// worker keeps its result and retries instead of dropping it.
func (s *WorkerService) resolveSession(ctx context.Context) (*models.Worker, *models.WorkerSession, error) {
	token, ok := uiapi.AuthTokenFromContext(ctx)
	if !ok || token == "" {
		return nil, nil, uiapi.NewServiceError("unauthorized", "a valid worker session is required")
	}
	wkr, session, err := s.deps.Sessions.Resolve(ctx, token)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, nil, uiapi.NewServiceError("unauthorized", "a valid worker session is required")
		}
		logging.Log.WithError(err).Warn("Worker session lookup failed; store unavailable")
		return nil, nil, errUnavailable()
	}
	return wkr, session, nil
}

// errUnavailable is the retryable ServiceError for a store that did not
// answer. Workers retry an "unavailable" call with backoff (see
// internal/coordinatorworker.isRetryable).
func errUnavailable() error {
	return uiapi.NewServiceError("unavailable", "coordinator store unavailable; retry")
}

// leaseLookupError maps a failed lease or job lookup: a missing row is
// "not_found" (the worker stops retrying), anything else is "unavailable".
func leaseLookupError(err error, message string) error {
	if errors.Is(err, store.ErrNotFound) {
		return uiapi.NewServiceError("not_found", message)
	}
	logging.Log.WithError(err).Warn("Lease lookup failed; store unavailable")
	return errUnavailable()
}

// --- shared claim-path helpers --------------------------------------------

func (s *WorkerService) getJobWithRetry(ctx context.Context, jobID string) (*models.Job, error) {
	backoff := getJobLookupBackoff
	var lastErr error
	for i := 0; i < getJobLookupAttempts; i++ {
		job, err := s.deps.Store.GetJobByID(ctx, jobID)
		if err == nil {
			return job, nil
		}
		if !errors.Is(err, store.ErrNotFound) {
			return nil, err
		}
		lastErr = err
		if i == getJobLookupAttempts-1 {
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

func (s *WorkerService) requeueTask(ctx context.Context, task *pb.Task) {
	if _, err := s.deps.CorndogsClient.UpdateTask(ctx, task.Uuid, task.CurrentState, "submitted", nil); err != nil {
		logging.Log.WithError(err).WithField("task_id", task.Uuid).Error("Failed to requeue task")
	}
}

func (s *WorkerService) updateTaskFailed(ctx context.Context, task *pb.Task, reason string) {
	if s.deps.CorndogsClient == nil {
		return
	}
	if _, err := s.deps.CorndogsClient.UpdateTask(ctx, task.Uuid, task.CurrentState, "failed", nil); err != nil {
		logging.Log.WithError(err).WithFields(map[string]interface{}{"task_id": task.Uuid, "reason": reason}).Error("Failed to mark task failed")
	}
}

func (s *WorkerService) publishJobUpdate(ctx context.Context, job *models.Job, at time.Time) {
	if s.deps.Publisher == nil || job == nil {
		return
	}
	s.deps.Publisher.PublishJobUpdate(ctx, jobRef(job, at))
}

// jobRef builds the event reference for a job, carrying the ownership fields a
// UI stream authorizes each frame from. OwnershipOrgID is the owning org
// (user_id IS the org id everywhere here).
func jobRef(job *models.Job, at time.Time) pubsub.JobRef {
	ref := pubsub.JobRef{
		JobID:       job.JobID,
		Status:      job.Status,
		UpdatedAt:   at.UTC().Format(time.RFC3339Nano),
		OwnerUserID: job.OwnershipOrgID(),
	}
	if job.ProjectID != nil {
		ref.ProjectID = *job.ProjectID
	}
	if job.WorkflowID != nil {
		ref.WorkflowID = *job.WorkflowID
	}
	return ref
}

// --- pure helpers -----------------------------------------------------

func satisfyingQueueUUIDs(workerChars characteristics.Characteristics, queues []models.Queue) []string {
	var uuids []string
	for _, q := range queues {
		if characteristics.Satisfies(workerChars, q.Characteristics) {
			uuids = append(uuids, q.QueueUUID)
		}
	}
	return uuids
}

func toCharacteristicKVs(custom []csilapi.CustomCharacteristic) []characteristics.KV {
	out := make([]characteristics.KV, len(custom))
	for i, c := range custom {
		out[i] = characteristics.KV{Key: c.Key, Value: c.Value}
	}
	return out
}

func derefOrEmpty(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// buildLease assembles the Lease returned to a worker on a successful
// RequestJob claim, mirroring internal/worker.(*JobProcessor).buildJobConfig
// exactly for image/command/working_dir/resources/capabilities/run_as_user --
// except env/secrets, which come from the already-split resolvedLeaseSecrets
// rather than a single unresolved env map. vcsAuth is nil for a job whose
// source needs no checkout credential; when non-nil it becomes the Lease's
// own optional vcs_auth field (see vcs_auth.go), never folded into
// Env/Secrets.
func buildLease(leaseID string, job *models.Job, resolved *resolvedLeaseSecrets, vcsAuth *csilapi.VCSAuth) *csilapi.Lease {
	image := worker.DefaultRunnerImage
	if job.ContainerImage != nil && *job.ContainerImage != "" {
		image = *job.ContainerImage
	} else if job.RunnerImage != "" {
		image = job.RunnerImage
	}

	shellPrefix := ""
	if job.JobEnvVars != nil {
		if v, ok := job.JobEnvVars["REACTORCIDE_JOB_SHELLCMD"]; ok {
			if s, ok := v.(string); ok {
				shellPrefix = s
			}
		}
	}

	return &csilapi.Lease{
		LeaseId:    leaseID,
		JobId:      job.JobID,
		Image:      image,
		Command:    worker.ParseCommandWithPrefix(job.JobCommand, shellPrefix),
		WorkingDir: worker.DefaultJobDir(job.CodeDir, job.JobDir),
		Env:        envVarsFromMap(resolved.Env),
		Secrets:    envVarsFromMap(resolved.Secrets),
		Resources: csilapi.Resources{
			CpuRequest:  job.ResourceCPURequest,
			CpuLimit:    job.ResourceCPULimit,
			MemoryLimit: job.ResourceMemoryLimit,
		},
		TimeoutSeconds:     int64(job.TimeoutSeconds),
		CancelGraceSeconds: int64(worker.DefaultCancelGrace / time.Second),
		Capabilities:       append([]string{}, job.Capabilities...),
		ImagePullSecrets:   append([]string{}, job.ImagePullSecrets...),
		RunAsUser:          job.RunAsUser,
		VcsAuth:            vcsAuth,
	}
}

func envVarsFromMap(m map[string]string) []csilapi.EnvVar {
	out := make([]csilapi.EnvVar, 0, len(m))
	for k, v := range m {
		out = append(out, csilapi.EnvVar{Key: k, Value: v})
	}
	return out
}
