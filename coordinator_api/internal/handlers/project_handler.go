package handlers

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/checkauth"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

// ProjectHandler handles project CRUD operations
type ProjectHandler struct {
	BaseHandler
	store store.Store
}

// NewProjectHandler creates a new ProjectHandler
func NewProjectHandler(store store.Store) *ProjectHandler {
	return &ProjectHandler{store: store}
}

// CreateProjectRequest represents the request body for creating a project
type CreateProjectRequest struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	RepoURL     string `json:"repo_url"`

	Enabled           *bool    `json:"enabled,omitempty"`
	TargetBranches    []string `json:"target_branches,omitempty"`
	AllowedEventTypes []string `json:"allowed_event_types,omitempty"`

	DefaultCISourceType string `json:"default_ci_source_type,omitempty"`
	DefaultCISourceURL  string `json:"default_ci_source_url,omitempty"`
	DefaultCISourceRef  string `json:"default_ci_source_ref,omitempty"`

	DefaultRunnerImage    string `json:"default_runner_image,omitempty"`
	DefaultJobCommand     string `json:"default_job_command,omitempty"`
	DefaultTimeoutSeconds *int   `json:"default_timeout_seconds,omitempty"`
	DefaultQueueName      string `json:"default_queue_name,omitempty"`

	VCSTokenSecret string `json:"vcs_token_secret,omitempty"`
	WebhookSecret  string `json:"webhook_secret,omitempty"`
}

// UpdateProjectRequest represents the request body for updating a project
type UpdateProjectRequest struct {
	Name        *string `json:"name,omitempty"`
	Description *string `json:"description,omitempty"`
	RepoURL     *string `json:"repo_url,omitempty"`

	Enabled           *bool    `json:"enabled,omitempty"`
	TargetBranches    []string `json:"target_branches,omitempty"`
	AllowedEventTypes []string `json:"allowed_event_types,omitempty"`

	DefaultCISourceType *string `json:"default_ci_source_type,omitempty"`
	DefaultCISourceURL  *string `json:"default_ci_source_url,omitempty"`
	DefaultCISourceRef  *string `json:"default_ci_source_ref,omitempty"`

	DefaultRunnerImage    *string `json:"default_runner_image,omitempty"`
	DefaultJobCommand     *string `json:"default_job_command,omitempty"`
	DefaultTimeoutSeconds *int    `json:"default_timeout_seconds,omitempty"`
	DefaultQueueName      *string `json:"default_queue_name,omitempty"`

	VCSTokenSecret *string `json:"vcs_token_secret,omitempty"`
	WebhookSecret  *string `json:"webhook_secret,omitempty"`
}

// ProjectResponse represents the response body for a project
type ProjectResponse struct {
	ProjectID   string    `json:"project_id"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	RepoURL     string    `json:"repo_url"`

	Enabled           bool     `json:"enabled"`
	TargetBranches    []string `json:"target_branches"`
	AllowedEventTypes []string `json:"allowed_event_types"`

	DefaultCISourceType string `json:"default_ci_source_type"`
	DefaultCISourceURL  string `json:"default_ci_source_url,omitempty"`
	DefaultCISourceRef  string `json:"default_ci_source_ref"`

	DefaultRunnerImage    string `json:"default_runner_image"`
	DefaultJobCommand     string `json:"default_job_command,omitempty"`
	DefaultTimeoutSeconds int    `json:"default_timeout_seconds"`
	DefaultQueueName      string `json:"default_queue_name"`

	VCSTokenSecret string `json:"vcs_token_secret,omitempty"`
	WebhookSecret  string `json:"webhook_secret,omitempty"`
}

// ListProjectsResponse represents the response body for listing projects
type ListProjectsResponse struct {
	Projects []ProjectResponse `json:"projects"`
	Total    int               `json:"total"`
	Limit    int               `json:"limit"`
	Offset   int               `json:"offset"`
}

func projectToResponse(p *models.Project) ProjectResponse {
	return ProjectResponse{
		ProjectID:             p.ProjectID,
		CreatedAt:             p.CreatedAt,
		UpdatedAt:             p.UpdatedAt,
		Name:                  p.Name,
		Description:           p.Description,
		RepoURL:               p.RepoURL,
		Enabled:               p.Enabled,
		TargetBranches:        p.TargetBranches,
		AllowedEventTypes:     p.AllowedEventTypes,
		DefaultCISourceType:   string(p.DefaultCISourceType),
		DefaultCISourceURL:    p.DefaultCISourceURL,
		DefaultCISourceRef:    p.DefaultCISourceRef,
		DefaultRunnerImage:    p.DefaultRunnerImage,
		DefaultJobCommand:     p.DefaultJobCommand,
		DefaultTimeoutSeconds: p.DefaultTimeoutSeconds,
		DefaultQueueName:      p.DefaultQueueName,
		VCSTokenSecret:        p.VCSTokenSecret,
		WebhookSecret:         p.WebhookSecret,
	}
}

// CreateProject handles POST /api/v1/projects
func (h *ProjectHandler) CreateProject(w http.ResponseWriter, r *http.Request) {
	user := checkauth.GetUserFromContext(r.Context())
	if user == nil {
		h.respondWithError(w, http.StatusUnauthorized, store.ErrUnauthorized)
		return
	}

	var req CreateProjectRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.respondWithError(w, http.StatusBadRequest, store.ErrInvalidInput)
		return
	}

	if req.Name == "" || req.RepoURL == "" {
		h.respondWithError(w, http.StatusBadRequest, store.ErrInvalidInput)
		return
	}

	project := &models.Project{
		Name:        req.Name,
		Description: req.Description,
		RepoURL:     req.RepoURL,
	}

	if req.Enabled != nil {
		project.Enabled = *req.Enabled
	}
	if req.TargetBranches != nil {
		project.TargetBranches = req.TargetBranches
	}
	if req.AllowedEventTypes != nil {
		project.AllowedEventTypes = req.AllowedEventTypes
	}
	if req.DefaultCISourceType != "" {
		project.DefaultCISourceType = models.SourceType(req.DefaultCISourceType)
	}
	if req.DefaultCISourceURL != "" {
		project.DefaultCISourceURL = req.DefaultCISourceURL
	}
	if req.DefaultCISourceRef != "" {
		project.DefaultCISourceRef = req.DefaultCISourceRef
	}
	if req.DefaultRunnerImage != "" {
		project.DefaultRunnerImage = req.DefaultRunnerImage
	}
	if req.DefaultJobCommand != "" {
		project.DefaultJobCommand = req.DefaultJobCommand
	}
	if req.DefaultTimeoutSeconds != nil {
		project.DefaultTimeoutSeconds = *req.DefaultTimeoutSeconds
	}
	if req.DefaultQueueName != "" {
		project.DefaultQueueName = req.DefaultQueueName
	}
	if req.VCSTokenSecret != "" {
		project.VCSTokenSecret = req.VCSTokenSecret
	}
	if req.WebhookSecret != "" {
		project.WebhookSecret = req.WebhookSecret
	}

	if err := h.store.CreateProject(r.Context(), project); err != nil {
		h.respondWithError(w, http.StatusInternalServerError, err)
		return
	}

	h.respondWithJSON(w, http.StatusCreated, projectToResponse(project))
}

// GetProject handles GET /api/v1/projects/{project_id}
func (h *ProjectHandler) GetProject(w http.ResponseWriter, r *http.Request) {
	user := checkauth.GetUserFromContext(r.Context())
	if user == nil {
		h.respondWithError(w, http.StatusUnauthorized, store.ErrUnauthorized)
		return
	}

	projectID := h.getID(r, "project_id")
	if projectID == "" {
		h.respondWithError(w, http.StatusBadRequest, store.ErrInvalidInput)
		return
	}

	project, err := h.store.GetProjectByID(r.Context(), projectID)
	if err != nil {
		h.respondWithError(w, http.StatusNotFound, err)
		return
	}

	h.respondWithJSON(w, http.StatusOK, projectToResponse(project))
}

// ListProjects handles GET /api/v1/projects
func (h *ProjectHandler) ListProjects(w http.ResponseWriter, r *http.Request) {
	user := checkauth.GetUserFromContext(r.Context())
	if user == nil {
		h.respondWithError(w, http.StatusUnauthorized, store.ErrUnauthorized)
		return
	}

	limit := 20
	offset := 0

	if l, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && l > 0 && l <= 100 {
		limit = l
	}
	if o, err := strconv.Atoi(r.URL.Query().Get("offset")); err == nil && o >= 0 {
		offset = o
	}

	projects, err := h.store.ListProjects(r.Context(), limit, offset)
	if err != nil {
		h.respondWithError(w, http.StatusInternalServerError, err)
		return
	}

	responses := make([]ProjectResponse, len(projects))
	for i := range projects {
		responses[i] = projectToResponse(&projects[i])
	}

	h.respondWithJSON(w, http.StatusOK, ListProjectsResponse{
		Projects: responses,
		Total:    len(responses),
		Limit:    limit,
		Offset:   offset,
	})
}

// UpdateProject handles PUT /api/v1/projects/{project_id}
func (h *ProjectHandler) UpdateProject(w http.ResponseWriter, r *http.Request) {
	user := checkauth.GetUserFromContext(r.Context())
	if user == nil {
		h.respondWithError(w, http.StatusUnauthorized, store.ErrUnauthorized)
		return
	}

	projectID := h.getID(r, "project_id")
	if projectID == "" {
		h.respondWithError(w, http.StatusBadRequest, store.ErrInvalidInput)
		return
	}

	project, err := h.store.GetProjectByID(r.Context(), projectID)
	if err != nil {
		h.respondWithError(w, http.StatusNotFound, err)
		return
	}

	var req UpdateProjectRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.respondWithError(w, http.StatusBadRequest, store.ErrInvalidInput)
		return
	}

	if req.Name != nil {
		project.Name = *req.Name
	}
	if req.Description != nil {
		project.Description = *req.Description
	}
	if req.RepoURL != nil {
		project.RepoURL = *req.RepoURL
	}
	if req.Enabled != nil {
		project.Enabled = *req.Enabled
	}
	if req.TargetBranches != nil {
		project.TargetBranches = req.TargetBranches
	}
	if req.AllowedEventTypes != nil {
		project.AllowedEventTypes = req.AllowedEventTypes
	}
	if req.DefaultCISourceType != nil {
		project.DefaultCISourceType = models.SourceType(*req.DefaultCISourceType)
	}
	if req.DefaultCISourceURL != nil {
		project.DefaultCISourceURL = *req.DefaultCISourceURL
	}
	if req.DefaultCISourceRef != nil {
		project.DefaultCISourceRef = *req.DefaultCISourceRef
	}
	if req.DefaultRunnerImage != nil {
		project.DefaultRunnerImage = *req.DefaultRunnerImage
	}
	if req.DefaultJobCommand != nil {
		project.DefaultJobCommand = *req.DefaultJobCommand
	}
	if req.DefaultTimeoutSeconds != nil {
		project.DefaultTimeoutSeconds = *req.DefaultTimeoutSeconds
	}
	if req.DefaultQueueName != nil {
		project.DefaultQueueName = *req.DefaultQueueName
	}
	if req.VCSTokenSecret != nil {
		project.VCSTokenSecret = *req.VCSTokenSecret
	}
	if req.WebhookSecret != nil {
		project.WebhookSecret = *req.WebhookSecret
	}

	if err := h.store.UpdateProject(r.Context(), project); err != nil {
		h.respondWithError(w, http.StatusInternalServerError, err)
		return
	}

	h.respondWithJSON(w, http.StatusOK, projectToResponse(project))
}

// DeleteProject handles DELETE /api/v1/projects/{project_id}
func (h *ProjectHandler) DeleteProject(w http.ResponseWriter, r *http.Request) {
	user := checkauth.GetUserFromContext(r.Context())
	if user == nil {
		h.respondWithError(w, http.StatusUnauthorized, store.ErrUnauthorized)
		return
	}

	projectID := h.getID(r, "project_id")
	if projectID == "" {
		h.respondWithError(w, http.StatusBadRequest, store.ErrInvalidInput)
		return
	}

	if err := h.store.DeleteProject(r.Context(), projectID); err != nil {
		h.respondWithError(w, http.StatusNotFound, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
