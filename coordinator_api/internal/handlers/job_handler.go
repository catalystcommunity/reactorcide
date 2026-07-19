package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/authz"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/checkauth"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/config"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/corndogs"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/jobcontrol"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/metrics"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/objects"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/vcs"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/worker"
)

// LogEntry represents a single log line in JSON format (matches worker.LogEntry)
type LogEntry struct {
	Timestamp string `json:"timestamp"`
	Stream    string `json:"stream"`
	Level     string `json:"level,omitempty"`
	Message   string `json:"message"`
}

// JobHandler handles job-related HTTP requests
type JobHandler struct {
	BaseHandler
	store            store.Store
	corndogsClient   corndogs.ClientInterface
	objectStore      objects.ObjectStore
	triggerProcessor *worker.TriggerProcessor
	// visibility is non-nil only when store also satisfies authz.RoleStore
	// (true for *postgres_store.PostgresDbStore; nil for the narrower test
	// mocks in job_handler_test.go — construction now logs a WARNING in
	// that case, see roleStoreResolver). It's consulted additively by read
	// endpoints (GetJob, ListJobs, GetJobLogs) to extend the pre-existing
	// owner-or-admin access check with "or the resource is publicly
	// visible" — see canUserViewJob. Most mutation endpoints (cancel,
	// delete, triggers) intentionally keep using the original
	// canUserAccessJob (owner-or-admin only): a wider *view* grant must
	// never widen who can *mutate* a job. KillJob is the one exception —
	// see canUserKillJob and cancelOrKillJob — because the permission
	// matrix (UI_AUTH_PLAN.md) restricts kill to an org admin of the job's
	// org or a global admin, which is a NARROWER grant than owner-or-admin,
	// not a wider one. See UI_AUTH_PLAN.md task D.
	visibility *authz.Resolver
}

// NewJobHandler creates a new job handler
func NewJobHandler(store store.Store, corndogsClient corndogs.ClientInterface) *JobHandler {
	return &JobHandler{
		store:            store,
		corndogsClient:   corndogsClient,
		triggerProcessor: worker.NewTriggerProcessor(store, corndogsClient),
		visibility:       roleStoreResolver(store, "JobHandler"),
	}
}

// NewJobHandlerWithObjectStore creates a new job handler with object store support
func NewJobHandlerWithObjectStore(store store.Store, corndogsClient corndogs.ClientInterface, objectStore objects.ObjectStore) *JobHandler {
	return &JobHandler{
		store:            store,
		corndogsClient:   corndogsClient,
		objectStore:      objectStore,
		triggerProcessor: worker.NewTriggerProcessor(store, corndogsClient),
		visibility:       roleStoreResolver(store, "JobHandler"),
	}
}

// SetStatusUpdater wires a VCS status updater so that child jobs created via
// the /api/v1/jobs/{id}/triggers callback register as pending checks on
// their commit immediately.
func (h *JobHandler) SetStatusUpdater(u vcs.JobStatusUpdaterInterface) {
	if h.triggerProcessor != nil {
		h.triggerProcessor.SetStatusUpdater(u)
	}
}

// CreateJobRequest represents the request payload for creating a job
type CreateJobRequest struct {
	Name        string `json:"name" validate:"required,max=255"`
	Description string `json:"description,omitempty"`
	JobFile     string `json:"job_file,omitempty"`

	// Source configuration (VCS-agnostic: works with git, mercurial, svn, etc.)
	// This is the untrusted source code being tested (e.g., PR code)
	SourceURL  string `json:"source_url,omitempty"`
	SourceRef  string `json:"source_ref,omitempty"`
	SourceType string `json:"source_type" validate:"required,oneof=git copy"`
	SourcePath string `json:"source_path,omitempty"`

	// CI Source configuration (trusted CI pipeline code - optional)
	// This is the trusted code that defines the job (e.g., test scripts, build config)
	CISourceType string `json:"ci_source_type,omitempty" validate:"omitempty,oneof=git copy"`
	CISourceURL  string `json:"ci_source_url,omitempty"`
	CISourceRef  string `json:"ci_source_ref,omitempty"`

	// Runnerlib configuration
	CodeDir     string `json:"code_dir,omitempty"`
	JobDir      string `json:"job_dir,omitempty"`
	JobCommand  string `json:"job_command" validate:"required"`
	RunnerImage string `json:"runner_image,omitempty"`

	// Environment configuration
	JobEnvVars map[string]string `json:"job_env_vars,omitempty"`
	JobEnvFile string            `json:"job_env_file,omitempty"`

	// Execution settings
	TimeoutSeconds *int   `json:"timeout_seconds,omitempty"`
	Priority       *int   `json:"priority,omitempty"`
	RunAsUser      string `json:"run_as_user,omitempty"`
	QueueName      string `json:"queue_name,omitempty"`
}

// JobResponse represents the response for job operations
type JobResponse struct {
	JobID       string    `json:"job_id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	JobFile     string    `json:"job_file,omitempty"`
	Status      string    `json:"status"`
	LastError   string    `json:"last_error,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`

	// Source info (VCS-agnostic) - untrusted code being tested
	SourceURL  string `json:"source_url,omitempty"`
	SourceRef  string `json:"source_ref,omitempty"`
	SourceType string `json:"source_type"`
	SourcePath string `json:"source_path,omitempty"`

	// CI Source info (trusted CI pipeline code)
	CISourceType string `json:"ci_source_type,omitempty"`
	CISourceURL  string `json:"ci_source_url,omitempty"`
	CISourceRef  string `json:"ci_source_ref,omitempty"`

	// Runnerlib config
	CodeDir     string            `json:"code_dir"`
	JobDir      string            `json:"job_dir"`
	JobCommand  string            `json:"job_command"`
	RunnerImage string            `json:"runner_image"`
	JobEnvVars  map[string]string `json:"job_env_vars,omitempty"`
	JobEnvFile  string            `json:"job_env_file,omitempty"`
	RunAsUser   string            `json:"run_as_user,omitempty"`

	// Execution info
	TimeoutSeconds int        `json:"timeout_seconds"`
	Priority       int        `json:"priority"`
	QueueName      string     `json:"queue_name"`
	StartedAt      *time.Time `json:"started_at,omitempty"`
	CompletedAt    *time.Time `json:"completed_at,omitempty"`
	ExitCode       *int       `json:"exit_code,omitempty"`

	// Object store references
	LogsObjectKey      string `json:"logs_object_key,omitempty"`
	ArtifactsObjectKey string `json:"artifacts_object_key,omitempty"`

	ProjectID        *string `json:"project_id,omitempty"`
	ParentJobID      *string `json:"parent_job_id,omitempty"`
	WorkflowID       *string `json:"workflow_id,omitempty"`
	WorkflowNodeID   *string `json:"workflow_node_id,omitempty"`
	WorkflowRunID    *string `json:"workflow_run_id,omitempty"`
	WorkflowNodeName string  `json:"workflow_node_name,omitempty"`
}

// ListJobsResponse represents the response for listing jobs
type ListJobsResponse struct {
	Jobs   []JobResponse `json:"jobs"`
	Total  int           `json:"total"`
	Limit  int           `json:"limit"`
	Offset int           `json:"offset"`
}

// CreateJob handles POST /api/v1/jobs
func (h *JobHandler) CreateJob(w http.ResponseWriter, r *http.Request) {
	var req CreateJobRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.respondWithError(w, http.StatusBadRequest, store.ErrInvalidInput)
		return
	}

	// Get user from context
	user := checkauth.GetUserFromContext(r.Context())
	if user == nil {
		h.respondWithError(w, http.StatusUnauthorized, store.ErrUnauthorized)
		return
	}

	// Validate required fields
	if err := h.validateCreateJobRequest(&req); err != nil {
		// Check if this is a forbidden error (e.g., CI code URL not in allowlist)
		if err == store.ErrForbidden {
			h.respondWithError(w, http.StatusForbidden, err)
		} else {
			h.respondWithError(w, http.StatusBadRequest, err)
		}
		return
	}

	// Convert request to job model
	job := h.createJobFromRequest(&req, user.UserID)

	// Create job in database
	if err := h.store.CreateJob(r.Context(), job); err != nil {
		h.respondWithError(w, http.StatusInternalServerError, err)
		return
	}

	// Record job submission metric
	sourceTypeStr := ""
	if job.SourceType != nil {
		sourceTypeStr = string(*job.SourceType)
	}
	metrics.RecordJobSubmission(job.QueueName, sourceTypeStr)

	// Submit job to Corndogs queue
	if h.corndogsClient != nil {
		// Dereference pointer fields for payload
		sourceTypeStr := ""
		if job.SourceType != nil {
			sourceTypeStr = string(*job.SourceType)
		}
		sourceURL := ""
		if job.SourceURL != nil {
			sourceURL = *job.SourceURL
		}
		sourceRef := ""
		if job.SourceRef != nil {
			sourceRef = *job.SourceRef
		}
		sourcePath := ""
		if job.SourcePath != nil {
			sourcePath = *job.SourcePath
		}

		taskPayload := &corndogs.TaskPayload{
			JobID:   job.JobID,
			JobType: "run",
			Config: map[string]interface{}{
				"image":       job.RunnerImage,
				"command":     job.JobCommand,
				"working_dir": job.JobDir,
				"timeout":     job.TimeoutSeconds,
				"code_dir":    job.CodeDir,
				"job_dir":     job.JobDir,
			},
			Source: map[string]interface{}{
				"type":        sourceTypeStr,
				"url":         sourceURL,
				"ref":         sourceRef,
				"source_path": sourcePath,
			},
			Metadata: map[string]interface{}{
				"user_id":      job.UserID,
				"submitted_at": job.CreatedAt,
				"name":         job.Name,
				"description":  job.Description,
			},
		}

		// Add environment variables if present
		if job.JobEnvVars != nil {
			taskPayload.Config["environment"] = job.JobEnvVars
		}
		if job.JobEnvFile != "" {
			taskPayload.Config["env_file"] = job.JobEnvFile
		}

		task, err := h.corndogsClient.SubmitTask(r.Context(), taskPayload, int64(job.Priority))
		if err != nil {
			// Log error but don't fail the request - job is in DB
			log.Printf("ERROR: Failed to submit task to Corndogs - job_id=%s job_name=%s queue=%s error=%v",
				job.JobID, job.Name, job.QueueName, err)
			job.Status = "failed"
			job.LastError = fmt.Sprintf("failed to submit to Corndogs: %v", err)
			// Record failed submission metric
			metrics.RecordCornDogsTaskSubmission(job.QueueName, false)
		} else {
			// Record successful submission metric
			metrics.RecordCornDogsTaskSubmission(job.QueueName, true)
			taskID := task.Uuid
			job.CorndogsTaskID = &taskID
			job.Status = task.CurrentState
		}

		// Update job with Corndogs task ID and status
		if err := h.store.UpdateJob(r.Context(), job); err != nil {
			// Log error but continue - job was created
		}
	}

	// Return created job
	response := h.jobToResponse(job)
	h.respondWithJSON(w, http.StatusCreated, response)
}

// GetJob handles GET /api/v1/jobs/{job_id}
func (h *JobHandler) GetJob(w http.ResponseWriter, r *http.Request) {
	jobID := h.getID(r, "job_id")
	if jobID == "" {
		h.respondWithError(w, http.StatusBadRequest, store.ErrInvalidInput)
		return
	}

	job, err := h.store.GetJobByID(r.Context(), jobID)
	if err != nil {
		h.respondWithError(w, http.StatusNotFound, err)
		return
	}

	// Check if user can access this job. GetJob is a read endpoint, so it
	// additionally grants access when the job is publicly visible (see
	// canUserViewJob) — unlike the mutation endpoints below, which keep the
	// original owner-or-admin-only canUserAccessJob.
	user := checkauth.GetUserFromContext(r.Context())
	if user == nil {
		h.respondWithError(w, http.StatusUnauthorized, store.ErrUnauthorized)
		return
	}

	if !h.canUserViewJob(r.Context(), user, job) {
		h.respondWithError(w, http.StatusForbidden, store.ErrForbidden)
		return
	}

	response := h.jobToResponse(job)
	h.respondWithJSON(w, http.StatusOK, response)
}

// jobsVisibleToStore is the narrow store capability that lets ListJobs push
// visibility filtering into SQL instead of fetching a LIMIT/OFFSET page and
// then filtering it down in Go. See
// postgres_store/visibility_operations.go's ListJobsVisibleTo.
//
// Why this exists (code-review finding, "pagination before visibility
// filtering breaks lists"): ListJobs used to always fetch a page via
// h.store.ListJobs(filters, limit, offset) and THEN call
// authz.FilterVisibleJobs on the page — when the store also relaxed
// parseFilters' forced user_id scoping (see roleStoreResolver), that
// combination could return a page shorter than `limit` (or empty) even
// though more visible rows existed past the offset, and reported Total as
// the post-filtered page length instead of a real count. ListJobs now only
// ever does ONE of two things: (1) when the store implements
// jobsVisibleToStore and h.visibility is non-nil, push the visibility
// predicate into SQL via ListJobsVisibleTo, so LIMIT/OFFSET and COUNT(*)
// both operate on the already-filtered set — exact pagination, exact
// Total; or (2) fall back to the pre-authz behavior in full (parseFilters'
// STRICT own-jobs-only scoping, no post-filter at all) via
// parseFiltersStrict — never the broken middle combination of relaxed SQL
// scoping plus a post-query filter.
type jobsVisibleToStore interface {
	ListJobsVisibleTo(ctx context.Context, viewerID string, isGlobalAdmin bool, filters map[string]interface{}, limit, offset int) ([]models.Job, int64, error)
}

// ListJobs handles GET /api/v1/jobs
func (h *JobHandler) ListJobs(w http.ResponseWriter, r *http.Request) {
	user := checkauth.GetUserFromContext(r.Context())
	if user == nil {
		h.respondWithError(w, http.StatusUnauthorized, store.ErrUnauthorized)
		return
	}

	limit, offset := h.parsePagination(r)

	// Primary path: SQL-side visibility filtering with exact pagination and
	// Total (see jobsVisibleToStore's doc comment).
	if jvs, ok := h.store.(jobsVisibleToStore); ok && h.visibility != nil {
		id := authz.IdentityFromUser(user)
		isGlobalAdmin, err := h.visibility.IsGlobalAdmin(r.Context(), id)
		if err != nil {
			h.respondWithError(w, http.StatusInternalServerError, err)
			return
		}

		filters := h.parseFilters(r, user)
		jobs, total, err := jvs.ListJobsVisibleTo(r.Context(), user.UserID, isGlobalAdmin, filters, limit, offset)
		if err != nil {
			h.respondWithError(w, http.StatusInternalServerError, err)
			return
		}

		jobResponses := make([]JobResponse, len(jobs))
		for i, job := range jobs {
			jobResponses[i] = h.jobToResponse(&job)
		}
		h.respondWithJSON(w, http.StatusOK, ListJobsResponse{
			Jobs:   jobResponses,
			Total:  int(total),
			Limit:  limit,
			Offset: offset,
		})
		return
	}

	// Fallback: the wired store doesn't support SQL-side visibility (or
	// h.visibility is nil). Use the strict pre-authz own-jobs-only scoping
	// unconditionally — see parseFiltersStrict — and no post-query filter,
	// so pagination and Total (still an approximation: Total is the page
	// length, same as always in this fallback) are at least self-consistent
	// again instead of silently short-paging.
	filters := h.parseFiltersStrict(r, user)
	jobs, err := h.store.ListJobs(r.Context(), filters, limit, offset)
	if err != nil {
		h.respondWithError(w, http.StatusInternalServerError, err)
		return
	}

	jobResponses := make([]JobResponse, len(jobs))
	for i, job := range jobs {
		jobResponses[i] = h.jobToResponse(&job)
	}
	h.respondWithJSON(w, http.StatusOK, ListJobsResponse{
		Jobs:   jobResponses,
		Total:  len(jobResponses),
		Limit:  limit,
		Offset: offset,
	})
}

// CancelJob handles PUT /api/v1/jobs/{job_id}/cancel
//
// Graceful cancel: submitted/queued jobs (never started) are cancelled
// outright; running jobs are moved to "cancelling" so the worker's
// cancel-poll drives a graceful JobRunner.Stop (SIGTERM, runnerlib cleanup
// hooks, then forced kill after the configured grace). See
// UI_AUTH_PLAN.md's Cancel vs Kill section and internal/jobcontrol.CancelJob,
// which is the shared implementation this handler and the future CSIL UI
// service both call.
//
// Authz here is unchanged from the pre-existing behavior (owner or admin) —
// this matches UI_AUTH_PLAN.md's permission matrix, which grants cancel to
// (at least) the resource owner; finer-grained project-owner/org-admin
// scoping is what authz.Resolver.Capabilities computes for the CSIL UI
// service (internal/uiapi/ui_jobcontrol.go's CancelJob).
func (h *JobHandler) CancelJob(w http.ResponseWriter, r *http.Request) {
	h.cancelOrKillJob(w, r, false)
}

// KillJob handles POST /api/v1/jobs/{job_id}/kill
//
// Admin kill: immediate forced container/pod removal, no SIGTERM grace, no
// guarantee runnerlib's cleanup hooks run. See internal/jobcontrol.KillJob.
//
// Authz: per UI_AUTH_PLAN.md's permission matrix, kill is restricted to an
// org admin of the job's org (job.UserID — org == user in this schema, so
// unlike cancel this is NOT the same as "the job's creator") or a global
// admin — see canUserKillJob, which mirrors
// internal/uiapi/ui_jobcontrol.go's KillJob
// (authz.Resolver.RequireOrgAdmin(ctx, id, job.UserID)) exactly so REST and
// the CSIL UI service can't drift apart on this security-sensitive check.
func (h *JobHandler) KillJob(w http.ResponseWriter, r *http.Request) {
	h.cancelOrKillJob(w, r, true)
}

func (h *JobHandler) cancelOrKillJob(w http.ResponseWriter, r *http.Request, kill bool) {
	jobID := h.getID(r, "job_id")
	if jobID == "" {
		h.respondWithError(w, http.StatusBadRequest, store.ErrInvalidInput)
		return
	}

	job, err := h.store.GetJobByID(r.Context(), jobID)
	if err != nil {
		h.respondWithError(w, http.StatusNotFound, err)
		return
	}

	// Check if user can access this job
	user := checkauth.GetUserFromContext(r.Context())
	if user == nil {
		h.respondWithError(w, http.StatusUnauthorized, store.ErrUnauthorized)
		return
	}

	if kill {
		if !h.canUserKillJob(r.Context(), user, job) {
			h.respondWithError(w, http.StatusForbidden, store.ErrForbidden)
			return
		}
	} else if !h.canUserAccessJob(user, job) {
		h.respondWithError(w, http.StatusForbidden, store.ErrForbidden)
		return
	}

	// Check if the job can be cancelled/killed. Kill additionally admits an
	// already-"cancelling" job (models.Job.CanBeKilled): it can escalate a
	// stuck graceful cancel to an immediate kill, whereas a second graceful
	// cancel request has nothing new to do (models.Job.CanBeCancelled stays
	// false for "cancelling"). See internal/jobcontrol.transitionJob, which
	// re-checks this same distinction race-safely at the guarded-update
	// layer — this is just a fast local pre-check.
	allowed := job.CanBeCancelled()
	if kill {
		allowed = job.CanBeKilled()
	}
	if !allowed {
		h.respondWithError(w, http.StatusBadRequest, store.ErrInvalidInput)
		return
	}

	var updated *models.Job
	if kill {
		updated, err = jobcontrol.KillJob(r.Context(), h.store, h.corndogsClient, job)
	} else {
		updated, err = jobcontrol.CancelJob(r.Context(), h.store, h.corndogsClient, job)
	}
	if err != nil {
		if errors.Is(err, jobcontrol.ErrNotCancellable) {
			h.respondWithError(w, http.StatusBadRequest, store.ErrInvalidInput)
			return
		}
		h.respondWithError(w, http.StatusInternalServerError, err)
		return
	}

	response := h.jobToResponse(updated)
	h.respondWithJSON(w, http.StatusOK, response)
}

// RetryJob handles POST /api/v1/jobs/{job_id}/retry.
//
// Retries a single job in place — same workflow, same workflow node (if
// any) — by cloning its spec into a brand-new job row and resubmitting. See
// internal/jobcontrol.RetryJob, the shared implementation this handler and
// the future CSIL UI service both call.
//
// Authz: same tier as CancelJob (owner-or-admin via canUserAccessJob) —
// per UI_AUTH_PLAN.md's permission matrix, retry is not the
// admin-restricted operation kill is; it's scoped like cancel.
func (h *JobHandler) RetryJob(w http.ResponseWriter, r *http.Request) {
	jobID := h.getID(r, "job_id")
	if jobID == "" {
		h.respondWithError(w, http.StatusBadRequest, store.ErrInvalidInput)
		return
	}

	job, err := h.store.GetJobByID(r.Context(), jobID)
	if err != nil {
		h.respondWithError(w, http.StatusNotFound, err)
		return
	}

	user := checkauth.GetUserFromContext(r.Context())
	if user == nil {
		h.respondWithError(w, http.StatusUnauthorized, store.ErrUnauthorized)
		return
	}
	if !h.canUserAccessJob(user, job) {
		h.respondWithError(w, http.StatusForbidden, store.ErrForbidden)
		return
	}

	// Fast local pre-check, same pattern as cancelOrKillJob — the
	// authoritative check is models.Job.IsRetryable inside
	// jobcontrol.RetryJob itself.
	if !job.IsRetryable() {
		h.respondWithError(w, http.StatusBadRequest, store.ErrInvalidInput)
		return
	}

	newJob, err := jobcontrol.RetryJob(r.Context(), h.store, h.corndogsClient, job)
	if err != nil {
		if errors.Is(err, jobcontrol.ErrNotRetryable) {
			h.respondWithError(w, http.StatusBadRequest, store.ErrInvalidInput)
			return
		}
		h.respondWithError(w, http.StatusInternalServerError, err)
		return
	}

	response := h.jobToResponse(newJob)
	h.respondWithJSON(w, http.StatusCreated, response)
}

// DeleteJob handles DELETE /api/v1/jobs/{job_id}
func (h *JobHandler) DeleteJob(w http.ResponseWriter, r *http.Request) {
	jobID := h.getID(r, "job_id")
	if jobID == "" {
		h.respondWithError(w, http.StatusBadRequest, store.ErrInvalidInput)
		return
	}

	job, err := h.store.GetJobByID(r.Context(), jobID)
	if err != nil {
		h.respondWithError(w, http.StatusNotFound, err)
		return
	}

	// Check if user can access this job
	user := checkauth.GetUserFromContext(r.Context())
	if user == nil {
		h.respondWithError(w, http.StatusUnauthorized, store.ErrUnauthorized)
		return
	}

	// Only admins or job owners can delete jobs
	if !h.isAdmin(user) && job.UserID != user.UserID {
		h.respondWithError(w, http.StatusForbidden, store.ErrForbidden)
		return
	}

	if err := h.store.DeleteJob(r.Context(), jobID); err != nil {
		h.respondWithError(w, http.StatusInternalServerError, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// GetJobLogs handles GET /api/v1/jobs/{job_id}/logs
// Query parameters:
//   - stream: "stdout", "stderr", or "combined" (default: "combined")
func (h *JobHandler) GetJobLogs(w http.ResponseWriter, r *http.Request) {
	jobID := h.getID(r, "job_id")
	if jobID == "" {
		h.respondWithError(w, http.StatusBadRequest, store.ErrInvalidInput)
		return
	}

	job, err := h.store.GetJobByID(r.Context(), jobID)
	if err != nil {
		h.respondWithError(w, http.StatusNotFound, err)
		return
	}

	// Check if user can access this job. Read endpoint: also allow public
	// visibility, same as GetJob.
	user := checkauth.GetUserFromContext(r.Context())
	if user == nil {
		h.respondWithError(w, http.StatusUnauthorized, store.ErrUnauthorized)
		return
	}

	if !h.canUserViewJob(r.Context(), user, job) {
		h.respondWithError(w, http.StatusForbidden, store.ErrForbidden)
		return
	}

	// Check if object store is configured
	if h.objectStore == nil {
		h.respondWithError(w, http.StatusServiceUnavailable, store.ErrServiceUnavailable)
		return
	}

	// Get the stream parameter (default to combined)
	stream := r.URL.Query().Get("stream")
	if stream == "" {
		stream = "combined"
	}

	// Validate stream parameter
	if stream != "stdout" && stream != "stderr" && stream != "combined" {
		h.respondWithError(w, http.StatusBadRequest, store.ErrInvalidInput)
		return
	}

	// Build log object keys based on job ID
	// Log format: logs/{job_id}/{stdout|stderr}.json (JSON array format)
	var logContent []byte

	switch stream {
	case "stdout":
		key := fmt.Sprintf("logs/%s/stdout.json", jobID)
		content, err := h.fetchLogContent(r.Context(), key)
		if err != nil {
			if err == objects.ErrNotFound {
				h.respondWithError(w, http.StatusNotFound, store.ErrNotFound)
				return
			}
			h.respondWithError(w, http.StatusInternalServerError, err)
			return
		}
		logContent = content

	case "stderr":
		key := fmt.Sprintf("logs/%s/stderr.json", jobID)
		content, err := h.fetchLogContent(r.Context(), key)
		if err != nil {
			if err == objects.ErrNotFound {
				h.respondWithError(w, http.StatusNotFound, store.ErrNotFound)
				return
			}
			h.respondWithError(w, http.StatusInternalServerError, err)
			return
		}
		logContent = content

	case "combined":
		// Fetch both stdout and stderr, combine them into a single sorted array
		stdoutKey := fmt.Sprintf("logs/%s/stdout.json", jobID)
		stderrKey := fmt.Sprintf("logs/%s/stderr.json", jobID)

		stdoutContent, stdoutErr := h.fetchLogContent(r.Context(), stdoutKey)
		stderrContent, stderrErr := h.fetchLogContent(r.Context(), stderrKey)

		// If both are not found, return 404
		if stdoutErr == objects.ErrNotFound && stderrErr == objects.ErrNotFound {
			h.respondWithError(w, http.StatusNotFound, store.ErrNotFound)
			return
		}

		// Handle other errors
		if stdoutErr != nil && stdoutErr != objects.ErrNotFound {
			h.respondWithError(w, http.StatusInternalServerError, stdoutErr)
			return
		}
		if stderrErr != nil && stderrErr != objects.ErrNotFound {
			h.respondWithError(w, http.StatusInternalServerError, stderrErr)
			return
		}

		// Merge JSON arrays and sort by timestamp
		combined, err := h.mergeAndSortLogArrays(stdoutContent, stderrContent)
		if err != nil {
			h.respondWithError(w, http.StatusInternalServerError, err)
			return
		}
		logContent = combined
	}

	// Return logs as JSON
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(logContent)))
	w.WriteHeader(http.StatusOK)
	w.Write(logContent)
}

// SubmitTriggersResponse represents the response for trigger submission
type SubmitTriggersResponse struct {
	CreatedJobIDs []string `json:"created_job_ids"`
	Count         int      `json:"count"`
}

// SubmitTriggers handles POST /api/v1/jobs/{job_id}/triggers
func (h *JobHandler) SubmitTriggers(w http.ResponseWriter, r *http.Request) {
	jobID := h.getID(r, "job_id")
	if jobID == "" {
		h.respondWithError(w, http.StatusBadRequest, store.ErrInvalidInput)
		return
	}

	parentJob, err := h.store.GetJobByID(r.Context(), jobID)
	if err != nil {
		h.respondWithError(w, http.StatusNotFound, err)
		return
	}

	// Check if user can access this job
	user := checkauth.GetUserFromContext(r.Context())
	if user == nil {
		h.respondWithError(w, http.StatusUnauthorized, store.ErrUnauthorized)
		return
	}

	if !h.canUserAccessJob(user, parentJob) {
		h.respondWithError(w, http.StatusForbidden, store.ErrForbidden)
		return
	}

	// Read request body
	body, err := io.ReadAll(r.Body)
	if err != nil {
		h.respondWithError(w, http.StatusBadRequest, store.ErrInvalidInput)
		return
	}

	// Process triggers via TriggerProcessor
	createdJobIDs, err := h.triggerProcessor.ProcessTriggersFromData(r.Context(), body, "", parentJob)
	if err != nil {
		h.respondWithError(w, http.StatusBadRequest, store.ErrInvalidInput)
		return
	}

	if createdJobIDs == nil {
		createdJobIDs = []string{}
	}

	h.respondWithJSON(w, http.StatusCreated, SubmitTriggersResponse{
		CreatedJobIDs: createdJobIDs,
		Count:         len(createdJobIDs),
	})
}

// fetchLogContent retrieves log content from object storage
func (h *JobHandler) fetchLogContent(ctx context.Context, key string) ([]byte, error) {
	reader, err := h.objectStore.Get(ctx, key)
	if err != nil {
		return nil, err
	}
	defer reader.Close()

	content, err := io.ReadAll(reader)
	if err != nil {
		return nil, fmt.Errorf("failed to read log content: %w", err)
	}

	return content, nil
}

// mergeAndSortLogArrays merges two JSON log arrays and sorts by timestamp
func (h *JobHandler) mergeAndSortLogArrays(stdoutContent, stderrContent []byte) ([]byte, error) {
	var allEntries []LogEntry

	// Parse stdout entries if present
	if len(stdoutContent) > 0 {
		var stdoutEntries []LogEntry
		if err := json.Unmarshal(stdoutContent, &stdoutEntries); err != nil {
			return nil, fmt.Errorf("failed to parse stdout logs: %w", err)
		}
		allEntries = append(allEntries, stdoutEntries...)
	}

	// Parse stderr entries if present
	if len(stderrContent) > 0 {
		var stderrEntries []LogEntry
		if err := json.Unmarshal(stderrContent, &stderrEntries); err != nil {
			return nil, fmt.Errorf("failed to parse stderr logs: %w", err)
		}
		allEntries = append(allEntries, stderrEntries...)
	}

	// Sort by timestamp
	sort.Slice(allEntries, func(i, j int) bool {
		return allEntries[i].Timestamp < allEntries[j].Timestamp
	})

	// Marshal back to JSON
	result, err := json.Marshal(allEntries)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal combined logs: %w", err)
	}

	return result, nil
}

// Helper methods

func (h *JobHandler) validateCreateJobRequest(req *CreateJobRequest) error {
	if req.Name == "" {
		return store.ErrInvalidInput
	}

	if req.JobCommand == "" {
		return store.ErrInvalidInput
	}

	if req.SourceType != "git" && req.SourceType != "copy" {
		return store.ErrInvalidInput
	}

	if req.SourceType == "git" && req.SourceURL == "" {
		return store.ErrInvalidInput
	}

	if req.SourceType == "copy" && req.SourcePath == "" {
		return store.ErrInvalidInput
	}
	if _, err := worker.NormalizeRunAsUser(req.RunAsUser); err != nil {
		return store.ErrInvalidInput
	}

	// Validate CI source fields if provided
	if req.CISourceType != "" {
		if req.CISourceType != "git" && req.CISourceType != "copy" {
			return store.ErrInvalidInput
		}

		if req.CISourceType == "git" && req.CISourceURL == "" {
			return store.ErrInvalidInput
		}

		if req.CISourceType == "copy" {
			// Copy type not supported for security - could allow local path injection
			log.Printf("WARNING: Rejected ci_source_type 'copy' - not yet supported for security reasons")
			return store.ErrInvalidInput
		}

		// Validate CI code URL against allowlist
		if req.CISourceURL != "" {
			if err := h.validateCiCodeURL(req.CISourceURL); err != nil {
				return err
			}
		}
	}

	return nil
}

// validateCiCodeURL validates that a CI source URL is in the allowlist
// Returns store.ErrForbidden if the URL is not allowed
func (h *JobHandler) validateCiCodeURL(ciSourceURL string) error {
	// Get the allowlist from config
	allowlist := config.CiCodeAllowlist

	// If allowlist is empty, warn but allow (not recommended for production)
	if allowlist == "" {
		log.Printf("WARNING: REACTORCIDE_CI_CODE_ALLOWLIST is not configured - all CI code URLs are allowed. " +
			"This is a security risk and not recommended for production.")
		return nil
	}

	// Parse the comma-separated allowlist
	allowedURLs := strings.Split(allowlist, ",")

	// Normalize the CI source URL
	normalizedCIURL := vcs.NormalizeRepoURL(ciSourceURL)

	// Check if the URL is in the allowlist
	for _, allowedURL := range allowedURLs {
		allowedURL = strings.TrimSpace(allowedURL)
		if allowedURL == "" {
			continue
		}

		// Use URL matching to handle different formats
		if vcs.MatchRepoURL(ciSourceURL, allowedURL) {
			return nil
		}
	}

	// URL not in allowlist - log and return forbidden error
	log.Printf("SECURITY: Rejected CI source URL not in allowlist: %s (normalized: %s)", ciSourceURL, normalizedCIURL)
	return store.ErrForbidden
}

func (h *JobHandler) createJobFromRequest(req *CreateJobRequest, userID string) *models.Job {
	// Convert source type string to SourceType enum
	var sourceType models.SourceType
	switch req.SourceType {
	case "git":
		sourceType = models.SourceTypeGit
	case "copy":
		sourceType = models.SourceTypeCopy
	default:
		sourceType = models.SourceTypeNone
	}

	job := &models.Job{
		UserID:      userID,
		Name:        req.Name,
		Description: req.Description,
		JobFile:     req.JobFile,
		Status:      "submitted",

		SourceURL:  &req.SourceURL,
		SourceRef:  &req.SourceRef,
		SourceType: &sourceType,
		SourcePath: &req.SourcePath,

		JobCommand:  req.JobCommand,
		CodeDir:     worker.DefaultJobCodeDir(req.CodeDir),
		JobDir:      worker.DefaultJobDir(req.CodeDir, req.JobDir),
		RunnerImage: req.RunnerImage,
		JobEnvFile:  req.JobEnvFile,
		RunAsUser:   req.RunAsUser,

		QueueName: req.QueueName,
	}

	// Handle CI source fields with defaults if not provided
	if req.CISourceType != "" {
		// Convert CI source type to enum
		var ciSourceType models.SourceType
		switch req.CISourceType {
		case "git":
			ciSourceType = models.SourceTypeGit
		case "copy":
			ciSourceType = models.SourceTypeCopy
		default:
			ciSourceType = models.SourceTypeNone
		}
		job.CISourceType = &ciSourceType

		// Use provided CI source URL or fall back to default
		ciSourceURL := req.CISourceURL
		if ciSourceURL == "" && config.DefaultCiSourceURL != "" {
			ciSourceURL = config.DefaultCiSourceURL
		}
		if ciSourceURL != "" {
			job.CISourceURL = &ciSourceURL
		}

		// Use provided CI source ref or fall back to default
		ciSourceRef := req.CISourceRef
		if ciSourceRef == "" && config.DefaultCiSourceRef != "" {
			ciSourceRef = config.DefaultCiSourceRef
		}
		if ciSourceRef != "" {
			job.CISourceRef = &ciSourceRef
		}
	}

	// Set defaults
	// Note: CodeDir is intentionally not defaulted - if not specified,
	// the container will use its own WORKDIR from the image
	if job.RunnerImage == "" && config.DefaultRunnerImage != "" {
		job.RunnerImage = config.DefaultRunnerImage
	}
	if job.QueueName == "" {
		job.QueueName = "reactorcide-jobs"
	}

	// Set timeout and priority
	if req.TimeoutSeconds != nil {
		job.TimeoutSeconds = *req.TimeoutSeconds
	} else {
		job.TimeoutSeconds = 3600 // 1 hour default
	}

	if req.Priority != nil {
		job.Priority = *req.Priority
	}

	// Convert env vars
	if req.JobEnvVars != nil {
		job.JobEnvVars = make(map[string]interface{})
		for k, v := range req.JobEnvVars {
			job.JobEnvVars[k] = v
		}
	}

	return job
}

func (h *JobHandler) jobToResponse(job *models.Job) JobResponse {
	// Handle pointer fields for source
	sourceURL := ""
	if job.SourceURL != nil {
		sourceURL = *job.SourceURL
	}
	sourceRef := ""
	if job.SourceRef != nil {
		sourceRef = *job.SourceRef
	}
	sourceType := ""
	if job.SourceType != nil {
		sourceType = string(*job.SourceType)
	}
	sourcePath := ""
	if job.SourcePath != nil {
		sourcePath = *job.SourcePath
	}

	// Handle pointer fields for CI source
	ciSourceType := ""
	if job.CISourceType != nil {
		ciSourceType = string(*job.CISourceType)
	}
	ciSourceURL := ""
	if job.CISourceURL != nil {
		ciSourceURL = *job.CISourceURL
	}
	ciSourceRef := ""
	if job.CISourceRef != nil {
		ciSourceRef = *job.CISourceRef
	}

	response := JobResponse{
		JobID:       job.JobID,
		Name:        job.Name,
		Description: job.Description,
		JobFile:     job.JobFile,
		Status:      job.Status,
		LastError:   job.LastError,
		CreatedAt:   job.CreatedAt,
		UpdatedAt:   job.UpdatedAt,

		SourceURL:  sourceURL,
		SourceRef:  sourceRef,
		SourceType: sourceType,
		SourcePath: sourcePath,

		CISourceType: ciSourceType,
		CISourceURL:  ciSourceURL,
		CISourceRef:  ciSourceRef,

		CodeDir:        job.CodeDir,
		JobDir:         job.JobDir,
		JobCommand:     job.JobCommand,
		RunnerImage:    job.RunnerImage,
		JobEnvFile:     job.JobEnvFile,
		RunAsUser:      job.RunAsUser,
		TimeoutSeconds: job.TimeoutSeconds,
		Priority:       job.Priority,
		QueueName:      job.QueueName,

		StartedAt:   job.StartedAt,
		CompletedAt: job.CompletedAt,
		ExitCode:    job.ExitCode,

		LogsObjectKey:      job.LogsObjectKey,
		ArtifactsObjectKey: job.ArtifactsObjectKey,

		ProjectID:        job.ProjectID,
		ParentJobID:      job.ParentJobID,
		WorkflowID:       job.WorkflowID,
		WorkflowNodeID:   job.WorkflowNodeID,
		WorkflowRunID:    job.WorkflowRunID,
		WorkflowNodeName: job.WorkflowNodeName,
	}

	// Convert env vars
	if job.JobEnvVars != nil {
		response.JobEnvVars = make(map[string]string)
		for k, v := range job.JobEnvVars {
			if str, ok := v.(string); ok {
				response.JobEnvVars[k] = str
			}
		}
	}

	return response
}

func (h *JobHandler) canUserAccessJob(user *models.User, job *models.Job) bool {
	// Admins can access all jobs
	if h.isAdmin(user) {
		return true
	}

	// Users can access their own jobs
	return job.UserID == user.UserID
}

// canUserViewJob is canUserAccessJob plus public visibility (additive, read
// endpoints only — see the JobHandler.visibility field doc). h.visibility
// is nil whenever the wired store doesn't support authz role/visibility
// lookups (e.g. the test mocks in job_handler_test.go), so this is exactly
// canUserAccessJob's original owner-or-admin check in that case.
func (h *JobHandler) canUserViewJob(ctx context.Context, user *models.User, job *models.Job) bool {
	if h.canUserAccessJob(user, job) {
		return true
	}
	if h.visibility == nil {
		return false
	}
	visible, err := h.visibility.CanViewJob(ctx, authz.IdentityFromUser(user), job)
	return err == nil && visible
}

// canUserKillJob reports whether user may force-kill job. Per
// UI_AUTH_PLAN.md's permission matrix, kill is restricted to an org admin
// of the job's org (job.UserID) or a global admin — plain job ownership is
// NOT by itself sufficient (unlike cancel), though in practice a job's
// creator is also their own org's admin (users act as orgs in this schema
// — see authz.Resolver.IsOrgAdmin's doc comment), so the common case of "I
// killed my own job" still works via that reflexive org-admin rule, not via
// a separate ownership check here.
//
// When h.visibility is nil (the wired store doesn't satisfy authz.RoleStore
// — see the JobHandler.visibility field doc, now logged loudly at
// construction time by roleStoreResolver), this FAILS CLOSED: only the
// legacy isAdmin(user) check (user.Roles contains "admin"/"system_admin")
// is honored. Job ownership alone is deliberately NOT enough to kill in
// that fallback, unlike canUserAccessJob — kill is the one mutation where a
// missing authz resolver must narrow access, not just skip an additive
// widening.
func (h *JobHandler) canUserKillJob(ctx context.Context, user *models.User, job *models.Job) bool {
	if h.visibility == nil {
		return h.isAdmin(user)
	}
	err := h.visibility.RequireOrgAdmin(ctx, authz.IdentityFromUser(user), job.UserID)
	return err == nil
}

func (h *JobHandler) isAdmin(user *models.User) bool {
	for _, role := range user.Roles {
		if role == "admin" || role == "system_admin" {
			return true
		}
	}
	return false
}

func (h *JobHandler) parsePagination(r *http.Request) (limit, offset int) {
	limit = 20 // default
	offset = 0 // default

	if limitStr := r.URL.Query().Get("limit"); limitStr != "" {
		if l, err := strconv.Atoi(limitStr); err == nil && l > 0 && l <= 100 {
			limit = l
		}
	}

	if offsetStr := r.URL.Query().Get("offset"); offsetStr != "" {
		if o, err := strconv.Atoi(offsetStr); err == nil && o >= 0 {
			offset = o
		}
	}

	return limit, offset
}

// commonJobQueryFilters parses the filter query parameters ListJobs honors
// regardless of user-scoping policy (status, queue_name, source_type,
// project_id, workflow_id). user_id scoping is decided separately by
// parseFilters/parseFiltersStrict since the two callers apply different
// policies there.
func (h *JobHandler) commonJobQueryFilters(r *http.Request) map[string]interface{} {
	filters := make(map[string]interface{})

	if status := r.URL.Query().Get("status"); status != "" {
		validStatuses := []string{"submitted", "queued", "running", "cancelling", "completed", "failed", "cancelled", "timeout"}
		for _, validStatus := range validStatuses {
			if status == validStatus {
				filters["status"] = status
				break
			}
		}
	}

	if queue := r.URL.Query().Get("queue_name"); queue != "" {
		filters["queue_name"] = queue
	}

	if sourceType := r.URL.Query().Get("source_type"); sourceType != "" {
		if sourceType == "git" || sourceType == "copy" {
			filters["source_type"] = sourceType
		}
	}

	if projectID := r.URL.Query().Get("project_id"); projectID != "" {
		filters["project_id"] = projectID
	}
	if workflowID := r.URL.Query().Get("workflow_id"); workflowID != "" {
		filters["workflow_id"] = workflowID
	}

	return filters
}

// parseFilters builds ListJobs' filter set for the SQL-side-visibility
// primary path (jobsVisibleToStore — see ListJobs). Non-admins are NOT
// restricted to their own jobs here, with or without an explicit
// ?user_id=: the visibility predicate ListJobsVisibleTo evaluates in SQL is
// the actual authorization decision for every row this query can return
// (own jobs, public jobs, and anything the caller has an org-admin/
// project-role grant on), so leaving user_id unset lets that predicate
// determine the full breadth, and an explicit ?user_id= override is always
// safe to honor too (it can only narrow the visible set further, never
// widen it). Do not call this for a store that lacks jobsVisibleToStore —
// see parseFiltersStrict for that fallback, which forces the pre-authz
// own-jobs-only scoping instead.
func (h *JobHandler) parseFilters(r *http.Request, user *models.User) map[string]interface{} {
	filters := h.commonJobQueryFilters(r)
	if userID := r.URL.Query().Get("user_id"); userID != "" {
		filters["user_id"] = userID
	}
	return filters
}

// parseFiltersStrict is ListJobs' fallback-path filter builder (store
// doesn't implement jobsVisibleToStore, or h.visibility is nil): non-admins
// are unconditionally restricted to their own jobs at the SQL layer — the
// original pre-authz behavior — since there is no SQL-side (or post-query)
// visibility check backing a wider query in this path. See ListJobs and
// jobsVisibleToStore's doc comment for why this must never be paired with a
// relaxed filter.
func (h *JobHandler) parseFiltersStrict(r *http.Request, user *models.User) map[string]interface{} {
	filters := h.commonJobQueryFilters(r)
	if !h.isAdmin(user) {
		filters["user_id"] = user.UserID
	} else if userID := r.URL.Query().Get("user_id"); userID != "" {
		filters["user_id"] = userID
	}
	return filters
}
