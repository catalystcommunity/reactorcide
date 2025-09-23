package handlers

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/checkauth"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/corndogs"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/metrics"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

// JobHandler handles job-related HTTP requests
type JobHandler struct {
	BaseHandler
	store          store.Store
	corndogsClient corndogs.ClientInterface
}

// NewJobHandler creates a new job handler
func NewJobHandler(store store.Store, corndogsClient corndogs.ClientInterface) *JobHandler {
	return &JobHandler{
		store:          store,
		corndogsClient: corndogsClient,
	}
}

// CreateJobRequest represents the request payload for creating a job
type CreateJobRequest struct {
	Name        string `json:"name" validate:"required,max=255"`
	Description string `json:"description,omitempty"`

	// Source configuration
	GitURL     string `json:"git_url,omitempty"`
	GitRef     string `json:"git_ref,omitempty"`
	SourceType string `json:"source_type" validate:"required,oneof=git copy"`
	SourcePath string `json:"source_path,omitempty"`

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

	// Source info
	GitURL     string `json:"git_url,omitempty"`
	GitRef     string `json:"git_ref,omitempty"`
	SourceType string `json:"source_type"`
	SourcePath string `json:"source_path,omitempty"`

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
		h.respondWithError(w, http.StatusBadRequest, err)
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
	metrics.RecordJobSubmission(job.QueueName, job.SourceType)

	// Submit job to Corndogs queue
	if h.corndogsClient != nil {
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
				"type":        job.SourceType,
				"url":         job.GitURL,
				"ref":         job.GitRef,
				"source_path": job.SourcePath,
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
			// TODO: Add proper logging
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

	if req.SourceType == "git" && req.GitURL == "" {
		return store.ErrInvalidInput
	}

	if req.SourceType == "copy" && req.SourcePath == "" {
		return store.ErrInvalidInput
	}

	return nil
}

func (h *JobHandler) createJobFromRequest(req *CreateJobRequest, userID string) *models.Job {
	job := &models.Job{
		UserID:      userID,
		Name:        req.Name,
		Description: req.Description,
		Status:      "submitted",

		GitURL:     req.GitURL,
		GitRef:     req.GitRef,
		SourceType: req.SourceType,
		SourcePath: req.SourcePath,

		JobCommand:  req.JobCommand,
		CodeDir:     req.CodeDir,
		JobDir:      req.JobDir,
		RunnerImage: req.RunnerImage,
		JobEnvFile:  req.JobEnvFile,

		QueueName: req.QueueName,
	}

	// Set defaults
	if job.CodeDir == "" {
		job.CodeDir = "/job/src"
	}
	if job.JobDir == "" {
		job.JobDir = job.CodeDir
	}
	if job.RunnerImage == "" {
		job.RunnerImage = "quay.io/catalystcommunity/reactorcide_runner"
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
	response := JobResponse{
		JobID:       job.JobID,
		Name:        job.Name,
		Description: job.Description,
		Status:      job.Status,
		CreatedAt:   job.CreatedAt,
		UpdatedAt:   job.UpdatedAt,

		GitURL:     job.GitURL,
		GitRef:     job.GitRef,
		SourceType: job.SourceType,
		SourcePath: job.SourcePath,

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
