package handlers

import (
	"context"
	"encoding/json"
	"errors"
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

type projectSecretGrantStore interface {
	CreateSecretGrant(ctx context.Context, grant *models.SecretGrant) error
	ListSecretGrants(ctx context.Context, userID string, projectID *string) ([]models.SecretGrant, error)
	GetSecretGrant(ctx context.Context, userID string, projectID *string, ref string) (*models.SecretGrant, error)
	UpdateSecretGrant(ctx context.Context, grant *models.SecretGrant) error
	DeleteSecretGrant(ctx context.Context, userID string, projectID *string, ref string) error
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

	VCSTokenSecret       string            `json:"vcs_token_secret,omitempty"`
	VCSCredentialSecrets map[string]string `json:"vcs_token_secrets,omitempty"`
	WebhookSecret        string            `json:"webhook_secret,omitempty"`
	WebhookSecrets       map[string]string `json:"webhook_secrets,omitempty"`
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

	VCSTokenSecret       *string           `json:"vcs_token_secret,omitempty"`
	VCSCredentialSecrets map[string]string `json:"vcs_token_secrets,omitempty"`
	WebhookSecret        *string           `json:"webhook_secret,omitempty"`
	WebhookSecrets       map[string]string `json:"webhook_secrets,omitempty"`
}

// ProjectResponse represents the response body for a project
type ProjectResponse struct {
	ProjectID   string    `json:"project_id"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
	UserID      *string   `json:"user_id,omitempty"`
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

	VCSTokenSecret       string            `json:"vcs_token_secret,omitempty"`
	VCSCredentialSecrets map[string]string `json:"vcs_token_secrets,omitempty"`
	WebhookSecret        string            `json:"webhook_secret,omitempty"`
	WebhookSecrets       map[string]string `json:"webhook_secrets,omitempty"`
}

// ListProjectsResponse represents the response body for listing projects
type ListProjectsResponse struct {
	Projects []ProjectResponse `json:"projects"`
	Total    int               `json:"total"`
	Limit    int               `json:"limit"`
	Offset   int               `json:"offset"`
}

type SecretGrantRequest struct {
	Name              string `json:"name,omitempty"`
	ProjectID         string `json:"project_id,omitempty"`
	Project           string `json:"project,omitempty"`
	SecretPathMatch   string `json:"secret_path_match,omitempty"`
	SecretPathPattern string `json:"secret_path_pattern,omitempty"`
	SecretPathPrefix  string `json:"secret_path_prefix,omitempty"`
	JobNameMatch      string `json:"job_name_match,omitempty"`
	JobNamePattern    string `json:"job_name_pattern,omitempty"`
	JobName           string `json:"job_name,omitempty"`
	Description       string `json:"description,omitempty"`
	State             string `json:"state,omitempty"`
}

type ListSecretGrantsResponse struct {
	Grants []models.SecretGrant `json:"grants"`
	Total  int                  `json:"total"`
}

type SecretGrantApplyRequest struct {
	DryRun bool                 `json:"dry_run,omitempty"`
	Prune  bool                 `json:"prune,omitempty"`
	Grants []SecretGrantRequest `json:"grants"`
}

type SecretGrantApplyResponse struct {
	DryRun    bool                 `json:"dry_run"`
	Created   []models.SecretGrant `json:"created,omitempty"`
	Updated   []models.SecretGrant `json:"updated,omitempty"`
	Deleted   []models.SecretGrant `json:"deleted,omitempty"`
	Unchanged []models.SecretGrant `json:"unchanged,omitempty"`
}

func projectToResponse(p *models.Project) ProjectResponse {
	return ProjectResponse{
		ProjectID:             p.ProjectID,
		CreatedAt:             p.CreatedAt,
		UpdatedAt:             p.UpdatedAt,
		UserID:                p.UserID,
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
		VCSCredentialSecrets:  jsonbStringMap(p.VCSCredentialSecrets),
		WebhookSecret:         p.WebhookSecret,
		WebhookSecrets:        jsonbStringMap(p.WebhookSecrets),
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
		UserID:      &user.UserID,
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
	if req.VCSCredentialSecrets != nil {
		project.VCSCredentialSecrets = stringMapJSONB(req.VCSCredentialSecrets)
	}
	if req.WebhookSecret != "" {
		project.WebhookSecret = req.WebhookSecret
	}
	if req.WebhookSecrets != nil {
		project.WebhookSecrets = stringMapJSONB(req.WebhookSecrets)
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
	if req.VCSCredentialSecrets != nil {
		project.VCSCredentialSecrets = stringMapJSONB(req.VCSCredentialSecrets)
	}
	if req.WebhookSecret != nil {
		project.WebhookSecret = *req.WebhookSecret
	}
	if req.WebhookSecrets != nil {
		project.WebhookSecrets = stringMapJSONB(req.WebhookSecrets)
	}

	if err := h.store.UpdateProject(r.Context(), project); err != nil {
		h.respondWithError(w, http.StatusInternalServerError, err)
		return
	}

	h.respondWithJSON(w, http.StatusOK, projectToResponse(project))
}

func stringMapJSONB(values map[string]string) models.JSONB {
	result := models.JSONB{}
	for k, v := range values {
		result[k] = v
	}
	return result
}

func jsonbStringMap(values models.JSONB) map[string]string {
	if values == nil {
		return nil
	}
	result := map[string]string{}
	for k, v := range values {
		if s, ok := v.(string); ok {
			result[k] = s
		}
	}
	if len(result) == 0 {
		return nil
	}
	return result
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

func (h *ProjectHandler) ListSecretGrants(w http.ResponseWriter, r *http.Request) {
	user := checkauth.GetUserFromContext(r.Context())
	if user == nil {
		h.respondWithError(w, http.StatusUnauthorized, store.ErrUnauthorized)
		return
	}
	grantStore, ok := h.store.(projectSecretGrantStore)
	if !ok {
		h.respondWithError(w, http.StatusNotImplemented, errors.New("secret grant store not available"))
		return
	}
	project, ownerID, ok := h.projectAndOwner(w, r, user.UserID)
	if !ok {
		return
	}
	grants, err := grantStore.ListSecretGrants(r.Context(), ownerID, &project.ProjectID)
	if err != nil {
		h.respondWithError(w, http.StatusInternalServerError, err)
		return
	}
	h.respondWithJSON(w, http.StatusOK, ListSecretGrantsResponse{
		Grants: grants,
		Total:  len(grants),
	})
}

func (h *ProjectHandler) CreateSecretGrant(w http.ResponseWriter, r *http.Request) {
	h.createSecretGrantWithScope(w, r, true)
}

func (h *ProjectHandler) GetSecretGrant(w http.ResponseWriter, r *http.Request) {
	user := checkauth.GetUserFromContext(r.Context())
	if user == nil {
		h.respondWithError(w, http.StatusUnauthorized, store.ErrUnauthorized)
		return
	}
	grantStore, ok := h.store.(projectSecretGrantStore)
	if !ok {
		h.respondWithError(w, http.StatusNotImplemented, errors.New("secret grant store not available"))
		return
	}
	project, ownerID, ok := h.projectAndOwner(w, r, user.UserID)
	if !ok {
		return
	}
	ref := h.getID(r, "grant_id")
	if ref == "" {
		h.respondWithError(w, http.StatusBadRequest, store.ErrInvalidInput)
		return
	}
	grant, err := grantStore.GetSecretGrant(r.Context(), ownerID, &project.ProjectID, ref)
	if err != nil {
		h.respondWithError(w, http.StatusNotFound, err)
		return
	}
	h.respondWithJSON(w, http.StatusOK, grant)
}

func (h *ProjectHandler) UpdateSecretGrant(w http.ResponseWriter, r *http.Request) {
	user := checkauth.GetUserFromContext(r.Context())
	if user == nil {
		h.respondWithError(w, http.StatusUnauthorized, store.ErrUnauthorized)
		return
	}
	grantStore, ok := h.store.(projectSecretGrantStore)
	if !ok {
		h.respondWithError(w, http.StatusNotImplemented, errors.New("secret grant store not available"))
		return
	}
	project, ownerID, ok := h.projectAndOwner(w, r, user.UserID)
	if !ok {
		return
	}
	ref := h.getID(r, "grant_id")
	if ref == "" {
		h.respondWithError(w, http.StatusBadRequest, store.ErrInvalidInput)
		return
	}
	var req SecretGrantRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.respondWithError(w, http.StatusBadRequest, store.ErrInvalidInput)
		return
	}
	grant, err := grantStore.GetSecretGrant(r.Context(), ownerID, &project.ProjectID, ref)
	if err != nil {
		h.respondWithError(w, http.StatusNotFound, err)
		return
	}
	if err := applySecretGrantRequest(grant, req); err != nil {
		h.respondWithError(w, http.StatusBadRequest, err)
		return
	}
	if err := grantStore.UpdateSecretGrant(r.Context(), grant); err != nil {
		h.respondWithError(w, http.StatusInternalServerError, err)
		return
	}
	h.respondWithJSON(w, http.StatusOK, grant)
}

func (h *ProjectHandler) DeleteSecretGrant(w http.ResponseWriter, r *http.Request) {
	user := checkauth.GetUserFromContext(r.Context())
	if user == nil {
		h.respondWithError(w, http.StatusUnauthorized, store.ErrUnauthorized)
		return
	}
	grantStore, ok := h.store.(projectSecretGrantStore)
	if !ok {
		h.respondWithError(w, http.StatusNotImplemented, errors.New("secret grant store not available"))
		return
	}
	project, ownerID, ok := h.projectAndOwner(w, r, user.UserID)
	if !ok {
		return
	}
	grantID := h.getID(r, "grant_id")
	if grantID == "" {
		h.respondWithError(w, http.StatusBadRequest, store.ErrInvalidInput)
		return
	}
	if err := grantStore.DeleteSecretGrant(r.Context(), ownerID, &project.ProjectID, grantID); err != nil {
		h.respondWithError(w, http.StatusNotFound, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *ProjectHandler) projectAndOwner(w http.ResponseWriter, r *http.Request, fallbackUserID string) (*models.Project, string, bool) {
	projectID := h.getID(r, "project_id")
	if projectID == "" {
		h.respondWithError(w, http.StatusBadRequest, store.ErrInvalidInput)
		return nil, "", false
	}
	project, err := h.store.GetProjectByID(r.Context(), projectID)
	if err != nil {
		h.respondWithError(w, http.StatusNotFound, err)
		return nil, "", false
	}
	ownerID := fallbackUserID
	if project.UserID != nil && *project.UserID != "" {
		ownerID = *project.UserID
	}
	return project, ownerID, true
}
