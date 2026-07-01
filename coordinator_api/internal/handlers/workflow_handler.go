package handlers

import (
	"context"
	"net/http"
	"strconv"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/checkauth"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

type workflowSummaryStore interface {
	ListWorkflowSummaries(ctx context.Context, filters map[string]interface{}, limit, offset int) ([]models.WorkflowSummary, error)
	GetWorkflowSummary(ctx context.Context, workflowID string) (*models.WorkflowSummary, error)
}

type WorkflowHandler struct {
	BaseHandler
	store store.Store
}

type ListWorkflowsResponse struct {
	Workflows []models.WorkflowSummary `json:"workflows"`
	Total     int                      `json:"total"`
	Limit     int                      `json:"limit"`
	Offset    int                      `json:"offset"`
}

func NewWorkflowHandler(store store.Store) *WorkflowHandler {
	return &WorkflowHandler{store: store}
}

func (h *WorkflowHandler) ListWorkflows(w http.ResponseWriter, r *http.Request) {
	user := checkauth.GetUserFromContext(r.Context())
	if user == nil {
		h.respondWithError(w, http.StatusUnauthorized, store.ErrUnauthorized)
		return
	}

	limit, offset := h.parsePagination(r)
	filters := h.parseWorkflowFilters(r, user)

	var summaries []models.WorkflowSummary
	if ws, ok := h.store.(workflowSummaryStore); ok {
		rows, err := ws.ListWorkflowSummaries(r.Context(), filters, limit, offset)
		if err != nil {
			h.respondWithError(w, http.StatusInternalServerError, err)
			return
		}
		summaries = rows
	} else {
		rows, err := h.looseJobSummaries(r.Context(), filters, limit, offset)
		if err != nil {
			h.respondWithError(w, http.StatusInternalServerError, err)
			return
		}
		summaries = rows
	}

	h.respondWithJSON(w, http.StatusOK, ListWorkflowsResponse{
		Workflows: summaries,
		Total:     len(summaries),
		Limit:     limit,
		Offset:    offset,
	})
}

func (h *WorkflowHandler) GetWorkflow(w http.ResponseWriter, r *http.Request) {
	user := checkauth.GetUserFromContext(r.Context())
	if user == nil {
		h.respondWithError(w, http.StatusUnauthorized, store.ErrUnauthorized)
		return
	}

	workflowID := h.getID(r, "workflow_id")
	if workflowID == "" {
		h.respondWithError(w, http.StatusBadRequest, store.ErrInvalidInput)
		return
	}
	if ws, ok := h.store.(workflowSummaryStore); ok {
		summary, err := ws.GetWorkflowSummary(r.Context(), workflowID)
		if err != nil {
			h.respondWithError(w, http.StatusNotFound, err)
			return
		}
		if !h.canUserAccessWorkflow(user, summary) {
			h.respondWithError(w, http.StatusForbidden, store.ErrForbidden)
			return
		}
		h.respondWithJSON(w, http.StatusOK, summary)
		return
	}

	job, err := h.store.GetJobByID(r.Context(), workflowID)
	if err != nil {
		h.respondWithError(w, http.StatusNotFound, err)
		return
	}
	if !h.canUserAccessJob(user, job) {
		h.respondWithError(w, http.StatusForbidden, store.ErrForbidden)
		return
	}
	summary := workflowSummaryFromLooseJob(job)
	h.respondWithJSON(w, http.StatusOK, summary)
}

func (h *WorkflowHandler) parsePagination(r *http.Request) (limit, offset int) {
	limit = 20
	offset = 0
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

func (h *WorkflowHandler) parseWorkflowFilters(r *http.Request, user *models.User) map[string]interface{} {
	filters := make(map[string]interface{})
	if !h.isAdmin(user) {
		filters["user_id"] = user.UserID
	} else if userID := r.URL.Query().Get("user_id"); userID != "" {
		filters["user_id"] = userID
	}
	if status := r.URL.Query().Get("status"); status != "" {
		filters["status"] = status
	}
	if projectID := r.URL.Query().Get("project_id"); projectID != "" {
		filters["project_id"] = projectID
	}
	return filters
}

func (h *WorkflowHandler) looseJobSummaries(ctx context.Context, filters map[string]interface{}, limit, offset int) ([]models.WorkflowSummary, error) {
	jobs, err := h.store.ListJobs(ctx, filters, limit, offset)
	if err != nil {
		return nil, err
	}
	out := make([]models.WorkflowSummary, 0, len(jobs))
	for i := range jobs {
		if jobs[i].WorkflowID != nil {
			continue
		}
		out = append(out, workflowSummaryFromLooseJob(&jobs[i]))
	}
	return out, nil
}

func workflowSummaryFromLooseJob(job *models.Job) models.WorkflowSummary {
	running := 0
	completed := 0
	failed := 0
	switch job.Status {
	case "running":
		running = 1
	case "completed":
		completed = 1
	case "failed", "timeout", "cancelled":
		failed = 1
	}
	return models.WorkflowSummary{
		WorkflowID:      job.JobID,
		Kind:            "job",
		Name:            job.Name,
		Status:          job.Status,
		UserID:          job.UserID,
		ProjectID:       job.ProjectID,
		CreatedAt:       job.CreatedAt,
		UpdatedAt:       job.UpdatedAt,
		CompletedAt:     job.CompletedAt,
		QueueName:       job.QueueName,
		JobCount:        1,
		RunningCount:    running,
		CompletedCount:  completed,
		FailedCount:     failed,
		LooseJobID:      &job.JobID,
		LooseJobExit:    job.ExitCode,
		DecisionSummary: job.LastError,
	}
}

func (h *WorkflowHandler) canUserAccessWorkflow(user *models.User, summary *models.WorkflowSummary) bool {
	if h.isAdmin(user) {
		return true
	}
	return summary.UserID == user.UserID
}

func (h *WorkflowHandler) canUserAccessJob(user *models.User, job *models.Job) bool {
	if h.isAdmin(user) {
		return true
	}
	return job.UserID == user.UserID
}

func (h *WorkflowHandler) isAdmin(user *models.User) bool {
	for _, role := range user.Roles {
		if role == "admin" || role == "system_admin" {
			return true
		}
	}
	return false
}
