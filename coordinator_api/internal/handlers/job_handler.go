package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/checkauth"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/config"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/corndogs"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/metrics"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/objects"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/vcs"
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
	store          store.Store
	corndogsClient corndogs.ClientInterface
	objectStore    objects.ObjectStore
}

// NewJobHandler creates a new job handler
func NewJobHandler(store store.Store, corndogsClient corndogs.ClientInterface) *JobHandler {
	return &JobHandler{
		store:          store,
		corndogsClient: corndogsClient,
	}
}

// NewJobHandlerWithObjectStore creates a new job handler with object store support
func NewJobHandlerWithObjectStore(store store.Store, corndogsClient corndogs.ClientInterface, objectStore objects.ObjectStore) *JobHandler {
	return &JobHandler{
		store:          store,
		corndogsClient: corndogsClient,
		objectStore:    objectStore,
	}
}

// CreateJobRequest represents the request payload for creating a job
type CreateJobRequest struct {
	Name        string `json:"name" validate:"required,max=255"`
	Description string `json:"description,omitempty"`

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
	QueueName      string `json:"queue_name,omitempty"`
}

// JobResponse represents the response for job operations
type JobResponse struct {
	JobID       string    `json:"job_id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Status      string    `json:"status"`
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

	// Check if user can access this job
	user := checkauth.GetUserFromContext(r.Context())
	if user == nil {
		h.respondWithError(w, http.StatusUnauthorized, store.ErrUnauthorized)
		return
	}

	if !h.canUserAccessJob(user, job) {
		h.respondWithError(w, http.StatusForbidden, store.ErrForbidden)
		return
	}

	response := h.jobToResponse(job)
	h.respondWithJSON(w, http.StatusOK, response)
}

// ListJobs handles GET /api/v1/jobs
func (h *JobHandler) ListJobs(w http.ResponseWriter, r *http.Request) {
	user := checkauth.GetUserFromContext(r.Context())
	if user == nil {
		h.respondWithError(w, http.StatusUnauthorized, store.ErrUnauthorized)
		return
	}

	// Parse query parameters
	limit, offset := h.parsePagination(r)
	filters := h.parseFilters(r, user)

	// Get jobs from database
	jobs, err := h.store.ListJobs(r.Context(), filters, limit, offset)
	if err != nil {
		h.respondWithError(w, http.StatusInternalServerError, err)
		return
	}

	// Convert to response format
	jobResponses := make([]JobResponse, len(jobs))
	for i, job := range jobs {
		jobResponses[i] = h.jobToResponse(&job)
	}

	response := ListJobsResponse{
		Jobs:   jobResponses,
		Total:  len(jobResponses), // TODO: Get actual total count
		Limit:  limit,
		Offset: offset,
	}

	h.respondWithJSON(w, http.StatusOK, response)
}

// CancelJob handles PUT /api/v1/jobs/{job_id}/cancel
func (h *JobHandler) CancelJob(w http.ResponseWriter, r *http.Request) {
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

	if !h.canUserAccessJob(user, job) {
		h.respondWithError(w, http.StatusForbidden, store.ErrForbidden)
		return
	}

	// Check if job can be cancelled
	if !job.CanBeCancelled() {
		h.respondWithError(w, http.StatusBadRequest, store.ErrInvalidInput)
		return
	}

	// Cancel job in Corndogs queue first
	if h.corndogsClient != nil && job.CorndogsTaskID != nil && *job.CorndogsTaskID != "" {
		_, err := h.corndogsClient.CancelTask(r.Context(), *job.CorndogsTaskID, job.Status)
		if err != nil {
			// Log error but continue - we'll still mark as cancelled in DB
		}
	}

	// Update job status
	job.Status = "cancelled"
	job.UpdatedAt = time.Now()

	if err := h.store.UpdateJob(r.Context(), job); err != nil {
		h.respondWithError(w, http.StatusInternalServerError, err)
		return
	}

	response := h.jobToResponse(job)
	h.respondWithJSON(w, http.StatusOK, response)
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

	// Check if user can access this job
	user := checkauth.GetUserFromContext(r.Context())
	if user == nil {
		h.respondWithError(w, http.StatusUnauthorized, store.ErrUnauthorized)
		return
	}

	if !h.canUserAccessJob(user, job) {
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
		Status:      "submitted",

		SourceURL:  &req.SourceURL,
		SourceRef:  &req.SourceRef,
		SourceType: &sourceType,
		SourcePath: &req.SourcePath,

		JobCommand:  req.JobCommand,
		CodeDir:     req.CodeDir,
		JobDir:      req.JobDir,
		RunnerImage: req.RunnerImage,
		JobEnvFile:  req.JobEnvFile,

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
	if job.CodeDir == "" {
		job.CodeDir = "/job/src"
	}
	if job.JobDir == "" {
		job.JobDir = job.CodeDir
	}
	if job.RunnerImage == "" {
		job.RunnerImage = "reactorcide/runner:latest"
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
		Status:      job.Status,
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
		TimeoutSeconds: job.TimeoutSeconds,
		Priority:       job.Priority,
		QueueName:      job.QueueName,

		StartedAt:   job.StartedAt,
		CompletedAt: job.CompletedAt,
		ExitCode:    job.ExitCode,

		LogsObjectKey:      job.LogsObjectKey,
		ArtifactsObjectKey: job.ArtifactsObjectKey,
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

func (h *JobHandler) parseFilters(r *http.Request, user *models.User) map[string]interface{} {
	filters := make(map[string]interface{})

	// Non-admins can only see their own jobs
	if !h.isAdmin(user) {
		filters["user_id"] = user.UserID
	} else {
		// Admins can filter by user_id if specified
		if userID := r.URL.Query().Get("user_id"); userID != "" {
			filters["user_id"] = userID
		}
	}

	// Status filter
	if status := r.URL.Query().Get("status"); status != "" {
		validStatuses := []string{"submitted", "queued", "running", "completed", "failed", "cancelled", "timeout"}
		for _, validStatus := range validStatuses {
			if status == validStatus {
				filters["status"] = status
				break
			}
		}
	}

	// Queue filter
	if queue := r.URL.Query().Get("queue_name"); queue != "" {
		filters["queue_name"] = queue
	}

	// Source type filter
	if sourceType := r.URL.Query().Get("source_type"); sourceType != "" {
		if sourceType == "git" || sourceType == "copy" {
			filters["source_type"] = sourceType
		}
	}

	return filters
}
